package facade4datatug

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type queryActivityBindingReaderFunc func(context.Context, dal.ReadTransaction, string, string, string, time.Time) (BusinessActivityBinding, error)

func (f queryActivityBindingReaderFunc) ReadCurrentBusinessActivityBinding(ctx context.Context, tx dal.ReadTransaction, actorID, spaceID, projectID string, at time.Time) (BusinessActivityBinding, error) {
	return f(ctx, tx, actorID, spaceID, projectID, at)
}

func TestQueryActivityRequiresCanonicalCurrentPaidBinding(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*BusinessActivityBinding, time.Time)
	}{
		{"query-use denied", func(binding *BusinessActivityBinding, _ time.Time) { binding.QueryUseAllowed = false }},
		{"test billing mode", func(binding *BusinessActivityBinding, _ time.Time) {
			binding.Period.Scope.Mode = contract4paymentus.ModeTest
		}},
		{"wrong Space", func(binding *BusinessActivityBinding, _ time.Time) { binding.Period.Scope.SpaceID = "other-space" }},
		{"wrong product", func(binding *BusinessActivityBinding, _ time.Time) { binding.Period.Scope.ProductID = "other-product" }},
		{"wrong service", func(binding *BusinessActivityBinding, _ time.Time) { binding.Period.Scope.ServiceID = "other-service" }},
		{"invalid period start", func(binding *BusinessActivityBinding, _ time.Time) { binding.PeriodStartUTC = time.Time{} }},
		{"invalid period end", func(binding *BusinessActivityBinding, _ time.Time) { binding.PeriodEndUTC = time.Time{} }},
		{"invalid paid end", func(binding *BusinessActivityBinding, _ time.Time) { binding.PaidUntilUTC = time.Time{} }},
		{"end before start", func(binding *BusinessActivityBinding, _ time.Time) { binding.PeriodEndUTC = binding.PeriodStartUTC }},
		{"paid end before start", func(binding *BusinessActivityBinding, _ time.Time) { binding.PaidUntilUTC = binding.PeriodStartUTC }},
		{"not started", func(binding *BusinessActivityBinding, now time.Time) { binding.PeriodStartUTC = now.Add(time.Minute) }},
		{"period ended", func(binding *BusinessActivityBinding, now time.Time) { binding.PeriodEndUTC = now }},
		{"paid service ended", func(binding *BusinessActivityBinding, now time.Time) { binding.PaidUntilUTC = now }},
		{"missing paid proof", func(binding *BusinessActivityBinding, _ time.Time) { binding.PaidBindingProofID = "" }},
		{"missing query-use proof", func(binding *BusinessActivityBinding, _ time.Time) { binding.QueryUseProofID = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newQueryActivityFixture(t)
			f.service.binding = queryActivityBindingReaderFunc(func(_ context.Context, tx dal.ReadTransaction, actorID, spaceID, projectID string, at time.Time) (BusinessActivityBinding, error) {
				if tx == nil || actorID != "actor" || spaceID != "business-space" || projectID != "project" || at.Location() != time.UTC {
					t.Fatalf("binding read escaped supplied transaction or scope: tx=%T actor=%q space=%q project=%q at=%s", tx, actorID, spaceID, projectID, at)
				}
				binding := f.binding()
				test.mutate(&binding, at)
				return binding, nil
			})
			if _, err := f.service.IssueContext(f.ctx, "actor", "business-space", "project"); !errors.Is(err, ErrQueryActivityUnauthorized) {
				t.Fatalf("invalid financial authority was admitted: %v", err)
			}
			contextRecord, _ := models4datatug.NewQueryActivityContextRecord("business-space", models4datatug.NewQueryActivityContextID("actor", "project", f.period.Ref))
			if err := f.db.Get(f.ctx, contextRecord); !record.IsNotFound(err) {
				t.Fatalf("rejected authority wrote a context: %v", err)
			}
		})
	}
}

