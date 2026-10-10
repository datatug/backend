package facade4datatug

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

type lifecyclePeriodOpenerFunc func(context.Context, string) (contract4paymentus.UsagePeriodSnapshot, error)

func (f lifecyclePeriodOpenerFunc) OpenCurrent(ctx context.Context, spaceID string) (contract4paymentus.UsagePeriodSnapshot, error) {
	return f(ctx, spaceID)
}

type lifecycleQueryActivityService struct {
	order    *[]string
	issueErr error
}

func (s lifecycleQueryActivityService) IssueContext(context.Context, string, string, string) (QueryActivityContextResponse, error) {
	*s.order = append(*s.order, "issue")
	return QueryActivityContextResponse{ContextID: "context"}, s.issueErr
}

func (s lifecycleQueryActivityService) Report(context.Context, string, string, QueryActivityReport) (QueryActivityReportResult, error) {
	return QueryActivityReportResult{Accepted: true, ReceiptID: "receipt"}, nil
}

func TestBusinessUsageLifecycleGroupPagesResumeByFullSpaceDocumentPath(t *testing.T) {
	ctx := context.Background()
	db := sneatcoretesting.NewMemoryDB()
	worker := &BusinessUsageLifecycleWorker{query: db}
	anchor := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, spaceID := range []string{"space-a", "space-b"} {
		period := lifecycleTestPeriod(t, spaceID, anchor)
		recordValue, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(period.Ref)
		*checkpoint = models4datatug.QueryActivityPeriodCheckpoint{
			Version: 1, Period: period.Ref, Snapshot: period, AnchorUTC: anchor,
			AnchorProofDigest: strings.Repeat("a", 64), State: models4datatug.QueryActivityCheckpointOpening,
			UpdatedAtUTC: anchor,
		}
		if err := checkpoint.Validate(); err != nil {
			t.Fatalf("test checkpoint for %s invalid: %v", spaceID, err)
		}
		if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
			return tx.Insert(txCtx, recordValue)
		}); err != nil {
			t.Fatalf("insert checkpoint for %s: %v", spaceID, err)
		}
	}
	first, hasMore, err := worker.readGroupPage(ctx, models4datatug.QueryActivityPeriodCheckpointsCollection, "", 1, false)
	if err != nil || len(first) != 1 || !hasMore {
		t.Fatalf("first checkpoint page: len=%d hasMore=%t err=%v", len(first), hasMore, err)
	}
	firstPath := first[0].Key().String()
	cursorState := models4datatug.BusinessUsageWorkerState{Version: 1, CheckpointAfterPath: firstPath, UpdatedAtUTC: anchor}
	if !strings.HasPrefix(firstPath, "spaces/space-") || !strings.Contains(firstPath, "/ext/datatug/"+models4datatug.QueryActivityPeriodCheckpointsCollection+"/") ||
		cursorState.Validate() != nil {
		t.Fatalf("checkpoint cursor omitted full Space path: %q", firstPath)
	}
	all, hasMore, err := worker.readGroupPage(ctx, models4datatug.QueryActivityPeriodCheckpointsCollection, "", 10, false)
	if err != nil || hasMore || len(all) != 2 {
		t.Fatalf("checkpoint group page: len=%d hasMore=%t err=%v", len(all), hasMore, err)
	}
	paths := map[string]bool{}
	for _, row := range all {
		paths[row.Key().String()] = true
	}
	if !paths[firstPath] || len(paths) != 2 {
		t.Fatalf("collection-group cursor identity is not full-path unique: %#v", paths)
	}
}

func TestBusinessUsageLifecycleWorkerRecoversOpeningAfterRolloverAndHoldsMissingNativePeriod(t *testing.T) {
	cases := []struct {
		name                string
		nativeCommitted     bool
		currentAccessActive bool
	}{
		{name: "native Open committed before lost response", nativeCommitted: true},
		{name: "missing native period after access ended"},
		{name: "missing native period cannot become a new period after rollover", currentAccessActive: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newBusinessOpeningRecoveryFixture(t, test.nativeCommitted, test.currentAccessActive)
			result, err := f.worker.Run(context.Background(), 10)
			if err != nil {
				t.Fatalf("first lifecycle pass: %+v / %v", result, err)
			}
			checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(f.period.Ref)
			if err := f.db.Get(context.Background(), checkpointRecord); err != nil || checkpoint.Validate() != nil {
				t.Fatalf("read Opening checkpoint: %+v / %v", checkpoint, err)
			}
			if !test.nativeCommitted {
				if checkpoint.State != models4datatug.QueryActivityCheckpointOpening || result.Failures == 0 || f.accessRead() == 0 {
					t.Fatalf("missing native period was not held behind current-access failure: checkpoint=%+v result=%+v accessReads=%d", checkpoint, result, f.accessRead())
				}
				newCheckpointRecord, _ := models4datatug.NewQueryActivityPeriodCheckpointRecord(f.currentRef)
				if err := f.db.Get(context.Background(), newCheckpointRecord); !record.IsNotFound(err) {
					t.Fatalf("old Opening checkpoint created a different current period after rollover: %v", err)
				}
				return
			}
			if checkpoint.State != models4datatug.QueryActivityCheckpointReady || result.PeriodsOpened != 1 || result.Failures != 0 || f.accessRead() != 0 {
				t.Fatalf("lost native Open was not recovered independently of ended current access: checkpoint=%+v result=%+v accessReads=%d", checkpoint, result, f.accessRead())
			}
			closedResult, err := f.worker.Run(context.Background(), 10)
			if err != nil || closedResult.PeriodsClosed != 1 || closedResult.Failures != 0 {
				t.Fatalf("historical period was not closed after recovery: %+v / %v", closedResult, err)
			}
			if f.accessRead() != 0 {
				t.Fatalf("historical close consulted expired current access %d times", f.accessRead())
			}
		})
	}
}

