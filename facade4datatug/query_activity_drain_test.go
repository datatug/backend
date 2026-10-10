package facade4datatug

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	firestorepb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"github.com/dal-go/dalgo2firestore"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type queryActivityFailOnceLedger struct {
	contract4paymentus.UsageLedger
	targetEventID string
	mu            sync.Mutex
	failed        bool
}

type queryActivityCrashAfterAdmissionLedger struct {
	contract4paymentus.UsageLedger
	failed bool
}

type queryActivityCancelAtLedger struct {
	contract4paymentus.UsageLedger
	targetEventID string
	cancel        context.CancelFunc
}

func (l queryActivityCancelAtLedger) Admit(ctx context.Context, activity contract4paymentus.UsageActivity) (contract4paymentus.UsageAdmission, error) {
	if activity.EventID == l.targetEventID {
		l.cancel()
		return contract4paymentus.UsageAdmission{}, ctx.Err()
	}
	return l.UsageLedger.Admit(ctx, activity)
}

func (l *queryActivityCrashAfterAdmissionLedger) Admit(ctx context.Context, activity contract4paymentus.UsageActivity) (contract4paymentus.UsageAdmission, error) {
	result, err := l.UsageLedger.Admit(ctx, activity)
	if err == nil && !l.failed {
		l.failed = true
		return result, errors.New("simulated worker return loss after ledger commit")
	}
	return result, err
}

func (l *queryActivityFailOnceLedger) Admit(ctx context.Context, activity contract4paymentus.UsageActivity) (contract4paymentus.UsageAdmission, error) {
	l.mu.Lock()
	if activity.EventID == l.targetEventID && !l.failed {
		l.failed = true
		l.mu.Unlock()
		return contract4paymentus.UsageAdmission{}, errors.New("temporary ledger outage")
	}
	l.mu.Unlock()
	return l.UsageLedger.Admit(ctx, activity)
}

func TestQueryActivityDrainRecoversLostResponseAndAdvancesPastRetryableFailure(t *testing.T) {
	f := newQueryActivityFixture(t)
	var receiptIDs []string
	eventByReceiptID := make(map[string]string)
	for i := 0; i < 3; i++ {
		actorID := "drain-actor-" + string(rune('a'+i))
		projectID := "drain-project-" + string(rune('a'+i))
		activityContext := f.issue(actorID, projectID)
		result, err := f.service.Report(f.ctx, actorID, "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "lost-http-response", Kind: models4datatug.QueryActivityEdit})
		if err != nil || !result.Accepted {
			t.Fatalf("report for %s: %+v / %v", actorID, result, err)
		}
		receiptIDs = append(receiptIDs, result.ReceiptID)
		eventByReceiptID[result.ReceiptID] = models4datatug.NewQueryActivityEventID(activityContext.ContextID, "lost-http-response")
	}
	sort.Strings(receiptIDs)
	// The first page's earliest receipt fails once. Later work in that page and
	// the next page still progresses; the failed item remains discoverable from
	// the beginning on the following drain cycle.
	f.service.ledger = &queryActivityFailOnceLedger{UsageLedger: f.ledger, targetEventID: eventByReceiptID[receiptIDs[0]]}
	first, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 2})
	if err != nil || first.Scanned != 2 || first.Delivered != 1 || first.Failed != 1 || !first.HasMore || first.NextAfterID != receiptIDs[1] {
		t.Fatalf("first bounded page %+v: %v; sorted ids=%v", first, err, receiptIDs)
	}
	second, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 2, AfterID: first.NextAfterID})
	if err != nil || second.Scanned != 1 || second.Delivered != 1 || second.Failed != 0 || second.HasMore || second.NextAfterID != "" {
		t.Fatalf("forward page %+v: %v", second, err)
	}
	// A fresh service instance has no receipt ID or in-memory queue. Starting a
	// new scan discovers and retries the earlier failed item from durable state.
	restarted, err := NewQueryActivityService(contract4paymentus.ModeLive, f.db, queryActivityTestBindingReader{}, f.ledger, f.service.corrections, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	var recovered QueryActivityDrainResult
	totalRecovered, totalFailed, totalScanned := 0, 0, 0
	for attempts := 0; attempts < 4; attempts++ {
		recovered, err = restarted.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 2, AfterID: recovered.NextAfterID})
		if err != nil {
			t.Fatalf("restart recovery: %+v / %v", recovered, err)
		}
		totalRecovered += recovered.Delivered
		totalFailed += recovered.Failed
		totalScanned += recovered.Scanned
		if recovered.NextAfterID == "" {
			break
		}
	}
	if totalScanned != 3 || totalRecovered != 1 || totalFailed != 0 || recovered.NextAfterID != "" {
		t.Fatalf("restart recovery did not finish a full bounded pass: %+v (scanned=%d delivered=%d failed=%d)", recovered, totalScanned, totalRecovered, totalFailed)
	}
	for _, actorID := range []string{"drain-actor-a", "drain-actor-b", "drain-actor-c"} {
		r, pending := models4datatug.NewQueryActivityPendingRecord("business-space", models4datatug.NewQueryActivityReceiptID(actorID, f.period.Ref))
		if err := f.db.Get(f.ctx, r); err != nil || pending.Validate() != nil || pending.DeliveryState != models4datatug.QueryActivityPendingStateDelivered {
			t.Fatalf("pending work for %s: %+v / %v", actorID, pending, err)
		}
	}
}

