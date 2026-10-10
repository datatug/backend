//go:build integration

package facade4datatug

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2firestore"
	"github.com/datatug/backend/models4datatug"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Follow Core's owned demo emulator contract; never fall back to cloud/ADC.
// The project suffix isolates each case; emulators:exec owns teardown.
func paidFirestore(t *testing.T, extra ...option.ClientOption) (dal.DB, *firestore.Client, context.Context, string) {
	t.Helper()
	host, project := os.Getenv("FIRESTORE_EMULATOR_HOST"), os.Getenv("GCLOUD_PROJECT")
	hostname, _, err := net.SplitHostPort(host)
	if err != nil || (hostname != "127.0.0.1" && hostname != "localhost") || !strings.HasPrefix(project, "demo-") {
		t.Fatal("test-owned loopback Firestore emulator and demo GCLOUD_PROJECT required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	project = fmt.Sprintf("%s-%d", project, time.Now().UnixNano())
	client, err := firestore.NewClient(ctx, project, append([]option.ClientOption{option.WithoutAuthentication()}, extra...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return dalgo2firestore.NewDatabase("(default)", client), client, ctx, project
}

func TestPaidSharedProjectFirestoreRoundtrip(t *testing.T) {
	t.Run("wire-fields-and-durable-replay", func(t *testing.T) {
		db, client, ctx, project := paidFirestore(t)
		_, s, o := paidCreateFixtureWithDB(t, db)
		c := sharedCommand()
		ref, err := s.Create(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		qr, _ := models4datatug.NewProtectedProjectQuotaRecord("live", "datatug", "personal-1")
		// Read raw SDK fields as well as DataTo: a wrong update path can otherwise
		// leave both an unused field and an unchanged typed count.
		snapshot, err := client.Doc(qr.Key().String()).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		raw := snapshot.Data()
		if raw["v"] != int64(1) || raw["Allocated"] != int64(1) || raw["Revision"] != int64(2) || raw["allocated"] != nil || raw["revision"] != nil || raw["Version"] != nil {
			t.Fatalf("quota wire fields: %v", raw)
		}
		var q models4datatug.ProtectedProjectQuota
		if err := snapshot.DataTo(&q); err != nil || q.Validate() != nil || q.Allocated != 1 {
			t.Fatalf("quota roundtrip %+v %v", q, err)
		}
		ar, _ := models4datatug.NewProjectAdmissionRecord(c.SpaceID, ref.ProjectID)
		aSnapshot, err := client.Doc(ar.Key().String()).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var a models4datatug.ProjectAdmission
		if err := aSnapshot.DataTo(&a); err != nil || a.Validate() != nil || a.ProtectedProjectsLimit != 5 || a.ProtectedUsersLimit != 5 || a.PayerID != "personal-1" || a.ActorID != c.ActorID || a.OwnerContact.Validate() != nil {
			t.Fatalf("allocation roundtrip %+v %v", a, err)
		}

		// Decode through the actual SDK: aliases/embedded linkage fields and
		// owner provenance must round-trip at the existing Space project key.
		pr, linked := models4datatug.NewSharedLinkedProjectRecord(ref.SpaceID, ref.ProjectID)
		ps, err := client.Doc(pr.Key().String()).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := ps.DataTo(linked); err != nil {
			t.Fatal(err)
		}
		roles, err := readProjectContactRoles(a.OwnerContact.Contact.SpaceID, linked.WithRelatedAndIDs, s.ownerLinks.catalog)
		if err != nil || assignedProjectContacts(roles) != 1 || len(linked.UserIDs) != 0 {
			t.Fatalf("project linkage wire %+v %v", linked, err)
		}
		cr, _ := models4datatug.NewProjectContactLinkageRecord(a.OwnerContact.Contact)
		cs, err := client.Doc(cr.Key().String()).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var contact projectContactFixture
		if err := cs.DataTo(&contact); err != nil {
			t.Fatal(err)
		}
		edge, err := graphItem(contact.WithRelatedAndIDs, a.OwnerContact.Contact.SpaceID, projectFixtureRef(ref.SpaceID, ref.ProjectID))
		if err != nil || len(rolesOf(edge, false)) != 1 || contact.UserID != c.ActorID || !contact.Active {
			t.Fatalf("contact linkage wire %+v %v", contact, err)
		}

		// Fill the paid allowance before replay: a full quota cannot prevent
		// recovery of an already committed command.
		for i := range 4 {
			other := c
			other.SpaceID, other.CommandID = fmt.Sprintf("other-space-%d", i), fmt.Sprintf("other-command-%d", i)
			seedPaidOwnerContact(t, db, other.SpaceID)
			if _, err := s.Create(ctx, other); err != nil {
				t.Fatal(err)
			}
		}
		q = paidQuota(t, db)
		if q.Allocated != 5 {
			t.Fatalf("full quota %+v", q)
		}
		client2, err := firestore.NewClient(ctx, project, option.WithoutAuthentication())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client2.Close() })
		db2 := dalgo2firestore.NewDatabase("(default)", client2)
		s2, err := NewPaidSharedProjectService(db2, fakeIDGenerator{err: errors.New("no new ID on replay")}, &sharedAuthority{}, func() time.Time { return sharedTestTime }, o)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := s2.Create(ctx, c)
		if err != nil || replay != ref || paidQuota(t, db2) != q {
			t.Fatalf("durable replay %+v %v", replay, err)
		}
	})
	t.Run("writer-source-grants", func(t *testing.T) {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprint(explicit), func(t *testing.T) {
				db, client, ctx, _ := paidFirestore(t)
				f, s := paidWriterCreateFixture(t, db, explicit)
				snapshot, err := client.Doc(models4datatug.NewPlanApplicationKey("live", "datatug", "personal-1").String()).Get(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var app models4datatug.PlanApplication
				if err := snapshot.DataTo(&app); err != nil {
					t.Fatal(err)
				}
				if explicit && (app.LastProProtectedProjects != 5 || app.LastProProtectedProjectUsers != 5) {
					t.Fatal("explicit source grants lost on wire")
				}
				if !explicit && (app.LastProProtectedProjects != 0 || app.LastProProtectedProjectUsers != 0) {
					t.Fatal("legacy config manufactured source grants")
				}
				if _, err := s.Create(ctx, sharedCommand()); explicit {
					if err != nil || paidQuota(t, f.db).Allocated != 1 {
						t.Fatal(err)
					}
				} else {
					if !errors.Is(err, ErrPlanEffectUnproved) || paidQuota(t, f.db).Allocated != 0 {
						t.Fatalf("legacy grant admission: %v", err)
					}
					assertSharedAbsent(t, db, "space", "project-1", "command")
					ar, _ := models4datatug.NewProjectAdmissionRecord("space", "project-1")
					if exists, err := db.Exists(ctx, ar.Key()); err != nil || exists {
						t.Fatal("legacy refusal wrote allocation", err)
					}
				}
			})
		}
	})
	t.Run("buffered-writes-roll-back", func(t *testing.T) {
		for _, fault := range []int{2, 3} {
			t.Run(fmt.Sprint(fault), func(t *testing.T) {
				db, _, ctx, _ := paidFirestore(t)
				_, s, _ := paidCreateFixtureWithDB(t, db)
				s.db = sharedFaultDB{DB: db, wrap: func(tx dal.ReadwriteTransaction) dal.ReadwriteTransaction {
					return &sharedFaultTx{ReadwriteTransaction: tx, failInsert: fault}
				}}
				if _, err := s.Create(ctx, sharedCommand()); err == nil {
					t.Fatal("injected write failure committed")
				}
				assertSharedAbsent(t, db, "space", "project-1", "command")
				ar, _ := models4datatug.NewProjectAdmissionRecord("space", "project-1")
				if exists, err := db.Exists(ctx, ar.Key()); err != nil || exists {
					t.Fatal("rollback left allocation", err)
				}
				if paidQuota(t, db).Allocated != 0 {
					t.Fatal("rollback counted allocation")
				}
				s.db = db
				if _, err := s.Create(ctx, sharedCommand()); err != nil || paidQuota(t, db).Allocated != 1 {
					t.Fatal("retry after rollback", err)
				}
			})
		}
	})
	t.Run("response-loss-and-ended-replay", func(t *testing.T) {
		db, _, ctx, _ := paidFirestore(t)
		_, s, _ := paidCreateFixtureWithDB(t, db)
		s.db = sharedFaultDB{DB: db, afterCommit: errors.New("lost response")}
		if _, err := s.Create(ctx, sharedCommand()); err == nil {
			t.Fatal("response-loss fixture did not fire")
		}
		before := paidQuota(t, db)
		receipt := mustReadSharedReceipt(t, db, sharedCommand())
		s.db = db
		ref, err := s.Create(ctx, sharedCommand())
		if err != nil || ref.ProjectID != receipt.ProjectID || paidQuota(t, db) != before {
			t.Fatalf("response-loss replay %+v %v", ref, err)
		}
		paidUpdate(t, db, models4datatug.NewCurrentPlanKey("personal-1"), "status", "ended")
		if _, err := s.Create(ctx, sharedCommand()); !errors.Is(err, ErrSharedProjectUnauthorized) || paidQuota(t, db) != before {
			t.Fatalf("ended replay %v", err)
		}
	})
	t.Run("injected-sdk-commit-abort-retries", func(t *testing.T) {
		var armed atomic.Bool
		var injected, attempts atomic.Int32
		// Return one synthetic commit-conflict response before sending that
		// commit. The actual SDK owns retry and the emulator owns the final
		// transaction; this is transport fault injection, not server contention.
		interceptor := func(ctx context.Context, method string, req, reply any, conn *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			if method == "/google.firestore.v1.Firestore/Commit" && armed.CompareAndSwap(true, false) {
				injected.Add(1)
				// A genuine server abort releases its locks. Our synthetic
				// response must do the same before the SDK begins its retry.
				commit, ok := req.(*firestorepb.CommitRequest)
				if !ok || len(commit.Transaction) == 0 {
					return status.Error(codes.Internal, "expected transactional commit")
				}
				if _, err := firestorepb.NewFirestoreClient(conn).Rollback(ctx, &firestorepb.RollbackRequest{Database: commit.Database, Transaction: commit.Transaction}); err != nil {
					return err
				}
				return status.Error(codes.Aborted, "injected commit abort")
			}
			return invoke(ctx, method, req, reply, conn, opts...)
		}
		// Emulator mode creates its own connection, so DialOption alone is
		// ignored by this SDK. Supply the explicitly intercepted loopback conn.
		host := os.Getenv("FIRESTORE_EMULATOR_HOST")
		hostname, _, err := net.SplitHostPort(host)
		if err != nil || (hostname != "127.0.0.1" && hostname != "localhost") {
			t.Fatal("owned loopback emulator required")
		}
		conn, err := grpc.NewClient("passthrough:///"+host, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithChainUnaryInterceptor(interceptor))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		db, client, ctx, _ := paidFirestore(t, option.WithGRPCConn(conn))
		_, s, _ := paidCreateFixtureWithDB(t, db)
		s.db = sharedFaultDB{DB: db, wrap: func(tx dal.ReadwriteTransaction) dal.ReadwriteTransaction { attempts.Add(1); return tx }}
		armed.Store(true) // Only Create can consume the fault; seed commits are complete.
		ref, err := s.Create(ctx, sharedCommand())
		if err != nil || injected.Load() != 1 || attempts.Load() != 2 {
			t.Fatalf("SDK retry ref=%+v error=%v injected=%d attempts=%d", ref, err, injected.Load(), attempts.Load())
		}
		q := paidQuota(t, db)
		if q.Allocated != 1 || q.Revision != 2 {
			t.Fatalf("retry double-counted %+v", q)
		}
		project, _ := models4datatug.NewSharedProjectRecord("space", ref.ProjectID)
		receipt, _ := models4datatug.NewSharedProjectCreateReceiptRecord("space", "command")
		for _, collection := range []string{project.Key().Collection(), receipt.Key().Collection(), models4datatug.ProjectAdmissionCollection} {
			docs, err := client.CollectionGroup(collection).Documents(ctx).GetAll()
			if err != nil || len(docs) != 1 {
				t.Fatalf("retry %s committed=%d err=%v", collection, len(docs), err)
			}
		}
		if got := mustReadSharedReceipt(t, db, sharedCommand()); got.ProjectID != ref.ProjectID {
			t.Fatal("retry receipt binding changed")
		}
		s.ids = fakeIDGenerator{err: errors.New("replay must not allocate")}
		replay, err := s.Create(ctx, sharedCommand())
		if err != nil || replay != ref || paidQuota(t, db) != q {
			t.Fatalf("retry replay %+v %v", replay, err)
		}
	})

	t.Run("concurrent-cap-across-spaces", func(t *testing.T) {
		db, client, ctx, _ := paidFirestore(t)
		_, s, _ := paidCreateFixtureWithDB(t, db)
		var attempts atomic.Int32
		s.db = sharedFaultDB{DB: db, wrap: func(tx dal.ReadwriteTransaction) dal.ReadwriteTransaction { attempts.Add(1); return tx }}
		var wg sync.WaitGroup
		start := make(chan struct{})
		var successes, denied atomic.Int32
		failures := make(chan error, 6)
		for i := range 6 {
			wg.Go(func() {
				<-start
				c := sharedCommand()
				c.SpaceID, c.CommandID = fmt.Sprintf("space-%d", i), fmt.Sprintf("command-%d", i)
				_, err := s.Create(ctx, c)
				if err == nil {
					successes.Add(1)
				} else if errors.Is(err, ErrProtectedProjectQuota) {
					denied.Add(1)
				} else {
					failures <- err
				}
			})
		}
		close(start)
		wg.Wait()
		close(failures)
		for err := range failures {
			t.Error(err)
		}
		if successes.Load() != 5 || denied.Load() != 1 || paidQuota(t, db).Allocated != 5 {
			t.Fatalf("successes=%d denied=%d quota=%+v", successes.Load(), denied.Load(), paidQuota(t, db))
		}
		project, _ := models4datatug.NewSharedProjectRecord("space", "project")
		receipt, _ := models4datatug.NewSharedProjectCreateReceiptRecord("space", "command")
		for _, collection := range []string{project.Key().Collection(), receipt.Key().Collection(), models4datatug.ProjectAdmissionCollection} {
			docs, err := client.CollectionGroup(collection).Documents(ctx).GetAll()
			if err != nil || len(docs) != 5 {
				t.Fatalf("%s committed=%d err=%v", collection, len(docs), err)
			}
		}
		t.Logf("concurrent cap SDK attempts=%d (informational, retries are separately fault-tested)", attempts.Load())

	})
}

func TestBusinessUsageLifecycleFirestoreCollectionGroupCursorUsesFullPath(t *testing.T) {
	db, _, ctx, _ := paidFirestore(t)
	anchor := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, spaceID := range []string{"cursor-space-a", "cursor-space-b"} {
		period := lifecycleTestPeriod(t, spaceID, anchor)
		recordValue, checkpoint := models4datatug.NewQueryActivityPeriodCheckpointRecord(period.Ref)
		*checkpoint = models4datatug.QueryActivityPeriodCheckpoint{
			Version: 1, Period: period.Ref, Snapshot: period, AnchorUTC: anchor,
			AnchorProofDigest: strings.Repeat("a", 64), State: models4datatug.QueryActivityCheckpointOpening,
			UpdatedAtUTC: anchor,
		}
		if err := checkpoint.Validate(); err != nil {
			t.Fatalf("invalid checkpoint fixture: %v", err)
		}
		if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
			return tx.Insert(txCtx, recordValue)
		}); err != nil {
			t.Fatalf("insert checkpoint for %s: %v", spaceID, err)
		}
	}
	worker := &BusinessUsageLifecycleWorker{query: db}
	first, hasMore, err := worker.readGroupPage(ctx, models4datatug.QueryActivityPeriodCheckpointsCollection, "", 1, false)
	if err != nil || len(first) != 1 || !hasMore {
		t.Fatalf("first real collection-group page: len=%d hasMore=%t err=%v", len(first), hasMore, err)
	}
	firstPath := first[0].Key().String()
	if !validWorkerCursorPath(firstPath, models4datatug.QueryActivityPeriodCheckpointsCollection) {
		t.Fatalf("Firestore cursor is not a validated full Space-owned document path: %q", firstPath)
	}
	second, hasMore, err := worker.readGroupPage(ctx, models4datatug.QueryActivityPeriodCheckpointsCollection, firstPath, 1, false)
	if err != nil || len(second) != 1 || hasMore || second[0].Key().String() == firstPath {
		t.Fatalf("full-path cursor did not resume collection-group page: len=%d hasMore=%t first=%q err=%v", len(second), hasMore, firstPath, err)
	}
}
