package facade4datatug

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

type queryActivityFailOnceLedger struct {
	contract4paymentus.UsageLedger
	targetEventID string
	mu            sync.Mutex
	failed        bool
}

type queryActivityCrashAfterAdmissionLedger struct {
	contract4paymentus.UsageLedger
	failed bool
}

type queryActivityCancelAtLedger struct {
	contract4paymentus.UsageLedger
	targetEventID string
	cancel        context.CancelFunc
}

func (l queryActivityCancelAtLedger) Admit(ctx context.Context, activity contract4paymentus.UsageActivity) (contract4paymentus.UsageAdmission, error) {
	if activity.EventID == l.targetEventID {
		l.cancel()
		return contract4paymentus.UsageAdmission{}, ctx.Err()
	}
	return l.UsageLedger.Admit(ctx, activity)
}

func (l *queryActivityCrashAfterAdmissionLedger) Admit(ctx context.Context, activity contract4paymentus.UsageActivity) (contract4paymentus.UsageAdmission, error) {
	result, err := l.UsageLedger.Admit(ctx, activity)
	if err == nil && !l.failed {
		l.failed = true
		return result, errors.New("simulated worker return loss after ledger commit")
	}
	return result, err
}

func (l *queryActivityFailOnceLedger) Admit(ctx context.Context, activity contract4paymentus.UsageActivity) (contract4paymentus.UsageAdmission, error) {
	l.mu.Lock()
	if activity.EventID == l.targetEventID && !l.failed {
		l.failed = true
		l.mu.Unlock()
		return contract4paymentus.UsageAdmission{}, errors.New("temporary ledger outage")
	}
	l.mu.Unlock()
	return l.UsageLedger.Admit(ctx, activity)
}

func TestQueryActivityDrainRecoversLostResponseAndAdvancesPastRetryableFailure(t *testing.T) {
	f := newQueryActivityFixture(t)
	var receiptIDs []string
	eventByReceiptID := make(map[string]string)
	for i := 0; i < 3; i++ {
		actorID := "drain-actor-" + string(rune('a'+i))
		projectID := "drain-project-" + string(rune('a'+i))
		activityContext := f.issue(actorID, projectID)
		result, err := f.service.Report(f.ctx, actorID, "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "lost-http-response", Kind: models4datatug.QueryActivityEdit})
		if err != nil || !result.Accepted {
			t.Fatalf("report for %s: %+v / %v", actorID, result, err)
		}
		receiptIDs = append(receiptIDs, result.ReceiptID)
		eventByReceiptID[result.ReceiptID] = models4datatug.NewQueryActivityEventID(activityContext.ContextID, "lost-http-response")
	}
	sort.Strings(receiptIDs)
	// The first page's earliest receipt fails once. Later work in that page and
	// the next page still progresses; the failed item remains discoverable from
	// the beginning on the following drain cycle.
	f.service.ledger = &queryActivityFailOnceLedger{UsageLedger: f.ledger, targetEventID: eventByReceiptID[receiptIDs[0]]}
	first, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 2})
	if err != nil || first.Scanned != 2 || first.Delivered != 1 || first.Failed != 1 || !first.HasMore || first.NextAfterID != receiptIDs[1] {
		t.Fatalf("first bounded page %+v: %v; sorted ids=%v", first, err, receiptIDs)
	}
	second, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 2, AfterID: first.NextAfterID})
	if err != nil || second.Scanned != 1 || second.Delivered != 1 || second.Failed != 0 || second.HasMore || second.NextAfterID != "" {
		t.Fatalf("forward page %+v: %v", second, err)
	}
	// A fresh service instance has no receipt ID or in-memory queue. Starting a
	// new scan discovers and retries the earlier failed item from durable state.
	restarted, err := NewQueryActivityService(f.db, queryActivityTestBindingReader{}, f.ledger, f.service.corrections, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	var recovered QueryActivityDrainResult
	totalRecovered, totalFailed, totalScanned := 0, 0, 0
	for attempts := 0; attempts < 4; attempts++ {
		recovered, err = restarted.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 2, AfterID: recovered.NextAfterID})
		if err != nil {
			t.Fatalf("restart recovery: %+v / %v", recovered, err)
		}
		totalRecovered += recovered.Delivered
		totalFailed += recovered.Failed
		totalScanned += recovered.Scanned
		if recovered.NextAfterID == "" {
			break
		}
	}
	if totalScanned != 3 || totalRecovered != 1 || totalFailed != 0 || recovered.NextAfterID != "" {
		t.Fatalf("restart recovery did not finish a full bounded pass: %+v (scanned=%d delivered=%d failed=%d)", recovered, totalScanned, totalRecovered, totalFailed)
	}
	for _, actorID := range []string{"drain-actor-a", "drain-actor-b", "drain-actor-c"} {
		r, pending := models4datatug.NewQueryActivityPendingRecord("business-space", models4datatug.NewQueryActivityReceiptID(actorID, f.period.Ref))
		if err := f.db.Get(f.ctx, r); err != nil || pending.Validate() != nil || pending.DeliveryState != models4datatug.QueryActivityPendingStateDelivered {
			t.Fatalf("pending work for %s: %+v / %v", actorID, pending, err)
		}
	}
}

