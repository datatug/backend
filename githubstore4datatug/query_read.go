package githubstore4datatug

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

type QueryReadResult struct {
	Query                 json.RawMessage `json:"query"`
	Revision              string          `json:"revision"`
	BranchHead            string          `json:"branchHead"`
	SaveSupported         bool            `json:"saveSupported"`
	UnsupportedSaveReason string          `json:"unsupportedSaveReason,omitempty"`
}

// ReadQueryRevision retains unknown and legacy metadata in the read response.
// Only a current narrow Core pair receives a writable Core revision. Rich
// definitions are readable but cannot be round-tripped through narrow saves.
func (s *ProjectSnapshot) ReadQueryRevision(ctx context.Context, folderPath, id string) (out *QueryReadResult, resultErr error) {
	pair, kind, err := s.QueryFiles(ctx, folderPath, id)
	if err != nil {
		return nil, err
	}
	var definition, body []byte
	var definitionPath string
	for name, content := range pair {
		if strings.HasSuffix(name, ".json") {
			definitionPath = name
			definition = content
		} else {
			body = content
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(definition, &fields); err != nil || fields == nil {
		return nil, ErrInvalidProjectSnapshot
	}
	if folderPath == "" {
		folderPath = "~"
	}
	for key, value := range map[string]any{"folderPath": folderPath, "id": id, "type": kind, "text": string(body)} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		fields[key] = encoded
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	result := &QueryReadResult{Query: encoded, BranchHead: s.Head, SaveSupported: false, UnsupportedSaveReason: "This definition contains metadata outside the supported save contract."}
	checksum := sha256.Sum256(append(append([]byte(nil), definition...), body...))
	result.Revision = "read-" + hex.EncodeToString(checksum[:])
	if !strings.HasSuffix(definitionPath, ".query.json") || (kind != "SQL" && kind != "DTQL") {
		return result, nil
	}
	for key := range fields {
		switch key {
		case "folderPath", "id", "title", "type", "text", "draft", "federation":
		default:
			return result, nil
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(definition))
	decoder.DisallowUnknownFields()
	var query datatug.QueryDef
	if err := decoder.Decode(&query); err != nil {
		return result, nil
	}
	tmp, err := os.MkdirTemp("", "datatug-query-read-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(tmp); cleanupErr != nil {
			out = nil
			resultErr = errors.Join(resultErr, fmt.Errorf("remove query read scratch: %w", cleanupErr))
		}
	}()
	root := filepath.Join(tmp, "project")
	for name, content := range pair {
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filename, content, 0o600); err != nil {
			return nil, err
		}
	}
	store, ok := filestore.NewProjectStore("read", root).(datatug.RevisionedQueriesStore)
	if !ok {
		return nil, errors.New("core query revision reader unavailable")
	}
	location := id
	if folderPath != "~" {
		location = path.Join(folderPath, id)
	}
	stored, err := store.LoadQueryRevision(ctx, location)
	if err != nil {
		return result, nil
	}
	result.Revision = string(stored.Revision)
	result.SaveSupported = true
	result.UnsupportedSaveReason = ""
	return result, nil
}
