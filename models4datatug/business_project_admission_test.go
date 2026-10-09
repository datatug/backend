package models4datatug

import (
	"testing"
	"time"

	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/coretypes"
)

func validBusinessAdmission() ProjectAdmission {
	return ProjectAdmission{
		Version: 2, Mode: "live", Product: "datatug-business-usage", PayerID: "business-space",
		ActorID: "creator", SpaceID: "business-space", ProjectID: "project", CommandID: "operation",
		RequestDigest: "bound-request", LimitsVersion: "business-grant-v1", ProfileVersion: "business-profile-v1",
		SubscriptionID: "subscription", OwnerGeneration: 3, OwnerRevision: 5,
		QuotaBasisDigest: "complete-inventory", QuotaRevision: 1, ServiceID: "datatug",
		PlanID: "datatug-business-usage-monthly", PaidServiceProofID: "paid-service-proof",
		UnlimitedProjects: true, UnlimitedContacts: true, CreatedAt: time.Now().UTC(),
		OwnerContact: ProjectOwnerContactProof{
			Version: 1, Role: "owner", CatalogVersion: "1",
			Contact: contract4linkage.RelationshipEntityRef{
				SpaceID: coretypes.SpaceID("business-space"),
				ItemRef: contract4linkage.ItemRef{ExtID: "contactus", Collection: "contacts", ItemID: "owner-contact"},
			},
		},
	}
}

func TestBusinessAdmissionRequiresExplicitUnlimitedServiceProvenance(t *testing.T) {
	valid := validBusinessAdmission()
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ProjectAdmission){
		"wrong version":      func(a *ProjectAdmission) { a.Version = 3 },
		"TEST":               func(a *ProjectAdmission) { a.Mode = "test" },
		"personal product":   func(a *ProjectAdmission) { a.Product = "datatug" },
		"foreign payer":      func(a *ProjectAdmission) { a.PayerID = "foreign-space" },
		"missing service":    func(a *ProjectAdmission) { a.ServiceID = "" },
		"wrong service":      func(a *ProjectAdmission) { a.ServiceID = "sso" },
		"old plan":           func(a *ProjectAdmission) { a.PlanID = "datatug-business" },
		"missing paid proof": func(a *ProjectAdmission) { a.PaidServiceProofID = "" },
		"missing revision":   func(a *ProjectAdmission) { a.OwnerRevision = 0 },
		"missing owner":      func(a *ProjectAdmission) { a.OwnerContact = ProjectOwnerContactProof{} },
		"foreign owner": func(a *ProjectAdmission) {
			a.OwnerContact.Contact.SpaceID = coretypes.SpaceID("foreign-space")
		},
		"wrong owner role":    func(a *ProjectAdmission) { a.OwnerContact.Role = "viewer" },
		"invalid paid proof":  func(a *ProjectAdmission) { a.PaidServiceProofID = "../proof" },
		"finite projects":     func(a *ProjectAdmission) { a.UnlimitedProjects = false },
		"finite contacts":     func(a *ProjectAdmission) { a.UnlimitedContacts = false },
		"mixed project count": func(a *ProjectAdmission) { a.ProtectedProjectsLimit = 1 },
		"mixed contact count": func(a *ProjectAdmission) { a.ProtectedUsersLimit = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			a := valid
			change(&a)
			if err := a.Validate(); err == nil {
				t.Fatalf("accepted malformed Business admission: %+v", a)
			}
		})
	}
}

func TestFiniteProAdmissionCannotAdoptBusinessFields(t *testing.T) {
	a := ProjectAdmission{
		Version: 1, Mode: "live", Product: "datatug", PayerID: "personal-space", ActorID: "actor",
		SpaceID: "project-space", ProjectID: "project", CommandID: "command", RequestDigest: "digest",
		LimitsVersion: "pro-v1", SubscriptionID: "subscription", ProfileVersion: "profile-v1",
		QuotaBasisDigest: "complete-inventory", ProtectedProjectsLimit: 5, ProtectedUsersLimit: 5,
		QuotaRevision: 1, OwnerGeneration: 1, CreatedAt: time.Now().UTC(),
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ProjectAdmission){
		"unlimited projects": func(a *ProjectAdmission) { a.UnlimitedProjects = true },
		"unlimited contacts": func(a *ProjectAdmission) { a.UnlimitedContacts = true },
		"service":            func(a *ProjectAdmission) { a.ServiceID = "datatug" },
		"paid proof":         func(a *ProjectAdmission) { a.PaidServiceProofID = "proof" },
		"owner revision":     func(a *ProjectAdmission) { a.OwnerRevision = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			mixed := a
			change(&mixed)
			if err := mixed.Validate(); err == nil {
				t.Fatalf("finite Pro admission adopted Business fields: %+v", mixed)
			}
		})
	}
}
