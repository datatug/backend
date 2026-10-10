package facade4datatug

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

var (
	ErrQueryActivityUnavailable  = errors.New("query activity is unavailable")
	ErrQueryActivityUnauthorized = errors.New("query activity is unauthorized")
	ErrQueryActivityInvalid      = errors.New("invalid query activity request")
	ErrQueryActivityConflict     = errors.New("query activity identity conflicts")
	ErrQueryActivityRateLimited  = errors.New("query activity rate limit exceeded")
)

// BusinessActivityBinding is returned only by the host's transaction-bound
// financial and membership authority. It contains canonical paid-period facts,
// not caller claims. QueryUseAllowed includes current actor/project query-use
// access; publish/contributor permission is deliberately not required.
type BusinessActivityBinding struct {
	Period                                                  contract4paymentus.UsagePeriodRef
	PeriodStartUTC, PeriodEndUTC, PaidUntilUTC              time.Time
	PaidBindingProofID, PayerBindingDigest, QueryUseProofID string
	QueryUseAllowed                                         bool
}

// BusinessActivityBindingReader must validate current reciprocal membership,
// query-use permission and paid service binding using the supplied transaction.
// It must not start a transaction or call a provider.
type BusinessActivityBindingReader interface {
	ReadCurrentBusinessActivityBinding(context.Context, dal.ReadTransaction, string, string, string, time.Time) (BusinessActivityBinding, error)
}

type QueryActivityContextResponse struct {
	ContextID    string    `json:"contextID"`
	ExpiresAtUTC time.Time `json:"expiresAtUTC"`
}

type QueryActivityReport struct {
	ContextID   string                           `json:"contextID"`
	OperationID string                           `json:"operationID"`
	Kind        models4datatug.QueryActivityKind `json:"kind"`
}

type QueryActivityReportResult struct {
	Accepted  bool   `json:"accepted"`
	Coalesced bool   `json:"coalesced"`
	ReceiptID string `json:"receiptID,omitempty"`
}

// QueryActivityService is an opt-in server intake and outbox. Its zero value
// and an unbound instance fail closed; browser attribution remains reported,
// first-party activity rather than proof of local execution.
type QueryActivityService struct {
	mode        contract4paymentus.Mode
	db          dal.DB
	binding     BusinessActivityBindingReader
	ledger      contract4paymentus.UsageLedger
	corrections contract4paymentus.UsageCorrectionInbox
	now         func() time.Time
}

func NewQueryActivityService(mode contract4paymentus.Mode, db dal.DB, binding BusinessActivityBindingReader, ledger contract4paymentus.UsageLedger, corrections contract4paymentus.UsageCorrectionInbox, now func() time.Time) (*QueryActivityService, error) {
	if !validBusinessUsageMode(mode) || sharedProjectPortAbsent(db) || sharedProjectPortAbsent(binding) || sharedProjectPortAbsent(ledger) || sharedProjectPortAbsent(corrections) || now == nil {
		return nil, ErrQueryActivityUnavailable
	}
	return &QueryActivityService{mode: mode, db: db, binding: binding, ledger: ledger, corrections: corrections, now: now}, nil
}

