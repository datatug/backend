package facade4datatug

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crediterra/money"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
	"github.com/sneat-co/paymentus/backend/subscriptions"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

type queryActivityAuthorityContextKey struct{}

type queryActivityGrantRecord struct {
	ActorID, SpaceID, ProjectID string
	Active, QueryUseAllowed     bool
	Binding                     BusinessActivityBinding
}

func queryActivityGrantKey(actorID, spaceID, projectID string) *record.Key {
	return record.NewKeyWithID("testQueryActivityGrants", models4datatug.NewQueryActivityContextID(actorID, projectID, activityTestBinding().Period))
}

type queryActivityTestBindingReader struct {
	advanceClock func()
}

func (r queryActivityTestBindingReader) ReadCurrentBusinessActivityBinding(ctx context.Context, tx dal.ReadTransaction, actorID, spaceID, projectID string, at time.Time) (BusinessActivityBinding, error) {
	if tx == nil {
		return BusinessActivityBinding{}, ErrQueryActivityUnauthorized
	}
	var grant queryActivityGrantRecord
	key := queryActivityGrantKey(actorID, spaceID, projectID)
	if err := tx.Get(ctx, record.NewRecordWithData(key, &grant)); err != nil || !grant.Active || grant.ActorID != actorID || grant.SpaceID != spaceID || grant.ProjectID != projectID || !at.Before(grant.Binding.PaidUntilUTC) {
		return BusinessActivityBinding{}, ErrQueryActivityUnauthorized
	}
	if r.advanceClock != nil {
		r.advanceClock()
	}
	binding := grant.Binding
	binding.QueryUseAllowed = grant.QueryUseAllowed
	return binding, nil
}

// queryActivityTestPeriodAuthority is test-only billing-period setup authority.
// All real ledger admissions and late-inbox writes delegate to the production
// read-only QueryActivityUsageAuthority above.
type queryActivityTestPeriodAuthority struct{}

func (queryActivityTestPeriodAuthority) VerifyUsage(ctx context.Context, tx dal.ReadTransaction, operation contract4paymentus.UsageOperation) error {
	switch operation.Action {
	case contract4paymentus.UsageAdmit, contract4paymentus.UsageRecordLate:
		return NewQueryActivityUsageAuthority().VerifyUsage(ctx, tx, operation)
	case contract4paymentus.UsageOpen, contract4paymentus.UsageClose, contract4paymentus.UsageReadLate, contract4paymentus.UsageScanLate:
		if ctx.Value(queryActivityAuthorityContextKey{}) == true {
			return nil // Test-only period/inbox authority; production authority fails closed.
		}
	}
	return contract4paymentus.ErrUsageAuthority
}

type queryActivityFixture struct {
	t       *testing.T
	db      dal.DB
	ctx     context.Context
	now     time.Time
	period  contract4paymentus.UsagePeriodSnapshot
	service *QueryActivityService
	ledger  contract4paymentus.UsageLedger
}

func newQueryActivityFixture(t *testing.T) *queryActivityFixture {
	return newQueryActivityFixtureWindow(t, 30*24*time.Hour)
}

