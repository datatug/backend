package facade4datatug

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
)

var (
	ErrSharedProjectUnavailable  = errors.New("shared project creation is unavailable")
	ErrSharedProjectUnauthorized = errors.New("shared project creation is unauthorized")
	ErrSharedProjectConflict     = errors.New("shared project create command conflicts")
	ErrSharedProjectInvalid      = errors.New("invalid shared project create command")
)

type SharedProjectCreateCommand struct {
	ActorID, SpaceID, CommandID, Title string
}

func (c SharedProjectCreateCommand) Validate() error {
	if c.ActorID == "" || len(c.ActorID) > 128 || c.ActorID != strings.TrimSpace(c.ActorID) || !utf8.ValidString(c.ActorID) {
		return ErrSharedProjectInvalid
	}
	for _, r := range c.ActorID {
		if unicode.IsControl(r) {
			return ErrSharedProjectInvalid
		}
	}
	for _, id := range []string{c.SpaceID, c.CommandID} {
		if err := models4datatug.ValidateSharedProjectIdentifier(id); err != nil {
			return fmt.Errorf("%w: %v", ErrSharedProjectInvalid, err)
		}
	}
	if err := models4datatug.ValidateSharedProjectTitle(c.Title); err != nil {
		return fmt.Errorf("%w: %v", ErrSharedProjectInvalid, err)
	}
	return nil
}

// SharedProjectService is deliberately separate from the private-project
// Facade, so existing callers do not acquire shared creation implicitly.
type SharedProjectService struct {
	db        dal.DB
	ids       IDGenerator
	authority SharedProjectCreateAuthority
	now       func() time.Time
	paid      *PaidSharedProjectOptions
}

func sharedProjectPortAbsent(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	default:
		return false
	}
}

func NewSharedProjectService(db dal.DB, ids IDGenerator, authority SharedProjectCreateAuthority, now func() time.Time) (*SharedProjectService, error) {
	if sharedProjectPortAbsent(db) || sharedProjectPortAbsent(ids) || sharedProjectPortAbsent(authority) || now == nil {
		return nil, ErrSharedProjectUnavailable
	}
	return &SharedProjectService{db: db, ids: ids, authority: authority, now: now}, nil
}

