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
	"github.com/sneat-co/sneat-go-core/facade"
)

var (
	ErrSharedProjectUnavailable  = errors.New("shared project creation is unavailable")
	ErrSharedProjectUnauthorized = errors.New("shared project creation is unauthorized")
	ErrSharedProjectConflict     = errors.New("shared project create command conflicts")
	ErrSharedProjectInvalid      = errors.New("invalid shared project create command")
)

type SharedProjectCreateCommand struct {
	ActorID, SpaceID, CommandID, Title string
	BillingIntent                      SharedProjectBillingIntent
}

// SharedProjectBillingIntent is only a user's plan choice. It grants no
// access: the service still verifies current Pro or Business authority.
type SharedProjectBillingIntent string

const (
	BillingIntentPersonalPro   SharedProjectBillingIntent = "personal_pro"
	BillingIntentSpaceBusiness SharedProjectBillingIntent = "space_business"
)

func (i SharedProjectBillingIntent) Validate() error {
	if i != "" && i != BillingIntentPersonalPro && i != BillingIntentSpaceBusiness {
		return ErrSharedProjectInvalid
	}
	return nil
}

func (c SharedProjectCreateCommand) Validate() error {
	if c.BillingIntent.Validate() != nil {
		return ErrSharedProjectInvalid
	}
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
	db                        dal.DB
	ids                       IDGenerator
	authority                 SharedProjectCreateAuthority
	now                       func() time.Time
	paid                      *PaidSharedProjectOptions
	business                  *BusinessProjectAccessVerifier
	ownerLinks                *sharedProjectOwnerLinks
	activation                *sharedProjectActivation
	recordQueryEditCandidates bool
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
	if s == nil || sharedProjectPortAbsent(s.db) || sharedProjectPortAbsent(s.ids) || (s.activation == nil && sharedProjectPortAbsent(s.authority)) || s.now == nil {
		return result, ErrSharedProjectUnavailable
	}
	if err := command.Validate(); err != nil {
		return result, err
	}
	binding, err := s.resolveSharedProjectCreateBinding(ctx, command.ActorID, command.SpaceID, command.CommandID, command.Title, command.BillingIntent)
	if err != nil {
		return result, err
	}
	if s.activation != nil && !isBusinessProjectBinding(binding) {
		userCtx, ok := ctx.(facade.ContextWithUser)
		if !ok || sharedProjectPortAbsent(userCtx) {
			return result, ErrSharedProjectUnauthorized
		}
		if err := s.EnsureProtectedProjectQuotaForCreate(userCtx, binding); err != nil {
			return result, err
		}
	}
	// Existing-capability flows prepare their Core reservation before this
	// transaction. The activating flow captures time here and plans Core's
	// module change only after all domain reads in this same transaction.
	prepared, observedAt, userCtx, err := s.prepareCreateAuthority(ctx, binding)
	if err != nil {
		return result, err
	}
	projectID, idErr := s.ids.NewID(ctx)
	if idErr == nil {
		if err := models4datatug.ValidateSharedProjectIdentifier(projectID); err != nil {
			idErr = fmt.Errorf("%w: invalid generated project ID", ErrSharedProjectUnavailable)
		}
	}
	err = s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		if err := s.validatePreparedCreateInTransaction(txCtx, tx, binding, prepared, observedAt); err != nil {
			return err
		}
		var admission *projectAdmissionState
		var businessAccess *SpaceServiceAccess
		if s.paid != nil && !isBusinessProjectBinding(binding) {
			var err error
			// Recheck paid-through against the actual time of every transaction
			// attempt. Command entropy and audit time remain stable through retry.
			paidAt := s.now().UTC()
			if paidAt.IsZero() || paidAt.Before(observedAt) {
				return ErrSharedProjectUnauthorized
			}
			admission, err = s.readPaidAdmission(txCtx, tx, binding, paidAt)
			if err != nil {
				return err
			}
		} else if s.business != nil {
			paidAt := s.now().UTC()
			if paidAt.IsZero() || paidAt.Before(observedAt) {
				return ErrSharedProjectUnauthorized
			}
			access, err := s.business.ReadCurrent(txCtx, tx, command.SpaceID)
			if err != nil {
				return err
			}
			if access.Mode != binding.Mode {
				return ErrSharedProjectUnauthorized
			}
			businessAccess = &access
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
				if err := s.verifyProjectOwnerReplay(txCtx, tx, binding, receipt.ProjectID, receipt.OwnerContact); err != nil {
					return err
				}
			} else if businessAccess != nil {
				if err := verifyBusinessProjectAdmissionReplay(txCtx, tx, binding, receipt.ProjectID); err != nil {
					return err
				}
				if err := s.verifyProjectOwnerReplay(txCtx, tx, binding, receipt.ProjectID, receipt.OwnerContact); err != nil {
					return err
				}
			}
			if err := s.applyExplicitCreateActivation(userCtx, txCtx, tx, binding, observedAt); err != nil {
				return err
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
		} else if businessAccess != nil {
			if err := readNewBusinessProjectAllocation(txCtx, tx, binding, projectID); err != nil {
				return err
			}
		}
		var ownerPlan *projectOwnerLinkPlan
		if admission != nil || businessAccess != nil {
			var err error
			ownerPlan, err = s.prepareProjectOwnerLink(txCtx, tx, binding, projectID, observedAt)
			if err != nil {
				return err
			}
		}
		if businessAccess != nil {
			if err := verifyBusinessProjectAccessAt(*businessAccess, s.now().UTC()); err != nil {
				return err
			}
		}
		if err := s.applyExplicitCreateActivation(userCtx, txCtx, tx, binding, observedAt); err != nil {
			return err
		}
		// All authority and command reads precede writes. For the activating
		// constructor, Core's module update is applied first in this transaction.
		projectRecord, project := models4datatug.NewSharedProjectRecord(command.SpaceID, projectID)
		if ownerPlan != nil {
			var linked *models4datatug.SharedLinkedProject
			projectRecord, linked = models4datatug.NewSharedLinkedProjectRecord(command.SpaceID, projectID)
			project = &linked.Project
			linked.WithRelatedAndIDs = ownerPlan.graph
		}
		project.Title = command.Title
		project.Access = models4datatug.AccessProtected
		project.Created = &models4datatug.Created{At: observedAt}
		*receipt = models4datatug.SharedProjectCreateReceipt{
			Version: 1, ActorID: command.ActorID, SpaceID: command.SpaceID, CommandID: command.CommandID,
			Title: command.Title, RequestDigest: binding.RequestDigest, ProjectID: projectID, CreatedAt: observedAt,
			PayerID: binding.PayerID, Mode: binding.Mode, Product: binding.Product,
		}
		if ownerPlan != nil {
			receipt.OwnerContact = ownerPlan.proof
		}
		if err := tx.Insert(txCtx, projectRecord); err != nil {
			return fmt.Errorf("insert shared project: %w", err)
		}
		if err := tx.Insert(txCtx, receiptRecord); err != nil {
			return fmt.Errorf("insert shared project create receipt: %w", err)
		}
		if admission != nil {
			if err := admission.writeAllocation(txCtx, tx, binding, projectID, observedAt, ownerPlan.proof); err != nil {
				return err
			}
		} else if businessAccess != nil {
			if err := writeBusinessProjectAdmission(txCtx, tx, binding, projectID, observedAt, ownerPlan.proof, *businessAccess); err != nil {
				return err
			}
		}
		if ownerPlan != nil {
			if err := ownerPlan.writeContact(txCtx, tx); err != nil {
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
