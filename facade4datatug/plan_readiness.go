package facade4datatug

import (
	"context"
	"math"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
)

// PurchaseReadinessRequest contains only server-verified session and quote
// identity. The host translates the authenticated payment status request.
type PurchaseReadinessRequest struct {
	BuyerID, Mode, SiteID, Family, AccountID    string
	SubscriptionID, QuoteKey, SessionID, PlanID string
}

// PurchaseReadiness verifies the current owner, the last actual Pro plan
// application, and its effective public plan in one read-only transaction.
type PurchaseReadiness struct {
	DB       dal.DB
	Owner    PlanEffectOwnerPort
	Personal PersonalPlanOwnerPort
	Config   PlanConfigReader
	Clock    PlanClock
}

func (r PurchaseReadiness) ReadyForPurchase(ctx context.Context, req PurchaseReadinessRequest) (bool, error) {
	if r.DB == nil || r.Owner == nil || r.Personal == nil || r.Config == nil || r.Clock == nil ||
		req.BuyerID == "" || req.SiteID == "" || req.AccountID == "" || req.SubscriptionID == "" ||
		req.QuoteKey == "" || req.SessionID == "" || req.Family != "datatug" ||
		(req.Mode != "live" && req.Mode != "test") ||
		(req.PlanID != "datatug-pro-monthly" && req.PlanID != "datatug-pro-annual") {
		return false, nil
	}
	now := r.Clock.Now().UTC()
	if now.IsZero() {
		return false, nil
	}
	config, err := r.Config.ReadPlanConfig(ctx, req.AccountID, now)
	if err != nil || ValidatePlanConfig(config) != nil {
		return false, err
	}
	var ready bool
	err = r.DB.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		ready = false // a retried snapshot must not inherit a prior attempt's truth
		if err := r.Personal.VerifyPersonalOwner(ctx, tx, req.BuyerID, req.AccountID); err != nil {
			return err
		}
		owner, err := r.Owner.ReadOwner(ctx, tx, req.Mode, req.Family, req.AccountID)
		if err != nil {
			return err
		}
		if owner.Mode != req.Mode || owner.Family != req.Family || owner.AccountID != req.AccountID ||
			owner.OwnerSubscriptionID != req.SubscriptionID || owner.OwnerGeneration < 1 {
			return nil
		}
		app, exists, err := readPlanApplication(ctx, tx, owner)
		if err != nil || !exists {
			return err
		}
		if app.OwnerSubscriptionID != owner.OwnerSubscriptionID || app.OwnerGeneration != owner.OwnerGeneration ||
			app.SubscriptionRevision != owner.SubscriptionRevision ||
			app.LastProSubscriptionID != req.SubscriptionID || app.LastProOwnerGeneration != owner.OwnerGeneration ||
			app.LastProQuoteKey != req.QuoteKey || app.LastProPlanID != req.PlanID ||
			app.LastProPaidServiceProofID == "" {
			return nil
		}
		plan, exists, err := readPublicPlan(ctx, tx, owner)
		if err != nil || !exists {
			return err
		}
		ready = effectiveProForPurchase(plan, config, now)
		return nil
	})
	if err != nil {
		return false, err
	}
	return ready, nil
}

func effectiveProForPurchase(plan models4datatug.PlanRecord, config PlanConfig, now time.Time) bool {
	if plan.Plan != "pro" || plan.Limits == nil || plan.AIExtraQuestions < 0 || !paidAccessHolds(&plan, now, config) {
		return false
	}
	limits := clonePlanLimits(*plan.Limits)
	if limits.ProjectContributors == nil {
		limits.ProjectContributors = config.ProLimits.ProjectContributors
	}
	return limits.AIQuestions <= math.MaxInt64-plan.AIExtraQuestions &&
		validateLimits(limits) == nil && validateModels(config.ProModels, limits) == nil
}
