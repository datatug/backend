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
	start := time.Date(2026, 10, 1, 7, 0, 0, 123456000, time.UTC)
	end := time.Date(2026, 11, 1, 7, 0, 0, 123456000, time.UTC)
	period := contract4paymentus.UsagePeriodRef{Scope: contract4paymentus.UsageScope{
		Mode: contract4paymentus.ModeLive, SpaceID: "space-a", ProductID: "datatug-business-usage", PayerID: "space-a", ServiceID: "datatug",
	}, PeriodID: "period-a"}
	return QueryActivityPeriodCheckpoint{
		Version: 1, Period: period,
		Snapshot:  contract4paymentus.UsagePeriodSnapshot{Ref: period, StartUTC: start, EndUTC: end, Config: contract4paymentus.DataTugBusinessUsagePricing()},
		AnchorUTC: start, AnchorProofDigest: strings.Repeat("a", 64), State: QueryActivityCheckpointOpening,
		UpdatedAtUTC: start,
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

	sequence := QueryActivityPeriodSequence{
		Version: 1, Period: checkpoint.Period, Sequence: 1, ReceiptID: strings.Repeat("b", 64),
		ActivityDigest: strings.Repeat("c", 64), DeliveryState: QueryActivitySequencePending,
	}
	if err := sequence.Validate(); err != nil {
		t.Fatalf("valid pending sequence rejected: %v", err)
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
	sequence.Sequence = math.MaxInt64
	if err := sequence.Validate(); err != nil {
		t.Fatalf("valid large sequence was capped: %v", err)
	}

	// The public pricing snapshot remains the exact product-owned static config.
	checkpoint = validQueryActivityPeriodCheckpoint()
	checkpoint.Snapshot.Config.Currency = money.CurrencyGBP
	if err := checkpoint.Validate(); err == nil {
		t.Fatal("foreign pricing config accepted")
	}
}
