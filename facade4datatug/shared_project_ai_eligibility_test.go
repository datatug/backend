package facade4datatug

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record/update"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

func paidSharedProjectWithReader(t *testing.T) (*linkagePolicyFixture, time.Time) {
	t.Helper()
	f := newLinkagePolicyFixture(t)
	seedProjectContact(t, f.db, contactFixtureRef("space", "reader-contact"), "reader", true)
	if err := f.db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		batch, err := f.batch(ctx, tx, "reader-contact", []string{"role-b"}, nil, false)
		if err != nil {
			return err
		}
		return writeFixtureBatch(ctx, tx, batch)
	}); err != nil {
		t.Fatal(err)
	}
	f.service.now = func() time.Time { return sharedTestTime }
	return f, sharedTestTime.Add(24 * time.Hour)
}

func TestSharedProjectAIEligibilitySeparatesSponsorFromReadOnlyActor(t *testing.T) {
	f, paidUntil := paidSharedProjectWithReader(t)
	eligibility, err := f.service.ReadSharedProjectAIEligibility(context.Background(), "reader", string(f.project.SpaceID), f.project.ItemRef.ItemID)
	if err != nil || !eligibility.AIAllowed || eligibility.Reason != "" {
		t.Fatalf("active sponsor should allow read-only member AI: %+v, %v", eligibility, err)
	}
	data, err := json.Marshal(eligibility)
	if err != nil || strings.Contains(string(data), "payer") || strings.Contains(string(data), "subscription") {
		t.Fatalf("eligibility exposed sponsor data: %s, %v", data, err)
	}

	f.service.now = func() time.Time { return paidUntil }
	eligibility, err = f.service.ReadSharedProjectAIEligibility(context.Background(), "reader", string(f.project.SpaceID), f.project.ItemRef.ItemID)
	if err != nil || eligibility.AIAllowed || eligibility.Reason != "plan_ended" {
		t.Fatalf("exact paid-through boundary should end shared-project AI: %+v, %v", eligibility, err)
	}
}

type aiEligibilityOwnerProofProbe struct {
	t              *testing.T
	actor, payer   string
	readTx         dal.ReadTransaction
	personalCalls  int
	ownerReadCalls int
}

func (p *aiEligibilityOwnerProofProbe) VerifyPersonalOwner(ctx context.Context, tx dal.ReadTransaction, actor, payer string) error {
	p.t.Helper()
	if actor != p.actor || payer != p.payer || tx == nil {
		p.t.Fatalf("personal sponsor proof used actor=%q payer=%q tx=%T", actor, payer, tx)
	}
	p.readTx = tx
	p.personalCalls++
	return (paidCreateAuthority{}).VerifyPersonalOwner(ctx, tx, actor, payer)
}

func (p *aiEligibilityOwnerProofProbe) ReadOwner(ctx context.Context, tx dal.ReadTransaction, mode, product, payer string) (PlanOwnerFence, error) {
	p.t.Helper()
	if tx == nil || tx != p.readTx || payer != p.payer {
		p.t.Fatalf("plan owner read escaped sponsor proof transaction: tx=%T payer=%q", tx, payer)
	}
	p.ownerReadCalls++
	return (paidCreateAuthority{}).ReadOwner(ctx, tx, mode, product, payer)
}

func TestSharedProjectAIEligibilityProvesAdmittedCreatorInSameReadTransaction(t *testing.T) {
	f, _ := paidSharedProjectWithReader(t)
	probe := &aiEligibilityOwnerProofProbe{t: t, actor: "actor", payer: "personal-1"}
	f.service.paid.Personal, f.service.paid.Owner = probe, probe
	eligibility, err := f.service.ReadSharedProjectAIEligibility(context.Background(), "reader", string(f.project.SpaceID), f.project.ItemRef.ItemID)
	if err != nil || !eligibility.AIAllowed || probe.personalCalls != 1 || probe.ownerReadCalls != 1 {
		t.Fatalf("admitted sponsor proof %+v calls=%d/%d error=%v", eligibility, probe.personalCalls, probe.ownerReadCalls, err)
	}
}

