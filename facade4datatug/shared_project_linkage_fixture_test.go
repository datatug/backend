// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/coretypes"
	"testing"
)

type projectContactFixture struct {
	contract4linkage.WithRelatedAndIDs
	UserID string `json:"userID,omitempty" firestore:"userID,omitempty"`
	Active bool   `json:"active" firestore:"active"`
	Status string `json:"status,omitempty" firestore:"status,omitempty"`
}
type projectContactFixturePort struct{}

func (projectContactFixturePort) ReadCurrentProjectContact(ctx context.Context, tx dal.ReadTransaction, ref contract4linkage.RelationshipEntityRef) (ProjectContactState, error) {
	r, _ := models4datatug.NewProjectContactLinkageRecord(ref)
	value := new(projectContactFixture)
	err := tx.Get(ctx, record.NewRecordWithData(r.Key(), value))
	if record.IsNotFound(err) {
		return ProjectContactState{Ref: ref}, nil
	}
	return ProjectContactState{Ref: ref, Exists: err == nil, Active: value.Active && value.Status != "deleted", UserID: value.UserID}, err
}
func (projectContactFixturePort) ResolveProjectOwnerContact(_ context.Context, _ dal.ReadTransaction, b SharedProjectCreateBinding) (contract4linkage.RelationshipEntityRef, error) {
	return contactFixtureRef(b.SpaceID, "owner-contact"), nil
}
func contactFixtureRef(space, id string) contract4linkage.RelationshipEntityRef {
	return contract4linkage.RelationshipEntityRef{SpaceID: coretypes.SpaceID(space), ItemRef: coretypes.ItemRef{ExtID: "contactus", Collection: "contacts", ItemID: id}}
}
func projectFixtureRef(space, id string) contract4linkage.RelationshipEntityRef {
	return contract4linkage.RelationshipEntityRef{SpaceID: coretypes.SpaceID(space), ItemRef: coretypes.ItemRef{ExtID: "datatug", Collection: "projects", ItemID: id}}
}
func emptyProjectLinkage() contract4linkage.WithRelatedAndIDs {
	return contract4linkage.WithRelatedAndIDs{WithRelatedIDs: contract4linkage.WithRelatedIDs{RelatedIDs: []string{contract4linkage.NoRelatedID}}}
}
func paidFixtureOwnerLinks() *PaidProjectOwnerLinksOptions {
	return &PaidProjectOwnerLinksOptions{Roles: ProjectRoleCatalog{Version: "roles-1", Roles: []string{"role-a", "role-b"}, OwnerRole: "role-a"}, Owner: projectContactFixturePort{}, Contacts: projectContactFixturePort{}}
}
func seedProjectContact(t *testing.T, db dal.DB, ref contract4linkage.RelationshipEntityRef, uid string, active bool) {
	t.Helper()
	r, _ := models4datatug.NewProjectContactLinkageRecord(ref)
	value := &projectContactFixture{WithRelatedAndIDs: emptyProjectLinkage(), UserID: uid, Active: active}
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Insert(ctx, record.NewRecordWithData(r.Key(), value))
	}); err != nil {
		t.Fatal(err)
	}
}
func seedPaidOwnerContact(t *testing.T, db dal.DB, space string) {
	t.Helper()
	seedProjectContact(t, db, contactFixtureRef(space, "owner-contact"), "actor", true)
}
