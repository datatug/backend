// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-core-modules/spaceus/contract4spaceus"
	"github.com/sneat-co/sneat-core-modules/spaceus/dbo4spaceus"
	coreSpaceFacade "github.com/sneat-co/sneat-core-modules/spaceus/facade4spaceus"
	"github.com/sneat-co/sneat-core-modules/userus/dbo4userus"
	"github.com/sneat-co/sneat-ext-contracts/contactus/contactusmodels/briefs4contactus"
	"github.com/sneat-co/sneat-ext-contracts/contactus/contactusmodels/const4contactus"
	"github.com/sneat-co/sneat-go-core/coretypes"
	"github.com/sneat-co/sneat-go-core/facade"
	"github.com/sneat-co/sneat-go-core/models/dbmodels"
	"github.com/strongo/strongoapp/person"
	"github.com/strongo/strongoapp/with"
)

func paidBindingForCommand(command SharedProjectCreateCommand, paid PaidSharedProjectOptions) SharedProjectCreateBinding {
	return SharedProjectCreateBinding{
		ActorID: command.ActorID, SpaceID: command.SpaceID, CommandID: command.CommandID,
		PayerID: "personal-1", Mode: paid.Mode, Product: paid.Product,
		RequestDigest: models4datatug.PaidSharedProjectCreateDigest(command.ActorID, command.SpaceID, command.CommandID, command.Title, "personal-1", paid.Mode, paid.Product),
	}
}

type activationProbe struct{ applied bool }

func (p *activationProbe) Apply(context.Context, dal.ReadwriteTransaction) error {
	p.applied = true
	return nil
}

type transactionalActivatorProbe struct {
	request contract4spaceus.ReserveRoleCapabilityRequest
	called  int
	plan    contract4spaceus.PreparedRoleCapabilityActivation
	err     error
}

func (p *transactionalActivatorProbe) PlanRoleCapabilityActivationInTransaction(_ facade.ContextWithUser, _ dal.ReadwriteTransaction, request contract4spaceus.ReserveRoleCapabilityRequest, at time.Time) (contract4spaceus.PreparedRoleCapabilityActivation, error) {
	p.called++
	p.request = request
	if at.IsZero() {
		return nil, ErrSharedProjectUnauthorized
	}
	return p.plan, p.err
}

