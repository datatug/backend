// Copyright 2026 Sneat.co
package models4datatug

import (
	"fmt"
	"github.com/dal-go/record"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
)

// SharedLinkedProject reuses Linkage's persisted graph at the existing shared
// project key. Legacy private Project/UserIDs remain unchanged; shared access
// never derives from that legacy list.
type SharedLinkedProject struct {
	Project
	contract4linkage.WithRelatedAndIDs
}

func NewSharedLinkedProjectRecord(space, project string) (record.Record, *SharedLinkedProject) {
	old, _ := NewSharedProjectRecord(space, project)
	value := new(SharedLinkedProject)
	return record.NewRecordWithData(old.Key(), value), value
}

// ProjectOwnerContactProof is immutable paid-create provenance, not another
// membership list. The actual assignment lives only in the Related graph.
// Ownerless historical admissions require a separately reviewed migration.
type ProjectOwnerContactProof struct {
	Version        int                                    `json:"v,omitempty" firestore:"v,omitempty"`
	Contact        contract4linkage.RelationshipEntityRef `json:"contact" firestore:"contact"`
	Role           string                                 `json:"role,omitempty" firestore:"role,omitempty"`
	CatalogVersion string                                 `json:"catalogVersion,omitempty" firestore:"catalogVersion,omitempty"`
}

func (p ProjectOwnerContactProof) Present() bool { return p != (ProjectOwnerContactProof{}) }
func (p ProjectOwnerContactProof) Validate() error {
	if p.Version != 1 || p.Role == "" || p.CatalogVersion == "" || ValidateProjectContactRef(p.Contact) != nil {
		return fmt.Errorf("invalid project owner contact proof")
	}
	return nil
}
func ValidateProjectContactRef(ref contract4linkage.RelationshipEntityRef) error {
	if ValidateSharedProjectIdentifier(string(ref.SpaceID)) != nil || ref.ItemRef.ExtID != "contactus" || ref.ItemRef.Collection != "contacts" || ref.ItemRef.SubPath != "" || ValidateSharedProjectIdentifier(ref.ItemRef.ItemID) != nil || ref.ItemRef.Validate() != nil {
		return fmt.Errorf("invalid resolved project contact reference")
	}
	return nil
}

// NewProjectContactLinkageRecord projects only the existing Contactus linkage
// fields. Mutation callers update those fields only, never contact identity or
// status. Contact authorization is supplied by the current-contact port.
func NewProjectContactLinkageRecord(ref contract4linkage.RelationshipEntityRef) (record.Record, *contract4linkage.WithRelatedAndIDs) {
	graph := new(contract4linkage.WithRelatedAndIDs)
	ext := record.NewKeyWithParentAndID(record.NewKeyWithID("spaces", string(ref.SpaceID)), "ext", "contactus")
	key := record.NewKeyWithParentAndID(ext, "contacts", ref.ItemRef.ItemID)
	return record.NewRecordWithData(key, graph), graph
}