func TestUnifiedBusinessAdmissionCannotInheritProAIWhenPayerIDsCoincide(t *testing.T) {
	for _, github := range []bool{false, true} {
		name := "cloud"
		if github {
			name = "GitHub"
		}
		t.Run(name, func(t *testing.T) {
			db, _, pro := paidCreateFixture(t)
			seedPaidOwnerContact(t, db, "personal-1")
			access := validPaymentusBusinessAccess(sharedTestTime)
			access.Scope.SpaceID, access.PayerSpaceID = "personal-1", "personal-1"
			reader := &businessCurrentServiceReader{access: access}
			service, err := NewProBusinessSharedProjectService(db, &sharedCounterIDs{}, &sharedAuthority{}, func() time.Time { return sharedTestTime }, ProBusinessSharedProjectOptions{
				Pro: pro, Business: BusinessSharedProjectOptions{AccessPolicy: BusinessProjectAccessPolicy{GrantVersion: "business-v1", Mode: contract4paymentus.ModeLive}, ServiceReader: reader},
			})
			if err != nil {
				t.Fatal(err)
			}
			var projectID string
			if github {
				command := githubCommand()
				command.SpaceID = "personal-1"
				command.BillingIntent = BillingIntentSpaceBusiness
				created, err := service.CreateGitHubProject(context.Background(), command, githubRepo())
				if err != nil {
					t.Fatalf("create Business GitHub project: %v", err)
				}
				projectID = created.SharedProjectID
			} else {
				command := sharedCommand()
				command.SpaceID = "personal-1"
				command.BillingIntent = BillingIntentSpaceBusiness
				created, err := service.Create(context.Background(), command)
				if err != nil {
					t.Fatalf("create Business cloud project: %v", err)
				}
				projectID = created.ProjectID
			}
			admissionRecord, admission := models4datatug.NewProjectAdmissionRecord("personal-1", projectID)
			if err := db.Get(context.Background(), admissionRecord); err != nil || admission.Validate() != nil || admission.Version != 2 || admission.Product != BusinessProjectProductID || admission.PayerID != "personal-1" {
				t.Fatalf("coincident-payer fixture is not a valid Business admission: %+v, %v", admission, err)
			}
			proof := &aiEligibilityOwnerProofProbe{t: t, actor: "actor", payer: "personal-1"}
			service.paid.Personal, service.paid.Owner = proof, proof
			var eligibility ProjectAIEligibility
			if github {
				eligibility, err = service.ReadGitHubProjectAIEligibility(context.Background(), "actor", 123, "owner", "repo", "datatug")
			} else {
				eligibility, err = service.ReadSharedProjectAIEligibility(context.Background(), "actor", "personal-1", projectID)
			}
			if !errors.Is(err, ErrProjectAIEligibilityUnavailable) || eligibility.AIAllowed || eligibility.Reason != "" || proof.personalCalls != 0 || proof.ownerReadCalls != 0 {
				t.Fatalf("Business admission inherited Pro AI or reached sponsor authority: result=%+v proof=%d/%d err=%v", eligibility, proof.personalCalls, proof.ownerReadCalls, err)
			}
		})
	}
}

func TestSharedProjectAIEligibilityClassifiesEndedAndUnprovedPlansSeparately(t *testing.T) {
	f, _ := paidSharedProjectWithReader(t)
	planKey := models4datatug.NewCurrentPlanKey("personal-1")
	paidUpdate(t, f.db, planKey, "plan", "free")
	paidUpdate(t, f.db, planKey, "status", "ended")
	paidUpdate(t, f.db, planKey, "period", "none")
	paidUpdate(t, f.db, planKey, "limits", nil)
	paidUpdate(t, f.db, planKey, "founding", false)
	paidUpdate(t, f.db, planKey, "endedReason", "canceled")
	eligibility, err := f.service.ReadSharedProjectAIEligibility(context.Background(), "reader", string(f.project.SpaceID), f.project.ItemRef.ItemID)
	if err != nil || eligibility.AIAllowed || eligibility.Reason != "plan_ended" {
		t.Fatalf("known ended plan %+v, %v", eligibility, err)
	}

	paidUpdate(t, f.db, models4datatug.NewPlanApplicationKey("live", "datatug", "personal-1"), "lastProPaidServiceProofId", "")
	eligibility, err = f.service.ReadSharedProjectAIEligibility(context.Background(), "reader", string(f.project.SpaceID), f.project.ItemRef.ItemID)
	if !errors.Is(err, ErrPlanEffectUnproved) || eligibility.AIAllowed || eligibility.Reason == "plan_ended" {
		t.Fatalf("unproved sponsor was conflated with plan expiry: %+v, %v", eligibility, err)
	}
}

