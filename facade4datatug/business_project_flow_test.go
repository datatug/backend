package facade4datatug

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/coretypes"
	"github.com/sneat-co/sneat-go-core/facade"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

type businessCurrentServiceReader struct {
	access contract4paymentus.CurrentSpaceServiceAccess
	err    error
	calls  int
}

func (*businessCurrentServiceReader) Mode() contract4paymentus.Mode {
	return contract4paymentus.ModeLive
}

func (r *businessCurrentServiceReader) ReadCurrentSpaceServiceAccess(_ context.Context, tx dal.ReadTransaction, scope contract4paymentus.ServicePurchaseScope) (contract4paymentus.CurrentSpaceServiceAccess, error) {
	r.calls++
	if tx == nil || scope.Mode != contract4paymentus.ModeLive || scope.ServiceID != BusinessProjectServiceID || scope.SpaceID == "" || scope != r.access.Scope || scope.SpaceID != r.access.PayerSpaceID {
		return contract4paymentus.CurrentSpaceServiceAccess{}, ErrBusinessServiceUnproved
	}
	if _, writable := tx.(dal.ReadwriteTransaction); writable {
		return contract4paymentus.CurrentSpaceServiceAccess{}, ErrBusinessServiceUnproved
	}
	if r.err != nil {
		return contract4paymentus.CurrentSpaceServiceAccess{}, r.err
	}
	return r.access, nil
}

