package models4datatug

import (
	"strings"
	"testing"
	"time"

	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

func TestBusinessUsageWorkerStateRecordsUseOnlyCursorInfrastructureAndSpaceScope(t *testing.T) {
	stateRecord, state := NewBusinessUsageWorkerStateRecord(contract4paymentus.ModeLive)
	if state == nil || stateRecord.Key().String() != businessUsageWorkerStateCollection+"/business-usage-lifecycle-live" {
		t.Fatalf("worker cursor record key/data = %v / %p", stateRecord.Key(), state)
	}
	testStateRecord, _ := NewBusinessUsageWorkerStateRecord(contract4paymentus.ModeTest)
	if testStateRecord.Key().String() == stateRecord.Key().String() {
		t.Fatal("TEST and LIVE lifecycle cursors share a key")
	}
	drainRecord, drain := NewQueryActivityDrainCursorRecord(contract4paymentus.ModeLive, "space-a")
	want := "spaces/space-a/ext/datatug/" + queryActivityDrainCursorsCollection + "/business-usage-live"
	if drain == nil || drainRecord.Key().String() != want {
		t.Fatalf("per-Space drain cursor key/data = %v / %p, want %q", drainRecord.Key(), drain, want)
	}
}

func TestBusinessUsageWorkerCursorModelsFailClosed(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	valid := BusinessUsageWorkerState{Version: 1, Mode: contract4paymentus.ModeLive, UpdatedAtUTC: now}
	if err := valid.Validate(); err != nil {
		t.Fatalf("empty initial worker state rejected: %v", err)
	}
	valid.CheckpointAfterPath = "spaces/space-a/ext/datatug/" + QueryActivityPeriodCheckpointsCollection + "/checkpoint"
	valid.PendingAfterPath = "spaces/space-b/ext/datatug/" + QueryActivityPendingCollection + "/receipt"
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid full-path scan cursors rejected: %v", err)
	}
	wrongMode := valid
	wrongMode.Mode = "sandbox"
	if err := wrongMode.Validate(); err == nil {
		t.Fatal("worker cursor accepted an unconfigured environment")
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
	drain := QueryActivityDrainCursor{Version: 1, Mode: contract4paymentus.ModeLive, SpaceID: "space-a", AfterID: strings.Repeat("a", 64), UpdatedAtUTC: now}
	if err := drain.Validate(); err != nil {
		t.Fatalf("valid per-Space drain cursor rejected: %v", err)
	}
	wrongModeDrain := drain
	wrongModeDrain.Mode = "sandbox"
	if err := wrongModeDrain.Validate(); err == nil {
		t.Fatal("Space drain cursor accepted an unconfigured environment")
	}
	drain.AfterID = "receipt-id"
	if err := drain.Validate(); err == nil {
		t.Fatal("per-Space drain cursor accepted a non-digest leaf cursor")
	}
}
