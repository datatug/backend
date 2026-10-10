package facade4datatug

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

const BusinessUsageCloseGrace = 48 * time.Hour

var ErrBusinessUsagePeriodUnavailable = errors.New("business usage period is unavailable")

// BusinessUsageCloseTerms is server-derived close authority. A request's
// capacity digest and pricing choices are not accepted unless this port
// independently resolves the same frozen terms from the caller's transaction.
type BusinessUsageCloseTerms struct {
	Capacity           contract4paymentus.UsageCapacityEvidence
	BaseEvent          contract4paymentus.UsageBaseEvent
	BillMonthlyOverage bool
	DiscountPercent    int
}

// BusinessUsageCloseTermsReader must verify all capacity and pricing inputs
// from the supplied transaction. It must not use a provider or open a nested
// transaction. The native implementation refuses any non-empty prepaid basis
// until DataTug's partial-window coverage policy is approved.
type BusinessUsageCloseTermsReader interface {
	ReadBusinessUsageCloseTerms(context.Context, dal.ReadTransaction, contract4paymentus.UsagePeriodSnapshot) (BusinessUsageCloseTerms, error)
}

// NativeBusinessUsageCloseTermsReader composes the existing native paid-lot
// reader with the immutable Business anchor and exact open usage period. Empty
// paid-capacity basis is the only supported close today; it does not choose an
// unapproved lot-window rule.
type NativeBusinessUsageCloseTermsReader struct {
	db            dal.DB
	initialStarts contract4paymentus.InitialServiceStartReader
	periods       contract4paymentus.UsagePeriodReader
}

func NewNativeBusinessUsageCloseTermsReader(db dal.DB, initialStarts contract4paymentus.InitialServiceStartReader, periods contract4paymentus.UsagePeriodReader) (*NativeBusinessUsageCloseTermsReader, error) {
	if sharedProjectPortAbsent(db) || sharedProjectPortAbsent(initialStarts) || sharedProjectPortAbsent(periods) {
		return nil, ErrBusinessUsagePeriodUnavailable
	}
	return &NativeBusinessUsageCloseTermsReader{db: db, initialStarts: initialStarts, periods: periods}, nil
}

func (r *NativeBusinessUsageCloseTermsReader) ReadBusinessUsageCloseTerms(ctx context.Context, tx dal.ReadTransaction, period contract4paymentus.UsagePeriodSnapshot) (BusinessUsageCloseTerms, error) {
	var zero BusinessUsageCloseTerms
	if r == nil || sharedProjectPortAbsent(r.db) || sharedProjectPortAbsent(r.initialStarts) || sharedProjectPortAbsent(r.periods) ||
		ctx == nil || sharedProjectPortAbsent(tx) || !validBusinessUsageSnapshot(period) {
		return zero, ErrBusinessUsagePeriodUnavailable
	}
	scope := contract4paymentus.ServicePurchaseScope{
		Mode: contract4paymentus.ModeLive, SpaceID: period.Ref.Scope.SpaceID, ServiceID: BusinessProjectServiceID,
	}
	initial, err := r.initialStarts.ReadInitialServiceStart(ctx, tx, scope)
	if err != nil || !validBusinessInitialServiceStart(initial, scope) || !usagePeriodMatchesOriginalStart(period, initial) {
		return zero, ErrBusinessUsagePeriodUnavailable
	}
	state, err := r.periods.ReadPeriod(ctx, tx, period.Ref)
	if err != nil || state.Snapshot != period || state.Closed {
		return zero, ErrBusinessUsagePeriodUnavailable
	}
	owner := contract4paymentus.CapacityOwner{
		Mode: contract4paymentus.ModeLive, SpaceID: period.Ref.Scope.SpaceID,
		ProductID: BusinessProjectProductID, PayerID: period.Ref.Scope.SpaceID,
	}
	ledger, err := contract4paymentus.NewDalgoPaidCapacityLedger(r.db, businessUsageCapacityReadAuthority{
		initialStarts: r.initialStarts, periods: r.periods, period: period,
	})
	if err != nil {
		return zero, ErrBusinessUsagePeriodUnavailable
	}
	basis, err := ledger.ReadLotBasis(ctx, tx, owner)
	if err != nil {
		return zero, ErrBusinessUsagePeriodUnavailable
	}
	return businessUsageCloseTermsForBasis(period, basis)
}