func TestEnsureProtectedProjectQuotaForCreateUsesCompleteInventorySnapshot(t *testing.T) {
	db, _, paid := paidCreateFixture(t)
	quotaRecord, _ := models4datatug.NewProtectedProjectQuotaRecord(paid.Mode, paid.Product, "personal-1")
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Delete(ctx, quotaRecord.Key())
	}); err != nil {
		t.Fatal(err)
	}
	activator := &transactionalActivatorProbe{plan: &activationProbe{}}
	service, err := NewActivatingPaidSharedProjectService(db, &sharedCounterIDs{}, func() time.Time { return sharedTestTime }, paid, SharedProjectActivationOptions{
		Activator: activator, RequiredRoles: []string{"content-editor", "content-admin"}, InventoryQuery: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := facade.NewContextWithUserID(context.Background(), "actor")
	if err := service.EnsureProtectedProjectQuotaForCreate(ctx, paidBindingForCommand(sharedCommand(), paid)); err != nil {
		t.Fatal(err)
	}
	quotaRecord, quota := models4datatug.NewProtectedProjectQuotaRecord(paid.Mode, paid.Product, "personal-1")
	if err := db.Get(context.Background(), quotaRecord); err != nil || quota.Validate() != nil || quota.Allocated != 0 || quota.BasisDigest == "" {
		t.Fatalf("initialized quota %+v, %v", quota, err)
	}
	if activator.called != 2 || activator.plan.(*activationProbe).applied {
		t.Fatalf("quota preflight must recheck Core authority before seed without applying: calls=%d applied=%v", activator.called, activator.plan.(*activationProbe).applied)
	}
}

func TestEnsureProtectedProjectQuotaForCreateConcurrentInitializersConverge(t *testing.T) {
	db, _, paid := paidCreateFixture(t)
	quotaRecord, _ := models4datatug.NewProtectedProjectQuotaRecord(paid.Mode, paid.Product, "personal-1")
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Delete(ctx, quotaRecord.Key())
	}); err != nil {
		t.Fatal(err)
	}
	barrier := &inventoryQueryBarrier{ready: make(chan struct{})}
	makeService := func() *SharedProjectService {
		service, err := NewActivatingPaidSharedProjectService(db, &sharedCounterIDs{}, func() time.Time { return sharedTestTime }, paid, SharedProjectActivationOptions{
			Activator: &transactionalActivatorProbe{plan: &activationProbe{}}, RequiredRoles: []string{"content-admin"},
			InventoryQuery: &firstQueryBarrier{QueryExecutor: db, barrier: barrier},
		})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	services := []*SharedProjectService{makeService(), makeService()}
	ctx := facade.NewContextWithUserID(context.Background(), "actor")
	binding := paidBindingForCommand(sharedCommand(), paid)
	results := make(chan error, len(services))
	var wg sync.WaitGroup
	for _, service := range services {
		wg.Add(1)
		go func(service *SharedProjectService) {
			defer wg.Done()
			results <- service.EnsureProtectedProjectQuotaForCreate(ctx, binding)
		}(service)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	quotaRecord, quota := models4datatug.NewProtectedProjectQuotaRecord(paid.Mode, paid.Product, "personal-1")
	if err := db.Get(context.Background(), quotaRecord); err != nil || quota.Validate() != nil || quota.Allocated != 0 || quota.Revision != 1 {
		t.Fatalf("quota %+v, %v", quota, err)
	}
}

func TestEnsureProtectedProjectQuotaForCreateRechecksAuthorityBeforeSeed(t *testing.T) {
	for _, test := range []struct {
		name   string
		revoke func(*testing.T, dal.DB)
	}{
		{
			name: "Core Space role",
			revoke: func(t *testing.T, db dal.DB) {
				user := dbo4userus.NewUserEntry("actor")
				if err := db.Get(context.Background(), user.Record); err != nil {
					t.Fatal(err)
				}
				brief := user.Data.Spaces["space"]
				brief.Roles = []string{const4contactus.SpaceMemberRoleMember}
				if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error { return tx.Set(ctx, user.Record) }); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "owner contact",
			revoke: func(t *testing.T, db dal.DB) {
				r, _ := models4datatug.NewProjectContactLinkageRecord(contactFixtureRef("space", "owner-contact"))
				if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error { return tx.Delete(ctx, r.Key()) }); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, _, paid := paidCreateFixture(t)
			deletePaidQuota(t, db)
			seedCoreSpaceWithRole(t, db, "space", "actor", const4contactus.SpaceMemberRoleOwner)
			query := &revokeAfterInventoryScan{QueryExecutor: db, revoke: func() { test.revoke(t, db) }}
			service, err := NewActivatingPaidSharedProjectService(db, &sharedCounterIDs{}, func() time.Time { return sharedTestTime }, paid, SharedProjectActivationOptions{
				Activator: coreSpaceFacade.NewCapabilityAuthority(), RequiredRoles: []string{const4contactus.SpaceMemberRoleOwner}, InventoryQuery: query,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := facade.NewContextWithUserID(context.Background(), "actor")
			if err := service.EnsureProtectedProjectQuotaForCreate(ctx, paidBindingForCommand(sharedCommand(), paid)); err == nil {
				t.Fatal("expected seed authority recheck to reject create")
			}
			assertPaidQuotaAbsent(t, db)
			assertDataTugNotActivated(t, db)
		})
	}
}

type revokeAfterInventoryScan struct {
	dal.QueryExecutor
	revoke func()
	reads  int
	called bool
}

func (q *revokeAfterInventoryScan) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	reader, err := q.QueryExecutor.ExecuteQueryToRecordsReader(ctx, query)
	q.reads++
	if err == nil && q.reads == 3 && !q.called {
		q.called = true
		q.revoke()
	}
	return reader, err
}

func (q *revokeAfterInventoryScan) ExecuteQueryToRecordsetReader(ctx context.Context, query dal.Query, options ...recordset.Option) (dal.RecordsetReader, error) {
	return q.QueryExecutor.ExecuteQueryToRecordsetReader(ctx, query, options...)
}

type inventoryQueryBarrier struct {
	mu      sync.Mutex
	arrived int
	ready   chan struct{}
}

func (b *inventoryQueryBarrier) wait() {
	b.mu.Lock()
	b.arrived++
	if b.arrived == 2 {
		close(b.ready)
	}
	b.mu.Unlock()
	<-b.ready
}

type firstQueryBarrier struct {
	dal.QueryExecutor
	barrier *inventoryQueryBarrier
	once    sync.Once
}

func (q *firstQueryBarrier) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	reader, err := q.QueryExecutor.ExecuteQueryToRecordsReader(ctx, query)
	if err == nil {
		q.once.Do(q.barrier.wait)
	}
	return reader, err
}

