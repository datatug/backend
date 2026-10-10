package models4datatug

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/crediterra/money"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

func validQueryActivityPeriodCheckpoint() QueryActivityPeriodCheckpoint {
	anchor := time.Date(2026, 10, 1, 7, 0, 0, 123456000, time.UTC)
	scope := contract4paymentus.UsageScope{
		Mode: contract4paymentus.ModeLive, SpaceID: "space-a", ProductID: "datatug-business-usage", PayerID: "space-a", ServiceID: "datatug",
	}
	snapshot, err := contract4paymentus.UsagePeriodForAnchor(scope, contract4paymentus.DataTugBusinessUsagePricing(), anchor, anchor)
	if err != nil {
		panic(err)
	}
	period := snapshot.Ref
	return QueryActivityPeriodCheckpoint{
		Version: 1, Period: period,
		Snapshot:  snapshot,
		AnchorUTC: anchor, AnchorProofDigest: strings.Repeat("a", 64), State: QueryActivityCheckpointOpening,
		UpdatedAtUTC: anchor,
	}
}

func TestQueryActivityPeriodCheckpointAndSequenceFailClosed(t *testing.T) {
	checkpoint := validQueryActivityPeriodCheckpoint()
	if err := checkpoint.Validate(); err != nil {
		t.Fatalf("valid opening checkpoint rejected: %v", err)
	}
	for name, mutate := range map[string]func(*QueryActivityPeriodCheckpoint){
		"missing proof":     func(c *QueryActivityPeriodCheckpoint) { c.AnchorProofDigest = "" },
		"wrong scope":       func(c *QueryActivityPeriodCheckpoint) { c.Snapshot.Ref.Scope.PayerID = "other-space" },
		"unknown state":     func(c *QueryActivityPeriodCheckpoint) { c.State = "complete" },
		"negative accepted": func(c *QueryActivityPeriodCheckpoint) { c.AcceptedCount = -1 },
		"delivered past accepted": func(c *QueryActivityPeriodCheckpoint) {
			c.AcceptedCount, c.DeliveredThrough = 1, 2
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := checkpoint
			mutate(&bad)
			if err := bad.Validate(); err == nil {
				t.Fatal("invalid checkpoint accepted")
			}
		})
	}
	large := checkpoint
	large.AcceptedCount = math.MaxInt64
	if err := large.Validate(); err != nil {
		t.Fatalf("valid large count was capped: %v", err)
	}
	closing := checkpoint
	closing.State = QueryActivityCheckpointClosing
	closing.CloseRequest = contract4paymentus.UsageCloseRequest{
		Ref: checkpoint.Period, CloseID: "close-a",
		Capacity:  contract4paymentus.UsageCapacityEvidence{Revision: "empty-0", Digest: strings.Repeat("e", 64)},
		BaseEvent: contract4paymentus.UsageBaseNone, BillMonthlyOverage: true,
	}
	if err := closing.Validate(); err != nil {
		t.Fatalf("valid close fence rejected: %v", err)
	}
	closing.AcceptedCount = 1
	if err := closing.Validate(); err == nil {
		t.Fatal("close fence accepted an undelivered receipt")
	}
	closing.AcceptedCount = 0
	closing.CloseRequest.CloseID = ""
	if err := closing.Validate(); err == nil {
		t.Fatal("closing checkpoint without immutable request accepted")
	}
	openingWithCloseRequest := checkpoint
	openingWithCloseRequest.CloseRequest = closing.CloseRequest
	if err := openingWithCloseRequest.Validate(); err == nil {
		t.Fatal("opening checkpoint retained a close request before the close fence")
	}

	sequence := QueryActivityPeriodSequence{
		Version: 1, Period: checkpoint.Period, Sequence: 1, ReceiptID: strings.Repeat("b", 64),
		ActivityDigest: strings.Repeat("c", 64), DeliveryState: QueryActivitySequencePending,
	}
	if err := sequence.Validate(); err != nil {
		t.Fatalf("valid pending sequence rejected: %v", err)
	}
	badSequence := sequence
	badSequence.Sequence = 0
	if err := badSequence.Validate(); err == nil {
		t.Fatal("sequence zero was accepted")
	}
	sequence.DeliveryState = QueryActivitySequenceDelivered
	sequence.DeliveryProofDigest = strings.Repeat("d", 64)
	sequence.DeliveredAtUTC = checkpoint.UpdatedAtUTC
	if err := sequence.Validate(); err != nil {
		t.Fatalf("valid delivered sequence rejected: %v", err)
	}
	sequence.DeliveryProofDigest = ""
	if err := sequence.Validate(); err == nil {
		t.Fatal("delivered sequence without proof accepted")
	}
	sequence.DeliveryProofDigest = strings.Repeat("d", 64)
	sequence.DeliveredAtUTC = time.Time{}
	if err := sequence.Validate(); err == nil {
		t.Fatal("delivered sequence without a delivery timestamp accepted")
	}
	sequence.DeliveryState = QueryActivitySequencePending
	sequence.DeliveredAtUTC = checkpoint.UpdatedAtUTC
	if err := sequence.Validate(); err == nil {
		t.Fatal("pending sequence retained a delivery timestamp")
	}
	sequence.DeliveryState = "unknown"
	sequence.DeliveredAtUTC = time.Time{}
	if err := sequence.Validate(); err == nil {
		t.Fatal("sequence with an unknown delivery state accepted")
	}
	sequence.DeliveryProofDigest = strings.Repeat("d", 64)
	sequence.DeliveryState = QueryActivitySequenceDelivered
	sequence.DeliveredAtUTC = checkpoint.UpdatedAtUTC
	sequence.Sequence = math.MaxInt64
	if err := sequence.Validate(); err != nil {
		t.Fatalf("valid large sequence was capped: %v", err)
	}

	// Configs still pass the Paymentus pricing validator and must remain in EUR.
	checkpoint = validQueryActivityPeriodCheckpoint()
	checkpoint.Snapshot.Config.Currency = money.CurrencyGBP
	if err := checkpoint.Validate(); err == nil {
		t.Fatal("foreign pricing config accepted")
	}
}