func businessUsageCloseTermsForBasis(period contract4paymentus.UsagePeriodSnapshot, basis contract4paymentus.CapacityBasis) (BusinessUsageCloseTerms, error) {
	var zero BusinessUsageCloseTerms
	wantOwner := contract4paymentus.CapacityOwner{
		Mode: contract4paymentus.ModeLive, SpaceID: period.Ref.Scope.SpaceID,
		ProductID: BusinessProjectProductID, PayerID: period.Ref.Scope.SpaceID,
	}
	if !validBusinessUsageSnapshot(period) || basis.Owner != wantOwner || basis.Revision != 0 || basis.Digest != "" || len(basis.Lots) != 0 {
		return zero, ErrBusinessUsagePeriodUnavailable
	}
	capacityDigest, err := emptyBusinessCapacityDigest(period, basis)
	if err != nil {
		return zero, ErrBusinessUsagePeriodUnavailable
	}
	return BusinessUsageCloseTerms{
		Capacity: contract4paymentus.UsageCapacityEvidence{
			Revision: "empty-" + strconv.FormatInt(basis.Revision, 10), Digest: capacityDigest, EffectivePrepaidUnits: 0,
		},
		// The base subscription is charged on its normal provider schedule. Usage
		// close bills only monthly overage; Paymentus derives the intro waiver from
		// the immutable period snapshot, and overage is never launch-discounted.
		BaseEvent: contract4paymentus.UsageBaseNone, BillMonthlyOverage: true, DiscountPercent: 0,
	}, nil
}

type businessUsageCapacityReadAuthority struct {
	initialStarts contract4paymentus.InitialServiceStartReader
	periods       contract4paymentus.UsagePeriodReader
	period        contract4paymentus.UsagePeriodSnapshot
}

func (a businessUsageCapacityReadAuthority) VerifyCapacityAction(ctx context.Context, tx dal.ReadTransaction, operation contract4paymentus.CapacityOperation) error {
	if operation.Action != contract4paymentus.CapacityRead || ctx == nil || sharedProjectPortAbsent(tx) ||
		sharedProjectPortAbsent(a.initialStarts) || sharedProjectPortAbsent(a.periods) || !validBusinessUsageSnapshot(a.period) || operation.Owner != (contract4paymentus.CapacityOwner{
		Mode: contract4paymentus.ModeLive, SpaceID: a.period.Ref.Scope.SpaceID,
		ProductID: BusinessProjectProductID, PayerID: a.period.Ref.Scope.SpaceID,
	}) {
		return contract4paymentus.ErrCapacityAuthority
	}
	scope := contract4paymentus.ServicePurchaseScope{
		Mode: contract4paymentus.ModeLive, SpaceID: a.period.Ref.Scope.SpaceID, ServiceID: BusinessProjectServiceID,
	}
	initial, err := a.initialStarts.ReadInitialServiceStart(ctx, tx, scope)
	if err != nil || !validBusinessInitialServiceStart(initial, scope) || !usagePeriodMatchesOriginalStart(a.period, initial) {
		return contract4paymentus.ErrCapacityAuthority
	}
	state, err := a.periods.ReadPeriod(ctx, tx, a.period.Ref)
	if err != nil || state.Snapshot != a.period || state.Closed {
		return contract4paymentus.ErrCapacityAuthority
	}
	return nil
}

