package models4datatug

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dal-go/record"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

const (
	QueryActivitySourceID                = "datatug-browser-activity/1"
	QueryActivityContextsCollection      = "queryActivityContexts"
	QueryActivityContextQuotasCollection = "queryActivityContextQuotas"
	QueryActivityReceiptsCollection      = "queryActivityReceipts"
	QueryActivityPendingCollection       = "queryActivityPending"
	QueryActivityMaxContextsPerMinute    = 60
	QueryActivityMaxReportsPerMinute     = 60
)

var ErrInvalidQueryActivity = errors.New("invalid query activity record")

type QueryActivityKind string

const (
	QueryActivityEdit                QueryActivityKind = "query_edit"
	QueryActivityExecutionDispatched QueryActivityKind = "query_execution_dispatched"
)

func (k QueryActivityKind) Valid() bool {
	return k == QueryActivityEdit || k == QueryActivityExecutionDispatched
}

// QueryActivityContext is private server state. It freezes the first-party
// reporter, project, paid service, and original metering window without
// retaining query content or client-supplied financial facts.
type QueryActivityContext struct {
	Version            int                               `firestore:"v"`
	ContextID          string                            `firestore:"contextID"`
	ActorID            string                            `firestore:"actorID"`
	SpaceID            string                            `firestore:"spaceID"`
	ProjectID          string                            `firestore:"projectID"`
	Period             contract4paymentus.UsagePeriodRef `firestore:"period"`
	PeriodStartUTC     time.Time                         `firestore:"periodStartUTC"`
	PeriodEndUTC       time.Time                         `firestore:"periodEndUTC"`
	PaidUntilUTC       time.Time                         `firestore:"paidUntilUTC"`
	BindingDigest      string                            `firestore:"bindingDigest"`
	PayerBindingDigest string                            `firestore:"payerBindingDigest"`
	PaidBindingProofID string                            `firestore:"paidBindingProofID"`
	QueryUseProofID    string                            `firestore:"queryUseProofID"`
	IssuedAtUTC        time.Time                         `firestore:"issuedAtUTC"`
	ExpiresAtUTC       time.Time                         `firestore:"expiresAtUTC"`
}

func (c QueryActivityContext) Validate() error {
	if c.Version != 1 || !validActivityID(c.ContextID) || !validActivityID(c.ActorID) ||
		ValidateSharedProjectIdentifier(c.SpaceID) != nil || ValidateSharedProjectIdentifier(c.ProjectID) != nil ||
		!validActivityPeriod(c.Period, c.SpaceID) || !validActivityUTC(c.PeriodStartUTC) || !validActivityUTC(c.PeriodEndUTC) || !validActivityUTC(c.PaidUntilUTC) ||
		!c.PeriodEndUTC.After(c.PeriodStartUTC) || !c.PaidUntilUTC.After(c.PeriodStartUTC) || !validActivityDigest(c.BindingDigest) || !validActivityDigest(c.PayerBindingDigest) ||
		!validActivityID(c.PaidBindingProofID) || !validActivityID(c.QueryUseProofID) ||
		!validActivityUTC(c.IssuedAtUTC) || !validActivityUTC(c.ExpiresAtUTC) ||
		c.ExpiresAtUTC.After(c.PeriodEndUTC) || c.ExpiresAtUTC.After(c.PaidUntilUTC) || !c.ExpiresAtUTC.After(c.IssuedAtUTC) ||
		c.IssuedAtUTC.Before(c.PeriodStartUTC) {
		return ErrInvalidQueryActivity
	}
	return nil
}

type QueryActivityContextQuota struct {
	Version               int                               `firestore:"v"`
	ActorID               string                            `firestore:"actorID"`
	Period                contract4paymentus.UsagePeriodRef `firestore:"period"`
	ContextWindowStartUTC time.Time                         `firestore:"contextWindowStartUTC"`
	ContextsInWindow      int64                             `firestore:"contextsInWindow"`
	ReportWindowStartUTC  time.Time                         `firestore:"reportWindowStartUTC"`
	ReportsInWindow       int64                             `firestore:"reportsInWindow"`
}

