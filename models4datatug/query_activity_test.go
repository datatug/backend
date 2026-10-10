package models4datatug

import (
	"strings"
	"testing"
	"time"

	"github.com/dal-go/record"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

func validQueryActivityReceipt() QueryActivityReceipt {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	period := contract4paymentus.UsagePeriodRef{
		Scope: contract4paymentus.UsageScope{
			Mode: contract4paymentus.ModeLive, SpaceID: "space-a", ProductID: "datatug-business-usage",
			PayerID: "payer-a", ServiceID: "datatug",
		},
		PeriodID: "period-a",
	}
	r := QueryActivityReceipt{
		Version: 1, ContextID: "context-a", OperationID: "operation-a", Kind: QueryActivityEdit,
		ActorID: "actor-a", SpaceID: "space-a", ProjectID: "project-a", Period: period,
		PeriodStartUTC: at.Add(-time.Hour), PeriodEndUTC: at.Add(time.Hour), PaidUntilUTC: at.Add(time.Hour),
		PaidBindingProofID: "paid-proof-a", QueryUseProofID: "query-proof-a", AcceptedAtUTC: at,
	}
	r.BindingDigest = QueryActivityBindingDigest(r.ActorID, r.SpaceID, r.ProjectID, r.Period, r.PeriodStartUTC, r.PeriodEndUTC, r.PaidUntilUTC, r.PaidBindingProofID, r.QueryUseProofID)
	r.ReceiptID = NewQueryActivityReceiptID(r.ActorID, period)
	r.Activity = contract4paymentus.UsageActivity{
		Ref: period, SourceID: QueryActivitySourceID, EventID: NewQueryActivityEventID(r.ContextID, r.OperationID),
		UserID: r.ActorID, OccurredAtUTC: at,
	}
	r.StructuralDigest = QueryActivityStructuralDigest(r)
	r.Activity.EvidenceDigest = r.StructuralDigest
	return r
}

func TestQueryActivityReceiptIsScopedAndContentFree(t *testing.T) {
	r := validQueryActivityReceipt()
	if err := r.Validate(); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	if got, want := len(r.StructuralDigest), 64; got != want {
		t.Fatalf("structural digest length = %d, want %d", got, want)
	}
	other := r
	other.Period.Scope.PayerID = "payer-b"
	if NewQueryActivityReceiptID(r.ActorID, other.Period) == r.ReceiptID {
		t.Fatal("receipt identity crossed payer scope")
	}
	other = r
	other.ActorID = "actor-b"
	if NewQueryActivityReceiptID(other.ActorID, other.Period) == r.ReceiptID {
		t.Fatal("receipt identity crossed actor scope")
	}
}

func TestQueryActivityReceiptRejectsTamperAndUntrustedTime(t *testing.T) {
	for name, mutate := range map[string]func(*QueryActivityReceipt){
		"operation rebinding":      func(r *QueryActivityReceipt) { r.OperationID = "operation-b" },
		"actor rebinding":          func(r *QueryActivityReceipt) { r.ActorID = "actor-b" },
		"financial scope":          func(r *QueryActivityReceipt) { r.Period.Scope.PayerID = "payer-b" },
		"client time":              func(r *QueryActivityReceipt) { r.AcceptedAtUTC = r.AcceptedAtUTC.Add(time.Second) },
		"original paid interval":   func(r *QueryActivityReceipt) { r.PaidUntilUTC = r.PaidUntilUTC.Add(time.Second) },
		"original query-use proof": func(r *QueryActivityReceipt) { r.QueryUseProofID = "query-proof-b" },
		"missing digest":           func(r *QueryActivityReceipt) { r.Activity.EvidenceDigest = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := validQueryActivityReceipt()
			mutate(&r)
			if err := r.Validate(); err == nil {
				t.Fatal("tampered receipt validated")
			}
		})
	}
}

func TestQueryActivityDurableTimesUseMicrosecondPrecision(t *testing.T) {
	nanosecond := time.Date(2026, 10, 9, 12, 0, 0, 123456789, time.UTC)
	canonical := time.Date(2026, 10, 9, 12, 0, 0, 123456000, time.UTC)
	if got := CanonicalQueryActivityTime(nanosecond); !got.Equal(canonical) || got.Nanosecond() != 123456000 {
		t.Fatalf("canonical durable time = %s, want %s", got, canonical)
	}
	period := validQueryActivityReceipt().Period
	first := QueryActivityBindingDigest("actor-a", "space-a", "project-a", period, nanosecond, nanosecond.Add(time.Hour), nanosecond.Add(2*time.Hour), "paid-proof-a", "query-proof-a")
	second := QueryActivityBindingDigest("actor-a", "space-a", "project-a", period, canonical, canonical.Add(time.Hour), canonical.Add(2*time.Hour), "paid-proof-a", "query-proof-a")
	if first != second {
		t.Fatalf("binding digest varies below durable precision: %s != %s", first, second)
	}
	receipt := validQueryActivityReceipt()
	receipt.AcceptedAtUTC = nanosecond
	receipt.Activity.OccurredAtUTC = nanosecond
	if err := receipt.Validate(); err == nil {
		t.Fatal("receipt with a noncanonical durable timestamp validated")
	}
}