// IssueContext returns a reusable actor/project/period context. Repeated loads
// and authority refreshes update its proof snapshot in place, so they do not
// create unbounded rows or change an accepted receipt's original evidence.
func (s *QueryActivityService) IssueContext(ctx context.Context, actorID, spaceID, projectID string) (QueryActivityContextResponse, error) {
	var result QueryActivityContextResponse
	if s == nil || !validBusinessUsageMode(s.mode) || sharedProjectPortAbsent(s.db) || sharedProjectPortAbsent(s.binding) || s.now == nil ||
		!validQueryActivityActor(actorID) || models4datatug.ValidateSharedProjectIdentifier(spaceID) != nil || models4datatug.ValidateSharedProjectIdentifier(projectID) != nil {
		return result, ErrQueryActivityInvalid
	}
	err := s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		observedAt := s.now().UTC()
		if !validQueryActivityTime(observedAt) {
			return ErrQueryActivityUnavailable
		}
		binding, err := s.readActivityBinding(txCtx, tx, actorID, spaceID, projectID, observedAt)
		if err != nil {
			return err
		}
		at, err := s.finalActivityAt(observedAt, binding)
		if err != nil {
			return err
		}
		persistedBinding := canonicalActivityBinding(binding)
		contextID := models4datatug.NewQueryActivityContextID(actorID, projectID, binding.Period)
		contextRecord, activityContext := models4datatug.NewQueryActivityContextRecord(spaceID, contextID)
		if err := tx.Get(txCtx, contextRecord); err != nil && !record.IsNotFound(err) {
			return err
		}
		quotaRecord, quota := models4datatug.NewQueryActivityContextQuotaRecord(actorID, binding.Period)
		if err := tx.Get(txCtx, quotaRecord); err != nil && !record.IsNotFound(err) {
			return err
		}
		if quotaRecord.Exists() {
			if quota.Validate() != nil || quota.ActorID != actorID || quota.Period != binding.Period {
				return ErrQueryActivityConflict
			}
		}
		if !contextRecord.Exists() {
			*activityContext = models4datatug.QueryActivityContext{
				Version: 1, ContextID: contextID, ActorID: actorID, SpaceID: spaceID, ProjectID: projectID,
				Period: persistedBinding.Period, PeriodStartUTC: persistedBinding.PeriodStartUTC, PeriodEndUTC: persistedBinding.PeriodEndUTC, PaidUntilUTC: persistedBinding.PaidUntilUTC,
				BindingDigest:      queryActivityBindingDigest(actorID, spaceID, projectID, persistedBinding),
				PaidBindingProofID: binding.PaidBindingProofID, PayerBindingDigest: binding.PayerBindingDigest, QueryUseProofID: binding.QueryUseProofID,
				IssuedAtUTC: models4datatug.CanonicalQueryActivityTime(at), ExpiresAtUTC: earlierActivityTime(persistedBinding.PeriodEndUTC, persistedBinding.PaidUntilUTC),
			}
			if err := activityContext.Validate(); err != nil {
				return ErrQueryActivityUnavailable
			}
		} else if activityContext.Validate() != nil || activityContext.ActorID != actorID || activityContext.SpaceID != spaceID ||
			activityContext.ProjectID != projectID || activityContext.Period != binding.Period ||
			activityContext.PeriodStartUTC != persistedBinding.PeriodStartUTC || activityContext.PeriodEndUTC != persistedBinding.PeriodEndUTC {
			return ErrQueryActivityConflict
		} else {
			activityContext.BindingDigest = queryActivityBindingDigest(actorID, spaceID, projectID, persistedBinding)
			activityContext.PaidBindingProofID = binding.PaidBindingProofID
			activityContext.PayerBindingDigest = binding.PayerBindingDigest
			activityContext.QueryUseProofID = binding.QueryUseProofID
			activityContext.PaidUntilUTC = persistedBinding.PaidUntilUTC
			activityContext.ExpiresAtUTC = earlierActivityTime(persistedBinding.PeriodEndUTC, persistedBinding.PaidUntilUTC)
			if activityContext.Validate() != nil {
				return ErrQueryActivityConflict
			}
		}
		at, err = s.finalActivityAt(at, binding)
		if err != nil || !at.Before(activityContext.ExpiresAtUTC) {
			return ErrQueryActivityUnauthorized
		}
		persistedAt := models4datatug.CanonicalQueryActivityTime(at)
		if !contextRecord.Exists() {
			activityContext.IssuedAtUTC = persistedAt
			if activityContext.Validate() != nil {
				return ErrQueryActivityUnavailable
			}
		}
		if quota.ContextsInWindow == 0 || persistedAt.Sub(quota.ContextWindowStartUTC) >= time.Minute || persistedAt.Before(quota.ContextWindowStartUTC) {
			quota.ContextWindowStartUTC, quota.ContextsInWindow = persistedAt, 0
		}
		if quota.ContextsInWindow >= models4datatug.QueryActivityMaxContextsPerMinute {
			return ErrQueryActivityRateLimited
		}
		quota.Version, quota.ActorID, quota.Period = 1, actorID, binding.Period
		quota.ContextsInWindow++
		if err := quota.Validate(); err != nil {
			return ErrQueryActivityUnavailable
		}
		if !contextRecord.Exists() {
			if err := tx.Insert(txCtx, contextRecord); err != nil {
				return err
			}
		} else if err := tx.Set(txCtx, contextRecord); err != nil {
			return err
		}
		if quotaRecord.Exists() {
			if err := tx.Set(txCtx, quotaRecord); err != nil {
				return err
			}
		} else if err := tx.Insert(txCtx, quotaRecord); err != nil {
			return err
		}
		result = QueryActivityContextResponse{ContextID: contextID, ExpiresAtUTC: activityContext.ExpiresAtUTC}
		return nil
	})
	return result, err
}

