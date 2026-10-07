package models4datatug

// ProjectRoleID is a stable role assigned to a contact linked to a standalone
// DataTug project. These roles are separate from the containing Space roles.
type ProjectRoleID string

const (
	ProjectRoleViewer      ProjectRoleID = "viewer"
	ProjectRoleContributor ProjectRoleID = "contributor"
	ProjectRoleOwner       ProjectRoleID = "owner"

	ProjectRoleCatalogVersion uint16 = 1
)

// ProjectRoleDefinition is the product-owned permission description for a
// project contact role. Platform and paid-plan guards still apply to each
// action represented here.
type ProjectRoleDefinition struct {
	ID                   ProjectRoleID `json:"id"`
	Name                 string        `json:"name"`
	CanBrowse            bool          `json:"canBrowse"`
	CanRunQueries        bool          `json:"canRunQueries"`
	CanEditLocally       bool          `json:"canEditLocally"`
	CanSaveShared        bool          `json:"canSaveShared"`
	CanManageAssignments bool          `json:"canManageAssignments"`
}

type ProjectRoleCatalogData struct {
	Version uint16                  `json:"version"`
	Roles   []ProjectRoleDefinition `json:"roles"`
}

var projectRoleDefinitionsV1 = [...]ProjectRoleDefinition{
	{
		ID: ProjectRoleViewer, Name: "Viewer",
		CanBrowse: true, CanRunQueries: true, CanEditLocally: true,
	},
	{
		ID: ProjectRoleContributor, Name: "Contributor",
		CanBrowse: true, CanRunQueries: true, CanEditLocally: true, CanSaveShared: true,
	},
	{
		ID: ProjectRoleOwner, Name: "Owner",
		CanBrowse: true, CanRunQueries: true, CanEditLocally: true, CanSaveShared: true, CanManageAssignments: true,
	},
}

// ProjectRoleCatalog returns the versioned product role catalog. A fresh slice
// prevents callers from mutating the canonical definitions used by policy.
func ProjectRoleCatalog() ProjectRoleCatalogData {
	return ProjectRoleCatalogData{
		Version: ProjectRoleCatalogVersion,
		Roles:   append([]ProjectRoleDefinition(nil), projectRoleDefinitionsV1[:]...),
	}
}