func TestQueryActivityContextAndQuotaBounds(t *testing.T) {
	period := validQueryActivityReceipt().Period
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	ctx := QueryActivityContext{
		Version: 1, ContextID: "context-a", ActorID: "actor-a", SpaceID: "space-a", ProjectID: "project-a",
		Period: period, PeriodStartUTC: at.Add(-time.Hour), PeriodEndUTC: at.Add(time.Hour), PaidUntilUTC: at.Add(time.Hour),
		BindingDigest: strings.Repeat("b", 64), PaidBindingProofID: "paid-a", QueryUseProofID: "use-a",
		IssuedAtUTC: at, ExpiresAtUTC: at.Add(time.Minute),
	}
	if err := ctx.Validate(); err != nil {
		t.Fatalf("valid context rejected: %v", err)
	}
	ctx.ExpiresAtUTC = ctx.PeriodEndUTC.Add(time.Second)
	if err := ctx.Validate(); err == nil {
		t.Fatal("context extending beyond paid period validated")
	}
	quota := QueryActivityContextQuota{Version: 1, ActorID: "actor-a", Period: period, ContextsInWindow: QueryActivityMaxContextsPerMinute, ContextWindowStartUTC: at}
	if err := quota.Validate(); err != nil {
		t.Fatalf("quota at bound rejected: %v", err)
	}
	quota.ContextsInWindow++
	if err := quota.Validate(); err == nil {
		t.Fatal("quota above bound validated")
	}
}

func TestQueryActivityPendingValidationAndPrivateRecordKeys(t *testing.T) {
	receipt := validQueryActivityReceipt()
	pending := QueryActivityPending{
		Version: 1, ReceiptID: receipt.ReceiptID, SpaceID: receipt.SpaceID, Period: receipt.Period,
		Activity: receipt.Activity, DeliveryState: QueryActivityPendingStatePending, Attempts: 1,
		UpdatedAtUTC: receipt.AcceptedAtUTC.Add(time.Second),
	}
	if err := pending.Validate(); err != nil {
		t.Fatalf("valid pending record rejected: %v", err)
	}
	for name, mutate := range map[string]func(*QueryActivityPending){
		"version":           func(p *QueryActivityPending) { p.Version++ },
		"receipt scope":     func(p *QueryActivityPending) { p.ReceiptID = "another-receipt" },
		"space scope":       func(p *QueryActivityPending) { p.SpaceID = "another-space" },
		"period scope":      func(p *QueryActivityPending) { p.Period.PeriodID = "another-period" },
		"activity source":   func(p *QueryActivityPending) { p.Activity.SourceID = "untrusted-source" },
		"activity event":    func(p *QueryActivityPending) { p.Activity.EventID = "" },
		"activity user":     func(p *QueryActivityPending) { p.Activity.UserID = "" },
		"activity evidence": func(p *QueryActivityPending) { p.Activity.EvidenceDigest = "not-a-digest" },
		"activity time":     func(p *QueryActivityPending) { p.Activity.OccurredAtUTC = time.Time{} },
		"delivery state":    func(p *QueryActivityPending) { p.DeliveryState = "unknown" },
		"negative attempts": func(p *QueryActivityPending) { p.Attempts = -1 },
		"updated time":      func(p *QueryActivityPending) { p.UpdatedAtUTC = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			value := pending
			mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("invalid pending record validated")
			}
		})
	}

	contextRecord, contextValue := NewQueryActivityContextRecord(receipt.SpaceID, receipt.ContextID)
	quotaRecord, quotaValue := NewQueryActivityContextQuotaRecord(receipt.ActorID, receipt.Period)
	receiptRecord, receiptValue := NewQueryActivityReceiptRecord(receipt.ActorID, receipt.Period)
	pendingRecord, pendingValue := NewQueryActivityPendingRecord(receipt.SpaceID, receipt.ReceiptID)
	if contextValue == nil || quotaValue == nil || receiptValue == nil || pendingValue == nil {
		t.Fatal("record constructor returned a nil DTO")
	}
	for name, item := range map[string]struct {
		record     interface{ Key() *record.Key }
		collection string
	}{
		"context": {contextRecord, QueryActivityContextsCollection},
		"quota":   {quotaRecord, QueryActivityContextQuotasCollection},
		"receipt": {receiptRecord, QueryActivityReceiptsCollection},
		"pending": {pendingRecord, QueryActivityPendingCollection},
	} {
		t.Run(name+" key", func(t *testing.T) {
			key := item.record.Key()
			if key.Collection() != item.collection || key.Parent() == nil || key.Parent().ID != "datatug" || key.Parent().Parent() == nil || key.Parent().Parent().ID != receipt.SpaceID {
				t.Fatalf("record key is not under the shared Space extension: %s", key)
			}
			if err := key.Validate(); err != nil {
				t.Fatalf("record key invalid: %v", err)
			}
		})
	}
	if NewQueryActivityContextID(receipt.ActorID, receipt.ProjectID, receipt.Period) != activityDigest(append([]string{"query-activity-context/1", receipt.ActorID, receipt.ProjectID}, periodKeyValues(receipt.Period)...)...) ||
		NewQueryActivityContextQuotaID(receipt.ActorID, receipt.Period) != quotaRecord.Key().ID ||
		NewQueryActivityReceiptID(receipt.ActorID, receipt.Period) != receiptRecord.Key().ID {
		t.Fatal("deterministic activity record IDs do not match their private record keys")
	}
}