func (s *SharedProjectService) Create(ctx context.Context, command SharedProjectCreateCommand) (models4datatug.SharedProjectRef, error) {
	var result models4datatug.SharedProjectRef
	if s == nil || sharedProjectPortAbsent(s.db) || sharedProjectPortAbsent(s.ids) || sharedProjectPortAbsent(s.authority) || s.now == nil {
		return result, ErrSharedProjectUnavailable
	}
	if err := command.Validate(); err != nil {
		return result, err
	}
	binding := SharedProjectCreateBinding{
		ActorID: command.ActorID, SpaceID: command.SpaceID, CommandID: command.CommandID,
		RequestDigest: models4datatug.SharedProjectCreateDigest(command.ActorID, command.SpaceID, command.CommandID, command.Title),
	}
	if s.paid != nil {
		account, err := ResolvePersonalPayer(ctx, command.ActorID, "", s.paid.Directory)
		if err != nil {
			return result, ErrSharedProjectUnauthorized
		}
		if models4datatug.ValidateSharedProjectIdentifier(account.ID) != nil {
			return result, ErrSharedProjectUnauthorized
		}
		binding.PayerID, binding.Mode, binding.Product = account.ID, s.paid.Mode, s.paid.Product
		binding.RequestDigest = models4datatug.PaidSharedProjectCreateDigest(command.ActorID, command.SpaceID, command.CommandID, command.Title, binding.PayerID, binding.Mode, binding.Product)
	}
	prepared, err := s.authority.PrepareSharedProjectCreate(ctx, binding)
	if err != nil {
		return result, fmt.Errorf("%w: %w", ErrSharedProjectUnauthorized, err)
	}
	if prepared.Binding != binding || prepared.IssuedAt.IsZero() || sharedProjectPortAbsent(prepared.Validator) {
		return result, ErrSharedProjectUnauthorized
	}
	// Preparation may commit a Core reservation. Capture time only afterwards,
	// then keep the same observation and entropy through DAL transaction retries.
	observedAt := s.now().UTC()
	if observedAt.IsZero() || observedAt.Before(prepared.IssuedAt) {
		return result, ErrSharedProjectUnauthorized
	}
	projectID, idErr := s.ids.NewID(ctx)
	if idErr == nil {
		if err := models4datatug.ValidateSharedProjectIdentifier(projectID); err != nil {
			idErr = fmt.Errorf("%w: invalid generated project ID", ErrSharedProjectUnavailable)
		}
	}
	err = s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		if err := prepared.Validator.ValidateSharedProjectCreateInTransaction(txCtx, tx, binding, observedAt); err != nil {
			return fmt.Errorf("%w: %w", ErrSharedProjectUnauthorized, err)
		}
		var admission *projectAdmissionState
		if s.paid != nil {
			var err error
			admission, err = s.readPaidAdmission(txCtx, tx, binding, observedAt)
			if err != nil {
				return err
			}
		}
		receiptRecord, receipt := models4datatug.NewSharedProjectCreateReceiptRecord(command.SpaceID, command.CommandID)
		if err := tx.Get(txCtx, receiptRecord); err != nil && !record.IsNotFound(err) {
			return err
		}
		if receiptRecord.Exists() {
			if err := receipt.Validate(); err != nil {
				return fmt.Errorf("%w: stored receipt is invalid", ErrSharedProjectConflict)
			}
			if receipt.SpaceID != command.SpaceID || receipt.CommandID != command.CommandID || receipt.ActorID != command.ActorID || receipt.RequestDigest != binding.RequestDigest {
				return ErrSharedProjectConflict
			}
			if receipt.PayerID != binding.PayerID || receipt.Mode != binding.Mode || receipt.Product != binding.Product {
				return ErrSharedProjectConflict
			}
			if admission != nil {
				if err := admission.verifyReplay(txCtx, tx, binding, receipt.ProjectID); err != nil {
					return err
				}
			}
			result = models4datatug.SharedProjectRef{StoreID: models4datatug.FirestoreStoreID, SpaceID: receipt.SpaceID, ProjectID: receipt.ProjectID}
			return nil
		}
		// Committed replay needs only current authority and the durable receipt,
		// so a temporary entropy failure must not prevent response-loss recovery.
		if idErr != nil {
			return fmt.Errorf("generate shared project ID: %w", idErr)
		}
		if admission != nil {
			if err := admission.readNewAllocation(txCtx, tx, binding, projectID); err != nil {
				return err
			}
		}
		// All authority and command reads precede the first write. No user index
		// or Space/module record is mutated by this domain command.
		projectRecord, project := models4datatug.NewSharedProjectRecord(command.SpaceID, projectID)
		project.Title = command.Title
		project.Access = models4datatug.AccessProtected
		project.Created = &models4datatug.Created{At: observedAt}
		*receipt = models4datatug.SharedProjectCreateReceipt{
			Version: 1, ActorID: command.ActorID, SpaceID: command.SpaceID, CommandID: command.CommandID,
			Title: command.Title, RequestDigest: binding.RequestDigest, ProjectID: projectID, CreatedAt: observedAt,
			PayerID: binding.PayerID, Mode: binding.Mode, Product: binding.Product,
		}
		if err := tx.Insert(txCtx, projectRecord); err != nil {
			return fmt.Errorf("insert shared project: %w", err)
		}
		if err := tx.Insert(txCtx, receiptRecord); err != nil {
			return fmt.Errorf("insert shared project create receipt: %w", err)
		}
		if admission != nil {
			if err := admission.writeAllocation(txCtx, tx, binding, projectID, observedAt); err != nil {
				return err
			}
		}
		result = models4datatug.SharedProjectRef{StoreID: models4datatug.FirestoreStoreID, SpaceID: command.SpaceID, ProjectID: projectID}
		return nil
	})
	if err != nil {
		return models4datatug.SharedProjectRef{}, err
	}
	return result, nil
}
