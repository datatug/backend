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
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2firestore"
	"github.com/datatug/backend/models4datatug"
	"google.golang.org/api/option"
)

// Follow Core's owned demo emulator contract; never fall back to cloud/ADC.
// The project suffix isolates each case; emulators:exec owns teardown.
func paidFirestore(t *testing.T) (dal.DB, *firestore.Client, context.Context, string) {
	t.Helper()
	host, project := os.Getenv("FIRESTORE_EMULATOR_HOST"), os.Getenv("GCLOUD_PROJECT")
	hostname, _, err := net.SplitHostPort(host)
	if err != nil || (hostname != "127.0.0.1" && hostname != "localhost") || !strings.HasPrefix(project, "demo-") {
		t.Fatal("test-owned loopback Firestore emulator and demo GCLOUD_PROJECT required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	project = fmt.Sprintf("%s-%d", project, time.Now().UnixNano())
	client, err := firestore.NewClient(ctx, project, option.WithoutAuthentication())
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
		if err := aSnapshot.DataTo(&a); err != nil || a.Validate() != nil || a.ProtectedProjectsLimit != 5 || a.ProtectedUsersLimit != 5 || a.PayerID != "personal-1" || a.ActorID != c.ActorID {
			t.Fatalf("allocation roundtrip %+v %v", a, err)
		}

		// Fill the paid allowance before replay: a full quota cannot prevent
		// recovery of an already committed command.
		for i := range 4 {
			other := c
			other.SpaceID, other.CommandID = fmt.Sprintf("other-space-%d", i), fmt.Sprintf("other-command-%d", i)
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
		t.Logf("real SDK transaction attempts=%d for 6 concurrent commands", attempts.Load())

	})
}
