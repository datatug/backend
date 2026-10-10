package facade4datatug

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

type businessUsagePeriodStateReader struct {
	mu     sync.Mutex
	states map[contract4paymentus.UsagePeriodRef]contract4paymentus.UsagePeriodState
	err    error
}

type failingBusinessUsageReadwriteDB struct {
	dal.DB
	err error
}

func (db failingBusinessUsageReadwriteDB) RunReadwriteTransaction(ctx context.Context, worker dal.RWTxWorker, options ...dal.TransactionOption) error {
	return db.DB.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return worker(txCtx, failingBusinessUsageReadwriteTx{ReadwriteTransaction: tx, err: db.err})
	}, options...)
}

type failingBusinessUsageReadwriteTx struct {
	dal.ReadwriteTransaction
	err error
}

func (tx failingBusinessUsageReadwriteTx) Get(context.Context, record.Record) error {
	return tx.err
}

type failingBusinessUsageReadTx struct {
	dal.ReadTransaction
	err error
}

func (tx failingBusinessUsageReadTx) Get(context.Context, record.Record) error {
	return tx.err
}

type failingBusinessUsageInsertDB struct {
	dal.DB
	err error
}

func (db failingBusinessUsageInsertDB) RunReadwriteTransaction(ctx context.Context, worker dal.RWTxWorker, options ...dal.TransactionOption) error {
	return db.DB.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return worker(txCtx, failingBusinessUsageInsertTx{ReadwriteTransaction: tx, err: db.err})
	}, options...)
}

type failingBusinessUsageInsertTx struct {
	dal.ReadwriteTransaction
	err error
}

func (tx failingBusinessUsageInsertTx) Insert(context.Context, record.Record, ...dal.InsertOption) error {
	return tx.err
}

func (r *businessUsagePeriodStateReader) ReadPeriod(_ context.Context, tx dal.ReadTransaction, ref contract4paymentus.UsagePeriodRef) (contract4paymentus.UsagePeriodState, error) {
	if r == nil || tx == nil {
		return contract4paymentus.UsagePeriodState{}, contract4paymentus.ErrUsageLedger
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return contract4paymentus.UsagePeriodState{}, r.err
	}
	state, ok := r.states[ref]
	if !ok {
		return contract4paymentus.UsagePeriodState{}, contract4paymentus.ErrUsagePeriodMissing
	}
	return state, nil
}

type businessUsageCloseTermsFunc func(context.Context, dal.ReadTransaction, contract4paymentus.UsagePeriodSnapshot) (BusinessUsageCloseTerms, error)

func validBusinessUsageTestSnapshot(t *testing.T, config contract4paymentus.UsagePricingConfig, anchor, at time.Time) contract4paymentus.UsagePeriodSnapshot {
	t.Helper()
	scope := contract4paymentus.UsageScope{Mode: contract4paymentus.ModeLive, SpaceID: "space-a", ProductID: BusinessProjectProductID, PayerID: "space-a", ServiceID: BusinessProjectServiceID}
	period, err := contract4paymentus.UsagePeriodForAnchor(scope, config, anchor, at)
	if err != nil {
		t.Fatalf("derive valid Business usage snapshot: %v", err)
	}
	return period
}

func (f businessUsageCloseTermsFunc) ReadBusinessUsageCloseTerms(ctx context.Context, tx dal.ReadTransaction, period contract4paymentus.UsagePeriodSnapshot) (BusinessUsageCloseTerms, error) {
	return f(ctx, tx, period)
}

type businessUsageStagedLedger struct {
	db                   dal.DB
	authority            *NativeBusinessUsagePeriodAuthority
	periods              *businessUsagePeriodStateReader
	operationAt          func() time.Time
	failOpen             bool
	failClose            bool
	failCloseAfterCommit bool
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
	state, exists := l.periods.states[period.Ref]
	if exists && state.Snapshot != period {
		l.periods.mu.Unlock()
		return contract4paymentus.ErrUsageLedger
	}
	if !exists {
		l.periods.states[period.Ref] = contract4paymentus.UsagePeriodState{Snapshot: period}
	}
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
	if l.failClose {
		l.failClose = false
		return contract4paymentus.UsagePeriodClose{}, errors.New("simulated close ledger failure before commit")
	}
	l.periods.mu.Lock()
	defer l.periods.mu.Unlock()
	state = l.periods.states[request.Ref]
	if state.Closed {
		return state.Closure, nil
	}
	closed := contract4paymentus.UsagePeriodClose{Period: state.Snapshot, Request: request, ClosedAtUTC: l.operationAt()}
	state.Closed, state.Closure = true, closed
	l.periods.states[request.Ref] = state
	if l.failCloseAfterCommit {
		l.failCloseAfterCommit = false
		return contract4paymentus.UsagePeriodClose{}, errors.New("simulated lost Close response")
	}
	return closed, nil
}