func (q QueryActivityContextQuota) Validate() error {
	if q.Version != 1 || !validActivityID(q.ActorID) || !validActivityPeriod(q.Period, q.Period.Scope.SpaceID) ||
		q.ContextsInWindow < 0 || q.ContextsInWindow > QueryActivityMaxContextsPerMinute ||
		q.ReportsInWindow < 0 || q.ReportsInWindow > QueryActivityMaxReportsPerMinute ||
		(q.ContextsInWindow > 0 && !validActivityUTC(q.ContextWindowStartUTC)) ||
		(q.ReportsInWindow > 0 && !validActivityUTC(q.ReportWindowStartUTC)) {
		return ErrInvalidQueryActivity
	}
	return nil
}

// QueryActivityReceipt coalesces the first qualifying reported action for one
// actor and canonical payer/service period. ContextID, OperationID and Kind
// are the immutable operation binding; the structural digest contains only
// server-derived facts, never query text, results, or request data.
type QueryActivityReceipt struct {
	Version            int                               `firestore:"v"`
	ReceiptID          string                            `firestore:"receiptID"`
	ContextID          string                            `firestore:"contextID"`
	OperationID        string                            `firestore:"operationID"`
	Kind               QueryActivityKind                 `firestore:"kind"`
	ActorID            string                            `firestore:"actorID"`
	SpaceID            string                            `firestore:"spaceID"`
	ProjectID          string                            `firestore:"projectID"`
	Period             contract4paymentus.UsagePeriodRef `firestore:"period"`
	PeriodStartUTC     time.Time                         `firestore:"periodStartUTC"`
	PeriodEndUTC       time.Time                         `firestore:"periodEndUTC"`
	PaidUntilUTC       time.Time                         `firestore:"paidUntilUTC"`
	Activity           contract4paymentus.UsageActivity  `firestore:"activity"`
	BindingDigest      string                            `firestore:"bindingDigest"`
	PayerBindingDigest string                            `firestore:"payerBindingDigest"`
	PaidBindingProofID string                            `firestore:"paidBindingProofID"`
	QueryUseProofID    string                            `firestore:"queryUseProofID"`
	AcceptedAtUTC      time.Time                         `firestore:"acceptedAtUTC"`
	StructuralDigest   string                            `firestore:"structuralDigest"`
}

func (r QueryActivityReceipt) Validate() error {
	if r.Version != 1 || r.ReceiptID != NewQueryActivityReceiptID(r.ActorID, r.Period) ||
		!validActivityID(r.ContextID) || !validActivityID(r.OperationID) || !r.Kind.Valid() || !validActivityID(r.ActorID) ||
		ValidateSharedProjectIdentifier(r.SpaceID) != nil || ValidateSharedProjectIdentifier(r.ProjectID) != nil ||
		!validActivityPeriod(r.Period, r.SpaceID) || r.Activity.Ref != r.Period ||
		!validActivityUTC(r.PeriodStartUTC) || !validActivityUTC(r.PeriodEndUTC) || !validActivityUTC(r.PaidUntilUTC) ||
		!r.PeriodEndUTC.After(r.PeriodStartUTC) || !r.PaidUntilUTC.After(r.PeriodStartUTC) ||
		r.Activity.SourceID != QueryActivitySourceID || r.Activity.EventID != NewQueryActivityEventID(r.ContextID, r.OperationID) ||
		r.Activity.UserID != r.ActorID || r.Activity.OccurredAtUTC != r.AcceptedAtUTC || !validActivityUTC(r.AcceptedAtUTC) ||
		r.AcceptedAtUTC.Before(r.PeriodStartUTC) || !r.AcceptedAtUTC.Before(r.PeriodEndUTC) || !r.AcceptedAtUTC.Before(r.PaidUntilUTC) ||
		QueryActivityBindingDigest(r.ActorID, r.SpaceID, r.ProjectID, r.Period, r.PeriodStartUTC, r.PeriodEndUTC, r.PaidUntilUTC, r.PayerBindingDigest, r.PaidBindingProofID, r.QueryUseProofID) != r.BindingDigest ||
		!validActivityDigest(r.BindingDigest) || !validActivityDigest(r.PayerBindingDigest) || !validActivityID(r.PaidBindingProofID) || !validActivityID(r.QueryUseProofID) ||
		r.StructuralDigest != QueryActivityStructuralDigest(r) || r.Activity.EvidenceDigest != r.StructuralDigest {
		return ErrInvalidQueryActivity
	}
	return nil
}

