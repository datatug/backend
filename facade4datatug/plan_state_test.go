package facade4datatug

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestPlanStateForPersonalContractCases(t *testing.T) {
	data, err := os.ReadFile("../testdata/contract/state-table.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name    string          `json:"name"`
			Facts   PlanFacts       `json:"facts"`
			Outcome string          `json:"outcome"`
			Reason  string          `json:"reason"`
			Record  json.RawMessage `json:"record"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, tc := range fixture.Cases {
		if tc.Facts.AccountKind != "personal" {
			continue
		}
		count++
		t.Run(tc.Name, func(t *testing.T) {
			got := PlanStateFor(tc.Facts)
			if got.Outcome != tc.Outcome || got.Reason != tc.Reason {
				t.Fatalf("result = %+v, want outcome %s reason %s", got, tc.Outcome, tc.Reason)
			}
			if tc.Outcome != "write" {
				if got.Record != nil {
					t.Fatalf("non-write returned record %+v", got.Record)
				}
				return
			}
			gotJSON, err := json.Marshal(got.Record)
			if err != nil {
				t.Fatal(err)
			}
			var gotObject, wantObject any
			if err := json.Unmarshal(gotJSON, &gotObject); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(tc.Record, &wantObject); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotObject, wantObject) {
				t.Fatalf("record = %s, want %s", gotJSON, tc.Record)
			}
		})
	}
	if count != 30 {
		t.Fatalf("checked %d personal state cases, want 30", count)
	}
}

func TestPlanStateForDoesNotGrantUnknownTier(t *testing.T) {
	result := PlanStateFor(PlanFacts{AccountKind: "personal", Tier: "unknown", ProviderStatus: "active"})
	if result.Outcome != "refuse" || result.Reason != "unknown_tier" {
		t.Fatal(result)
	}
}

func TestPlanStateForImmediateFullRefund(t *testing.T) {
	for _, status := range []string{"active", "trialing", "past_due"} {
		t.Run(status, func(t *testing.T) {
			got := PlanStateFor(PlanFacts{AccountKind: "personal", Tier: "pro", ProviderStatus: status, LastInvoiceRefundedInFull: true})
			if got.Outcome != "write" || got.Record == nil || got.Record.Plan != "free" || got.Record.Status != "ended" || got.Record.EndedReason != "refunded" {
				t.Fatalf("full refund state = %+v", got)
			}
		})
	}
	for _, status := range []string{"active", "trialing", "past_due", "unpaid", "paused", "canceled"} {
		t.Run("latched "+status, func(t *testing.T) {
			got := PlanStateFor(PlanFacts{AccountKind: "personal", Tier: "pro", ProviderStatus: status, TerminalFullRefund: true})
			if got.Outcome != "write" || got.Record == nil || got.Record.Plan != "free" || got.Record.EndedReason != "refunded" {
				t.Fatal(got)
			}
		})
	}
	if got := PlanStateFor(PlanFacts{AccountKind: "personal", Tier: "pro", ProviderStatus: "active", LastInvoiceRefundedInFull: false}); got.Outcome != "write" || got.Record == nil || got.Record.Plan != "pro" {
		t.Fatal(got)
	}
}
