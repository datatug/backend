package models4datatug

import (
	"errors"
	"strings"
	"time"

	"github.com/dal-go/record"
)

const (
	businessUsageWorkerStateCollection  = "datatugBusinessUsageWorkerStates"
	queryActivityDrainCursorsCollection = "queryActivityDrainCursors"
)

var ErrInvalidBusinessUsageWorkerState = errors.New("invalid business usage worker state")

// BusinessUsageWorkerState stores only resumable collection-group scan
// positions. Business authority remains in each Space-scoped checkpoint.
type BusinessUsageWorkerState struct {
	Version             int       `firestore:"v"`
	CheckpointAfterPath string    `firestore:"checkpointAfterPath,omitempty"`
	PendingAfterPath    string    `firestore:"pendingAfterPath,omitempty"`
	UpdatedAtUTC        time.Time `firestore:"updatedAtUTC"`
}

func (s BusinessUsageWorkerState) Validate() error {
	if s.Version != 1 || !validActivityUTC(s.UpdatedAtUTC) ||
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

func NewBusinessUsageWorkerStateRecord() (record.Record, *BusinessUsageWorkerState) {
	value := new(BusinessUsageWorkerState)
	return record.NewRecordWithData(record.NewKeyWithID(businessUsageWorkerStateCollection, "business-usage-lifecycle"), value), value
}

// QueryActivityDrainCursor is Space-scoped operational state. It records a
// paginated scan position and never authorizes activity or affects billing.
type QueryActivityDrainCursor struct {
	Version      int       `firestore:"v"`
	SpaceID      string    `firestore:"spaceID"`
	AfterID      string    `firestore:"afterID,omitempty"`
	UpdatedAtUTC time.Time `firestore:"updatedAtUTC"`
}

func (c QueryActivityDrainCursor) Validate() error {
	if c.Version != 1 || ValidateSharedProjectIdentifier(c.SpaceID) != nil || !validActivityUTC(c.UpdatedAtUTC) ||
		(c.AfterID != "" && !validActivityDigest(c.AfterID)) {
		return ErrInvalidBusinessUsageWorkerState
	}
	return nil
}

func NewQueryActivityDrainCursorRecord(spaceID string) (record.Record, *QueryActivityDrainCursor) {
	value := new(QueryActivityDrainCursor)
	key := record.NewKeyWithParentAndID(sharedProjectExtensionKey(spaceID), queryActivityDrainCursorsCollection, "business-usage")
	return record.NewRecordWithData(key, value), value
}