func TestNewQueryActivityServiceRequiresEveryProductionPort(t *testing.T) {
	f := newQueryActivityFixture(t)
	tests := []struct {
		name string
		new  func() (*QueryActivityService, error)
	}{
		{"database", func() (*QueryActivityService, error) {
			return NewQueryActivityService(contract4paymentus.ModeLive, nil, queryActivityTestBindingReader{}, f.ledger, f.service.corrections, func() time.Time { return f.now })
		}},
		{"binding reader", func() (*QueryActivityService, error) {
			return NewQueryActivityService(contract4paymentus.ModeLive, f.db, nil, f.ledger, f.service.corrections, func() time.Time { return f.now })
		}},
		{"usage ledger", func() (*QueryActivityService, error) {
			return NewQueryActivityService(contract4paymentus.ModeLive, f.db, queryActivityTestBindingReader{}, nil, f.service.corrections, func() time.Time { return f.now })
		}},
		{"correction inbox", func() (*QueryActivityService, error) {
			return NewQueryActivityService(contract4paymentus.ModeLive, f.db, queryActivityTestBindingReader{}, f.ledger, nil, func() time.Time { return f.now })
		}},
		{"clock", func() (*QueryActivityService, error) {
			return NewQueryActivityService(contract4paymentus.ModeLive, f.db, queryActivityTestBindingReader{}, f.ledger, f.service.corrections, nil)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if service, err := test.new(); service != nil || !errors.Is(err, ErrQueryActivityUnavailable) {
				t.Fatalf("incomplete service was constructed: service=%v err=%v", service, err)
			}
		})
	}
}

func TestQueryActivityReportRejectsMissingExpiredAndStaleContexts(t *testing.T) {
	f := newQueryActivityFixture(t)
	missing := QueryActivityReport{ContextID: "missing-context", OperationID: "operation", Kind: models4datatug.QueryActivityEdit}
	if _, err := f.service.Report(f.ctx, "actor", "business-space", missing); !errors.Is(err, ErrQueryActivityUnauthorized) {
		t.Fatalf("missing context accepted: %v", err)
	}

	activityContext := f.issue("actor", "project")
	f.setGrant("actor", "project", true, true, "revised-paid-proof")
	// A quiet refresh/renewal may replace volatile provider proof IDs; only a
	// changed frozen payer requires the accepted operation to be held.
	if _, err := f.service.Report(f.ctx, "actor", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "same-payer-renewal", Kind: models4datatug.QueryActivityEdit}); err != nil {
		t.Fatalf("same-payer renewal proof refresh was rejected: %v", err)
	}
	f.setGrantPayerDigest("actor", "project", strings.Repeat("b", 64))
	request := QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "stale-operation", Kind: models4datatug.QueryActivityEdit}
	if _, err := f.service.Report(f.ctx, "actor", "business-space", request); !errors.Is(err, ErrQueryActivityUnauthorized) {
		t.Fatalf("context with changed stable payer accepted: %v", err)
	}

	if _, err := f.service.IssueContext(f.ctx, "actor", "business-space", "project"); err != nil {
		t.Fatalf("refresh context to current authority: %v", err)
	}
	refreshedRecord, refreshed := models4datatug.NewQueryActivityContextRecord("business-space", activityContext.ContextID)
	if err := f.db.Get(f.ctx, refreshedRecord); err != nil {
		t.Fatal(err)
	}
	f.now = refreshed.ExpiresAtUTC
	if _, err := f.service.Report(f.ctx, "actor", "business-space", request); !errors.Is(err, ErrQueryActivityUnauthorized) {
		t.Fatalf("expired context accepted at its exclusive expiration boundary: %v", err)
	}
}

