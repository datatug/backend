package facade4datatug

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"

	"github.com/datatug/backend/models4datatug"
)

type PlanApplyOutcome string

const (
	PlanApplied    PlanApplyOutcome = "applied"
	PlanIdempotent PlanApplyOutcome = "idempotent"
	PlanObsolete   PlanApplyOutcome = "obsolete"
)

// AccountPlanWriter is the DataTug domain writer. Its ports are bound by the
// host to one DAL database; no provider or cross-extension package is imported.
type AccountPlanWriter struct {
	DB        dal.DB
	Owner     PlanEffectOwnerPort
	Personal  PersonalPlanOwnerPort
	Allocator PaidMonthAllocator
	Limits    ProLimitsResolver
	Clock     PlanClock
}

func (w AccountPlanWriter) Apply(ctx context.Context, effect AccountPlanEffect) (PlanApplyOutcome, error) {
	if w.DB == nil || w.Owner == nil || w.Personal == nil || w.Allocator == nil || w.Clock == nil ||
		!validPlanFence(effect.Fence) {
		return "", ErrPlanEffectUnproved
	}
	now := w.Clock.Now().UTC()
	if now.IsZero() {
		return "", ErrPlanEffectUnproved
	}
	var outcome PlanApplyOutcome
	var frozenLimits *ProLimitsSnapshot
	var protectedProjects, protectedUsers int64
	err := w.DB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		outcome = ""
		current, err := w.Owner.ReadOwner(ctx, tx, effect.Fence.Mode, effect.Fence.Family, effect.Fence.AccountID)
		if err != nil {
			return err
		}
		money, err := w.Owner.ReadMoneyFence(ctx, tx, effect.Fence.Mode, effect.Fence.Family, effect.Fence.AccountID)
		if err != nil {
			return err
		}
		authority, err := w.Owner.ClassifyEffect(ctx, tx, effect.Fence)
		if err != nil {
			return err
		}
		if authority == PlanEffectObsolete {
			outcome = PlanObsolete
			return nil // obsolete semantic payload is never inspected or written
		}
		if authority != PlanEffectCurrent || current != effect.Fence {
			return ErrPlanEffectUnproved
		}
		if err := w.Owner.VerifyCurrentEffect(ctx, tx, effect); err != nil {
			return err
		}
		if !validCurrentPlanEffect(effect) {
			return ErrPlanEffectUnproved
		}
		if err := w.Personal.VerifyPersonalOwner(ctx, tx, effect.BuyerID, effect.Fence.AccountID); err != nil {
			return err
		}
		application, appExists, err := readPlanApplication(ctx, tx, effect.Fence)
		if err != nil {
			return err
		}
		digest, err := planEffectDigest(effect)
		if err != nil {
			return err
		}
		if appExists {
			if application.OwnerSubscriptionID == effect.Fence.OwnerSubscriptionID &&
				application.OwnerGeneration == effect.Fence.OwnerGeneration &&
				application.SubscriptionRevision == effect.Fence.SubscriptionRevision {
				if application.EffectDigest != digest {
					return ErrPlanEffectUnproved
				}
				outcome = PlanIdempotent
				return nil // no config read, projection, CAS or plan rewrite
			}
			if application.OwnerGeneration > effect.Fence.OwnerGeneration ||
				(application.OwnerGeneration == effect.Fence.OwnerGeneration && application.SubscriptionRevision >= effect.Fence.SubscriptionRevision) {
				return ErrPlanEffectUnproved
			}
		}
		if money.IngestEpoch != effect.MoneyIngestEpoch || money.ReconciledEpoch > money.IngestEpoch ||
			(!effect.CashBasisKnown && !money.Unresolved) {
			return ErrPlanEffectUnproved
		}
		plan, planExists, err := readPublicPlan(ctx, tx, effect.Fence)
		if err != nil {
			return err
		}
		mapping, err := mapPlanEffect(effect, application)
		if err != nil {
			return err
		}
		if mapping.Outcome == "write" && mapping.Record.Plan == "pro" {
			if frozenLimits == nil {
				if w.Limits == nil {
					return ErrPlanEffectUnproved
				}
				snapshot, resolveErr := w.Limits.ResolveProLimits(effect.PlanID, clonePlanEffectGrants(effect.Grants))
				if resolveErr != nil {
					return resolveErr
				}
				snapshot, resolveErr = checkedProLimits(snapshot, effect.Grants)
				if resolveErr != nil {
					return resolveErr
				}
				// Freeze only explicit, verified source grants before writes and
				// preserve the same scalar snapshot across transaction retries.
				if effect.Grants.ProtectedProjects != nil && effect.Grants.ProtectedProjectUsers != nil {
					if *effect.Grants.ProtectedProjects <= 0 || *effect.Grants.ProtectedProjectUsers <= 0 {
						return ErrPlanEffectUnproved
					}
					protectedProjects, protectedUsers = *effect.Grants.ProtectedProjects, *effect.Grants.ProtectedProjectUsers
				}
				frozenLimits = &snapshot
			}
			limits := clonePlanLimits(frozenLimits.Limits)
			mapping.Record.Limits = &limits
		}
		if mapping.Outcome == "write" && planExists {
			if plan.AIExtraQuestions < 0 {
				return ErrPlanEffectUnproved
			}
			mapping.Record.AIExtraQuestions = plan.AIExtraQuestions
		}
		var months map[string]models4datatug.PaidMoneyMonth
		var existed map[string]bool
		var changed []string
		periods := application.BasisPeriods
		if effect.CashBasisKnown {
			basis, projectionErr := w.projectBasis(effect)
			if projectionErr != nil {
				return projectionErr
			}
			periods = unionPeriods(application.BasisPeriods, basis)
			months, existed, changed, err = readMoneyMonths(ctx, tx, effect.Fence, application.BasisVersion, periods, basis)
			if err != nil {
				return err
			}
			if len(changed) > 0 {
				application.BasisVersion, err = nextBasisVersion(application.BasisVersion)
				if err != nil {
					return err
				}
				for _, period := range changed {
					month := months[period]
					month.BasisRevision = application.BasisVersion
					months[period] = month
				}
			}
		}
		// ReconcileMoney reads owner and digest itself; it must be the first
		// transaction write. No DAL read may follow it.
		if effect.CashBasisKnown {
			if err := w.Owner.ReconcileMoney(ctx, tx, effect); err != nil {
				return err
			}
		}
		for _, period := range changed {
			month := months[period]
			rec := record.NewRecordWithData(models4datatug.NewPaidMoneyMonthKey(effect.Fence.Mode, effect.Fence.Family, effect.Fence.AccountID, period), &month)
			if existed[period] {
				err = tx.Update(ctx, rec.Key(), []update.Update{
					update.ByFieldPath([]string{"basisRevision"}, month.BasisRevision),
					update.ByFieldPath([]string{"basisMicroEUR"}, month.BasisMicroEUR),
				})
			} else {
				err = tx.Insert(ctx, rec)
			}
			if err != nil {
				return err
			}
		}
		application.V, application.Mode, application.Family, application.AccountID = 1, effect.Fence.Mode, effect.Fence.Family, effect.Fence.AccountID
		application.OwnerSubscriptionID, application.OwnerGeneration = effect.Fence.OwnerSubscriptionID, effect.Fence.OwnerGeneration
		application.SubscriptionRevision, application.EffectDigest = effect.Fence.SubscriptionRevision, digest
		application.BasisPeriods = append([]string(nil), periods...)
		if !effect.BasisOnly {
			application.LastFullEffectSubscriptionID = effect.Fence.OwnerSubscriptionID
			application.LastFullEffectOwnerGeneration = effect.Fence.OwnerGeneration
			application.LastFullEffectRevision = effect.Fence.SubscriptionRevision
			application.LastFullEffectQuoteKey = effect.QuoteKey
		}
		if mapping.Outcome == "write" {
			if mapping.Record.Plan == "pro" {
				application.LimitsVersion = frozenLimits.Version
				application.LastProSubscriptionID = effect.Fence.OwnerSubscriptionID
				application.LastProOwnerGeneration = effect.Fence.OwnerGeneration
				application.LastProQuoteKey = effect.QuoteKey
				application.LastProPlanID = effect.PlanID
				application.LastProPaidServiceProofID = effect.PaidServiceProofID
				// Legacy config-resolved display limits do not prove paid grants.
				application.LastProProtectedProjects, application.LastProProtectedProjectUsers = protectedProjects, protectedUsers
			} else {
				application.LastProSubscriptionID = ""
				application.LastProOwnerGeneration = 0
				application.LastProQuoteKey = ""
				application.LastProPlanID = ""
				application.LastProPaidServiceProofID = ""
				application.LastProProtectedProjects, application.LastProProtectedProjectUsers = 0, 0
			}
		}
		appRec := record.NewRecordWithData(models4datatug.NewPlanApplicationKey(effect.Fence.Mode, effect.Fence.Family, effect.Fence.AccountID), &application)
		if appExists {
			err = tx.Set(ctx, appRec)
		} else {
			err = tx.Insert(ctx, appRec)
		}
		if err != nil {
			return err
		}
		if mapping.Outcome == "write" {
			mapping.Record.UpdatedAt = &now
			if err := writePublicPlan(ctx, tx, effect.Fence, mapping.Record, planExists); err != nil {
				return err
			}
		}
		outcome = PlanApplied
		return nil
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
}

