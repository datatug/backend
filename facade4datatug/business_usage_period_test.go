package facade4datatug

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

type businessUsagePeriodStateReader struct {
	mu     sync.Mutex
	states map[contract4paymentus.UsagePeriodRef]contract4paymentus.UsagePeriodState
}

func (r *businessUsagePeriodStateReader) ReadPeriod(_ context.Context, tx dal.ReadTransaction, ref contract4paymentus.UsagePeriodRef) (contract4paymentus.UsagePeriodState, error) {
	if r == nil || tx == nil {
		return contract4paymentus.UsagePeriodState{}, contract4paymentus.ErrUsageLedger
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[ref]
	if !ok {
		return contract4paymentus.UsagePeriodState{}, contract4paymentus.ErrUsagePeriodMissing
	}
	return state, nil
}

type businessUsageCloseTermsFunc func(context.Context, dal.ReadTransaction, contract4paymentus.UsagePeriodSnapshot) (BusinessUsageCloseTerms, error)

func (f businessUsageCloseTermsFunc) ReadBusinessUsageCloseTerms(ctx context.Context, tx dal.ReadTransaction, period contract4paymentus.UsagePeriodSnapshot) (BusinessUsageCloseTerms, error) {
	return f(ctx, tx, period)
}

type businessUsageStagedLedger struct {
	db          dal.DB
	authority   *NativeBusinessUsagePeriodAuthority
	periods     *businessUsagePeriodStateReader
	operationAt func() time.Time
	failOpen    bool
}

func (l *businessUsageStagedLedger) Open(ctx context.Context, period contract4paymentus.UsagePeriodSnapshot) error {
	if err := l.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return l.authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageOpen, Period: period, ObservedAtUTC: l.operationAt()})
	}); err != nil {
		return err
	}
	l.periods.mu.Lock()
	if l.periods.states == nil {
		l.periods.states = make(map[contract4paymentus.UsagePeriodRef]contract4paymentus.UsagePeriodState)
	}
	l.periods.states[period.Ref] = contract4paymentus.UsagePeriodState{Snapshot: period}
	l.periods.mu.Unlock()
	if l.failOpen {
		l.failOpen = false
		return errors.New("simulated lost Open response")
	}
	return nil
}

func (*businessUsageStagedLedger) Admit(context.Context, contract4paymentus.UsageActivity) (contract4paymentus.UsageAdmission, error) {
	return contract4paymentus.UsageAdmission{}, contract4paymentus.ErrUsageLedger
}

func (l *businessUsageStagedLedger) Close(ctx context.Context, request contract4paymentus.UsageCloseRequest) (contract4paymentus.UsagePeriodClose, error) {
	var state contract4paymentus.UsagePeriodState
	if err := l.db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		var err error
		state, err = l.periods.ReadPeriod(txCtx, tx, request.Ref)
		return err
	}); err != nil {
		return contract4paymentus.UsagePeriodClose{}, err
	}
	if err := l.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		if err := l.authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageClose, Period: state.Snapshot, Close: request, ObservedAtUTC: l.operationAt()}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return contract4paymentus.UsagePeriodClose{}, err
	}
	l.periods.mu.Lock()
	defer l.periods.mu.Unlock()
	state = l.periods.states[request.Ref]
	closed := contract4paymentus.UsagePeriodClose{Period: state.Snapshot, Request: request, ClosedAtUTC: l.operationAt()}
	state.Closed, state.Closure = true, closed
	l.periods.states[request.Ref] = state
	return closed, nil
}

func TestBusinessUsageCloseRequiresServerClockAtFortyEightHourBoundary(t *testing.T) {
	end := time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)
	boundary := end.Add(BusinessUsageCloseGrace)
	tests := []struct {
		name        string
		operationAt time.Time
		serverNow   time.Time
		want        bool
	}{
		{name: "before grace", operationAt: boundary.Add(-time.Nanosecond), serverNow: boundary.Add(-time.Nanosecond)},
		{name: "early operation with later server time", operationAt: boundary.Add(-time.Second), serverNow: boundary.Add(time.Second)},
		{name: "exact boundary", operationAt: boundary, serverNow: boundary, want: true},
		{name: "future operation clock", operationAt: boundary.Add(time.Second), serverNow: boundary, want: false},
		{name: "operation cannot outrun server", operationAt: boundary.Add(time.Second), serverNow: boundary.Add(-time.Second), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := businessUsageCloseTimeAllowed(end, test.operationAt, test.serverNow); got != test.want {
				t.Fatalf("close fence=%t, want %t", got, test.want)
			}
		})
	}
}