func newQueryActivityFixtureWindow(t *testing.T, window time.Duration) *queryActivityFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	start, end := now.Add(-time.Hour), now.Add(window)
	period := contract4paymentus.UsagePeriodSnapshot{
		Ref: contract4paymentus.UsagePeriodRef{Scope: contract4paymentus.UsageScope{
			Mode: contract4paymentus.ModeLive, SpaceID: "business-space", ProductID: "datatug-business-usage",
			PayerID: "business-space", ServiceID: "datatug",
		}, PeriodID: "period-2026-10"},
		StartUTC: start, EndUTC: end,
		Config: contract4paymentus.UsagePricingConfig{
			ProductID: "datatug-business-usage", ConfigID: "business-usage", Version: "1", Currency: money.CurrencyUSD,
			MonthlyBaseMinor: 9900, AnnualBaseMinor: 99000, IncludedMonthlyUnits: 7, MonthlyOverageUnitMinor: 2000,
			AnnualPrepaidUnitMinor: 19200, DiscountBase: true,
		},
	}
	db := sneatcoretesting.NewMemoryDB()
	ctx := context.WithValue(context.Background(), queryActivityAuthorityContextKey{}, true)
	ledger, err := subscriptions.NewDalgoUsageLedger(db, queryActivityTestPeriodAuthority{})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := subscriptions.NewDalgoUsageCorrectionInbox(db, queryActivityTestPeriodAuthority{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Open(ctx, period); err != nil {
		t.Fatalf("open real usage ledger: %v", err)
	}
	f := &queryActivityFixture{t: t, db: db, ctx: ctx, now: now, period: period, ledger: ledger}
	service, err := NewQueryActivityService(db, queryActivityTestBindingReader{}, ledger, inbox, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.service = service
	return f
}

func activityTestBinding() BusinessActivityBinding {
	return BusinessActivityBinding{
		Period: contract4paymentus.UsagePeriodRef{Scope: contract4paymentus.UsageScope{
			Mode: contract4paymentus.ModeLive, SpaceID: "business-space", ProductID: "datatug-business-usage",
			PayerID: "business-space", ServiceID: "datatug",
		}, PeriodID: "period-2026-10"},
		PeriodStartUTC:     time.Now().UTC().Truncate(time.Second).Add(-time.Hour),
		PeriodEndUTC:       time.Now().UTC().Truncate(time.Second).Add(30 * 24 * time.Hour),
		PaidUntilUTC:       time.Now().UTC().Truncate(time.Second).Add(30 * 24 * time.Hour),
		PaidBindingProofID: "paid-proof-1", QueryUseProofID: "query-use-proof-1", QueryUseAllowed: true,
	}
}

func (f *queryActivityFixture) seedGrant(actorID, projectID string, active, queryUse bool) {
	f.t.Helper()
	binding := f.binding()
	grant := queryActivityGrantRecord{ActorID: actorID, SpaceID: "business-space", ProjectID: projectID, Active: active, QueryUseAllowed: queryUse, Binding: binding}
	key := queryActivityGrantKey(actorID, "business-space", projectID)
	if err := f.db.RunReadwriteTransaction(f.ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, record.NewRecordWithData(key, &grant))
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *queryActivityFixture) binding() BusinessActivityBinding {
	b := activityTestBinding()
	b.PeriodStartUTC, b.PeriodEndUTC, b.PaidUntilUTC = f.period.StartUTC, f.period.EndUTC, f.period.EndUTC
	return b
}

func (f *queryActivityFixture) setGrant(actorID, projectID string, active, queryUse bool, paidProof string) {
	f.t.Helper()
	key := queryActivityGrantKey(actorID, "business-space", projectID)
	grant := queryActivityGrantRecord{ActorID: actorID, SpaceID: "business-space", ProjectID: projectID, Active: active, QueryUseAllowed: queryUse, Binding: f.binding()}
	grant.Binding.PaidBindingProofID = paidProof
	if err := f.db.RunReadwriteTransaction(f.ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, record.NewRecordWithData(key, &grant))
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *queryActivityFixture) issue(actorID, projectID string) QueryActivityContextResponse {
	f.t.Helper()
	f.seedGrant(actorID, projectID, true, true)
	got, err := f.service.IssueContext(f.ctx, actorID, "business-space", projectID)
	if err != nil {
		f.t.Fatalf("issue actor=%s project=%s: %v", actorID, projectID, err)
	}
	return got
}

func (f *queryActivityFixture) receipt(actorID string) (record.Record, *models4datatug.QueryActivityReceipt) {
	return models4datatug.NewQueryActivityReceiptRecord(actorID, f.period.Ref)
}

func TestQueryActivityCoalescesAcrossProjectsAndIsolatesActors(t *testing.T) {
	f := newQueryActivityFixture(t)
	first := f.issue("actor-one", "project-one")
	second := f.issue("actor-one", "project-two")
	if first.ContextID == second.ContextID {
		t.Fatal("project contexts shared an identity")
	}
	result, err := f.service.Report(f.ctx, "actor-one", "business-space", QueryActivityReport{ContextID: first.ContextID, OperationID: "op-one", Kind: models4datatug.QueryActivityEdit})
	if err != nil || !result.Accepted || result.Coalesced {
		t.Fatalf("first report = %+v, %v", result, err)
	}
	replay, err := f.service.Report(f.ctx, "actor-one", "business-space", QueryActivityReport{ContextID: first.ContextID, OperationID: "op-one", Kind: models4datatug.QueryActivityEdit})
	if err != nil || !replay.Accepted || replay.ReceiptID != result.ReceiptID {
		t.Fatalf("same operation replay = %+v, %v", replay, err)
	}
	rebound, err := f.service.Report(f.ctx, "actor-one", "business-space", QueryActivityReport{ContextID: first.ContextID, OperationID: "op-one", Kind: models4datatug.QueryActivityExecutionDispatched})
	if !errors.Is(err, ErrQueryActivityConflict) || rebound.Accepted {
		t.Fatalf("operation kind rebind = %+v, %v", rebound, err)
	}
	otherProject, err := f.service.Report(f.ctx, "actor-one", "business-space", QueryActivityReport{ContextID: second.ContextID, OperationID: "op-two", Kind: models4datatug.QueryActivityEdit})
	if err != nil || !otherProject.Coalesced || otherProject.ReceiptID != result.ReceiptID {
		t.Fatalf("same actor other project = %+v, %v", otherProject, err)
	}
	otherActor := f.issue("actor-two", "project-one")
	separate, err := f.service.Report(f.ctx, "actor-two", "business-space", QueryActivityReport{ContextID: otherActor.ContextID, OperationID: "op-one", Kind: models4datatug.QueryActivityEdit})
	if err != nil || !separate.Accepted || separate.ReceiptID == result.ReceiptID {
		t.Fatalf("other actor = %+v, %v", separate, err)
	}
	for _, actorID := range []string{"actor-one", "actor-two"} {
		r, receipt := f.receipt(actorID)
		if err := f.db.Get(f.ctx, r); err != nil || receipt.Validate() != nil {
			t.Fatalf("durable receipt for %s: %v / %+v", actorID, err, receipt)
		}
	}
	for n := 0; n < 8; n++ {
		actorID := fmt.Sprintf("user-%d", n)
		ctx := f.issue(actorID, "project-one")
		result, err := f.service.Report(f.ctx, actorID, "business-space", QueryActivityReport{ContextID: ctx.ContextID, OperationID: "same-operation", Kind: models4datatug.QueryActivityEdit})
		if err != nil || !result.Accepted {
			t.Fatalf("distinct user %s = %+v, %v", actorID, result, err)
		}
	}
}

func TestQueryActivityUsesCurrentQueryUseGrantAndStableContext(t *testing.T) {
	f := newQueryActivityFixture(t)
	f.seedGrant("viewer", "project-one", true, true)
	first, err := f.service.IssueContext(f.ctx, "viewer", "business-space", "project-one")
	if err != nil {
		t.Fatalf("query-use viewer context: %v", err)
	}
	f.setGrant("viewer", "project-one", true, false, "paid-proof-1")
	if _, err := f.service.IssueContext(f.ctx, "viewer", "business-space", "project-one"); !errors.Is(err, ErrQueryActivityUnauthorized) {
		t.Fatalf("metadata-only viewer admitted: %v", err)
	}
	f.setGrant("viewer", "project-one", true, true, "paid-proof-2")
	second, err := f.service.IssueContext(f.ctx, "viewer", "business-space", "project-one")
	if err != nil || second.ContextID != first.ContextID {
		t.Fatalf("authority revision created a new context row: %+v / %+v / %v", first, second, err)
	}
	contextRecord, stored := models4datatug.NewQueryActivityContextRecord("business-space", first.ContextID)
	if err := f.db.Get(f.ctx, contextRecord); err != nil || stored.PaidBindingProofID != "paid-proof-2" {
		t.Fatalf("current authority snapshot did not refresh in place: %+v, %v", stored, err)
	}
}

func TestQueryActivityRejectsActorTamperAndReaderClockCrossing(t *testing.T) {
	f := newQueryActivityFixture(t)
	context := f.issue("actor-one", "project-one")
	if _, err := f.service.Report(f.ctx, "actor-two", "business-space", QueryActivityReport{ContextID: context.ContextID, OperationID: "op-one", Kind: models4datatug.QueryActivityEdit}); !errors.Is(err, ErrQueryActivityUnauthorized) {
		t.Fatalf("actor tamper accepted: %v", err)
	}
	f2 := newQueryActivityFixture(t)
	f2.seedGrant("actor-one", "project-one", true, true)
	clock := f2.now
	reader := queryActivityTestBindingReader{advanceClock: func() { f2.now = f2.period.EndUTC }}
	inbox, _ := subscriptions.NewDalgoUsageCorrectionInbox(f2.db, queryActivityTestPeriodAuthority{})
	f2.service, _ = NewQueryActivityService(f2.db, reader, f2.ledger, inbox, func() time.Time { return f2.now })
	if _, err := f2.service.IssueContext(f2.ctx, "actor-one", "business-space", "project-one"); !errors.Is(err, ErrQueryActivityUnauthorized) {
		t.Fatalf("context accepted across paid-window end: %v", err)
	}
	f2.now = clock
	r, _ := f2.receipt("actor-one")
	if err := f2.db.Get(f2.ctx, r); !record.IsNotFound(err) {
		t.Fatalf("cross-boundary operation left a receipt: %v", err)
	}
}

func TestQueryActivityAcceptedReceiptDeliversAfterRevocation(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("actor-one", "project-one")
	accepted, err := f.service.Report(f.ctx, "actor-one", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "accepted-op", Kind: models4datatug.QueryActivityExecutionDispatched})
	if err != nil || !accepted.Accepted {
		t.Fatalf("report: %+v, %v", accepted, err)
	}
	f.setGrant("actor-one", "project-one", false, false, "paid-proof-1")
	if err := f.service.Deliver(f.ctx, "business-space", accepted.ReceiptID); err != nil {
		t.Fatalf("accepted receipt failed delivery after membership revocation: %v", err)
	}
	pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("business-space", accepted.ReceiptID)
	if err := f.db.Get(f.ctx, pendingRecord); err != nil || pending.Validate() != nil || pending.DeliveryState != models4datatug.QueryActivityPendingStateDelivered {
		t.Fatalf("delivery state %+v, %v", pending, err)
	}
}

func TestQueryActivityContextRequestsReuseRowsAndRateLimit(t *testing.T) {
	f := newQueryActivityFixture(t)
	f.seedGrant("actor-one", "project-one", true, true)
	var contextID string
	for n := 0; n < models4datatug.QueryActivityMaxContextsPerMinute; n++ {
		got, err := f.service.IssueContext(f.ctx, "actor-one", "business-space", "project-one")
		if err != nil {
			t.Fatalf("context request %d: %v", n, err)
		}
		if contextID == "" {
			contextID = got.ContextID
		} else if got.ContextID != contextID {
			t.Fatalf("request %d created another context: %s != %s", n, got.ContextID, contextID)
		}
	}
	if _, err := f.service.IssueContext(f.ctx, "actor-one", "business-space", "project-one"); !errors.Is(err, ErrQueryActivityRateLimited) {
		t.Fatalf("context request over bound: %v", err)
	}
	contextRecord, stored := models4datatug.NewQueryActivityContextRecord("business-space", contextID)
	if err := f.db.Get(f.ctx, contextRecord); err != nil || stored.Validate() != nil {
		t.Fatalf("reusable context missing: %+v / %v", stored, err)
	}
}

func TestQueryActivityReportRateLimitDoesNotCreateExtraReceipts(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("actor-one", "project-one")
	for n := 0; n < models4datatug.QueryActivityMaxReportsPerMinute; n++ {
		op := fmt.Sprintf("operation-%d", n)
		result, err := f.service.Report(f.ctx, "actor-one", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: op, Kind: models4datatug.QueryActivityEdit})
		if err != nil {
			t.Fatalf("report %d: %v", n, err)
		}
		if n == 0 && !result.Accepted || n > 0 && !result.Coalesced {
			t.Fatalf("report %d unexpectedly admitted another receipt: %+v", n, result)
		}
	}
	if _, err := f.service.Report(f.ctx, "actor-one", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "overflow", Kind: models4datatug.QueryActivityEdit}); !errors.Is(err, ErrQueryActivityRateLimited) {
		t.Fatalf("report above per-minute bound: %v", err)
	}
	receiptRecord, receipt := f.receipt("actor-one")
	if err := f.db.Get(f.ctx, receiptRecord); err != nil || receipt.Validate() != nil || receipt.OperationID != "operation-0" {
		t.Fatalf("report flood changed the one first receipt: %+v / %v", receipt, err)
	}
}