func TestQueryActivityReportRequiresReadyCheckpointInAcceptanceTransaction(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("actor", "project")
	checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(f.period.Ref)
	if err := f.db.Get(f.ctx, checkpointRecord); err != nil {
		t.Fatal(err)
	}
	checkpoint.State = models4datatug.QueryActivityCheckpointOpening
	if err := f.db.RunReadwriteTransaction(f.ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	request := QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "checkpoint-opening", Kind: models4datatug.QueryActivityEdit}
	if _, err := f.service.Report(f.ctx, "actor", "business-space", request); !errors.Is(err, ErrQueryActivityUnavailable) {
		t.Fatalf("report accepted before native period open completed: %v", err)
	}
	if err := f.db.Get(f.ctx, checkpointRecord); err != nil || checkpoint.AcceptedCount != 0 {
		t.Fatalf("rejected report changed checkpoint: %+v / %v", checkpoint, err)
	}
	checkpoint.State = models4datatug.QueryActivityCheckpointReady
	if err := f.db.RunReadwriteTransaction(f.ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	if result, err := f.service.Report(f.ctx, "actor", "business-space", request); err != nil || !result.Accepted {
		t.Fatalf("ready period did not accept report: %+v / %v", result, err)
	}
	if err := f.db.Get(f.ctx, checkpointRecord); err != nil || checkpoint.AcceptedCount != 1 {
		t.Fatalf("receipt and sequence were not fenced by checkpoint write: %+v / %v", checkpoint, err)
	}
}

func TestQueryActivityReportRejectsStoredOperationRebind(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("actor", "project")
	request := QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "operation", Kind: models4datatug.QueryActivityEdit}
	if result, err := f.service.Report(f.ctx, "actor", "business-space", request); err != nil || !result.Accepted {
		t.Fatalf("initial report: %+v / %v", result, err)
	}
	receiptRecord, receipt := f.receipt("actor")
	if err := f.db.RunReadwriteTransaction(f.ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := tx.Get(ctx, receiptRecord); err != nil {
			return err
		}
		receipt.Kind = models4datatug.QueryActivityExecutionDispatched
		receipt.StructuralDigest = models4datatug.QueryActivityStructuralDigest(*receipt)
		receipt.Activity.EvidenceDigest = receipt.StructuralDigest
		return tx.Set(ctx, receiptRecord)
	}); err != nil {
		t.Fatalf("stored operation-rebind fixture: %v", err)
	}
	if _, err := f.service.Report(f.ctx, "actor", "business-space", request); !errors.Is(err, ErrQueryActivityConflict) {
		t.Fatalf("same operation was rebound to a different action: %v", err)
	}
}

type queryActivityUnexpectedAdmissionLedger struct{ contract4paymentus.UsageLedger }

func (queryActivityUnexpectedAdmissionLedger) Admit(context.Context, contract4paymentus.UsageActivity) (contract4paymentus.UsageAdmission, error) {
	return contract4paymentus.UsageAdmission{}, errors.New("already-delivered receipt was readmitted")
}

func TestQueryActivityDeliveryIsIdempotentAndRejectsUnknownReceipt(t *testing.T) {
	f := newQueryActivityFixture(t)
	if err := f.service.Deliver(f.ctx, "business-space", strings.Repeat("a", 64)); !errors.Is(err, ErrQueryActivityInvalid) {
		t.Fatalf("unknown pending receipt did not fail as invalid: %v", err)
	}
	activityContext := f.issue("actor", "project")
	accepted, err := f.service.Report(f.ctx, "actor", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "delivery-op", Kind: models4datatug.QueryActivityEdit})
	if err != nil || !accepted.Accepted {
		t.Fatalf("report for delivery: %+v / %v", accepted, err)
	}
	if err := f.service.Deliver(f.ctx, "business-space", accepted.ReceiptID); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	f.service.ledger = queryActivityUnexpectedAdmissionLedger{UsageLedger: f.ledger}
	if err := f.service.Deliver(f.ctx, "business-space", accepted.ReceiptID); err != nil {
		t.Fatalf("delivered receipt was not an idempotent no-op: %v", err)
	}
}