func TestEmptyBusinessCapacityDigestBindsTheExactPeriod(t *testing.T) {
	period := contract4paymentus.UsagePeriodSnapshot{Ref: contract4paymentus.UsagePeriodRef{
		Scope:    contract4paymentus.UsageScope{Mode: contract4paymentus.ModeLive, SpaceID: "space-a", ProductID: BusinessProjectProductID, PayerID: "space-a", ServiceID: BusinessProjectServiceID},
		PeriodID: "month-a",
	}, StartUTC: time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC), EndUTC: time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC), Config: contract4paymentus.DataTugBusinessUsagePricing()}
	basis := contract4paymentus.CapacityBasis{Owner: contract4paymentus.CapacityOwner{
		Mode: contract4paymentus.ModeLive, SpaceID: "space-a", ProductID: BusinessProjectProductID, PayerID: "space-a",
	}}
	first, err := emptyBusinessCapacityDigest(period, basis)
	if err != nil || first == "" {
		t.Fatalf("empty basis has no evidence digest: %q / %v", first, err)
	}
	second, err := emptyBusinessCapacityDigest(period, basis)
	if err != nil || first != second {
		t.Fatalf("empty basis digest is not stable: %q / %q / %v", first, second, err)
	}
	period.Ref.PeriodID = "month-b"
	third, err := emptyBusinessCapacityDigest(period, basis)
	if err != nil || first == third {
		t.Fatalf("capacity digest was not bound to period: %q / %q / %v", first, third, err)
	}
}

func TestBusinessUsageCloseTermsRequireEmptyPaidCapacityBasis(t *testing.T) {
	period := contract4paymentus.UsagePeriodSnapshot{Ref: contract4paymentus.UsagePeriodRef{
		Scope:    contract4paymentus.UsageScope{Mode: contract4paymentus.ModeLive, SpaceID: "space-a", ProductID: BusinessProjectProductID, PayerID: "space-a", ServiceID: BusinessProjectServiceID},
		PeriodID: "month-a",
	}, StartUTC: time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC), EndUTC: time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC), Config: contract4paymentus.DataTugBusinessUsagePricing()}
	basis := contract4paymentus.CapacityBasis{Owner: contract4paymentus.CapacityOwner{
		Mode: contract4paymentus.ModeLive, SpaceID: "space-a", ProductID: BusinessProjectProductID, PayerID: "space-a",
	}}
	terms, err := businessUsageCloseTermsForBasis(period, basis)
	if err != nil || terms.Capacity.EffectivePrepaidUnits != 0 || terms.Capacity.Digest == "" || terms.BaseEvent != contract4paymentus.UsageBaseNone ||
		!terms.BillMonthlyOverage || terms.DiscountPercent != 0 {
		t.Fatalf("empty-basis usage pricing terms: %+v / %v", terms, err)
	}
	basis.Lots = []contract4paymentus.PaidCapacityLot{{Owner: basis.Owner, LotID: "lot-a"}}
	if _, err := businessUsageCloseTermsForBasis(period, basis); err == nil {
		t.Fatal("non-empty paid-lot basis bypassed unresolved partial-window coverage policy")
	}
	basis.Lots = nil
	basis.Revision = 1
	if _, err := businessUsageCloseTermsForBasis(period, basis); err == nil {
		t.Fatal("inconsistent empty owner revision accepted")
	}
}

