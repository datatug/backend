package githubstore4datatug

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"

	"github.com/datatug/backend/githubauth4datatug"
	"github.com/datatug/backend/template4datatug"
)

const (
	maxProjectReadFile  = 4 << 20
	maxProjectTreeFiles = 4096
	maxListedQueryBytes = 4 << 20
)

var ErrInvalidProjectSnapshot = errors.New("invalid GitHub project snapshot")
var ErrProjectFileMissing = errors.New("GitHub project file missing")

type scopedReadClient interface {
	GetRef(context.Context, string) (githubauth4datatug.GitHubRef, error)
	GetCommit(context.Context, string) (githubauth4datatug.GitHubCommit, error)
	GetTree(context.Context, string) ([]githubauth4datatug.GitHubTreeEntry, error)
	GetBlob(context.Context, string) ([]byte, error)
}

// ProjectSnapshot pins every file lookup to one immutable commit. It never
// reads a second branch ref while constructing a response.
type ProjectSnapshot struct {
	client   scopedReadClient
	Folder   string
	Branch   string
	Head     string
	files    map[string]githubauth4datatug.GitHubTreeEntry
	allFiles map[string]githubauth4datatug.GitHubTreeEntry
}

func OpenProjectSnapshot(ctx context.Context, client scopedReadClient, folder, branch string) (*ProjectSnapshot, error) {
	if client == nil || template4datatug.ValidateFolder(folder) != nil || branch == "" {
		return nil, ErrInvalidProjectSnapshot
	}
	ref, err := client.GetRef(ctx, branch)
	if err != nil || ref.Name != branch || ref.OID == "" {
		return nil, ErrInvalidProjectSnapshot
	}
	return OpenProjectSnapshotAtHead(ctx, client, folder, branch, ref.OID)
}

// OpenProjectSnapshotAtHead is used to reconstruct an intended query mutation
// after a lost provider response. It never substitutes the branch's new head
// for the immutable expected head supplied by the original operation.
func OpenProjectSnapshotAtHead(ctx context.Context, client scopedReadClient, folder, branch, head string) (*ProjectSnapshot, error) {
	if client == nil || template4datatug.ValidateFolder(folder) != nil || branch == "" || head == "" {
		return nil, ErrInvalidProjectSnapshot
	}
	commit, err := client.GetCommit(ctx, head)
	if err != nil || !strings.EqualFold(commit.OID, head) || commit.TreeOID == "" {
		return nil, ErrInvalidProjectSnapshot
	}
	tree, err := client.GetTree(ctx, commit.TreeOID)
	if err != nil || len(tree) > maxProjectTreeFiles {
		return nil, ErrInvalidProjectSnapshot
	}
	snapshot := &ProjectSnapshot{client: client, Folder: folder, Branch: branch, Head: head, files: make(map[string]githubauth4datatug.GitHubTreeEntry), allFiles: make(map[string]githubauth4datatug.GitHubTreeEntry)}
	prefix := folder + "/"
	for _, entry := range tree {
		if entry.Type == "tree" {
			continue
		}
		if entry.Path == "" || path.Clean(entry.Path) != entry.Path || strings.HasPrefix(entry.Path, "../") || strings.HasPrefix(entry.Path, "/") {
			return nil, ErrInvalidProjectSnapshot
		}
		if _, found := snapshot.allFiles[entry.Path]; found {
			return nil, ErrInvalidProjectSnapshot
		}
		snapshot.allFiles[entry.Path] = entry
		if !strings.HasPrefix(entry.Path, prefix) {
			continue
		}
		relative := strings.TrimPrefix(entry.Path, prefix)
		if relative == "" || path.Clean(relative) != relative || strings.HasPrefix(relative, "../") || entry.Type != "blob" || entry.Mode != "100644" || entry.Size < 0 || entry.Size > maxProjectReadFile || (len(entry.OID) != 40 && len(entry.OID) != 64) {
			return nil, ErrInvalidProjectSnapshot
		}
		if _, found := snapshot.files[relative]; found {
			return nil, ErrInvalidProjectSnapshot
		}
		snapshot.files[relative] = entry
	}
	if _, found := snapshot.files["datatug-project.json"]; !found {
		return nil, ErrProjectFileMissing
	}
	return snapshot, nil
}

func (s *ProjectSnapshot) ReadFile(ctx context.Context, relative string) ([]byte, error) {
	if s == nil || relative == "" || path.Clean(relative) != relative || strings.HasPrefix(relative, "../") || strings.HasPrefix(relative, "/") {
		return nil, ErrInvalidProjectSnapshot
	}
	entry, exists := s.files[relative]
	if !exists {
		return nil, ErrProjectFileMissing
	}
	content, err := s.client.GetBlob(ctx, entry.OID)
	if err != nil || len(content) != int(entry.Size) || !gitBlobOIDMatches(entry.OID, content) {
		return nil, ErrInvalidProjectSnapshot
	}
	return append([]byte(nil), content...), nil
}

