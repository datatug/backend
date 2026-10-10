// Copyright 2026 Sneat.co
package models4datatug

import (
	"testing"
	"time"

	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/coretypes"
)

func TestProjectQuotaLimitRequiresExplicitUnlimited(t *testing.T) {
	for _, v := range []struct {
		limit     ProjectQuotaLimit
		allocated int64
		want      bool
	}{{ProjectQuotaLimit{}, 0, false}, {ProjectQuotaLimit{Count: 5}, 4, true}, {ProjectQuotaLimit{Count: 5}, 5, false}, {ProjectQuotaLimit{Unlimited: true}, 500, true}, {ProjectQuotaLimit{Unlimited: true, Count: 5}, 0, false}, {ProjectQuotaLimit{Count: -1}, 0, false}, {ProjectQuotaLimit{Unlimited: true}, -1, false}} {
		if got := v.limit.Allows(v.allocated); got != v.want {
			t.Fatalf("limit %+v allocated %d = %v", v.limit, v.allocated, got)
		}
	}
}

func TestPaidAdmissionRecordsRefuseUnknownOrUnattributedState(t *testing.T) {
	quota := ProtectedProjectQuota{Version: 1, Mode: "live", Product: "datatug", PayerID: "payer", BasisDigest: "proven-inventory", Allocated: 1, Revision: 1}
	if err := quota.Validate(); err != nil {
		t.Fatal(err)
	}
	quota.Allocated = -1
	if err := quota.Validate(); err == nil {
		t.Fatal("negative allocation accepted")
	}
	quota.Allocated = 1
	quota.BasisDigest = ""
	if err := quota.Validate(); err == nil {
		t.Fatal("quota without complete basis accepted")
	}
	admission := ProjectAdmission{Version: 1, Mode: "live", Product: "datatug", PayerID: "payer", ActorID: "actor", SpaceID: "space", ProjectID: "project", CommandID: "op", RequestDigest: "digest", LimitsVersion: "v1", SubscriptionID: "sub", ProfileVersion: "v1", QuotaBasisDigest: "basis", ProtectedProjectsLimit: 5, ProtectedUsersLimit: 5, QuotaRevision: 1, OwnerGeneration: 1, CreatedAt: time.Now().UTC()}
	if err := admission.Validate(); err != nil {
		t.Fatal(err)
	}
	admission.QuotaRevision = 0
	if err := admission.Validate(); err == nil {
		t.Fatal("admission without quota revision accepted")
	}
	admission.QuotaRevision = 1
	admission.ProjectID = "../other"
	if err := admission.Validate(); err == nil {
		t.Fatal("admission with ambiguous project ID accepted")
	}
}

func TestBusinessAdmissionSupportsExplicitTestModeButNoUnknownMode(t *testing.T) {
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	owner := ProjectOwnerContactProof{
		Version: 1, Role: "owner", CatalogVersion: "roles-v1",
		Contact: contract4linkage.RelationshipEntityRef{
			SpaceID: coretypes.SpaceID("business-space"),
			ItemRef: coretypes.ItemRef{ExtID: "contactus", Collection: "contacts", ItemID: "owner-contact"},
		},
	}
	admission := ProjectAdmission{
		OwnerContact: owner, Version: 2, Mode: "test", Product: "datatug-business-usage", PayerID: "business-space",
		ActorID: "actor", SpaceID: "business-space", ProjectID: "project", CommandID: "command", RequestDigest: "digest",
		LimitsVersion: "business-test-v1", SubscriptionID: "subscription", ProfileVersion: "business-test-v1",
		OwnerGeneration: 1, CreatedAt: at, ServiceID: "datatug", PlanID: "datatug-business-usage-monthly",
		PaidServiceProofID: "test-proof", OwnerRevision: 1, UnlimitedProjects: true, UnlimitedContacts: true,
	}
	if err := admission.Validate(); err != nil {
		t.Fatalf("fixed TEST Business admission rejected structurally: %v", err)
	}
	admission.Mode = "sandbox"
	if err := admission.Validate(); err == nil {
		t.Fatal("unknown-mode Business admission accepted")
	}
}
func TestProjectQuotaKeysIsolateModeProductPayerAndSpace(t *testing.T) {
	seen := map[string]bool{}
	for _, mode := range []string{"live", "test"} {
		for _, product := range []string{"datatug", "other"} {
			for _, payer := range []string{"p1", "p2"} {
				r, _ := NewProtectedProjectQuotaRecord(mode, product, payer)
				key := r.Key().String()
				if seen[key] {
					t.Fatal("key collision", key)
				}
				seen[key] = true
			}
		}
	}
	a, _ := NewProjectAdmissionRecord("space-1", "same")
	b, _ := NewProjectAdmissionRecord("space-2", "same")
	if a.Key().String() == b.Key().String() {
		t.Fatal("project scope collision")
	}
}