// Report accepts only a context, an operation identity and a closed action
// enum. It rechecks authority in the same transaction that commits the receipt
// and pending delivery outbox.
func (s *QueryActivityService) Report(ctx context.Context, actorID, spaceID string, report QueryActivityReport) (QueryActivityReportResult, error) {
	var result QueryActivityReportResult
	if s == nil || !validBusinessUsageMode(s.mode) || sharedProjectPortAbsent(s.db) || sharedProjectPortAbsent(s.binding) || s.now == nil ||
		!validQueryActivityActor(actorID) || models4datatug.ValidateSharedProjectIdentifier(spaceID) != nil ||
		!validQueryActivityID(report.ContextID) || !validQueryActivityID(report.OperationID) || !report.Kind.Valid() {
		return result, ErrQueryActivityInvalid
	}
	err := s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		observedAt := s.now().UTC()
		if !validQueryActivityTime(observedAt) {
			return ErrQueryActivityUnavailable
		}
		contextRecord, activityContext := models4datatug.NewQueryActivityContextRecord(spaceID, report.ContextID)
		if err := tx.Get(txCtx, contextRecord); err != nil {
			if record.IsNotFound(err) {
				return ErrQueryActivityUnauthorized
			}
			return err
		}
		if activityContext.Validate() != nil || activityContext.ActorID != actorID || activityContext.SpaceID != spaceID || !observedAt.Before(activityContext.ExpiresAtUTC) {
			return ErrQueryActivityUnauthorized
		}
		binding, err := s.readActivityBinding(txCtx, tx, actorID, activityContext.SpaceID, activityContext.ProjectID, observedAt)
		if err != nil {
			return err
		}
		at, err := s.finalActivityAt(observedAt, binding)
		if err != nil || !at.Before(activityContext.ExpiresAtUTC) {
			return ErrQueryActivityUnauthorized
		}
		persistedBinding := canonicalActivityBinding(binding)
		if binding.Period != activityContext.Period || persistedBinding.PeriodStartUTC != activityContext.PeriodStartUTC || persistedBinding.PeriodEndUTC != activityContext.PeriodEndUTC ||
			binding.PayerBindingDigest != activityContext.PayerBindingDigest {
			return ErrQueryActivityUnauthorized
		}
		checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(binding.Period)
		if err := tx.Get(txCtx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.Period != binding.Period ||
			checkpoint.State != models4datatug.QueryActivityCheckpointReady {
			return ErrQueryActivityUnavailable
		}
		receiptRecord, receipt := models4datatug.NewQueryActivityReceiptRecord(actorID, binding.Period)
		if err := tx.Get(txCtx, receiptRecord); err != nil && !record.IsNotFound(err) {
			return err
		}
		quotaRecord, quota := models4datatug.NewQueryActivityContextQuotaRecord(actorID, binding.Period)
		if err := tx.Get(txCtx, quotaRecord); err != nil && !record.IsNotFound(err) {
			return err
		}
		if quotaRecord.Exists() && (quota.Validate() != nil || quota.ActorID != actorID || quota.Period != binding.Period) {
			return ErrQueryActivityConflict
		}
		at, err = s.finalActivityAt(at, binding)
		if err != nil || !at.Before(activityContext.ExpiresAtUTC) {
			return ErrQueryActivityUnauthorized
		}
		persistedAt := models4datatug.CanonicalQueryActivityTime(at)
		if quota.ReportsInWindow == 0 || persistedAt.Sub(quota.ReportWindowStartUTC) >= time.Minute || persistedAt.Before(quota.ReportWindowStartUTC) {
			quota.ReportWindowStartUTC, quota.ReportsInWindow = persistedAt, 0
		}
		if quota.ReportsInWindow >= models4datatug.QueryActivityMaxReportsPerMinute {
			return ErrQueryActivityRateLimited
		}
		quota.Version, quota.ActorID, quota.Period = 1, actorID, binding.Period
		quota.ReportsInWindow++
		if receiptRecord.Exists() {
			if receipt.Validate() != nil || receipt.ActorID != actorID || receipt.Period != binding.Period {
				return ErrQueryActivityConflict
			}
			if receipt.OperationID == report.OperationID && (receipt.ContextID != report.ContextID || receipt.Kind != report.Kind) {
				return ErrQueryActivityConflict
			}
			accepted := receipt.OperationID == report.OperationID && receipt.ContextID == report.ContextID && receipt.Kind == report.Kind
			result = QueryActivityReportResult{Accepted: accepted, Coalesced: !accepted, ReceiptID: receipt.ReceiptID}
			if err := quota.Validate(); err != nil {
				return ErrQueryActivityUnavailable
			}
			if quotaRecord.Exists() {
				return tx.Set(txCtx, quotaRecord)
			}
			return tx.Insert(txCtx, quotaRecord)
		}
		structural := models4datatug.QueryActivityReceipt{
			Version: 1, ContextID: report.ContextID, OperationID: report.OperationID, Kind: report.Kind,
			ActorID: actorID, SpaceID: activityContext.SpaceID, ProjectID: activityContext.ProjectID, Period: binding.Period,
			PeriodStartUTC: persistedBinding.PeriodStartUTC, PeriodEndUTC: persistedBinding.PeriodEndUTC, PaidUntilUTC: persistedBinding.PaidUntilUTC,
			BindingDigest:      queryActivityBindingDigest(actorID, activityContext.SpaceID, activityContext.ProjectID, persistedBinding),
			PaidBindingProofID: binding.PaidBindingProofID, PayerBindingDigest: binding.PayerBindingDigest,
			QueryUseProofID: binding.QueryUseProofID, AcceptedAtUTC: persistedAt,
		}
		structural.ReceiptID = models4datatug.NewQueryActivityReceiptID(actorID, binding.Period)
		structural.Activity = contract4paymentus.UsageActivity{
			Ref: binding.Period, SourceID: models4datatug.QueryActivitySourceID,
			EventID: models4datatug.NewQueryActivityEventID(report.ContextID, report.OperationID),
			UserID:  actorID, OccurredAtUTC: persistedAt,
		}
		structural.StructuralDigest = models4datatug.QueryActivityStructuralDigest(structural)
		structural.Activity.EvidenceDigest = structural.StructuralDigest
		if structural.Validate() != nil || quota.Validate() != nil {
			return ErrQueryActivityUnavailable
		}
		if checkpoint.AcceptedCount == math.MaxInt64 {
			return ErrQueryActivityUnavailable
		}
		sequence := checkpoint.AcceptedCount + 1
		sequenceRecord, periodSequence := models4datatug.NewQueryActivityPeriodSequenceRecord(binding.Period, sequence)
		if err := tx.Get(txCtx, sequenceRecord); err != nil && !record.IsNotFound(err) {
			return err
		}
		if sequenceRecord.Exists() {
			return ErrQueryActivityConflict
		}
		checkpoint.AcceptedCount = sequence
		checkpoint.UpdatedAtUTC = persistedAt
		*periodSequence = models4datatug.QueryActivityPeriodSequence{
			Version: 1, Period: binding.Period, Sequence: sequence, ReceiptID: structural.ReceiptID,
			ActivityDigest: structural.Activity.EvidenceDigest, DeliveryState: models4datatug.QueryActivitySequencePending,
		}
		if checkpoint.Validate() != nil || periodSequence.Validate() != nil {
			return ErrQueryActivityUnavailable
		}
		*receipt = structural
		pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord(activityContext.SpaceID, structural.ReceiptID)
		*pending = models4datatug.QueryActivityPending{
			Version: 1, ReceiptID: structural.ReceiptID, SpaceID: activityContext.SpaceID, Period: binding.Period,
			Activity: structural.Activity, Sequence: sequence, DeliveryState: models4datatug.QueryActivityPendingStatePending, UpdatedAtUTC: persistedAt,
		}
		if pending.Validate() != nil {
			return ErrQueryActivityUnavailable
		}
		if err := tx.Insert(txCtx, receiptRecord); err != nil {
			return err
		}
		if err := tx.Insert(txCtx, pendingRecord); err != nil {
			return err
		}
		if err := tx.Insert(txCtx, sequenceRecord); err != nil {
			return err
		}
		if err := tx.Set(txCtx, checkpointRecord); err != nil {
			return err
		}
		if quotaRecord.Exists() {
			if err := tx.Set(txCtx, quotaRecord); err != nil {
				return err
			}
		} else if err := tx.Insert(txCtx, quotaRecord); err != nil {
			return err
		}
		result = QueryActivityReportResult{Accepted: true, ReceiptID: structural.ReceiptID}
		return nil
	})
	return result, err
}

