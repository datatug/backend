package facade4datatug

import (
	"context"
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
	Period                                     contract4paymentus.UsagePeriodRef
	PeriodStartUTC, PeriodEndUTC, PaidUntilUTC time.Time
	PaidBindingProofID, QueryUseProofID        string
	QueryUseAllowed                            bool
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
	db          dal.DB
	binding     BusinessActivityBindingReader
	ledger      contract4paymentus.UsageLedger
	corrections contract4paymentus.UsageCorrectionInbox
	now         func() time.Time
}

func NewQueryActivityService(db dal.DB, binding BusinessActivityBindingReader, ledger contract4paymentus.UsageLedger, corrections contract4paymentus.UsageCorrectionInbox, now func() time.Time) (*QueryActivityService, error) {
	if sharedProjectPortAbsent(db) || sharedProjectPortAbsent(binding) || sharedProjectPortAbsent(ledger) || sharedProjectPortAbsent(corrections) || now == nil {
		return nil, ErrQueryActivityUnavailable
	}
	return &QueryActivityService{db: db, binding: binding, ledger: ledger, corrections: corrections, now: now}, nil
}

// IssueContext returns a reusable actor/project/period context. Repeated loads
// and authority refreshes update its proof snapshot in place, so they do not
// create unbounded rows or change an accepted receipt's original evidence.
func (s *QueryActivityService) IssueContext(ctx context.Context, actorID, spaceID, projectID string) (QueryActivityContextResponse, error) {
	var result QueryActivityContextResponse
	if s == nil || sharedProjectPortAbsent(s.db) || sharedProjectPortAbsent(s.binding) || s.now == nil ||
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
				PaidBindingProofID: binding.PaidBindingProofID, QueryUseProofID: binding.QueryUseProofID,
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
	if s == nil || sharedProjectPortAbsent(s.db) || sharedProjectPortAbsent(s.binding) || s.now == nil ||
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
		if binding.Period != activityContext.Period || persistedBinding.PeriodStartUTC != activityContext.PeriodStartUTC || persistedBinding.PeriodEndUTC != activityContext.PeriodEndUTC || persistedBinding.PaidUntilUTC != activityContext.PaidUntilUTC ||
			binding.PaidBindingProofID != activityContext.PaidBindingProofID || binding.QueryUseProofID != activityContext.QueryUseProofID ||
			queryActivityBindingDigest(actorID, activityContext.SpaceID, activityContext.ProjectID, persistedBinding) != activityContext.BindingDigest {
			return ErrQueryActivityUnauthorized
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
			BindingDigest: activityContext.BindingDigest, PaidBindingProofID: activityContext.PaidBindingProofID,
			QueryUseProofID: activityContext.QueryUseProofID, AcceptedAtUTC: persistedAt,
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
		*receipt = structural
		pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord(activityContext.SpaceID, structural.ReceiptID)
		*pending = models4datatug.QueryActivityPending{
			Version: 1, ReceiptID: structural.ReceiptID, SpaceID: activityContext.SpaceID, Period: binding.Period,
			Activity: structural.Activity, DeliveryState: models4datatug.QueryActivityPendingStatePending, UpdatedAtUTC: persistedAt,
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
	if s == nil || sharedProjectPortAbsent(s.db) || sharedProjectPortAbsent(s.ledger) || sharedProjectPortAbsent(s.corrections) || s.now == nil ||
		models4datatug.ValidateSharedProjectIdentifier(spaceID) != nil || !validQueryActivityID(receiptID) {
		return ErrQueryActivityUnavailable
	}
	var pending models4datatug.QueryActivityPending
	err := s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		pendingRecord, stored := models4datatug.NewQueryActivityPendingRecord(spaceID, receiptID)
		if err := tx.Get(txCtx, pendingRecord); err != nil {
			if record.IsNotFound(err) {
				return ErrQueryActivityInvalid
			}
			return err
		}
		if stored.Validate() != nil || stored.ReceiptID != receiptID || stored.SpaceID != spaceID {
			return ErrQueryActivityConflict
		}
		if stored.DeliveryState == models4datatug.QueryActivityPendingStateDelivered {
			pending = *stored
			return nil
		}
		receiptRecord, receipt := models4datatug.NewQueryActivityReceiptRecord(stored.Activity.UserID, stored.Period)
		if err := tx.Get(txCtx, receiptRecord); err != nil {
			return err
		}
		if receipt.Validate() != nil || receipt.Activity != stored.Activity || receipt.SpaceID != spaceID || receipt.ReceiptID != receiptID {
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
	if pending.DeliveryState == models4datatug.QueryActivityPendingStateDelivered {
		return nil
	}
	_, err = s.ledger.Admit(ctx, pending.Activity)
	if errors.Is(err, contract4paymentus.ErrUsagePeriodClosed) {
		_, err = s.corrections.RecordLate(ctx, pending.Activity)
	}
	if err != nil {
		return err
	}
	return s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		pendingRecord, stored := models4datatug.NewQueryActivityPendingRecord(spaceID, receiptID)
		if err := tx.Get(txCtx, pendingRecord); err != nil {
			return err
		}
		if stored.Validate() != nil || stored.SpaceID != spaceID || stored.ReceiptID != receiptID || stored.Activity != pending.Activity {
			return ErrQueryActivityConflict
		}
		if stored.DeliveryState == models4datatug.QueryActivityPendingStateDelivered {
			return nil
		}
		stored.DeliveryState = models4datatug.QueryActivityPendingStateDelivered
		stored.UpdatedAtUTC = models4datatug.CanonicalQueryActivityTime(s.now())
		if !validQueryActivityTime(stored.UpdatedAtUTC) {
			return ErrQueryActivityUnavailable
		}
		return tx.Set(txCtx, pendingRecord)
	})
}

func (s *QueryActivityService) readActivityBinding(ctx context.Context, tx dal.ReadTransaction, actorID, spaceID, projectID string, at time.Time) (BusinessActivityBinding, error) {
	b, err := s.binding.ReadCurrentBusinessActivityBinding(ctx, sharedProjectReadTransaction{tx}, actorID, spaceID, projectID, at)
	if err != nil {
		return BusinessActivityBinding{}, err
	}
	if !b.QueryUseAllowed || b.Period.Scope.Mode != contract4paymentus.ModeLive || b.Period.Scope.SpaceID != spaceID ||
		b.Period.Scope.ProductID != "datatug-business-usage" || b.Period.Scope.ServiceID != "datatug" ||
		!validQueryActivityTime(b.PeriodStartUTC) || !validQueryActivityTime(b.PeriodEndUTC) || !validQueryActivityTime(b.PaidUntilUTC) ||
		!b.PeriodEndUTC.After(b.PeriodStartUTC) || !b.PaidUntilUTC.After(b.PeriodStartUTC) || at.Before(b.PeriodStartUTC) || !at.Before(b.PeriodEndUTC) || !at.Before(b.PaidUntilUTC) ||
		!validQueryActivityID(b.PaidBindingProofID) || !validQueryActivityID(b.QueryUseProofID) {
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
	return models4datatug.QueryActivityBindingDigest(actorID, spaceID, projectID, binding.Period, binding.PeriodStartUTC, binding.PeriodEndUTC, binding.PaidUntilUTC, binding.PaidBindingProofID, binding.QueryUseProofID)
}

func earlierActivityTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