// QueryActivityPending is the durable outbox item. It is updated in place and
// remains until the ledger admits the activity or its closed-period inbox
// records it, so a crash after ledger commit is safely replayable.
type QueryActivityPending struct {
	Version             int                               `firestore:"v"`
	ReceiptID           string                            `firestore:"receiptID"`
	SpaceID             string                            `firestore:"spaceID"`
	Period              contract4paymentus.UsagePeriodRef `firestore:"period"`
	Activity            contract4paymentus.UsageActivity  `firestore:"activity"`
	Sequence            int64                             `firestore:"sequence,omitempty"`
	DeliveryProofDigest string                            `firestore:"deliveryProofDigest,omitempty"`
	DeliveredAtUTC      time.Time                         `firestore:"deliveredAtUTC,omitempty"`
	DeliveryState       string                            `firestore:"deliveryState" json:"deliveryState"`
	Attempts            int64                             `firestore:"attempts"`
	UpdatedAtUTC        time.Time                         `firestore:"updatedAtUTC"`
}

const (
	QueryActivityPendingStatePending   = "pending"
	QueryActivityPendingStateDelivered = "delivered"
)

func (p QueryActivityPending) Validate() error {
	if p.Version != 1 || p.ReceiptID != NewQueryActivityReceiptID(p.Activity.UserID, p.Period) ||
		ValidateSharedProjectIdentifier(p.SpaceID) != nil || !validActivityPeriod(p.Period, p.SpaceID) ||
		p.Activity.Ref != p.Period || p.Activity.SourceID != QueryActivitySourceID || !validActivityID(p.Activity.EventID) ||
		!validActivityID(p.Activity.UserID) || !validActivityDigest(p.Activity.EvidenceDigest) || !validActivityUTC(p.Activity.OccurredAtUTC) ||
		(p.DeliveryState != QueryActivityPendingStatePending && p.DeliveryState != QueryActivityPendingStateDelivered) || p.Sequence < 0 ||
		p.Attempts < 0 || !validActivityUTC(p.UpdatedAtUTC) {
		return ErrInvalidQueryActivity
	}
	if p.Sequence > 0 {
		if p.DeliveryState == QueryActivityPendingStatePending && (p.DeliveryProofDigest != "" || !p.DeliveredAtUTC.IsZero()) {
			return ErrInvalidQueryActivity
		}
		if p.DeliveryState == QueryActivityPendingStateDelivered && (!validActivityDigest(p.DeliveryProofDigest) || !validActivityUTC(p.DeliveredAtUTC)) {
			return ErrInvalidQueryActivity
		}
	}
	return nil
}

func NewQueryActivityContextRecord(spaceID, contextID string) (record.Record, *QueryActivityContext) {
	value := new(QueryActivityContext)
	key := record.NewKeyWithParentAndID(sharedProjectExtensionKey(spaceID), QueryActivityContextsCollection, contextID)
	return record.NewRecordWithData(key, value), value
}

func NewQueryActivityContextQuotaRecord(actorID string, period contract4paymentus.UsagePeriodRef) (record.Record, *QueryActivityContextQuota) {
	value := new(QueryActivityContextQuota)
	key := record.NewKeyWithParentAndID(sharedProjectExtensionKey(period.Scope.SpaceID), QueryActivityContextQuotasCollection, NewQueryActivityContextQuotaID(actorID, period))
	return record.NewRecordWithData(key, value), value
}

// NewQueryActivityContextID reuses one context per actor/project/paid period.
// Proof revisions update the current authority snapshot in place; they do not
// create rows or change the immutable operation identity.
func NewQueryActivityContextID(actorID, projectID string, period contract4paymentus.UsagePeriodRef) string {
	values := []string{"query-activity-context/1", actorID, projectID}
	return activityDigest(append(values, periodKeyValues(period)...)...)
}

func NewQueryActivityReceiptRecord(actorID string, period contract4paymentus.UsagePeriodRef) (record.Record, *QueryActivityReceipt) {
	value := new(QueryActivityReceipt)
	key := record.NewKeyWithParentAndID(sharedProjectExtensionKey(period.Scope.SpaceID), QueryActivityReceiptsCollection, NewQueryActivityReceiptID(actorID, period))
	return record.NewRecordWithData(key, value), value
}

