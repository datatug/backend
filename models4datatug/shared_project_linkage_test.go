// Copyright 2026 Sneat.co
package models4datatug

import (
	"encoding/json"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/coretypes"
	"reflect"
	"testing"
)

func TestSharedLinkedProjectAndOwnerProofUseExistingKeysAndRoundTrip(t *testing.T) {
	old, _ := NewSharedProjectRecord("space", "same")
	linked, p := NewSharedLinkedProjectRecord("space", "same")
	if old.Key().String() != linked.Key().String() {
		t.Fatal("parallel project store")
	}
	ref := contract4linkage.RelationshipEntityRef{SpaceID: "space", ItemRef: coretypes.ItemRef{ExtID: "contactus", Collection: "contacts", ItemID: "contact"}}
	proof := ProjectOwnerContactProof{Version: 1, Contact: ref, Role: "configured-role", CatalogVersion: "catalog-1"}
	if proof.Validate() != nil || !proof.Present() || (ProjectOwnerContactProof{}).Present() {
		t.Fatal(proof)
	}
	p.Title = "Shared"
	p.Access = AccessProtected
	p.RelatedIDs = []string{"-"}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var out SharedLinkedProject
	if err := json.Unmarshal(encoded, &out); err != nil || !reflect.DeepEqual(*p, out) {
		t.Fatal(out, err)
	}
	cr, g := NewProjectContactLinkageRecord(ref)
	if cr.Key().String() != "spaces/space/ext/contactus/contacts/contact" || g == nil {
		t.Fatal(cr.Key())
	}
	for _, mutate := range []func(*ProjectOwnerContactProof){func(v *ProjectOwnerContactProof) { v.Version = 0 }, func(v *ProjectOwnerContactProof) { v.Role = "" }, func(v *ProjectOwnerContactProof) { v.CatalogVersion = "" }, func(v *ProjectOwnerContactProof) { v.Contact.SpaceID = "" }, func(v *ProjectOwnerContactProof) { v.Contact.ItemRef.Collection = "projects" }, func(v *ProjectOwnerContactProof) { v.Contact.ItemRef.ItemID = "contact@space" }, func(v *ProjectOwnerContactProof) { v.Contact.ItemRef.SubPath = "part" }} {
		v := proof
		mutate(&v)
		if v.Validate() == nil {
			t.Fatal("invalid owner proof", v)
		}
	}
}