func TestQueryActivityDrainIsSpaceBoundedAndConcurrentReplaySafe(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("drain-actor", "drain-project")
	if _, err := f.service.Report(f.ctx, "drain-actor", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "concurrent-drain", Kind: models4datatug.QueryActivityExecutionDispatched}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: QueryActivityMaxDrainBatchSize + 1}); !errors.Is(err, ErrQueryActivityInvalid) {
		t.Fatalf("unbounded request accepted: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 1})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent drain: %v", err)
		}
	}
	// Restart scanning is idempotent whether both workers saw the same page or
	// one transaction lost the delivery race.
	if _, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 1}); err != nil {
		t.Fatalf("concurrent replay: %v", err)
	}
	activityRecord, receipt := f.receipt("drain-actor")
	if err := f.db.Get(f.ctx, activityRecord); err != nil {
		t.Fatal(err)
	}
	admission, err := f.ledger.Admit(f.ctx, receipt.Activity)
	if err != nil || !admission.Replay {
		t.Fatalf("one replayable ledger event not retained: %+v / %v", admission, err)
	}
}

func TestQueryActivityDrainRecoversLostReturnAfterLedgerCommit(t *testing.T) {
	f := newQueryActivityFixture(t)
	activityContext := f.issue("drain-crash-actor", "drain-crash-project")
	accepted, err := f.service.Report(f.ctx, "drain-crash-actor", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "lost-after-commit", Kind: models4datatug.QueryActivityEdit})
	if err != nil || !accepted.Accepted {
		t.Fatalf("report: %+v / %v", accepted, err)
	}
	f.service.ledger = &queryActivityCrashAfterAdmissionLedger{UsageLedger: f.ledger}
	first, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 10})
	if err != nil || first.Failed != 1 || first.Delivered != 0 {
		t.Fatalf("post-commit lost return was hidden: %+v / %v", first, err)
	}
	pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("business-space", accepted.ReceiptID)
	if err := f.db.Get(f.ctx, pendingRecord); err != nil || pending.Validate() != nil || pending.DeliveryState != models4datatug.QueryActivityPendingStatePending {
		t.Fatalf("lost return completed pending prematurely: %+v / %v", pending, err)
	}
	// A new worker has only durable Space storage, not the lost receipt ID. It
	// finds the same pending row and the real ledger returns an idempotent replay.
	restarted, err := NewQueryActivityService(contract4paymentus.ModeLive, f.db, queryActivityTestBindingReader{}, f.ledger, f.service.corrections, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 10})
	if err != nil || recovered.Delivered != 1 || recovered.Failed != 0 {
		t.Fatalf("post-commit restart recovery %+v / %v", recovered, err)
	}
	if err := f.db.Get(f.ctx, pendingRecord); err != nil || pending.Validate() != nil || pending.DeliveryState != models4datatug.QueryActivityPendingStateDelivered || pending.Attempts != 2 {
		t.Fatalf("replayed pending state %+v / %v", pending, err)
	}
	if admission, err := f.ledger.Admit(f.ctx, pending.Activity); err != nil || !admission.Replay {
		t.Fatalf("ledger did not retain one replayable event: %+v / %v", admission, err)
	}
}