func TestBusinessUsageLifecycleWorkersRaceOpeningCheckpointAndCursorsSafely(t *testing.T) {
	f := newBusinessOpeningRecoveryFixture(t, true, false)
	var results [2]BusinessUsageLifecycleResult
	var errs [2]error
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = f.worker.Run(context.Background(), 10)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent worker %d returned error: %+v / %v", i, results[i], err)
		}
		if results[i].Failures != 0 {
			t.Fatalf("concurrent worker %d reported an avoidable checkpoint race: %+v", i, results[i])
		}
	}
	checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(f.period.Ref)
	if err := f.db.Get(context.Background(), checkpointRecord); err != nil || checkpoint.Validate() != nil ||
		(checkpoint.State != models4datatug.QueryActivityCheckpointReady && checkpoint.State != models4datatug.QueryActivityCheckpointClosed) {
		t.Fatalf("concurrent recovery corrupted or lost the checkpoint: %+v / %v", checkpoint, err)
	}
	stateRecord, state := models4datatug.NewBusinessUsageWorkerStateRecord()
	if err := f.db.Get(context.Background(), stateRecord); err != nil || state.Validate() != nil {
		t.Fatalf("concurrent collection cursors are invalid: %+v / %v", state, err)
	}
	if checkpoint.State == models4datatug.QueryActivityCheckpointReady {
		closed, err := f.worker.Run(context.Background(), 10)
		if err != nil || closed.PeriodsClosed != 1 || closed.Failures != 0 {
			t.Fatalf("cursor race prevented historical close recovery: %+v / %v", closed, err)
		}
	}
}

type businessOpeningRecoveryFixture struct {
	db         dal.DB
	worker     *BusinessUsageLifecycleWorker
	period     contract4paymentus.UsagePeriodSnapshot
	currentRef contract4paymentus.UsagePeriodRef
	accessRead func() int
}

