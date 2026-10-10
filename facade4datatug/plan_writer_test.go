package facade4datatug

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/dal-go/dalgo/adapters/dalgo2memory"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"github.com/datatug/backend/models4datatug"
)

type planOwnerTestRecord struct {
	Fence   PlanOwnerFence `json:"fence"`
	Money   PlanMoneyFence `json:"money"`
	Buyer   string         `json:"buyer"`
	Allowed bool           `json:"allowed"`
}

func planOwnerTestKey() *record.Key { return record.NewKeyWithID("writerTestAuthority", "one") }

type planOwnerTestPort struct {
	authority         PlanEffectAuthority
	verifyErr         error
	readOwnerErr      error
	readMoneyErr      error
	classifyErr       error
	reconcileErr      error
	reads, reconciles int
	lastTx            dal.ReadTransaction
	verifiedPlaceID   string
	reconciledPlaceID string
	onReconcile       func()
}

func (p *planOwnerTestPort) read(ctx context.Context, tx dal.ReadTransaction) (planOwnerTestRecord, error) {
	p.reads++
	p.lastTx = tx
	var data planOwnerTestRecord
	err := tx.Get(ctx, record.NewRecordWithData(planOwnerTestKey(), &data))
	return data, err
}

func (p *planOwnerTestPort) ReadOwner(ctx context.Context, tx dal.ReadTransaction, _, _, _ string) (PlanOwnerFence, error) {
	if p.readOwnerErr != nil {
		return PlanOwnerFence{}, p.readOwnerErr
	}
	data, err := p.read(ctx, tx)
	return data.Fence, err
}
func (p *planOwnerTestPort) ReadMoneyFence(ctx context.Context, tx dal.ReadTransaction, _, _, _ string) (PlanMoneyFence, error) {
	if p.readMoneyErr != nil {
		return PlanMoneyFence{}, p.readMoneyErr
	}
	data, err := p.read(ctx, tx)
	return data.Money, err
}
func (p *planOwnerTestPort) ClassifyEffect(ctx context.Context, tx dal.ReadTransaction, _ PlanOwnerFence) (PlanEffectAuthority, error) {
	if p.classifyErr != nil {
		return "", p.classifyErr
	}
	_, err := p.read(ctx, tx)
	return p.authority, err
}
func (p *planOwnerTestPort) VerifyCurrentEffect(ctx context.Context, tx dal.ReadTransaction, effect AccountPlanEffect) error {
	p.verifiedPlaceID = effect.PlaceID
	_, err := p.read(ctx, tx)
	if err != nil {
		return err
	}
	return p.verifyErr
}
func (p *planOwnerTestPort) ReconcileMoney(ctx context.Context, tx dal.ReadwriteTransaction, effect AccountPlanEffect) error {
	if p.reconcileErr != nil {
		return p.reconcileErr
	}
	p.reconciledPlaceID = effect.PlaceID
	data, err := p.read(ctx, tx) // paymentus CAS performs one final read
	if err != nil {
		return err
	}
	if data.Money.IngestEpoch != effect.MoneyIngestEpoch {
		return ErrPlanEffectUnproved
	}
	p.reconciles++
	if p.onReconcile != nil {
		p.onReconcile()
	}
	data.Money.ReconciledEpoch = data.Money.IngestEpoch
	data.Money.Unresolved = false
	return tx.Set(ctx, record.NewRecordWithData(planOwnerTestKey(), &data))
}

type planPersonalTestPort struct {
	calls int
	fail  bool
}

func (p *planPersonalTestPort) VerifyPersonalOwner(ctx context.Context, tx dal.ReadTransaction, buyer, account string) error {
	p.calls++
	var data planOwnerTestRecord
	if err := tx.Get(ctx, record.NewRecordWithData(planOwnerTestKey(), &data)); err != nil {
		return err
	}
	if p.fail || !data.Allowed || data.Buyer != buyer || data.Fence.AccountID != account {
		return ErrPlanEffectUnproved
	}
	return nil
}

type planAllocatorTestPort struct {
	values map[string]map[string]int64
	calls  int
	err    error
}

func (a *planAllocatorTestPort) AllocatePaidMonths(p PlanPayment, family string) (map[string]int64, error) {
	a.calls++
	if a.err != nil || family != "datatug" {
		return nil, a.err
	}
	return a.values[p.PaymentID], nil
}

type planLimitsTestPort struct {
	snapshot  ProLimitsSnapshot
	calls     int
	err       error
	onResolve func(PlanEffectGrants)
}

func (l *planLimitsTestPort) ResolveProLimits(_ string, grants PlanEffectGrants) (ProLimitsSnapshot, error) {
	l.calls++
	if l.onResolve != nil {
		l.onResolve(grants)
	}
	return l.snapshot, l.err
}

type planWriterFixture struct {
	db        dal.DB
	owner     *planOwnerTestPort
	personal  *planPersonalTestPort
	allocator *planAllocatorTestPort
	limits    *planLimitsTestPort
	writer    AccountPlanWriter
	effect    AccountPlanEffect
}

func newPlanWriterFixture(t *testing.T) *planWriterFixture {
	t.Helper()
	return newPlanWriterFixtureWithDB(t, dalgo2memory.New(dalgo2memory.FirestoreProfile()))
}

func newPlanWriterFixtureWithDB(t *testing.T, db dal.DB) *planWriterFixture {
	t.Helper()
	ctx := context.Background()
	fence := PlanOwnerFence{Mode: "live", Family: "datatug", AccountID: "personal-1", OwnerSubscriptionID: "sub-A", OwnerGeneration: 1, SubscriptionRevision: 1}
	if err := db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, record.NewRecordWithData(planOwnerTestKey(), &planOwnerTestRecord{Fence: fence, Money: PlanMoneyFence{IngestEpoch: 0, Unresolved: true}, Buyer: "buyer-A", Allowed: true}))
	}); err != nil {
		t.Fatal(err)
	}
	pj := int64(3)
	protected := int64(5)
	limits := &planLimitsTestPort{snapshot: ProLimitsSnapshot{Version: "v1", ProjectGuestsKnown: true, Limits: models4datatug.PlanLimits{Contributors: 2, ProjectGuests: 0, ProjectContributors: &pj, ProtectedProjects: &protected, ProtectedProjectUsers: &protected, AIQuestions: 10, AIModelClasses: []string{"fast", "standard"}, AIPaysFor: "owner"}}}
	owner := &planOwnerTestPort{authority: PlanEffectCurrent}
	personal := &planPersonalTestPort{}
	allocator := &planAllocatorTestPort{values: map[string]map[string]int64{"pay-A": {"2026-10": 1000}}}
	clock := &testClock{now: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	effect := AccountPlanEffect{Fence: fence, BuyerID: "buyer-A", AccountKind: "personal", PlanID: "datatug-pro-monthly", Tier: "pro", Period: "month", Status: "active", LastPaidEnd: clock.now.AddDate(0, 1, 0), PaidService: true, Grants: PlanEffectGrants{Contributors: 2, ProjectContributors: 3, AIQuestions: 10, AIPaysFor: "owner"}, CashBasisKnown: true, MoneyIngestEpoch: 0, QuoteKey: "quote-A", PaidServiceProofID: "invoice-A", SourceSubscriptionID: "sub-A", SourceRevision: 1, LastServiceRefund: &ServiceRefundProof{Known: true, HasPaidServiceInvoice: true, InvoiceID: "invoice-A", ServiceStartUTC: clock.now, ServiceEndUTC: clock.now.AddDate(0, 1, 0)}, Payments: []PlanPayment{{Mode: "live", Provider: "fake", PaymentID: "pay-A", AccountID: "personal-1"}}}
	w := AccountPlanWriter{DB: db, Owner: owner, Personal: personal, Allocator: allocator, Limits: limits, Clock: clock}
	return &planWriterFixture{db: db, owner: owner, personal: personal, allocator: allocator, limits: limits, writer: w, effect: effect}
}