func TestBusinessUsagePeriodOpenRecoversAfterLedgerCommitAndCloseUsesHistoricalPaidProof(t *testing.T) {
	ctx := context.Background()
	db := sneatcoretesting.NewMemoryDB()
	anchor := time.Date(2026, 7, 3, 9, 30, 0, 0, time.UTC)
	openAt := anchor.Add(time.Hour)
	scope := contract4paymentus.ServicePurchaseScope{Mode: contract4paymentus.ModeLive, SpaceID: "business-space", ServiceID: BusinessProjectServiceID}
	initial := contract4paymentus.ServiceInitialServiceStart{
		Version: 1, Scope: scope, PayerID: scope.SpaceID, InitiatingActorID: "buyer",
		LineageID: "initial-lineage", QuoteID: "initial-quote", QuoteFingerprint: "initial-fingerprint",
		ProviderAccountID: "provider-account", CustomerID: "original-customer", SessionID: "initial-session",
		SubscriptionID: "initial-subscription", InvoiceID: "initial-invoice", InvoiceLineID: "initial-line",
		PaymentID: "initial-payment", PriceID: "business-price", Currency: "eur", PaymentType: "new_subscription",
		ExecutionGeneration: 1, CashMinor: 9900, CashConfirmed: true, RefundsKnown: true, NoRefunds: true, DisputeResolved: true,
		AnchorUTC: anchor, InitialPeriodEndUTC: anchor.AddDate(0, 1, 0), ObservedAtUTC: anchor.Add(time.Minute),
	}
	initialReader := initialServiceStartReaderFunc(func(_ context.Context, tx dal.ReadTransaction, got contract4paymentus.ServicePurchaseScope) (contract4paymentus.ServiceInitialServiceStart, error) {
		if tx == nil || got != scope {
			t.Fatalf("initial proof read used wrong tx/scope: %T / %+v", tx, got)
		}
		return initial, nil
	})
	accessReads := 0
	currentAccess := businessServiceAccessReader(func(_ context.Context, tx dal.ReadTransaction, got contract4paymentus.ServicePurchaseScope) (contract4paymentus.CurrentSpaceServiceAccess, error) {
		accessReads++
		if tx == nil || got != scope {
			t.Fatalf("current paid read used wrong tx/scope: %T / %+v", tx, got)
		}
		access := validPaymentusBusinessAccess(openAt)
		access.Scope.SpaceID, access.PayerSpaceID = scope.SpaceID, scope.SpaceID
		return access, nil
	})
	access := newBusinessVerifier(t, currentAccess, func() time.Time { return openAt })
	periods := &businessUsagePeriodStateReader{states: make(map[contract4paymentus.UsagePeriodRef]contract4paymentus.UsagePeriodState)}
	terms := businessUsageCloseTermsFunc(func(_ context.Context, tx dal.ReadTransaction, period contract4paymentus.UsagePeriodSnapshot) (BusinessUsageCloseTerms, error) {
		if tx == nil {
			t.Fatal("close terms did not use the supplied transaction")
		}
		owner := contract4paymentus.CapacityOwner{Mode: contract4paymentus.ModeLive, SpaceID: scope.SpaceID, ProductID: BusinessProjectProductID, PayerID: scope.SpaceID}
		return businessUsageCloseTermsForBasis(period, contract4paymentus.CapacityBasis{Owner: owner})
	})
	serverNow := openAt
	authority, err := NewNativeBusinessUsagePeriodAuthority(BusinessUsagePeriodAuthorityOptions{
		Access: access, InitialStarts: initialReader, Periods: periods, CloseTerms: terms, Now: func() time.Time { return serverNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger := &businessUsageStagedLedger{db: db, authority: authority, periods: periods, operationAt: func() time.Time { return serverNow }, failOpen: true}
	service, err := NewBusinessUsagePeriodService(db, authority, ledger, func() time.Time { return serverNow })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenCurrent(ctx, scope.SpaceID); err == nil {
		t.Fatal("lost Open response was hidden")
	}
	snapshot, err := contract4paymentus.UsagePeriodForAnchor(
		contract4paymentus.UsageScope{Mode: contract4paymentus.ModeLive, SpaceID: scope.SpaceID, ProductID: BusinessProjectProductID, PayerID: scope.SpaceID, ServiceID: BusinessProjectServiceID},
		contract4paymentus.DataTugBusinessUsagePricing(), anchor, openAt,
	)
	if err != nil {
		t.Fatalf("derive exact test period: %v", err)
	}
	checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(snapshot.Ref)
	if err := db.Get(ctx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.State != models4datatug.QueryActivityCheckpointOpening {
		t.Fatalf("partial Open did not remain fenced: %+v / %v", checkpoint, err)
	}
	opened, err := service.OpenCurrent(ctx, scope.SpaceID)
	if err != nil || opened != snapshot {
		t.Fatalf("Open retry did not confirm exact ledger snapshot: %+v / %v", opened, err)
	}
	if err := db.Get(ctx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.State != models4datatug.QueryActivityCheckpointReady || checkpoint.AnchorUTC != anchor {
		t.Fatalf("confirmed Open checkpoint: %+v / %v", checkpoint, err)
	}
	var termsForClose BusinessUsageCloseTerms
	err = db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		var readErr error
		termsForClose, readErr = terms.ReadBusinessUsageCloseTerms(txCtx, tx, snapshot)
		return readErr
	})
	if err != nil {
		t.Fatalf("read server-derived close terms: %v", err)
	}
	request := contract4paymentus.UsageCloseRequest{
		Ref: snapshot.Ref, CloseID: "close-business-period", Capacity: termsForClose.Capacity,
		BaseEvent: termsForClose.BaseEvent, BillMonthlyOverage: termsForClose.BillMonthlyOverage, DiscountPercent: termsForClose.DiscountPercent,
	}
	serverNow = snapshot.EndUTC.Add(BusinessUsageCloseGrace)
	closed, err := service.Close(ctx, request)
	if err != nil || closed.Request != request || closed.Period != snapshot {
		t.Fatalf("close at the authorized boundary: %+v / %v", closed, err)
	}
	if accessReads != 4 {
		t.Fatalf("historical Close re-read current paid ownership: current access calls=%d, want only the two Open attempts", accessReads)
	}
	if err := db.Get(ctx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.State != models4datatug.QueryActivityCheckpointClosed || checkpoint.AcceptedCount != 0 || checkpoint.DeliveredThrough != 0 {
		t.Fatalf("closed zero-activity period checkpoint: %+v / %v", checkpoint, err)
	}
	closedAgain, err := service.Close(ctx, request)
	if err != nil || closedAgain != closed {
		t.Fatalf("close replay did not preserve immutable closure: %+v / %+v / %v", closedAgain, closed, err)
	}
}
