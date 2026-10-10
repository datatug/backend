// Copyright 2026 Sneat.co
package models4datatug

import (
	"fmt"
	"time"

	"github.com/dal-go/record"
)

// These server-only roots require Firebase read/list exclusions at every depth
// before activation: spaces/{payer}/ext/datatug/protectedProjectQuota/{scope}
// and spaces/{projectSpace}/ext/datatug/projectAdmissions/{projectID}.
const ProtectedProjectQuotaCollection = "protectedProjectQuota"
const ProjectAdmissionCollection = "projectAdmissions"

// ProjectQuotaLimit distinguishes an explicit unlimited grant from absent data.
// Current Pro admission derives a finite limit from its frozen paid grants.
type ProjectQuotaLimit struct {
	Unlimited bool
	Count     int64
}

func (l ProjectQuotaLimit) Validate() error {
	if l.Count < 0 || (l.Unlimited && l.Count != 0) {
		return fmt.Errorf("invalid project quota limit")
	}
	return nil
}
func (l ProjectQuotaLimit) Allows(allocated int64) bool {
	return l.Validate() == nil && allocated >= 0 && (l.Unlimited || allocated < l.Count)
}

// ProtectedProjectQuota retains allocation count across renewals and Space
// boundaries. Missing records never mean zero. BasisDigest identifies a proved
// complete empty/imported inventory, not an operator assertion or billing term.
type ProtectedProjectQuota struct {
	Version                             int `firestore:"v"`
	Mode, Product, PayerID, BasisDigest string
	Allocated, Revision                 int64
}

func (q ProtectedProjectQuota) Validate() error {
	if q.Version != 1 || (q.Mode != "live" && q.Mode != "test") || q.BasisDigest == "" || q.Allocated < 0 || q.Revision < 1 {
		return fmt.Errorf("invalid protected project quota")
	}
	for _, v := range []string{q.Product, q.PayerID} {
		if err := ValidateSharedProjectIdentifier(v); err != nil {
			return err
		}
	}
	return nil
}
func NewProtectedProjectQuotaRecord(mode, product, payer string) (record.Record, *ProtectedProjectQuota) {
	q := new(ProtectedProjectQuota)
	key := record.NewKeyWithParentAndID(sharedProjectExtensionKey(payer), ProtectedProjectQuotaCollection, privatePlanID(mode, product))
	return record.NewRecordWithData(key, q), q
}

// ProjectAdmission is immutable attribution for future write/AI enforcement.
// It does not grant membership, included guest AI, or local execution authority.
type ProjectAdmission struct {
	OwnerContact                                                                  ProjectOwnerContactProof `json:"ownerContact,omitempty" firestore:"ownerContact,omitempty"`
	Version                                                                       int                      `firestore:"v"`
	Mode, Product, PayerID, ActorID, SpaceID, ProjectID, CommandID, RequestDigest string
	LimitsVersion, SubscriptionID                                                 string
	ProfileVersion, QuotaBasisDigest                                              string
	ProtectedProjectsLimit, ProtectedUsersLimit, QuotaRevision                    int64
	OwnerGeneration                                                               int64
	CreatedAt                                                                     time.Time
	// Version 2 is reserved for a proved Business Space service. Its explicit
	// unlimited grants cannot be inferred from absent finite Pro limits.
	ServiceID, PlanID, PaidServiceProofID string
	OwnerRevision                         int64
	UnlimitedProjects, UnlimitedContacts  bool
}

func (a ProjectAdmission) Validate() error {
	if a.OwnerContact.Present() {
		if err := a.OwnerContact.Validate(); err != nil {
			return err
		}
	}
	if (a.Mode != "live" && a.Mode != "test") || a.ActorID == "" || a.RequestDigest == "" || a.ProfileVersion == "" || a.SubscriptionID == "" || a.OwnerGeneration < 1 || a.CreatedAt.IsZero() || a.CreatedAt.Location() != time.UTC {
		return fmt.Errorf("invalid project admission")
	}
	for _, v := range []string{a.Product, a.PayerID, a.SpaceID, a.ProjectID, a.CommandID} {
		if err := ValidateSharedProjectIdentifier(v); err != nil {
			return err
		}
	}
	switch a.Version {
	case 1:
		if a.LimitsVersion == "" || a.QuotaBasisDigest == "" || a.QuotaRevision < 1 || a.ProtectedProjectsLimit < 1 || a.ProtectedUsersLimit < 1 || a.ServiceID != "" || a.PlanID != "" || a.PaidServiceProofID != "" || a.OwnerRevision != 0 || a.UnlimitedProjects || a.UnlimitedContacts {
			return fmt.Errorf("invalid finite project admission")
		}
	case 2:
		if a.Product != "datatug-business-usage" || a.PayerID != a.SpaceID || a.ServiceID != "datatug" || a.LimitsVersion == "" ||
			(a.PlanID != "datatug-business-usage-monthly" && a.PlanID != "datatug-business-usage-annual") ||
			a.PaidServiceProofID == "" || a.OwnerRevision < 1 || !a.UnlimitedProjects || !a.UnlimitedContacts ||
			a.QuotaBasisDigest != "" || a.QuotaRevision != 0 || a.ProtectedProjectsLimit != 0 || a.ProtectedUsersLimit != 0 || !a.OwnerContact.Present() ||
			string(a.OwnerContact.Contact.SpaceID) != a.SpaceID ||
			ValidateSharedProjectIdentifier(a.PaidServiceProofID) != nil {
			return fmt.Errorf("invalid Business project admission")
		}
	default:
		return fmt.Errorf("unknown project admission version")
	}
	return nil
}
func NewProjectAdmissionRecord(space, project string) (record.Record, *ProjectAdmission) {
	a := new(ProjectAdmission)
	key := record.NewKeyWithParentAndID(sharedProjectExtensionKey(space), ProjectAdmissionCollection, project)
	return record.NewRecordWithData(key, a), a
}
