// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
)

var ErrProjectContactLimit = errors.New("protected project contact limit unavailable or exhausted")

type PaidProjectLinkageOptions struct {
	Paid     PaidSharedProjectOptions
	Roles    ProjectRoleCatalog
	Contacts CurrentProjectContactPort
	Manager  ProjectRoleManagerPort
	Targets  ProjectContactRoleTargetPort
	Now      func() time.Time
}

// PaidProjectLinkagePolicy is the DataTug implementation of Core's generic
// transaction policy. Host activation MUST declare datatug/projects protected
// before binding generic routes or creating records, then bind this policy for
// BOTH linkage directions. A UI role list never supplies authority.
type PaidProjectLinkagePolicy struct {
	paid     PaidSharedProjectOptions
	catalog  projectRoleCatalog
	contacts CurrentProjectContactPort
	manager  ProjectRoleManagerPort
	targets  ProjectContactRoleTargetPort
	now      func() time.Time
}

var _ contract4linkage.RelationshipMutationPolicy = (*PaidProjectLinkagePolicy)(nil)

func NewPaidProjectLinkagePolicy(o PaidProjectLinkageOptions) (*PaidProjectLinkagePolicy, error) {
	catalog, err := snapshotProjectRoleCatalog(o.Roles)
	if err != nil || o.Paid.validate() != nil || sharedProjectPortAbsent(o.Contacts) || sharedProjectPortAbsent(o.Manager) || sharedProjectPortAbsent(o.Targets) || o.Now == nil {
		return nil, ErrSharedProjectUnavailable
	}
	return &PaidProjectLinkagePolicy{paid: snapshotPaidSharedProjectOptions(o.Paid), catalog: catalog, contacts: o.Contacts, manager: o.Manager, targets: o.Targets, now: o.Now}, nil
}
func (p *PaidProjectLinkagePolicy) AuthorizeRelationshipMutation(ctx context.Context, tx dal.ReadTransaction, b contract4linkage.RelationshipMutationBatch) error {
	if p == nil || ctx == nil || sharedProjectPortAbsent(tx) || b.ActorUserID == "" || b.ObservedAt.IsZero() || len(b.Entities) == 0 {
		return ErrSharedProjectInvalid
	}
	tx = sharedProjectReadTransaction{tx}
	now := p.now().UTC()
	if now.IsZero() || now.Before(b.ObservedAt) {
		return ErrSharedProjectUnauthorized
	}
	entities := make(map[contract4linkage.RelationshipEntityRef]contract4linkage.RelationshipEntityMutation, len(b.Entities))
	for _, entity := range b.Entities {
		if !validProjectRelationshipRef(entity.Ref) {
			return ErrSharedProjectInvalid
		}
		if _, duplicate := entities[entity.Ref]; duplicate {
			return ErrSharedProjectConflict
		}
		entities[entity.Ref] = entity
	}
	if _, exists := entities[b.Source]; !exists {
		return ErrSharedProjectInvalid
	}
	for _, command := range b.Commands {
		if command.Validate() != nil {
			return ErrSharedProjectInvalid
		}
	}
	found := false
	// Every affected project is validated, including later projects in a contact-
	// origin batch. The Core planner writes nothing if any policy call refuses.
	for _, entity := range b.Entities {
		if !isProjectRelationshipRef(entity.Ref) {
			continue
		}
		found = true
		if err := p.authorizeProject(ctx, tx, b, entity, entities, now); err != nil {
			return err
		}
	}
	if !found {
		return ErrSharedProjectInvalid
	}
	return nil
}
func validProjectRelationshipRef(ref contract4linkage.RelationshipEntityRef) bool {
	return models4datatug.ValidateSharedProjectIdentifier(string(ref.SpaceID)) == nil && models4datatug.ValidateSharedProjectIdentifier(ref.ItemRef.ItemID) == nil && ref.ItemRef.SubPath == "" && ref.ItemRef.Validate() == nil
}
func isProjectRelationshipRef(ref contract4linkage.RelationshipEntityRef) bool {
	return ref.ItemRef.ExtID == "datatug" && ref.ItemRef.Collection == "projects"
}
func projectPublicRef(ref contract4linkage.RelationshipEntityRef) models4datatug.SharedProjectRef {
	return models4datatug.SharedProjectRef{StoreID: models4datatug.FirestoreStoreID, SpaceID: string(ref.SpaceID), ProjectID: ref.ItemRef.ItemID}
}
func (p *PaidProjectLinkagePolicy) authorizeProject(ctx context.Context, tx dal.ReadTransaction, b contract4linkage.RelationshipMutationBatch, e contract4linkage.RelationshipEntityMutation, entities map[contract4linkage.RelationshipEntityRef]contract4linkage.RelationshipEntityMutation, now time.Time) error {
	r, project := models4datatug.NewSharedLinkedProjectRecord(string(e.Ref.SpaceID), e.Ref.ItemRef.ItemID)
	if err := tx.Get(ctx, r); err != nil {
		return err
	}
	if project.Access != models4datatug.AccessProtected {
		// This paid protected-contact policy adds no cap or contact-role requirement
		// to public/owner-only private projects. Their existing platform ACL remains.
		if project.Access == models4datatug.AccessPrivate || project.Access == "public" {
			return nil
		}
		return ErrSharedProjectConflict
	}
	if len(project.UserIDs) != 0 || !reflect.DeepEqual(project.WithRelatedAndIDs, e.Before) || !onlyProjectContactsChanged(e.Before, e.Proposed) {
		return ErrSharedProjectConflict
	}
	before, err := readProjectContactRoles(e.Ref.SpaceID, e.Before, p.catalog)
	if err != nil {
		return err
	}
	after, err := readProjectContactRoles(e.Ref.SpaceID, e.Proposed, p.catalog)
	if err != nil {
		return err
	}
	admission, err := readLinkedProjectAdmission(ctx, tx, e.Ref, project)
	if err != nil {
		return err
	}
	access, err := readCurrentPaidProjectAccess(ctx, tx, p.paid, admission.ActorID, admission.PayerID, now)
	if err != nil {
		return err
	}
	if assignedProjectContacts(after) > access.contactLimit {
		return ErrProjectContactLimit
	}
	if !slices.Contains(before[admission.OwnerContact.Contact], admission.OwnerContact.Role) || !slices.Contains(after[admission.OwnerContact.Contact], admission.OwnerContact.Role) || !p.catalog.allows(admission.OwnerContact.Role) {
		return ErrSharedProjectUnauthorized
	}
	if b.Source != e.Ref && models4datatug.ValidateProjectContactRef(b.Source) != nil {
		return ErrSharedProjectInvalid
	}
	// A protected project may change contact-role relationships only. Every
	// changed contact must be in Core's staged batch, with a matching reciprocal.
	changes := make([]ProjectContactRoleChange, 0)
	actorContacts := make([]ProjectRoleEvidence, 0)
	refs := make(map[contract4linkage.RelationshipEntityRef]bool)
	for ref := range before {
		refs[ref] = true
	}
	for ref := range after {
		refs[ref] = true
	}
	orderedRefs := make([]contract4linkage.RelationshipEntityRef, 0, len(refs))
	for ref := range refs {
		orderedRefs = append(orderedRefs, ref)
	}
	sort.Slice(orderedRefs, func(i, j int) bool { return projectContactRefLess(orderedRefs[i], orderedRefs[j]) })
	for _, ref := range orderedRefs {
		state, err := p.contacts.ReadCurrentProjectContact(ctx, tx, ref)
		if err != nil {
			return err
		}
		if state.Ref != ref || (!state.Exists && (state.Active || state.UserID != "")) {
			return ErrSharedProjectConflict
		}
		if ref == admission.OwnerContact.Contact && (!state.Exists || !state.Active || state.UserID != admission.ActorID) {
			return ErrSharedProjectUnauthorized
		}
		if state.Exists && state.Active && state.UserID == b.ActorUserID && len(before[ref]) > 0 {
			actorContacts = append(actorContacts, ProjectRoleEvidence{Contact: state, Roles: slices.Clone(before[ref])})
		}
		// Read the persisted reverse graph even for unchanged assigned contacts.
		// A deleted-but-present contact can be explicitly cleaned up; a physically
		// missing/corrupt reciprocal record cannot silently reclaim an assignment.
		cr, currentGraph := models4datatug.NewProjectContactLinkageRecord(ref)
		graphErr := tx.Get(ctx, cr)
		if graphErr != nil && !(record.IsNotFound(graphErr) && len(before[ref]) == 0) {
			return graphErr
		}
		if graphErr == nil && currentGraph.Validate() != nil {
			return ErrSharedProjectConflict
		}
		if graphErr == nil {
			edge, err := graphItem(*currentGraph, ref.SpaceID, e.Ref)
			if err != nil || len(rolesOf(edge, true)) != 0 || !slices.Equal(rolesOf(edge, false), before[ref]) {
				return ErrSharedProjectConflict
			}
		}
		if !slices.Equal(before[ref], after[ref]) {
			counterpart, exists := entities[ref]
			if !exists {
				return ErrSharedProjectConflict
			}
			if graphErr != nil {
				return graphErr
			}
			if !reflect.DeepEqual(*currentGraph, counterpart.Before) {
				return ErrSharedProjectConflict
			}
			if err := verifyProjectContactReciprocal(e, ref, counterpart, before[ref], after[ref]); err != nil {
				return err
			}
			changes = append(changes, ProjectContactRoleChange{Contact: state, Before: slices.Clone(before[ref]), Proposed: slices.Clone(after[ref])})
		}
	}
	if len(actorContacts) == 0 {
		return ErrSharedProjectUnauthorized
	}
	sort.Slice(actorContacts, func(i, j int) bool {
		return projectContactRefLess(actorContacts[i].Contact.Ref, actorContacts[j].Contact.Ref)
	})
	request := ProjectRoleMutationAuthorization{ActorID: b.ActorUserID, Project: projectPublicRef(e.Ref), ActorContacts: actorContacts, Changes: changes}
	if err := p.manager.AuthorizeProjectRoleMutation(ctx, tx, cloneProjectRoleAuthorization(request)); err != nil {
		return err
	}
	for _, change := range changes {
		if err := p.targets.AuthorizeProjectContactRoleChange(ctx, tx, cloneProjectRoleAuthorization(request), cloneProjectRoleChange(change)); err != nil {
			return err
		}
	}
	return nil
}
func verifyProjectContactReciprocal(project contract4linkage.RelationshipEntityMutation, ref contract4linkage.RelationshipEntityRef, contact contract4linkage.RelationshipEntityMutation, before, after []string) error {
	if contact.Ref != ref {
		return ErrSharedProjectConflict
	}
	old, err := graphItem(contact.Before, ref.SpaceID, project.Ref)
	if err != nil {
		return err
	}
	next, err := graphItem(contact.Proposed, ref.SpaceID, project.Ref)
	if err != nil {
		return err
	}
	if len(rolesOf(old, true)) != 0 || len(rolesOf(next, true)) != 0 || !slices.Equal(rolesOf(old, false), before) || !slices.Equal(rolesOf(next, false), after) {
		return ErrSharedProjectConflict
	}
	return nil
}
func cloneProjectRoleChange(in ProjectContactRoleChange) ProjectContactRoleChange {
	in.Before = slices.Clone(in.Before)
	in.Proposed = slices.Clone(in.Proposed)
	return in
}
func cloneProjectRoleAuthorization(in ProjectRoleMutationAuthorization) ProjectRoleMutationAuthorization {
	in.ActorContacts = slices.Clone(in.ActorContacts)
	for i := range in.ActorContacts {
		in.ActorContacts[i].Roles = slices.Clone(in.ActorContacts[i].Roles)
	}
	in.Changes = slices.Clone(in.Changes)
	for i := range in.Changes {
		in.Changes[i] = cloneProjectRoleChange(in.Changes[i])
	}
	return in
}

func projectContactRefLess(a, b contract4linkage.RelationshipEntityRef) bool {
	if a.SpaceID != b.SpaceID {
		return a.SpaceID < b.SpaceID
	}
	return a.ItemRef.ID() < b.ItemRef.ID()
}