func emptyBusinessCapacityDigest(period contract4paymentus.UsagePeriodSnapshot, basis contract4paymentus.CapacityBasis) (string, error) {
	payload := struct {
		Version  string
		Period   contract4paymentus.UsagePeriodRef
		Owner    contract4paymentus.CapacityOwner
		Revision int64
		Digest   string
		Lots     int
	}{Version: "datatug-business-empty-paid-capacity/1", Period: period.Ref, Owner: basis.Owner, Revision: basis.Revision, Digest: basis.Digest, Lots: len(basis.Lots)}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

type BusinessUsagePeriodAuthorityOptions struct {
	Access        *BusinessProjectAccessVerifier
	InitialStarts contract4paymentus.InitialServiceStartReader
	Periods       contract4paymentus.UsagePeriodReader
	CloseTerms    BusinessUsageCloseTermsReader
	Now           func() time.Time
}

// NativeBusinessUsagePeriodAuthority combines current paid access for Open,
// the immutable original start for both Open and historical Close, and the
// accepted-receipt completeness checkpoint. It never grants new query access.
type NativeBusinessUsagePeriodAuthority struct {
	access        *BusinessProjectAccessVerifier
	initialStarts contract4paymentus.InitialServiceStartReader
	periods       contract4paymentus.UsagePeriodReader
	closeTerms    BusinessUsageCloseTermsReader
	now           func() time.Time
}

func NewNativeBusinessUsagePeriodAuthority(options BusinessUsagePeriodAuthorityOptions) (*NativeBusinessUsagePeriodAuthority, error) {
	if sharedProjectPortAbsent(options.Access) || sharedProjectPortAbsent(options.InitialStarts) ||
		sharedProjectPortAbsent(options.Periods) || sharedProjectPortAbsent(options.CloseTerms) || options.Now == nil {
		return nil, ErrBusinessUsagePeriodUnavailable
	}
	return &NativeBusinessUsagePeriodAuthority{
		access: options.Access, initialStarts: options.InitialStarts, periods: options.Periods,
		closeTerms: options.CloseTerms, now: options.Now,
	}, nil
}

func (a *NativeBusinessUsagePeriodAuthority) VerifyUsage(ctx context.Context, tx dal.ReadTransaction, operation contract4paymentus.UsageOperation) error {
	if a == nil || sharedProjectPortAbsent(a.access) || sharedProjectPortAbsent(a.initialStarts) ||
		sharedProjectPortAbsent(a.closeTerms) || ctx == nil || sharedProjectPortAbsent(tx) ||
		!validBusinessUsageSnapshot(operation.Period) || operation.Period.Ref.Scope.Mode != contract4paymentus.ModeLive ||
		operation.Period.Ref.Scope.ProductID != BusinessProjectProductID || operation.Period.Ref.Scope.ServiceID != BusinessProjectServiceID {
		return contract4paymentus.ErrUsageAuthority
	}
	switch operation.Action {
	case contract4paymentus.UsageAdmit, contract4paymentus.UsageRecordLate:
		return NewQueryActivityUsageAuthority().VerifyUsage(ctx, tx, operation)
	case contract4paymentus.UsageOpen:
		return a.verifyOpen(ctx, tx, operation)
	case contract4paymentus.UsageClose:
		return a.verifyClose(ctx, tx, operation)
	default:
		return contract4paymentus.ErrUsageAuthority
	}
}

func (a *NativeBusinessUsagePeriodAuthority) verifyOpen(ctx context.Context, tx dal.ReadTransaction, operation contract4paymentus.UsageOperation) error {
	if !validQueryActivityTime(operation.ObservedAtUTC) {
		return contract4paymentus.ErrUsageAuthority
	}
	access, initial, expected, err := a.readCurrentOpenFacts(ctx, tx, operation.Period.Ref.Scope.SpaceID, operation.ObservedAtUTC)
	if err != nil || expected != operation.Period || !operation.ObservedAtUTC.Before(access.PaidUntilUTC) {
		return contract4paymentus.ErrUsageAuthority
	}
	checkpoint, err := readBusinessUsageCheckpoint(ctx, tx, operation.Period.Ref)
	if err != nil || checkpoint.Validate() != nil || checkpoint.Snapshot != operation.Period ||
		checkpoint.AnchorUTC != models4datatug.CanonicalQueryActivityTime(initial.AnchorUTC) ||
		checkpoint.AnchorProofDigest != businessInitialStartDigest(initial) ||
		(checkpoint.State != models4datatug.QueryActivityCheckpointOpening && checkpoint.State != models4datatug.QueryActivityCheckpointReady) {
		return contract4paymentus.ErrUsageAuthority
	}
	return nil
}

func (a *NativeBusinessUsagePeriodAuthority) verifyClose(ctx context.Context, tx dal.ReadTransaction, operation contract4paymentus.UsageOperation) error {
	period := operation.Period
	serverNow := a.now().UTC()
	if operation.Close.Ref != period.Ref || !businessUsageCloseTimeAllowed(period.EndUTC, operation.ObservedAtUTC, serverNow) {
		return contract4paymentus.ErrUsageAuthority
	}
	initial, err := a.readInitialStart(ctx, tx, period.Ref.Scope.SpaceID)
	if err != nil || !usagePeriodMatchesOriginalStart(period, initial) {
		return contract4paymentus.ErrUsageAuthority
	}
	checkpoint, err := readBusinessUsageCheckpoint(ctx, tx, period.Ref)
	if err != nil || checkpoint.Validate() != nil || checkpoint.Snapshot != period ||
		checkpoint.AnchorUTC != models4datatug.CanonicalQueryActivityTime(initial.AnchorUTC) ||
		checkpoint.AnchorProofDigest != businessInitialStartDigest(initial) ||
		(checkpoint.State != models4datatug.QueryActivityCheckpointClosing && checkpoint.State != models4datatug.QueryActivityCheckpointClosed) ||
		checkpoint.CloseRequest != operation.Close || checkpoint.DeliveredThrough != checkpoint.AcceptedCount {
		return contract4paymentus.ErrUsageAuthority
	}
	storedPeriod, err := a.periods.ReadPeriod(ctx, tx, period.Ref)
	if err != nil || storedPeriod.Snapshot != period {
		return contract4paymentus.ErrUsageAuthority
	}
	if storedPeriod.Closed {
		if storedPeriod.Closure.Request != operation.Close || checkpoint.State == models4datatug.QueryActivityCheckpointOpening || checkpoint.State == models4datatug.QueryActivityCheckpointReady {
			return contract4paymentus.ErrUsageAuthority
		}
		return nil
	}
	if checkpoint.State != models4datatug.QueryActivityCheckpointClosing {
		return contract4paymentus.ErrUsageAuthority
	}
	terms, err := a.closeTerms.ReadBusinessUsageCloseTerms(ctx, tx, period)
	if err != nil || operation.Close.Capacity != terms.Capacity || operation.Close.BaseEvent != terms.BaseEvent ||
		operation.Close.BillMonthlyOverage != terms.BillMonthlyOverage || operation.Close.DiscountPercent != terms.DiscountPercent {
		return contract4paymentus.ErrUsageAuthority
	}
	return nil
}

func businessUsageCloseTimeAllowed(periodEndUTC, operationAtUTC, serverNowUTC time.Time) bool {
	if !validQueryActivityTime(periodEndUTC) || !validQueryActivityTime(operationAtUTC) || !validQueryActivityTime(serverNowUTC) {
		return false
	}
	eligibleAt := periodEndUTC.Add(BusinessUsageCloseGrace)
	return !operationAtUTC.Before(eligibleAt) && !serverNowUTC.Before(eligibleAt) && !operationAtUTC.After(serverNowUTC)
}

func (a *NativeBusinessUsagePeriodAuthority) readCurrentOpenFacts(ctx context.Context, tx dal.ReadTransaction, spaceID string, observedAt time.Time) (SpaceServiceAccess, contract4paymentus.ServiceInitialServiceStart, contract4paymentus.UsagePeriodSnapshot, error) {
	var zeroAccess SpaceServiceAccess
	var zeroInitial contract4paymentus.ServiceInitialServiceStart
	var zeroPeriod contract4paymentus.UsagePeriodSnapshot
	if !validQueryActivityTime(observedAt) {
		return zeroAccess, zeroInitial, zeroPeriod, ErrBusinessUsagePeriodUnavailable
	}
	access, err := a.access.ReadCurrent(ctx, tx, spaceID)
	if err != nil || access.Mode != string(contract4paymentus.ModeLive) || access.ProductID != BusinessProjectProductID ||
		access.ServiceID != BusinessProjectServiceID || access.PayerSpaceID != spaceID || access.State != "active" ||
		!access.PaidUntilUTC.After(observedAt) {
		return zeroAccess, zeroInitial, zeroPeriod, ErrBusinessUsagePeriodUnavailable
	}
	initial, err := a.readInitialStart(ctx, tx, spaceID)
	if err != nil {
		return zeroAccess, zeroInitial, zeroPeriod, err
	}
	scope := contract4paymentus.UsageScope{Mode: contract4paymentus.ModeLive, SpaceID: spaceID, ProductID: BusinessProjectProductID, PayerID: spaceID, ServiceID: BusinessProjectServiceID}
	period, err := contract4paymentus.UsagePeriodForAnchor(scope, contract4paymentus.DataTugBusinessUsagePricing(), initial.AnchorUTC, observedAt)
	if err != nil {
		return zeroAccess, zeroInitial, zeroPeriod, err
	}
	fresh := a.now().UTC()
	if !validQueryActivityTime(fresh) || fresh.Before(observedAt) || fresh.Before(access.checkedAtUTC) || !fresh.Before(access.PaidUntilUTC) {
		return zeroAccess, zeroInitial, zeroPeriod, ErrBusinessUsagePeriodUnavailable
	}
	return access, initial, period, nil
}

func (a *NativeBusinessUsagePeriodAuthority) readInitialStart(ctx context.Context, tx dal.ReadTransaction, spaceID string) (contract4paymentus.ServiceInitialServiceStart, error) {
	scope := contract4paymentus.ServicePurchaseScope{Mode: contract4paymentus.ModeLive, SpaceID: spaceID, ServiceID: BusinessProjectServiceID}
	initial, err := a.initialStarts.ReadInitialServiceStart(ctx, tx, scope)
	if err != nil || !validBusinessInitialServiceStart(initial, scope) {
		return contract4paymentus.ServiceInitialServiceStart{}, ErrBusinessUsagePeriodUnavailable
	}
	return initial, nil
}

func usagePeriodMatchesOriginalStart(period contract4paymentus.UsagePeriodSnapshot, initial contract4paymentus.ServiceInitialServiceStart) bool {
	if initial.Scope.Mode != contract4paymentus.ModeLive || initial.Scope.ServiceID != BusinessProjectServiceID ||
		initial.PayerID != initial.Scope.SpaceID || !validBusinessInitialServiceStart(initial, initial.Scope) || period.Ref.Scope != (contract4paymentus.UsageScope{
		Mode: contract4paymentus.ModeLive, SpaceID: initial.Scope.SpaceID, ProductID: BusinessProjectProductID,
		PayerID: initial.Scope.SpaceID, ServiceID: BusinessProjectServiceID,
	}) {
		return false
	}
	// Asking for the period at its exact start verifies the complete anchored
	// UTC half-open window without accepting a caller-selected origin.
	expected, err := contract4paymentus.UsagePeriodForAnchor(period.Ref.Scope, contract4paymentus.DataTugBusinessUsagePricing(), initial.AnchorUTC, period.StartUTC)
	return err == nil && expected == period
}

func validBusinessUsageSnapshot(period contract4paymentus.UsagePeriodSnapshot) bool {
	return period.Ref.Scope.Mode == contract4paymentus.ModeLive && period.Ref.Scope.ProductID == BusinessProjectProductID &&
		period.Ref.Scope.ServiceID == BusinessProjectServiceID && period.Ref.Scope.PayerID == period.Ref.Scope.SpaceID &&
		period.Config == contract4paymentus.DataTugBusinessUsagePricing() && !period.StartUTC.IsZero() && !period.EndUTC.IsZero() &&
		period.StartUTC.Location() == time.UTC && period.EndUTC.Location() == time.UTC && period.EndUTC.After(period.StartUTC)
}

func readBusinessUsageCheckpoint(ctx context.Context, tx dal.ReadTransaction, period contract4paymentus.UsagePeriodRef) (*models4datatug.QueryActivityPeriodCheckpoint, error) {
	checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(period)
	if err := tx.Get(ctx, checkpointRecord); err != nil {
		if record.IsNotFound(err) {
			return nil, ErrBusinessUsagePeriodUnavailable
		}
		return nil, err
	}
	return checkpoint, nil
}

func businessInitialStartDigest(initial contract4paymentus.ServiceInitialServiceStart) string {
	initial.AnchorUTC = models4datatug.CanonicalQueryActivityTime(initial.AnchorUTC)
	initial.InitialPeriodEndUTC = models4datatug.CanonicalQueryActivityTime(initial.InitialPeriodEndUTC)
	initial.ObservedAtUTC = models4datatug.CanonicalQueryActivityTime(initial.ObservedAtUTC)
	encoded, _ := json.Marshal(initial)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// BusinessUsagePeriodService makes the staged open/close sequence externally
// idempotent. A partial stage leaves a non-ready checkpoint that cannot admit
// reports or close; retry completes only after the native ledger confirms the
// exact stored period.
type BusinessUsagePeriodService struct {
	db        dal.DB
	authority *NativeBusinessUsagePeriodAuthority
	ledger    contract4paymentus.UsageLedger
	periods   contract4paymentus.UsagePeriodReader
	now       func() time.Time
}

func NewBusinessUsagePeriodService(db dal.DB, authority *NativeBusinessUsagePeriodAuthority, ledger contract4paymentus.UsageLedger, now func() time.Time) (*BusinessUsagePeriodService, error) {
	if sharedProjectPortAbsent(db) || sharedProjectPortAbsent(authority) || sharedProjectPortAbsent(ledger) ||
		sharedProjectPortAbsent(authority.periods) || sharedProjectPortAbsent(authority.closeTerms) || now == nil {
		return nil, ErrBusinessUsagePeriodUnavailable
	}
	return &BusinessUsagePeriodService{db: db, authority: authority, ledger: ledger, periods: authority.periods, now: now}, nil
}

func (s *BusinessUsagePeriodService) OpenCurrent(ctx context.Context, spaceID string) (contract4paymentus.UsagePeriodSnapshot, error) {
	var snapshot contract4paymentus.UsagePeriodSnapshot
	if s == nil || sharedProjectPortAbsent(s.db) || sharedProjectPortAbsent(s.authority) || sharedProjectPortAbsent(s.ledger) ||
		models4datatug.ValidateSharedProjectIdentifier(spaceID) != nil {
		return snapshot, ErrBusinessUsagePeriodUnavailable
	}
	err := s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		observedAt := s.now().UTC()
		if !validQueryActivityTime(observedAt) {
			return ErrBusinessUsagePeriodUnavailable
		}
		_, initial, expected, err := s.authority.readCurrentOpenFacts(txCtx, tx, spaceID, observedAt)
		if err != nil {
			return err
		}
		checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(expected.Ref)
		if err := tx.Get(txCtx, checkpointRecord); err != nil && !record.IsNotFound(err) {
			return err
		}
		digest := businessInitialStartDigest(initial)
		if checkpointRecord.Exists() {
			if checkpoint.Validate() != nil || checkpoint.Snapshot != expected || checkpoint.AnchorUTC != models4datatug.CanonicalQueryActivityTime(initial.AnchorUTC) || checkpoint.AnchorProofDigest != digest {
				return ErrBusinessUsagePeriodUnavailable
			}
			if checkpoint.State != models4datatug.QueryActivityCheckpointOpening && checkpoint.State != models4datatug.QueryActivityCheckpointReady {
				return ErrBusinessUsagePeriodUnavailable
			}
		} else {
			*checkpoint = models4datatug.QueryActivityPeriodCheckpoint{
				Version: 1, Period: expected.Ref, Snapshot: expected,
				AnchorUTC: models4datatug.CanonicalQueryActivityTime(initial.AnchorUTC), AnchorProofDigest: digest,
				State: models4datatug.QueryActivityCheckpointOpening, UpdatedAtUTC: models4datatug.CanonicalQueryActivityTime(observedAt),
			}
			if checkpoint.Validate() != nil {
				return ErrBusinessUsagePeriodUnavailable
			}
			if err := tx.Insert(txCtx, checkpointRecord); err != nil {
				return err
			}
		}
		snapshot = expected
		return nil
	})
	if err != nil {
		return contract4paymentus.UsagePeriodSnapshot{}, err
	}
	if err := s.ledger.Open(ctx, snapshot); err != nil {
		return contract4paymentus.UsagePeriodSnapshot{}, err
	}
	var state contract4paymentus.UsagePeriodState
	err = s.db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		var err error
		state, err = s.periods.ReadPeriod(txCtx, tx, snapshot.Ref)
		return err
	})
	if err != nil || state.Snapshot != snapshot || state.Closed || state.DistinctMAU != 0 {
		return contract4paymentus.UsagePeriodSnapshot{}, ErrBusinessUsagePeriodUnavailable
	}
	err = s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(snapshot.Ref)
		if err := tx.Get(txCtx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.Snapshot != snapshot ||
			(checkpoint.State != models4datatug.QueryActivityCheckpointOpening && checkpoint.State != models4datatug.QueryActivityCheckpointReady) {
			return ErrBusinessUsagePeriodUnavailable
		}
		checkpoint.State = models4datatug.QueryActivityCheckpointReady
		checkpoint.UpdatedAtUTC = models4datatug.CanonicalQueryActivityTime(s.now())
		if checkpoint.Validate() != nil {
			return ErrBusinessUsagePeriodUnavailable
		}
		return tx.Set(txCtx, checkpointRecord)
	})
	if err != nil {
		return contract4paymentus.UsagePeriodSnapshot{}, err
	}
	return snapshot, nil
}