func (f *planWriterFixture) setAuthority(t *testing.T, fence PlanOwnerFence, epoch int64, unresolved bool) {
	t.Helper()
	ctx := context.Background()
	data := planOwnerTestRecord{Fence: fence, Money: PlanMoneyFence{IngestEpoch: epoch, Unresolved: unresolved}, Buyer: f.effect.BuyerID, Allowed: true}
	if err := f.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, record.NewRecordWithData(planOwnerTestKey(), &data))
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *planWriterFixture) application(t *testing.T) models4datatug.PlanApplication {
	t.Helper()
	var app models4datatug.PlanApplication
	if err := f.db.Get(context.Background(), record.NewRecordWithData(models4datatug.NewPlanApplicationKey("live", "datatug", "personal-1"), &app)); err != nil {
		t.Fatal(err)
	}
	return app
}
func (f *planWriterFixture) publicPlan(t *testing.T) models4datatug.PlanRecord {
	t.Helper()
	var plan models4datatug.PlanRecord
	if err := f.db.Get(context.Background(), record.NewRecordWithData(models4datatug.NewCurrentPlanKey("personal-1"), &plan)); err != nil {
		t.Fatal(err)
	}
	return plan
}
func (f *planWriterFixture) month(t *testing.T) models4datatug.PaidMoneyMonth {
	t.Helper()
	var month models4datatug.PaidMoneyMonth
	if err := f.db.Get(context.Background(), record.NewRecordWithData(models4datatug.NewPaidMoneyMonthKey("live", "datatug", "personal-1", "2026-10"), &month)); err != nil {
		t.Fatal(err)
	}
	return month
}

func TestAccountPlanWriterCurrentReplayAndUnknownCash(t *testing.T) {
	f := newPlanWriterFixture(t)
	got, err := f.writer.Apply(context.Background(), f.effect)
	if err != nil || got != PlanApplied {
		t.Fatal(got, err)
	}
	if m := f.month(t); m.BasisRevision != 1 || m.BasisMicroEUR != 1000 {
		t.Fatal(m)
	}
	if p := f.publicPlan(t); p.Plan != "pro" || p.Limits == nil || p.AIExtraQuestions != 0 {
		t.Fatal(p)
	}
	if a := f.application(t); a.LastProQuoteKey != "quote-A" || a.LastFullEffectRevision != 1 || a.LimitsVersion != "v1" {
		t.Fatal(a)
	}
	got, err = f.writer.Apply(context.Background(), f.effect)
	if err != nil || got != PlanIdempotent || f.limits.calls != 1 || f.allocator.calls != 1 || f.owner.reconciles != 1 {
		t.Fatal(got, err, f.limits.calls, f.allocator.calls, f.owner.reconciles)
	}
	// A new proved service effect can update the plan with unknown cash, but
	// cannot clear the direct money fence or reuse the old positive basis.
	f.effect.Fence.SubscriptionRevision++
	f.effect.SourceRevision++
	f.effect.CashBasisKnown = false
	f.effect.MoneyIngestEpoch = 1
	f.setAuthority(t, f.effect.Fence, 1, true)
	got, err = f.writer.Apply(context.Background(), f.effect)
	if err != nil || got != PlanApplied || f.owner.reconciles != 1 {
		t.Fatal(got, err)
	}
	if m := f.month(t); m.BasisMicroEUR != 1000 || m.BasisRevision != 1 {
		t.Fatal(m)
	}
}

func TestAccountPlanWriterEndedThenBasisOnly(t *testing.T) {
	f := newPlanWriterFixture(t)
	if _, err := f.writer.Apply(context.Background(), f.effect); err != nil {
		t.Fatal(err)
	}
	f.effect.Fence.SubscriptionRevision++
	f.effect.SourceRevision++
	f.effect.Status = "canceled"
	f.effect.Payments = nil
	f.setAuthority(t, f.effect.Fence, 1, true)
	f.effect.MoneyIngestEpoch = 1
	if _, err := f.writer.Apply(context.Background(), f.effect); err != nil {
		t.Fatal(err)
	}
	before := f.publicPlan(t)
	if before.Plan != "free" || f.application(t).LastProQuoteKey != "" {
		t.Fatal(before)
	}
	f.effect.Fence.SubscriptionRevision++
	f.effect.SourceRevision++
	f.effect.BasisOnly = true
	f.effect.Payments = []PlanPayment{{Mode: "live", Provider: "fake", PaymentID: "pay-A", AccountID: "personal-1"}}
	f.allocator.values["pay-A"] = map[string]int64{"2026-10": 300}
	f.effect.MoneyIngestEpoch = 2
	f.setAuthority(t, f.effect.Fence, 2, true)
	if _, err := f.writer.Apply(context.Background(), f.effect); err != nil {
		t.Fatal(err)
	}
	if after := f.publicPlan(t); !reflect.DeepEqual(before, after) {
		t.Fatal(before, after)
	}
	if m := f.month(t); m.BasisMicroEUR != 300 || m.BasisRevision != 3 {
		t.Fatal(m)
	}
	if a := f.application(t); a.LastFullEffectRevision != 2 || a.SubscriptionRevision != 3 || a.LastProQuoteKey != "" {
		t.Fatal(a)
	}
}

func TestAccountPlanWriterPreservesPlaceIDThroughVerificationAndMoneyReconciliation(t *testing.T) {
	f := newPlanWriterFixture(t)
	f.effect.PlaceID = "paymentus-place/opaque-reference-v1"
	if got, err := f.writer.Apply(context.Background(), f.effect); err != nil || got != PlanApplied {
		t.Fatalf("apply with signed place identity: result=%q err=%v", got, err)
	}
	if f.owner.verifiedPlaceID != f.effect.PlaceID || f.owner.reconciledPlaceID != f.effect.PlaceID || f.owner.reconciles != 1 {
		t.Fatalf("place identity changed across authority ports: effect=%q verified=%q reconciled=%q calls=%d",
			f.effect.PlaceID, f.owner.verifiedPlaceID, f.owner.reconciledPlaceID, f.owner.reconciles)
	}
	if got, err := f.writer.Apply(context.Background(), f.effect); err != nil || got != PlanIdempotent {
		t.Fatalf("same place identity was not idempotent: result=%q err=%v", got, err)
	}
	changed := f.effect
	changed.PlaceID = "another-place"
	if got, err := f.writer.Apply(context.Background(), changed); !errors.Is(err, ErrPlanEffectUnproved) || got != "" {
		t.Fatalf("same owner revision accepted a different place identity: result=%q err=%v", got, err)
	}
}