func TestQueryActivityDrainIsSpaceBoundedAndConcurrentReplaySafe(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("drain-actor", "drain-project")
	if _, err := f.service.Report(f.ctx, "drain-actor", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "concurrent-drain", Kind: models4datatug.QueryActivityExecutionDispatched}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: QueryActivityMaxDrainBatchSize + 1}); !errors.Is(err, ErrQueryActivityInvalid) {
		t.Fatalf("unbounded request accepted: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 1})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent drain: %v", err)
		}
	}
	// Restart scanning is idempotent whether both workers saw the same page or
	// one transaction lost the delivery race.
	if _, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 1}); err != nil {
		t.Fatalf("concurrent replay: %v", err)
	}
	activityRecord, receipt := f.receipt("drain-actor")
	if err := f.db.Get(f.ctx, activityRecord); err != nil {
		t.Fatal(err)
	}
	admission, err := f.ledger.Admit(f.ctx, receipt.Activity)
	if err != nil || !admission.Replay {
		t.Fatalf("one replayable ledger event not retained: %+v / %v", admission, err)
	}
}

func TestQueryActivityDrainRecoversLostReturnAfterLedgerCommit(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("drain-crash-actor", "drain-crash-project")
	accepted, err := f.service.Report(f.ctx, "drain-crash-actor", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "lost-after-commit", Kind: models4datatug.QueryActivityEdit})
	if err != nil || !accepted.Accepted {
		t.Fatalf("report: %+v / %v", accepted, err)
	}
	f.service.ledger = &queryActivityCrashAfterAdmissionLedger{UsageLedger: f.ledger}
	first, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 10})
	if err != nil || first.Failed != 1 || first.Delivered != 0 {
		t.Fatalf("post-commit lost return was hidden: %+v / %v", first, err)
	}
	pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("business-space", accepted.ReceiptID)
	if err := f.db.Get(f.ctx, pendingRecord); err != nil || pending.Validate() != nil || pending.DeliveryState != models4datatug.QueryActivityPendingStatePending {
		t.Fatalf("lost return completed pending prematurely: %+v / %v", pending, err)
	}
	// A new worker has only durable Space storage, not the lost receipt ID. It
	// finds the same pending row and the real ledger returns an idempotent replay.
	restarted, err := NewQueryActivityService(f.db, queryActivityTestBindingReader{}, f.ledger, f.service.corrections, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 10})
	if err != nil || recovered.Delivered != 1 || recovered.Failed != 0 {
		t.Fatalf("post-commit restart recovery %+v / %v", recovered, err)
	}
	if err := f.db.Get(f.ctx, pendingRecord); err != nil || pending.Validate() != nil || pending.DeliveryState != models4datatug.QueryActivityPendingStateDelivered || pending.Attempts != 2 {
		t.Fatalf("replayed pending state %+v / %v", pending, err)
	}
	if admission, err := f.ledger.Admit(f.ctx, pending.Activity); err != nil || !admission.Replay {
		t.Fatalf("ledger did not retain one replayable event: %+v / %v", admission, err)
	}
}

func TestQueryActivityDrainReturnsCancellationWithResumeCursor(t *testing.T) {
	f := newQueryActivityFixture(t)
	type receiptEvent struct{ receiptID, eventID string }
	var receipts []receiptEvent
	for _, actorID := range []string{"cancel-a", "cancel-b"} {
		activityContext := f.issue(actorID, "cancel-project")
		accepted, err := f.service.Report(f.ctx, actorID, "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "cancel-report", Kind: models4datatug.QueryActivityEdit})
		if err != nil || !accepted.Accepted {
			t.Fatalf("report for %s: %+v / %v", actorID, accepted, err)
		}
		receipts = append(receipts, receiptEvent{receiptID: accepted.ReceiptID, eventID: models4datatug.NewQueryActivityEventID(activityContext.ContextID, "cancel-report")})
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].receiptID < receipts[j].receiptID })
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.service.ledger = queryActivityCancelAtLedger{UsageLedger: f.ledger, targetEventID: receipts[1].eventID, cancel: cancel}
	partial, err := f.service.Drain(ctx, "business-space", QueryActivityDrainRequest{Limit: 10})
	if !errors.Is(err, context.Canceled) || partial.Scanned != 2 || partial.Delivered != 1 || partial.Failed != 0 || partial.NextAfterID != receipts[0].receiptID || partial.HasMore {
		t.Fatalf("canceled drain lost its partial cursor: %+v / %v", partial, err)
	}
	// Resume exactly after the completed row; the canceled receipt was not
	// advanced over and remains pending for a fresh worker context.
	f.service.ledger = f.ledger
	resumed, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 10, AfterID: partial.NextAfterID})
	if err != nil || resumed.Scanned != 1 || resumed.Delivered != 1 || resumed.Failed != 0 || resumed.NextAfterID != "" {
		t.Fatalf("resume after cancellation: %+v / %v", resumed, err)
	}
	for _, receipt := range receipts {
		pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("business-space", receipt.receiptID)
		if err := f.db.Get(f.ctx, pendingRecord); err != nil || pending.Validate() != nil || pending.DeliveryState != models4datatug.QueryActivityPendingStateDelivered {
			t.Fatalf("receipt %s after resume: %+v / %v", receipt.receiptID, pending, err)
		}
	}
}
