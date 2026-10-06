// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

// Reuse the plan-writer's durable authority row and existing DAL fault/gate
// fixtures. Every owner/identity read participates in the caller transaction.
type paidCreateDirectory struct{ payer string }

func (d paidCreateDirectory) PersonalAccount(_ context.Context, actor string) (PersonalAccount, error) {
	if actor != "actor" {
		return PersonalAccount{}, ErrSharedProjectUnauthorized
	}
	return PersonalAccount{ID: d.payer, Title: "Personal"}, nil
}

type paidCreateAuthority struct{}

func (paidCreateAuthority) ReadOwner(ctx context.Context, tx dal.ReadTransaction, _, _, _ string) (PlanOwnerFence, error) {
	var v planOwnerTestRecord
	err := tx.Get(ctx, record.NewRecordWithData(planOwnerTestKey(), &v))
	return v.Fence, err
}
func (paidCreateAuthority) VerifyPersonalOwner(ctx context.Context, tx dal.ReadTransaction, actor, payer string) error {
	var v planOwnerTestRecord
	if err := tx.Get(ctx, record.NewRecordWithData(planOwnerTestKey(), &v)); err != nil {
		return err
	}
	if !v.Allowed || v.Buyer != actor || v.Fence.AccountID != payer {
		return ErrSharedProjectUnauthorized
	}
	return nil
}

