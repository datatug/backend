package template4datatug

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"reflect"
	"testing"
	"time"
)

func TestCloneDemoProjectPreservesAllOtherTemplateBytes(t *testing.T) {
	original, err := DemoProjectFiles("demo-project-1")
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 10, 7, 14, 30, 0, 0, time.UTC)
	cloned, err := CloneDemoProject("work/datatug", "project-42", "Customer queries", created)
	if err != nil {
		t.Fatal(err)
	}
	if len(cloned) != 631 {
		t.Fatalf("got %d files, want 631", len(cloned))
	}
	for name, source := range original {
		relative := name[len("demo-project-1/"):]
		candidate, ok := cloned[path.Join("work/datatug", relative)]
		if !ok {
			t.Fatalf("missing cloned file %s", relative)
		}
		if relative != "datatug-project.json" && !bytes.Equal(candidate, source) {
			t.Fatalf("changed legacy template file %s", relative)
		}
	}
	var source, result map[string]json.RawMessage
	if err := json.Unmarshal(original["demo-project-1/datatug-project.json"], &source); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(cloned["work/datatug/datatug-project.json"], &result); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "title", "access", "created"} {
		delete(source, key)
		delete(result, key)
	}
	if !reflect.DeepEqual(source, result) {
		t.Fatal("clone discarded or changed non-identity manifest metadata")
	}
	if string(cloned["work/datatug/datatug-project.json"]) == string(original["demo-project-1/datatug-project.json"]) {
		t.Fatal("clone retained template identity")
	}
}

func TestEmbeddedTemplateDecoderRefusesArchiveSubstitution(t *testing.T) {
	manifestBytes, err := assets.ReadFile("assets/demo-project-1-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, file string
		mode       int64
		kind       byte
		content    string
	}{
		{"symlink", m.Files[0].Path, 0644, tar.TypeSymlink, ""},
		{"executable", m.Files[0].Path, 0755, tar.TypeReg, "x"},
		{"unlisted path", "demo-project-1/foreign.txt", 0644, tar.TypeReg, "x"},
		{"path traversal", "demo-project-1/../private", 0644, tar.TypeReg, "x"},
		{"tampered content", m.Files[0].Path, 0644, tar.TypeReg, "foreign"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var packed bytes.Buffer
			gz := gzip.NewWriter(&packed)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(&tar.Header{Name: tc.file, Mode: tc.mode, Typeflag: tc.kind, Size: int64(len(tc.content))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(tc.content)); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := decodeDemoProject(packed.Bytes(), manifestBytes, "project"); !errors.Is(err, ErrInvalidTemplate) {
				t.Fatalf("untrusted archive accepted: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*manifest)
	}{
		{"wrong source", func(m *manifest) { m.Source = "other" }},
		{"duplicate file declaration", func(m *manifest) { m.Files[1].Path = m.Files[0].Path }},
		{"untrusted mode", func(m *manifest) { m.Files[0].Mode = "100755" }},
		{"unbounded file", func(m *manifest) { m.Files[0].Size = maxTemplateFile + 1 }},
		{"wrong declared bytes", func(m *manifest) { m.Files[0].Size++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var modified manifest
			if err := json.Unmarshal(manifestBytes, &modified); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&modified)
			b, err := json.Marshal(modified)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeDemoProject([]byte("ignored"), b, "project"); !errors.Is(err, ErrInvalidTemplate) {
				t.Fatalf("invalid declaration accepted: %v", err)
			}
		})
	}
}

func TestDemoProjectRejectsPathTraversalAndModifiedManifest(t *testing.T) {
	for _, folder := range []string{"", "../escape", "/absolute", "a//b", "a/./b", "a\\b", "a@b"} {
		_, err := DemoProjectFiles(folder)
		if !errors.Is(err, ErrInvalidFolder) {
			t.Errorf("folder %q: %v", folder, err)
		}
	}
	archive, err := assets.ReadFile("assets/demo-project-1.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := assets.ReadFile("assets/demo-project-1-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		t.Fatal(err)
	}
	m.Files[0].SHA256 = hex.EncodeToString(sha256.New().Sum(nil))
	altered, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeDemoProject(archive, altered, "datatug"); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("modified file hash accepted: %v", err)
	}
}