// Deliver replays a committed pending receipt through the Paymentus ledger.
// Ledger admission and late-evidence retention happen outside the acceptance
// transaction. A crash before marking completion is safe to replay.
func (s *QueryActivityService) Deliver(ctx context.Context, spaceID, receiptID string) error {
	if s == nil || !validBusinessUsageMode(s.mode) || sharedProjectPortAbsent(s.db) || sharedProjectPortAbsent(s.ledger) || sharedProjectPortAbsent(s.corrections) || s.now == nil ||
		models4datatug.ValidateSharedProjectIdentifier(spaceID) != nil || !validQueryActivityID(receiptID) {
		return ErrQueryActivityUnavailable
	}
	var pending models4datatug.QueryActivityPending
	alreadyDelivered := false
	err := s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		pendingRecord, stored := models4datatug.NewQueryActivityPendingRecord(spaceID, receiptID)
		if err := tx.Get(txCtx, pendingRecord); err != nil {
			if record.IsNotFound(err) {
				return ErrQueryActivityInvalid
			}
			return err
		}
		if stored.Validate() != nil || stored.ReceiptID != receiptID || stored.SpaceID != spaceID || stored.Period.Scope.Mode != s.mode {
			return ErrQueryActivityConflict
		}
		checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(stored.Period)
		if err := tx.Get(txCtx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.Period != stored.Period ||
			(checkpoint.State != models4datatug.QueryActivityCheckpointReady && checkpoint.State != models4datatug.QueryActivityCheckpointClosed) ||
			stored.Sequence < 1 || stored.Sequence > checkpoint.AcceptedCount {
			return ErrQueryActivityConflict
		}
		sequenceRecord, sequence := models4datatug.NewQueryActivityPeriodSequenceRecord(stored.Period, stored.Sequence)
		if err := tx.Get(txCtx, sequenceRecord); err != nil || sequence.Validate() != nil || sequence.Period != stored.Period ||
			sequence.Sequence != stored.Sequence || sequence.ReceiptID != receiptID || sequence.ActivityDigest != stored.Activity.EvidenceDigest {
			return ErrQueryActivityConflict
		}
		if stored.DeliveryState == models4datatug.QueryActivityPendingStateDelivered {
			if sequence.DeliveryState != models4datatug.QueryActivitySequenceDelivered || sequence.DeliveryProofDigest != stored.DeliveryProofDigest || sequence.DeliveredAtUTC != stored.DeliveredAtUTC {
				return ErrQueryActivityConflict
			}
			pending = *stored
			alreadyDelivered = true
			return nil
		}
		if checkpoint.State != models4datatug.QueryActivityCheckpointReady {
			return ErrQueryActivityConflict
		}
		receiptRecord, receipt := models4datatug.NewQueryActivityReceiptRecord(stored.Activity.UserID, stored.Period)
		if err := tx.Get(txCtx, receiptRecord); err != nil {
			return err
		}
		if receipt.Validate() != nil || receipt.Activity != stored.Activity || receipt.SpaceID != spaceID || receipt.ReceiptID != receiptID {
			return ErrQueryActivityConflict
		}
		if sequence.DeliveryState != models4datatug.QueryActivitySequencePending || stored.DeliveryState != models4datatug.QueryActivityPendingStatePending {
			return ErrQueryActivityConflict
		}
		if stored.Attempts == math.MaxInt64 {
			return ErrQueryActivityUnavailable
		}
		stored.Attempts++
		stored.UpdatedAtUTC = models4datatug.CanonicalQueryActivityTime(s.now())
		if !validQueryActivityTime(stored.UpdatedAtUTC) {
			return ErrQueryActivityUnavailable
		}
		pending = *stored
		return tx.Set(txCtx, pendingRecord)
	})
	if err != nil {
		return err
	}
	if alreadyDelivered {
		return s.advanceDeliveryWatermark(ctx, pending.Period)
	}
	admission, err := s.ledger.Admit(ctx, pending.Activity)
	var deliveryProof any
	if errors.Is(err, contract4paymentus.ErrUsagePeriodClosed) {
		var late contract4paymentus.UsageLateReceipt
		late, err = s.corrections.RecordLate(ctx, pending.Activity)
		if err == nil {
			if late.Kind == contract4paymentus.UsageLateQueued && late.Evidence.Activity == pending.Activity &&
				late.Evidence.OriginalClose.Period.Ref == pending.Period && validQueryActivityTime(late.Evidence.ReceivedAtUTC) {
				deliveryProof = late
			} else if late.Kind == contract4paymentus.UsageLateAlreadyAdmitted && late.Admission.Activity == pending.Activity && validQueryActivityTime(late.Admission.AcceptedAtUTC) {
				deliveryProof = late
			} else {
				return ErrQueryActivityConflict
			}
		}
	} else if err == nil {
		if admission.Activity != pending.Activity || !validQueryActivityTime(admission.AcceptedAtUTC) {
			return ErrQueryActivityConflict
		}
		deliveryProof = admission
	}
	if err != nil {
		return err
	}
	proofDigest := queryActivityDeliveryProofDigest(deliveryProof)
	if proofDigest == "" {
		return ErrQueryActivityUnavailable
	}
	return s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		pendingRecord, stored := models4datatug.NewQueryActivityPendingRecord(spaceID, receiptID)
		if err := tx.Get(txCtx, pendingRecord); err != nil {
			return err
		}
		if stored.Validate() != nil || stored.SpaceID != spaceID || stored.ReceiptID != receiptID || stored.Activity != pending.Activity || stored.Period.Scope.Mode != s.mode {
			return ErrQueryActivityConflict
		}
		checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(stored.Period)
		if err := tx.Get(txCtx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.Period != stored.Period ||
			(checkpoint.State != models4datatug.QueryActivityCheckpointReady && checkpoint.State != models4datatug.QueryActivityCheckpointClosed) ||
			stored.Sequence < 1 || stored.Sequence > checkpoint.AcceptedCount {
			return ErrQueryActivityConflict
		}
		sequenceRecord, sequence := models4datatug.NewQueryActivityPeriodSequenceRecord(stored.Period, stored.Sequence)
		if err := tx.Get(txCtx, sequenceRecord); err != nil || sequence.Validate() != nil || sequence.ReceiptID != receiptID ||
			sequence.ActivityDigest != stored.Activity.EvidenceDigest || sequence.Period != stored.Period || sequence.Sequence != stored.Sequence {
			return ErrQueryActivityConflict
		}
		if sequence.DeliveryState == models4datatug.QueryActivitySequenceDelivered {
			if stored.DeliveryState != models4datatug.QueryActivityPendingStateDelivered || stored.DeliveryProofDigest != sequence.DeliveryProofDigest || stored.DeliveredAtUTC != sequence.DeliveredAtUTC {
				return ErrQueryActivityConflict
			}
			previous := checkpoint.DeliveredThrough
			checkpoint.DeliveredThrough, err = advanceDeliveredQueryActivitySequence(txCtx, tx, checkpoint, sequence, stored)
			if err != nil || checkpoint.DeliveredThrough == previous {
				return err
			}
			checkpoint.UpdatedAtUTC = models4datatug.CanonicalQueryActivityTime(s.now())
			if !validQueryActivityTime(checkpoint.UpdatedAtUTC) || checkpoint.Validate() != nil {
				return ErrQueryActivityUnavailable
			}
			return tx.Set(txCtx, checkpointRecord)
		}
		if checkpoint.State != models4datatug.QueryActivityCheckpointReady {
			return ErrQueryActivityConflict
		}
		stored.DeliveryProofDigest = proofDigest
		stored.DeliveredAtUTC = models4datatug.CanonicalQueryActivityTime(s.now())
		if !validQueryActivityTime(stored.DeliveredAtUTC) {
			return ErrQueryActivityUnavailable
		}
		stored.DeliveryState = models4datatug.QueryActivityPendingStateDelivered
		stored.UpdatedAtUTC = stored.DeliveredAtUTC
		if !validQueryActivityTime(stored.UpdatedAtUTC) {
			return ErrQueryActivityUnavailable
		}
		sequence.DeliveryState = models4datatug.QueryActivitySequenceDelivered
		sequence.DeliveryProofDigest = proofDigest
		sequence.DeliveredAtUTC = stored.DeliveredAtUTC
		if sequence.Validate() != nil || stored.Validate() != nil {
			return ErrQueryActivityUnavailable
		}
		checkpoint.DeliveredThrough, err = advanceDeliveredQueryActivitySequence(txCtx, tx, checkpoint, sequence, stored)
		if err != nil {
			return err
		}
		checkpoint.UpdatedAtUTC = stored.DeliveredAtUTC
		if checkpoint.Validate() != nil {
			return ErrQueryActivityUnavailable
		}
		if err := tx.Set(txCtx, pendingRecord); err != nil {
			return err
		}
		if err := tx.Set(txCtx, sequenceRecord); err != nil {
			return err
		}
		return tx.Set(txCtx, checkpointRecord)
	})
}