func TestBusinessUsageCloseRequiresServerClockAtFortyEightHourBoundary(t *testing.T) {
	end := time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)
	boundary := end.Add(BusinessUsageCloseGrace)
	tests := []struct {
		name        string
		operationAt time.Time
		serverNow   time.Time
		periodEnd   time.Time
		want        bool
	}{
		{name: "before grace", operationAt: boundary.Add(-time.Nanosecond), serverNow: boundary.Add(-time.Nanosecond)},
		{name: "early operation with later server time", operationAt: boundary.Add(-time.Second), serverNow: boundary.Add(time.Second)},
		{name: "exact boundary", operationAt: boundary, serverNow: boundary, want: true},
		{name: "future operation clock", operationAt: boundary.Add(time.Second), serverNow: boundary, want: false},
		{name: "operation cannot outrun server", operationAt: boundary.Add(time.Second), serverNow: boundary.Add(-time.Second), want: false},
		{name: "missing operation time", serverNow: boundary},
		{name: "non-UTC operation time", operationAt: time.Date(2026, 10, 3, 7, 0, 0, 0, time.FixedZone("UTC", 0)), serverNow: boundary},
		{name: "missing period end", operationAt: boundary, serverNow: boundary, periodEnd: time.Time{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			periodEnd := test.periodEnd
			if periodEnd.IsZero() && test.name != "missing period end" {
				periodEnd = end
			}
			if got := businessUsageCloseTimeAllowed(periodEnd, test.operationAt, test.serverNow); got != test.want {
				t.Fatalf("close fence=%t, want %t", got, test.want)
			}
		})
	}
}

func TestBusinessUsageSnapshotRejectsUnanchoredOrUnapprovedFrozenState(t *testing.T) {
	anchor := time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)
	valid := validBusinessUsageTestSnapshot(t, contract4paymentus.DataTugBusinessUsagePricing(), anchor, anchor)
	for name, mutate := range map[string]func(*contract4paymentus.UsagePeriodSnapshot){
		"non-live scope": func(p *contract4paymentus.UsagePeriodSnapshot) { p.Ref.Scope.Mode = contract4paymentus.ModeTest },
		"foreign payer":  func(p *contract4paymentus.UsagePeriodSnapshot) { p.Ref.Scope.PayerID = "other-space" },
		"missing anchor": func(p *contract4paymentus.UsagePeriodSnapshot) { p.AnchorUTC = time.Time{} },
		"foreign timezone": func(p *contract4paymentus.UsagePeriodSnapshot) {
			p.StartUTC = p.StartUTC.In(time.FixedZone("offset", 3600))
		},
		"noncanonical period id":  func(p *contract4paymentus.UsagePeriodSnapshot) { p.Ref.PeriodID = "other-period" },
		"invalid frozen currency": func(p *contract4paymentus.UsagePeriodSnapshot) { p.Config.Currency = "gbp" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid
			mutate(&bad)
			if validBusinessUsageSnapshot(bad) {
				t.Fatalf("invalid native snapshot was accepted: %+v", bad)
			}
		})
	}
}

func TestBusinessUsagePeriodServiceNilReceiverFailsClosed(t *testing.T) {
	var service *BusinessUsagePeriodService
	if _, err := service.OpenCurrent(context.Background(), "space-a"); !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
		t.Fatalf("nil OpenCurrent receiver error = %v, want unavailable", err)
	}
	if _, err := service.Close(context.Background(), contract4paymentus.UsageCloseRequest{}); !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
		t.Fatalf("nil Close receiver error = %v, want unavailable", err)
	}
}

func TestEmptyBusinessCapacityDigestBindsTheExactPeriod(t *testing.T) {
	anchor := time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)
	period := validBusinessUsageTestSnapshot(t, contract4paymentus.DataTugBusinessUsagePricing(), anchor, anchor)
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
	otherPeriod := validBusinessUsageTestSnapshot(t, period.Config, anchor, anchor.AddDate(0, 1, 0))
	third, err := emptyBusinessCapacityDigest(otherPeriod, basis)
	if err != nil || first == third {
		t.Fatalf("capacity digest was not bound to period: %q / %q / %v", first, third, err)
	}
}

func TestBusinessUsageCloseTermsRequireEmptyPaidCapacityBasis(t *testing.T) {
	anchor := time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)
	period := validBusinessUsageTestSnapshot(t, contract4paymentus.DataTugBusinessUsagePricing(), anchor, anchor)
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

