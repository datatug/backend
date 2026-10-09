package facade4datatug

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

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