func (q *firstQueryBarrier) ExecuteQueryToRecordsetReader(ctx context.Context, query dal.Query, options ...recordset.Option) (dal.RecordsetReader, error) {
	return q.QueryExecutor.ExecuteQueryToRecordsetReader(ctx, query, options...)
}

func TestPlanExplicitProjectCreateActivationBindsVerifiedActorAndPaidCommand(t *testing.T) {
	db, _, paid := paidCreateFixture(t)
	plan := &activationProbe{}
	activator := &transactionalActivatorProbe{plan: plan}
	service, err := NewActivatingPaidSharedProjectService(db, &sharedCounterIDs{}, func() time.Time { return sharedTestTime }, paid, SharedProjectActivationOptions{
		Activator: activator, RequiredRoles: []string{"content-editor", "content-admin"}, InventoryQuery: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := SharedProjectCreateCommand{ActorID: "actor", SpaceID: "space-one", CommandID: "create-one", Title: "Project"}
	binding := SharedProjectCreateBinding{
		ActorID: command.ActorID, SpaceID: command.SpaceID, CommandID: command.CommandID,
		PayerID: "personal-1", Mode: paid.Mode, Product: paid.Product,
		RequestDigest: models4datatug.PaidSharedProjectCreateDigest(command.ActorID, command.SpaceID, command.CommandID, command.Title, "personal-1", paid.Mode, paid.Product),
	}
	ctx := facade.NewContextWithUserID(context.Background(), "actor")
	var prepared contract4spaceus.PreparedRoleCapabilityActivation
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		var err error
		prepared, err = service.PlanExplicitProjectCreateActivationInTransaction(ctx, tx, binding, sharedTestTime)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	request := activator.request
	if activator.called != 1 || prepared != plan || request.SpaceID != "space-one" || request.Capability != "datatug" || request.Purpose != "shared-project-create" || request.Stage != "enable-datatug" || request.CommandID == "" || len(request.RequiredRoles) != 2 || request.RequiredRoles[0] != "content-admin" || request.RequiredRoles[1] != "content-editor" {
		t.Fatalf("activation plan request %+v calls=%d", request, activator.called)
	}
	if plan.applied {
		t.Fatal("planning applied the module capability")
	}
	if err := db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		_, err := service.PlanExplicitProjectCreateActivationInTransaction(facade.NewContextWithUserID(txCtx, "another-actor"), tx, binding, sharedTestTime)
		return err
	}); !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatalf("cross-actor plan error = %v", err)
	}
	if activator.called != 1 {
		t.Fatal("Core activator was called for another actor")
	}
}

func TestActivatingPaidCreateEnablesDataTugInCreateTransaction(t *testing.T) {
	for _, create := range []struct {
		name string
		run  func(*SharedProjectService, facade.ContextWithUser) error
	}{
		{
			name: "cloud shared project",
			run: func(service *SharedProjectService, ctx facade.ContextWithUser) error {
				_, err := service.Create(ctx, sharedCommand())
				return err
			},
		},
		{
			name: "GitHub project",
			run: func(service *SharedProjectService, ctx facade.ContextWithUser) error {
				_, err := service.CreateGitHubProject(ctx, githubCommand(), githubRepo())
				return err
			},
		},
	} {
		t.Run(create.name, func(t *testing.T) {
			db, service, ctx := activatingPaidCreateFixture(t, const4contactus.SpaceMemberRoleOwner)
			if err := create.run(service, ctx); err != nil {
				t.Fatal(err)
			}
			assertDataTugActivatedAndAllocated(t, db)
		})
	}
}

