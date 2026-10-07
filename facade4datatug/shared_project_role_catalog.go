// Copyright 2026 Sneat.co
package facade4datatug

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ProjectRoleCatalog is server configuration. This list approves role IDs only;
// it grants no operations or authority. The host's current role/permission ports
// separately authorize the operation. No public request may supply the list.
type ProjectRoleCatalog struct {
	Version   string
	Roles     []string
	OwnerRole string
}

type projectRoleCatalog struct {
	version, owner string
	roles          map[string]struct{}
}

func snapshotProjectRoleCatalog(in ProjectRoleCatalog) (projectRoleCatalog, error) {
	out := projectRoleCatalog{version: in.Version, owner: in.OwnerRole, roles: make(map[string]struct{}, len(in.Roles))}
	if in.Version == "" || len(in.Roles) == 0 || !validProjectRoleID(in.OwnerRole) {
		return projectRoleCatalog{}, fmt.Errorf("%w: project role catalog is incomplete", ErrSharedProjectUnavailable)
	}
	for _, role := range in.Roles {
		if !validProjectRoleID(role) {
			return projectRoleCatalog{}, fmt.Errorf("%w: invalid project role ID", ErrSharedProjectUnavailable)
		}
		if _, exists := out.roles[role]; exists {
			return projectRoleCatalog{}, fmt.Errorf("%w: duplicate project role ID", ErrSharedProjectUnavailable)
		}
		out.roles[role] = struct{}{}
	}
	if _, exists := out.roles[out.owner]; !exists {
		return projectRoleCatalog{}, fmt.Errorf("%w: owner role is not approved", ErrSharedProjectUnavailable)
	}
	return out, nil
}
func validProjectRoleID(role string) bool {
	if role == "" || len(role) > 128 || role != strings.TrimSpace(role) || !utf8.ValidString(role) {
		return false
	}
	for _, r := range role {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (c projectRoleCatalog) allows(role string) bool { _, ok := c.roles[role]; return ok }