func paidCreateFixture(t *testing.T) (dal.DB, *SharedProjectService, PaidSharedProjectOptions) {
	t.Helper()
	db := sneatcoretesting.NewMemoryDB()
	base, _, _, _, _, _ := validPlanTestService()
	config := base.Config.(testConfigReader).config
	o := PaidSharedProjectOptions{Version: "reviewed-1", Mode: "live", Product: "datatug", Config: config, Directory: paidCreateDirectory{payer: "personal-1"}, Personal: paidCreateAuthority{}, Owner: paidCreateAuthority{}}
	f := PlanOwnerFence{Mode: o.Mode, Family: o.Product, AccountID: "personal-1", OwnerSubscriptionID: "subscription-1", OwnerGeneration: 1, SubscriptionRevision: 1}
	end := sharedTestTime.Add(24 * time.Hour)
	plan := models4datatug.PlanRecord{V: 1, Plan: "pro", Status: "active", Period: "month", PaidUntil: &end, Limits: &config.ProLimits}
	app := models4datatug.PlanApplication{V: 1, Mode: f.Mode, Family: f.Family, AccountID: f.AccountID, OwnerSubscriptionID: f.OwnerSubscriptionID, OwnerGeneration: 1, SubscriptionRevision: 1, EffectDigest: "accepted-effect", LimitsVersion: "paid-grant-1", LastProSubscriptionID: f.OwnerSubscriptionID, LastProOwnerGeneration: 1, LastProQuoteKey: "quote-1", LastProPlanID: "datatug-pro-monthly", LastProPaidServiceProofID: "paid-invoice-1", LastProProtectedProjects: 5, LastProProtectedProjectUsers: 5}
	q := models4datatug.ProtectedProjectQuota{Version: 1, Mode: o.Mode, Product: o.Product, PayerID: f.AccountID, BasisDigest: "verified-complete-empty-inventory", Revision: 1}
	qr, _ := models4datatug.NewProtectedProjectQuotaRecord(o.Mode, o.Product, f.AccountID)
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		for _, r := range []record.Record{record.NewRecordWithData(planOwnerTestKey(), &planOwnerTestRecord{Fence: f, Buyer: "actor", Allowed: true}), record.NewRecordWithData(models4datatug.NewCurrentPlanKey(f.AccountID), &plan), record.NewRecordWithData(models4datatug.NewPlanApplicationKey(f.Mode, f.Family, f.AccountID), &app), record.NewRecordWithData(qr.Key(), &q)} {
			if err := tx.Insert(ctx, r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s, err := NewPaidSharedProjectService(db, &sharedCounterIDs{}, &sharedAuthority{}, func() time.Time { return sharedTestTime }, o)
	if err != nil {
		t.Fatal(err)
	}
	return db, s, o
}

func paidQuota(t *testing.T, db dal.DB) models4datatug.ProtectedProjectQuota {
	t.Helper()
	r, q := models4datatug.NewProtectedProjectQuotaRecord("live", "datatug", "personal-1")
	if err := db.Get(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return *q
}
func paidUpdate(t *testing.T, db dal.DB, key *record.Key, field string, value any) {
	t.Helper()
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Update(ctx, key, []update.Update{update.ByFieldPath([]string{field}, value)})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPaidSharedCreateAggregateQuotaReplayAndImmutableBinding(t *testing.T) {
	db, s, o := paidCreateFixture(t)
	// Mutating the host's input config cannot change the service snapshot.
	*o.Config.ProLimits.ProtectedProjects = 99
	var first models4datatug.SharedProjectRef
	for i := range 5 {
		c := sharedCommand()
		c.SpaceID = fmt.Sprintf("space-%d", i)
		c.CommandID = fmt.Sprintf("command-%d", i)
		ref, err := s.Create(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = ref
		}
	}
	before := paidQuota(t, db)
	if before.Allocated != 5 || before.Revision != 6 {
		t.Fatalf("quota %+v", before)
	}
	c := sharedCommand()
	c.SpaceID = "space-0"
	c.CommandID = "command-0"
	receipt := mustReadSharedReceipt(t, db, c)
	s.ids = fakeIDGenerator{err: errors.New("response-loss entropy failure")}
	ref, err := s.Create(context.Background(), c)
	if err != nil || ref != first || paidQuota(t, db) != before || mustReadSharedReceipt(t, db, c) != receipt {
		t.Fatalf("replay %+v %v", ref, err)
	}
	c.Title = "Changed"
	if _, err := s.Create(context.Background(), c); !errors.Is(err, ErrSharedProjectConflict) {
		t.Fatalf("changed payload: %v", err)
	}
	s.ids = &sharedCounterIDs{}
	c = sharedCommand()
	c.SpaceID = "sixth-space"
	c.CommandID = "sixth-command"
	if _, err := s.Create(context.Background(), c); !errors.Is(err, ErrProtectedProjectQuota) {
		t.Fatalf("sixth %v", err)
	}
	if paidQuota(t, db) != before {
		t.Fatal("failed admission changed quota")
	}
}

func TestPaidSharedCreateConcurrentSixthFencesAcrossSpaces(t *testing.T) {
	db, s, _ := paidCreateFixture(t)
	gate := make(chan struct{})
	var reads atomic.Int32
	s.db = sharedFaultDB{DB: db, wrap: func(tx dal.ReadwriteTransaction) dal.ReadwriteTransaction {
		return sharedGateTx{ReadwriteTransaction: tx, n: &reads, gate: gate, count: 6}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	out := make(chan error, 6)
	for i := range 6 {
		wg.Go(func() {
			c := sharedCommand()
			c.SpaceID = fmt.Sprintf("space-%d", i)
			c.CommandID = fmt.Sprintf("command-%d", i)
			_, err := s.Create(ctx, c)
			out <- err
		})
	}
	wg.Wait()
	close(out)
	success, denied := 0, 0
	for err := range out {
		if err == nil {
			success++
		} else if errors.Is(err, ErrProtectedProjectQuota) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if success != 5 || denied != 1 || paidQuota(t, db).Allocated != 5 {
		t.Fatalf("success %d denied %d quota %+v", success, denied, paidQuota(t, db))
	}
}

func TestPaidSharedCreateEndProvenanceAndSponsorRefuse(t *testing.T) {
	for _, name := range []string{"paid-boundary", "scheduled-end", "refunded", "paused", "unknown-tier", "nil-limits", "missing-frozen-pair", "one-frozen-limit", "missing-proof", "missing-source-pair", "source-mismatch", "stale-owner", "wrong-mode", "wrong-sponsor", "missing-quota", "malformed-quota"} {
		t.Run(name, func(t *testing.T) {
			db, s, _ := paidCreateFixture(t)
			ctx := context.Background()
			planKey := models4datatug.NewCurrentPlanKey("personal-1")
			appKey := models4datatug.NewPlanApplicationKey("live", "datatug", "personal-1")
			qr, _ := models4datatug.NewProtectedProjectQuotaRecord("live", "datatug", "personal-1")
			switch name {
			case "paid-boundary":
				paidUpdate(t, db, planKey, "paidUntil", sharedTestTime)
			case "scheduled-end":
				paidUpdate(t, db, planKey, "endsAt", sharedTestTime)
			case "refunded":
				paidUpdate(t, db, planKey, "status", "ended")
			case "paused":
				paidUpdate(t, db, planKey, "status", "paused")
			case "unknown-tier":
				paidUpdate(t, db, planKey, "plan", "business")
			case "nil-limits":
				paidUpdate(t, db, planKey, "limits", nil)
			case "missing-frozen-pair":
				paidUpdate(t, db, planKey, "limits", models4datatug.PlanLimits{Contributors: 1, AIQuestions: 11, AIModelClasses: []string{"fast", "standard"}, AIPaysFor: "owner"})
			case "one-frozen-limit":
				five := int64(5)
				paidUpdate(t, db, planKey, "limits", models4datatug.PlanLimits{Contributors: 1, AIQuestions: 11, AIModelClasses: []string{"fast", "standard"}, AIPaysFor: "owner", ProtectedProjects: &five})
			case "missing-proof":
				paidUpdate(t, db, appKey, "lastProPaidServiceProofId", "")
			case "missing-source-pair":
				paidUpdate(t, db, appKey, "lastProProtectedProjects", int64(0))
				paidUpdate(t, db, appKey, "lastProProtectedProjectUsers", int64(0))
			case "source-mismatch":
				paidUpdate(t, db, appKey, "lastProProtectedProjectUsers", int64(6))
			case "stale-owner":
				paidUpdate(t, db, appKey, "subscriptionRevision", int64(2))
			case "wrong-mode":
				s.paid.Mode = "test"
			case "wrong-sponsor":
				s.paid.Directory = paidCreateDirectory{payer: "foreign-personal"}
			case "missing-quota":
				if err := db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error { return tx.Delete(ctx, qr.Key()) }); err != nil {
					t.Fatal(err)
				}
			case "malformed-quota":
				paidUpdate(t, db, qr.Key(), "Allocated", int64(-1))
			}
			if ref, err := s.Create(ctx, sharedCommand()); err == nil || ref != (models4datatug.SharedProjectRef{}) {
				t.Fatalf("ref %+v err %v", ref, err)
			}
			assertSharedAbsent(t, db, "space", "project-1", "command")
		})
	}
}

func TestPaidSharedCreateRevocationReplayAndFaultRollback(t *testing.T) {
	for _, fault := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(fault), func(t *testing.T) {
			db, s, _ := paidCreateFixture(t)
			before := paidQuota(t, db)
			s.db = sharedFaultDB{DB: db, wrap: func(tx dal.ReadwriteTransaction) dal.ReadwriteTransaction {
				return &sharedFaultTx{ReadwriteTransaction: tx, failInsert: fault}
			}}
			if _, err := s.Create(context.Background(), sharedCommand()); err == nil {
				t.Fatal("fault succeeded")
			}
			if paidQuota(t, db) != before {
				t.Fatal("quota leaked")
			}
			assertSharedAbsent(t, db, "space", "project-1", "command")
			s.db = db
			if _, err := s.Create(context.Background(), sharedCommand()); err != nil {
				t.Fatal(err)
			}
			paidUpdate(t, db, planOwnerTestKey(), "allowed", false)
			if _, err := s.Create(context.Background(), sharedCommand()); !errors.Is(err, ErrSharedProjectUnauthorized) {
				t.Fatalf("revoked replay %v", err)
			}
		})
	}
}

func TestPaidSharedCreateConfigurationRefusesAndCopies(t *testing.T) {
	db, _, o := paidCreateFixture(t)
	for _, change := range []func(*PaidSharedProjectOptions){func(o *PaidSharedProjectOptions) { o.Version = "" }, func(o *PaidSharedProjectOptions) { o.Mode = "" }, func(o *PaidSharedProjectOptions) { o.Mode = "test" }, func(o *PaidSharedProjectOptions) { o.Product = "" }, func(o *PaidSharedProjectOptions) { o.Product = "other" }, func(o *PaidSharedProjectOptions) { o.Personal = nil }, func(o *PaidSharedProjectOptions) { o.Owner = nil }, func(o *PaidSharedProjectOptions) { o.Directory = (*testDirectory)(nil) }, func(o *PaidSharedProjectOptions) { o.Config = PlanConfig{} }} {
		v := o
		change(&v)
		if _, err := NewPaidSharedProjectService(db, &sharedCounterIDs{}, &sharedAuthority{}, func() time.Time { return sharedTestTime }, v); !errors.Is(err, ErrSharedProjectUnavailable) {
			t.Fatal(err)
		}
	}
}

type paidBasisProof struct {
	expected InitialProtectedProjectBasis
	deny     bool
}

func (p paidBasisProof) VerifyInitialProtectedProjectBasisInTransaction(ctx context.Context, tx dal.ReadTransaction, actor, mode, product, payer string, b InitialProtectedProjectBasis) error {
	var row planOwnerTestRecord
	if err := tx.Get(ctx, record.NewRecordWithData(planOwnerTestKey(), &row)); err != nil {
		return err
	}
	if p.deny || !reflect.DeepEqual(b, p.expected) || actor != row.Buyer || payer != row.Fence.AccountID || mode != row.Fence.Mode || product != row.Fence.Family {
		return ErrProtectedProjectQuota
	}
	return nil
}
func TestProtectedProjectQuotaInitialBasisIsVerifiedAndNeverReset(t *testing.T) {
	db, s, o := paidCreateFixture(t)
	ctx := context.Background()
	qr, _ := models4datatug.NewProtectedProjectQuotaRecord("live", "datatug", "personal-1")
	if err := db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error { return tx.Delete(ctx, qr.Key()) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, sharedCommand()); !errors.Is(err, ErrProtectedProjectQuota) {
		t.Fatalf("absence %v", err)
	}
	basis := InitialProtectedProjectBasis{Digest: "verified-source-basis", Allocated: 0}
	if err := InitializeProtectedProjectQuota(ctx, db, o, "actor", "personal-1", basis, paidBasisProof{expected: basis, deny: true}); err == nil {
		t.Fatal("unproved initial basis")
	}
	if err := InitializeProtectedProjectQuota(ctx, db, o, "actor", "personal-1", basis, paidBasisProof{expected: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, sharedCommand()); err != nil {
		t.Fatal(err)
	}
	before := paidQuota(t, db)
	if err := InitializeProtectedProjectQuota(ctx, db, o, "actor", "personal-1", basis, paidBasisProof{expected: basis}); !errors.Is(err, ErrSharedProjectConflict) {
		t.Fatalf("reset %v", err)
	}
	if paidQuota(t, db) != before {
		t.Fatal("reset changed allocations")
	}
}

func TestPaidSharedCreateCommitResponseLossAndRefundedReplay(t *testing.T) {
	db, s, _ := paidCreateFixture(t)
	ctx := context.Background()
	s.db = sharedFaultDB{DB: db, afterCommit: errors.New("response lost after durable commit")}
	if _, err := s.Create(ctx, sharedCommand()); err == nil {
		t.Fatal("lost response did not fail")
	}
	before := paidQuota(t, db)
	receipt := mustReadSharedReceipt(t, db, sharedCommand())
	s.db = db
	ref, err := s.Create(ctx, sharedCommand())
	if err != nil || ref.ProjectID != receipt.ProjectID || paidQuota(t, db) != before {
		t.Fatalf("response loss replay %+v %v", ref, err)
	}
	paidUpdate(t, db, models4datatug.NewCurrentPlanKey("personal-1"), "status", "ended")
	if _, err := s.Create(ctx, sharedCommand()); !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatalf("ended replay %v", err)
	}
	if mustReadSharedReceipt(t, db, sharedCommand()) != receipt || paidQuota(t, db) != before {
		t.Fatal("ended replay rewrote durable evidence")
	}
}

type paidRevocationTx struct {
	dal.ReadwriteTransaction
	observed, release chan struct{}
	attempts          *atomic.Int32
}

func (t paidRevocationTx) Get(ctx context.Context, r record.Record) error {
	err := t.ReadwriteTransaction.Get(ctx, r)
	if err == nil && r.Key().Collection() == models4datatug.ProtectedProjectQuotaCollection && t.attempts.Add(1) == 1 {
		close(t.observed)
		select {
		case <-t.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
func TestPaidSharedCreateConcurrentRefundRetriesBeforeAnyWrite(t *testing.T) {
	db, s, _ := paidCreateFixture(t)
	observed, release := make(chan struct{}), make(chan struct{})
	var attempts atomic.Int32
	s.db = sharedFaultDB{DB: db, wrap: func(tx dal.ReadwriteTransaction) dal.ReadwriteTransaction {
		return paidRevocationTx{ReadwriteTransaction: tx, observed: observed, release: release, attempts: &attempts}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := make(chan error, 1)
	go func() { _, err := s.Create(ctx, sharedCommand()); out <- err }()
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("did not reach paid snapshot")
	}
	paidUpdate(t, db, models4datatug.NewCurrentPlanKey("personal-1"), "status", "ended")
	close(release)
	if err := <-out; !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatalf("refund race %v", err)
	}
	assertSharedAbsent(t, db, "space", "project-1", "command")
	if paidQuota(t, db).Allocated != 0 {
		t.Fatal("refunded attempt counted")
	}
}

func TestPaidSharedCreateTestProjectionNeverAdmitsLiveProject(t *testing.T) {
	db, s, o := paidCreateFixture(t)
	ctx := context.Background()
	var plan models4datatug.PlanRecord
	if err := db.Get(ctx, record.NewRecordWithData(models4datatug.NewCurrentPlanKey("personal-1"), &plan)); err != nil {
		t.Fatal(err)
	}
	if err := db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := tx.Delete(ctx, models4datatug.NewCurrentPlanKey("personal-1")); err != nil {
			return err
		}
		return tx.Insert(ctx, record.NewRecordWithData(models4datatug.NewTestPlanKey("personal-1"), &plan))
	}); err != nil {
		t.Fatal(err)
	}
	before := paidQuota(t, db)
	if _, err := s.Create(ctx, sharedCommand()); err == nil {
		t.Fatal("TEST plan admitted real project")
	}
	assertSharedAbsent(t, db, "space", "project-1", "command")
	if paidQuota(t, db) != before {
		t.Fatal("TEST projection changed LIVE quota")
	}
	o.Mode = "test"
	if _, err := NewPaidSharedProjectService(db, &sharedCounterIDs{}, &sharedAuthority{}, func() time.Time { return sharedTestTime }, o); !errors.Is(err, ErrSharedProjectUnavailable) {
		t.Fatalf("TEST constructor %v", err)
	}
	basis := InitialProtectedProjectBasis{Digest: "empty", Allocated: 0}
	if err := InitializeProtectedProjectQuota(ctx, db, o, "actor", "personal-1", basis, paidBasisProof{expected: basis}); !errors.Is(err, ErrProtectedProjectQuota) {
		t.Fatalf("TEST initialization %v", err)
	}
}

// Exercise the real writer: legacy effects can project the config's 5x5 pair,
// but that projection must not masquerade as a frozen paid grant.
func TestPaidSharedCreateRequiresWriterSourceProtectedGrants(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit-%t", explicit), func(t *testing.T) {
			ctx := context.Background()
			f := newPlanWriterFixture(t)
			_, _, o := paidCreateFixture(t)
			f.effect.BuyerID = "actor"
			f.effect.LastPaidEnd = sharedTestTime.Add(24 * time.Hour)
			f.effect.LastServiceRefund.ServiceEndUTC = f.effect.LastPaidEnd
			f.limits.snapshot.Limits = clonePlanLimits(o.Config.ProLimits)
			f.effect.Grants.Contributors = o.Config.ProLimits.Contributors
			f.effect.Grants.ProjectContributors = *o.Config.ProLimits.ProjectContributors
			f.effect.Grants.AIQuestions = o.Config.ProLimits.AIQuestions
			f.effect.Grants.AIPaysFor = o.Config.ProLimits.AIPaysFor
			if explicit {
				projects, users := int64(5), int64(5)
				f.effect.Grants.ProtectedProjects, f.effect.Grants.ProtectedProjectUsers = &projects, &users
			}
			f.setAuthority(t, f.effect.Fence, 0, true)
			if outcome, err := f.writer.Apply(ctx, f.effect); err != nil || outcome != PlanApplied {
				t.Fatal(outcome, err)
			}
			plan := f.publicPlan(t)
			if plan.Limits == nil || plan.Limits.ProtectedProjects == nil || *plan.Limits.ProtectedProjects != 5 || plan.Limits.ProtectedProjectUsers == nil || *plan.Limits.ProtectedProjectUsers != 5 {
				t.Fatalf("projection %+v", plan)
			}
			qr, q := models4datatug.NewProtectedProjectQuotaRecord("live", "datatug", "personal-1")
			*q = models4datatug.ProtectedProjectQuota{Version: 1, Mode: "live", Product: "datatug", PayerID: "personal-1", BasisDigest: "verified-empty-inventory", Revision: 1}
			if err := f.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error { return tx.Insert(ctx, qr) }); err != nil {
				t.Fatal(err)
			}
			s, err := NewPaidSharedProjectService(f.db, &sharedCounterIDs{}, &sharedAuthority{}, func() time.Time { return sharedTestTime }, o)
			if err != nil {
				t.Fatal(err)
			}
			c := sharedCommand()
			ref, err := s.Create(ctx, c)
			if explicit {
				if err != nil || ref.ProjectID == "" || paidQuota(t, f.db).Allocated != 1 {
					t.Fatalf("paid source %+v %v", ref, err)
				}
			} else {
				if !errors.Is(err, ErrPlanEffectUnproved) {
					t.Fatalf("config-only projection admitted: %+v %v", ref, err)
				}
				if paidQuota(t, f.db).Allocated != 0 {
					t.Fatal("legacy refusal allocated quota")
				}
				r, _ := models4datatug.NewSharedProjectCreateReceiptRecord(c.SpaceID, c.CommandID)
				if err := f.db.Get(ctx, r); !record.IsNotFound(err) {
					t.Fatalf("legacy refusal receipt: %v", err)
				}

				project, _ := models4datatug.NewSharedProjectRecord(c.SpaceID, "project-1")
				allocation, _ := models4datatug.NewProjectAdmissionRecord(c.SpaceID, "project-1")
				for _, r := range []record.Record{project, allocation} {
					if err := f.db.Get(ctx, r); !record.IsNotFound(err) {
						t.Fatalf("legacy refusal left durable project/allocation: %v", err)
					}
				}
			}
		})
	}
}

func TestAccountPlanWriterProtectedSourceProvenanceLifecycle(t *testing.T) {
	for _, final := range []string{"replacement-legacy", "ended"} {
		t.Run(final, func(t *testing.T) {
			ctx := context.Background()
			f := newPlanWriterFixture(t)
			projects, users := int64(5), int64(5)
			f.effect.Grants.ProtectedProjects, f.effect.Grants.ProtectedProjectUsers = &projects, &users
			f.owner.onReconcile = func() { projects, users = 99, 99 }
			apply := func() {
				t.Helper()
				f.setAuthority(t, f.effect.Fence, f.effect.MoneyIngestEpoch, true)
				if got, err := f.writer.Apply(ctx, f.effect); err != nil || got != PlanApplied {
					t.Fatal(got, err)
				}
			}
			apply()
			projects, users = 99, 99 // no alias to caller's mutable payment effect
			if a := f.application(t); a.LastProProtectedProjects != 5 || a.LastProProtectedProjectUsers != 5 {
				t.Fatal(a)
			}
			f.effect.Grants.ProtectedProjects, f.effect.Grants.ProtectedProjectUsers = nil, nil
			f.effect.BasisOnly = true
			f.effect.Fence.SubscriptionRevision++
			f.effect.SourceRevision++
			f.effect.MoneyIngestEpoch++
			apply()
			if a := f.application(t); a.LastProProtectedProjects != 5 || a.LastProProtectedProjectUsers != 5 {
				t.Fatal(a)
			}
			f.effect.BasisOnly = false
			f.effect.Fence.SubscriptionRevision++
			f.effect.SourceRevision++
			f.effect.MoneyIngestEpoch++
			if final == "replacement-legacy" {
				f.effect.Fence.OwnerSubscriptionID, f.effect.SourceSubscriptionID = "sub-B", "sub-B"
				f.effect.Fence.OwnerGeneration++
				f.effect.QuoteKey = "quote-B"
			} else {
				f.effect.TerminalFullRefund = true
				f.effect.LastServiceRefund.RefundedInFull = true
			}
			apply()
			if a := f.application(t); a.LastProProtectedProjects != 0 || a.LastProProtectedProjectUsers != 0 {
				t.Fatal(a)
			}
		})
	}
}
