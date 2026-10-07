package githubstore4datatug

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/backend/githubauth4datatug"
)

func TestProjectSnapshotRejectsUntrustedTreeBeforeExposingPrivateContent(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		mutate func(*snapshotFake)
		want   error
	}{
		{"path escape", func(f *snapshotFake) {
			f.tree = append(f.tree, githubauth4datatug.GitHubTreeEntry{Path: "../outside", Type: "blob"})
		}, ErrInvalidProjectSnapshot},
		{"duplicate path", func(f *snapshotFake) { f.tree = append(f.tree, f.tree[0]) }, ErrInvalidProjectSnapshot},
		{"submodule in project", func(f *snapshotFake) {
			f.tree = append(f.tree, githubauth4datatug.GitHubTreeEntry{Path: "demo-project-1/vendor", Type: "commit", OID: createCommittedHead, Mode: "160000"})
		}, ErrInvalidProjectSnapshot},
		{"oversized project file", func(f *snapshotFake) {
			f.tree = append(f.tree, githubauth4datatug.GitHubTreeEntry{Path: "demo-project-1/huge", Type: "blob", OID: createCommittedHead, Mode: "100644", Size: maxProjectReadFile + 1})
		}, ErrInvalidProjectSnapshot},
		{"missing manifest", func(f *snapshotFake) {
			for i, entry := range f.tree {
				if entry.Path == "demo-project-1/datatug-project.json" {
					f.tree = append(f.tree[:i], f.tree[i+1:]...)
					break
				}
			}
		}, ErrProjectFileMissing},
		{"too many entries", func(f *snapshotFake) {
			f.tree = append(f.tree, make([]githubauth4datatug.GitHubTreeEntry, maxProjectTreeFiles+1)...)
		}, ErrInvalidProjectSnapshot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newSnapshotFake(t)
			tc.mutate(fake)
			if _, err := OpenProjectSnapshot(ctx, fake, "demo-project-1", "work"); !errors.Is(err, tc.want) {
				t.Fatalf("untrusted tree accepted: %v", err)
			}
		})
	}
}

func TestProjectSnapshotRefusesMalformedManifestAndQueryPair(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, path, content string
		query               bool
	}{
		{"manifest lacks identity", "demo-project-1/datatug-project.json", `{}`, false},
		{"query definition invalid JSON", "demo-project-1/queries/albums/albums_by_title.sql.json", `{`, true},
		{"query kind unsupported", "demo-project-1/queries/custom.query.json", `{"id":"custom","type":"UNKNOWN"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newSnapshotFake(t)
			oid := testGitBlobOID([]byte(tc.content))
			fake.files[oid] = []byte(tc.content)
			found := false
			for i := range fake.tree {
				if fake.tree[i].Path == tc.path {
					fake.tree[i].OID, fake.tree[i].Size = oid, int64(len(tc.content))
					found = true
				}
			}
			if !found {
				fake.tree = append(fake.tree, githubauth4datatug.GitHubTreeEntry{Path: tc.path, OID: oid, Type: "blob", Mode: "100644", Size: int64(len(tc.content))})
			}
			snapshot, err := OpenProjectSnapshot(ctx, fake, "demo-project-1", "work")
			if err != nil {
				t.Fatal(err)
			}
			if tc.query {
				if strings.Contains(tc.name, "invalid JSON") {
					_, err = snapshot.AllQueries(ctx)
				} else {
					_, _, err = snapshot.QueryFiles(ctx, "~", "custom")
				}
			} else {
				_, err = snapshot.ProjectSummary(ctx)
			}
			if !errors.Is(err, ErrInvalidProjectSnapshot) {
				t.Fatalf("malformed content accepted: %v", err)
			}
		})
	}
}

func TestProjectSnapshotRejectsInvalidOrMissingQueryBody(t *testing.T) {
	ctx := context.Background()
	fake := newSnapshotFake(t)
	snapshot, err := OpenProjectSnapshot(ctx, fake, "demo-project-1", "work")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../escape", "no-such-query"} {
		_, _, err := snapshot.QueryFiles(ctx, "~", id)
		if err == nil {
			t.Fatalf("invalid or missing query %q accepted", id)
		}
	}
	for i, entry := range fake.tree {
		if strings.HasSuffix(entry.Path, "/albums_by_title.sql") {
			fake.tree = append(fake.tree[:i], fake.tree[i+1:]...)
			break
		}
	}
	snapshot, err = OpenProjectSnapshot(ctx, fake, "demo-project-1", "work")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := snapshot.QueryFiles(ctx, "albums", "albums_by_title"); !errors.Is(err, ErrProjectFileMissing) {
		t.Fatalf("missing body accepted: %v", err)
	}
}
