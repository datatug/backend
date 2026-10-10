package models4datatug

import (
	"errors"
	"strings"
	"time"

	"github.com/dal-go/record"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

const (
	businessUsageWorkerStateCollection  = "datatugBusinessUsageWorkerStates"
	queryActivityDrainCursorsCollection = "queryActivityDrainCursors"
)

var ErrInvalidBusinessUsageWorkerState = errors.New("invalid business usage worker state")

// BusinessUsageWorkerState stores only resumable collection-group scan
// positions. Business authority remains in each Space-scoped checkpoint.
type BusinessUsageWorkerState struct {
	Version             int                     `firestore:"v"`
	Mode                contract4paymentus.Mode `firestore:"mode"`
	CheckpointAfterPath string                  `firestore:"checkpointAfterPath,omitempty"`
	PendingAfterPath    string                  `firestore:"pendingAfterPath,omitempty"`
	UpdatedAtUTC        time.Time               `firestore:"updatedAtUTC"`
}

func (s BusinessUsageWorkerState) Validate() error {
	if s.Version != 1 || !validActivityMode(s.Mode) || !validActivityUTC(s.UpdatedAtUTC) ||
		!validWorkerCollectionCursor(s.CheckpointAfterPath, QueryActivityPeriodCheckpointsCollection) ||
		!validWorkerCollectionCursor(s.PendingAfterPath, QueryActivityPendingCollection) {
		return ErrInvalidBusinessUsageWorkerState
	}
	return nil
}

func validWorkerCollectionCursor(value, collection string) bool {
	if value == "" {
		return true
	}
	parts := strings.Split(value, "/")
	if len(parts) < 2 || len(parts)%2 != 0 || parts[len(parts)-2] != collection {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func NewBusinessUsageWorkerStateRecord(mode contract4paymentus.Mode) (record.Record, *BusinessUsageWorkerState) {
	value := new(BusinessUsageWorkerState)
	value.Mode = mode
	return record.NewRecordWithData(record.NewKeyWithID(businessUsageWorkerStateCollection, "business-usage-lifecycle-"+string(mode)), value), value
}

// QueryActivityDrainCursor is Space-scoped operational state. It records a
// paginated scan position and never authorizes activity or affects billing.
type QueryActivityDrainCursor struct {
	Version      int                     `firestore:"v"`
	Mode         contract4paymentus.Mode `firestore:"mode"`
	SpaceID      string                  `firestore:"spaceID"`
	AfterID      string                  `firestore:"afterID,omitempty"`
	UpdatedAtUTC time.Time               `firestore:"updatedAtUTC"`
}

func (c QueryActivityDrainCursor) Validate() error {
	if c.Version != 1 || !validActivityMode(c.Mode) || ValidateSharedProjectIdentifier(c.SpaceID) != nil || !validActivityUTC(c.UpdatedAtUTC) ||
		(c.AfterID != "" && !validActivityDigest(c.AfterID)) {
		return ErrInvalidBusinessUsageWorkerState
	}
	return nil
}

func NewQueryActivityDrainCursorRecord(mode contract4paymentus.Mode, spaceID string) (record.Record, *QueryActivityDrainCursor) {
	value := new(QueryActivityDrainCursor)
	value.Mode, value.SpaceID = mode, spaceID
	key := record.NewKeyWithParentAndID(sharedProjectExtensionKey(spaceID), queryActivityDrainCursorsCollection, "business-usage-"+string(mode))
	return record.NewRecordWithData(key, value), value
}

func validActivityMode(mode contract4paymentus.Mode) bool {
	return mode == contract4paymentus.ModeTest || mode == contract4paymentus.ModeLive
}
