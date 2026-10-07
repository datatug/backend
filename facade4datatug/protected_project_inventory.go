// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
)

var ErrProtectedProjectInventory = errors.New("protected project inventory could not be proved complete")

// DALProtectedProjectInventory uses collection-group scans but accepts records
// only under the exact spaces/{id}/ext/datatug/projects and
// spaces/{id}/ext/datatug/projectAdmissions ancestry. Group scans also find
// orphan subcollections whose parent Space document was deleted. The scan is
// intentionally unbounded by Firestore query limits; a hard local ceiling
// fails closed rather than returning a truncated count.
type DALProtectedProjectInventory struct {
	query dal.QueryExecutor
}

const maxProtectedProjectInventoryRows = 100_000
const sharedProjectCollection = "projects"

func NewDALProtectedProjectInventory(query dal.QueryExecutor) (*DALProtectedProjectInventory, error) {
	if sharedProjectPortAbsent(query) {
		return nil, ErrProtectedProjectInventory
	}
	return &DALProtectedProjectInventory{query: query}, nil
}

type protectedProjectInventoryEntry struct {
	SpaceID   string                          `json:"spaceId"`
	ProjectID string                          `json:"projectId"`
	Admission models4datatug.ProjectAdmission `json:"admission"`
}

func (i *DALProtectedProjectInventory) CompleteProtectedProjectBasis(ctx context.Context, mode, product, payer string) (InitialProtectedProjectBasis, error) {
	var empty InitialProtectedProjectBasis
	if i == nil || sharedProjectPortAbsent(i.query) || ctx == nil || (mode != "live" && mode != "test") || models4datatug.ValidateSharedProjectIdentifier(product) != nil || models4datatug.ValidateSharedProjectIdentifier(payer) != nil {
		return empty, ErrProtectedProjectInventory
	}

	spaces, err := i.readSpaces(ctx)
	if err != nil {
		return empty, err
	}
	spaceIDs := make(map[string]struct{}, len(spaces))
	for _, space := range spaces {
		id, ok := space.Key().ID.(string)
		if !ok || models4datatug.ValidateSharedProjectIdentifier(id) != nil {
			return empty, ErrProtectedProjectInventory
		}
		spaceIDs[id] = struct{}{}
	}
	projects, err := i.readAllProjectRecords(ctx)
	if err != nil {
		return empty, err
	}
	if len(projects) > maxProtectedProjectInventoryRows {
		return empty, ErrProtectedProjectInventory
	}
	admissions, err := i.readAllAdmissionRecords(ctx)
	if err != nil {
		return empty, err
	}
	if len(admissions) > maxProtectedProjectInventoryRows-len(projects) {
		return empty, ErrProtectedProjectInventory
	}
	projectData := make(map[string]*models4datatug.Project, len(projects))
	for _, projectRecord := range projects {
		spaceID, projectID, owned, exactPath := sharedProjectInventoryPath(projectRecord.Key(), sharedProjectCollection)
		if !owned {
			continue
		}
		if !exactPath {
			return empty, ErrProtectedProjectInventory
		}
		path := spaceID + "/" + projectID
		if _, duplicate := projectData[path]; duplicate {
			return empty, ErrProtectedProjectInventory
		}
		project, ok := projectRecord.Data().(*models4datatug.Project)
		if !ok || project == nil {
			return empty, ErrProtectedProjectInventory
		}
		if project.Access == models4datatug.AccessProtected {
			if _, found := spaceIDs[spaceID]; !found {
				return empty, ErrProtectedProjectInventory
			}
		}
		projectData[path] = project
	}
	entries := make([]protectedProjectInventoryEntry, 0)
	var allocated int64
	for _, admissionRecord := range admissions {
		spaceID, projectID, owned, exactPath := sharedProjectInventoryPath(admissionRecord.Key(), models4datatug.ProjectAdmissionCollection)
		if !owned {
			continue
		}
		if !exactPath {
			return empty, ErrProtectedProjectInventory
		}
		if _, found := spaceIDs[spaceID]; !found {
			return empty, ErrProtectedProjectInventory
		}
		path := spaceID + "/" + projectID
		admission, ok := admissionRecord.Data().(*models4datatug.ProjectAdmission)
		if !ok || admission == nil || admission.Validate() != nil || admission.SpaceID != spaceID || admission.ProjectID != projectID {
			return empty, ErrProtectedProjectInventory
		}
		project, found := projectData[path]
		if !found || project.Access != models4datatug.AccessProtected {
			return empty, ErrProtectedProjectInventory
		}
		delete(projectData, path)
		entries = append(entries, protectedProjectInventoryEntry{
			SpaceID: spaceID, ProjectID: projectID, Admission: *admission,
		})
		if admission.Mode == mode && admission.Product == product && admission.PayerID == payer {
			if allocated == int64(^uint64(0)>>1) {
				return empty, ErrProtectedProjectInventory
			}
			allocated++
		}
	}
	for path, project := range projectData {
		if project.Access == models4datatug.AccessProtected {
			return empty, fmt.Errorf("%w: protected project %s has no admission", ErrProtectedProjectInventory, path)
		}
	}

	sort.Slice(entries, func(a, b int) bool {
		if entries[a].SpaceID != entries[b].SpaceID {
			return entries[a].SpaceID < entries[b].SpaceID
		}
		return entries[a].ProjectID < entries[b].ProjectID
	})
	encoded, err := json.Marshal(struct {
		Version string                           `json:"version"`
		Mode    string                           `json:"mode"`
		Product string                           `json:"product"`
		Payer   string                           `json:"payer"`
		Entries []protectedProjectInventoryEntry `json:"entries"`
	}{Version: "datatug-protected-project-inventory/1", Mode: mode, Product: product, Payer: payer, Entries: entries})
	if err != nil {
		return empty, fmt.Errorf("%w: encode inventory digest", ErrProtectedProjectInventory)
	}
	digest := sha256.Sum256(encoded)
	return InitialProtectedProjectBasis{Digest: hex.EncodeToString(digest[:]), Allocated: allocated}, nil
}