func TestQueryActivityDrainReturnsCancellationWithResumeCursor(t *testing.T) {
	f := newQueryActivityFixture(t)
	type receiptEvent struct{ receiptID, eventID string }
	var receipts []receiptEvent
	for _, actorID := range []string{"cancel-a", "cancel-b"} {
		activityContext := f.issue(actorID, "cancel-project")
		accepted, err := f.service.Report(f.ctx, actorID, "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "cancel-report", Kind: models4datatug.QueryActivityEdit})
		if err != nil || !accepted.Accepted {
			t.Fatalf("report for %s: %+v / %v", actorID, accepted, err)
		}
		receipts = append(receipts, receiptEvent{receiptID: accepted.ReceiptID, eventID: models4datatug.NewQueryActivityEventID(activityContext.ContextID, "cancel-report")})
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].receiptID < receipts[j].receiptID })
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.service.ledger = queryActivityCancelAtLedger{UsageLedger: f.ledger, targetEventID: receipts[1].eventID, cancel: cancel}
	partial, err := f.service.Drain(ctx, "business-space", QueryActivityDrainRequest{Limit: 10})
	if !errors.Is(err, context.Canceled) || partial.Scanned != 2 || partial.Delivered != 1 || partial.Failed != 0 || partial.NextAfterID != receipts[0].receiptID || partial.HasMore {
		t.Fatalf("canceled drain lost its partial cursor: %+v / %v", partial, err)
	}
	// Resume exactly after the completed row; the canceled receipt was not
	// advanced over and remains pending for a fresh worker context.
	f.service.ledger = f.ledger
	resumed, err := f.service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 10, AfterID: partial.NextAfterID})
	if err != nil || resumed.Scanned != 1 || resumed.Delivered != 1 || resumed.Failed != 0 || resumed.NextAfterID != "" {
		t.Fatalf("resume after cancellation: %+v / %v", resumed, err)
	}
	for _, receipt := range receipts {
		pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("business-space", receipt.receiptID)
		if err := f.db.Get(f.ctx, pendingRecord); err != nil || pending.Validate() != nil || pending.DeliveryState != models4datatug.QueryActivityPendingStateDelivered {
			t.Fatalf("receipt %s after resume: %+v / %v", receipt.receiptID, pending, err)
		}
	}
}

type queryActivityFirestoreDrainServer struct {
	firestorepb.UnimplementedFirestoreServer
	mu            sync.Mutex
	docs          map[string]*firestorepb.Document
	parent        string
	collection    string
	orderedIDs    []string
	queryCalls    int
	transactionID int
}

func (s *queryActivityFirestoreDrainServer) BeginTransaction(context.Context, *firestorepb.BeginTransactionRequest) (*firestorepb.BeginTransactionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transactionID++
	return &firestorepb.BeginTransactionResponse{Transaction: []byte(fmt.Sprintf("tx-%d", s.transactionID))}, nil
}

