// Copyright 2026 Sneat.co
package facade4datatug

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/coretypes"
)

type projectContactRoles map[contract4linkage.RelationshipEntityRef][]string

func cloneProjectLinkage(graph contract4linkage.WithRelatedAndIDs) (contract4linkage.WithRelatedAndIDs, error) {
	var copy contract4linkage.WithRelatedAndIDs
	data, err := json.Marshal(graph)
	if err == nil {
		err = json.Unmarshal(data, &copy)
	}
	return copy, err
}
func contactRefFromGraphKey(space coretypes.SpaceID, key string) (contract4linkage.RelationshipEntityRef, error) {
	id, suffix, explicit := strings.Cut(key, "@")
	if explicit {
		space = coretypes.SpaceID(suffix)
	}
	ref := contract4linkage.RelationshipEntityRef{SpaceID: space, ItemRef: coretypes.ItemRef{ExtID: "contactus", Collection: "contacts", ItemID: id}}
	return ref, models4datatug.ValidateProjectContactRef(ref)
}

// Count from the project graph, never contact status, UID or RelatedIDs. Aliased
// duplicate graph keys are malformed rather than a reason to merge privileges.
func readProjectContactRoles(space coretypes.SpaceID, graph contract4linkage.WithRelatedAndIDs, catalog projectRoleCatalog) (projectContactRoles, error) {
	if err := graph.Validate(); err != nil {
		return nil, ErrSharedProjectConflict
	}
	roles := make(projectContactRoles)
	for key, item := range graph.Related["contactus"]["contacts"] {
		ref, err := contactRefFromGraphKey(space, key)
		if err != nil || item == nil || len(item.SubPaths) != 0 || len(item.RolesToItem) != 0 {
			return nil, ErrSharedProjectConflict
		}
		if _, exists := roles[ref]; exists {
			return nil, ErrSharedProjectConflict
		}
		names := make([]string, 0, len(item.RolesOfItem))
		for role := range item.RolesOfItem {
			if !catalog.allows(role) {
				return nil, ErrSharedProjectUnauthorized
			}
			names = append(names, role)
		}
		sort.Strings(names)
		// Retain empty keys while detecting aliases, but they are not assignments.
		roles[ref] = names
	}
	return roles, nil
}
func assignedProjectContacts(roles projectContactRoles) int64 {
	var count int64
	for _, names := range roles {
		if len(names) > 0 {
			count++
		}
	}
	return count
}
func graphItem(graph contract4linkage.WithRelatedAndIDs, space coretypes.SpaceID, ref contract4linkage.RelationshipEntityRef) (*contract4linkage.RelatedItem, error) {
	var found *contract4linkage.RelatedItem
	for key, item := range graph.Related[string(ref.ItemRef.ExtID)][ref.ItemRef.Collection] {
		id, suffix, explicit := strings.Cut(key, "@")
		targetSpace := space
		if explicit {
			targetSpace = coretypes.SpaceID(suffix)
		}
		if targetSpace == ref.SpaceID && id == ref.ItemRef.ItemID {
			if found != nil || item == nil || len(item.SubPaths) != 0 {
				return nil, ErrSharedProjectConflict
			}
			found = item
		}
	}
	return found, nil
}
func rolesOf(item *contract4linkage.RelatedItem, ofItem bool) []string {
	if item == nil {
		return nil
	}
	values := item.RolesToItem
	if ofItem {
		values = item.RolesOfItem
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func withoutProjectContacts(graph contract4linkage.WithRelatedAndIDs) (contract4linkage.RelatedModules, error) {
	copy, err := cloneProjectLinkage(graph)
	if err != nil {
		return nil, err
	}
	if module := copy.Related["contactus"]; module != nil {
		delete(module, "contacts")
		if len(module) == 0 {
			delete(copy.Related, "contactus")
		}
	}
	return copy.Related, nil
}
func onlyProjectContactsChanged(before, after contract4linkage.WithRelatedAndIDs) bool {
	a, err := withoutProjectContacts(before)
	if err != nil {
		return false
	}
	b, err := withoutProjectContacts(after)
	return err == nil && reflect.DeepEqual(a, b)
}
