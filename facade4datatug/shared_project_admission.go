// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"github.com/datatug/backend/models4datatug"
)

var ErrProtectedProjectQuota = errors.New("protected project quota unavailable or exhausted")

// PaidSharedProjectOptions are reviewed server configuration, never request
// fields. This slice supports current personal Pro only. Business grant/AI
// policy is not inferred from missing limits or historical tier fixtures.
// Product mutation is LIVE-only: TEST checkout projections never create real
// shared projects. Future TEST product isolation needs a separate ref namespace.
type PaidSharedProjectOptions struct {
	Version, Mode, Product string
	ContactLinks           *PaidProjectOwnerLinksOptions
	Config                 PlanConfig
	Directory              PersonalAccountDirectory
	Personal               PersonalPlanOwnerPort
	Owner                  interface {
		ReadOwner(context.Context, dal.ReadTransaction, string, string, string) (PlanOwnerFence, error)
	}
}

func (o PaidSharedProjectOptions) validate() error {
	if o.Version == "" || o.Mode != "live" || o.Product != "datatug" || ValidatePlanConfig(o.Config) != nil || sharedProjectPortAbsent(o.Directory) || sharedProjectPortAbsent(o.Personal) || sharedProjectPortAbsent(o.Owner) {
		return ErrSharedProjectUnavailable
	}
	return nil
}

// NewPaidSharedProjectService connects admission to Create's own transaction.
// Activation still requires proved quota initialization/import, private rules,
// project contact-role guards and project-scoped hosted-write/AI enforcement. The
// legacy constructor remains ownership-only and must not activate paid routes.
func NewPaidSharedProjectService(db dal.DB, ids IDGenerator, authority SharedProjectCreateAuthority, now func() time.Time, options PaidSharedProjectOptions) (*SharedProjectService, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	s, err := NewSharedProjectService(db, ids, authority, now)
	if err != nil {
		return nil, err
	}
	ownerLinks, err := snapshotProjectOwnerLinks(options.ContactLinks)
	if err != nil {
		return nil, err
	}
	s.ownerLinks = ownerLinks
	options = snapshotPaidSharedProjectOptions(options)
	s.paid = &options
	return s, nil
}

func snapshotPaidSharedProjectOptions(options PaidSharedProjectOptions) PaidSharedProjectOptions {
	// Owner ports/catalog have their own snapshot; paid-proof readers need only money configuration.
	options.ContactLinks = nil
	options.Config.ProLimits = clonePlanLimits(options.Config.ProLimits)
	options.Config.FreeFirstMonthLimits = clonePlanLimits(options.Config.FreeFirstMonthLimits)
	options.Config.FreeLaterMonthLimits = clonePlanLimits(options.Config.FreeLaterMonthLimits)
	options.Config.ProModels = append([]PlanModel(nil), options.Config.ProModels...)
	options.Config.FreeModels = append([]PlanModel(nil), options.Config.FreeModels...)
	return options
}

type projectAdmissionState struct {
	options       PaidSharedProjectOptions
	fence         PlanOwnerFence
	limitsVersion string
	quotaRecord   record.Record
	quota         *models4datatug.ProtectedProjectQuota
	limit         models4datatug.ProjectQuotaLimit
	usersLimit    int64
}

func (s *SharedProjectService) readPaidAdmission(ctx context.Context, tx dal.ReadwriteTransaction, b SharedProjectCreateBinding, at time.Time) (*projectAdmissionState, error) {
	o := *s.paid
	access, err := readCurrentPaidProjectAccess(ctx, sharedProjectReadTransaction{tx}, o, b.ActorID, b.PayerID, at)
	if err != nil {
		return nil, err
	}
	qrec, q := models4datatug.NewProtectedProjectQuotaRecord(o.Mode, o.Product, b.PayerID)
	if err = tx.Get(ctx, qrec); err != nil {
		if record.IsNotFound(err) {
			return nil, ErrProtectedProjectQuota
		}
		return nil, err
	}
	if q.Validate() != nil || q.Mode != o.Mode || q.Product != o.Product || q.PayerID != b.PayerID {
		return nil, ErrProtectedProjectQuota
	}
	return &projectAdmissionState{options: o, fence: access.fence, limitsVersion: access.limitsVersion, quotaRecord: qrec, quota: q, limit: models4datatug.ProjectQuotaLimit{Count: access.projectLimit}, usersLimit: access.contactLimit}, nil
}

func (a *projectAdmissionState) verifyReplay(ctx context.Context, tx dal.ReadwriteTransaction, b SharedProjectCreateBinding, projectID string) error {
	r, v := models4datatug.NewProjectAdmissionRecord(b.SpaceID, projectID)
	if err := tx.Get(ctx, r); err != nil {
		return ErrSharedProjectConflict
	}
	if v.Validate() != nil || v.Mode != b.Mode || v.Product != b.Product || v.PayerID != b.PayerID || v.ActorID != b.ActorID || v.SpaceID != b.SpaceID || v.ProjectID != projectID || v.CommandID != b.CommandID || v.RequestDigest != b.RequestDigest {
		return ErrSharedProjectConflict
	}
	return nil
}

