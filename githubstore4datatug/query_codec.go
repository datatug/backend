package githubstore4datatug

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

const maxQueryPreviewBytes = 4 << 20

var ErrInvalidQuerySnapshot = errors.New("invalid query snapshot")
var ErrUnsupportedExistingQuery = errors.New("existing query contains unsupported metadata")

type QueryFileChange struct {
	Path    string
	Content []byte
	Delete  bool
}

type QueryMutationPreview struct {
	Changes  []QueryFileChange
	Response dto.SaveQueryResponse
}

// PreviewQueryMutation runs the published Core v0.44.0 revisioned query store
// in a fresh private scratch directory. The returned changes are the exact
// metadata/body files to send together in one Git commit at an expected head.
// All temp data is removed on every result, including conflicts and faults.
// The snapshot contains only the target query pair at one immutable commit.
func PreviewQueryMutation(ctx context.Context, request dto.SaveQueryRequest, snapshot map[string][]byte) (out *QueryMutationPreview, resultErr error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query := request.Query
	if query.FolderPath == "~" {
		query.FolderPath = ""
	}
	queryDir := "queries"
	if query.FolderPath != "" {
		queryDir = path.Join(queryDir, query.FolderPath)
	}
	prefix := query.ID + ".query."
	if len(snapshot) > 3 {
		return nil, ErrInvalidQuerySnapshot
	}
	var total int
	for name, content := range snapshot {
		if path.Dir(name) != queryDir || !strings.HasPrefix(path.Base(name), prefix) || path.Clean(name) != name || len(content) > maxQueryPreviewBytes {
			return nil, ErrInvalidQuerySnapshot
		}
		total += len(content)
	}
	if total > maxQueryPreviewBytes {
		return nil, ErrInvalidQuerySnapshot
	}
	if metadata, exists := snapshot[path.Join(queryDir, query.ID+".query.json")]; exists {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(metadata, &raw); err != nil || raw == nil {
			return nil, ErrUnsupportedExistingQuery
		}
		for field := range raw {
			switch field {
			case "id", "title", "type", "draft", "federation":
			default:
				return nil, fmt.Errorf("%w: %s", ErrUnsupportedExistingQuery, field)
			}
		}
		decoder := json.NewDecoder(bytes.NewReader(metadata))
		decoder.DisallowUnknownFields()
		var existing datatug.QueryDef
		if err := decoder.Decode(&existing); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnsupportedExistingQuery, err)
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			return nil, ErrUnsupportedExistingQuery
		}
	}
	tmp, err := os.MkdirTemp("", "datatug-query-preview-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(tmp); cleanupErr != nil {
			out = nil
			resultErr = errors.Join(resultErr, fmt.Errorf("remove query preview scratch: %w", cleanupErr))
		}
	}()
	root := filepath.Join(tmp, "project")
	if err := os.Mkdir(root, 0o700); err != nil {
		return nil, err
	}
	scratch, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := scratch.Close(); closeErr != nil {
			out = nil
			resultErr = errors.Join(resultErr, fmt.Errorf("close query preview scratch: %w", closeErr))
		}
	}()
	if err := scratch.MkdirAll(queryDir, 0o700); err != nil {
		return nil, err
	}
	for name, content := range snapshot {
		if err := scratch.WriteFile(name, content, 0o600); err != nil {
			return nil, err
		}
	}
	store, ok := filestore.NewProjectStore("preview", root).(datatug.RevisionedQueriesStore)
	if !ok {
		return nil, fmt.Errorf("core query store has no revisioned contract")
	}
	saved, err := store.PutQuery(ctx, &query, datatug.QueryWriteCondition{IfNoneMatch: request.IfNoneMatch, IfMatch: datatug.QueryRevision(request.IfMatch)})
	if err != nil {
		return nil, err
	}
	dir, err := scratch.Open(queryDir)
	if err != nil {
		return nil, err
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	result := &QueryMutationPreview{Response: dto.SaveQueryResponse{Query: saved.Query, Revision: string(saved.Revision)}}
	result.Response.Query.FolderPath = request.Query.FolderPath
	seen := make(map[string]bool, len(snapshot))
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, ErrInvalidQuerySnapshot
		}
		name := path.Join(queryDir, entry.Name())
		content, err := scratch.ReadFile(name)
		if err != nil || len(content) > maxQueryPreviewBytes {
			return nil, ErrInvalidQuerySnapshot
		}
		seen[name] = true
		if previous, exists := snapshot[name]; !exists || !bytes.Equal(previous, content) {
			result.Changes = append(result.Changes, QueryFileChange{Path: name, Content: content})
		}
	}
	for name := range snapshot {
		if !seen[name] {
			result.Changes = append(result.Changes, QueryFileChange{Path: name, Delete: true})
		}
	}
	if len(result.Changes) == 0 {
		return nil, ErrInvalidQuerySnapshot
	}
	sort.Slice(result.Changes, func(i, j int) bool { return result.Changes[i].Path < result.Changes[j].Path })
	return result, nil
}
