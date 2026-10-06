package models4datatug

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestPlanKeysSeparateCurrentTestAndUTCUsage(t *testing.T) {
	if got := NewCurrentPlanKey("p1").String(); got != "spaces/p1/ext/datatug/plan/current" {
		t.Fatal(got)
	}
	if got := NewTestPlanKey("p1").String(); got != "spaces/p1/ext/datatug/plan/test" {
		t.Fatal(got)
	}
	if got := NewAIUsageKey("p1", "2026-03").String(); got != "spaces/p1/ext/datatug/aiUsage/2026-03" {
		t.Fatal(got)
	}
}

func TestPlanRecordProjectContributorsFixtureRoundTrip(t *testing.T) {
	data, err := os.ReadFile("../testdata/contract/plan-record-pro-project-contributors.json")
	if err != nil {
		t.Fatal(err)
	}
	var record PlanRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Limits == nil || record.Limits.ProjectContributors == nil || *record.Limits.ProjectContributors != 5 {
		t.Fatal(record)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	limits := wire["limits"].(map[string]any)
	if limits["projectContributors"] != float64(5) {
		t.Fatal(limits)
	}
}

func TestPlanRecordProtectedPairFixtureRoundTrip(t *testing.T) {
	data, err := os.ReadFile("../testdata/contract/plan-record-pro-protected.json")
	if err != nil {
		t.Fatal(err)
	}
	var record PlanRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Limits == nil || record.Limits.ProtectedProjects == nil || record.Limits.ProtectedProjectUsers == nil ||
		*record.Limits.ProtectedProjects != 5 || *record.Limits.ProtectedProjectUsers != 5 {
		t.Fatal(record)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	limits := wire["limits"].(map[string]any)
	if limits["protectedProjects"] != float64(5) || limits["protectedProjectUsers"] != float64(5) {
		t.Fatal(limits)
	}
	var legacy PlanRecord
	if err := json.Unmarshal([]byte(`{"v":1,"limits":{"contributors":1}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	legacyBytes, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(legacyBytes, []byte("protectedProjects")) || bytes.Contains(legacyBytes, []byte("protectedProjectUsers")) {
		t.Fatal(string(legacyBytes))
	}
}