const QueryActivityWatermarkBatchSize = 128

func (s *QueryActivityService) advanceDeliveryWatermark(ctx context.Context, period contract4paymentus.UsagePeriodRef) error {
	return s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(period)
		if err := tx.Get(txCtx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.Snapshot.Ref != period ||
			(checkpoint.State != models4datatug.QueryActivityCheckpointReady && checkpoint.State != models4datatug.QueryActivityCheckpointClosed) {
			return ErrQueryActivityConflict
		}
		if checkpoint.State == models4datatug.QueryActivityCheckpointClosed {
			if checkpoint.DeliveredThrough == checkpoint.AcceptedCount {
				return nil
			}
			return ErrQueryActivityConflict
		}
		previous := checkpoint.DeliveredThrough
		advanced, err := advanceDeliveredQueryActivitySequence(txCtx, tx, checkpoint, nil, nil)
		if err != nil || advanced == previous {
			return err
		}
		checkpoint.DeliveredThrough = advanced
		checkpoint.UpdatedAtUTC = models4datatug.CanonicalQueryActivityTime(s.now())
		if !validQueryActivityTime(checkpoint.UpdatedAtUTC) || checkpoint.Validate() != nil {
			return ErrQueryActivityUnavailable
		}
		return tx.Set(txCtx, checkpointRecord)
	})
}

