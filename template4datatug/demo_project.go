// Package template4datatug holds a reviewed, immutable DataTug demo template.
// The provider adapter supplies the selected repository and branch; this
// package only returns bounded regular-file bytes for one project folder.
package template4datatug

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
)

const (
	DemoProjectID     = "demo-project-1"
	DemoProjectSource = "datatug/datatug-demo-project"
	DemoProjectCommit = "51716f3a4d682d5cb7ef70a7fd37f42e5418fd3d"
	archiveSHA256     = "f34921d45f9766e4aeca3dba0a8cd757ddd579f957fc8c35642d3ff8a483a74c"
	manifestSHA256    = "958d48ce1ec4a5839aec2ff5973c8b47acf3855b7d5abf6e06660c44a9fb5b99"
	maxTemplateFiles  = 631
	maxTemplateBytes  = 279006
	maxTemplateFile   = 1 << 20
)

var (
	ErrInvalidTemplate = errors.New("invalid DataTug demo template")
	ErrInvalidFolder   = errors.New("invalid project folder")
	folderSegment      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

//go:embed assets/demo-project-1.tar.gz assets/demo-project-1-manifest.json
var assets embed.FS

type manifestFile struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	Source     string         `json:"source"`
	TemplateID string         `json:"templateID"`
	Commit     string         `json:"commit"`
	Files      []manifestFile `json:"files"`
}

// ValidateFolder rejects path traversal and ambiguous external-key delimiters.
// Nested folders are supported only as clean, bounded relative paths.
func ValidateFolder(folder string) error {
	if folder == "" || len(folder) > 256 || strings.ContainsAny(folder, "\\@") || path.IsAbs(folder) || path.Clean(folder) != folder {
		return ErrInvalidFolder
	}
	for _, segment := range strings.Split(folder, "/") {
		if segment == "." || segment == ".." || !folderSegment.MatchString(segment) {
			return ErrInvalidFolder
		}
	}
	return nil
}

// DemoProjectFiles verifies the embedded archive against its independently
// recorded manifest, then returns a clone beneath folder. Every non-manifest
// file keeps its source bytes. Callers may replace datatug-project.json with
// a new identity/title/access/created object before a single Git commit.
func DemoProjectFiles(folder string) (map[string][]byte, error) {
	if err := ValidateFolder(folder); err != nil {
		return nil, err
	}
	archive, err := assets.ReadFile("assets/demo-project-1.tar.gz")
	if err != nil || !hasSHA256(archive, archiveSHA256) {
		return nil, ErrInvalidTemplate
	}
	manifestBytes, err := assets.ReadFile("assets/demo-project-1-manifest.json")
	if err != nil || !hasSHA256(manifestBytes, manifestSHA256) {
		return nil, ErrInvalidTemplate
	}
	return decodeDemoProject(archive, manifestBytes, folder)
}

// CloneDemoProject rewrites only the source project's identity fields. The
// manifest's other JSON members and all 630 other template files survive.
func CloneDemoProject(folder, projectID, title string, created time.Time) (map[string][]byte, error) {
	if projectID == "" || len(projectID) > 256 || title == "" || len(title) > 400 || created.IsZero() {
		return nil, ErrInvalidTemplate
	}
	files, err := DemoProjectFiles(folder)
	if err != nil {
		return nil, err
	}
	manifestPath := path.Join(folder, "datatug-project.json")
	var original map[string]json.RawMessage
	if err := json.Unmarshal(files[manifestPath], &original); err != nil || original == nil {
		return nil, ErrInvalidTemplate
	}
	fields := map[string]any{
		"id": projectID, "title": title, "access": "protected",
		"created": map[string]string{"at": created.UTC().Format(time.RFC3339Nano)},
	}
	for key, value := range fields {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, ErrInvalidTemplate
		}
		original[key] = encoded
	}
	encoded, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		return nil, ErrInvalidTemplate
	}
	files[manifestPath] = append(encoded, '\n')
	return files, nil
}

func decodeDemoProject(archive, manifestBytes []byte, folder string) (map[string][]byte, error) {
	var expected manifest
	if err := json.Unmarshal(manifestBytes, &expected); err != nil || expected.Source != DemoProjectSource || expected.TemplateID != DemoProjectID || expected.Commit != DemoProjectCommit || len(expected.Files) != maxTemplateFiles {
		return nil, ErrInvalidTemplate
	}
	entries := make(map[string]manifestFile, len(expected.Files))
	var declaredBytes int64
	for _, file := range expected.Files {
		if !validArchivePath(file.Path) || file.Mode != "100644" || file.Size < 0 || file.Size > maxTemplateFile || len(file.SHA256) != 64 {
			return nil, ErrInvalidTemplate
		}
		if _, exists := entries[file.Path]; exists {
			return nil, ErrInvalidTemplate
		}
		entries[file.Path] = file
		declaredBytes += file.Size
	}
	if declaredBytes != maxTemplateBytes {
		return nil, ErrInvalidTemplate
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("%w: gzip: %v", ErrInvalidTemplate, err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	files := make(map[string][]byte, len(entries))
	var readBytes int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || header == nil || header.Typeflag != tar.TypeReg || !validArchivePath(header.Name) || header.Mode != 0644 {
			return nil, ErrInvalidTemplate
		}
		want, exists := entries[header.Name]
		if !exists || header.Size != want.Size || header.Size > maxTemplateFile {
			return nil, ErrInvalidTemplate
		}
		if _, duplicate := files[header.Name]; duplicate {
			return nil, ErrInvalidTemplate
		}
		content, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		if err != nil || int64(len(content)) != header.Size || !hasSHA256(content, want.SHA256) {
			return nil, ErrInvalidTemplate
		}
		readBytes += int64(len(content))
		if readBytes > maxTemplateBytes {
			return nil, ErrInvalidTemplate
		}
		relative := strings.TrimPrefix(header.Name, DemoProjectID+"/")
		files[path.Join(folder, relative)] = content
	}
	if len(files) != len(entries) || readBytes != maxTemplateBytes {
		return nil, ErrInvalidTemplate
	}
	return files, nil
}

func validArchivePath(file string) bool {
	if !strings.HasPrefix(file, DemoProjectID+"/") || strings.ContainsAny(file, "\\@") || path.Clean(file) != file {
		return false
	}
	for _, segment := range strings.Split(file, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func hasSHA256(b []byte, expected string) bool {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]) == expected
}
