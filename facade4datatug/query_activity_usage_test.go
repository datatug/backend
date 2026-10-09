package facade4datatug

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
	"github.com/sneat-co/paymentus/backend/subscriptions"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type queryActivityReadFaultTx struct {
	dal.ReadTransaction
	err error
}

func (tx queryActivityReadFaultTx) Get(context.Context, record.Record) error { return tx.err }

func TestQueryActivityUsageAuthorityVerifiesAcceptedReceiptInSuppliedTransaction(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("actor-one", "project-one")
	accepted, err := f.service.Report(f.ctx, "actor-one", "business-space", QueryActivityReport{
		ContextID: activityContext.ContextID, OperationID: "authority-op", Kind: models4datatug.QueryActivityExecutionDispatched,
	})
	if err != nil || !accepted.Accepted {
		t.Fatalf("accepted receipt: %+v, %v", accepted, err)
	}
	receiptRecord, receipt := f.receipt("actor-one")
	if err := f.db.Get(f.ctx, receiptRecord); err != nil || receipt.Validate() != nil {
		t.Fatalf("read valid receipt: %+v, %v", receipt, err)
	}
	base := contract4paymentus.UsageOperation{Action: contract4paymentus.UsageAdmit, Period: f.period, Activity: receipt.Activity, ObservedAtUTC: f.now}
	authority := NewQueryActivityUsageAuthority()
	verify := func(operation contract4paymentus.UsageOperation) error {
		return f.db.RunReadwriteTransaction(f.ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			return authority.VerifyUsage(ctx, tx, operation)
		})
	}
	if err := verify(base); err != nil {
		t.Fatalf("production receipt authority rejected exact accepted operation: %v", err)
	}

	tampered := []struct {
		name string
		edit func(*contract4paymentus.UsageOperation)
	}{
		{"source", func(op *contract4paymentus.UsageOperation) { op.Activity.SourceID += "-forged" }},
		{"actor", func(op *contract4paymentus.UsageOperation) { op.Activity.UserID = "actor-two" }},
		{"payer scope", func(op *contract4paymentus.UsageOperation) {
			op.Activity.Ref.Scope.PayerID = "other-payer"
			op.Period.Ref = op.Activity.Ref
		}},
		{"accepted time", func(op *contract4paymentus.UsageOperation) {
			op.Activity.OccurredAtUTC = op.Activity.OccurredAtUTC.Add(time.Second)
		}},
		{"evidence", func(op *contract4paymentus.UsageOperation) { op.Activity.EvidenceDigest += "forged" }},
		{"period start", func(op *contract4paymentus.UsageOperation) { op.Period.StartUTC = op.Period.StartUTC.Add(time.Second) }},
		{"period end", func(op *contract4paymentus.UsageOperation) { op.Period.EndUTC = op.Period.EndUTC.Add(-time.Second) }},
	}
	for _, test := range tampered {
		t.Run(test.name, func(t *testing.T) {
			operation := base
			test.edit(&operation)
			if err := verify(operation); !errors.Is(err, contract4paymentus.ErrUsageAuthority) {
				t.Fatalf("tampered operation accepted: %v", err)
			}
		})
	}

	for _, action := range []contract4paymentus.UsageAction{
		contract4paymentus.UsageOpen,
		contract4paymentus.UsageClose,
		contract4paymentus.UsageReadLate,
		contract4paymentus.UsageScanLate,
	} {
		t.Run(string(action)+" denied", func(t *testing.T) {
			operation := base
			operation.Action = action
			if err := verify(operation); !errors.Is(err, contract4paymentus.ErrUsageAuthority) {
				t.Fatalf("unsupported action %q accepted: %v", action, err)
			}
		})
	}
}

func TestQueryActivityUsageAuthorityDeniesMissingReceiptAndPreservesReadErrors(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("actor-one", "project-one")
	accepted, err := f.service.Report(f.ctx, "actor-one", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "read-error-op", Kind: models4datatug.QueryActivityEdit})
	if err != nil || !accepted.Accepted {
		t.Fatalf("accepted report: %+v / %v", accepted, err)
	}
	receiptRecord, receipt := f.receipt("actor-one")
	if err := f.db.Get(f.ctx, receiptRecord); err != nil {
		t.Fatal(err)
	}
	operation := contract4paymentus.UsageOperation{Action: contract4paymentus.UsageAdmit, Period: f.period, Activity: receipt.Activity, ObservedAtUTC: f.now}
	authority := NewQueryActivityUsageAuthority()
	missing := authority.VerifyUsage(f.ctx, queryActivityReadFaultTx{err: record.ErrRecordNotFound}, operation)
	if !errors.Is(missing, contract4paymentus.ErrUsageAuthority) {
		t.Fatalf("missing receipt did not deny: %v", missing)
	}
	for name, want := range map[string]error{
		"cancelled":   context.Canceled,
		"unavailable": status.Error(codes.Unavailable, "receipt store unavailable"),
		"aborted":     status.Error(codes.Aborted, "receipt transaction aborted"),
	} {
		t.Run(name, func(t *testing.T) {
			got := authority.VerifyUsage(f.ctx, queryActivityReadFaultTx{err: want}, operation)
			if got != want {
				t.Fatalf("operational error identity was replaced: got %v want %v", got, want)
			}
		})
	}
}

type queryActivityUnavailableReceiptAuthority struct{ err error }

func (a queryActivityUnavailableReceiptAuthority) VerifyUsage(ctx context.Context, tx dal.ReadTransaction, operation contract4paymentus.UsageOperation) error {
	if operation.Action == contract4paymentus.UsageAdmit || operation.Action == contract4paymentus.UsageRecordLate {
		return NewQueryActivityUsageAuthority().VerifyUsage(ctx, queryActivityReadFaultTx{ReadTransaction: tx, err: a.err}, operation)
	}
	return queryActivityTestPeriodAuthority{}.VerifyUsage(ctx, tx, operation)
}

func TestQueryActivityUsageReadFailureRetainsPendingWithoutAdmission(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("actor-one", "project-one")
	accepted, err := f.service.Report(f.ctx, "actor-one", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "unavailable-op", Kind: models4datatug.QueryActivityExecutionDispatched})
	if err != nil {
		t.Fatal(err)
	}
	readErr := status.Error(codes.Unavailable, "receipt read unavailable")
	ledger, err := subscriptions.NewDalgoUsageLedger(f.db, queryActivityUnavailableReceiptAuthority{err: readErr})
	if err != nil {
		t.Fatal(err)
	}
	f.service.ledger = ledger
	if err := f.service.Deliver(f.ctx, "business-space", accepted.ReceiptID); err != readErr {
		t.Fatalf("delivery did not preserve DAL cause: %v", err)
	}
	pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("business-space", accepted.ReceiptID)
	if err := f.db.Get(f.ctx, pendingRecord); err != nil || pending.Validate() != nil || pending.DeliveryState != models4datatug.QueryActivityPendingStatePending {
		t.Fatalf("failed delivery completed pending: %+v / %v", pending, err)
	}
	// Restoring the real receipt authority permits first admission, proving the
	// transient read failure made no ledger write.
	if admission, err := f.ledger.Admit(f.ctx, pending.Activity); err != nil || !admission.NewActiveUser || admission.Replay {
		t.Fatalf("first admission after read failure: %+v / %v", admission, err)
	}
}