func advanceDeliveredQueryActivitySequence(ctx context.Context, tx dal.ReadTransaction, checkpoint *models4datatug.QueryActivityPeriodCheckpoint, stagedSequence *models4datatug.QueryActivityPeriodSequence, stagedPending *models4datatug.QueryActivityPending) (int64, error) {
	through := checkpoint.DeliveredThrough
	for steps := 0; steps < QueryActivityWatermarkBatchSize && through < checkpoint.AcceptedCount; steps++ {
		next := through + 1
		var sequence models4datatug.QueryActivityPeriodSequence
		var pending models4datatug.QueryActivityPending
		if stagedSequence != nil && stagedPending != nil && stagedSequence.Sequence == next {
			sequence, pending = *stagedSequence, *stagedPending
		} else {
			sequenceRecord, storedSequence := models4datatug.NewQueryActivityPeriodSequenceRecord(checkpoint.Period, next)
			if err := tx.Get(ctx, sequenceRecord); err != nil {
				if record.IsNotFound(err) {
					return through, nil
				}
				return through, err
			}
			sequence = *storedSequence
			if sequence.Validate() != nil || sequence.Sequence != next || sequence.Period != checkpoint.Period {
				return through, ErrQueryActivityConflict
			}
			pendingRecord, storedPending := models4datatug.NewQueryActivityPendingRecord(checkpoint.Period.Scope.SpaceID, sequence.ReceiptID)
			if err := tx.Get(ctx, pendingRecord); err != nil {
				if record.IsNotFound(err) {
					return through, nil
				}
				return through, err
			}
			pending = *storedPending
		}
		if sequence.Validate() != nil || pending.Validate() != nil || sequence.DeliveryState != models4datatug.QueryActivitySequenceDelivered ||
			pending.DeliveryState != models4datatug.QueryActivityPendingStateDelivered || pending.Period != checkpoint.Period || pending.Sequence != next ||
			pending.ReceiptID != sequence.ReceiptID || pending.Activity.EvidenceDigest != sequence.ActivityDigest ||
			pending.DeliveryProofDigest != sequence.DeliveryProofDigest || pending.DeliveredAtUTC != sequence.DeliveredAtUTC {
			return through, nil
		}
		through = next
	}
	return through, nil
}

