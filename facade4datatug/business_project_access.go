// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"errors"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

const (
	BusinessProjectProductID   = "datatug-business-usage"
	BusinessProjectServiceID   = "datatug"
	BusinessProjectOwnerFamily = "datatug"
	BusinessProjectAccountKind = "space"
	BusinessMonthlyPlanID      = "datatug-business-usage-monthly"
	BusinessAnnualPlanID       = "datatug-business-usage-annual"
)

var (
	ErrBusinessServiceUnproved = errors.New("business space service access is unproved")
	ErrBusinessServiceEnded    = errors.New("business space service has ended")
)

// SpaceServiceAccess is the product-authorized current service projection.
// It is derived from Paymentus' read-only financial projection plus this
// versioned DataTug entitlement policy. The client cannot supply any field.
type SpaceServiceAccess struct {
	Mode, ServiceID, PayerSpaceID, OwnerFamily, AccountKind, ProductID, PlanID string
	OwnerSubscriptionID, PaidServiceProofID, GrantVersion                      string
	OwnerGeneration, OwnerRevision                                             int64
	PaidUntilUTC                                                               time.Time
	State                                                                      string // active or ended, after complete source reconciliation
	UnlimitedProjects, UnlimitedContacts                                       bool
	checkedAtUTC                                                               time.Time
}

// BusinessProjectAccessPolicy is server configuration. Unlimited project and
// contact grants come from this versioned product policy, never from a money
// field or the absence of Pro limits.
type BusinessProjectAccessPolicy struct {
	GrantVersion string
}

func (p BusinessProjectAccessPolicy) validate() error {
	if p.GrantVersion == "" || len(p.GrantVersion) > 128 {
		return ErrBusinessServiceUnproved
	}
	return nil
}

// BusinessProjectAccessVerifier contains no provider or mutation handle. The
// Paymentus reader can only inspect the caller's transaction; paid end and
// evidence expiry are both enforced before DataTug uses this result.
type BusinessProjectAccessVerifier struct {
	reader contract4paymentus.CurrentSpaceServiceReader
	policy BusinessProjectAccessPolicy
	now    func() time.Time
}

func NewBusinessProjectAccessVerifier(reader contract4paymentus.CurrentSpaceServiceReader, policy BusinessProjectAccessPolicy, now func() time.Time) (*BusinessProjectAccessVerifier, error) {
	if sharedProjectPortAbsent(reader) || policy.validate() != nil || now == nil {
		return nil, ErrBusinessServiceUnproved
	}
	return &BusinessProjectAccessVerifier{reader: reader, policy: policy, now: now}, nil
}