func TestQueryActivityPeriodCheckpointAcceptsValidFrozenPricingRevision(t *testing.T) {
	checkpoint := validQueryActivityPeriodCheckpoint()
	checkpoint.Snapshot.Config.Version = "2026-11-01"
	checkpoint.Snapshot.Config.ConfigID = "datatug-business-usage-next"
	checkpoint.Snapshot.Config.MonthlyOverageUnitMinor += 100
	frozen, err := contract4paymentus.UsagePeriodForAnchor(checkpoint.Period.Scope, checkpoint.Snapshot.Config, checkpoint.AnchorUTC, checkpoint.Snapshot.StartUTC)
	if err != nil {
		t.Fatalf("derive frozen pricing revision: %v", err)
	}
	checkpoint.Snapshot, checkpoint.Period = frozen, frozen.Ref
	if err := checkpoint.Validate(); err != nil {
		t.Fatalf("historical checkpoint was revalidated against today's pricing: %v", err)
	}
}

func TestQueryActivityPeriodKeysAreSpaceBoundAndSequenceOrdered(t *testing.T) {
	checkpoint := validQueryActivityPeriodCheckpoint()
	checkpointRecord, _ := NewQueryActivityPeriodCheckpointRecord(checkpoint.Period)
	otherScope := checkpoint.Period
	otherScope.Scope.SpaceID = "space-b"
	otherScope.Scope.PayerID = "space-b"
	otherRecord, _ := NewQueryActivityPeriodCheckpointRecord(otherScope)
	if checkpointRecord.Key().String() == otherRecord.Key().String() ||
		!strings.Contains(checkpointRecord.Key().String(), "spaces/space-a") ||
		!strings.Contains(checkpointRecord.Key().String(), QueryActivityPeriodCheckpointsCollection) {
		t.Fatalf("period checkpoint keys are not distinct and Space-bounded: %s / %s", checkpointRecord.Key(), otherRecord.Key())
	}
	firstRecord, first := NewQueryActivityPeriodSequenceRecord(checkpoint.Period, 1)
	secondRecord, second := NewQueryActivityPeriodSequenceRecord(checkpoint.Period, 2)
	if firstRecord.Key().String() == secondRecord.Key().String() || first.Version != 0 || second.Sequence != 0 {
		t.Fatalf("sequence record constructor did not create independent empty values: %s / %s / %+v / %+v", firstRecord.Key(), secondRecord.Key(), first, second)
	}
	if got, want := NewQueryActivityPeriodID(checkpoint.Period), checkpoint.Period.PeriodID; got == "" || got == want {
		t.Fatalf("period storage ID did not use the scoped digest: %q", got)
	}
}