func (i *DALProtectedProjectInventory) readAllProjectRecords(ctx context.Context) ([]record.Record, error) {
	query := dal.From(dal.NewCollectionGroupRef(sharedProjectCollection, "")).NewQuery().SelectIntoRecord(func() record.Record {
		rec, _ := models4datatug.NewSharedProjectRecord("inventory-space", "inventory-project")
		return rec
	})
	return executeCompleteInventoryQuery(ctx, i.query, query)
}

func (i *DALProtectedProjectInventory) readSpaces(ctx context.Context) ([]record.Record, error) {
	query := dal.From(dal.NewRootCollectionRef("spaces", "")).NewQuery().SelectIntoRecord(func() record.Record {
		return record.NewRecordWithIncompleteKey("spaces", reflect.String, new(struct{}))
	})
	return executeCompleteInventoryQuery(ctx, i.query, query)
}

func (i *DALProtectedProjectInventory) readAllAdmissionRecords(ctx context.Context) ([]record.Record, error) {
	query := dal.From(dal.NewCollectionGroupRef(models4datatug.ProjectAdmissionCollection, "")).NewQuery().SelectIntoRecord(func() record.Record {
		rec, _ := models4datatug.NewProjectAdmissionRecord("inventory-space", "inventory-project")
		return rec
	})
	return executeCompleteInventoryQuery(ctx, i.query, query)
}

func sharedProjectInventoryPath(key *record.Key, collection string) (spaceID, projectID string, owned, exact bool) {
	if key == nil || key.Collection() != collection {
		return "", "", false, false
	}
	// A collection-group query can also return similarly named collections
	// outside DataTug. Skip those, but once the ancestry claims the DataTug
	// extension, malformed or nested paths must fail the completeness proof.
	owned = false
	for ancestor := key.Parent(); ancestor != nil; ancestor = ancestor.Parent() {
		if ancestor.Collection() == "ext" && ancestor.ID == "datatug" {
			owned = true
			break
		}
	}
	if !owned {
		return "", "", false, false
	}
	projectIDValue, projectIDOK := key.ID.(string)
	extension := key.Parent()
	if !projectIDOK || extension == nil || extension.Collection() != "ext" || extension.ID != "datatug" {
		return "", "", true, false
	}
	space := extension.Parent()
	if space == nil || space.Collection() != "spaces" || space.Parent() != nil {
		return "", "", true, false
	}
	spaceIDValue, spaceIDOK := space.ID.(string)
	if !spaceIDOK || models4datatug.ValidateSharedProjectIdentifier(spaceIDValue) != nil || models4datatug.ValidateSharedProjectIdentifier(projectIDValue) != nil {
		return "", "", true, false
	}
	return spaceIDValue, projectIDValue, true, true
}

func executeCompleteInventoryQuery(ctx context.Context, query dal.QueryExecutor, q dal.Query) (rows []record.Record, resultErr error) {
	reader, err := query.ExecuteQueryToRecordsReader(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("%w: open inventory query", ErrProtectedProjectInventory)
	}
	defer func() {
		if err := reader.Close(); err != nil && resultErr == nil {
			rows = nil
			resultErr = fmt.Errorf("%w: close inventory query", ErrProtectedProjectInventory)
		}
	}()
	rows = make([]record.Record, 0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrProtectedProjectInventory, err)
		}
		row, err := reader.Next()
		if err != nil {
			if errors.Is(err, dal.ErrNoMoreRecords) || errors.Is(err, io.EOF) {
				return rows, nil
			}
			return nil, fmt.Errorf("%w: read inventory query", ErrProtectedProjectInventory)
		}
		if sharedProjectPortAbsent(row) || sharedProjectPortAbsent(row.Key()) {
			return nil, ErrProtectedProjectInventory
		}
		if len(rows) >= maxProtectedProjectInventoryRows {
			return nil, ErrProtectedProjectInventory
		}
		rows = append(rows, row)
	}
}
