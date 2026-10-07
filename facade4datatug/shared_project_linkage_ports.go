// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
)

// ProjectContactState comes from the current full contact record in the
// supplied transaction, never a cached active-contact brief. Missing/inactive
// contacts still count while linked. UserID is used for authentication only.
// Being a Space member is not a requirement for an assigned project contact.
type ProjectContactState struct {
	Ref            contract4linkage.RelationshipEntityRef
	Exists, Active bool
	UserID         string
}
type CurrentProjectContactPort interface {
	ReadCurrentProjectContact(context.Context, dal.ReadTransaction, contract4linkage.RelationshipEntityRef) (ProjectContactState, error)
}
type ProjectRoleEvidence struct {
	Contact ProjectContactState
	Roles   []string
}
type ProjectContactRoleChange struct {
	Contact          ProjectContactState
	Before, Proposed []string
}
type ProjectRoleMutationAuthorization struct {
	ActorID       string
	Project       models4datatug.SharedProjectRef
	ActorContacts []ProjectRoleEvidence
	Changes       []ProjectContactRoleChange
}

// Manager authority must independently bind ActorID to the authenticated
// context and authorize with CURRENT project roles and platform guards. Space
// role alone does not supply a project role. Multiple contacts for one UID are
// separate evidence: the port must not silently union their privileges.
type ProjectRoleManagerPort interface {
	AuthorizeProjectRoleMutation(context.Context, dal.ReadTransaction, ProjectRoleMutationAuthorization) error
}

// Target authority decides eligibility of adds/removals, including deliberate
// stale-contact cleanup; counting a stale link does not approve assigning one.
type ProjectContactRoleTargetPort interface {
	AuthorizeProjectContactRoleChange(context.Context, dal.ReadTransaction, ProjectRoleMutationAuthorization, ProjectContactRoleChange) error
}

// This port resolves an EXISTING contact in the original create transaction.
// It must independently bind the actor to the verified context, without writes,
// nested transactions, new contacts or automatic Space membership. The domain
// rechecks current status/UID and constructs only reciprocal linkage writes.
type ProjectOwnerContactPort interface {
	ResolveProjectOwnerContact(context.Context, dal.ReadTransaction, SharedProjectCreateBinding) (contract4linkage.RelationshipEntityRef, error)
}