func TestAccountPlanWriterRejectsMalformedAndObsolete(t *testing.T) {
	f := newPlanWriterFixture(t)
	f.owner.authority = PlanEffectObsolete
	f.effect.BuyerID = "" // deliberately invalid discarded payload
	got, err := f.writer.Apply(context.Background(), f.effect)
	if err != nil || got != PlanObsolete || f.personal.calls != 0 || f.owner.reconciles != 0 {
		t.Fatal(got, err)
	}
	f.owner.authority = PlanEffectCurrent
	f.effect.BuyerID = "buyer-A"
	f.owner.verifyErr = errors.New("changed accepted digest")
	if got, err = f.writer.Apply(context.Background(), f.effect); err == nil || got != "" {
		t.Fatal(got, err)
	}
	f.owner.verifyErr = nil
	f.limits.snapshot.Version = ""
	if got, err = f.writer.Apply(context.Background(), f.effect); err == nil || got != "" {
		t.Fatal(got, err)
	}
	for _, key := range []*record.Key{
		models4datatug.NewCurrentPlanKey("personal-1"),
		models4datatug.NewPlanApplicationKey("live", "datatug", "personal-1"),
		models4datatug.NewPaidMoneyMonthKey("live", "datatug", "personal-1", "2026-10"),
	} {
		exists, err := f.db.Exists(context.Background(), key)
		if err != nil || exists {
			t.Fatal(key, exists, err)
		}
	}
}

func TestAccountPlanWriterFreezesUntrustedLimits(t *testing.T) {
	f := newPlanWriterFixture(t)
	projectContributors := f.limits.snapshot.Limits.ProjectContributors
	protectedProjects := f.limits.snapshot.Limits.ProtectedProjects
	protectedUsers := f.limits.snapshot.Limits.ProtectedProjectUsers
	classes := f.limits.snapshot.Limits.AIModelClasses
	f.owner.onReconcile = func() {
		*projectContributors = 99
		*protectedProjects = 99
		*protectedUsers = 99
		classes[0] = "wrong"
	}
	if _, err := f.writer.Apply(context.Background(), f.effect); err != nil {
		t.Fatal(err)
	}
	plan := f.publicPlan(t)
	if plan.Limits == nil || *plan.Limits.ProjectContributors != 3 || *plan.Limits.ProtectedProjects != 5 || *plan.Limits.ProtectedProjectUsers != 5 || !reflect.DeepEqual(plan.Limits.AIModelClasses, []string{"fast", "standard"}) {
		t.Fatal(plan.Limits)
	}
	bad := []func(*ProLimitsSnapshot){
		func(s *ProLimitsSnapshot) { s.ProjectGuestsKnown = false },
		func(s *ProLimitsSnapshot) { s.Limits.ProjectContributors = nil },
		func(s *ProLimitsSnapshot) { s.Limits.ProtectedProjects = nil },
		func(s *ProLimitsSnapshot) { s.Limits.ProtectedProjects = timeInt64(0) },
		func(s *ProLimitsSnapshot) { s.Limits.AIQuestions++ },
		func(s *ProLimitsSnapshot) { s.Limits.AIModelClasses = nil },
	}
	for i, mutate := range bad {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			candidate := newPlanWriterFixture(t)
			mutate(&candidate.limits.snapshot)
			got, err := candidate.writer.Apply(context.Background(), candidate.effect)
			if err == nil || got != "" {
				t.Fatal(got, err)
			}
			exists, err := candidate.db.Exists(context.Background(), models4datatug.NewCurrentPlanKey("personal-1"))
			if err != nil || exists {
				t.Fatal(exists, err)
			}
		})
	}
}

func TestAccountPlanWriterResolverCannotMutateAcceptedProtectedGrants(t *testing.T) {
	f := newPlanWriterFixture(t)
	f.effect.Grants.ProtectedProjects = timeInt64(5)
	f.effect.Grants.ProtectedProjectUsers = timeInt64(5)
	f.limits.onResolve = func(grants PlanEffectGrants) {
		*grants.ProtectedProjects = 99
		*grants.ProtectedProjectUsers = 99
	}
	if got, err := f.writer.Apply(context.Background(), f.effect); err != nil || got != PlanApplied {
		t.Fatal(got, err)
	}
	if *f.effect.Grants.ProtectedProjects != 5 || *f.effect.Grants.ProtectedProjectUsers != 5 {
		t.Fatal(f.effect.Grants)
	}
	if plan := f.publicPlan(t); plan.Limits == nil || *plan.Limits.ProtectedProjects != 5 || *plan.Limits.ProtectedProjectUsers != 5 {
		t.Fatal(plan)
	}
	if got, err := f.writer.Apply(context.Background(), f.effect); err != nil || got != PlanIdempotent {
		t.Fatal(got, err)
	}
}