func newBusinessOpeningRecoveryFixture(t *testing.T, nativeCommitted, currentAccessActive bool) businessOpeningRecoveryFixture {
	t.Helper()
	ctx := context.Background()
	db := sneatcoretesting.NewMemoryDB()
	spaceID := "recovery-space"
	anchor := time.Date(2026, 7, 3, 9, 30, 0, 0, time.UTC)
	period := lifecycleTestPeriod(t, spaceID, anchor)
	now := period.EndUTC.Add(BusinessUsageCloseGrace)
	currentPeriod, err := contract4paymentus.UsagePeriodForAnchor(period.Ref.Scope, period.Config, anchor, now)
	if err != nil {
		t.Fatalf("derive rolled-forward period: %v", err)
	}
	scope := contract4paymentus.ServicePurchaseScope{Mode: contract4paymentus.ModeLive, SpaceID: spaceID, ServiceID: BusinessProjectServiceID}
	initial := contract4paymentus.ServiceInitialServiceStart{
		Version: 1, Scope: scope, PayerID: spaceID, InitiatingActorID: "buyer", LineageID: "original-lineage",
		QuoteID: "original-quote", QuoteFingerprint: "original-fingerprint", ProviderAccountID: "provider",
		CustomerID: "original-customer", SessionID: "original-session", SubscriptionID: "original-subscription",
		InvoiceID: "original-invoice", InvoiceLineID: "original-line", PaymentID: "original-payment",
		PriceID: "business-price", Currency: "eur", PaymentType: "new_subscription", ExecutionGeneration: 1,
		CashMinor: 9900, CashConfirmed: true, RefundsKnown: true, NoRefunds: true, DisputeResolved: true,
		AnchorUTC: anchor, InitialPeriodEndUTC: anchor.AddDate(0, 1, 0), ObservedAtUTC: anchor.Add(time.Minute),
	}
	initialReader := initialServiceStartReaderFunc(func(_ context.Context, tx dal.ReadTransaction, got contract4paymentus.ServicePurchaseScope) (contract4paymentus.ServiceInitialServiceStart, error) {
		if tx == nil || got != scope {
			t.Fatalf("historical initial proof used wrong transaction/scope: %T / %+v", tx, got)
		}
		return initial, nil
	})
	accessReads := 0
	accessReader := businessServiceAccessReader(func(_ context.Context, tx dal.ReadTransaction, got contract4paymentus.ServicePurchaseScope) (contract4paymentus.CurrentSpaceServiceAccess, error) {
		accessReads++
		if tx == nil || got != scope || !currentAccessActive {
			return contract4paymentus.CurrentSpaceServiceAccess{}, errors.New("current service access has ended")
		}
		access := validPaymentusBusinessAccess(now)
		access.Scope, access.PayerSpaceID = scope, spaceID
		return access, nil
	})
	access := newBusinessVerifier(t, accessReader, func() time.Time { return now })
	periods := &businessUsagePeriodStateReader{states: make(map[contract4paymentus.UsagePeriodRef]contract4paymentus.UsagePeriodState)}
	if nativeCommitted {
		periods.states[period.Ref] = contract4paymentus.UsagePeriodState{Snapshot: period}
	}
	terms, err := NewNativeBusinessUsageCloseTermsReader(db, initialReader, periods)
	if err != nil {
		t.Fatal(err)
	}
	pricing := contract4paymentus.DataTugBusinessUsagePricing()
	authority, err := NewNativeBusinessUsagePeriodAuthority(BusinessUsagePeriodAuthorityOptions{
		Access: access, InitialStarts: initialReader, Periods: periods, CloseTerms: terms,
		Pricing: func() contract4paymentus.UsagePricingConfig { return pricing }, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger := &businessUsageStagedLedger{db: db, authority: authority, periods: periods, operationAt: func() time.Time { return now }}
	service, err := NewBusinessUsagePeriodService(db, authority, ledger, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(period.Ref)
	*checkpoint = models4datatug.QueryActivityPeriodCheckpoint{
		Version: 1, Period: period.Ref, Snapshot: period, AnchorUTC: anchor,
		AnchorProofDigest: businessInitialStartDigest(initial), State: models4datatug.QueryActivityCheckpointOpening,
		UpdatedAtUTC: anchor,
	}
	if err := checkpoint.Validate(); err != nil {
		t.Fatalf("invalid recovery checkpoint: %v", err)
	}
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Insert(txCtx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := NewBusinessUsageLifecycleWorker(db, db, service, &QueryActivityService{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return businessOpeningRecoveryFixture{db: db, worker: worker, period: period, currentRef: currentPeriod.Ref, accessRead: func() int { return accessReads }}
}

func TestBusinessUsageLifecyclePendingCollectionGroupUsesSpaceOwnedPaths(t *testing.T) {
	ctx := context.Background()
	db := sneatcoretesting.NewMemoryDB()
	worker := &BusinessUsageLifecycleWorker{query: db}
	anchor := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for i, deliveryState := range []string{models4datatug.QueryActivityPendingStatePending, models4datatug.QueryActivityPendingStateDelivered} {
		spaceID := []string{"space-a", "space-b"}[i]
		period := lifecycleTestPeriod(t, spaceID, anchor)
		userID := "actor-" + string(rune('a'+i))
		activity := contract4paymentus.UsageActivity{
			Ref: period.Ref, SourceID: models4datatug.QueryActivitySourceID, EventID: "event-" + userID,
			UserID: userID, EvidenceDigest: strings.Repeat("b", 64), OccurredAtUTC: anchor.Add(time.Hour),
		}
		id := models4datatug.NewQueryActivityReceiptID(userID, period.Ref)
		recordValue, pending := models4datatug.NewQueryActivityPendingRecord(spaceID, id)
		*pending = models4datatug.QueryActivityPending{
			Version: 1, ReceiptID: id, SpaceID: spaceID, Period: period.Ref, Activity: activity,
			Sequence: 1, DeliveryState: deliveryState, UpdatedAtUTC: anchor.Add(time.Hour),
		}
		if deliveryState == models4datatug.QueryActivityPendingStateDelivered {
			pending.DeliveryProofDigest = strings.Repeat("c", 64)
			pending.DeliveredAtUTC = anchor.Add(2 * time.Hour)
		}
		if err := pending.Validate(); err != nil {
			t.Fatalf("test pending record invalid: %v", err)
		}
		if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
			return tx.Insert(txCtx, recordValue)
		}); err != nil {
			t.Fatalf("insert %s pending row: %v", deliveryState, err)
		}
	}
	page, hasMore, err := worker.readGroupPage(ctx, models4datatug.QueryActivityPendingCollection, "", 10, true)
	if err != nil || hasMore || len(page) != 1 {
		t.Fatalf("pending discovery included delivered rows or missed pending row: len=%d hasMore=%t err=%v", len(page), hasMore, err)
	}
	if pending, ok := page[0].Data().(*models4datatug.QueryActivityPending); !ok || pending.DeliveryState != models4datatug.QueryActivityPendingStatePending {
		t.Fatalf("pending discovery returned unexpected row: %#v", page[0].Data())
	}
}

func TestBusinessUsageLifecycleCollectionGroupCandidatesEnforceSpaceOwnership(t *testing.T) {
	anchor := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	period := lifecycleTestPeriod(t, "space-a", anchor)
	checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(period.Ref)
	*checkpoint = models4datatug.QueryActivityPeriodCheckpoint{
		Version: 1, Period: period.Ref, Snapshot: period, AnchorUTC: anchor,
		AnchorProofDigest: strings.Repeat("a", 64), State: models4datatug.QueryActivityCheckpointOpening, UpdatedAtUTC: anchor,
	}
	if err := checkpoint.Validate(); err != nil {
		t.Fatal(err)
	}
	spaceID, candidate, ok := businessUsageCheckpointCandidate(checkpointRecord)
	if !ok || spaceID != "space-a" || candidate != *checkpoint {
		t.Fatalf("valid checkpoint candidate: %q %+v %t", spaceID, candidate, ok)
	}
	wrongParent := record.NewRecordWithData(record.NewKeyWithID(models4datatug.QueryActivityPeriodCheckpointsCollection, "checkpoint"), checkpoint)
	if _, _, ok := businessUsageCheckpointCandidate(wrongParent); ok {
		t.Fatal("checkpoint outside a Space extension was accepted")
	}
	wrongData := record.NewRecordWithData(checkpointRecord.Key(), "not a checkpoint")
	if _, _, ok := businessUsageCheckpointCandidate(wrongData); ok {
		t.Fatal("checkpoint row with unexpected data type was accepted")
	}
	activity := contract4paymentus.UsageActivity{
		Ref: period.Ref, SourceID: models4datatug.QueryActivitySourceID, EventID: "candidate-event",
		UserID: "candidate-actor", EvidenceDigest: strings.Repeat("b", 64), OccurredAtUTC: anchor.Add(time.Minute),
	}
	id := models4datatug.NewQueryActivityReceiptID(activity.UserID, period.Ref)
	pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("space-a", id)
	*pending = models4datatug.QueryActivityPending{
		Version: 1, ReceiptID: id, SpaceID: "space-a", Period: period.Ref, Activity: activity,
		Sequence: 1, DeliveryState: models4datatug.QueryActivityPendingStatePending, UpdatedAtUTC: anchor,
	}
	if err := pending.Validate(); err != nil {
		t.Fatal(err)
	}
	if got, ok := businessUsagePendingSpace(pendingRecord); !ok || got != "space-a" {
		t.Fatalf("valid pending candidate: %q %t", got, ok)
	}
	wrongSpaceRecord, wrongSpace := models4datatug.NewQueryActivityPendingRecord("space-b", id)
	*wrongSpace = *pending
	if _, ok := businessUsagePendingSpace(wrongSpaceRecord); ok {
		t.Fatal("pending receipt whose payload names a different Space was accepted")
	}
	wrongPendingData := record.NewRecordWithData(pendingRecord.Key(), "not pending")
	if _, ok := businessUsagePendingSpace(wrongPendingData); ok {
		t.Fatal("pending row with unexpected data type was accepted")
	}
	for _, key := range []*record.Key{
		record.NewKeyWithParentAndID(record.NewKeyWithID("spaces", "bad space"), "ext", "datatug"),
		record.NewKeyWithParentAndID(record.NewKeyWithParentAndID(record.NewKeyWithID("spaces", "space-a"), "ext", "wrong-extension"), models4datatug.QueryActivityPendingCollection, id),
		record.NewKeyWithParentAndID(record.NewKeyWithParentAndID(record.NewKeyWithParentAndID(record.NewKeyWithID("orgs", "org-a"), "spaces", "space-a"), "ext", "datatug"), models4datatug.QueryActivityPendingCollection, id),
	} {
		row := record.NewRecordWithData(key, pending)
		if _, ok := businessUsagePendingSpace(row); ok {
			t.Fatalf("pending row with malformed collection-group path was accepted: %q", key.String())
		}
	}
}

func TestBusinessUsageLifecycleWorkerCountsForeignCheckpointAndUnscopedPendingRows(t *testing.T) {
	ctx := context.Background()
	db := sneatcoretesting.NewMemoryDB()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	period := lifecycleTestPeriod(t, "space-a", now)
	_, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(period.Ref)
	*checkpoint = models4datatug.QueryActivityPeriodCheckpoint{
		Version: 1, Period: period.Ref, Snapshot: period, AnchorUTC: period.AnchorUTC,
		AnchorProofDigest: strings.Repeat("a", 64), State: models4datatug.QueryActivityCheckpointOpening,
		UpdatedAtUTC: now,
	}
	if err := checkpoint.Validate(); err != nil {
		t.Fatal(err)
	}
	foreignSpace := record.NewKeyWithID("spaces", "space-b")
	foreignExtension := record.NewKeyWithParentAndID(foreignSpace, "ext", "datatug")
	foreignCollection := record.NewKeyWithParentAndID(foreignExtension, models4datatug.QueryActivityPeriodCheckpointsCollection, period.Ref.PeriodID)
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Insert(txCtx, record.NewRecordWithData(foreignCollection, checkpoint))
	}); err != nil {
		t.Fatal(err)
	}
	activity := contract4paymentus.UsageActivity{
		Ref: period.Ref, SourceID: models4datatug.QueryActivitySourceID, EventID: "unscoped-event",
		UserID: "unscoped-actor", EvidenceDigest: strings.Repeat("b", 64), OccurredAtUTC: now,
	}
	id := models4datatug.NewQueryActivityReceiptID(activity.UserID, period.Ref)
	value := &models4datatug.QueryActivityPending{
		Version: 1, ReceiptID: id, SpaceID: "space-a", Period: period.Ref, Activity: activity,
		Sequence: 1, DeliveryState: models4datatug.QueryActivityPendingStatePending, UpdatedAtUTC: now,
	}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Insert(txCtx, record.NewRecordWithData(record.NewKeyWithID(models4datatug.QueryActivityPendingCollection, id), value))
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := NewBusinessUsageLifecycleWorker(db, db, &BusinessUsagePeriodService{}, &QueryActivityService{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.Run(ctx, 10)
	if err != nil || result.CheckpointsScanned != 1 || result.ActivitySpaces != 0 || result.Failures != 2 {
		t.Fatalf("worker did not reject untrusted collection-group paths: %+v / %v", result, err)
	}
}

func TestBusinessUsageLifecycleWorkerCursorAdvancesPastForeignGroupRows(t *testing.T) {
	f := newBusinessOpeningRecoveryFixture(t, true, false)
	foreignOrg := record.NewKeyWithID("orgs", "org-a")
	foreignSpace := record.NewKeyWithParentAndID(foreignOrg, "spaces", "space-a")
	foreignExtension := record.NewKeyWithParentAndID(foreignSpace, "ext", "datatug")
	foreignCollection := record.NewKeyWithParentAndID(foreignExtension, models4datatug.QueryActivityPeriodCheckpointsCollection, f.period.Ref.PeriodID)
	foreignCheckpoint := models4datatug.QueryActivityPeriodCheckpoint{
		Version: 1, Period: f.period.Ref, Snapshot: f.period, AnchorUTC: f.period.AnchorUTC,
		AnchorProofDigest: strings.Repeat("f", 64), State: models4datatug.QueryActivityCheckpointOpening,
		UpdatedAtUTC: f.period.AnchorUTC,
	}
	validRecord, _ := models4datatug.NewQueryActivityPeriodCheckpointRecord(f.period.Ref)
	if err := f.db.Get(context.Background(), validRecord); err != nil {
		t.Fatalf("read valid Space checkpoint for next page: %v", err)
	}
	dummyKey := record.NewKeyWithParentAndID(foreignExtension, models4datatug.QueryActivityPeriodCheckpointsCollection, "later-foreign")
	f.worker.query = &lifecycleSequencedQueryExecutor{pages: [][]record.Record{
		{record.NewRecordWithData(foreignCollection, foreignCheckpoint), record.NewRecordWithData(dummyKey, foreignCheckpoint)},
		{},
		{validRecord},
		{},
	}}

	first, err := f.worker.Run(context.Background(), 1)
	if err != nil || first.CheckpointsScanned != 1 || first.Failures != 1 || !first.CheckpointHasMore || first.CheckpointAfter != foreignCollection.String() {
		t.Fatalf("first pass did not record and advance past the foreign row: %+v / %v", first, err)
	}
	second, err := f.worker.Run(context.Background(), 1)
	if err != nil || second.CheckpointsScanned != 1 || second.PeriodsOpened != 1 || second.Failures != 0 {
		t.Fatalf("next pass was starved behind the foreign row: %+v / %v", second, err)
	}
	checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(f.period.Ref)
	if err := f.db.Get(context.Background(), checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.State != models4datatug.QueryActivityCheckpointReady {
		t.Fatalf("valid Space checkpoint was not processed after foreign row: %+v / %v", checkpoint, err)
	}
}

type lifecycleScriptedQueryExecutor struct {
	dal.QueryExecutor
	reader dal.RecordsReader
	err    error
}

type lifecycleSequencedQueryExecutor struct {
	dal.QueryExecutor
	pages [][]record.Record
}

func (q *lifecycleSequencedQueryExecutor) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	if len(q.pages) == 0 {
		return nil, errors.New("unexpected collection-group query")
	}
	page := q.pages[0]
	q.pages = q.pages[1:]
	return &lifecycleScriptedRecordsReader{rows: append([]record.Record(nil), page...)}, nil
}

func (q lifecycleScriptedQueryExecutor) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return q.reader, q.err
}

type lifecycleScriptedRecordsReader struct {
	rows     []record.Record
	nextErr  error
	closeErr error
	closed   bool
}

func (r *lifecycleScriptedRecordsReader) Cursor() (string, error) { return "", nil }
func (r *lifecycleScriptedRecordsReader) Close() error {
	r.closed = true
	return r.closeErr
}
func (r *lifecycleScriptedRecordsReader) Next() (record.Record, error) {
	if r.nextErr != nil {
		return nil, r.nextErr
	}
	if len(r.rows) == 0 {
		return nil, io.EOF
	}
	row := r.rows[0]
	r.rows = r.rows[1:]
	return row, nil
}

func TestBusinessUsageLifecycleGroupReaderPropagatesReadAndCloseFailures(t *testing.T) {
	ctx := context.Background()
	queryErr := errors.New("query rejected")
	worker := &BusinessUsageLifecycleWorker{query: lifecycleScriptedQueryExecutor{err: queryErr}}
	if _, _, err := worker.readGroupPage(ctx, models4datatug.QueryActivityPendingCollection, "", 1, true); !errors.Is(err, queryErr) {
		t.Fatalf("query execution failure was lost: %v", err)
	}
	nextErr := errors.New("read failed")
	reader := &lifecycleScriptedRecordsReader{nextErr: nextErr}
	worker.query = lifecycleScriptedQueryExecutor{reader: reader}
	if _, _, err := worker.readGroupPage(ctx, models4datatug.QueryActivityPendingCollection, "", 1, true); !errors.Is(err, nextErr) || !reader.closed {
		t.Fatalf("reader failure did not propagate and close: %v closed=%t", err, reader.closed)
	}
	closeErr := errors.New("close failed")
	reader = &lifecycleScriptedRecordsReader{closeErr: closeErr}
	worker.query = lifecycleScriptedQueryExecutor{reader: reader}
	if _, _, err := worker.readGroupPage(ctx, models4datatug.QueryActivityPendingCollection, "", 1, true); !errors.Is(err, closeErr) || !reader.closed {
		t.Fatalf("reader close failure did not propagate: %v closed=%t", err, reader.closed)
	}
	reader = &lifecycleScriptedRecordsReader{rows: []record.Record{nil}}
	worker.query = lifecycleScriptedQueryExecutor{reader: reader}
	if _, _, err := worker.readGroupPage(ctx, models4datatug.QueryActivityPendingCollection, "", 1, true); !errors.Is(err, ErrBusinessUsageLifecycleUnavailable) {
		t.Fatalf("nil collection-group result was accepted: %v", err)
	}
}

func TestBusinessUsageLifecycleWorkerConstructorAndRunRejectInvalidInputs(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	queryActivity := &QueryActivityService{}
	periods := &BusinessUsagePeriodService{}
	now := func() time.Time { return time.Now().UTC() }
	for name, args := range map[string]struct {
		db       dal.DB
		query    dal.QueryExecutor
		periods  *BusinessUsagePeriodService
		activity *QueryActivityService
		now      func() time.Time
	}{
		"missing database":         {query: db, periods: periods, activity: queryActivity, now: now},
		"missing query":            {db: db, periods: periods, activity: queryActivity, now: now},
		"missing period service":   {db: db, query: db, activity: queryActivity, now: now},
		"missing activity service": {db: db, query: db, periods: periods, now: now},
		"missing clock":            {db: db, query: db, periods: periods, activity: queryActivity},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewBusinessUsageLifecycleWorker(args.db, args.query, args.periods, args.activity, args.now); !errors.Is(err, ErrBusinessUsageLifecycleUnavailable) {
				t.Fatalf("constructor accepted missing dependency: %v", err)
			}
		})
	}
	worker, err := NewBusinessUsageLifecycleWorker(db, db, periods, queryActivity, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{0, BusinessUsageLifecycleMaxPageSize + 1} {
		if _, err := worker.Run(context.Background(), size); !errors.Is(err, ErrBusinessUsageLifecycleUnavailable) {
			t.Fatalf("Run accepted page size %d: %v", size, err)
		}
	}
	var absent *BusinessUsageLifecycleWorker
	if _, err := absent.Run(context.Background(), 1); !errors.Is(err, ErrBusinessUsageLifecycleUnavailable) {
		t.Fatalf("nil worker accepted Run: %v", err)
	}
}

func TestBusinessUsageLifecycleWorkerFailsClosedOnCorruptStateAndQueryFailures(t *testing.T) {
	ctx := context.Background()
	db := sneatcoretesting.NewMemoryDB()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	worker, err := NewBusinessUsageLifecycleWorker(db, db, &BusinessUsagePeriodService{}, &QueryActivityService{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	stateRecord, state := models4datatug.NewBusinessUsageWorkerStateRecord()
	*state = models4datatug.BusinessUsageWorkerState{Version: 1, UpdatedAtUTC: now}
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Insert(txCtx, stateRecord)
	}); err != nil {
		t.Fatal(err)
	}
	worker.db = corruptBusinessUsageWorkerStateDB{DB: db}
	if _, err := worker.Run(ctx, 1); !errors.Is(err, ErrBusinessUsageLifecycleUnavailable) {
		t.Fatalf("worker trusted a malformed saved scan cursor: %v", err)
	}
	queryErr := errors.New("collection-group query unavailable")
	worker.db = sneatcoretesting.NewMemoryDB()
	worker.query = lifecycleScriptedQueryExecutor{err: queryErr}
	if _, err := worker.Run(ctx, 1); !errors.Is(err, queryErr) {
		t.Fatalf("worker hid collection-group query failure: %v", err)
	}
	stateErr := errors.New("worker state read unavailable")
	worker.db = failingBusinessUsageReadwriteDB{DB: db, err: stateErr}
	worker.query = db
	if _, err := worker.Run(ctx, 1); !errors.Is(err, stateErr) {
		t.Fatalf("worker hid scan-state read failure: %v", err)
	}
}

type corruptBusinessUsageWorkerStateDB struct{ dal.DB }

func (db corruptBusinessUsageWorkerStateDB) RunReadwriteTransaction(ctx context.Context, worker dal.RWTxWorker, options ...dal.TransactionOption) error {
	return db.DB.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return worker(txCtx, corruptBusinessUsageWorkerStateTx{ReadwriteTransaction: tx})
	}, options...)
}