func (s *queryActivityFirestoreDrainServer) BatchGetDocuments(request *firestorepb.BatchGetDocumentsRequest, stream firestorepb.Firestore_BatchGetDocumentsServer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range request.Documents {
		document := s.docs[name]
		if document == nil {
			return fmt.Errorf("document %q was not seeded", name)
		}
		if err := stream.Send(&firestorepb.BatchGetDocumentsResponse{
			Result:   &firestorepb.BatchGetDocumentsResponse_Found{Found: proto.Clone(document).(*firestorepb.Document)},
			ReadTime: timestamppb.New(time.Unix(2, 0)),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *queryActivityFirestoreDrainServer) Commit(_ context.Context, request *firestorepb.CommitRequest) (*firestorepb.CommitResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	commitTime := timestamppb.New(time.Unix(3, 0))
	response := &firestorepb.CommitResponse{CommitTime: commitTime}
	for _, write := range request.Writes {
		document := write.GetUpdate()
		if document == nil {
			return nil, fmt.Errorf("unexpected non-update write: %T", write.Operation)
		}
		copy := proto.Clone(document).(*firestorepb.Document)
		if copy.CreateTime == nil {
			copy.CreateTime = commitTime
		}
		copy.UpdateTime = commitTime
		s.docs[copy.Name] = copy
		response.WriteResults = append(response.WriteResults, &firestorepb.WriteResult{UpdateTime: commitTime})
	}
	return response, nil
}

func (s *queryActivityFirestoreDrainServer) RunQuery(request *firestorepb.RunQueryRequest, stream firestorepb.Firestore_RunQueryServer) error {
	s.mu.Lock()
	query := request.GetStructuredQuery()
	if request.Parent != s.parent || len(query.From) != 1 || query.From[0].CollectionId != s.collection ||
		len(query.OrderBy) != 1 || query.OrderBy[0].Field.GetFieldPath() != firestore.DocumentID || query.Limit.GetValue() != 3 {
		s.mu.Unlock()
		return fmt.Errorf("unexpected Firestore drain query: parent=%q query=%+v", request.Parent, query)
	}
	start := 0
	switch s.queryCalls {
	case 0:
		if query.StartAt != nil {
			s.mu.Unlock()
			return fmt.Errorf("first page unexpectedly has a cursor: %+v", query.StartAt)
		}
	case 1:
		wantCursor := s.parent + "/" + s.collection + "/" + s.orderedIDs[1]
		if query.StartAt == nil || query.StartAt.GetBefore() || len(query.StartAt.GetValues()) != 1 || query.StartAt.GetValues()[0].GetReferenceValue() != wantCursor {
			s.mu.Unlock()
			return fmt.Errorf("drain continuation cursor = %+v, want exclusive document ref %q", query.StartAt, wantCursor)
		}
		start = 2
	default:
		s.mu.Unlock()
		return fmt.Errorf("unexpected RunQuery call %d", s.queryCalls+1)
	}
	s.queryCalls++
	end := start + int(query.Limit.GetValue())
	if end > len(s.orderedIDs) {
		end = len(s.orderedIDs)
	}
	documents := make([]*firestorepb.Document, 0, end-start)
	for _, id := range s.orderedIDs[start:end] {
		name := request.Parent + "/" + s.collection + "/" + id
		document := s.docs[name]
		if document == nil {
			s.mu.Unlock()
			return fmt.Errorf("query document %q was not seeded", name)
		}
		documents = append(documents, proto.Clone(document).(*firestorepb.Document))
	}
	s.mu.Unlock()
	for _, document := range documents {
		if err := stream.Send(&firestorepb.RunQueryResponse{Document: document, ReadTime: timestamppb.New(time.Unix(2, 0))}); err != nil {
			return err
		}
	}
	return nil
}

type queryActivityDrainRecordingLedger struct {
	contract4paymentus.UsageLedger
	mu       sync.Mutex
	accepted []contract4paymentus.UsageActivity
}

func (l *queryActivityDrainRecordingLedger) Admit(_ context.Context, activity contract4paymentus.UsageActivity) (contract4paymentus.UsageAdmission, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.accepted = append(l.accepted, activity)
	return contract4paymentus.UsageAdmission{Activity: activity, AcceptedAtUTC: time.Now().UTC(), NewActiveUser: true}, nil
}

func newQueryActivityDrainFirestoreClient(t *testing.T, fake *queryActivityFirestoreDrainServer) *firestore.Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	firestorepb.RegisterFirestoreServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	client, err := firestore.NewClient(ctx, "business-activity-drain-test", option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		option.WithGRPCDialOption(grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() })))
	cancel()
	if err != nil {
		server.Stop()
		_ = listener.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		_ = client.Close()
		server.Stop()
		_ = listener.Close()
	}
	t.Cleanup(cleanup)
	return client
}