func TestAcceptedQueryActivityDrainsAfterCurrentBusinessAccessIsRevoked(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("actor", "project")
	accepted, err := f.service.Report(f.ctx, "actor", "business-space", QueryActivityReport{
		ContextID: activityContext.ContextID, OperationID: "accepted-before-revocation", Kind: models4datatug.QueryActivityEdit,
	})
	if err != nil || !accepted.Accepted {
		t.Fatalf("accept activity before revocation: %+v / %v", accepted, err)
	}
	// Once Report commits the receipt, the bounded drain uses its immutable
	// receipt/period evidence. It must not require the Space to regain current
	// Business access before retrying delivery.
	f.setGrant("actor", "project", false, false, "revoked-paid-proof")
	f.now = f.period.EndUTC.Add(time.Minute)
	if err := f.service.Deliver(f.ctx, "business-space", accepted.ReceiptID); err != nil {
		t.Fatalf("deliver accepted receipt after access ended: %v", err)
	}
	pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("business-space", accepted.ReceiptID)
	if err := f.db.Get(f.ctx, pendingRecord); err != nil || pending.Validate() != nil || pending.DeliveryState != models4datatug.QueryActivityPendingStateDelivered {
		t.Fatalf("accepted receipt was not durably drained: %+v / %v", pending, err)
	}
}

func TestQueryActivityRequiresAValidServerClockBeforeDurableWrites(t *testing.T) {
	f := newQueryActivityFixture(t)
	f.seedGrant("actor", "project", true, true)
	validNow := f.now
	f.now = time.Time{}
	if _, err := f.service.IssueContext(f.ctx, "actor", "business-space", "project"); !errors.Is(err, ErrQueryActivityUnavailable) {
		t.Fatalf("context accepted an invalid server clock: %v", err)
	}
	f.now = validNow
	activityContext, err := f.service.IssueContext(f.ctx, "actor", "business-space", "project")
	if err != nil {
		t.Fatalf("valid context issue: %v", err)
	}
	f.now = time.Time{}
	request := QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "clock-check", Kind: models4datatug.QueryActivityEdit}
	if _, err := f.service.Report(f.ctx, "actor", "business-space", request); !errors.Is(err, ErrQueryActivityUnavailable) {
		t.Fatalf("report accepted an invalid server clock: %v", err)
	}
	f.now = validNow
	accepted, err := f.service.Report(f.ctx, "actor", "business-space", request)
	if err != nil || !accepted.Accepted {
		t.Fatalf("valid-clock report: %+v / %v", accepted, err)
	}
	f.now = time.Time{}
	if err := f.service.Deliver(f.ctx, "business-space", accepted.ReceiptID); !errors.Is(err, ErrQueryActivityUnavailable) {
		t.Fatalf("delivery accepted an invalid server clock: %v", err)
	}
	pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("business-space", accepted.ReceiptID)
	if err := f.db.Get(f.ctx, pendingRecord); err != nil || pending.Validate() != nil || pending.Attempts != 0 || pending.DeliveryState != models4datatug.QueryActivityPendingStatePending {
		t.Fatalf("invalid clock changed durable outbox or admitted usage: pending=%+v err=%v", pending, err)
	}
}

func TestQueryActivityOperationalBindingFailuresArePreserved(t *testing.T) {
	f := newQueryActivityFixture(t)
	upstreamErr := errors.New("temporary authority store failure")
	f.service.binding = queryActivityBindingReaderFunc(func(context.Context, dal.ReadTransaction, string, string, string, time.Time) (BusinessActivityBinding, error) {
		return BusinessActivityBinding{}, upstreamErr
	})
	if _, err := f.service.IssueContext(f.ctx, "actor", "business-space", "project"); !errors.Is(err, upstreamErr) {
		t.Fatalf("context issue hid binding reader failure: %v", err)
	}
	contextID := models4datatug.NewQueryActivityContextID("actor", "project", f.period.Ref)
	contextRecord, _ := models4datatug.NewQueryActivityContextRecord("business-space", contextID)
	if err := f.db.Get(f.ctx, contextRecord); !record.IsNotFound(err) {
		t.Fatalf("failed authority read left a context: %v", err)
	}

	f.service.binding = queryActivityTestBindingReader{}
	activityContext := f.issue("actor", "project")
	f.service.binding = queryActivityBindingReaderFunc(func(context.Context, dal.ReadTransaction, string, string, string, time.Time) (BusinessActivityBinding, error) {
		return BusinessActivityBinding{}, upstreamErr
	})
	request := QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "authority-outage", Kind: models4datatug.QueryActivityEdit}
	if _, err := f.service.Report(f.ctx, "actor", "business-space", request); !errors.Is(err, upstreamErr) {
		t.Fatalf("report hid binding reader failure: %v", err)
	}
	receiptRecord, _ := f.receipt("actor")
	if err := f.db.Get(f.ctx, receiptRecord); !record.IsNotFound(err) {
		t.Fatalf("failed authority read left a receipt: %v", err)
	}
}

