// Copyright 2026 Sneat.co
package facade4datatug

import (
	"errors"
	"testing"
)

func TestProjectRoleCatalogIsCopiedAndDoesNotInferPermission(t *testing.T) {
	// Opaque fixture IDs deliberately do not invent viewer/contributor rights.
	input := ProjectRoleCatalog{Version: "test-reviewed-1", Roles: []string{"role-a", "role-b"}, OwnerRole: "role-a"}
	catalog, err := snapshotProjectRoleCatalog(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Roles[0] = "unreviewed"
	input.OwnerRole = "role-b"
	if !catalog.allows("role-a") || catalog.allows("unreviewed") || catalog.owner != "role-a" || catalog.version != "test-reviewed-1" {
		t.Fatal("mutable server input changed accepted catalog")
	}
	// Unknown request roles cannot select a different permission policy.
	if catalog.allows("arbitrary-request-role") {
		t.Fatal("unknown role approved")
	}
}
func TestProjectRoleCatalogRefusesIncompleteOrAmbiguousConfig(t *testing.T) {
	for _, config := range []ProjectRoleCatalog{
		{},
		{Version: "v", Roles: []string{"role-a"}},
		{Roles: []string{"role-a"}, OwnerRole: "role-a"},
		{Version: "v", Roles: []string{"role-a"}, OwnerRole: "role-b"},
		{Version: "v", Roles: []string{"role-a", "role-a"}, OwnerRole: "role-a"},
		{Version: "v", Roles: []string{"role-a", " role-b"}, OwnerRole: "role-a"},
		{Version: "v", Roles: []string{"role-a", "role\nb"}, OwnerRole: "role-a"},
	} {
		if _, err := snapshotProjectRoleCatalog(config); !errors.Is(err, ErrSharedProjectUnavailable) {
			t.Fatalf("accepted %+v: %v", config, err)
		}
	}
}
