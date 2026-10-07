package models4datatug

import (
	"reflect"
	"testing"
)

func TestProjectRoleCatalogV1HasStablePermissions(t *testing.T) {
	catalog := ProjectRoleCatalog()
	want := ProjectRoleCatalogData{
		Version: 1,
		Roles: []ProjectRoleDefinition{
			{ID: "viewer", Name: "Viewer", CanBrowse: true, CanRunQueries: true, CanEditLocally: true},
			{ID: "contributor", Name: "Contributor", CanBrowse: true, CanRunQueries: true, CanEditLocally: true, CanSaveShared: true},
			{ID: "owner", Name: "Owner", CanBrowse: true, CanRunQueries: true, CanEditLocally: true, CanSaveShared: true, CanManageAssignments: true},
		},
	}
	if !reflect.DeepEqual(catalog, want) {
		t.Fatalf("ProjectRoleCatalog() = %#v, want %#v", catalog, want)
	}

	catalog.Roles[0].ID = "mutated"
	if got := ProjectRoleCatalog().Roles[0].ID; got != ProjectRoleViewer {
		t.Fatalf("mutating returned catalog changed canonical role ID: %q", got)
	}
}