func newBusinessProjectFixture(t *testing.T) (dal.DB, *SharedProjectService, *businessCurrentServiceReader) {
	t.Helper()
	db := sneatcoretesting.NewMemoryDB()
	seedPaidOwnerContact(t, db, "space")
	access := validPaymentusBusinessAccess(sharedTestTime)
	access.Scope.SpaceID = "space"
	access.PayerSpaceID = "space"
	reader := &businessCurrentServiceReader{access: access}
	service, err := NewBusinessSharedProjectService(db, &sharedCounterIDs{}, &sharedAuthority{}, func() time.Time { return sharedTestTime }, BusinessSharedProjectOptions{
		AccessPolicy:  BusinessProjectAccessPolicy{GrantVersion: "business-project-v1"},
		ServiceReader: reader,
		ContactLinks:  paidFixtureOwnerLinks(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, service, reader
}

func advancingBusinessClock() func() time.Time {
	current := sharedTestTime
	return func() time.Time {
		current = current.Add(time.Microsecond)
		return current
	}
}

func TestBusinessCreateSharedProjectUsesLiveSpaceAuthorityWithoutProQuota(t *testing.T) {
	db, service, reader := newBusinessProjectFixture(t)
	command := sharedCommand()
	ref, err := service.Create(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 {
		t.Fatalf("current service reader calls = %d", reader.calls)
	}
	admissionRecord, admission := models4datatug.NewProjectAdmissionRecord(ref.SpaceID, ref.ProjectID)
	if err := db.Get(context.Background(), admissionRecord); err != nil {
		t.Fatal(err)
	}
	if admission.Validate() != nil || admission.Version != 2 || admission.PayerID != ref.SpaceID || admission.Product != BusinessProjectProductID || admission.ServiceID != BusinessProjectServiceID || admission.PlanID != BusinessMonthlyPlanID || admission.SubscriptionID != "subscription" || admission.PaidServiceProofID != "reconciled-service" || admission.QuotaBasisDigest != "" || admission.QuotaRevision != 0 {
		t.Fatalf("wrong Business admission: %+v", admission)
	}
	quotaRecord, _ := models4datatug.NewProtectedProjectQuotaRecord("live", "datatug", ref.SpaceID)
	if err := db.Get(context.Background(), quotaRecord); !record.IsNotFound(err) {
		t.Fatalf("Business created a Pro quota: %v", err)
	}
	projectRecord, project := models4datatug.NewSharedLinkedProjectRecord(ref.SpaceID, ref.ProjectID)
	if err := db.Get(context.Background(), projectRecord); err != nil {
		t.Fatal(err)
	}
	projectRef := contract4linkage.RelationshipEntityRef{SpaceID: coretypes.SpaceID(ref.SpaceID), ItemRef: coretypes.ItemRef{ExtID: "datatug", Collection: "projects", ItemID: ref.ProjectID}}
	if err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		got, err := readLinkedProjectAdmission(ctx, tx, projectRef, project)
		if err != nil || got.Version != 2 || got.PaidServiceProofID != "reconciled-service" {
			t.Fatalf("linked Business admission %+v, %v", got, err)
		}
		proOnly := &SharedProjectService{paid: &PaidSharedProjectOptions{}}
		if err := proOnly.verifyCurrentProjectService(ctx, tx, got, sharedTestTime); !errors.Is(err, ErrBusinessServiceUnproved) {
			t.Fatalf("Pro-only service accepted Business admission: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if replay, err := service.Create(context.Background(), command); err != nil || replay != ref {
		t.Fatalf("same-command replay %+v: %v", replay, err)
	}
	if _, err := service.ReadSharedProjectAIEligibility(context.Background(), "actor", ref.SpaceID, ref.ProjectID); !errors.Is(err, ErrProjectAIEligibilityUnavailable) {
		t.Fatalf("unresolved Business AI entitlement was enabled: %v", err)
	}
}

func TestBusinessGitHubCloneAndQueryWriteRecheckCurrentSpacePayer(t *testing.T) {
	db, service, reader := newBusinessProjectFixture(t)
	command := githubCommand()
	repo := githubRepo()
	repo.failBeforeWrite = true
	if _, err := service.CreateGitHubProject(context.Background(), command, repo); !errors.Is(err, ErrGitHubOutcomeUncertain) {
		t.Fatalf("unavailable provider did not retain a retryable Business reservation: %v", err)
	}
	repo.failBeforeWrite = false
	created, err := service.CreateGitHubProject(context.Background(), command, repo)
	if err != nil || created.SharedProjectID == "" || created.TemplateID != command.Source.TemplateID {
		t.Fatalf("Business GitHub clone %+v: %v", created, err)
	}
	admissionRecord, admission := models4datatug.NewProjectAdmissionRecord(command.SpaceID, created.SharedProjectID)
	if err := db.Get(context.Background(), admissionRecord); err != nil || admission.Validate() != nil || admission.Version != 2 {
		t.Fatalf("Business clone admission %+v: %v", admission, err)
	}

	// A same-Space replacement can be current without rewriting immutable
	// creation provenance; the current payer/product identity remains exact.
	reader.access.OwnerSubscriptionID = "replacement-subscription"
	reader.access.OwnerGeneration = 4
	reader.access.OwnerRevision = 1
	reader.access.PaidServiceProofID = "replacement-proof"
	reader.access.PlanID = BusinessAnnualPlanID
	service.now = advancingBusinessClock()
	service.business.now = service.now
	queryRepo := &fakeGitHubQueryRepo{scope: GitHubCreateRepositoryScope{ActorID: "actor", RepositoryID: 123, Owner: "owner", Name: "repo", Permission: "write"}, head: createNewHead}
	if _, err := service.SaveGitHubQuery(context.Background(), "actor", 123, "owner", "repo", "datatug", querySaveRequest(), queryRepo); err != nil || queryRepo.commits != 1 {
		t.Fatalf("current replacement could not save query; commits=%d err=%v", queryRepo.commits, err)
	}
	if _, err := service.AuthorizeGitHubProjectWrite(context.Background(), "actor", 123, "owner", "repo", "datatug"); err != nil {
		t.Fatalf("advancing clock rejected current Business project access: %v", err)
	}
	if err := db.Get(context.Background(), admissionRecord); err != nil {
		t.Fatal(err)
	}
	if admission.SubscriptionID != "subscription" || admission.OwnerGeneration != 2 || admission.PaidServiceProofID != "reconciled-service" {
		t.Fatalf("immutable admission followed replacement owner: %+v", admission)
	}

	reader.access.State = contract4paymentus.ServiceCurrentFinancialRefunded
	request := querySaveRequest()
	request.OperationID = "save-after-refund"
	if _, err := service.SaveGitHubQuery(context.Background(), "actor", 123, "owner", "repo", "datatug", request, queryRepo); !errors.Is(err, ErrBusinessServiceEnded) || queryRepo.commits != 1 {
		t.Fatalf("refunded base allowed hosted query write: commits=%d err=%v", queryRepo.commits, err)
	}
	if _, err := service.AuthorizeGitHubProjectWrite(context.Background(), "actor", 123, "owner", "repo", "datatug"); !errors.Is(err, ErrBusinessServiceEnded) {
		t.Fatalf("refunded base allowed hosted project access: %v", err)
	}
}

func TestBusinessLinkageUsesCurrentGrantAndDeniesAtPaidEnd(t *testing.T) {
	db, service, reader := newBusinessProjectFixture(t)
	ref, err := service.Create(context.Background(), sharedCommand())
	if err != nil {
		t.Fatal(err)
	}
	authority := &projectRoleFixtureAuthority{t: t}
	policy, err := NewPaidProjectLinkagePolicy(PaidProjectLinkageOptions{
		Business: service.business, Roles: paidFixtureOwnerLinks().Roles, Contacts: projectContactFixturePort{},
		Manager: authority, Targets: authority, Now: advancingBusinessClock(),
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &linkagePolicyFixture{t: t, db: db, service: service, policy: policy, authority: authority, project: projectFixtureRef(ref.SpaceID, ref.ProjectID)}
	seedProjectContact(t, db, contactFixtureRef(ref.SpaceID, "team-contact"), "teammate", true)
	err = db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		read := sharedProjectReadTransaction{tx}
		fixture.authority.expectedTX = sharedProjectReadTransaction{read}
		batch, err := fixture.batch(ctx, read, "team-contact", []string{"role-b"}, nil, false)
		if err != nil {
			return err
		}
		if err := policy.AuthorizeRelationshipMutation(ctx, read, batch); err != nil {
			t.Fatalf("Business contact assignment refused: %v", err)
		}
		reader.access.State = contract4paymentus.ServiceCurrentFinancialEnded
		if err := policy.AuthorizeRelationshipMutation(ctx, read, batch); !errors.Is(err, ErrBusinessServiceEnded) {
			t.Fatalf("ended Business service allowed assignment: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBusinessFirstCreateActivatesFreshSpaceOnlyWithAcceptedCommand(t *testing.T) {
	for _, test := range []struct {
		name   string
		github bool
	}{
		{name: "cloud project"},
		{name: "GitHub clone", github: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := sneatcoretesting.NewMemoryDB()
			seedPaidOwnerContact(t, db, "space")
			access := validPaymentusBusinessAccess(sharedTestTime)
			access.Scope.SpaceID, access.PayerSpaceID = "space", "space"
			reader := &businessCurrentServiceReader{access: access}
			plan := &activationProbe{}
			activator := &transactionalActivatorProbe{plan: plan}
			service, err := NewActivatingBusinessSharedProjectService(db, &sharedCounterIDs{}, advancingBusinessClock(), BusinessSharedProjectOptions{
				AccessPolicy:  BusinessProjectAccessPolicy{GrantVersion: "business-project-v1"},
				ServiceReader: reader, ContactLinks: paidFixtureOwnerLinks(),
			}, SharedProjectActivationOptions{Activator: activator, RequiredRoles: []string{"content-admin", "content-editor"}})
			if err != nil {
				t.Fatal(err)
			}
			ctx := facade.NewContextWithUserID(context.Background(), "actor")
			var businessProjectID string
			if test.github {
				created, err := service.CreateGitHubProject(ctx, githubCommand(), githubRepo())
				if err != nil {
					t.Fatal(err)
				}
				businessProjectID = created.SharedProjectID
			} else {
				created, err := service.Create(ctx, sharedCommand())
				if err != nil {
					t.Fatal(err)
				}
				businessProjectID = created.ProjectID
			}
			if activator.called != 1 || !plan.applied || activator.request.SpaceID != coretypes.SpaceID("space") || activator.request.Capability != "datatug" {
				t.Fatalf("fresh-Space capability was not transactionally activated: calls=%d applied=%v request=%+v", activator.called, plan.applied, activator.request)
			}
			if reader.calls < 1 {
				t.Fatal("first create did not consult current LIVE Business authority")
			}
			if activator.request.Purpose != "shared-project-create" || activator.request.Stage != "enable-datatug" || len(activator.request.RequiredRoles) != 2 {
				t.Fatalf("activation was not scoped to explicit project creation: %+v", activator.request)
			}
			admissionRecord, admission := models4datatug.NewProjectAdmissionRecord("space", businessProjectID)
			if test.github {
				createRecord, operation := models4datatug.NewGitHubProjectCreateOperationRecord("actor", "op")
				if err := db.Get(context.Background(), createRecord); err != nil {
					t.Fatal(err)
				}
				admissionRecord, admission = models4datatug.NewProjectAdmissionRecord("space", operation.ProjectID)
			}
			if err := db.Get(context.Background(), admissionRecord); err != nil || admission.Validate() != nil || admission.Version != 2 || admission.PayerID != "space" {
				t.Fatalf("first create did not retain Business Space admission: %+v, %v", admission, err)
			}
			quotaRecord, _ := models4datatug.NewProtectedProjectQuotaRecord("live", "datatug", "space")
			if err := db.Get(context.Background(), quotaRecord); !record.IsNotFound(err) {
				t.Fatalf("Business first create initialized Pro quota: %v", err)
			}
			reader.access.State = contract4paymentus.ServiceCurrentFinancialRefunded
			var denied error
			if test.github {
				deniedRepo := githubRepo()
				deniedCommand := githubCommand()
				deniedCommand.OperationID = "after-refund"
				_, denied = service.CreateGitHubProject(ctx, deniedCommand, deniedRepo)
				if deniedRepo.commitCount != 0 {
					t.Fatalf("refunded Business Space changed GitHub: %d commits", deniedRepo.commitCount)
				}
			} else {
				deniedCommand := sharedCommand()
				deniedCommand.CommandID = "after-refund"
				_, denied = service.Create(ctx, deniedCommand)
			}
			if !errors.Is(denied, ErrBusinessServiceEnded) || activator.called != 1 {
				t.Fatalf("refunded first-create path reached capability activation: err=%v calls=%d", denied, activator.called)
			}
		})
	}
}

func TestUnifiedProBusinessServiceSelectsOnlyRequestedVerifiedPlan(t *testing.T) {
	for _, tc := range []struct {
		name        string
		intent      SharedProjectBillingIntent
		github      bool
		wantVersion int
		wantPayer   string
	}{
		{name: "omitted intent defaults to Pro", wantVersion: 1, wantPayer: "personal-1"},
		{name: "Pro sponsors ordinary Space", intent: BillingIntentPersonalPro, wantVersion: 1, wantPayer: "personal-1"},
		{name: "Business pays selected Space", intent: BillingIntentSpaceBusiness, wantVersion: 2, wantPayer: "space"},
		{name: "Pro GitHub clone", intent: BillingIntentPersonalPro, github: true, wantVersion: 1, wantPayer: "personal-1"},
		{name: "Business GitHub clone", intent: BillingIntentSpaceBusiness, github: true, wantVersion: 2, wantPayer: "space"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, pro := paidCreateFixture(t)
			access := validPaymentusBusinessAccess(sharedTestTime)
			access.Scope.SpaceID, access.PayerSpaceID = "space", "space"
			reader := &businessCurrentServiceReader{access: access}
			activator := &transactionalActivatorProbe{plan: &activationProbe{}}
			service, err := NewActivatingProBusinessSharedProjectService(db, &sharedCounterIDs{}, func() time.Time { return sharedTestTime }, ProBusinessSharedProjectOptions{
				Pro: pro, Business: BusinessSharedProjectOptions{AccessPolicy: BusinessProjectAccessPolicy{GrantVersion: "business-project-v1"}, ServiceReader: reader},
			}, SharedProjectActivationOptions{Activator: activator, RequiredRoles: []string{"content-admin", "content-editor"}, InventoryQuery: db})
			if err != nil {
				t.Fatal(err)
			}
			command := sharedCommand()
			command.BillingIntent = tc.intent
			var ref models4datatug.SharedProjectRef
			if tc.github {
				github := githubCommand()
				github.BillingIntent = tc.intent
				created, err := service.CreateGitHubProject(facade.NewContextWithUserID(context.Background(), "actor"), github, githubRepo())
				if err != nil {
					t.Fatalf("GitHub create with %q: %v", tc.intent, err)
				}
				ref = models4datatug.SharedProjectRef{StoreID: models4datatug.GithubStoreID, SpaceID: github.SpaceID, ProjectID: created.SharedProjectID}
			} else {
				var err error
				ref, err = service.Create(facade.NewContextWithUserID(context.Background(), "actor"), command)
				if err != nil {
					t.Fatalf("create with %q: %v", tc.intent, err)
				}
			}
			r, admission := models4datatug.NewProjectAdmissionRecord(ref.SpaceID, ref.ProjectID)
			if err := db.Get(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if admission.Validate() != nil || admission.Version != tc.wantVersion || admission.PayerID != tc.wantPayer {
				t.Fatalf("billing intent selected wrong durable admission: %+v", admission)
			}
			if tc.wantVersion == 1 && reader.calls != 0 {
				t.Fatalf("Pro intent queried Business authority %d times", reader.calls)
			}
			if tc.wantVersion == 2 && reader.calls == 0 {
				t.Fatal("Business intent skipped current selected-Space proof")
			}
			if activator.called == 0 || !activator.plan.(*activationProbe).applied {
				t.Fatalf("unified service skipped explicit activation: calls=%d", activator.called)
			}
			bad := command
			bad.CommandID = "bad-intent"
			bad.BillingIntent = "unlimited"
			if _, err := service.Create(facade.NewContextWithUserID(context.Background(), "actor"), bad); !errors.Is(err, ErrSharedProjectInvalid) {
				t.Fatalf("unknown billing intent accepted: %v", err)
			}
		})
	}
}

func TestSharedProjectCreateBindingRejectsUnconfiguredAndWrongPlanIntent(t *testing.T) {
	db, _, proOptions := paidCreateFixture(t)
	_, businessOnly, _ := newBusinessProjectFixture(t)

	tests := []struct {
		name    string
		service *SharedProjectService
		intent  SharedProjectBillingIntent
		wantErr error
	}{
		{name: "legacy service may not claim a paid plan", service: &SharedProjectService{}, intent: BillingIntentSpaceBusiness, wantErr: ErrSharedProjectUnauthorized},
		{name: "Pro-only service may not select Business", service: &SharedProjectService{paid: &proOptions}, intent: BillingIntentSpaceBusiness, wantErr: ErrSharedProjectUnauthorized},
		{name: "Business-only service may not select Pro", service: businessOnly, intent: BillingIntentPersonalPro, wantErr: ErrSharedProjectUnauthorized},
		{name: "unsupported intent is rejected", service: &SharedProjectService{paid: &proOptions}, intent: "unlimited", wantErr: ErrSharedProjectInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binding, err := tc.service.resolveSharedProjectCreateBinding(context.Background(), "actor", "space", "command", "Project", tc.intent)
			if !errors.Is(err, tc.wantErr) || binding.PayerID != "" || binding.Mode != "" || binding.Product != "" {
				t.Fatalf("invalid plan selection returned binding %+v, error %v; want %v", binding, err, tc.wantErr)
			}
		})
	}

	t.Run("Business-only service defaults only to its configured plan", func(t *testing.T) {
		binding, err := businessOnly.resolveSharedProjectCreateBinding(context.Background(), "actor", "business-space", "command", "Project", "")
		if err != nil || binding.PayerID != "business-space" || binding.Mode != "live" || binding.Product != BusinessProjectProductID {
			t.Fatalf("Business-only default did not bind its selected Space: %+v, %v", binding, err)
		}
	})

	t.Run("invalid directory payer cannot become Pro binding", func(t *testing.T) {
		badOptions := proOptions
		badOptions.Directory = paidCreateDirectory{payer: "not/a/space-id"}
		service := &SharedProjectService{paid: &badOptions}
		binding, err := service.resolveSharedProjectCreateBinding(context.Background(), "actor", "space", "command", "Project", BillingIntentPersonalPro)
		if !errors.Is(err, ErrSharedProjectUnauthorized) || binding.PayerID != "" {
			t.Fatalf("invalid directory identity became paid binding %+v, %v", binding, err)
		}
	})

	// Keep the legacy no-intent path available without treating it as paid.
	t.Run("unconfigured legacy selection carries no payer", func(t *testing.T) {
		service, err := NewSharedProjectService(db, &sharedCounterIDs{}, &sharedAuthority{}, func() time.Time { return sharedTestTime })
		if err != nil {
			t.Fatal(err)
		}
		binding, err := service.resolveSharedProjectCreateBinding(context.Background(), "actor", "space", "command", "Project", "")
		if err != nil || binding.PayerID != "" || binding.Mode != "" || binding.Product != "" {
			t.Fatalf("legacy empty intent acquired paid authority: %+v, %v", binding, err)
		}
	})
}