func TestSharedProjectAIEligibilityRejectsMalformedSponsorEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *linkagePolicyFixture)
	}{
		{name: "missing paid until", mutate: func(t *testing.T, f *linkagePolicyFixture) {
			paidUpdate(t, f.db, models4datatug.NewCurrentPlanKey("personal-1"), "paidUntil", nil)
		}},
		{name: "unknown plan", mutate: func(t *testing.T, f *linkagePolicyFixture) {
			paidUpdate(t, f.db, models4datatug.NewCurrentPlanKey("personal-1"), "plan", "business")
		}},
		{name: "unknown status", mutate: func(t *testing.T, f *linkagePolicyFixture) {
			paidUpdate(t, f.db, models4datatug.NewCurrentPlanKey("personal-1"), "status", "paused")
		}},
		{name: "malformed limits", mutate: func(t *testing.T, f *linkagePolicyFixture) {
			paidUpdate(t, f.db, models4datatug.NewCurrentPlanKey("personal-1"), "limits", nil)
		}},
		{name: "malformed models", mutate: func(_ *testing.T, f *linkagePolicyFixture) {
			f.service.paid.Config.ProModels = []PlanModel{{ID: "model", Class: "unknown", Weight: 1, Default: true}}
		}},
		{name: "admitted owner mismatch", mutate: func(_ *testing.T, f *linkagePolicyFixture) {
			f.service.paid.Personal = personalOwnerProofFunc(func(context.Context, dal.ReadTransaction, string, string) error {
				return errors.New("owner mismatch")
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f, _ := paidSharedProjectWithReader(t)
			test.mutate(t, f)
			eligibility, err := f.service.ReadSharedProjectAIEligibility(context.Background(), "reader", string(f.project.SpaceID), f.project.ItemRef.ItemID)
			if err == nil || eligibility.AIAllowed || eligibility.Reason == "plan_ended" {
				t.Fatalf("malformed evidence reported as confirmed expiry: %+v, %v", eligibility, err)
			}
		})
	}
}

type personalOwnerProofFunc func(context.Context, dal.ReadTransaction, string, string) error

func (f personalOwnerProofFunc) VerifyPersonalOwner(ctx context.Context, tx dal.ReadTransaction, actor, payer string) error {
	return f(ctx, tx, actor, payer)
}

func TestSharedProjectAIEligibilityRequiresCurrentLinkedReaderAndFullLocator(t *testing.T) {
	f, _ := paidSharedProjectWithReader(t)
	if _, err := f.service.ReadSharedProjectAIEligibility(context.Background(), "stranger", string(f.project.SpaceID), f.project.ItemRef.ItemID); !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatalf("unlinked actor was authorized: %v", err)
	}
	if _, err := f.service.ReadSharedProjectAIEligibility(context.Background(), "reader", "other-space", f.project.ItemRef.ItemID); err == nil {
		t.Fatal("eligibility from one Space was reused for another project locator")
	}
}

func TestSharedProjectAIEligibilityRejectsBrokenReciprocalMembership(t *testing.T) {
	f, _ := paidSharedProjectWithReader(t)
	contact := contactFixtureRef("space", "reader-contact")
	project := f.project
	if err := f.db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		record, graph := models4datatug.NewProjectContactLinkageRecord(contact)
		return tx.Update(ctx, record.Key(), []update.Update{update.ByFieldPath([]string{"related"}, graph.Related), update.ByFieldPath([]string{"relatedIDs"}, graph.RelatedIDs)})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ReadSharedProjectAIEligibility(context.Background(), "reader", string(project.SpaceID), project.ItemRef.ItemID); err == nil {
		t.Fatal("one-sided project membership was accepted")
	}
}