func TestQueryActivityFailsClosedOnCorruptStoredAuthorityAndOutbox(t *testing.T) {
	t.Run("context identity", func(t *testing.T) {
		f := newQueryActivityFixture(t)
		activityContext := f.issue("actor", "project")
		contextRecord, stored := models4datatug.NewQueryActivityContextRecord("business-space", activityContext.ContextID)
		if err := f.db.RunReadwriteTransaction(f.ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			if err := tx.Get(ctx, contextRecord); err != nil {
				return err
			}
			stored.ProjectID = "other-project"
			return tx.Set(ctx, contextRecord)
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.IssueContext(f.ctx, "actor", "business-space", "project"); !errors.Is(err, ErrQueryActivityConflict) {
			t.Fatalf("context key rebound to another project: %v", err)
		}
	})

	t.Run("quota identity", func(t *testing.T) {
		f := newQueryActivityFixture(t)
		f.seedGrant("actor", "project", true, true)
		quotaRecord, quota := models4datatug.NewQueryActivityContextQuotaRecord("actor", f.period.Ref)
		*quota = models4datatug.QueryActivityContextQuota{
			Version: 1, ActorID: "another-actor", Period: f.period.Ref,
			ContextWindowStartUTC: f.now, ContextsInWindow: 1,
		}
		if err := f.db.RunReadwriteTransaction(f.ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			return tx.Insert(ctx, quotaRecord)
		}); err != nil {
			t.Fatal(err)
		}
		if err := quota.Validate(); err != nil {
			t.Fatalf("corrupt-identity test fixture is malformed: %v", err)
		}
		if _, err := f.service.IssueContext(f.ctx, "actor", "business-space", "project"); !errors.Is(err, ErrQueryActivityConflict) {
			t.Fatalf("quota key rebound to another actor: %v", err)
		}
	})

	t.Run("pending corruption", func(t *testing.T) {
		f := newQueryActivityFixture(t)
		activityContext := f.issue("actor", "project")
		accepted, err := f.service.Report(f.ctx, "actor", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "corrupt-pending", Kind: models4datatug.QueryActivityEdit})
		if err != nil || !accepted.Accepted {
			t.Fatalf("initial receipt: %+v / %v", accepted, err)
		}
		pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("business-space", accepted.ReceiptID)
		if err := f.db.RunReadwriteTransaction(f.ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			if err := tx.Get(ctx, pendingRecord); err != nil {
				return err
			}
			pending.Activity.EventID = "different-valid-event"
			return tx.Set(ctx, pendingRecord)
		}); err != nil {
			t.Fatal(err)
		}
		if err := pending.Validate(); err != nil {
			t.Fatalf("mismatched receipt test fixture is not a valid pending row: %v", err)
		}
		if err := f.service.Deliver(f.ctx, "business-space", accepted.ReceiptID); !errors.Is(err, ErrQueryActivityConflict) {
			t.Fatalf("pending row no longer matching its accepted receipt was delivered: %v", err)
		}
	})
}

func TestQueryActivityInputValidationFailsBeforeStorage(t *testing.T) {
	f := newQueryActivityFixture(t)
	if _, err := f.service.IssueContext(f.ctx, "", "business-space", "project"); !errors.Is(err, ErrQueryActivityInvalid) {
		t.Fatalf("invalid actor was not rejected: %v", err)
	}
	if _, err := f.service.Report(f.ctx, "actor", "business-space", QueryActivityReport{ContextID: "", OperationID: "operation", Kind: models4datatug.QueryActivityEdit}); !errors.Is(err, ErrQueryActivityInvalid) {
		t.Fatalf("invalid report identity was not rejected: %v", err)
	}
	if err := f.service.Deliver(f.ctx, "business-space", "not-a-receipt-id"); !errors.Is(err, ErrQueryActivityInvalid) {
		t.Fatalf("invalid delivery identity was not rejected: %v", err)
	}
}