// ProjectSummary returns the existing manifest wire object without dropping
// future or legacy metadata. The immutable registered locator supplies access
// control; this JSON is only display data.
func (s *ProjectSnapshot) ProjectSummary(ctx context.Context) (json.RawMessage, error) {
	content, err := s.ReadFile(ctx, "datatug-project.json")
	if err != nil {
		return nil, err
	}
	var manifest struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Access string `json:"access"`
	}
	if err := json.Unmarshal(content, &manifest); err != nil || manifest.ID == "" || manifest.Title == "" || manifest.Access == "" || !json.Valid(content) {
		return nil, ErrInvalidProjectSnapshot
	}
	return json.RawMessage(content), nil
}

type QueryFolder struct {
	ID      string            `json:"id"`
	Folders []*QueryFolder    `json:"folders,omitempty"`
	Items   []json.RawMessage `json:"items,omitempty"`
}

func (s *ProjectSnapshot) AllQueries(ctx context.Context) (*QueryFolder, error) {
	root := &QueryFolder{ID: "~"}
	folders := map[string]*QueryFolder{"": root}
	paths := make([]string, 0)
	for name := range s.files {
		if strings.HasPrefix(name, "queries/") && (strings.HasSuffix(name, ".query.json") || strings.HasSuffix(name, ".sql.json")) {
			paths = append(paths, name)
		}
	}
	sort.Strings(paths)
	var total int
	for _, name := range paths {
		content, err := s.ReadFile(ctx, name)
		if err != nil {
			return nil, err
		}
		total += len(content)
		if total > maxListedQueryBytes {
			return nil, ErrInvalidProjectSnapshot
		}
		var item map[string]json.RawMessage
		if err := json.Unmarshal(content, &item); err != nil || item == nil {
			return nil, ErrInvalidProjectSnapshot
		}
		itemName := path.Base(name)
		id := strings.TrimSuffix(strings.TrimSuffix(itemName, ".query.json"), ".sql.json")
		if id == "" {
			return nil, ErrInvalidProjectSnapshot
		}
		encodedID, _ := json.Marshal(id)
		item["id"] = encodedID
		if _, hasType := item["type"]; !hasType && strings.HasSuffix(name, ".sql.json") {
			item["type"] = json.RawMessage(`"SQL"`)
		}
		delete(item, "text")
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		dir := strings.TrimPrefix(path.Dir(name), "queries")
		dir = strings.TrimPrefix(dir, "/")
		parent := root
		if dir != "" {
			var current string
			for _, part := range strings.Split(dir, "/") {
				child := part
				if current != "" {
					child = current + "/" + part
				}
				if folders[child] == nil {
					folders[child] = &QueryFolder{ID: part}
					parent.Folders = append(parent.Folders, folders[child])
				}
				parent = folders[child]
				current = child
			}
		}
		parent.Items = append(parent.Items, encoded)
	}
	return root, nil
}

func (s *ProjectSnapshot) QueryFiles(ctx context.Context, folderPath, id string) (map[string][]byte, string, error) {
	if folderPath == "~" {
		folderPath = ""
	}
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "\\") || (folderPath != "" && template4datatug.ValidateFolder(folderPath) != nil) || template4datatug.ValidateFolder(id) != nil {
		return nil, "", ErrInvalidProjectSnapshot
	}
	dir := path.Join("queries", folderPath)
	for _, suffix := range []string{".query.json", ".sql.json"} {
		def := path.Join(dir, id+suffix)
		if _, exists := s.files[def]; !exists {
			continue
		}
		metadata, err := s.ReadFile(ctx, def)
		if err != nil {
			return nil, "", err
		}
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(metadata, &header); err != nil {
			return nil, "", ErrInvalidProjectSnapshot
		}
		if header.Type == "" && suffix == ".sql.json" {
			header.Type = "SQL"
		}
		var bodyPath string
		if suffix == ".sql.json" {
			bodyPath = path.Join(dir, id+".sql")
		} else {
			switch header.Type {
			case "SQL", "DTQL", "HTTP":
				bodyPath = path.Join(dir, id+".query."+strings.ToLower(header.Type))
			default:
				return nil, "", ErrInvalidProjectSnapshot
			}
		}
		body, err := s.ReadFile(ctx, bodyPath)
		if err != nil {
			return nil, "", err
		}
		return map[string][]byte{def: metadata, bodyPath: body}, header.Type, nil
	}
	return nil, "", ErrProjectFileMissing
}
