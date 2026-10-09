package models4datatug

import (
	"strings"
	"testing"
	"time"

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