func (s *BusinessUsagePeriodService) Close(ctx context.Context, request contract4paymentus.UsageCloseRequest) (contract4paymentus.UsagePeriodClose, error) {
	if s == nil || sharedProjectPortAbsent(s.db) || sharedProjectPortAbsent(s.ledger) || sharedProjectPortAbsent(s.authority) || sharedProjectPortAbsent(s.authority.periods) {
		return contract4paymentus.UsagePeriodClose{}, ErrBusinessUsagePeriodUnavailable
	}
	if err := s.prepareClose(ctx, request); err != nil {
		return contract4paymentus.UsagePeriodClose{}, err
	}
	closed, err := s.ledger.Close(ctx, request)
	if err != nil {
		return contract4paymentus.UsagePeriodClose{}, err
	}
	var state contract4paymentus.UsagePeriodState
	err = s.db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		var err error
		state, err = s.authority.periods.ReadPeriod(txCtx, tx, request.Ref)
		return err
	})
	if err != nil || !state.Closed || state.Closure.Request != request || state.Closure != closed {
		return contract4paymentus.UsagePeriodClose{}, ErrBusinessUsagePeriodUnavailable
	}
	err = s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(request.Ref)
		if err := tx.Get(txCtx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.Snapshot != closed.Period ||
			checkpoint.DeliveredThrough != checkpoint.AcceptedCount ||
			(checkpoint.State != models4datatug.QueryActivityCheckpointClosing && checkpoint.State != models4datatug.QueryActivityCheckpointClosed) || checkpoint.CloseRequest != request {
			return ErrBusinessUsagePeriodUnavailable
		}
		checkpoint.State = models4datatug.QueryActivityCheckpointClosed
		checkpoint.UpdatedAtUTC = models4datatug.CanonicalQueryActivityTime(s.now())
		if checkpoint.Validate() != nil {
			return ErrBusinessUsagePeriodUnavailable
		}
		return tx.Set(txCtx, checkpointRecord)
	})
	if err != nil {
		return contract4paymentus.UsagePeriodClose{}, err
	}
	return closed, nil
}