type corruptBusinessUsageWorkerStateTx struct{ dal.ReadwriteTransaction }

func (tx corruptBusinessUsageWorkerStateTx) Get(ctx context.Context, value record.Record) error {
	err := tx.ReadwriteTransaction.Get(ctx, value)
	if err == nil {
		if state, ok := value.Data().(*models4datatug.BusinessUsageWorkerState); ok {
			state.Version = 2
		}
	}
	return err
}

func TestBusinessUsageLifecycleWorkerCancellationLeavesOpeningCheckpointForRetry(t *testing.T) {
	ctx := context.Background()
	db := sneatcoretesting.NewMemoryDB()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	period := lifecycleTestPeriod(t, "cancel-space", now)
	checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(period.Ref)
	*checkpoint = models4datatug.QueryActivityPeriodCheckpoint{
		Version: 1, Period: period.Ref, Snapshot: period, AnchorUTC: period.AnchorUTC,
		AnchorProofDigest: strings.Repeat("a", 64), State: models4datatug.QueryActivityCheckpointOpening,
		UpdatedAtUTC: now,
	}
	if err := checkpoint.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Insert(txCtx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := NewBusinessUsageLifecycleWorker(db, db, &BusinessUsagePeriodService{}, &QueryActivityService{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := worker.Run(cancelled, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("worker did not stop promptly at the checkpoint boundary: %v", err)
	}
	if err := db.Get(ctx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.State != models4datatug.QueryActivityCheckpointOpening {
		t.Fatalf("cancelled pass advanced the Opening checkpoint: %+v / %v", checkpoint, err)
	}
}

func TestBusinessUsageLifecycleCursorWritesValidateAndPreserveIndependentPositions(t *testing.T) {
	ctx := context.Background()
	db := sneatcoretesting.NewMemoryDB()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	worker := &BusinessUsageLifecycleWorker{db: db, now: func() time.Time { return now }}
	checkpointPath := "spaces/space-a/ext/datatug/" + models4datatug.QueryActivityPeriodCheckpointsCollection + "/checkpoint"
	if err := worker.saveWorkerCursor(ctx, true, "not-a-full-path"); err == nil {
		t.Fatal("module cursor accepted an unscoped path")
	}
	if err := worker.saveWorkerCursor(ctx, true, checkpointPath); err != nil {
		t.Fatalf("initialize checkpoint cursor: %v", err)
	}
	pendingPath := "spaces/space-b/ext/datatug/" + models4datatug.QueryActivityPendingCollection + "/receipt"
	if err := worker.saveWorkerCursor(ctx, false, pendingPath); err != nil {
		t.Fatalf("update independent pending cursor: %v", err)
	}
	stateRecord, state := models4datatug.NewBusinessUsageWorkerStateRecord()
	if err := db.Get(ctx, stateRecord); err != nil || state.Validate() != nil || state.CheckpointAfterPath != checkpointPath || state.PendingAfterPath != pendingPath {
		t.Fatalf("module cursor update overwrote the other collection position: %+v / %v", state, err)
	}
	if err := worker.saveSpaceDrainCursor(ctx, "space-a", strings.Repeat("a", 64)); err != nil {
		t.Fatalf("initialize per-Space activity cursor: %v", err)
	}
	if err := worker.saveSpaceDrainCursor(ctx, "space-a", "invalid"); err == nil {
		t.Fatal("per-Space activity cursor accepted an arbitrary document ID")
	}
	drainRecord, drain := models4datatug.NewQueryActivityDrainCursorRecord("space-a")
	if err := db.Get(ctx, drainRecord); err != nil || drain.Validate() != nil || drain.AfterID != strings.Repeat("a", 64) {
		t.Fatalf("invalid cursor attempt damaged the stored position: %+v / %v", drain, err)
	}
}

func TestBusinessUsageLifecycleWorkerDiscoversAndDrainsAcceptedActivity(t *testing.T) {
	f := newQueryActivityFixture(t)
	actorID, projectID := "worker-actor", "worker-project"
	activityContext := f.issue(actorID, projectID)
	accepted, err := f.service.Report(f.ctx, actorID, "business-space", QueryActivityReport{
		ContextID: activityContext.ContextID, OperationID: "worker-report", Kind: models4datatug.QueryActivityEdit,
	})
	if err != nil || !accepted.Accepted {
		t.Fatalf("accept activity fixture: %+v / %v", accepted, err)
	}
	periodService := &BusinessUsagePeriodService{}
	worker, err := NewBusinessUsageLifecycleWorker(f.db, f.db, periodService, f.service, func() time.Time { return f.now })
	if err != nil {
		t.Fatalf("construct lifecycle worker: %v", err)
	}
	result, err := worker.Run(f.ctx, 10)
	if err != nil || result.ActivityDelivered != 1 || result.ActivityScanned != 1 || result.Failures != 0 {
		t.Fatalf("worker did not drain accepted activity: %+v / %v", result, err)
	}
	pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("business-space", accepted.ReceiptID)
	if err := f.db.Get(f.ctx, pendingRecord); err != nil || pending.Validate() != nil || pending.DeliveryState != models4datatug.QueryActivityPendingStateDelivered {
		t.Fatalf("worker did not persist delivery proof: %+v / %v", pending, err)
	}
	checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(f.period.Ref)
	if err := f.db.Get(f.ctx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.DeliveredThrough != 1 || checkpoint.AcceptedCount != 1 {
		t.Fatalf("worker did not advance complete delivery watermark: %+v / %v", checkpoint, err)
	}
}

func TestBusinessUsageLifecycleWorkerCatchesUpWatermarkFromDeliveredRows(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("catchup-actor", "catchup-project")
	accepted, err := f.service.Report(f.ctx, "catchup-actor", "business-space", QueryActivityReport{
		ContextID: activityContext.ContextID, OperationID: "catchup-report", Kind: models4datatug.QueryActivityEdit,
	})
	if err != nil || !accepted.Accepted {
		t.Fatalf("accept activity fixture: %+v / %v", accepted, err)
	}
	delivered, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 10})
	if err != nil || delivered.Delivered != 1 {
		t.Fatalf("prepare delivered receipt with lagging watermark: %+v / %v", delivered, err)
	}
	checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(f.period.Ref)
	if err := f.db.Get(f.ctx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.AcceptedCount != 1 || checkpoint.DeliveredThrough != 1 {
		t.Fatalf("initial delivery checkpoint: %+v / %v", checkpoint, err)
	}
	checkpoint.DeliveredThrough = 0 // Simulate a persisted receipt delivery with a lost watermark update.
	if err := f.db.RunReadwriteTransaction(f.ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(txCtx, checkpointRecord)
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := NewBusinessUsageLifecycleWorker(f.db, f.db, &BusinessUsagePeriodService{}, f.service, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.Run(f.ctx, 10)
	if err != nil || result.Failures != 0 || result.ActivityScanned != 1 || result.ActivityDelivered != 0 {
		t.Fatalf("worker did not reconcile already-delivered row: %+v / %v", result, err)
	}
	if err := f.db.Get(f.ctx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.DeliveredThrough != checkpoint.AcceptedCount {
		t.Fatalf("delivered watermark did not catch up: %+v / %v", checkpoint, err)
	}
}

func TestBusinessUsageLifecycleWorkerReportsHeldPendingRowsAndResetsWrappedCursors(t *testing.T) {
	t.Run("held pending row remains visible as a failure", func(t *testing.T) {
		f := newQueryActivityFixture(t)
		activity := contract4paymentus.UsageActivity{
			Ref: f.period.Ref, SourceID: models4datatug.QueryActivitySourceID, EventID: "held-event",
			UserID: "held-actor", EvidenceDigest: strings.Repeat("d", 64), OccurredAtUTC: f.now,
		}
		id := models4datatug.NewQueryActivityReceiptID(activity.UserID, f.period.Ref)
		pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("business-space", id)
		*pending = models4datatug.QueryActivityPending{
			Version: 1, ReceiptID: id, SpaceID: "business-space", Period: f.period.Ref, Activity: activity,
			Sequence: 1, DeliveryState: models4datatug.QueryActivityPendingStatePending, UpdatedAtUTC: f.now,
		}
		if err := pending.Validate(); err != nil {
			t.Fatalf("pending fixture is invalid: %v", err)
		}
		if err := f.db.RunReadwriteTransaction(f.ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
			return tx.Insert(txCtx, pendingRecord)
		}); err != nil {
			t.Fatal(err)
		}
		worker, err := NewBusinessUsageLifecycleWorker(f.db, f.db, &BusinessUsagePeriodService{}, f.service, func() time.Time { return f.now })
		if err != nil {
			t.Fatal(err)
		}
		result, err := worker.Run(f.ctx, 10)
		if err != nil || result.ActivityScanned != 1 || result.ActivityDelivered != 0 || result.Failures != 1 {
			t.Fatalf("missing immutable receipt proof was not reported as held: %+v / %v", result, err)
		}
	})
	t.Run("empty pages wrap saved module cursors", func(t *testing.T) {
		ctx := context.Background()
		db := sneatcoretesting.NewMemoryDB()
		now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
		worker, err := NewBusinessUsageLifecycleWorker(db, db, &BusinessUsagePeriodService{}, &QueryActivityService{}, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		stateRecord, state := models4datatug.NewBusinessUsageWorkerStateRecord()
		*state = models4datatug.BusinessUsageWorkerState{
			Version:             1,
			CheckpointAfterPath: "spaces/space-a/ext/datatug/" + models4datatug.QueryActivityPeriodCheckpointsCollection + "/checkpoint",
			PendingAfterPath:    "spaces/space-b/ext/datatug/" + models4datatug.QueryActivityPendingCollection + "/receipt",
			UpdatedAtUTC:        now,
		}
		if err := state.Validate(); err != nil {
			t.Fatal(err)
		}
		if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
			return tx.Insert(txCtx, stateRecord)
		}); err != nil {
			t.Fatal(err)
		}
		result, err := worker.Run(ctx, 2)
		if err != nil || result.CheckpointAfter != "" || result.PendingAfter != "" {
			t.Fatalf("empty page did not wrap the durable cursors: %+v / %v", result, err)
		}
		if err := db.Get(ctx, stateRecord); err != nil || state.Validate() != nil || state.CheckpointAfterPath != "" || state.PendingAfterPath != "" {
			t.Fatalf("wrapped cursor state was not persisted: %+v / %v", state, err)
		}
	})
}

func TestBusinessUsageLifecycleWorkerInitializesCursorsWithNilMissingContract(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("worker-actor", "worker-project")
	accepted, err := f.service.Report(f.ctx, "worker-actor", "business-space", QueryActivityReport{
		ContextID: activityContext.ContextID, OperationID: "worker-nil-missing", Kind: models4datatug.QueryActivityEdit,
	})
	if err != nil || !accepted.Accepted {
		t.Fatalf("accept activity fixture: %+v / %v", accepted, err)
	}
	workerDB := nilMissingReadDB{DB: f.db}
	worker, err := NewBusinessUsageLifecycleWorker(workerDB, f.db, &BusinessUsagePeriodService{}, f.service, func() time.Time { return f.now })
	if err != nil {
		t.Fatalf("construct lifecycle worker: %v", err)
	}
	result, err := worker.Run(f.ctx, 10)
	if err != nil || result.ActivityDelivered != 1 || result.Failures != 0 {
		t.Fatalf("worker failed with nil+Exists=false missing reads: %+v / %v", result, err)
	}
	stateRecord, state := models4datatug.NewBusinessUsageWorkerStateRecord()
	if err := f.db.Get(f.ctx, stateRecord); err != nil || state.Validate() != nil || state.PendingAfterPath != "" {
		t.Fatalf("module scan state was not initialized: %+v / %v", state, err)
	}
	drainRecord, drain := models4datatug.NewQueryActivityDrainCursorRecord("business-space")
	if err := f.db.Get(f.ctx, drainRecord); err != nil || drain.Validate() != nil {
		t.Fatalf("Space drain cursor was not initialized: %+v / %v", drain, err)
	}
}

type nilMissingReadDB struct{ dal.DB }

func (db nilMissingReadDB) RunReadwriteTransaction(ctx context.Context, worker dal.RWTxWorker, options ...dal.TransactionOption) error {
	return db.DB.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		return worker(txCtx, nilMissingReadTx{ReadwriteTransaction: tx})
	}, options...)
}

type nilMissingReadTx struct{ dal.ReadwriteTransaction }

func (tx nilMissingReadTx) Get(ctx context.Context, value record.Record) error {
	err := tx.ReadwriteTransaction.Get(ctx, value)
	if record.IsNotFound(err) {
		return nil
	}
	return err
}

func TestBusinessQueryActivityCompositionOpensBeforeContextAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	var order []string
	period := lifecycleTestPeriod(t, "space-a", time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	service, err := NewBusinessQueryActivityService(lifecyclePeriodOpenerFunc(func(_ context.Context, spaceID string) (contract4paymentus.UsagePeriodSnapshot, error) {
		order = append(order, "open")
		if spaceID != "space-a" {
			t.Fatalf("OpenCurrent Space = %q", spaceID)
		}
		return period, nil
	}), lifecycleQueryActivityService{order: &order})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.IssueContext(ctx, "actor", "space-a", "project"); err != nil {
		t.Fatalf("IssueContext failed: %v", err)
	}
	if len(order) != 2 || order[0] != "open" || order[1] != "issue" {
		t.Fatalf("context issued before period open: %v", order)
	}
	reported, err := service.Report(ctx, "actor", "space-a", QueryActivityReport{OperationID: "operation", Kind: models4datatug.QueryActivityEdit})
	if err != nil || !reported.Accepted || reported.ReceiptID != "receipt" {
		t.Fatalf("Report was not delegated unchanged: %+v / %v", reported, err)
	}
	openErr := errors.New("current paid access unavailable")
	order = nil
	service, err = NewBusinessQueryActivityService(lifecyclePeriodOpenerFunc(func(context.Context, string) (contract4paymentus.UsagePeriodSnapshot, error) {
		order = append(order, "open")
		return contract4paymentus.UsagePeriodSnapshot{}, openErr
	}), lifecycleQueryActivityService{order: &order})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.IssueContext(ctx, "actor", "space-a", "project"); !errors.Is(err, openErr) || len(order) != 1 {
		t.Fatalf("failed Open did not block context issuance: order=%v err=%v", order, err)
	}
	if _, err := NewBusinessQueryActivityService(nil, lifecycleQueryActivityService{order: &order}); !errors.Is(err, ErrBusinessUsagePeriodUnavailable) {
		t.Fatalf("composition accepted missing Open authority: %v", err)
	}
}

func lifecycleTestPeriod(t *testing.T, spaceID string, anchor time.Time) contract4paymentus.UsagePeriodSnapshot {
	t.Helper()
	scope := contract4paymentus.UsageScope{
		Mode: contract4paymentus.ModeLive, SpaceID: spaceID, ProductID: BusinessProjectProductID,
		PayerID: spaceID, ServiceID: BusinessProjectServiceID,
	}
	period, err := contract4paymentus.UsagePeriodForAnchor(scope, contract4paymentus.DataTugBusinessUsagePricing(), anchor, anchor.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return period
}
