// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
)

// sharedProjectReadTransaction hides write capabilities from trusted proof and
// owner-contact ports while forwarding reads to the original transaction.
// Ports must not recover a DB, start a nested transaction, or call providers.
type sharedProjectReadTransaction struct{ dal.ReadTransaction }

// currentPaidProjectAccess contains current verified grants, not historical
// admission limits. A replacement subscription for the same admitted sponsor
// may qualify; neither public projection nor configuration fills missing source
// grants. The caller must use its mutation's original transaction and current
// clock on each attempt, including replay and no-op changes.
type currentPaidProjectAccess struct {
	fence                      PlanOwnerFence
	limitsVersion              string
	projectLimit, contactLimit int64
}

func readCurrentPaidProjectAccess(ctx context.Context, tx dal.ReadTransaction, o PaidSharedProjectOptions, actor, payer string, at time.Time) (*currentPaidProjectAccess, error) {
	if ctx == nil || sharedProjectPortAbsent(tx) || o.validate() != nil || actor == "" || models4datatug.ValidateSharedProjectIdentifier(payer) != nil || at.IsZero() {
		return nil, ErrSharedProjectUnavailable
	}
	if err := o.Personal.VerifyPersonalOwner(ctx, tx, actor, payer); err != nil {
		return nil, ErrSharedProjectUnauthorized
	}
	f, err := o.Owner.ReadOwner(ctx, tx, o.Mode, o.Product, payer)
	if err != nil {
		return nil, err
	}
	// Paymentus preserves the lookup identity when its optional owner row is
	// absent, leaving only the ownership tuple empty. Classify exactly that
	// shape as no current paid owner; mismatched identities and partial tuples
	// remain incomplete or contradictory proof below.
	if f.Mode == o.Mode && f.Family == o.Product && f.AccountID == payer &&
		f.OwnerSubscriptionID == "" && f.OwnerGeneration == 0 && f.SubscriptionRevision == 0 {
		return nil, ErrSharedProjectUnauthorized
	}
	if f.Mode != o.Mode || f.Family != o.Product || f.AccountID != payer || f.OwnerSubscriptionID == "" || f.OwnerGeneration < 1 || f.SubscriptionRevision < 1 {
		return nil, ErrPlanEffectUnproved
	}
	app, exists, err := readPlanApplication(ctx, tx, f)
	if err != nil {
		return nil, err
	}
	if !exists || app.V != 1 || app.Mode != f.Mode || app.Family != f.Family || app.AccountID != f.AccountID || app.OwnerSubscriptionID != f.OwnerSubscriptionID || app.OwnerGeneration != f.OwnerGeneration || app.SubscriptionRevision != f.SubscriptionRevision || app.LastProSubscriptionID != f.OwnerSubscriptionID || app.LastProOwnerGeneration != f.OwnerGeneration || (app.LastProPlanID != "datatug-pro-monthly" && app.LastProPlanID != "datatug-pro-annual") || app.LastProQuoteKey == "" || app.LastProPaidServiceProofID == "" || app.LimitsVersion == "" || app.LastProProtectedProjects <= 0 || app.LastProProtectedProjectUsers <= 0 {
		return nil, ErrPlanEffectUnproved
	}
	plan, exists, err := readPublicPlan(ctx, tx, f)
	if err != nil {
		return nil, err
	}
	// Reuse effective-Pro status/config validation, with a strict paid-through
	// boundary for new hosted mutations: display/purchase grace cannot extend
	// this write authority. Match private verified source grants to the public
	// projection before the legacy helper can fill missing pairs from config.
	if !exists || plan.V != 1 || plan.Limits == nil || plan.Limits.ProtectedProjects == nil || plan.Limits.ProtectedProjectUsers == nil || !effectiveProForPurchase(plan, o.Config, at) || plan.PaidUntil == nil || !at.Before(*plan.PaidUntil) {
		return nil, ErrSharedProjectUnauthorized
	}

	if *plan.Limits.ProtectedProjects != app.LastProProtectedProjects || *plan.Limits.ProtectedProjectUsers != app.LastProProtectedProjectUsers {
		return nil, ErrPlanEffectUnproved
	}
	limits, err := resolvedProLimits(*plan.Limits, o.Config.ProLimits)
	if err != nil {
		return nil, err
	}
	return &currentPaidProjectAccess{fence: f, limitsVersion: app.LimitsVersion, projectLimit: *limits.ProtectedProjects, contactLimit: *limits.ProtectedProjectUsers}, nil
}
