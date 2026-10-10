package facade4datatug

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	firestorepb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type queryActivityFirestoreCodecStore struct {
	firestorepb.UnimplementedFirestoreServer
	mu   sync.Mutex
	docs map[string]*firestorepb.Document
}

func (s *queryActivityFirestoreCodecStore) Commit(_ context.Context, request *firestorepb.CommitRequest) (*firestorepb.CommitResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := timestamppb.Now()
	response := &firestorepb.CommitResponse{CommitTime: now}
	for _, write := range request.Writes {
		update := write.GetUpdate()
		if update == nil {
			continue
		}
		document := proto.Clone(update).(*firestorepb.Document)
		for _, value := range document.Fields {
			truncateFirestoreTimestampToMicros(value)
		}
		document.CreateTime, document.UpdateTime = now, now
		s.docs[document.Name] = document
		response.WriteResults = append(response.WriteResults, &firestorepb.WriteResult{UpdateTime: now})
	}
	return response, nil
}

func (s *queryActivityFirestoreCodecStore) BatchGetDocuments(request *firestorepb.BatchGetDocumentsRequest, stream firestorepb.Firestore_BatchGetDocumentsServer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range request.Documents {
		document := s.docs[name]
		if document == nil {
			continue
		}
		if err := stream.Send(&firestorepb.BatchGetDocumentsResponse{
			Result: &firestorepb.BatchGetDocumentsResponse_Found{Found: document}, ReadTime: timestamppb.Now(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func truncateFirestoreTimestampToMicros(value *firestorepb.Value) {
	if timestamp := value.GetTimestampValue(); timestamp != nil {
		timestamp.Nanos = timestamp.Nanos / 1000 * 1000
	}
	if nested := value.GetMapValue(); nested != nil {
		for _, field := range nested.Fields {
			truncateFirestoreTimestampToMicros(field)
		}
	}
	if nested := value.GetArrayValue(); nested != nil {
		for _, item := range nested.Values {
			truncateFirestoreTimestampToMicros(item)
		}
	}
}

func newQueryActivityFirestoreCodecClient(t *testing.T) *firestore.Client {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	firestorepb.RegisterFirestoreServer(server, &queryActivityFirestoreCodecStore{docs: make(map[string]*firestorepb.Document)})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := firestore.NewClient(ctx, "query-activity-precision-test",
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		option.WithGRPCDialOption(grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() })),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func roundTripQueryActivityReceiptThroughFirestoreSDK(t *testing.T, client *firestore.Client, receipt models4datatug.QueryActivityReceipt) models4datatug.QueryActivityReceipt {
	t.Helper()
	var got models4datatug.QueryActivityReceipt
	roundTripQueryActivityFirestoreValue(t, client, "activityReceipts/receipt", receipt, &got)
	return got
}

func roundTripQueryActivityFirestoreValue(t *testing.T, client *firestore.Client, path string, value, destination any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	doc := client.Doc(path)
	if _, err := doc.Set(ctx, value); err != nil {
		t.Fatalf("Firestore SDK encode %s: %v", path, err)
	}
	snapshot, err := doc.Get(ctx)
	if err != nil {
		t.Fatalf("Firestore SDK read %s: %v", path, err)
	}
	if err := snapshot.DataTo(destination); err != nil {
		t.Fatalf("Firestore SDK decode %s: %v", path, err)
	}
}

func TestQueryActivityReceiptFirestorePrecisionSurvivesRealAdmissionReplayAndLateDelivery(t *testing.T) {
	client := newQueryActivityFirestoreCodecClient(t)
	// The real SDK codec round trips and ledger transaction must finish inside
	// the still-open period before the test closes it and exercises late replay.
	f := newQueryActivityFixtureWindow(t, 10*time.Second)
	// Keep the precise event strictly in the past relative to the independent
	// real clock used by Paymentus' ledger, while remaining inside this period.
	rawAcceptedAt := f.now.Add(-time.Second).Add(123456789 * time.Nanosecond)
	f.now = rawAcceptedAt
	activityContext := f.issue("precision-actor", "precision-project")
	contextRecord, storedContext := models4datatug.NewQueryActivityContextRecord("business-space", activityContext.ContextID)
	if err := f.db.Get(f.ctx, contextRecord); err != nil || storedContext.Validate() != nil {
		t.Fatalf("read canonical context: %+v / %v", storedContext, err)
	}
	var decodedContext models4datatug.QueryActivityContext
	roundTripQueryActivityFirestoreValue(t, client, "activityContexts/context", *storedContext, &decodedContext)
	if err := decodedContext.Validate(); err != nil || decodedContext.IssuedAtUTC.Nanosecond() != 123456000 {
		t.Fatalf("SDK context precision invalid: %+v / %v", decodedContext, err)
	}
	quotaRecord, storedQuota := models4datatug.NewQueryActivityContextQuotaRecord("precision-actor", f.period.Ref)
	if err := f.db.Get(f.ctx, quotaRecord); err != nil || storedQuota.Validate() != nil {
		t.Fatalf("read canonical quota: %+v / %v", storedQuota, err)
	}
	var decodedQuota models4datatug.QueryActivityContextQuota
	roundTripQueryActivityFirestoreValue(t, client, "activityQuotas/quota", *storedQuota, &decodedQuota)
	if err := decodedQuota.Validate(); err != nil || decodedQuota.ContextWindowStartUTC.Nanosecond() != 123456000 {
		t.Fatalf("SDK quota precision invalid: %+v / %v", decodedQuota, err)
	}
	accepted, err := f.service.Report(f.ctx, "precision-actor", "business-space", QueryActivityReport{ContextID: activityContext.ContextID, OperationID: "precision-admission", Kind: models4datatug.QueryActivityEdit})
	if err != nil || !accepted.Accepted {
		t.Fatalf("report at sub-microsecond server time: %+v / %v", accepted, err)
	}
	receiptRecord, stored := f.receipt("precision-actor")
	if err := f.db.Get(f.ctx, receiptRecord); err != nil || stored.Validate() != nil || stored.AcceptedAtUTC.Nanosecond() != 123456000 {
		t.Fatalf("service did not canonicalize receipt: %+v / %v", stored, err)
	}
	decoded := roundTripQueryActivityReceiptThroughFirestoreSDK(t, client, *stored)
	if err := decoded.Validate(); err != nil || decoded.AcceptedAtUTC.Nanosecond() != 123456000 || decoded.Activity.OccurredAtUTC != decoded.AcceptedAtUTC {
		t.Fatalf("SDK-roundtripped receipt invalid: nanos=%d receipt=%+v err=%v", decoded.AcceptedAtUTC.Nanosecond(), decoded, err)
	}
	if err := f.db.RunReadwriteTransaction(f.ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		*stored = decoded
		return tx.Set(ctx, receiptRecord)
	}); err != nil {
		t.Fatal(err)
	}
	pendingRecord, storedPending := models4datatug.NewQueryActivityPendingRecord("business-space", accepted.ReceiptID)
	if err := f.db.Get(f.ctx, pendingRecord); err != nil || storedPending.Validate() != nil {
		t.Fatalf("read canonical pending: %+v / %v", storedPending, err)
	}
	var decodedPending models4datatug.QueryActivityPending
	roundTripQueryActivityFirestoreValue(t, client, "activityPending/pending", *storedPending, &decodedPending)
	if err := decodedPending.Validate(); err != nil || decodedPending.UpdatedAtUTC.Nanosecond() != 123456000 {
		t.Fatalf("SDK pending precision invalid: %+v / %v", decodedPending, err)
	}
	// This delivery enters the production receipt authority from the real
	// Paymentus ledger transaction using the value returned by the SDK codec.
	if err := f.service.Deliver(f.ctx, "business-space", accepted.ReceiptID); err != nil {
		t.Fatalf("SDK receipt ledger delivery: %v", err)
	}
	admission, err := f.ledger.Admit(f.ctx, decoded.Activity)
	if err != nil || !admission.Replay {
		t.Fatalf("SDK receipt ledger replay: %+v / %v", admission, err)
	}

	lateContext := f.issue("late-precision-actor", "late-precision-project")
	lateReport, err := f.service.Report(f.ctx, "late-precision-actor", "business-space", QueryActivityReport{ContextID: lateContext.ContextID, OperationID: "precision-late", Kind: models4datatug.QueryActivityExecutionDispatched})
	if err != nil || !lateReport.Accepted {
		t.Fatalf("late report: %+v / %v", lateReport, err)
	}
	lateRecord, lateStored := f.receipt("late-precision-actor")
	if err := f.db.Get(f.ctx, lateRecord); err != nil {
		t.Fatal(err)
	}
	lateDecoded := roundTripQueryActivityReceiptThroughFirestoreSDK(t, client, *lateStored)
	if err := lateDecoded.Validate(); err != nil {
		t.Fatalf("SDK-roundtripped late receipt invalid: %v", err)
	}
	if err := f.db.RunReadwriteTransaction(f.ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		*lateStored = lateDecoded
		return tx.Set(ctx, lateRecord)
	}); err != nil {
		t.Fatal(err)
	}
	if wait := time.Until(f.period.EndUTC) + 20*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
	f.now = models4datatug.CanonicalQueryActivityTime(time.Now().UTC())
	closeRequest := contract4paymentus.UsageCloseRequest{Ref: f.period.Ref, CloseID: "precision-close", Capacity: contract4paymentus.UsageCapacityEvidence{Revision: "precision-basis", Digest: "precision-digest"}, BaseEvent: contract4paymentus.UsageBaseMonthly, BillMonthlyOverage: true}
	if _, err := f.ledger.Close(f.ctx, closeRequest); err != nil {
		t.Fatalf("close precision-test period: %v", err)
	}
	if err := f.service.Deliver(f.ctx, "business-space", lateReport.ReceiptID); err != nil {
		t.Fatalf("SDK receipt late delivery: %v", err)
	}
	lateEvidence, found, err := f.service.corrections.Get(f.ctx, contract4paymentus.UsageEventRef{Ref: f.period.Ref, SourceID: lateDecoded.Activity.SourceID, EventID: lateDecoded.Activity.EventID})
	if err != nil || !found || lateEvidence.Activity != lateDecoded.Activity {
		t.Fatalf("SDK receipt late evidence: found=%v evidence=%+v err=%v", found, lateEvidence, err)
	}
}
