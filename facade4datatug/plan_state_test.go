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
	if count != 29 {
		t.Fatalf("checked %d personal state cases, want 29", count)
	}
}

func TestPlanStateForDoesNotGrantUnknownTier(t *testing.T) {
	result := PlanStateFor(PlanFacts{AccountKind: "personal", Tier: "unknown", ProviderStatus: "active"})
	if result.Outcome != "refuse" || result.Reason != "unknown_tier" {
		t.Fatal(result)
	}
}
