// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-core-modules/spaceus/contract4spaceus"
	"github.com/sneat-co/sneat-go-core/coretypes"
	"github.com/sneat-co/sneat-go-core/facade"
)

var ErrSharedProjectActivationUnavailable = errors.New("shared project activation is unavailable")

// SharedProjectActivationOptions are trusted host composition. The Core
// activator never runs for passive reads or precommitted reservations.
type SharedProjectActivationOptions struct {
	Activator      contract4spaceus.TransactionalRoleCapabilityActivator
	RequiredRoles  []string
	InventoryQuery dal.QueryExecutor
}

type sharedProjectActivation struct {
	activator     contract4spaceus.TransactionalRoleCapabilityActivator
	requiredRoles []string
	inventory     *DALProtectedProjectInventory
}

// NewActivatingPaidSharedProjectService is the explicit-create composition.
// It has no preflight SharedProjectCreateAuthority because a new Space may not
// yet have the DataTug capability. Its Create methods seed a complete paid
// quota basis before mutation, then plan/apply capability activation in the
// same transaction as project admission.
func NewActivatingPaidSharedProjectService(
	db dal.DB,
	ids IDGenerator,
	now func() time.Time,
	paid PaidSharedProjectOptions,
	activation SharedProjectActivationOptions,
) (*SharedProjectService, error) {
	if sharedProjectPortAbsent(db) || sharedProjectPortAbsent(ids) || now == nil || paid.validate() != nil ||
		sharedProjectPortAbsent(activation.Activator) || sharedProjectPortAbsent(activation.InventoryQuery) || len(activation.RequiredRoles) == 0 || len(activation.RequiredRoles) > 10 {
		return nil, ErrSharedProjectActivationUnavailable
	}
	activationSnapshot, err := snapshotSharedProjectActivation(activation, true)
	if err != nil {
		return nil, err
	}
	ownerLinks, err := snapshotProjectOwnerLinks(paid.ContactLinks)
	if err != nil {
		return nil, ErrSharedProjectActivationUnavailable
	}
	paidSnapshot := snapshotPaidSharedProjectOptions(paid)
	return &SharedProjectService{
		db: db, ids: ids, now: now, paid: &paidSnapshot, ownerLinks: ownerLinks,
		activation: activationSnapshot,
	}, nil
}

func snapshotSharedProjectActivation(options SharedProjectActivationOptions, requireInventory bool) (*sharedProjectActivation, error) {
	if sharedProjectPortAbsent(options.Activator) || len(options.RequiredRoles) == 0 || len(options.RequiredRoles) > 10 || (requireInventory && sharedProjectPortAbsent(options.InventoryQuery)) {
		return nil, ErrSharedProjectActivationUnavailable
	}
	var inventory *DALProtectedProjectInventory
	if !sharedProjectPortAbsent(options.InventoryQuery) {
		var err error
		inventory, err = NewDALProtectedProjectInventory(options.InventoryQuery)
		if err != nil {
			return nil, ErrSharedProjectActivationUnavailable
		}
	}
	roles := append([]string(nil), options.RequiredRoles...)
	for index, role := range roles {
		if strings.TrimSpace(role) == "" || role != strings.TrimSpace(role) {
			return nil, ErrSharedProjectActivationUnavailable
		}
		for previous := 0; previous < index; previous++ {
			if roles[previous] == role {
				return nil, ErrSharedProjectActivationUnavailable
			}
		}
	}
	sort.Strings(roles)
	return &sharedProjectActivation{activator: options.Activator, requiredRoles: roles, inventory: inventory}, nil
}

