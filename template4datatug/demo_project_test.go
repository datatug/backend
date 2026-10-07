package template4datatug

import (
	"bytes"
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
