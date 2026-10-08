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