func TestNativeBusinessCloseTermsRejectBrokenAnchorAndPeriodReads(t *testing.T) {
	ctx := context.Background()
	db := sneatcoretesting.NewMemoryDB()
	anchor := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)
	spaceID := "space-a"
	scope := contract4paymentus.ServicePurchaseScope{Mode: contract4paymentus.ModeLive, SpaceID: spaceID, ServiceID: BusinessProjectServiceID}
	initial := contract4paymentus.ServiceInitialServiceStart{
		Version: 1, Scope: scope, PayerID: spaceID, InitiatingActorID: "buyer", LineageID: "lineage",
		QuoteID: "quote", QuoteFingerprint: "quote-fingerprint", ProviderAccountID: "provider",
		CustomerID: "customer", SessionID: "session", SubscriptionID: "subscription", InvoiceID: "invoice",
		InvoiceLineID: "invoice-line", PaymentID: "payment", PriceID: "price", Currency: "eur",
		PaymentType: "new_subscription", ExecutionGeneration: 1, CashMinor: 9900,
		CashConfirmed: true, RefundsKnown: true, NoRefunds: true, DisputeResolved: true,
		AnchorUTC: anchor, InitialPeriodEndUTC: anchor.AddDate(0, 1, 0), ObservedAtUTC: anchor.Add(time.Minute),
	}
	period := validBusinessUsageTestSnapshot(t, contract4paymentus.DataTugBusinessUsagePricing(), anchor, anchor)
	periods := &businessUsagePeriodStateReader{states: map[contract4paymentus.UsagePeriodRef]contract4paymentus.UsagePeriodState{
		period.Ref: {Snapshot: period},
	}}
	readErr := error(nil)
	initialReader := initialServiceStartReaderFunc(func(_ context.Context, tx dal.ReadTransaction, got contract4paymentus.ServicePurchaseScope) (contract4paymentus.ServiceInitialServiceStart, error) {
		if tx == nil || got != scope {
			t.Fatalf("initial proof read used wrong transaction or scope: %T / %+v", tx, got)
		}
		return initial, readErr
	})
	reader, err := NewNativeBusinessUsageCloseTermsReader(db, initialReader, periods)
	if err != nil {
		t.Fatal(err)
	}
	assertUnavailable := func(name string) {
		t.Helper()
		err := db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
			_, err := reader.ReadBusinessUsageCloseTerms(txCtx, tx, period)
			return err
		})
		if !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
			t.Fatalf("%s close terms error = %v, want unavailable", name, err)
		}
	}

	readErr = errors.New("initial receipt unavailable")
	assertUnavailable("initial read failure")
	readErr = nil
	initial.CashConfirmed = false
	assertUnavailable("invalid paid anchor proof")
	initial.CashConfirmed = true
	badScopeInitial := initial
	badScopeInitial.Scope.Mode = contract4paymentus.ModeTest
	if usagePeriodMatchesOriginalStart(period, badScopeInitial) {
		t.Fatal("period proof accepted an initial receipt from another mode")
	}

	periods.mu.Lock()
	periods.err = errors.New("native period unavailable")
	periods.mu.Unlock()
	assertUnavailable("period read failure")
	periods.mu.Lock()
	periods.err = nil
	state := periods.states[period.Ref]
	state.Snapshot.Config.MonthlyOverageUnitMinor++
	periods.states[period.Ref] = state
	periods.mu.Unlock()
	assertUnavailable("period snapshot mismatch")
	periods.mu.Lock()
	periods.states[period.Ref] = contract4paymentus.UsagePeriodState{Snapshot: period, Closed: true}
	periods.mu.Unlock()
	assertUnavailable("closed period")

	capacityAuthority := businessUsageCapacityReadAuthority{initialStarts: initialReader, periods: periods, period: period}
	operation := contract4paymentus.CapacityOperation{
		Action: contract4paymentus.CapacityRead,
		Owner:  contract4paymentus.CapacityOwner{Mode: contract4paymentus.ModeLive, SpaceID: spaceID, ProductID: BusinessProjectProductID, PayerID: spaceID},
	}
	verifyCapacity := func() error {
		return db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
			return capacityAuthority.VerifyCapacityAction(txCtx, tx, operation)
		})
	}
	initial.CashConfirmed = false
	if err := verifyCapacity(); !errors.Is(err, contract4paymentus.ErrCapacityAuthority) {
		t.Fatalf("capacity reader accepted a broken original paid proof: %v", err)
	}
	initial.CashConfirmed = true
	periods.mu.Lock()
	periods.err = errors.New("native period unavailable")
	periods.mu.Unlock()
	if err := verifyCapacity(); !errors.Is(err, contract4paymentus.ErrCapacityAuthority) {
		t.Fatalf("capacity reader accepted an unreadable native period: %v", err)
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
	paidAccess := validPaymentusBusinessAccess(openAt)
	currentAccess := businessServiceAccessReader(func(_ context.Context, tx dal.ReadTransaction, got contract4paymentus.ServicePurchaseScope) (contract4paymentus.CurrentSpaceServiceAccess, error) {
		accessReads++
		if tx == nil || got != scope {
			t.Fatalf("current paid read used wrong tx/scope: %T / %+v", tx, got)
		}
		access := paidAccess
		access.Scope.SpaceID, access.PayerSpaceID = scope.SpaceID, scope.SpaceID
		return access, nil
	})
	access := newBusinessVerifier(t, currentAccess, func() time.Time { return openAt })
	periods := &businessUsagePeriodStateReader{states: make(map[contract4paymentus.UsagePeriodRef]contract4paymentus.UsagePeriodState)}
	terms, err := NewNativeBusinessUsageCloseTermsReader(db, initialReader, periods)
	if err != nil {
		t.Fatalf("construct native close terms reader: %v", err)
	}
	serverNow := openAt
	currentPricing := contract4paymentus.DataTugBusinessUsagePricing()
	authorityOptions := BusinessUsagePeriodAuthorityOptions{
		Access: access, InitialStarts: initialReader, Periods: periods, CloseTerms: terms,
		Pricing: func() contract4paymentus.UsagePricingConfig { return currentPricing }, Now: func() time.Time { return serverNow },
	}
	authority, err := NewNativeBusinessUsagePeriodAuthority(authorityOptions)
	if err != nil {
		t.Fatal(err)
	}
	defaultPricingOptions := authorityOptions
	defaultPricingOptions.Pricing = nil
	defaultPricingAuthority, err := NewNativeBusinessUsagePeriodAuthority(defaultPricingOptions)
	if err != nil || defaultPricingAuthority.pricing() != contract4paymentus.DataTugBusinessUsagePricing() {
		t.Fatalf("nil pricing resolver did not use the reviewed server default: %v", err)
	}
	for name, mutate := range map[string]func(*BusinessUsagePeriodAuthorityOptions){
		"current access": func(o *BusinessUsagePeriodAuthorityOptions) { o.Access = nil },
		"initial start":  func(o *BusinessUsagePeriodAuthorityOptions) { o.InitialStarts = nil },
		"period reader":  func(o *BusinessUsagePeriodAuthorityOptions) { o.Periods = nil },
		"close terms":    func(o *BusinessUsagePeriodAuthorityOptions) { o.CloseTerms = nil },
		"server clock":   func(o *BusinessUsagePeriodAuthorityOptions) { o.Now = nil },
	} {
		t.Run("authority constructor requires "+name, func(t *testing.T) {
			bad := authorityOptions
			mutate(&bad)
			if got, err := NewNativeBusinessUsagePeriodAuthority(bad); got != nil || !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
				t.Fatalf("authority = %v, error = %v; want unavailable", got, err)
			}
		})
	}
	for name, args := range map[string]struct {
		db      dal.DB
		initial contract4paymentus.InitialServiceStartReader
		periods contract4paymentus.UsagePeriodReader
	}{
		"database":       {initial: initialReader, periods: periods},
		"initial reader": {db: db, periods: periods},
		"period reader":  {db: db, initial: initialReader},
	} {
		t.Run("close terms constructor requires "+name, func(t *testing.T) {
			if got, err := NewNativeBusinessUsageCloseTermsReader(args.db, args.initial, args.periods); got != nil || !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
				t.Fatalf("close terms reader = %v, error = %v; want unavailable", got, err)
			}
		})
	}
	missingPeriod := contract4paymentus.UsagePeriodRef{Scope: contract4paymentus.UsageScope{
		Mode: contract4paymentus.ModeLive, SpaceID: scope.SpaceID, ProductID: BusinessProjectProductID,
		PayerID: scope.SpaceID, ServiceID: BusinessProjectServiceID,
	}, PeriodID: "missing-period"}
	err = db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		_, err := readBusinessUsageCheckpoint(txCtx, tx, missingPeriod)
		return err
	})
	if !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
		t.Fatalf("missing checkpoint did not fail closed: %v", err)
	}
	checkpointReadErr := errors.New("checkpoint storage unavailable")
	err = db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		_, readErr := readBusinessUsageCheckpoint(txCtx, failingBusinessUsageReadTx{ReadTransaction: tx, err: checkpointReadErr}, missingPeriod)
		return readErr
	})
	if !errors.Is(err, checkpointReadErr) {
		t.Fatalf("checkpoint reader did not preserve a real storage error: %v", err)
	}
	ledger := &businessUsageStagedLedger{db: db, authority: authority, periods: periods, operationAt: func() time.Time { return serverNow }, failOpen: true}
	service, err := NewBusinessUsagePeriodService(db, authority, ledger, func() time.Time { return serverNow })
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []struct {
		name      string
		db        dal.DB
		authority *NativeBusinessUsagePeriodAuthority
		ledger    contract4paymentus.UsageLedger
		now       func() time.Time
	}{
		{name: "database", authority: authority, ledger: ledger, now: func() time.Time { return serverNow }},
		{name: "authority", db: db, ledger: ledger, now: func() time.Time { return serverNow }},
		{name: "ledger", db: db, authority: authority, now: func() time.Time { return serverNow }},
		{name: "server clock", db: db, authority: authority, ledger: ledger},
	} {
		t.Run("period service constructor requires "+args.name, func(t *testing.T) {
			if got, err := NewBusinessUsagePeriodService(args.db, args.authority, args.ledger, args.now); got != nil || !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
				t.Fatalf("period service = %v, error = %v; want unavailable", got, err)
			}
		})
	}
	if _, err := service.OpenCurrent(ctx, ""); !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
		t.Fatalf("OpenCurrent accepted an invalid Space identifier: %v", err)
	}
	paidAccess.PaidThroughUTC = openAt
	if _, err := service.OpenCurrent(ctx, scope.SpaceID); !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
		t.Fatalf("OpenCurrent accepted an expired current paid interval: %v", err)
	}
	paidAccess = validPaymentusBusinessAccess(openAt)
	checkpointReadErr = errors.New("checkpoint query failed")
	service.db = failingBusinessUsageReadwriteDB{DB: db, err: checkpointReadErr}
	if _, err := service.OpenCurrent(ctx, scope.SpaceID); !errors.Is(err, checkpointReadErr) {
		t.Fatalf("OpenCurrent did not preserve a checkpoint read failure: %v", err)
	}
	service.db = db
	checkpointInsertErr := errors.New("checkpoint insert rejected")
	service.db = failingBusinessUsageInsertDB{DB: db, err: checkpointInsertErr}
	if _, err := service.OpenCurrent(ctx, scope.SpaceID); !errors.Is(err, checkpointInsertErr) {
		t.Fatalf("OpenCurrent did not preserve a checkpoint insert failure: %v", err)
	}
	service.db = db
	if err := service.prepareClose(ctx, contract4paymentus.UsageCloseRequest{}); !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
		t.Fatalf("prepareClose accepted an invalid period ref: %v", err)
	}
	if _, err := service.Close(ctx, contract4paymentus.UsageCloseRequest{}); !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
		t.Fatalf("Close accepted an invalid period ref: %v", err)
	}
	serverNow = time.Time{}
	if _, err := service.OpenCurrent(ctx, scope.SpaceID); !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
		t.Fatalf("OpenCurrent accepted an invalid server clock: %v", err)
	}
	if err := service.prepareClose(ctx, contract4paymentus.UsageCloseRequest{Ref: missingPeriod}); !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
		t.Fatalf("prepareClose accepted an invalid server clock: %v", err)
	}
	serverNow = openAt
	snapshot, err := contract4paymentus.UsagePeriodForAnchor(
		contract4paymentus.UsageScope{Mode: contract4paymentus.ModeLive, SpaceID: scope.SpaceID, ProductID: BusinessProjectProductID, PayerID: scope.SpaceID, ServiceID: BusinessProjectServiceID},
		currentPricing, anchor, openAt,
	)
	if err != nil {
		t.Fatalf("derive exact test period: %v", err)
	}
	operation := contract4paymentus.UsageOperation{Action: contract4paymentus.UsageOpen, Period: snapshot, ObservedAtUTC: openAt}
	if err := authority.VerifyUsage(ctx, nil, operation); !errors.Is(err, contract4paymentus.ErrUsageAuthority) {
		t.Fatalf("usage authority accepted a missing caller transaction: %v", err)
	}
	if err := authority.VerifyUsage(nil, nil, operation); !errors.Is(err, contract4paymentus.ErrUsageAuthority) {
		t.Fatalf("usage authority accepted a missing request context: %v", err)
	}
	if _, err := terms.ReadBusinessUsageCloseTerms(nil, nil, snapshot); !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
		t.Fatalf("close terms reader accepted missing context/transaction: %v", err)
	}
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageOpen, ObservedAtUTC: openAt})
	}); !errors.Is(err, contract4paymentus.ErrUsageAuthority) {
		t.Fatalf("usage authority accepted a missing period snapshot: %v", err)
	}
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageOpen, Period: snapshot, ObservedAtUTC: openAt})
	}); err == nil {
		t.Fatal("native Open authority accepted a period before its checkpoint was staged")
	}
	err = db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		_, _, _, readErr := authority.readCurrentOpenFacts(txCtx, tx, scope.SpaceID, time.Time{})
		return readErr
	})
	if !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
		t.Fatalf("Open facts accepted an invalid occurrence time: %v", err)
	}
	if _, err := service.OpenCurrent(ctx, scope.SpaceID); err == nil {
		t.Fatal("lost Open response was hidden")
	}
	periods.mu.Lock()
	state := periods.states[snapshot.Ref]
	state.DistinctMAU = 1 // an Opening period must not contain admitted activity
	periods.states[snapshot.Ref] = state
	periods.mu.Unlock()
	if _, err := service.OpenCurrent(ctx, scope.SpaceID); err == nil {
		t.Fatal("Open retry made an Opening period with existing activity ready")
	}
	periods.mu.Lock()
	state = periods.states[snapshot.Ref]
	state.DistinctMAU = 0
	periods.states[snapshot.Ref] = state
	periods.mu.Unlock()
	checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(snapshot.Ref)
	if err := db.Get(ctx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.State != models4datatug.QueryActivityCheckpointOpening {
		t.Fatalf("partial Open did not remain fenced: %+v / %v", checkpoint, err)
	}
	// A reviewed price change after the native Open must not reprice the
	// already-open period or prevent a lost-response retry from confirming it.
	currentPricing.Version = "2026-11-10"
	currentPricing.ConfigID = "datatug-business-usage-next"
	currentPricing.MonthlyOverageUnitMinor += 100
	validRevisedPricing := currentPricing
	currentPricing.Currency = "gbp"
	err = db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		_, _, _, readErr := authority.readCurrentOpenFacts(txCtx, tx, scope.SpaceID, openAt)
		return readErr
	})
	if err == nil {
		t.Fatal("new Open accepted an invalid server pricing configuration")
	}
	currentPricing = validRevisedPricing
	periods.mu.Lock()
	committedPeriod := periods.states[snapshot.Ref]
	delete(periods.states, snapshot.Ref)
	periods.mu.Unlock()
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageOpen, Period: snapshot, ObservedAtUTC: openAt})
	}); err == nil {
		t.Fatal("Open authority accepted a frozen snapshot that the native ledger no longer contains")
	}
	if _, err := service.OpenCurrent(ctx, scope.SpaceID); err == nil {
		t.Fatal("Opening checkpoint alone pinned superseded pricing without a native period")
	}
	periods.mu.Lock()
	periods.states[snapshot.Ref] = committedPeriod
	periods.mu.Unlock()
	opened, err := service.OpenCurrent(ctx, scope.SpaceID)
	if err != nil || opened != snapshot || opened.Config.Version != "2026-10-10" {
		t.Fatalf("Open retry did not confirm exact ledger snapshot: %+v / %v", opened, err)
	}
	if err := db.Get(ctx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.State != models4datatug.QueryActivityCheckpointReady || checkpoint.AnchorUTC != anchor {
		t.Fatalf("confirmed Open checkpoint: %+v / %v", checkpoint, err)
	}
	readyCheckpoint := *checkpoint
	checkpoint.AnchorProofDigest = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(txCtx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenCurrent(ctx, scope.SpaceID); err == nil {
		t.Fatal("Open replay accepted a checkpoint with a changed immutable anchor proof")
	}
	*checkpoint = readyCheckpoint
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(txCtx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	err = db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		return (businessUsageCapacityReadAuthority{initialStarts: initialReader, periods: periods, period: snapshot}).VerifyCapacityAction(
			txCtx, tx, contract4paymentus.CapacityOperation{Action: contract4paymentus.CapacityAction("settle")},
		)
	})
	if !errors.Is(err, contract4paymentus.ErrCapacityAuthority) {
		t.Fatalf("non-read capacity operation passed the read-only authority: %v", err)
	}
	err = db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		return (businessUsageCapacityReadAuthority{initialStarts: initialReader, periods: periods, period: snapshot}).VerifyCapacityAction(
			txCtx, tx, contract4paymentus.CapacityOperation{
				Action: contract4paymentus.CapacityRead,
				Owner:  contract4paymentus.CapacityOwner{Mode: contract4paymentus.ModeLive, SpaceID: scope.SpaceID, ProductID: "other-product", PayerID: scope.SpaceID},
			},
		)
	})
	if !errors.Is(err, contract4paymentus.ErrCapacityAuthority) {
		t.Fatalf("capacity read for another product passed the authority: %v", err)
	}
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: "unknown", Period: snapshot, ObservedAtUTC: openAt})
	}); err == nil {
		t.Fatal("unknown usage-authority action was accepted")
	}
	for _, action := range []contract4paymentus.UsageAction{contract4paymentus.UsageAdmit, contract4paymentus.UsageRecordLate} {
		if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
			return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: action, Period: snapshot})
		}); err == nil {
			t.Fatalf("%s without receipt proof was accepted", action)
		}
	}
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageOpen, Period: snapshot})
	}); err == nil {
		t.Fatal("Open without a canonical observation time was accepted")
	}
	paidAccess.PaidThroughUTC = openAt
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageOpen, Period: snapshot, ObservedAtUTC: openAt})
	}); err == nil {
		t.Fatal("Open accepted an expired current paid interval")
	}
	paidAccess = validPaymentusBusinessAccess(openAt)
	initial.CashConfirmed = false
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageOpen, Period: snapshot, ObservedAtUTC: openAt})
	}); err == nil {
		t.Fatal("Open accepted an invalid immutable original-service proof")
	}
	initial.CashConfirmed = true
	serverNow = openAt.Add(-time.Second)
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageOpen, Period: snapshot, ObservedAtUTC: openAt})
	}); err == nil {
		t.Fatal("Open accepted stale evidence checked after the requested observation time")
	}
	serverNow = openAt
	periods.mu.Lock()
	state = periods.states[snapshot.Ref]
	state.DistinctMAU = 2 // a ready native period may already contain admitted users
	periods.states[snapshot.Ref] = state
	periods.mu.Unlock()
	replayed, err := service.OpenCurrent(ctx, scope.SpaceID)
	if err != nil || replayed != snapshot {
		t.Fatalf("ready Open replay rejected a period with activity: %+v / %v", replayed, err)
	}
	periods.mu.Lock()
	state = periods.states[snapshot.Ref]
	if state.DistinctMAU != 2 {
		periods.mu.Unlock()
		t.Fatalf("ready Open replay erased native MAU: %d", state.DistinctMAU)
	}
	state.DistinctMAU = 0
	periods.states[snapshot.Ref] = state
	periods.mu.Unlock()
	var termsForClose BusinessUsageCloseTerms
	err = db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		var readErr error
		termsForClose, readErr = terms.ReadBusinessUsageCloseTerms(txCtx, tx, snapshot)
		return readErr
	})
	if err != nil {
		t.Fatalf("read server-derived close terms: %v", err)
	}
	initial.AnchorUTC = anchor.Add(-time.Hour)
	err = db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		_, readErr := terms.ReadBusinessUsageCloseTerms(txCtx, tx, snapshot)
		return readErr
	})
	initial.AnchorUTC = anchor
	if err == nil {
		t.Fatal("native close terms accepted a period detached from the immutable original anchor")
	}
	periods.mu.Lock()
	state = periods.states[snapshot.Ref]
	state.Closed = true
	periods.states[snapshot.Ref] = state
	periods.mu.Unlock()
	err = db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		_, readErr := terms.ReadBusinessUsageCloseTerms(txCtx, tx, snapshot)
		return readErr
	})
	periods.mu.Lock()
	state = periods.states[snapshot.Ref]
	state.Closed = false
	periods.states[snapshot.Ref] = state
	periods.mu.Unlock()
	if err == nil {
		t.Fatal("close terms accepted an already closed native usage period")
	}
	request := contract4paymentus.UsageCloseRequest{
		Ref: snapshot.Ref, CloseID: "close-business-period", Capacity: termsForClose.Capacity,
		BaseEvent: termsForClose.BaseEvent, BillMonthlyOverage: termsForClose.BillMonthlyOverage, DiscountPercent: termsForClose.DiscountPercent,
	}
	if err := db.Get(ctx, checkpointRecord); err != nil {
		t.Fatal(err)
	}
	checkpoint.State = models4datatug.QueryActivityCheckpointClosing
	checkpoint.CloseRequest = request
	checkpoint.UpdatedAtUTC = serverNow
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(txCtx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenCurrent(ctx, scope.SpaceID); err == nil {
		t.Fatal("Open replay reopened a period after the immutable close fence was written")
	}
	checkpoint.State = models4datatug.QueryActivityCheckpointReady
	checkpoint.CloseRequest = contract4paymentus.UsageCloseRequest{}
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(txCtx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageClose, Period: snapshot, Close: request, ObservedAtUTC: openAt})
	}); err == nil {
		t.Fatal("Close before the 48-hour completeness grace was accepted")
	}
	serverNow = snapshot.EndUTC.Add(BusinessUsageCloseGrace)
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageClose, Period: snapshot, Close: request, ObservedAtUTC: serverNow})
	}); err == nil {
		t.Fatal("native Close authority accepted a ready checkpoint without its close fence")
	}
	if err := db.Get(ctx, checkpointRecord); err != nil {
		t.Fatal(err)
	}
	checkpoint.AcceptedCount, checkpoint.DeliveredThrough = 1, 0
	checkpoint.UpdatedAtUTC = serverNow
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(txCtx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.prepareClose(ctx, request); err == nil {
		t.Fatal("Close ignored an accepted receipt that was not delivered")
	}
	if err := db.Get(ctx, checkpointRecord); err != nil {
		t.Fatal(err)
	}
	checkpoint.AcceptedCount, checkpoint.DeliveredThrough = 0, 0
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(txCtx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	wrongTerms := request
	wrongTerms.DiscountPercent = 1
	if err := service.prepareClose(ctx, wrongTerms); err == nil {
		t.Fatal("Close accepted caller-selected discount terms instead of native terms")
	}
	periods.mu.Lock()
	state = periods.states[snapshot.Ref]
	state.DistinctMAU = 1
	periods.states[snapshot.Ref] = state
	periods.mu.Unlock()
	if err := service.prepareClose(ctx, request); err == nil {
		t.Fatal("Close accepted native MAU that differs from the delivered receipt count")
	}
	periods.mu.Lock()
	state = periods.states[snapshot.Ref]
	state.DistinctMAU = 0
	periods.states[snapshot.Ref] = state
	periods.mu.Unlock()
	initial.CashConfirmed = false
	if err := service.prepareClose(ctx, request); err == nil {
		t.Fatal("Close accepted an invalid original paid proof")
	}
	initial.CashConfirmed = true
	periods.mu.Lock()
	state = periods.states[snapshot.Ref]
	state.Closed = true
	periods.states[snapshot.Ref] = state
	periods.mu.Unlock()
	if err := service.prepareClose(ctx, request); err == nil {
		t.Fatal("a ready checkpoint admitted a natively closed period")
	}
	periods.mu.Lock()
	state = periods.states[snapshot.Ref]
	state.Closed = false
	periods.states[snapshot.Ref] = state
	periods.mu.Unlock()
	wrongRef := request
	wrongRef.Ref.PeriodID = "another-period"
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageClose, Period: snapshot, Close: wrongRef, ObservedAtUTC: serverNow})
	}); err == nil {
		t.Fatal("native Close authority accepted a request bound to another period")
	}
	initial.CashConfirmed = false
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageClose, Period: snapshot, Close: request, ObservedAtUTC: serverNow})
	}); err == nil {
		t.Fatal("native Close authority accepted an invalid original paid proof")
	}
	initial.CashConfirmed = true
	if err := db.Get(ctx, checkpointRecord); err != nil {
		t.Fatal(err)
	}
	checkpoint.State = models4datatug.QueryActivityCheckpointClosing
	checkpoint.CloseRequest = request
	checkpoint.UpdatedAtUTC = serverNow
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(txCtx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	wrongClosingRequest := request
	wrongClosingRequest.CloseID = "different-close"
	if err := service.prepareClose(ctx, wrongClosingRequest); err == nil {
		t.Fatal("retry changed the immutable close request after entering Closing")
	}
	checkpoint.State = models4datatug.QueryActivityCheckpointClosed
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(txCtx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.prepareClose(ctx, request); err == nil {
		t.Fatal("a Closed checkpoint was accepted while the native period remained open")
	}
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageClose, Period: snapshot, Close: request, ObservedAtUTC: serverNow})
	}); err == nil {
		t.Fatal("native Close authority accepted a Closed checkpoint while the usage period remained open")
	}
	checkpoint.State = models4datatug.QueryActivityCheckpointClosing
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(txCtx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	originalCloseTerms := authority.closeTerms
	authority.closeTerms = businessUsageCloseTermsFunc(func(context.Context, dal.ReadTransaction, contract4paymentus.UsagePeriodSnapshot) (BusinessUsageCloseTerms, error) {
		return BusinessUsageCloseTerms{Capacity: termsForClose.Capacity, BaseEvent: contract4paymentus.UsageBaseAnnual}, nil
	})
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageClose, Period: snapshot, Close: request, ObservedAtUTC: serverNow})
	}); err == nil {
		t.Fatal("native Close authority accepted caller terms different from its trusted terms reader")
	}
	authority.closeTerms = originalCloseTerms
	for _, mismatch := range []struct {
		name        string
		accepted    int64
		nativeUsers int64
	}{
		{name: "native undercount", accepted: 2, nativeUsers: 1},
		{name: "native overcount", accepted: 1, nativeUsers: 2},
	} {
		t.Run(mismatch.name, func(t *testing.T) {
			if err := db.Get(ctx, checkpointRecord); err != nil {
				t.Fatal(err)
			}
			checkpoint.State = models4datatug.QueryActivityCheckpointClosing
			checkpoint.CloseRequest = request
			checkpoint.AcceptedCount, checkpoint.DeliveredThrough = mismatch.accepted, mismatch.accepted
			checkpoint.UpdatedAtUTC = serverNow
			if err := checkpoint.Validate(); err != nil {
				t.Fatalf("test close checkpoint invalid: %v", err)
			}
			if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
				return tx.Set(txCtx, checkpointRecord)
			}); err != nil {
				t.Fatal(err)
			}
			periods.mu.Lock()
			state := periods.states[snapshot.Ref]
			state.DistinctMAU = mismatch.nativeUsers
			periods.states[snapshot.Ref] = state
			periods.mu.Unlock()
			err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
				return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{Action: contract4paymentus.UsageClose, Period: snapshot, Close: request, ObservedAtUTC: serverNow})
			})
			if err == nil {
				t.Fatalf("close accepted accepted-count=%d with native MAU=%d", mismatch.accepted, mismatch.nativeUsers)
			}
		})
	}
	if err := db.Get(ctx, checkpointRecord); err != nil {
		t.Fatal(err)
	}
	checkpoint.State = models4datatug.QueryActivityCheckpointReady
	checkpoint.CloseRequest = contract4paymentus.UsageCloseRequest{}
	checkpoint.AcceptedCount, checkpoint.DeliveredThrough = 0, 0
	checkpoint.UpdatedAtUTC = serverNow
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(txCtx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	periods.mu.Lock()
	state = periods.states[snapshot.Ref]
	state.DistinctMAU = 0
	periods.states[snapshot.Ref] = state
	periods.mu.Unlock()
	ledger.failClose = true
	if _, err := service.Close(ctx, request); err == nil {
		t.Fatal("service hid a native ledger close failure before commit")
	}
	ledger.failCloseAfterCommit = true
	if _, err := service.Close(ctx, request); err == nil {
		t.Fatal("lost Close response was hidden")
	}
	if err := db.Get(ctx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.State != models4datatug.QueryActivityCheckpointClosing {
		t.Fatalf("lost Close response did not retain the retry fence: %+v / %v", checkpoint, err)
	}
	closed, err := service.Close(ctx, request)
	if err != nil || closed.Request != request || closed.Period != snapshot {
		t.Fatalf("close at the authorized boundary: %+v / %v", closed, err)
	}
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{
			Action: contract4paymentus.UsageClose, Period: snapshot, Close: request, ObservedAtUTC: serverNow,
		})
	}); err != nil {
		t.Fatalf("native close authority rejected an exact closed replay: %v", err)
	}
	changedClosedRequest := request
	changedClosedRequest.CloseID = "different-close"
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return authority.VerifyUsage(txCtx, tx, contract4paymentus.UsageOperation{
			Action: contract4paymentus.UsageClose, Period: snapshot, Close: changedClosedRequest, ObservedAtUTC: serverNow,
		})
	}); err == nil {
		t.Fatal("native close authority accepted a different request for the immutable closed period")
	}
	if accessReads != 20 {
		t.Fatalf("historical Close re-read current paid ownership: current access calls=%d, want the 20 earlier Open and denied-Open reads", accessReads)
	}
	if err := db.Get(ctx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.State != models4datatug.QueryActivityCheckpointClosed || checkpoint.AcceptedCount != 0 || checkpoint.DeliveredThrough != 0 {
		t.Fatalf("closed zero-activity period checkpoint: %+v / %v", checkpoint, err)
	}
	periods.mu.Lock()
	state = periods.states[snapshot.Ref]
	state.DistinctMAU = 1
	periods.states[snapshot.Ref] = state
	periods.mu.Unlock()
	if _, err := service.Close(ctx, request); err == nil {
		t.Fatal("closed replay accepted native MAU that disagreed with the frozen receipt count")
	}
	periods.mu.Lock()
	state = periods.states[snapshot.Ref]
	state.DistinctMAU = 0
	periods.states[snapshot.Ref] = state
	periods.mu.Unlock()
	closedAgain, err := service.Close(ctx, request)
	if err != nil || closedAgain != closed {
		t.Fatalf("close replay did not preserve immutable closure: %+v / %+v / %v", closedAgain, closed, err)
	}
}
