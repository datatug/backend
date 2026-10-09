package facade4datatug

import (
	"context"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

// QueryActivityUsageAuthority proves that Paymentus is admitting or retaining
// the exact activity backed by DataTug's immutable accepted receipt. It reads
// through the ledger or inbox caller's transaction; it has no database or
// mutable authority dependency of its own.
type QueryActivityUsageAuthority struct{}

var _ contract4paymentus.UsageAdmissionAuthority = QueryActivityUsageAuthority{}

// NewQueryActivityUsageAuthority constructs the receipt-only activity
// authority. UsageOpen, UsageClose and private late-evidence reads/scans need
// the separately composed period authority and are always denied here.
func NewQueryActivityUsageAuthority() QueryActivityUsageAuthority {
	return QueryActivityUsageAuthority{}
}

func (QueryActivityUsageAuthority) VerifyUsage(ctx context.Context, tx dal.ReadTransaction, operation contract4paymentus.UsageOperation) error {
	if tx == nil || (operation.Action != contract4paymentus.UsageAdmit && operation.Action != contract4paymentus.UsageRecordLate) {
		return contract4paymentus.ErrUsageAuthority
	}
	activity := operation.Activity
	if activity.UserID == "" || activity.Ref.PeriodID == "" || operation.Period.Ref != activity.Ref ||
		operation.Period.StartUTC.IsZero() || operation.Period.EndUTC.IsZero() ||
		operation.Period.StartUTC.Location() != time.UTC || operation.Period.EndUTC.Location() != time.UTC ||
		!operation.Period.EndUTC.After(operation.Period.StartUTC) || operation.Period.Config.ProductID != activity.Ref.Scope.ProductID {
		return contract4paymentus.ErrUsageAuthority
	}
	receiptRecord, receipt := models4datatug.NewQueryActivityReceiptRecord(activity.UserID, activity.Ref)
	if err := tx.Get(ctx, receiptRecord); err != nil {
		return contract4paymentus.ErrUsageAuthority
	}
	if receipt.Validate() != nil || receipt.Activity != activity || receipt.Period != operation.Period.Ref ||
		receipt.PeriodStartUTC != operation.Period.StartUTC || receipt.PeriodEndUTC != operation.Period.EndUTC {
		return contract4paymentus.ErrUsageAuthority
	}
	return nil
}