func (s *BusinessUsagePeriodService) prepareClose(ctx context.Context, request contract4paymentus.UsageCloseRequest) error {
	if !validBusinessUsageRef(request.Ref) {
		return ErrBusinessUsagePeriodUnavailable
	}
	return s.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		observedAt := s.now().UTC()
		if !validQueryActivityTime(observedAt) {
			return ErrBusinessUsagePeriodUnavailable
		}
		checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(request.Ref)
		if err := tx.Get(txCtx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.Period != request.Ref ||
			checkpoint.DeliveredThrough != checkpoint.AcceptedCount ||
			(checkpoint.State != models4datatug.QueryActivityCheckpointReady && checkpoint.State != models4datatug.QueryActivityCheckpointClosing && checkpoint.State != models4datatug.QueryActivityCheckpointClosed) {
			return ErrBusinessUsagePeriodUnavailable
		}
		initial, err := s.authority.readInitialStart(txCtx, tx, request.Ref.Scope.SpaceID)
		if err != nil || !usagePeriodMatchesOriginalStart(checkpoint.Snapshot, initial) ||
			checkpoint.AnchorUTC != models4datatug.CanonicalQueryActivityTime(initial.AnchorUTC) || checkpoint.AnchorProofDigest != businessInitialStartDigest(initial) ||
			!businessUsageCloseTimeAllowed(checkpoint.Snapshot.EndUTC, observedAt, observedAt) {
			return ErrBusinessUsagePeriodUnavailable
		}
		state, err := s.authority.periods.ReadPeriod(txCtx, tx, request.Ref)
		if err != nil || state.Snapshot != checkpoint.Snapshot {
			return ErrBusinessUsagePeriodUnavailable
		}
		if checkpoint.State == models4datatug.QueryActivityCheckpointClosing || checkpoint.State == models4datatug.QueryActivityCheckpointClosed {
			if checkpoint.CloseRequest != request {
				return ErrBusinessUsagePeriodUnavailable
			}
			if state.Closed {
				return nil
			}
			if checkpoint.State == models4datatug.QueryActivityCheckpointClosed {
				return ErrBusinessUsagePeriodUnavailable
			}
		} else if state.Closed {
			return ErrBusinessUsagePeriodUnavailable
		}
		terms, err := s.authority.closeTerms.ReadBusinessUsageCloseTerms(txCtx, tx, checkpoint.Snapshot)
		if err != nil || request.Ref != checkpoint.Period || request.Capacity != terms.Capacity || request.BaseEvent != terms.BaseEvent ||
			request.BillMonthlyOverage != terms.BillMonthlyOverage || request.DiscountPercent != terms.DiscountPercent {
			return ErrBusinessUsagePeriodUnavailable
		}
		if checkpoint.State == models4datatug.QueryActivityCheckpointReady {
			checkpoint.State = models4datatug.QueryActivityCheckpointClosing
			checkpoint.CloseRequest = request
			checkpoint.UpdatedAtUTC = models4datatug.CanonicalQueryActivityTime(observedAt)
			if checkpoint.Validate() != nil {
				return ErrBusinessUsagePeriodUnavailable
			}
			return tx.Set(txCtx, checkpointRecord)
		}
		return nil
	})
}

func validBusinessUsageRef(ref contract4paymentus.UsagePeriodRef) bool {
	return ref.Scope.Mode == contract4paymentus.ModeLive && ref.Scope.ProductID == BusinessProjectProductID &&
		ref.Scope.ServiceID == BusinessProjectServiceID && ref.Scope.PayerID == ref.Scope.SpaceID &&
		models4datatug.ValidateSharedProjectIdentifier(ref.Scope.SpaceID) == nil && validQueryActivityID(ref.PeriodID)
}
