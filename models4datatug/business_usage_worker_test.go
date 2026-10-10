package models4datatug

import (
	"strings"
	"testing"
	"time"
)

func TestBusinessUsageWorkerStateRecordsUseOnlyCursorInfrastructureAndSpaceScope(t *testing.T) {
	stateRecord, state := NewBusinessUsageWorkerStateRecord()
	if state == nil || stateRecord.Key().String() != businessUsageWorkerStateCollection+"/business-usage-lifecycle" {
		t.Fatalf("worker cursor record key/data = %v / %p", stateRecord.Key(), state)
	}
	drainRecord, drain := NewQueryActivityDrainCursorRecord("space-a")
	want := "spaces/space-a/ext/datatug/" + queryActivityDrainCursorsCollection + "/business-usage"
	if drain == nil || drainRecord.Key().String() != want {
		t.Fatalf("per-Space drain cursor key/data = %v / %p, want %q", drainRecord.Key(), drain, want)
	}
}

func TestBusinessUsageWorkerCursorModelsFailClosed(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	valid := BusinessUsageWorkerState{Version: 1, UpdatedAtUTC: now}
	if err := valid.Validate(); err != nil {
		t.Fatalf("empty initial worker state rejected: %v", err)
	}
	valid.CheckpointAfterPath = "spaces/space-a/ext/datatug/" + QueryActivityPeriodCheckpointsCollection + "/checkpoint"
	valid.PendingAfterPath = "spaces/space-b/ext/datatug/" + QueryActivityPendingCollection + "/receipt"
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid full-path scan cursors rejected: %v", err)
	}
	valid.CheckpointAfterPath = "orgs/org-a/spaces/space-a/ext/datatug/" + QueryActivityPeriodCheckpointsCollection + "/checkpoint"
	if err := valid.Validate(); err != nil {
		t.Fatalf("safe foreign collection-group scan cursor rejected: %v", err)
	}
	for name, mutate := range map[string]func(*BusinessUsageWorkerState){
		"wrong collection": func(state *BusinessUsageWorkerState) {
			state.PendingAfterPath = "spaces/space-b/ext/datatug/other/receipt"
		},
		"truncated path": func(state *BusinessUsageWorkerState) { state.CheckpointAfterPath = "checkpoint" },
		"invalid space": func(state *BusinessUsageWorkerState) {
			state.PendingAfterPath = "spaces/../ext/datatug/" + QueryActivityPendingCollection + "/receipt"
		},
		"invalid clock": func(state *BusinessUsageWorkerState) { state.UpdatedAtUTC = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("malformed worker state was accepted: %+v", candidate)
			}
		})
	}
	drain := QueryActivityDrainCursor{Version: 1, SpaceID: "space-a", AfterID: strings.Repeat("a", 64), UpdatedAtUTC: now}
	if err := drain.Validate(); err != nil {
		t.Fatalf("valid per-Space drain cursor rejected: %v", err)
	}
	drain.AfterID = "receipt-id"
	if err := drain.Validate(); err == nil {
		t.Fatal("per-Space drain cursor accepted a non-digest leaf cursor")
	}
}