// ReadCurrent verifies the selected Space's explicit LIVE DataTug Business
// service. Current contact membership and action-specific roles are separate
// checks. Same-Space owner replacement is allowed: the admission's original
// proof remains provenance, while this read verifies the current owner.
func (v *BusinessProjectAccessVerifier) ReadCurrent(ctx context.Context, tx dal.ReadTransaction, spaceID string) (SpaceServiceAccess, error) {
	var zero SpaceServiceAccess
	if v == nil || sharedProjectPortAbsent(v.reader) || v.policy.validate() != nil || v.now == nil || ctx == nil || sharedProjectPortAbsent(tx) || models4datatug.ValidateSharedProjectIdentifier(spaceID) != nil {
		return zero, ErrBusinessServiceUnproved
	}
	startedAt := v.now().UTC()
	if startedAt.IsZero() {
		return zero, ErrBusinessServiceUnproved
	}
	scope := contract4paymentus.ServicePurchaseScope{Mode: contract4paymentus.ModeLive, SpaceID: spaceID, ServiceID: BusinessProjectServiceID}
	current, err := v.reader.ReadCurrentSpaceServiceAccess(ctx, sharedProjectReadTransaction{tx}, scope)
	if err != nil {
		return zero, err
	}
	if current.Scope != scope || current.PayerSpaceID != spaceID || current.OwnerFamily != BusinessProjectOwnerFamily || current.AccountKind != BusinessProjectAccountKind || current.ProductID != BusinessProjectProductID ||
		(current.PlanID != BusinessMonthlyPlanID && current.PlanID != BusinessAnnualPlanID) || current.OwnerSubscriptionID == "" || current.OwnerGeneration < 1 || current.OwnerRevision < 1 || current.PaidServiceProofID == "" {
		return zero, ErrBusinessServiceUnproved
	}
	if current.State == contract4paymentus.ServiceCurrentFinancialEnded || current.State == contract4paymentus.ServiceCurrentFinancialRefunded {
		return zero, ErrBusinessServiceEnded
	}
	if current.State != contract4paymentus.ServiceCurrentFinancialPaid || !validBusinessServiceTime(current.PaidFromUTC) || !validBusinessServiceTime(current.PaidThroughUTC) ||
		!validBusinessServiceTime(current.EffectiveEndUTC) || (!current.ScheduledEndUTC.IsZero() && !validBusinessServiceTime(current.ScheduledEndUTC)) ||
		!validBusinessServiceTime(current.ObservedAtUTC) || !validBusinessServiceTime(current.EvidenceValidUntilUTC) {
		return zero, ErrBusinessServiceUnproved
	}
	// The source read may cross a paid or evidence boundary. Recheck with a
	// fresh server time after it returns; do not round or extend either fence.
	now := v.now().UTC()
	if now.IsZero() || now.Before(startedAt) || now.Before(current.ObservedAtUTC) || now.Before(current.PaidFromUTC) {
		return zero, ErrBusinessServiceUnproved
	}
	paidUntil := earlierBusinessTime(current.PaidThroughUTC, current.EvidenceValidUntilUTC, current.EffectiveEndUTC, current.ScheduledEndUTC)
	if !now.Before(paidUntil) {
		return zero, ErrBusinessServiceEnded
	}
	return SpaceServiceAccess{
		Mode: string(scope.Mode), ServiceID: scope.ServiceID, PayerSpaceID: current.PayerSpaceID,
		OwnerFamily: current.OwnerFamily, AccountKind: current.AccountKind, ProductID: current.ProductID, PlanID: current.PlanID,
		OwnerSubscriptionID: current.OwnerSubscriptionID, OwnerGeneration: current.OwnerGeneration, OwnerRevision: current.OwnerRevision,
		PaidServiceProofID: current.PaidServiceProofID, GrantVersion: v.policy.GrantVersion,
		PaidUntilUTC: paidUntil, State: "active", UnlimitedProjects: true, UnlimitedContacts: true, checkedAtUTC: now,
	}, nil
}

func validBusinessServiceTime(t time.Time) bool {
	return !t.IsZero() && t.Location() == time.UTC
}

func earlierBusinessTime(first time.Time, rest ...time.Time) time.Time {
	for _, candidate := range rest {
		if !candidate.IsZero() && candidate.Before(first) {
			first = candidate
		}
	}
	return first
}

func verifyBusinessProjectAccessAt(access SpaceServiceAccess, at time.Time) error {
	if access.State != "active" || access.PaidUntilUTC.IsZero() || at.IsZero() || !access.checkedAtUTC.IsZero() && at.Before(access.checkedAtUTC) {
		return ErrBusinessServiceUnproved
	}
	if !at.Before(access.PaidUntilUTC) {
		return ErrBusinessServiceEnded
	}
	return nil
}

func (s *SharedProjectService) verifyCurrentProjectService(ctx context.Context, tx dal.ReadTransaction, admission *models4datatug.ProjectAdmission, at time.Time) error {
	if s == nil || admission == nil {
		return ErrSharedProjectUnauthorized
	}
	switch admission.Version {
	case 1:
		if s.paid == nil || admission.Product != "datatug" {
			return ErrSharedProjectUnauthorized
		}
		_, err := readCurrentPaidProjectAccess(ctx, tx, *s.paid, admission.ActorID, admission.PayerID, at)
		return err
	case 2:
		if s.business == nil || admission.Product != BusinessProjectProductID || admission.PayerID != admission.SpaceID || admission.ServiceID != BusinessProjectServiceID {
			return ErrBusinessServiceUnproved
		}
		access, err := s.business.ReadCurrent(ctx, tx, admission.SpaceID)
		if err != nil {
			return err
		}
		if access.ProductID != admission.Product || access.PayerSpaceID != admission.PayerID {
			return ErrBusinessServiceUnproved
		}
		return verifyBusinessProjectAccessAt(access, at)
	default:
		return ErrSharedProjectUnauthorized
	}
}
