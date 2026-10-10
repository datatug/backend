// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"slices"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/coretypes"
)

type PaidProjectOwnerLinksOptions struct {
	Roles    ProjectRoleCatalog
	Owner    ProjectOwnerContactPort
	Contacts CurrentProjectContactPort
}
type sharedProjectOwnerLinks struct {
	catalog  projectRoleCatalog
	owner    ProjectOwnerContactPort
	contacts CurrentProjectContactPort
}

func snapshotProjectOwnerLinks(in *PaidProjectOwnerLinksOptions) (*sharedProjectOwnerLinks, error) {
	if in == nil || sharedProjectPortAbsent(in.Owner) || sharedProjectPortAbsent(in.Contacts) {
		return nil, ErrSharedProjectUnavailable
	}
	catalog, err := snapshotProjectRoleCatalog(in.Roles)
	if err != nil {
		return nil, err
	}
	return &sharedProjectOwnerLinks{catalog: catalog, owner: in.Owner, contacts: in.Contacts}, nil
}

type projectOwnerLinkPlan struct {
	proof        models4datatug.ProjectOwnerContactProof
	graph        contract4linkage.WithRelatedAndIDs
	contact      record.Record
	contactGraph contract4linkage.WithRelatedAndIDs
}

func (s *SharedProjectService) readCurrentProjectOwnerContact(ctx context.Context, tx dal.ReadTransaction, b SharedProjectCreateBinding) (contract4linkage.RelationshipEntityRef, record.Record, *contract4linkage.WithRelatedAndIDs, error) {
	var zero contract4linkage.RelationshipEntityRef
	if s.ownerLinks == nil {
		return zero, nil, nil, ErrSharedProjectUnavailable
	}
	read := sharedProjectReadTransaction{tx}
	ref, err := s.ownerLinks.owner.ResolveProjectOwnerContact(ctx, read, b)
	if err != nil {
		return zero, nil, nil, err
	}
	// Current generic Linkage rejects foreign related writes. Do not bypass that
	// guard through create; standalone cross-Space sharing needs its own proof.
	if models4datatug.ValidateProjectContactRef(ref) != nil || string(ref.SpaceID) != b.SpaceID {
		return zero, nil, nil, ErrSharedProjectUnauthorized
	}
	state, err := s.ownerLinks.contacts.ReadCurrentProjectContact(ctx, read, ref)
	if err != nil {
		return zero, nil, nil, err
	}
	if state.Ref != ref || !state.Exists || !state.Active || state.UserID != b.ActorID {
		return zero, nil, nil, ErrSharedProjectUnauthorized
	}
	cr, current := models4datatug.NewProjectContactLinkageRecord(ref)
	if err := tx.Get(ctx, cr); err != nil {
		return zero, nil, nil, err
	}
	if current.Validate() != nil {
		return zero, nil, nil, ErrSharedProjectConflict
	}
	return ref, cr, current, nil
}

func (s *SharedProjectService) prepareProjectOwnerLink(ctx context.Context, tx dal.ReadTransaction, b SharedProjectCreateBinding, project string, at time.Time) (*projectOwnerLinkPlan, error) {
	ref, cr, current, err := s.readCurrentProjectOwnerContact(ctx, tx, b)
	if err != nil {
		return nil, err
	}
	next, err := cloneProjectLinkage(*current)
	if err != nil {
		return nil, err
	}
	graph := contract4linkage.WithRelatedAndIDs{WithRelatedIDs: contract4linkage.WithRelatedIDs{RelatedIDs: []string{contract4linkage.NoRelatedID}}}
	ownerRole := s.ownerLinks.catalog.owner
	if _, err := graph.ApplyDirectedRelationshipAndID(at, b.ActorID, coretypes.SpaceID(b.SpaceID), contract4linkage.RelationshipItemRolesCommand{ItemRef: ref.ItemRef, Add: &contract4linkage.RolesCommand{RolesOfItem: []string{ownerRole}}}); err != nil {
		return nil, err
	}
	projectRef := contract4linkage.ItemRef{ExtID: "datatug", Collection: "projects", ItemID: project}
	if _, err := next.ApplyDirectedRelationshipAndID(at, b.ActorID, ref.SpaceID, contract4linkage.RelationshipItemRolesCommand{ItemRef: projectRef, Add: &contract4linkage.RolesCommand{RolesToItem: []string{ownerRole}}}); err != nil {
		return nil, err
	}
	return &projectOwnerLinkPlan{proof: models4datatug.ProjectOwnerContactProof{Version: 1, Contact: ref, Role: ownerRole, CatalogVersion: s.ownerLinks.catalog.version}, graph: graph, contact: cr, contactGraph: next}, nil
}
func (s *SharedProjectService) verifyProjectOwnerReplay(ctx context.Context, tx dal.ReadTransaction, b SharedProjectCreateBinding, projectID string, owner models4datatug.ProjectOwnerContactProof) error {
	if s.ownerLinks == nil || owner.Validate() != nil {
		return ErrSharedProjectConflict
	}
	ref := contract4linkage.RelationshipEntityRef{SpaceID: coretypes.SpaceID(b.SpaceID), ItemRef: contract4linkage.ItemRef{ExtID: "datatug", Collection: "projects", ItemID: projectID}}
	r, project := models4datatug.NewSharedLinkedProjectRecord(b.SpaceID, projectID)
	if err := tx.Get(ctx, r); err != nil {
		return err
	}
	// An exact retry may still be completing an accepted GitHub reservation.
	// Other access paths keep the default READY-only rule.
	admission, err := readLinkedProjectAdmission(ctx, tx, ref, project, true)
	if err != nil {
		return err
	}
	if project.Access != models4datatug.AccessProtected || len(project.UserIDs) != 0 || admission.OwnerContact != owner {
		return ErrSharedProjectConflict
	}
	roles, err := readProjectContactRoles(ref.SpaceID, project.WithRelatedAndIDs, s.ownerLinks.catalog)
	if err != nil {
		return err
	}
	if !slices.Contains(roles[owner.Contact], owner.Role) {
		return ErrSharedProjectUnauthorized
	}
	read := sharedProjectReadTransaction{tx}
	state, err := s.ownerLinks.contacts.ReadCurrentProjectContact(ctx, read, owner.Contact)
	if err != nil {
		return err
	}
	if state.Ref != owner.Contact || !state.Exists || !state.Active || state.UserID != b.ActorID {
		return ErrSharedProjectUnauthorized
	}
	cr, contactGraph := models4datatug.NewProjectContactLinkageRecord(owner.Contact)
	if err := tx.Get(ctx, cr); err != nil {
		return err
	}
	edge, err := graphItem(*contactGraph, owner.Contact.SpaceID, ref)
	if err != nil || !slices.Contains(rolesOf(edge, false), owner.Role) {
		return ErrSharedProjectUnauthorized
	}
	return nil
}
func (p *projectOwnerLinkPlan) writeContact(ctx context.Context, tx dal.ReadwriteTransaction) error {
	// Derive these two fields ourselves. The read-only binding port cannot
	// choose update keys or mutate contact status, UID, membership or other data.
	return tx.Update(ctx, p.contact.Key(), []update.Update{update.ByFieldPath([]string{"related"}, p.contactGraph.Related), update.ByFieldPath([]string{"relatedIDs"}, p.contactGraph.RelatedIDs)})
}