func NewQueryActivityPendingRecord(spaceID, receiptID string) (record.Record, *QueryActivityPending) {
	value := new(QueryActivityPending)
	key := record.NewKeyWithParentAndID(sharedProjectExtensionKey(spaceID), QueryActivityPendingCollection, receiptID)
	return record.NewRecordWithData(key, value), value
}

func NewQueryActivityContextQuotaID(actorID string, period contract4paymentus.UsagePeriodRef) string {
	return activityDigest(append([]string{"query-activity-context-quota/1", actorID}, periodKeyValues(period)...)...)
}

func NewQueryActivityReceiptID(actorID string, period contract4paymentus.UsagePeriodRef) string {
	return activityDigest(append([]string{"query-activity-receipt/1", actorID}, periodKeyValues(period)...)...)
}

func NewQueryActivityEventID(contextID, operationID string) string {
	return activityDigest("query-activity-event/1", contextID, operationID)
}

func QueryActivityStructuralDigest(r QueryActivityReceipt) string {
	return activityDigest("query-activity-evidence/1", r.ContextID, r.OperationID, string(r.Kind), r.ActorID,
		r.SpaceID, r.ProjectID, r.Period.Scope.SpaceID, string(r.Period.Scope.Mode), r.Period.Scope.ProductID,
		r.Period.Scope.PayerID, r.Period.Scope.ServiceID, r.Period.PeriodID, r.BindingDigest, r.PayerBindingDigest,
		r.PaidBindingProofID, r.QueryUseProofID, CanonicalQueryActivityTime(r.PeriodStartUTC).Format(time.RFC3339Nano),
		CanonicalQueryActivityTime(r.PeriodEndUTC).Format(time.RFC3339Nano), CanonicalQueryActivityTime(r.PaidUntilUTC).Format(time.RFC3339Nano),
		CanonicalQueryActivityTime(r.AcceptedAtUTC).Format(time.RFC3339Nano), r.Activity.EventID)
}

// QueryActivityBindingDigest freezes the canonical paid and query-use binding
// that authorized the first accepted report. It deliberately excludes later
// mutable authority revisions while retaining the original period fence.
func QueryActivityBindingDigest(actorID, spaceID, projectID string, period contract4paymentus.UsagePeriodRef, startUTC, endUTC, paidUntilUTC time.Time, payerBindingDigest, paidProofID, queryUseProofID string) string {
	startUTC = CanonicalQueryActivityTime(startUTC)
	endUTC = CanonicalQueryActivityTime(endUTC)
	paidUntilUTC = CanonicalQueryActivityTime(paidUntilUTC)
	values := struct {
		Version                                                 int
		ActorID, SpaceID, ProjectID                             string
		Period                                                  contract4paymentus.UsagePeriodRef
		StartUTC, EndUTC, PaidUntilUTC                          time.Time
		PayerBindingDigest, PaidBindingProofID, QueryUseProofID string
	}{1, actorID, spaceID, projectID, period, startUTC, endUTC, paidUntilUTC, payerBindingDigest, paidProofID, queryUseProofID}
	encoded, _ := json.Marshal(values)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// CanonicalQueryActivityTime truncates to Firestore's durable microsecond
// timestamp precision. Callers must check raw server time against raw paid
// boundaries before using this value for any persisted time or digest.
func CanonicalQueryActivityTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func periodKeyValues(period contract4paymentus.UsagePeriodRef) []string {
	return []string{period.Scope.SpaceID, string(period.Scope.Mode), period.Scope.ProductID, period.Scope.PayerID, period.Scope.ServiceID, period.PeriodID}
}

func activityDigest(parts ...string) string {
	data, _ := json.Marshal(parts)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validActivityPeriod(p contract4paymentus.UsagePeriodRef, spaceID string) bool {
	s := p.Scope
	return s.SpaceID == spaceID && (s.Mode == contract4paymentus.ModeLive || s.Mode == contract4paymentus.ModeTest) &&
		validActivityID(s.SpaceID) && validActivityID(s.ProductID) && validActivityID(s.PayerID) &&
		validActivityID(s.ServiceID) && validActivityID(p.PeriodID)
}

func validActivityUTC(t time.Time) bool {
	return !t.IsZero() && t.Location() == time.UTC && t.Equal(CanonicalQueryActivityTime(t))
}
func validActivityDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func validActivityID(value string) bool {
	return value != "" && len(value) <= 128 && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "/\\\x00\r\n")
}