type queryActivityCrashAfterLedgerCommit struct {
	contract4paymentus.UsageLedger
	fail bool
}

func (l *queryActivityCrashAfterLedgerCommit) Admit(ctx context.Context, activity contract4paymentus.UsageActivity) (contract4paymentus.UsageAdmission, error) {
	result, err := l.UsageLedger.Admit(ctx, activity)
	if err == nil && l.fail {
		l.fail = false
		return result, errors.New("simulated worker crash after ledger commit")
	}
	return result, err
}

func TestQueryActivityReplaysAfterLedgerCommitAndRoutesLateReceipt(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("actor-one", "project-one")
	accepted, err := f.service.Report(f.ctx, "actor-one", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "crash-op", Kind: models4datatug.QueryActivityEdit})
	if err != nil {
		t.Fatal(err)
	}
	f.service.ledger = &queryActivityCrashAfterLedgerCommit{UsageLedger: f.ledger, fail: true}
	if err := f.service.Deliver(f.ctx, "business-space", accepted.ReceiptID); err == nil {
		t.Fatal("simulated post-commit crash was hidden")
	}
	if err := f.service.Deliver(f.ctx, "business-space", accepted.ReceiptID); err != nil {
		t.Fatalf("ledger replay did not finish outbox: %v", err)
	}
	pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("business-space", accepted.ReceiptID)
	if err := f.db.Get(f.ctx, pendingRecord); err != nil || pending.Attempts != 2 || pending.DeliveryState != models4datatug.QueryActivityPendingStateDelivered {
		t.Fatalf("crash replay state %+v / %v", pending, err)
	}

	f2 := newQueryActivityFixtureWindow(t, 350*time.Millisecond)
	activityContext = f2.issue("actor-one", "project-one")
	accepted, err = f2.service.Report(f2.ctx, "actor-one", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "late-op", Kind: models4datatug.QueryActivityEdit})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(f2.period.EndUTC) + 20*time.Millisecond)
	closeRequest := contract4paymentus.UsageCloseRequest{Ref: f2.period.Ref, CloseID: "close-1", Capacity: contract4paymentus.UsageCapacityEvidence{Revision: "basis-1", Digest: strings.Repeat("c", 64)}, BaseEvent: contract4paymentus.UsageBaseMonthly, BillMonthlyOverage: true}
	if _, err := f2.ledger.Close(f2.ctx, closeRequest); err != nil {
		t.Fatalf("close period: %v", err)
	}
	if err := f2.service.Deliver(f2.ctx, "business-space", accepted.ReceiptID); err != nil {
		t.Fatalf("closed-period delivery did not retain late receipt: %v", err)
	}
	receiptRecord, receipt := f2.receipt("actor-one")
	if err := f2.db.Get(f2.ctx, receiptRecord); err != nil || receipt.Validate() != nil {
		t.Fatalf("late receipt source missing: %+v, %v", receipt, err)
	}
	late, found, err := f2.service.corrections.Get(f2.ctx, contract4paymentus.UsageEventRef{Ref: f2.period.Ref, SourceID: receipt.Activity.SourceID, EventID: receipt.Activity.EventID})
	if err != nil || !found || late.Activity != receipt.Activity {
		t.Fatalf("late evidence missing: found=%v evidence=%+v err=%v", found, late, err)
	}
}

func TestQueryActivityEvidenceHasNoClientContent(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("actor-one", "project-one")
	result, err := f.service.Report(f.ctx, "actor-one", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: fmt.Sprintf("op-%d", 1), Kind: models4datatug.QueryActivityEdit})
	if err != nil {
		t.Fatal(err)
	}
	receiptRecord, receipt := f.receipt("actor-one")
	if err := f.db.Get(f.ctx, receiptRecord); err != nil {
		t.Fatal(err)
	}
	if receipt.Validate() != nil || receipt.Activity.EventID == "" || receipt.StructuralDigest == "" || result.ReceiptID != receipt.ReceiptID {
		t.Fatalf("invalid structural evidence: %+v", receipt)
	}
	if strings.Contains(receipt.StructuralDigest, "query text") || receipt.Activity.EvidenceDigest != receipt.StructuralDigest {
		t.Fatal("evidence contains query text or mismatched digest")
	}
}