func TestPurchaseReadinessBoundToOwnerAndAppliedPro(t *testing.T) {
	f := newPlanWriterFixture(t)
	svc, _, _, _, _, _ := validPlanTestService()
	r := PurchaseReadiness{DB: f.db, Owner: f.owner, Personal: f.personal, Config: svc.Config, Clock: f.writer.Clock}
	req := PurchaseReadinessRequest{BuyerID: "buyer-A", Mode: "live", SiteID: "datatug", Family: "datatug", AccountID: "personal-1", SubscriptionID: "sub-A", QuoteKey: "quote-A", SessionID: "session-A", PlanID: "datatug-pro-monthly"}
	assertReady := func(want bool) {
		t.Helper()
		got, err := r.ReadyForPurchase(context.Background(), req)
		if err != nil || got != want {
			t.Fatal(got, err, want)
		}
	}
	assertReady(false)
	if _, err := f.writer.Apply(context.Background(), f.effect); err != nil {
		t.Fatal(err)
	}
	assertReady(true)
	plan := f.publicPlan(t)
	plan.AIExtraQuestions = math.MaxInt64
	seedPlanTestRecord(t, f.db, models4datatug.NewCurrentPlanKey("personal-1"), &plan)
	assertReady(false) // GET also falls Free on an overflowing allowance
	plan.AIExtraQuestions = 0
	seedPlanTestRecord(t, f.db, models4datatug.NewCurrentPlanKey("personal-1"), &plan)
	req.QuoteKey = "foreign"
	assertReady(false)
	req.QuoteKey = "quote-A"
	req.Mode = "test"
	assertReady(false)
	req.Mode = "live"
	// A's public Pro can still be active when C becomes the accepted owner.
	c := f.effect
	c.Fence.OwnerSubscriptionID = "sub-C"
	c.Fence.OwnerGeneration = 2
	c.Fence.SubscriptionRevision = 1
	c.BuyerID = "buyer-C"
	c.QuoteKey = "quote-C"
	c.SourceSubscriptionID = "sub-C"
	c.PaidServiceProofID = "invoice-C"
	c.MoneyIngestEpoch = 1
	c.Payments = []PlanPayment{{Mode: "live", Provider: "fake", PaymentID: "pay-A", AccountID: "personal-1"}, {Mode: "live", Provider: "fake", PaymentID: "pay-C", AccountID: "personal-1"}}
	f.allocator.values["pay-C"] = map[string]int64{"2026-10": 500}
	f.effect = c
	f.setAuthority(t, c.Fence, 1, true)
	req.BuyerID = "buyer-C"
	req.SubscriptionID = "sub-C"
	req.QuoteKey = "quote-C"
	assertReady(false)
	if _, err := f.writer.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	assertReady(true)
	// A basis-only revision cannot manufacture a new Pro provenance.
	c.Fence.SubscriptionRevision = 2
	c.SourceSubscriptionID = "sub-A"
	c.SourceRevision = 2
	c.BasisOnly = true
	c.MoneyIngestEpoch = 2
	f.effect = c
	f.setAuthority(t, c.Fence, 2, true)
	if _, err := f.writer.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	assertReady(true)
	f.personal.fail = true
	if got, err := r.ReadyForPurchase(context.Background(), req); got || err == nil {
		t.Fatal(got, err)
	}
	f.personal.fail = false
	c.Fence.SubscriptionRevision = 3
	c.SourceSubscriptionID = "sub-C"
	c.SourceRevision = 3
	c.BasisOnly = false
	c.Status = "canceled"
	c.MoneyIngestEpoch = 3
	f.effect = c
	f.setAuthority(t, c.Fence, 3, true)
	assertReady(false) // terminal C accepted but its plan sink has not run
	if _, err := f.writer.Apply(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	assertReady(false)
}

func TestAccountPlanWriterFullRefundEndsThenBasisOnly(t *testing.T) {
	f := newPlanWriterFixture(t)
	refunded := true
	f.effect.LastServiceRefund.RefundedInFull = refunded
	f.effect.TerminalFullRefund = true
	f.effect.Payments = nil
	got, err := f.writer.Apply(context.Background(), f.effect)
	if err != nil || got != PlanApplied {
		t.Fatal(got, err)
	}
	if a := f.application(t); a.LastFullEffectRevision != 1 || a.LastProQuoteKey != "" {
		t.Fatal(a)
	}
	plan := f.publicPlan(t)
	if plan.Plan != "free" || plan.Status != "ended" || plan.EndedReason != "refunded" || plan.Limits != nil {
		t.Fatal(plan)
	}
	svc, _, _, _, _, _ := validPlanTestService()
	readiness := PurchaseReadiness{DB: f.db, Owner: f.owner, Personal: f.personal, Config: svc.Config, Clock: f.writer.Clock}
	ready, err := readiness.ReadyForPurchase(context.Background(), PurchaseReadinessRequest{BuyerID: "buyer-A", Mode: "live", SiteID: "datatug", Family: "datatug", AccountID: "personal-1", SubscriptionID: "sub-A", QuoteKey: "quote-A", SessionID: "session-A", PlanID: "datatug-pro-monthly"})
	if err != nil || ready {
		t.Fatal(ready, err)
	}
	f.effect.Fence.SubscriptionRevision = 2
	f.effect.SourceRevision = 2
	f.effect.BasisOnly = true
	f.effect.MoneyIngestEpoch = 1
	f.setAuthority(t, f.effect.Fence, 1, true)
	if got, err = f.writer.Apply(context.Background(), f.effect); err != nil || got != PlanApplied {
		t.Fatal(got, err)
	}
	if a := f.application(t); a.SubscriptionRevision != 2 || a.LastFullEffectRevision != 1 || a.LastProQuoteKey != "" {
		t.Fatal(a)
	}
	plan = f.publicPlan(t)
	if plan.Plan != "free" || plan.EndedReason != "refunded" {
		t.Fatal(plan)
	}
}

func TestAccountPlanWriterActiveProImmediatelyEndsOnFullRefund(t *testing.T) {
	f := newPlanWriterFixture(t)
	if got, err := f.writer.Apply(context.Background(), f.effect); err != nil || got != PlanApplied {
		t.Fatal(got, err)
	}
	if plan := f.publicPlan(t); plan.Plan != "pro" || plan.Limits == nil || *plan.Limits.ProtectedProjects != 5 {
		t.Fatal(plan)
	}
	f.effect.Fence.SubscriptionRevision = 2
	f.effect.SourceRevision = 2
	f.effect.MoneyIngestEpoch = 1
	f.effect.TerminalFullRefund = true
	f.effect.LastServiceRefund.RefundedInFull = true
	f.effect.Payments = nil
	f.setAuthority(t, f.effect.Fence, 1, true)
	if got, err := f.writer.Apply(context.Background(), f.effect); err != nil || got != PlanApplied {
		t.Fatal(got, err)
	}
	if plan := f.publicPlan(t); plan.Plan != "free" || plan.Status != "ended" || plan.EndedReason != "refunded" || plan.Limits != nil {
		t.Fatal(plan)
	}
	if app := f.application(t); app.LastProQuoteKey != "" || app.LastFullEffectRevision != 2 {
		t.Fatal(app)
	}
}

func TestAccountPlanWriterLegacyRefundAndLatchedRenewal(t *testing.T) {
	f := newPlanWriterFixture(t)
	f.effect.LastServiceRefund.RefundedInFull = true // accepted before the additive latch existed
	f.effect.Payments = nil
	if got, err := f.writer.Apply(context.Background(), f.effect); err != nil || got != PlanApplied {
		t.Fatal(got, err)
	}
	if plan := f.publicPlan(t); plan.Plan != "free" || plan.EndedReason != "refunded" {
		t.Fatal(plan)
	}
	f.effect.Fence.SubscriptionRevision = 2
	f.effect.SourceRevision = 2
	f.effect.MoneyIngestEpoch = 1
	f.effect.TerminalFullRefund = true
	f.effect.LastServiceRefund = nil // a newer paid invoice does not erase the durable source latch
	f.effect.LastPaidEnd = f.effect.LastPaidEnd.AddDate(0, 1, 0)
	f.effect.Payments = []PlanPayment{{Mode: "live", Provider: "fake", PaymentID: "pay-A", AccountID: "personal-1"}}
	f.setAuthority(t, f.effect.Fence, 1, true)
	if got, err := f.writer.Apply(context.Background(), f.effect); err != nil || got != PlanApplied {
		t.Fatal(got, err)
	}
	if plan := f.publicPlan(t); plan.Plan != "free" || plan.EndedReason != "refunded" {
		t.Fatal(plan)
	}
	if app := f.application(t); app.LastProQuoteKey != "" || app.LastFullEffectRevision != 2 || len(app.BasisPeriods) != 1 {
		t.Fatal(app)
	}
	if got, err := f.writer.Apply(context.Background(), f.effect); err != nil || got != PlanIdempotent {
		t.Fatal(got, err)
	}
}

func TestAccountPlanWriterLatchedRefundWithUnknownMoneyStillEndsPro(t *testing.T) {
	f := newPlanWriterFixture(t)
	f.effect.TerminalFullRefund = true
	f.effect.CashBasisKnown = false
	f.effect.LastServiceRefund = nil
	f.effect.Payments = nil
	if got, err := f.writer.Apply(context.Background(), f.effect); err != nil || got != PlanApplied {
		t.Fatal(got, err)
	}
	if plan := f.publicPlan(t); plan.Plan != "free" || plan.EndedReason != "refunded" {
		t.Fatal(plan)
	}
	if err := f.db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		money, err := f.owner.ReadMoneyFence(ctx, tx, "live", "datatug", "personal-1")
		if err != nil {
			return err
		}
		if !money.Unresolved || money.IngestEpoch != 0 {
			t.Fatal(money)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountPlanWriterBasisProjectionRefusals(t *testing.T) {
	base := PlanPayment{Mode: "live", Provider: "fake", PaymentID: "pay-A", AccountID: "personal-1"}
	cases := []struct {
		name  string
		alter func(*planWriterFixture)
	}{
		{"unproved money", func(f *planWriterFixture) {
			f.effect.CashBasisKnown = false
			f.setAuthority(t, f.effect.Fence, 0, false)
		}},
		{"wrong epoch", func(f *planWriterFixture) { f.effect.MoneyIngestEpoch = 1 }},
		{"foreign receipt", func(f *planWriterFixture) { f.effect.Payments[0].AccountID = "foreign" }},
		{"missing receipt identity", func(f *planWriterFixture) { f.effect.Payments[0].PaymentID = "" }},
		{"changed duplicate", func(f *planWriterFixture) {
			p := base
			p.ReceivedCents = 1
			f.effect.Payments = append(f.effect.Payments, p)
		}},
		{"allocator error", func(f *planWriterFixture) { f.allocator.err = errors.New("unknown cash") }},
		{"bad period", func(f *planWriterFixture) { f.allocator.values["pay-A"] = map[string]int64{"2026-13": 1} }},
		{"negative amount", func(f *planWriterFixture) { f.allocator.values["pay-A"] = map[string]int64{"2026-10": -1} }},
		{"overflow", func(f *planWriterFixture) {
			p := base
			p.PaymentID = "pay-B"
			f.effect.Payments = append(f.effect.Payments, p)
			f.allocator.values["pay-A"] = map[string]int64{"2026-10": 9223372036854775807}
			f.allocator.values["pay-B"] = map[string]int64{"2026-10": 1}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlanWriterFixture(t)
			tc.alter(f)
			got, err := f.writer.Apply(context.Background(), f.effect)
			if err == nil || got != "" {
				t.Fatal(got, err)
			}
			exists, err := f.db.Exists(context.Background(), models4datatug.NewCurrentPlanKey("personal-1"))
			if err != nil || exists {
				t.Fatal(exists, err)
			}
		})
	}
}

func TestConfiguredProLimitsChecksSnapshotAndCopies(t *testing.T) {
	f := newPlanWriterFixture(t)
	config := ConfiguredProLimits{ByPlanID: map[string]ProLimitsSnapshot{"datatug-pro-monthly": f.limits.snapshot}}
	if _, err := config.ResolveProLimits("missing", f.effect.Grants); err == nil {
		t.Fatal("unknown plan accepted")
	}
	snapshot, err := config.ResolveProLimits(f.effect.PlanID, f.effect.Grants)
	if err != nil {
		t.Fatal(err)
	}
	*f.limits.snapshot.Limits.ProjectContributors = 9
	*f.limits.snapshot.Limits.ProtectedProjects = 9
	f.limits.snapshot.Limits.AIModelClasses[0] = "changed"
	if *snapshot.Limits.ProjectContributors != 3 || *snapshot.Limits.ProtectedProjects != 5 || snapshot.Limits.AIModelClasses[0] != "fast" {
		t.Fatal(snapshot)
	}
}

func TestProtectedGrantsAndLegacyEffectDigest(t *testing.T) {
	f := newPlanWriterFixture(t)
	legacy, err := json.Marshal(f.effect)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(legacy, []byte("protectedProjects")) || bytes.Contains(legacy, []byte("terminalFullRefund")) {
		t.Fatal(string(legacy))
	}
	first, err := planEffectDigest(f.effect)
	if err != nil {
		t.Fatal(err)
	}
	f.effect.ObservedAt = time.Now()
	second, err := planEffectDigest(f.effect)
	if err != nil || first != second {
		t.Fatal(first, second, err)
	}
	for _, tc := range []struct {
		name            string
		projects, users *int64
		valid           bool
	}{
		{"explicit", timeInt64(5), timeInt64(5), true},
		{"partial", timeInt64(5), nil, false},
		{"wrong count", timeInt64(6), timeInt64(5), false},
		{"zero", timeInt64(0), timeInt64(0), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := f.effect.Grants
			g.ProtectedProjects, g.ProtectedProjectUsers = tc.projects, tc.users
			_, err := checkedProLimits(f.limits.snapshot, g)
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}

func seedPlanTestRecord(t *testing.T, db dal.DB, key *record.Key, data any) {
	t.Helper()
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, record.NewRecordWithData(key, data))
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountPlanWriterAuthorityAndStateRefusals(t *testing.T) {
	boom := errors.New("source unavailable")
	cases := []struct {
		name  string
		alter func(*testing.T, *planWriterFixture)
	}{
		{"missing database", func(_ *testing.T, f *planWriterFixture) { f.writer.DB = nil }},
		{"invalid fence", func(_ *testing.T, f *planWriterFixture) { f.effect.Fence.Mode = "other" }},
		{"missing clock", func(_ *testing.T, f *planWriterFixture) { f.writer.Clock = &testClock{} }},
		{"owner read failure", func(_ *testing.T, f *planWriterFixture) { f.owner.readOwnerErr = boom }},
		{"money read failure", func(_ *testing.T, f *planWriterFixture) { f.owner.readMoneyErr = boom }},
		{"classify failure", func(_ *testing.T, f *planWriterFixture) { f.owner.classifyErr = boom }},
		{"unproved classification", func(_ *testing.T, f *planWriterFixture) { f.owner.authority = PlanEffectUnproved }},
		{"current tuple mismatch", func(t *testing.T, f *planWriterFixture) {
			other := f.effect.Fence
			other.OwnerSubscriptionID = "other"
			f.setAuthority(t, other, 0, true)
		}},
		{"invalid current plan", func(_ *testing.T, f *planWriterFixture) { f.effect.PlanID = "other" }},
		{"personal owner revoked", func(_ *testing.T, f *planWriterFixture) { f.personal.fail = true }},
		{"malformed application", func(t *testing.T, f *planWriterFixture) {
			seedPlanTestRecord(t, f.db, models4datatug.NewPlanApplicationKey("live", "datatug", "personal-1"), &models4datatug.PlanApplication{V: 2})
		}},
		{"undecodable effect time", func(_ *testing.T, f *planWriterFixture) {
			f.effect.CheckoutStartedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		}},
		{"unproved refund", func(_ *testing.T, f *planWriterFixture) { f.effect.LastServiceRefund = nil }},
		{"missing limits", func(_ *testing.T, f *planWriterFixture) { f.writer.Limits = nil }},
		{"resolver failure", func(_ *testing.T, f *planWriterFixture) { f.limits.err = boom }},
		{"reconcile failure", func(_ *testing.T, f *planWriterFixture) { f.owner.reconcileErr = boom }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlanWriterFixture(t)
			tc.alter(t, f)
			got, err := f.writer.Apply(context.Background(), f.effect)
			if err == nil || got != "" {
				t.Fatal(got, err)
			}
		})
	}
}

func TestAccountPlanWriterCurrentDigestAndRevisionFences(t *testing.T) {
	f := newPlanWriterFixture(t)
	if _, err := f.writer.Apply(context.Background(), f.effect); err != nil {
		t.Fatal(err)
	}
	f.effect.Grants.AIQuestions++
	if got, err := f.writer.Apply(context.Background(), f.effect); err == nil || got != "" {
		t.Fatal(got, err)
	}
	f.effect.Grants.AIQuestions--
	f.effect.Fence.SubscriptionRevision = 2
	f.effect.SourceRevision = 2
	f.effect.MoneyIngestEpoch = 1
	f.setAuthority(t, f.effect.Fence, 1, true)
	app := f.application(t)
	app.OwnerGeneration = 3 // cannot let old A overwrite a future generation
	seedPlanTestRecord(t, f.db, models4datatug.NewPlanApplicationKey("live", "datatug", "personal-1"), &app)
	if got, err := f.writer.Apply(context.Background(), f.effect); err == nil || got != "" {
		t.Fatal(got, err)
	}
}

func TestPlanEffectMappingAndRefundProof(t *testing.T) {
	f := newPlanWriterFixture(t)
	if got, err := mapPlanEffect(f.effect, models4datatug.PlanApplication{}); err != nil || got.Record == nil || got.Record.Plan != "pro" {
		t.Fatal(got, err)
	}
	invalid := []func(*AccountPlanEffect){
		func(e *AccountPlanEffect) { e.BasisOnly = true },
		func(e *AccountPlanEffect) { e.Status = "unknown" },
		func(e *AccountPlanEffect) { e.LastServiceRefund = nil },
		func(e *AccountPlanEffect) { e.LastServiceRefund.Known = false },
		func(e *AccountPlanEffect) { e.LastServiceRefund.HasPaidServiceInvoice = false },
		func(e *AccountPlanEffect) { e.LastServiceRefund.InvoiceID = "" },
		func(e *AccountPlanEffect) { e.LastServiceRefund.ServiceEndUTC = e.LastServiceRefund.ServiceStartUTC },
	}
	for i, mutate := range invalid {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			e := f.effect
			proof := *e.LastServiceRefund
			e.LastServiceRefund = &proof
			mutate(&e)
			if got, err := mapPlanEffect(e, models4datatug.PlanApplication{}); err == nil || got.Outcome != "" {
				t.Fatal(got, err)
			}
		})
	}
	// Explicit complete-source absence is only compatible with no paid invoice.
	e := f.effect
	e.PaidService = false
	e.LastPaidEnd = time.Time{}
	e.Status = "canceled"
	e.LastServiceRefund = &ServiceRefundProof{Known: true}
	if got, err := mapPlanEffect(e, models4datatug.PlanApplication{}); err != nil || got.Record == nil || got.Record.Plan != "free" {
		t.Fatal(got, err)
	}
	e.Status = "incomplete"
	e.LastServiceRefund = nil
	if got, err := mapPlanEffect(e, models4datatug.PlanApplication{}); err != nil || got.Outcome != "leave" {
		t.Fatal(got, err)
	}
	e = f.effect
	e.ScheduledEnd = f.writer.Clock.Now().AddDate(0, 2, 0)
	if got, err := mapPlanEffect(e, models4datatug.PlanApplication{}); err != nil || got.Record.EndsAt == nil || !got.Record.EndsAt.Equal(e.ScheduledEnd) {
		t.Fatal(got, err)
	}
	e.ScheduledEnd = time.Time{}
	e.EffectiveEnd = f.writer.Clock.Now().AddDate(0, 3, 0)
	if got, err := mapPlanEffect(e, models4datatug.PlanApplication{}); err != nil || got.Record.EndsAt == nil || !got.Record.EndsAt.Equal(e.EffectiveEnd) {
		t.Fatal(got, err)
	}
}

type faultPlanReadTx struct {
	dal.ReadTransaction
	fail error
}

func (tx faultPlanReadTx) Get(_ context.Context, _ record.Record) error { return tx.fail }

func TestPlanWriterReadHelpersFailClosed(t *testing.T) {
	f := newPlanWriterFixture(t)
	ctx := context.Background()
	readFailure := errors.New("unreadable record")
	if err := f.db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		broken := faultPlanReadTx{ReadTransaction: tx, fail: readFailure}
		if _, _, err := readPlanApplication(ctx, broken, f.effect.Fence); !errors.Is(err, readFailure) {
			t.Fatal(err)
		}
		if _, _, err := readPublicPlan(ctx, broken, f.effect.Fence); !errors.Is(err, readFailure) {
			t.Fatal(err)
		}
		if _, _, _, err := readMoneyMonths(ctx, broken, f.effect.Fence, 0, []string{"2026-10"}, map[string]int64{}); !errors.Is(err, readFailure) {
			t.Fatal(err)
		}
		if _, _, _, err := readMoneyMonths(ctx, tx, f.effect.Fence, 0, []string{"bad"}, map[string]int64{}); !errors.Is(err, ErrPlanEffectUnproved) {
			t.Fatal(err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	seedPlanTestRecord(t, f.db, models4datatug.NewCurrentPlanKey("personal-1"), &models4datatug.PlanRecord{V: 2})
	if err := f.db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		_, _, err := readPublicPlan(ctx, tx, f.effect.Fence)
		if !errors.Is(err, ErrPlanEffectUnproved) {
			t.Fatal(err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	seedPlanTestRecord(t, f.db, models4datatug.NewPaidMoneyMonthKey("live", "datatug", "personal-1", "2026-10"), &models4datatug.PaidMoneyMonth{V: 1, Mode: "live", AccountID: "personal-1", PeriodID: "2026-10", BasisRevision: 2, BasisMicroEUR: 10})
	if err := f.db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		_, _, _, err := readMoneyMonths(ctx, tx, f.effect.Fence, 1, []string{"2026-10"}, map[string]int64{"2026-10": 10})
		if !errors.Is(err, ErrPlanEffectUnproved) {
			t.Fatal(err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPurchaseReadinessFailurePaths(t *testing.T) {
	f := newPlanWriterFixture(t)
	svc, _, _, _, _, _ := validPlanTestService()
	r := PurchaseReadiness{DB: f.db, Owner: f.owner, Personal: f.personal, Config: svc.Config, Clock: f.writer.Clock}
	req := PurchaseReadinessRequest{BuyerID: "buyer-A", Mode: "live", SiteID: "datatug", Family: "datatug", AccountID: "personal-1", SubscriptionID: "sub-A", QuoteKey: "quote-A", SessionID: "session-A", PlanID: "datatug-pro-monthly"}
	if got, err := r.ReadyForPurchase(context.Background(), PurchaseReadinessRequest{}); got || err != nil {
		t.Fatal(got, err)
	}
	r.Clock = &testClock{}
	if got, err := r.ReadyForPurchase(context.Background(), req); got || err != nil {
		t.Fatal(got, err)
	}
	r.Clock = f.writer.Clock
	r.Config = testConfigReader{err: errors.New("config unavailable")}
	if got, err := r.ReadyForPurchase(context.Background(), req); got || err == nil {
		t.Fatal(got, err)
	}
	r.Config = svc.Config
	f.owner.readOwnerErr = errors.New("owner unavailable")
	if got, err := r.ReadyForPurchase(context.Background(), req); got || err == nil {
		t.Fatal(got, err)
	}
	f.owner.readOwnerErr = nil
	app := models4datatug.PlanApplication{V: 1, Mode: "live", Family: "datatug", AccountID: "personal-1", OwnerSubscriptionID: "sub-A", OwnerGeneration: 1, SubscriptionRevision: 1, EffectDigest: "accepted", LastProSubscriptionID: "sub-A", LastProOwnerGeneration: 1, LastProQuoteKey: "quote-A", LastProPlanID: "datatug-pro-monthly", LastProPaidServiceProofID: "invoice-A"}
	seedPlanTestRecord(t, f.db, models4datatug.NewPlanApplicationKey("live", "datatug", "personal-1"), &app)
	if got, err := r.ReadyForPurchase(context.Background(), req); got || err != nil {
		t.Fatal(got, err)
	}
	config := svc.Config.(testConfigReader).config
	if effectiveProForPurchase(models4datatug.PlanRecord{Plan: "free"}, config, f.writer.Clock.Now()) {
		t.Fatal("free is ready")
	}
	plan := models4datatug.PlanRecord{Plan: "pro", Status: "active", PaidUntil: &f.effect.LastPaidEnd, Limits: &config.ProLimits}
	plan.Limits.ProjectContributors = nil
	if !effectiveProForPurchase(plan, config, f.writer.Clock.Now()) {
		t.Fatal("supported omitted project contributors should use configured compatibility")
	}
}

type retryReadinessDB struct {
	dal.DB
	between func()
}

func (db retryReadinessDB) RunReadonlyTransaction(ctx context.Context, worker dal.ROTxWorker, options ...dal.TransactionOption) error {
	if err := db.DB.RunReadonlyTransaction(ctx, worker, options...); err != nil {
		return err
	}
	db.between()
	return db.DB.RunReadonlyTransaction(ctx, worker, options...)
}

func TestPurchaseReadinessClearsTruthOnTransactionRetry(t *testing.T) {
	f := newPlanWriterFixture(t)
	if _, err := f.writer.Apply(context.Background(), f.effect); err != nil {
		t.Fatal(err)
	}
	svc, _, _, _, _, _ := validPlanTestService()
	r := PurchaseReadiness{DB: retryReadinessDB{DB: f.db, between: func() {
		newOwner := f.effect.Fence
		newOwner.OwnerSubscriptionID = "sub-C"
		newOwner.OwnerGeneration = 2
		newOwner.SubscriptionRevision = 1
		f.setAuthority(t, newOwner, 1, true)
	}}, Owner: f.owner, Personal: f.personal, Config: svc.Config, Clock: f.writer.Clock}
	req := PurchaseReadinessRequest{BuyerID: "buyer-A", Mode: "live", SiteID: "datatug", Family: "datatug", AccountID: "personal-1", SubscriptionID: "sub-A", QuoteKey: "quote-A", SessionID: "session-A", PlanID: "datatug-pro-monthly"}
	got, err := r.ReadyForPurchase(context.Background(), req)
	if err != nil || got {
		t.Fatal(got, err)
	}
}

type faultPlanDB struct {
	dal.DB
	failWriteAt, writeCount int
	commitErr               error
}

func (db *faultPlanDB) RunReadwriteTransaction(ctx context.Context, worker dal.RWTxWorker, options ...dal.TransactionOption) error {
	return db.DB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := worker(ctx, &faultPlanTx{ReadwriteTransaction: tx, db: db}); err != nil {
			return err
		}
		return db.commitErr
	}, options...)
}

type faultPlanTx struct {
	dal.ReadwriteTransaction
	db *faultPlanDB
}

func (tx *faultPlanTx) fail() error {
	tx.db.writeCount++
	if tx.db.writeCount == tx.db.failWriteAt {
		return errors.New("injected write failure")
	}
	return nil
}
func (tx *faultPlanTx) Insert(ctx context.Context, r record.Record, options ...dal.InsertOption) error {
	if err := tx.fail(); err != nil {
		return err
	}
	return tx.ReadwriteTransaction.Insert(ctx, r, options...)
}
func (tx *faultPlanTx) Set(ctx context.Context, r record.Record) error {
	if err := tx.fail(); err != nil {
		return err
	}
	return tx.ReadwriteTransaction.Set(ctx, r)
}
func (tx *faultPlanTx) Update(ctx context.Context, k *record.Key, u []update.Update, preconditions ...dal.Precondition) error {
	if err := tx.fail(); err != nil {
		return err
	}
	return tx.ReadwriteTransaction.Update(ctx, k, u, preconditions...)
}

func TestAccountPlanWriterAtomicWriteFailures(t *testing.T) {
	for _, failAt := range []int{2, 3, 4} {
		t.Run(string(rune('a'+failAt)), func(t *testing.T) {
			f := newPlanWriterFixture(t)
			fault := &faultPlanDB{DB: f.db, failWriteAt: failAt}
			f.writer.DB = fault
			got, err := f.writer.Apply(context.Background(), f.effect)
			if err == nil || got != "" {
				t.Fatal(got, err)
			}
			exists, err := f.db.Exists(context.Background(), models4datatug.NewCurrentPlanKey("personal-1"))
			if err != nil || exists {
				t.Fatal(exists, err)
			}
			exists, err = f.db.Exists(context.Background(), models4datatug.NewPaidMoneyMonthKey("live", "datatug", "personal-1", "2026-10"))
			if err != nil || exists {
				t.Fatal(exists, err)
			}
		})
	}
	f := newPlanWriterFixture(t)
	f.writer.DB = &faultPlanDB{DB: f.db, commitErr: errors.New("commit conflict")}
	if got, err := f.writer.Apply(context.Background(), f.effect); err == nil || got != "" {
		t.Fatal(got, err)
	}
}

func TestAccountPlanWriterStoredStateAndModeRefusals(t *testing.T) {
	base := newPlanWriterFixture(t)
	seedPlanTestRecord(t, base.db, models4datatug.NewCurrentPlanKey("personal-1"), &models4datatug.PlanRecord{V: 2})
	if got, err := base.writer.Apply(context.Background(), base.effect); err == nil || got != "" {
		t.Fatal(got, err)
	}
	f := newPlanWriterFixture(t)
	seedPlanTestRecord(t, f.db, models4datatug.NewCurrentPlanKey("personal-1"), &models4datatug.PlanRecord{V: 1, AIExtraQuestions: -1})
	if got, err := f.writer.Apply(context.Background(), f.effect); err == nil || got != "" {
		t.Fatal(got, err)
	}
	f = newPlanWriterFixture(t)
	seedPlanTestRecord(t, f.db, models4datatug.NewPaidMoneyMonthKey("live", "datatug", "personal-1", "2026-10"), &models4datatug.PaidMoneyMonth{V: 2})
	if got, err := f.writer.Apply(context.Background(), f.effect); err == nil || got != "" {
		t.Fatal(got, err)
	}
	if _, err := nextBasisVersion(math.MaxInt64); !errors.Is(err, ErrPlanEffectUnproved) {
		t.Fatal(err)
	}
	if got, err := nextBasisVersion(0); err != nil || got != 1 {
		t.Fatal(got, err)
	}
	// Test mode has its own public plan and private keys.
	f = newPlanWriterFixture(t)
	f.effect.Fence.Mode = "test"
	f.effect.Payments[0].Mode = "test"
	f.setAuthority(t, f.effect.Fence, 0, true)
	if got, err := f.writer.Apply(context.Background(), f.effect); err != nil || got != PlanApplied {
		t.Fatal(got, err)
	}
	exists, err := f.db.Exists(context.Background(), models4datatug.NewTestPlanKey("personal-1"))
	if err != nil || !exists {
		t.Fatal(exists, err)
	}
	exists, err = f.db.Exists(context.Background(), models4datatug.NewCurrentPlanKey("personal-1"))
	if err != nil || exists {
		t.Fatal(exists, err)
	}
}

func TestAccountPlanWriterRejectsBasisVersionOverflowWithoutWrite(t *testing.T) {
	f := newPlanWriterFixture(t)
	if _, err := f.writer.Apply(context.Background(), f.effect); err != nil {
		t.Fatal(err)
	}
	previous := f.publicPlan(t)
	f.writer.DB = overrideAppBasisDB{DB: f.db}
	f.effect.Fence.SubscriptionRevision = 2
	f.effect.SourceRevision = 2
	f.effect.MoneyIngestEpoch = 1
	f.allocator.values["pay-A"] = map[string]int64{"2026-10": 2000}
	f.setAuthority(t, f.effect.Fence, 1, true)
	got, err := f.writer.Apply(context.Background(), f.effect)
	if !errors.Is(err, ErrPlanEffectUnproved) || got != "" || f.owner.reconciles != 1 {
		t.Fatal(got, err, f.owner.reconciles)
	}
	if after := f.publicPlan(t); !reflect.DeepEqual(previous, after) {
		t.Fatal(previous, after)
	}
	if month := f.month(t); month.BasisMicroEUR != 1000 || month.BasisRevision != 1 {
		t.Fatal(month)
	}
}

// DALgo's JSON-based memory adapter rounds MaxInt64. This wrapper emulates an
// exact Firestore int64 read after the real stored record has been fetched.
type overrideAppBasisDB struct{ dal.DB }

func (db overrideAppBasisDB) RunReadwriteTransaction(ctx context.Context, worker dal.RWTxWorker, options ...dal.TransactionOption) error {
	return db.DB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return worker(ctx, overrideAppBasisTx{ReadwriteTransaction: tx})
	}, options...)
}

type overrideAppBasisTx struct{ dal.ReadwriteTransaction }

func (tx overrideAppBasisTx) Get(ctx context.Context, rec record.Record) error {
	if err := tx.ReadwriteTransaction.Get(ctx, rec); err != nil {
		return err
	}
	if rec.Key().String() == models4datatug.NewPlanApplicationKey("live", "datatug", "personal-1").String() {
		rec.Data().(*models4datatug.PlanApplication).BasisVersion = math.MaxInt64
	}
	return nil
}

func TestPlanWriterDuplicateReceiptAndMalformedTimestamp(t *testing.T) {
	f := newPlanWriterFixture(t)
	f.effect.Payments = append(f.effect.Payments, f.effect.Payments[0])
	if got, err := f.writer.Apply(context.Background(), f.effect); err != nil || got != PlanApplied || f.allocator.calls != 1 {
		t.Fatal(got, err, f.allocator.calls)
	}
	f = newPlanWriterFixture(t)
	f.effect.Payments[0].Lines = []PlanPaymentLine{{StartUTC: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}
	if _, err := f.writer.projectBasis(f.effect); err == nil {
		t.Fatal("malformed receipt time accepted")
	}
}

func TestAccountPlanWriterRetainsUsageExtrasAndMoneyLiabilities(t *testing.T) {
	f := newPlanWriterFixture(t)
	clock := f.writer.Clock.(*testClock)
	if _, err := f.writer.Apply(context.Background(), f.effect); err != nil {
		t.Fatal(err)
	}
	if clock.calls != 1 {
		t.Fatal(clock.calls)
	}
	plan := f.publicPlan(t)
	plan.AIExtraQuestions = 7
	seedPlanTestRecord(t, f.db, models4datatug.NewCurrentPlanKey("personal-1"), &plan)
	month := f.month(t)
	month.SettledMicroEUR = 210
	month.OutstandingMicroEUR = 50
	month.Frozen = true
	seedPlanTestRecord(t, f.db, models4datatug.NewPaidMoneyMonthKey("live", "datatug", "personal-1", "2026-10"), &month)
	f.effect.Fence.SubscriptionRevision = 2
	f.effect.SourceRevision = 2
	f.effect.MoneyIngestEpoch = 1
	f.allocator.values["pay-A"] = map[string]int64{"2026-10": 0}
	f.setAuthority(t, f.effect.Fence, 1, true)
	if _, err := f.writer.Apply(context.Background(), f.effect); err != nil {
		t.Fatal(err)
	}
	if got := f.publicPlan(t); got.AIExtraQuestions != 7 {
		t.Fatal(got)
	}
	if got := f.month(t); got.BasisRevision != 2 || got.BasisMicroEUR != 0 || got.SettledMicroEUR != 210 || got.OutstandingMicroEUR != 50 || !got.Frozen {
		t.Fatal(got)
	}
	if clock.calls != 2 {
		t.Fatal(clock.calls)
	}
}

type retryPlanDB struct {
	dal.DB
	between func()
}

func (db retryPlanDB) RunReadwriteTransaction(ctx context.Context, worker dal.RWTxWorker, options ...dal.TransactionOption) error {
	_ = db.DB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := worker(ctx, tx); err != nil {
			return err
		}
		return errors.New("simulated retry conflict") // rollback first attempt
	}, options...)
	db.between()
	return db.DB.RunReadwriteTransaction(ctx, worker, options...)
}

func TestAccountPlanWriterFreezesLimitsAcrossTransactionRetry(t *testing.T) {
	f := newPlanWriterFixture(t)
	config := &f.limits.snapshot.Limits
	f.writer.DB = retryPlanDB{DB: f.db, between: func() {
		*config.ProjectContributors = 20
		config.AIModelClasses[0] = "drift"
	}}
	got, err := f.writer.Apply(context.Background(), f.effect)
	if err != nil || got != PlanApplied || f.limits.calls != 1 {
		t.Fatal(got, err, f.limits.calls)
	}
	plan := f.publicPlan(t)
	if plan.Limits == nil || *plan.Limits.ProjectContributors != 3 || plan.Limits.AIModelClasses[0] != "fast" {
		t.Fatal(plan.Limits)
	}
}
