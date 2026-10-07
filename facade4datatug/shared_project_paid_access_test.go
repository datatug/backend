// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
)

type currentPaidAccessProbe struct {
	t     *testing.T
	tx    dal.ReadTransaction
	calls int
}

func (p *currentPaidAccessProbe) VerifyPersonalOwner(ctx context.Context, tx dal.ReadTransaction, actor, payer string) error {
	p.t.Helper()
	if tx != p.tx {
		p.t.Fatal("personal owner read escaped supplied transaction")
	}
	p.calls++
	return (paidCreateAuthority{}).VerifyPersonalOwner(ctx, tx, actor, payer)
}
func (p *currentPaidAccessProbe) ReadOwner(ctx context.Context, tx dal.ReadTransaction, mode, product, payer string) (PlanOwnerFence, error) {
	p.t.Helper()
	if tx != p.tx {
		p.t.Fatal("payment owner read escaped supplied transaction")
	}
	p.calls++
	return (paidCreateAuthority{}).ReadOwner(ctx, tx, mode, product, payer)
}

func TestCurrentPaidProjectAccessUsesTransactionAndSourceLimitWithoutAllocation(t *testing.T) {
	db, _, options := paidCreateFixture(t)
	// Link changes must not depend on a new project-allocation slot. The grant is
	// configurable: a proved three-contact limit is not replaced by current five.
	three := int64(3)
	limits := clonePlanLimits(options.Config.ProLimits)
	limits.ProtectedProjectUsers = &three
	paidUpdate(t, db, models4datatug.NewCurrentPlanKey("personal-1"), "limits", limits)
	paidUpdate(t, db, models4datatug.NewPlanApplicationKey("live", "datatug", "personal-1"), "lastProProtectedProjectUsers", three)
	qr, _ := models4datatug.NewProtectedProjectQuotaRecord("live", "datatug", "personal-1")
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error { return tx.Delete(ctx, qr.Key()) }); err != nil {
		t.Fatal(err)
	}
	probe := &currentPaidAccessProbe{t: t}
	options.Personal, options.Owner = probe, probe
	if err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		probe.tx = tx
		access, err := readCurrentPaidProjectAccess(ctx, tx, options, "actor", "personal-1", sharedTestTime)
		if err != nil {
			return err
		}
		if access.contactLimit != 3 || access.projectLimit != 5 || access.limitsVersion != "paid-grant-1" || probe.calls != 2 {
			t.Fatalf("access %+v calls %d", access, probe.calls)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentPaidProjectAccessReplacementKeepsSponsorAndFreshEnd(t *testing.T) {
	db, _, options := paidCreateFixture(t)
	// Historical project admission may name subscription-1; current subscription
	// 2 is valid for the same personal sponsor after all current source rows agree.
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		var owner planOwnerTestRecord
		r := record.NewRecordWithData(planOwnerTestKey(), &owner)
		if err := tx.Get(ctx, r); err != nil {
			return err
		}
		app := new(models4datatug.PlanApplication)
		ar := record.NewRecordWithData(models4datatug.NewPlanApplicationKey("live", "datatug", "personal-1"), app)
		if err := tx.Get(ctx, ar); err != nil {
			return err
		}
		owner.Fence.OwnerSubscriptionID = "subscription-2"
		owner.Fence.OwnerGeneration = 2
		owner.Fence.SubscriptionRevision = 2
		app.OwnerSubscriptionID, app.LastProSubscriptionID = "subscription-2", "subscription-2"
		app.OwnerGeneration, app.LastProOwnerGeneration, app.SubscriptionRevision = 2, 2, 2
		if err := tx.Set(ctx, r); err != nil {
			return err
		}
		return tx.Set(ctx, ar)
	}); err != nil {
		t.Fatal(err)
	}
	read := func(actor string, at time.Time) error {
		return db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			access, err := readCurrentPaidProjectAccess(ctx, tx, options, actor, "personal-1", at)
			if err == nil && access.fence.OwnerSubscriptionID != "subscription-2" {
				t.Fatal("replacement not read")
			}
			return err
		})
	}
	if err := read("actor", sharedTestTime); err != nil {
		t.Fatal(err)
	}
	if err := read("guest", sharedTestTime); !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatalf("foreign sponsor %v", err)
	}
	if err := read("actor", sharedTestTime.Add(24*time.Hour)); !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatalf("expiry boundary %v", err)
	}
	paidUpdate(t, db, models4datatug.NewCurrentPlanKey("personal-1"), "status", "ended")
	if err := read("actor", sharedTestTime); !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatalf("refund reread %v", err)
	}
}

func TestPaidSharedCreateRetryCrossesPaidThroughBoundary(t *testing.T) {
	db, s, _ := paidCreateFixture(t)
	current := sharedTestTime
	s.now = func() time.Time { return current }
	s.db = retryPlanDB{DB: db, between: func() { current = sharedTestTime.Add(24 * time.Hour) }}
	// First attempt can read the active plan but is rolled back. The second
	// attempt reaches exactly the paid-through boundary and must not reuse the
	// first attempt's authority time, entropy or partial records.
	if ref, err := s.Create(context.Background(), sharedCommand()); !errors.Is(err, ErrSharedProjectUnauthorized) || ref != (models4datatug.SharedProjectRef{}) {
		t.Fatalf("expired retry ref %+v error %v", ref, err)
	}
	assertSharedAbsent(t, db, "space", "project-1", "command")
	if paidQuota(t, db).Allocated != 0 {
		t.Fatal("expired retry allocated project")
	}
}

func TestSharedProjectReadTransactionDoesNotExposeWrites(t *testing.T) {
	db, _, _ := paidCreateFixture(t)
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		var read dal.ReadTransaction = sharedProjectReadTransaction{tx}
		if _, ok := read.(dal.WriteSession); ok {
			t.Fatal("proof port can write")
		}
		if _, ok := read.(dal.TransactionCoordinator); ok {
			t.Fatal("proof port can nest transaction")
		}
		var owner planOwnerTestRecord
		if err := read.Get(ctx, record.NewRecordWithData(planOwnerTestKey(), &owner)); err != nil {
			return err
		}
		if owner.Buyer != "actor" {
			t.Fatal("read wrapper lost transaction source")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