func TestQueryActivityContextUsesEarlierExclusivePeriodFence(t *testing.T) {
	f := newQueryActivityFixture(t)
	f.service.binding = queryActivityBindingReaderFunc(func(_ context.Context, _ dal.ReadTransaction, _, _, _ string, _ time.Time) (BusinessActivityBinding, error) {
		binding := f.binding()
		binding.PeriodEndUTC = f.now.Add(2 * time.Hour)
		binding.PaidUntilUTC = f.now.Add(4 * time.Hour)
		return binding, nil
	})
	issued, err := f.service.IssueContext(f.ctx, "actor", "business-space", "project")
	if err != nil {
		t.Fatalf("issue context with shorter metering period: %v", err)
	}
	if !issued.ExpiresAtUTC.Equal(f.now.Add(2 * time.Hour)) {
		t.Fatalf("context expiry = %s, want earlier period fence %s", issued.ExpiresAtUTC, f.now.Add(2*time.Hour))
	}
}

func TestQueryActivityDrainCursorAndCancellationSignals(t *testing.T) {
	f := newQueryActivityFixture(t)
	for _, cursor := range []string{
		"short",
		strings.Repeat("A", 64),
		strings.Repeat("g", 64),
	} {
		if _, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 1, AfterID: cursor}); !errors.Is(err, ErrQueryActivityInvalid) {
			t.Errorf("invalid continuation cursor %q accepted: %v", cursor, err)
		}
	}
	for _, test := range []struct {
		name string
		err  error
	}{
		{"context canceled", context.Canceled},
		{"deadline exceeded", context.DeadlineExceeded},
		{"gRPC canceled", status.Error(codes.Canceled, "transaction canceled")},
		{"gRPC deadline", status.Error(codes.DeadlineExceeded, "transaction deadline")},
		{"ordinary delivery error", errors.New("temporary unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := activityDrainCancellation(context.Background(), test.err)
			if test.name == "ordinary delivery error" {
				if got != nil {
					t.Fatalf("ordinary failure treated as cancellation: %v", got)
				}
			} else if !errors.Is(got, test.err) {
				t.Fatalf("cancellation signal lost: %v", got)
			}
		})
	}
}

func TestQueryActivityDeliveryRejectsUnbackedPendingRows(t *testing.T) {
	t.Run("missing accepted receipt", func(t *testing.T) {
		f := newQueryActivityFixture(t)
		activityContext := f.issue("orphaned-actor", "orphaned-project")
		accepted, err := f.service.Report(f.ctx, "orphaned-actor", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "orphaned-operation", Kind: models4datatug.QueryActivityEdit})
		if err != nil || !accepted.Accepted {
			t.Fatalf("create accepted receipt: %+v / %v", accepted, err)
		}
		receiptRecord, _ := f.receipt("orphaned-actor")
		if err := f.db.RunReadwriteTransaction(f.ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			return tx.Delete(ctx, receiptRecord.Key())
		}); err != nil {
			t.Fatal(err)
		}
		pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("business-space", accepted.ReceiptID)
		if err := f.db.Get(f.ctx, pendingRecord); err != nil {
			t.Fatal(err)
		}
		if err := f.service.Deliver(f.ctx, "business-space", accepted.ReceiptID); !record.IsNotFound(err) {
			t.Fatalf("pending row without its accepted receipt was delivered: %v", err)
		}
		if err := f.db.Get(f.ctx, pendingRecord); err != nil || pending.DeliveryState != models4datatug.QueryActivityPendingStatePending || pending.Attempts != 0 {
			t.Fatalf("unbacked pending row changed: %+v / %v", pending, err)
		}
	})

}