// EnsureProtectedProjectQuotaForCreate is a command-bound explicit-submit
// preflight. It requires current paid, owner-contact and Core Space authority
// before scanning or seeding a missing quota. All protected allocation writers
// require an existing quota in their own transaction, so absence fences
// target-payer writes while the complete scan runs. The seed transaction
// repeats the command authority checks and continued quota absence before
// inserting the basis.
func (s *SharedProjectService) EnsureProtectedProjectQuotaForCreate(ctx facade.ContextWithUser, binding SharedProjectCreateBinding) error {
	if s == nil || s.activation == nil || s.paid == nil || sharedProjectPortAbsent(s.db) || s.now == nil || sharedProjectPortAbsent(ctx) || sharedProjectPortAbsent(ctx.User()) {
		return ErrSharedProjectActivationUnavailable
	}
	actor := ctx.User().GetUserID()
	if actor == "" || actor != binding.ActorID || models4datatug.ValidateSharedProjectIdentifier(binding.SpaceID) != nil || models4datatug.ValidateSharedProjectIdentifier(binding.CommandID) != nil || models4datatug.ValidateSharedProjectIdentifier(binding.PayerID) != nil || binding.Mode != s.paid.Mode || binding.Product != s.paid.Product || binding.Mode != "live" || binding.Product != "datatug" {
		return ErrSharedProjectUnauthorized
	}
	requestDigest, err := hex.DecodeString(binding.RequestDigest)
	if err != nil || len(requestDigest) != sha256.Size || binding.RequestDigest != strings.ToLower(binding.RequestDigest) {
		return ErrSharedProjectUnauthorized
	}
	account, err := ResolvePersonalPayer(ctx, actor, "", s.paid.Directory)
	if err != nil || models4datatug.ValidateSharedProjectIdentifier(account.ID) != nil || account.ID != binding.PayerID {
		return ErrSharedProjectUnauthorized
	}
	at := s.now().UTC()
	if at.IsZero() {
		return ErrSharedProjectUnauthorized
	}
	var quotaMissing bool
	// The Core contract accepts a readwrite transaction so it can bind its
	// opaque plan to the exact snapshot. Planning is read-only; Apply is not
	// called here.
	err = s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		if _, err := readCurrentPaidProjectAccess(txCtx, tx, *s.paid, actor, account.ID, at); err != nil {
			return err
		}
		if _, _, _, err := s.readCurrentProjectOwnerContact(txCtx, tx, binding); err != nil {
			return err
		}
		if _, err := s.PlanExplicitProjectCreateActivationInTransaction(ctx, tx, binding, at); err != nil {
			return err
		}
		quotaRecord, quota := models4datatug.NewProtectedProjectQuotaRecord(s.paid.Mode, s.paid.Product, account.ID)
		quotaMissing = false
		if err := tx.Get(txCtx, quotaRecord); err != nil {
			if record.IsNotFound(err) {
				quotaMissing = true
			} else {
				return ErrProtectedProjectQuota
			}
		}
		if !quotaMissing && (quota.Validate() != nil || quota.Mode != s.paid.Mode || quota.Product != s.paid.Product || quota.PayerID != account.ID) {
			return ErrProtectedProjectQuota
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !quotaMissing {
		return nil
	}
	basis, err := s.activation.inventory.CompleteProtectedProjectBasis(ctx, s.paid.Mode, s.paid.Product, account.ID)
	if err != nil || basis.Digest == "" || basis.Allocated < 0 {
		return ErrProtectedProjectInventory
	}
	authority := activationProtectedProjectBasisAuthority{service: s, user: ctx, binding: binding, at: at}
	if err := InitializeProtectedProjectQuota(ctx, s.db, *s.paid, actor, account.ID, basis, authority); err != nil {
		if !errors.Is(err, ErrSharedProjectConflict) && !record.IsAlreadyExists(err) {
			return err
		}
		return s.verifyInitializedQuota(ctx, account.ID, basis)
	}
	return nil
}
func (s *SharedProjectService) verifyInitializedQuota(ctx context.Context, payer string, basis InitialProtectedProjectBasis) error {
	return s.db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		r, quota := models4datatug.NewProtectedProjectQuotaRecord(s.paid.Mode, s.paid.Product, payer)
		if err := tx.Get(txCtx, r); err != nil || quota.Validate() != nil || quota.Mode != s.paid.Mode || quota.Product != s.paid.Product || quota.PayerID != payer || quota.BasisDigest != basis.Digest || quota.Allocated < basis.Allocated {
			return ErrProtectedProjectQuota
		}
		return nil
	})
}