func (a *projectAdmissionState) readNewAllocation(ctx context.Context, tx dal.ReadwriteTransaction, b SharedProjectCreateBinding, projectID string) error {
	if !a.limit.Allows(a.quota.Allocated) || a.quota.Allocated == math.MaxInt64 || a.quota.Revision == math.MaxInt64 {
		return ErrProtectedProjectQuota
	}
	r, _ := models4datatug.NewProjectAdmissionRecord(b.SpaceID, projectID)
	err := tx.Get(ctx, r)
	if err == nil {
		return ErrSharedProjectConflict
	}
	if !record.IsNotFound(err) {
		return err
	}
	return nil
}

func (a *projectAdmissionState) writeAllocation(ctx context.Context, tx dal.ReadwriteTransaction, b SharedProjectCreateBinding, projectID string, at time.Time, owner models4datatug.ProjectOwnerContactProof) error {
	r, v := models4datatug.NewProjectAdmissionRecord(b.SpaceID, projectID)
	*v = models4datatug.ProjectAdmission{OwnerContact: owner, Version: 1, Mode: b.Mode, Product: b.Product, PayerID: b.PayerID, ActorID: b.ActorID, SpaceID: b.SpaceID, ProjectID: projectID, CommandID: b.CommandID, RequestDigest: b.RequestDigest, LimitsVersion: a.limitsVersion, SubscriptionID: a.fence.OwnerSubscriptionID, OwnerGeneration: a.fence.OwnerGeneration, CreatedAt: at, ProfileVersion: a.options.Version, QuotaBasisDigest: a.quota.BasisDigest, ProtectedProjectsLimit: a.limit.Count, ProtectedUsersLimit: a.usersLimit, QuotaRevision: a.quota.Revision + 1}
	if err := tx.Insert(ctx, r); err != nil {
		return err
	}
	return tx.Update(ctx, a.quotaRecord.Key(), []update.Update{update.ByFieldPath([]string{"Allocated"}, a.quota.Allocated+1), update.ByFieldPath([]string{"Revision"}, a.quota.Revision+1)})
}

// InitialProtectedProjectBasis must be issued by a trusted complete-inventory
// verifier. It includes existing allocations across ALL Spaces for this payer.
// It must also independently bind actor to the verified context and may only read.
// No runtime reset or missing-record zero inference is provided. An imported
// count requires the verifier to prove matching immutable allocation records.
type InitialProtectedProjectBasis struct {
	Digest    string
	Allocated int64
}
type ProtectedProjectBasisAuthority interface {
	VerifyInitialProtectedProjectBasisInTransaction(context.Context, dal.ReadTransaction, string, string, string, string, InitialProtectedProjectBasis) error
}

// InitializeProtectedProjectQuota is one-time and transaction fenced. A host
// must bind an actual empty/import proof source before calling it; a caller or
// operator claim alone is insufficient. All creates refuse absent quota, so
// concurrent initialization cannot race an uncounted admission. Deletion and
// quota release require a future atomic allocation-retirement command; this
// count is not reset on payment renewal or subscription replacement.
func InitializeProtectedProjectQuota(ctx context.Context, db dal.DB, options PaidSharedProjectOptions, actor, payer string, basis InitialProtectedProjectBasis, authority ProtectedProjectBasisAuthority) error {
	if options.validate() != nil || sharedProjectPortAbsent(db) || sharedProjectPortAbsent(authority) || actor == "" || models4datatug.ValidateSharedProjectIdentifier(payer) != nil || basis.Digest == "" || basis.Allocated < 0 {
		return ErrProtectedProjectQuota
	}
	return db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := options.Personal.VerifyPersonalOwner(ctx, tx, actor, payer); err != nil {
			return ErrSharedProjectUnauthorized
		}
		if err := authority.VerifyInitialProtectedProjectBasisInTransaction(ctx, tx, actor, options.Mode, options.Product, payer, basis); err != nil {
			return err
		}
		r, q := models4datatug.NewProtectedProjectQuotaRecord(options.Mode, options.Product, payer)
		if err := tx.Get(ctx, r); err == nil {
			return ErrSharedProjectConflict
		} else if !record.IsNotFound(err) {
			return err
		}
		*q = models4datatug.ProtectedProjectQuota{Version: 1, Mode: options.Mode, Product: options.Product, PayerID: payer, BasisDigest: basis.Digest, Allocated: basis.Allocated, Revision: 1}
		return tx.Insert(ctx, r)
	})
}