func TestActivatingGitHubCreateDenialsLeaveRepoAndSpaceUnchanged(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, dal.DB)
		role  string
	}{
		{name: "Core role revoked", role: const4contactus.SpaceMemberRoleMember},
		{
			name: "paid access revoked",
			role: const4contactus.SpaceMemberRoleOwner,
			setup: func(t *testing.T, db dal.DB) {
				past := sharedTestTime.Add(-time.Minute)
				paidUpdate(t, db, models4datatug.NewCurrentPlanKey("personal-1"), "paidUntil", &past)
			},
		},
		{
			name: "owner contact unavailable",
			role: const4contactus.SpaceMemberRoleOwner,
			setup: func(t *testing.T, db dal.DB) {
				ref := contactFixtureRef("space", "owner-contact")
				record, _ := models4datatug.NewProjectContactLinkageRecord(ref)
				if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
					return tx.Delete(ctx, record.Key())
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, service, ctx := activatingPaidCreateFixture(t, test.role)
			deletePaidQuota(t, db)
			if test.setup != nil {
				test.setup(t, db)
			}
			repo := githubRepo()
			if _, err := service.CreateGitHubProject(ctx, githubCommand(), repo); err == nil {
				t.Fatal("expected create authorization failure")
			}
			if repo.commitCount != 0 || repo.head != createBaseHead || len(repo.files) != 0 {
				t.Fatal("rejected create mutated the GitHub repository")
			}
			assertPaidQuotaAbsent(t, db)
			inventory, err := NewDALProtectedProjectInventory(db)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inventory.CompleteProtectedProjectBasis(context.Background(), "live", "datatug", "personal-1"); err != nil {
				t.Fatalf("rejected create left project/admission inventory: %v", err)
			}
			assertDataTugNotActivated(t, db)
			opRecord, _ := models4datatug.NewGitHubProjectCreateOperationRecord("actor", "op")
			if err := db.Get(context.Background(), opRecord); !record.IsNotFound(err) {
				t.Fatalf("rejected create left operation record: %v", err)
			}
		})
	}
}

func TestActivatingCloudCreateDenialsLeaveAbsentQuotaAndSpaceUnchanged(t *testing.T) {
	for _, test := range []struct {
		name  string
		role  string
		setup func(*testing.T, dal.DB)
	}{
		{name: "Core role revoked", role: const4contactus.SpaceMemberRoleMember},
		{
			name: "paid access revoked",
			role: const4contactus.SpaceMemberRoleOwner,
			setup: func(t *testing.T, db dal.DB) {
				past := sharedTestTime.Add(-time.Minute)
				paidUpdate(t, db, models4datatug.NewCurrentPlanKey("personal-1"), "paidUntil", &past)
			},
		},
		{
			name: "owner contact unavailable",
			role: const4contactus.SpaceMemberRoleOwner,
			setup: func(t *testing.T, db dal.DB) {
				r, _ := models4datatug.NewProjectContactLinkageRecord(contactFixtureRef("space", "owner-contact"))
				if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error { return tx.Delete(ctx, r.Key()) }); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, service, ctx := activatingPaidCreateFixture(t, test.role)
			deletePaidQuota(t, db)
			if test.setup != nil {
				test.setup(t, db)
			}
			if _, err := service.Create(ctx, sharedCommand()); err == nil {
				t.Fatal("expected create authorization failure")
			}
			assertPaidQuotaAbsent(t, db)
			assertSharedAbsent(t, db, "space", "project-1", "command")
			admission, _ := models4datatug.NewProjectAdmissionRecord("space", "project-1")
			if err := db.Get(context.Background(), admission); !record.IsNotFound(err) {
				t.Fatalf("rejected create left an admission: %v", err)
			}
			assertDataTugNotActivated(t, db)
		})
	}
}

