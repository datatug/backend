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
			return NewQueryActivityService(nil, queryActivityTestBindingReader{}, f.ledger, f.service.corrections, func() time.Time { return f.now })
		}},
		{"binding reader", func() (*QueryActivityService, error) {
			return NewQueryActivityService(f.db, nil, f.ledger, f.service.corrections, func() time.Time { return f.now })
		}},
		{"usage ledger", func() (*QueryActivityService, error) {
			return NewQueryActivityService(f.db, queryActivityTestBindingReader{}, nil, f.service.corrections, func() time.Time { return f.now })
		}},
		{"correction inbox", func() (*QueryActivityService, error) {
			return NewQueryActivityService(f.db, queryActivityTestBindingReader{}, f.ledger, nil, func() time.Time { return f.now })
		}},
		{"clock", func() (*QueryActivityService, error) {
			return NewQueryActivityService(f.db, queryActivityTestBindingReader{}, f.ledger, f.service.corrections, nil)
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
	request := QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "stale-operation", Kind: models4datatug.QueryActivityEdit}
	if _, err := f.service.Report(f.ctx, "actor", "business-space", request); !errors.Is(err, ErrQueryActivityUnauthorized) {
		t.Fatalf("context with stale paid-binding proof accepted: %v", err)
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