// PlanExplicitProjectCreateActivationInTransaction binds Core's activation
// plan to the verified caller and immutable paid create command. The caller
// must run all of its authorization, quota, receipt, repository, and owner
// contact checks before calling Apply on the returned value in this same DAL
// transaction, immediately before its first write.
func (s *SharedProjectService) PlanExplicitProjectCreateActivationInTransaction(
	ctx facade.ContextWithUser,
	tx dal.ReadwriteTransaction,
	binding SharedProjectCreateBinding,
	observedAt time.Time,
) (contract4spaceus.PreparedRoleCapabilityActivation, error) {
	var empty contract4spaceus.PreparedRoleCapabilityActivation
	if s == nil || s.activation == nil || (s.paid == nil && s.business == nil) || sharedProjectPortAbsent(ctx) || sharedProjectPortAbsent(ctx.User()) || sharedProjectPortAbsent(tx) || observedAt.IsZero() {
		return empty, ErrSharedProjectActivationUnavailable
	}
	validBinding := s.paid != nil && binding.Mode == s.paid.Mode && binding.Product == s.paid.Product && binding.Mode == "live" && binding.Product == "datatug" && models4datatug.ValidateSharedProjectIdentifier(binding.PayerID) == nil
	if s.business != nil {
		validBinding = binding.Mode == "live" && binding.Product == BusinessProjectProductID && binding.PayerID == binding.SpaceID
	}
	if ctx.User().GetUserID() == "" || ctx.User().GetUserID() != binding.ActorID || !validBinding || models4datatug.ValidateSharedProjectIdentifier(binding.SpaceID) != nil || models4datatug.ValidateSharedProjectIdentifier(binding.PayerID) != nil {
		return empty, ErrSharedProjectUnauthorized
	}
	requestDigest, err := hex.DecodeString(binding.RequestDigest)
	if err != nil || len(requestDigest) != sha256.Size || binding.RequestDigest != strings.ToLower(binding.RequestDigest) {
		return empty, ErrSharedProjectUnauthorized
	}
	var businessAccess *SpaceServiceAccess
	if s.business != nil {
		access, err := s.business.ReadCurrent(ctx, tx, binding.SpaceID)
		if err != nil {
			return empty, err
		}
		if err := verifyBusinessProjectAccessAt(access, observedAt); err != nil {
			return empty, err
		}
		businessAccess = &access
		if _, _, _, err := s.readCurrentProjectOwnerContact(ctx, tx, binding); err != nil {
			return empty, err
		}
	} else {
		if _, err := readCurrentPaidProjectAccess(ctx, tx, *s.paid, binding.ActorID, binding.PayerID, observedAt); err != nil {
			return empty, err
		}
	}
	command := sha256.Sum256([]byte("datatug.explicit-shared-project-activation.v1:" + binding.RequestDigest))
	request := contract4spaceus.ReserveRoleCapabilityRequest{
		ReserveCapabilityRequest: contract4spaceus.ReserveCapabilityRequest{
			SpaceID: coretypes.SpaceID(binding.SpaceID), Capability: "datatug",
			Purpose: "shared-project-create", Stage: "enable-datatug",
			CommandID: hex.EncodeToString(command[:]),
		},
		RequiredRoles: append([]string(nil), s.activation.requiredRoles...),
	}
	prepared, err := s.activation.activator.PlanRoleCapabilityActivationInTransaction(ctx, tx, request, observedAt)
	if err != nil || sharedProjectPortAbsent(prepared) {
		return empty, fmt.Errorf("%w: role capability activation could not be planned", ErrSharedProjectUnauthorized)
	}
	if businessAccess != nil {
		at := s.now().UTC()
		if at.IsZero() || at.Before(observedAt) {
			return empty, ErrBusinessServiceUnproved
		}
		if err := verifyBusinessProjectAccessAt(*businessAccess, at); err != nil {
			return empty, err
		}
	}
	return prepared, nil
}