func nextBasisVersion(current int64) (int64, error) {
	if current < 0 || current == math.MaxInt64 {
		return 0, ErrPlanEffectUnproved
	}
	return current + 1, nil
}

func validPlanFence(f PlanOwnerFence) bool {
	return (f.Mode == "live" || f.Mode == "test") && f.Family == "datatug" && f.AccountID != "" &&
		f.OwnerSubscriptionID != "" && f.OwnerGeneration > 0 && f.SubscriptionRevision > 0
}

func validCurrentPlanEffect(e AccountPlanEffect) bool {
	if e.BuyerID == "" || e.AccountKind != "personal" || e.Tier != "pro" || e.QuoteKey == "" ||
		e.MoneyIngestEpoch < 0 || e.SourceSubscriptionID == "" || e.SourceRevision <= 0 ||
		(e.PlanID != "datatug-pro-monthly" && e.PlanID != "datatug-pro-annual") {
		return false
	}
	return (e.PlanID == "datatug-pro-monthly" && e.Period == "month") ||
		(e.PlanID == "datatug-pro-annual" && e.Period == "year")
}

func planEffectDigest(effect AccountPlanEffect) (string, error) {
	effect.ObservedAt = time.Time{}
	data, err := json.Marshal(effect)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func mapPlanEffect(effect AccountPlanEffect, previous models4datatug.PlanApplication) (PlanStateResult, error) {
	if effect.BasisOnly {
		if previous.LastFullEffectSubscriptionID != effect.Fence.OwnerSubscriptionID ||
			previous.LastFullEffectOwnerGeneration != effect.Fence.OwnerGeneration ||
			previous.LastFullEffectRevision < 1 || previous.LastFullEffectRevision >= effect.Fence.SubscriptionRevision ||
			previous.LastFullEffectQuoteKey != effect.QuoteKey {
			return PlanStateResult{}, ErrPlanEffectUnproved
		}
		return PlanStateResult{Outcome: "leave"}, nil
	}
	fullRefund := false
	if !effect.TerminalFullRefund {
		var err error
		fullRefund, err = provedLastServiceRefund(effect)
		if err != nil {
			return PlanStateResult{}, ErrPlanEffectUnproved
		}
	}
	facts := PlanFacts{AccountKind: effect.AccountKind, Tier: effect.Tier, Period: effect.Period,
		ProviderStatus: effect.Status, LastInvoiceRefundedInFull: fullRefund, TerminalFullRefund: effect.TerminalFullRefund, Founding: effect.LaunchGrant}
	if !effect.LastPaidEnd.IsZero() {
		facts.PaidUntil = &effect.LastPaidEnd
	}
	if !effect.ScheduledEnd.IsZero() {
		facts.EndsAt = &effect.ScheduledEnd
	} else if !effect.EffectiveEnd.IsZero() {
		facts.EndsAt = &effect.EffectiveEnd
	}
	result := PlanStateFor(facts)
	if result.Outcome == "refuse" || result.Outcome == "write" && result.Record == nil ||
		result.Outcome == "write" && result.Record.Plan == "pro" && (!effect.PaidService || effect.LastPaidEnd.IsZero() || effect.PaidServiceProofID == "") {
		return PlanStateResult{}, ErrPlanEffectUnproved
	}
	return result, nil
}

func provedLastServiceRefund(effect AccountPlanEffect) (bool, error) {
	proof := effect.LastServiceRefund
	if proof == nil || !proof.Known {
		if effect.Status == "incomplete" || effect.Status == "incomplete_expired" {
			return false, nil // these states leave the public plan untouched
		}
		return false, ErrPlanEffectUnproved
	}
	if !proof.HasPaidServiceInvoice {
		if proof.InvoiceID != "" || !proof.ServiceStartUTC.IsZero() || !proof.ServiceEndUTC.IsZero() ||
			proof.RefundedInFull || effect.PaidService || !effect.LastPaidEnd.IsZero() {
			return false, ErrPlanEffectUnproved
		}
		return false, nil
	}
	if proof.InvoiceID == "" || proof.ServiceStartUTC.IsZero() || proof.ServiceEndUTC.IsZero() ||
		proof.ServiceStartUTC.Location() != time.UTC || proof.ServiceEndUTC.Location() != time.UTC ||
		!proof.ServiceStartUTC.Before(proof.ServiceEndUTC) || !proof.ServiceEndUTC.Equal(effect.LastPaidEnd) {
		return false, ErrPlanEffectUnproved
	}
	return proof.RefundedInFull, nil
}

func clonePlanLimits(in models4datatug.PlanLimits) models4datatug.PlanLimits {
	out := in
	if in.ProjectContributors != nil {
		value := *in.ProjectContributors
		out.ProjectContributors = &value
	}
	if in.ProtectedProjects != nil {
		value := *in.ProtectedProjects
		out.ProtectedProjects = &value
	}
	if in.ProtectedProjectUsers != nil {
		value := *in.ProtectedProjectUsers
		out.ProtectedProjectUsers = &value
	}
	out.AIModelClasses = append([]string(nil), in.AIModelClasses...)
	return out
}

func (w AccountPlanWriter) projectBasis(effect AccountPlanEffect) (map[string]int64, error) {
	basis := map[string]int64{}
	seen := map[string][]byte{}
	for _, payment := range effect.Payments {
		if payment.Mode != effect.Fence.Mode || payment.AccountID != effect.Fence.AccountID ||
			payment.Provider == "" || payment.PaymentID == "" {
			return nil, ErrPlanEffectUnproved
		}
		identity := payment.Mode + "\x00" + payment.Provider + "\x00" + payment.PaymentID
		encoded, err := json.Marshal(payment)
		if err != nil {
			return nil, err
		}
		if prior, ok := seen[identity]; ok {
			if string(prior) != string(encoded) {
				return nil, ErrPlanEffectUnproved
			}
			continue
		}
		seen[identity] = encoded
		allocated, err := w.Allocator.AllocatePaidMonths(payment, effect.Fence.Family)
		if err != nil {
			return nil, err
		}
		for period, amount := range allocated {
			if !validPeriod(period) || amount < 0 || amount > math.MaxInt64-basis[period] {
				return nil, ErrPlanEffectUnproved
			}
			basis[period] += amount
		}
	}
	return basis, nil
}

func validPeriod(period string) bool {
	parsed, err := time.Parse("2006-01", period)
	return err == nil && parsed.Format("2006-01") == period
}

func unionPeriods(previous []string, basis map[string]int64) []string {
	seen := make(map[string]bool, len(previous)+len(basis))
	for _, period := range previous {
		seen[period] = true
	}
	for period := range basis {
		seen[period] = true
	}
	periods := make([]string, 0, len(seen))
	for period := range seen {
		periods = append(periods, period)
	}
	sort.Strings(periods)
	return periods
}

func readPlanApplication(ctx context.Context, tx dal.ReadTransaction, f PlanOwnerFence) (models4datatug.PlanApplication, bool, error) {
	var app models4datatug.PlanApplication
	rec := record.NewRecordWithData(models4datatug.NewPlanApplicationKey(f.Mode, f.Family, f.AccountID), &app)
	err := tx.Get(ctx, rec)
	if record.IsNotFound(err) {
		return app, false, nil
	}
	if err != nil {
		return app, false, err
	}
	if app.V != 1 || app.Mode != f.Mode || app.Family != f.Family || app.AccountID != f.AccountID ||
		app.OwnerGeneration < 1 || app.SubscriptionRevision < 1 || app.EffectDigest == "" || app.BasisVersion < 0 {
		return app, false, ErrPlanEffectUnproved
	}
	return app, true, nil
}

func readPublicPlan(ctx context.Context, tx dal.ReadTransaction, f PlanOwnerFence) (models4datatug.PlanRecord, bool, error) {
	var plan models4datatug.PlanRecord
	key := models4datatug.NewCurrentPlanKey(f.AccountID)
	if f.Mode == "test" {
		key = models4datatug.NewTestPlanKey(f.AccountID)
	}
	err := tx.Get(ctx, record.NewRecordWithData(key, &plan))
	if record.IsNotFound(err) {
		return plan, false, nil
	}
	if err != nil {
		return plan, false, err
	}
	if plan.V != 1 {
		return plan, false, ErrPlanEffectUnproved
	}
	return plan, true, nil
}

func readMoneyMonths(ctx context.Context, tx dal.ReadTransaction, f PlanOwnerFence, maxBasisVersion int64, periods []string, basis map[string]int64) (map[string]models4datatug.PaidMoneyMonth, map[string]bool, []string, error) {
	months := make(map[string]models4datatug.PaidMoneyMonth, len(periods))
	existed := make(map[string]bool, len(periods))
	var changed []string
	for _, period := range periods {
		if !validPeriod(period) {
			return nil, nil, nil, ErrPlanEffectUnproved
		}
		month := models4datatug.PaidMoneyMonth{V: 1, Mode: f.Mode, AccountID: f.AccountID, PeriodID: period}
		err := tx.Get(ctx, record.NewRecordWithData(models4datatug.NewPaidMoneyMonthKey(f.Mode, f.Family, f.AccountID, period), &month))
		if err != nil && !record.IsNotFound(err) {
			return nil, nil, nil, err
		}
		if err == nil {
			existed[period] = true
			if month.V != 1 || month.Mode != f.Mode || month.AccountID != f.AccountID || month.PeriodID != period ||
				month.BasisRevision < 1 || month.BasisRevision > maxBasisVersion || month.BasisMicroEUR < 0 || month.SettledMicroEUR < 0 || month.OutstandingMicroEUR < 0 {
				return nil, nil, nil, ErrPlanEffectUnproved
			}
		}
		if month.BasisMicroEUR != basis[period] {
			month.BasisMicroEUR = basis[period]
			changed = append(changed, period)
		}
		months[period] = month
	}
	return months, existed, changed, nil
}

func writePublicPlan(ctx context.Context, tx dal.ReadwriteTransaction, f PlanOwnerFence, plan *models4datatug.PlanRecord, exists bool) error {
	key := models4datatug.NewCurrentPlanKey(f.AccountID)
	if f.Mode == "test" {
		key = models4datatug.NewTestPlanKey(f.AccountID)
	}
	if !exists {
		return tx.Insert(ctx, record.NewRecordWithData(key, plan))
	}
	return tx.Update(ctx, key, []update.Update{
		update.ByFieldPath([]string{"v"}, plan.V),
		update.ByFieldPath([]string{"plan"}, plan.Plan),
		update.ByFieldPath([]string{"status"}, plan.Status),
		update.ByFieldPath([]string{"period"}, plan.Period),
		update.ByFieldPath([]string{"paidUntil"}, plan.PaidUntil),
		update.ByFieldPath([]string{"endsAt"}, plan.EndsAt),
		update.ByFieldPath([]string{"endedReason"}, plan.EndedReason),
		update.ByFieldPath([]string{"founding"}, plan.Founding),
		update.ByFieldPath([]string{"limits"}, plan.Limits),
		update.ByFieldPath([]string{"updatedAt"}, plan.UpdatedAt),
	})
}