func deletePaidQuota(t *testing.T, db dal.DB) {
	t.Helper()
	r, _ := models4datatug.NewProtectedProjectQuotaRecord("live", "datatug", "personal-1")
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error { return tx.Delete(ctx, r.Key()) }); err != nil {
		t.Fatal(err)
	}
}

func assertPaidQuotaAbsent(t *testing.T, db dal.DB) {
	t.Helper()
	r, _ := models4datatug.NewProtectedProjectQuotaRecord("live", "datatug", "personal-1")
	if err := db.Get(context.Background(), r); !record.IsNotFound(err) {
		t.Fatalf("rejected create left quota record: %v", err)
	}
}

func activatingPaidCreateFixture(t *testing.T, role string) (dal.DB, *SharedProjectService, facade.ContextWithUser) {
	t.Helper()
	db, _, paid := paidCreateFixture(t)
	seedCoreSpaceWithRole(t, db, "space", "actor", role)
	service, err := NewActivatingPaidSharedProjectService(db, &sharedCounterIDs{}, func() time.Time { return sharedTestTime }, paid, SharedProjectActivationOptions{
		Activator: coreSpaceFacade.NewCapabilityAuthority(), RequiredRoles: []string{const4contactus.SpaceMemberRoleOwner}, InventoryQuery: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, service, facade.NewContextWithUserID(context.Background(), "actor")
}

func seedCoreSpaceWithRole(t *testing.T, db dal.DB, spaceID, actorID, role string) {
	t.Helper()
	now := sharedTestTime
	spaceData := new(dbo4spaceus.SpaceDbo)
	spaceData.Type = coretypes.SpaceTypeFamily
	spaceData.Status = dbmodels.StatusActive
	spaceData.UserIDs = []string{actorID}
	spaceData.CreatedAt = now
	spaceData.CreatedBy = actorID
	spaceData.UpdatedAt = now
	spaceData.UpdatedBy = actorID
	spaceData.Version = 1
	space := dbo4spaceus.NewSpaceEntryWithDbo(coretypes.SpaceID(spaceID), spaceData)
	userData := new(dbo4userus.UserDbo)
	userData.Type = briefs4contactus.ContactTypePerson
	userData.Status = dbmodels.StatusActive
	userData.Names = &person.NameFields{FirstName: "Space", LastName: "Owner"}
	userData.Gender = "unknown"
	userData.AgeGroup = "unknown"
	userData.CountryID = with.UnknownCountryID
	userData.CreatedAt = now
	userData.CreatedBy = actorID
	userData.Created = dbmodels.CreatedInfo{Client: dbmodels.RemoteClientInfo{HostOrApp: "unit-test", RemoteAddr: "127.0.0.1"}}
	userData.SetSpaceBrief(coretypes.SpaceID(spaceID), &dbo4userus.UserSpaceBrief{
		SpaceBrief: spaceData.SpaceBrief, UserContactID: "owner-contact", Roles: []string{role},
	})
	user := dbo4userus.NewUserEntryWithDbo(actorID, userData)
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.InsertMulti(ctx, []record.Record{space.Record, user.Record})
	}); err != nil {
		t.Fatal(err)
	}
}

func assertDataTugActivatedAndAllocated(t *testing.T, db dal.DB) {
	t.Helper()
	space := dbo4spaceus.NewSpaceEntry(coretypes.SpaceID("space"))
	if err := db.Get(context.Background(), space.Record); err != nil {
		t.Fatal(err)
	}
	for _, module := range space.Data.Modules {
		if module == "datatug" {
			if paidQuota(t, db).Allocated != 1 {
				t.Fatal("successful create did not allocate paid quota")
			}
			return
		}
	}
	t.Fatalf("successful create did not activate DataTug: %v", space.Data.Modules)
}

func assertDataTugNotActivated(t *testing.T, db dal.DB) {
	t.Helper()
	space := dbo4spaceus.NewSpaceEntry(coretypes.SpaceID("space"))
	if err := db.Get(context.Background(), space.Record); err != nil {
		t.Fatal(err)
	}
	for _, module := range space.Data.Modules {
		if module == "datatug" {
			t.Fatal("failed create activated DataTug")
		}
	}
}