func (s *SharedProjectService) prepareCreateAuthority(
	ctx context.Context,
	binding SharedProjectCreateBinding,
) (PreparedSharedProjectCreate, time.Time, facade.ContextWithUser, error) {
	var empty PreparedSharedProjectCreate
	if s == nil || s.now == nil {
		return empty, time.Time{}, nil, ErrSharedProjectUnavailable
	}
	if s.activation != nil {
		userCtx, ok := ctx.(facade.ContextWithUser)
		if !ok || sharedProjectPortAbsent(userCtx) || sharedProjectPortAbsent(userCtx.User()) || userCtx.User().GetUserID() != binding.ActorID {
			return empty, time.Time{}, nil, ErrSharedProjectUnauthorized
		}
		observedAt := s.now().UTC()
		if observedAt.IsZero() {
			return empty, time.Time{}, nil, ErrSharedProjectUnauthorized
		}
		return PreparedSharedProjectCreate{Binding: binding, IssuedAt: observedAt}, observedAt, userCtx, nil
	}
	if sharedProjectPortAbsent(s.authority) {
		return empty, time.Time{}, nil, ErrSharedProjectUnavailable
	}
	prepared, err := s.authority.PrepareSharedProjectCreate(ctx, binding)
	if err != nil || prepared.Binding != binding || prepared.IssuedAt.IsZero() || sharedProjectPortAbsent(prepared.Validator) {
		return empty, time.Time{}, nil, ErrSharedProjectUnauthorized
	}
	observedAt := s.now().UTC()
	if observedAt.IsZero() || observedAt.Before(prepared.IssuedAt) {
		return empty, time.Time{}, nil, ErrSharedProjectUnauthorized
	}
	return prepared, observedAt, nil, nil
}

func (s *SharedProjectService) validatePreparedCreateInTransaction(
	ctx context.Context,
	tx dal.ReadwriteTransaction,
	binding SharedProjectCreateBinding,
	prepared PreparedSharedProjectCreate,
	observedAt time.Time,
) error {
	if s != nil && s.activation != nil {
		return nil // Core role and Space authority are checked by the transaction-bound activation plan.
	}
	if sharedProjectPortAbsent(prepared.Validator) || prepared.Binding != binding || prepared.IssuedAt.IsZero() {
		return ErrSharedProjectUnauthorized
	}
	if err := prepared.Validator.ValidateSharedProjectCreateInTransaction(ctx, tx, binding, observedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrSharedProjectUnauthorized, err)
	}
	return nil
}

func (s *SharedProjectService) applyExplicitCreateActivation(
	userCtx facade.ContextWithUser,
	txCtx context.Context,
	tx dal.ReadwriteTransaction,
	binding SharedProjectCreateBinding,
	observedAt time.Time,
) error {
	if s == nil || s.activation == nil {
		return nil
	}
	prepared, err := s.PlanExplicitProjectCreateActivationInTransaction(userCtx, tx, binding, observedAt)
	if err != nil {
		return err
	}
	if sharedProjectPortAbsent(prepared) {
		return ErrSharedProjectUnauthorized
	}
	if err := prepared.Apply(txCtx, tx); err != nil {
		return fmt.Errorf("%w: role capability activation could not be applied", ErrSharedProjectUnauthorized)
	}
	return nil
}

type activationProtectedProjectBasisAuthority struct {
	service *SharedProjectService
	user    facade.ContextWithUser
	binding SharedProjectCreateBinding
	at      time.Time
}

func (a activationProtectedProjectBasisAuthority) VerifyInitialProtectedProjectBasisInTransaction(ctx context.Context, tx dal.ReadwriteTransaction, actor, mode, product, payer string, basis InitialProtectedProjectBasis) error {
	if a.service == nil || a.service.paid == nil || a.service.paid.validate() != nil || sharedProjectPortAbsent(tx) || sharedProjectPortAbsent(a.user) || sharedProjectPortAbsent(a.user.User()) || actor == "" || mode != a.service.paid.Mode || product != a.service.paid.Product || payer == "" || basis.Digest == "" || basis.Allocated < 0 || a.at.IsZero() || actor != a.binding.ActorID || payer != a.binding.PayerID || mode != a.binding.Mode || product != a.binding.Product {
		return ErrProtectedProjectQuota
	}
	if a.user.User().GetUserID() != actor {
		return ErrSharedProjectUnauthorized
	}
	if _, err := readCurrentPaidProjectAccess(ctx, tx, *a.service.paid, actor, payer, a.at); err != nil {
		return ErrSharedProjectUnauthorized
	}
	if _, _, _, err := a.service.readCurrentProjectOwnerContact(ctx, tx, a.binding); err != nil {
		return err
	}
	_, err := a.service.PlanExplicitProjectCreateActivationInTransaction(a.user, tx, a.binding, a.at)
	return err
}