func TestQueryActivityDrainContinuesThroughRealFirestoreAdapterAfterDeliveredPrefix(t *testing.T) {
	f := newQueryActivityFixture(t)
	type seededPending struct {
		pending  models4datatug.QueryActivityPending
		receipt  models4datatug.QueryActivityReceipt
		sequence models4datatug.QueryActivityPeriodSequence
	}
	seeded := make([]seededPending, 0, 4)
	for i := 0; i < 4; i++ {
		actorID := fmt.Sprintf("firestore-drain-actor-%d", i)
		projectID := fmt.Sprintf("firestore-drain-project-%d", i)
		activityContext := f.issue(actorID, projectID)
		accepted, err := f.service.Report(f.ctx, actorID, "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "firestore-drain", Kind: models4datatug.QueryActivityEdit})
		if err != nil || !accepted.Accepted {
			t.Fatalf("report for %s: %+v / %v", actorID, accepted, err)
		}
		pendingRecord, pending := models4datatug.NewQueryActivityPendingRecord("business-space", accepted.ReceiptID)
		if err := f.db.Get(f.ctx, pendingRecord); err != nil || pending.Validate() != nil {
			t.Fatalf("read fixture pending %s: %+v / %v", actorID, pending, err)
		}
		receiptRecord, receipt := models4datatug.NewQueryActivityReceiptRecord(actorID, f.period.Ref)
		if err := f.db.Get(f.ctx, receiptRecord); err != nil || receipt.Validate() != nil {
			t.Fatalf("read fixture receipt %s: %+v / %v", actorID, receipt, err)
		}
		sequenceRecord, sequence := models4datatug.NewQueryActivityPeriodSequenceRecord(f.period.Ref, pending.Sequence)
		if err := f.db.Get(f.ctx, sequenceRecord); err != nil || sequence.Validate() != nil {
			t.Fatalf("read fixture sequence %s: %+v / %v", actorID, sequence, err)
		}
		seeded = append(seeded, seededPending{pending: *pending, receipt: *receipt, sequence: *sequence})
	}
	sort.Slice(seeded, func(i, j int) bool { return seeded[i].pending.ReceiptID < seeded[j].pending.ReceiptID })
	for i := range seeded {
		if i < 3 {
			seeded[i].pending.DeliveryState = models4datatug.QueryActivityPendingStateDelivered
			seeded[i].pending.DeliveryProofDigest = strings.Repeat("a", 64)
			seeded[i].pending.DeliveredAtUTC = f.now
			seeded[i].sequence.DeliveryState = models4datatug.QueryActivitySequenceDelivered
			seeded[i].sequence.DeliveryProofDigest = strings.Repeat("a", 64)
			seeded[i].sequence.DeliveredAtUTC = f.now
		}
	}
	checkpointRecord, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(f.period.Ref)
	if err := f.db.Get(f.ctx, checkpointRecord); err != nil || checkpoint.Validate() != nil || checkpoint.AcceptedCount != 4 {
		t.Fatalf("read fixture checkpoint: %+v / %v", checkpoint, err)
	}
	for checkpoint.DeliveredThrough < checkpoint.AcceptedCount {
		nextSequence := checkpoint.DeliveredThrough + 1
		foundDelivered := false
		for _, item := range seeded {
			if item.sequence.Sequence == nextSequence && item.sequence.DeliveryState == models4datatug.QueryActivitySequenceDelivered {
				foundDelivered = true
				break
			}
		}
		if !foundDelivered {
			break
		}
		checkpoint.DeliveredThrough = nextSequence
	}

	parentKey := record.NewKeyWithParentAndID(record.NewKeyWithID("spaces", "business-space"), "ext", "datatug")
	parentPath := "projects/business-activity-drain-test/databases/(default)/documents/" + parentKey.String()
	fake := &queryActivityFirestoreDrainServer{docs: make(map[string]*firestorepb.Document), parent: parentPath, collection: models4datatug.QueryActivityPendingCollection}
	client := newQueryActivityDrainFirestoreClient(t, fake)
	for _, item := range seeded {
		pendingPath := strings.Join([]string{"spaces", "business-space", "ext", "datatug", models4datatug.QueryActivityPendingCollection, item.pending.ReceiptID}, "/")
		if _, err := client.Doc(pendingPath).Set(f.ctx, item.pending); err != nil {
			t.Fatalf("seed pending %s through Firestore SDK: %v", item.pending.ReceiptID, err)
		}
		if item.pending.DeliveryState == models4datatug.QueryActivityPendingStatePending {
			receiptPath := strings.Join([]string{"spaces", "business-space", "ext", "datatug", models4datatug.QueryActivityReceiptsCollection, item.receipt.ReceiptID}, "/")
			if _, err := client.Doc(receiptPath).Set(f.ctx, item.receipt); err != nil {
				t.Fatalf("seed pending receipt %s through Firestore SDK: %v", item.receipt.ReceiptID, err)
			}
		}
		sequencePath := strings.Join([]string{"spaces", "business-space", "ext", "datatug", models4datatug.QueryActivityPeriodCheckpointsCollection, models4datatug.NewQueryActivityPeriodID(f.period.Ref), models4datatug.QueryActivityPeriodSequencesCollection, fmt.Sprintf("%020d", item.sequence.Sequence)}, "/")
		if _, err := client.Doc(sequencePath).Set(f.ctx, item.sequence); err != nil {
			t.Fatalf("seed period sequence %d through Firestore SDK: %v", item.sequence.Sequence, err)
		}
		fake.orderedIDs = append(fake.orderedIDs, item.pending.ReceiptID)
	}
	checkpointPath := strings.Join([]string{"spaces", "business-space", "ext", "datatug", models4datatug.QueryActivityPeriodCheckpointsCollection, models4datatug.NewQueryActivityPeriodID(f.period.Ref)}, "/")
	if _, err := client.Doc(checkpointPath).Set(f.ctx, *checkpoint); err != nil {
		t.Fatalf("seed period checkpoint through Firestore SDK: %v", err)
	}

	ledger := &queryActivityDrainRecordingLedger{}
	service, err := NewQueryActivityService(contract4paymentus.ModeLive, dalgo2firestore.NewDatabase("(default)", client), queryActivityTestBindingReader{}, ledger, f.service.corrections, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 2})
	if err != nil || first.Scanned != 2 || first.Delivered != 0 || first.Failed != 0 || !first.HasMore || first.NextAfterID != seeded[1].pending.ReceiptID {
		t.Fatalf("first real Firestore adapter page %+v / %v", first, err)
	}
	second, err := service.Drain(f.ctx, "business-space", QueryActivityDrainRequest{Limit: 2, AfterID: first.NextAfterID})
	if err != nil || second.Scanned != 2 || second.Delivered != 1 || second.Failed != 0 || second.HasMore || second.NextAfterID != "" {
		t.Fatalf("continued real Firestore adapter page %+v / %v", second, err)
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if len(ledger.accepted) != 1 || ledger.accepted[0] != seeded[3].pending.Activity || fake.queryCalls != 2 {
		t.Fatalf("real drain admission=%+v, query calls=%d, want only pending item after 3 delivered rows", ledger.accepted, fake.queryCalls)
	}
}