func queryActivityDeliveryProofDigest(proof any) string {
	if proof == nil {
		return ""
	}
	encoded, err := json.Marshal(proof)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (s *QueryActivityService) readActivityBinding(ctx context.Context, tx dal.ReadTransaction, actorID, spaceID, projectID string, at time.Time) (BusinessActivityBinding, error) {
	b, err := s.binding.ReadCurrentBusinessActivityBinding(ctx, sharedProjectReadTransaction{tx}, actorID, spaceID, projectID, at)
	if err != nil {
		return BusinessActivityBinding{}, err
	}
	if !b.QueryUseAllowed || b.Period.Scope.Mode != s.mode || b.Period.Scope.SpaceID != spaceID ||
		b.Period.Scope.ProductID != "datatug-business-usage" || b.Period.Scope.ServiceID != "datatug" ||
		!validQueryActivityTime(b.PeriodStartUTC) || !validQueryActivityTime(b.PeriodEndUTC) || !validQueryActivityTime(b.PaidUntilUTC) ||
		!b.PeriodEndUTC.After(b.PeriodStartUTC) || !b.PaidUntilUTC.After(b.PeriodStartUTC) || at.Before(b.PeriodStartUTC) || !at.Before(b.PeriodEndUTC) || !at.Before(b.PaidUntilUTC) ||
		b.Period.Scope.Mode != s.mode || !validQueryActivityID(b.PaidBindingProofID) || !validBusinessPayerDigest(b.PayerBindingDigest) || !validQueryActivityID(b.QueryUseProofID) {
		return BusinessActivityBinding{}, ErrQueryActivityUnauthorized
	}
	return b, nil
}

func (s *QueryActivityService) finalActivityAt(observedAt time.Time, binding BusinessActivityBinding) (time.Time, error) {
	at := s.now().UTC()
	if !validQueryActivityTime(at) || at.Before(observedAt) || at.Before(binding.PeriodStartUTC) || !at.Before(binding.PeriodEndUTC) || !at.Before(binding.PaidUntilUTC) {
		return time.Time{}, ErrQueryActivityUnauthorized
	}
	return at, nil
}

func validQueryActivityActor(value string) bool { return validQueryActivityID(value) }
func validQueryActivityID(value string) bool {
	return value != "" && len(value) <= 128 && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "/\\\x00\r\n")
}
func validQueryActivityTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Year() >= 1 && value.Year() <= 9999
}

func canonicalActivityBinding(binding BusinessActivityBinding) BusinessActivityBinding {
	binding.PeriodStartUTC = models4datatug.CanonicalQueryActivityTime(binding.PeriodStartUTC)
	binding.PeriodEndUTC = models4datatug.CanonicalQueryActivityTime(binding.PeriodEndUTC)
	binding.PaidUntilUTC = models4datatug.CanonicalQueryActivityTime(binding.PaidUntilUTC)
	return binding
}

func queryActivityBindingDigest(actorID, spaceID, projectID string, binding BusinessActivityBinding) string {
	return models4datatug.QueryActivityBindingDigest(actorID, spaceID, projectID, binding.Period, binding.PeriodStartUTC, binding.PeriodEndUTC, binding.PaidUntilUTC, binding.PayerBindingDigest, binding.PaidBindingProofID, binding.QueryUseProofID)
}

func earlierActivityTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
