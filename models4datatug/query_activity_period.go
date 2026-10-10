package models4datatug

import (
	"errors"
	"fmt"
	"time"

	"github.com/dal-go/record"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

const (
	QueryActivityPeriodCheckpointsCollection = "queryActivityPeriodCheckpoints"
	QueryActivityPeriodSequencesCollection   = "queryActivityPeriodSequences"
	QueryActivityCheckpointOpening           = "opening"
	QueryActivityCheckpointReady             = "ready"
	QueryActivityCheckpointClosing           = "closing"
	QueryActivityCheckpointClosed            = "closed"
	QueryActivitySequencePending             = "pending"
	QueryActivitySequenceDelivered           = "delivered"
)

var ErrInvalidQueryActivityPeriod = errors.New("invalid query activity period checkpoint")

// QueryActivityPeriodCheckpoint is the explicit completeness fence for one
// anchored Business usage period. A missing record is never interpreted as
// an empty period. The checkpoint is prepared before Paymentus.Open and becomes
// ready only after that ledger operation and a same-transaction read confirm
// the exact stored snapshot.
type QueryActivityPeriodCheckpoint struct {
	Version           int                                    `firestore:"v"`
	Period            contract4paymentus.UsagePeriodRef      `firestore:"period"`
	Snapshot          contract4paymentus.UsagePeriodSnapshot `firestore:"snapshot"`
	AnchorUTC         time.Time                              `firestore:"anchorUTC"`
	AnchorProofDigest string                                 `firestore:"anchorProofDigest"`
	State             string                                 `firestore:"state"`
	CloseRequest      contract4paymentus.UsageCloseRequest   `firestore:"closeRequest,omitempty"`
	AcceptedCount     int64                                  `firestore:"acceptedCount"`
	DeliveredThrough  int64                                  `firestore:"deliveredThrough"`
	UpdatedAtUTC      time.Time                              `firestore:"updatedAtUTC"`
}

func (c QueryActivityPeriodCheckpoint) Validate() error {
	if c.Version != 1 || !validActivityPeriod(c.Period, c.Period.Scope.SpaceID) || c.Snapshot.Ref != c.Period ||
		!validActivityUTC(c.Snapshot.StartUTC) || !validActivityUTC(c.Snapshot.EndUTC) ||
		!c.Snapshot.EndUTC.After(c.Snapshot.StartUTC) || !validCheckpointUsageSnapshot(c.Snapshot) ||
		!validActivityUTC(c.AnchorUTC) || c.AnchorUTC != c.Snapshot.AnchorUTC || c.AnchorUTC.After(c.Snapshot.StartUTC) || !validActivityDigest(c.AnchorProofDigest) ||
		(c.State != QueryActivityCheckpointOpening && c.State != QueryActivityCheckpointReady && c.State != QueryActivityCheckpointClosing && c.State != QueryActivityCheckpointClosed) ||
		c.AcceptedCount < 0 || c.DeliveredThrough < 0 || c.DeliveredThrough > c.AcceptedCount ||
		!validActivityUTC(c.UpdatedAtUTC) {
		return ErrInvalidQueryActivityPeriod
	}
	if c.State == QueryActivityCheckpointClosing || c.State == QueryActivityCheckpointClosed {
		if c.DeliveredThrough != c.AcceptedCount || c.CloseRequest.Ref != c.Period || !validActivityID(c.CloseRequest.CloseID) ||
			!validActivityID(c.CloseRequest.Capacity.Revision) || !validActivityDigest(c.CloseRequest.Capacity.Digest) ||
			c.CloseRequest.Capacity.EffectivePrepaidUnits < 0 ||
			(c.CloseRequest.BaseEvent != contract4paymentus.UsageBaseNone && c.CloseRequest.BaseEvent != contract4paymentus.UsageBaseMonthly && c.CloseRequest.BaseEvent != contract4paymentus.UsageBaseAnnual) ||
			c.CloseRequest.DiscountPercent < 0 || c.CloseRequest.DiscountPercent >= 100 {
			return ErrInvalidQueryActivityPeriod
		}
	} else if c.CloseRequest != (contract4paymentus.UsageCloseRequest{}) {
		return ErrInvalidQueryActivityPeriod
	}
	return nil
}

func validCheckpointUsageSnapshot(snapshot contract4paymentus.UsagePeriodSnapshot) bool {
	expected, err := contract4paymentus.UsagePeriodForAnchor(snapshot.Ref.Scope, snapshot.Config, snapshot.AnchorUTC, snapshot.StartUTC)
	return err == nil && expected == snapshot
}

// QueryActivityPeriodSequence maps a period-local accepted sequence to its
// immutable receipt. Delivery proof is recorded only after Paymentus admits
// the exact activity or the correction inbox durably retains it.
type QueryActivityPeriodSequence struct {
	Version             int                               `firestore:"v"`
	Period              contract4paymentus.UsagePeriodRef `firestore:"period"`
	Sequence            int64                             `firestore:"sequence"`
	ReceiptID           string                            `firestore:"receiptID"`
	ActivityDigest      string                            `firestore:"activityDigest"`
	DeliveryState       string                            `firestore:"deliveryState"`
	DeliveryProofDigest string                            `firestore:"deliveryProofDigest,omitempty"`
	DeliveredAtUTC      time.Time                         `firestore:"deliveredAtUTC,omitempty"`
}

func (s QueryActivityPeriodSequence) Validate() error {
	if s.Version != 1 || !validActivityPeriod(s.Period, s.Period.Scope.SpaceID) || s.Sequence < 1 ||
		!validActivityID(s.ReceiptID) || !validActivityDigest(s.ActivityDigest) {
		return ErrInvalidQueryActivityPeriod
	}
	switch s.DeliveryState {
	case QueryActivitySequencePending:
		if s.DeliveryProofDigest != "" || !s.DeliveredAtUTC.IsZero() {
			return ErrInvalidQueryActivityPeriod
		}
	case QueryActivitySequenceDelivered:
		if !validActivityDigest(s.DeliveryProofDigest) || !validActivityUTC(s.DeliveredAtUTC) {
			return ErrInvalidQueryActivityPeriod
		}
	default:
		return ErrInvalidQueryActivityPeriod
	}
	return nil
}

func NewQueryActivityPeriodCheckpointRecord(period contract4paymentus.UsagePeriodRef) (record.Record, *QueryActivityPeriodCheckpoint) {
	value := new(QueryActivityPeriodCheckpoint)
	key := record.NewKeyWithParentAndID(sharedProjectExtensionKey(period.Scope.SpaceID), QueryActivityPeriodCheckpointsCollection, NewQueryActivityPeriodID(period))
	return record.NewRecordWithData(key, value), value
}

func NewQueryActivityPeriodSequenceRecord(period contract4paymentus.UsagePeriodRef, sequence int64) (record.Record, *QueryActivityPeriodSequence) {
	value := new(QueryActivityPeriodSequence)
	checkpoint, _ := NewQueryActivityPeriodCheckpointRecord(period)
	key := record.NewKeyWithParentAndID(checkpoint.Key(), QueryActivityPeriodSequencesCollection, fmt.Sprintf("%020d", sequence))
	return record.NewRecordWithData(key, value), value
}

func NewQueryActivityPeriodID(period contract4paymentus.UsagePeriodRef) string {
	return activityDigest(append([]string{"query-activity-period/1"}, periodKeyValues(period)...)...)
}
