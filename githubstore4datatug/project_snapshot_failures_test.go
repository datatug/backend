package githubstore4datatug

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/backend/githubauth4datatug"
)

type wrongRefSnapshotClient struct{ *snapshotFake }

func (c wrongRefSnapshotClient) GetRef(context.Context, string) (githubauth4datatug.GitHubRef, error) {
	return githubauth4datatug.GitHubRef{Name: "other", OID: createCommittedHead}, nil
}

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

func TestProjectSnapshotRequiresImmutableSelectedHeadAndValidFolder(t *testing.T) {
	ctx := context.Background()
	fake := newSnapshotFake(t)
	for _, tc := range []struct {
		name, folder, branch, head string
		client                     scopedReadClient
	}{
		{"missing provider", "demo-project-1", "work", createCommittedHead, nil},
		{"unsafe folder", "../private", "work", createCommittedHead, fake},
		{"no selected branch", "demo-project-1", "", createCommittedHead, fake},
		{"no immutable head", "demo-project-1", "work", "", fake},
		{"unknown immutable head", "demo-project-1", "work", strings.Repeat("f", 40), fake},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := OpenProjectSnapshotAtHead(ctx, tc.client, tc.folder, tc.branch, tc.head); !errors.Is(err, ErrInvalidProjectSnapshot) {
				t.Fatalf("untrusted snapshot accepted: %v", err)
			}
		})
	}
	if _, err := OpenProjectSnapshot(ctx, nil, "demo-project-1", "work"); !errors.Is(err, ErrInvalidProjectSnapshot) {
		t.Fatalf("nil provider accepted: %v", err)
	}
	if _, err := OpenProjectSnapshot(ctx, fake, "demo-project-1", ""); !errors.Is(err, ErrInvalidProjectSnapshot) {
		t.Fatalf("missing branch accepted: %v", err)
	}
	if _, err := OpenProjectSnapshot(ctx, wrongRefSnapshotClient{fake}, "demo-project-1", "work"); !errors.Is(err, ErrInvalidProjectSnapshot) {
		t.Fatalf("mismatched ref accepted: %v", err)
	}
	snapshot, err := OpenProjectSnapshot(ctx, fake, "demo-project-1", "work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.ReadFile(ctx, "../outside"); !errors.Is(err, ErrInvalidProjectSnapshot) {
		t.Fatalf("path escape read accepted: %v", err)
	}
	if _, err := snapshot.ReadFile(ctx, "missing.txt"); !errors.Is(err, ErrProjectFileMissing) {
		t.Fatalf("missing blob accepted: %v", err)
	}
}

func TestProjectSnapshotQueryFileReadRefusesMalformedDefinition(t *testing.T) {
	ctx := context.Background()
	fake := newSnapshotFake(t)
	for i, entry := range fake.tree {
		if entry.Path == "demo-project-1/queries/demodb/chinook-top-customer-spend.query.json" {
			content := []byte(`{`)
			oid := testGitBlobOID(content)
			fake.files[oid] = content
			fake.tree[i].OID = oid
			fake.tree[i].Size = int64(len(content))
			break
		}
	}
	snapshot, err := OpenProjectSnapshot(ctx, fake, "demo-project-1", "work")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := snapshot.QueryFiles(ctx, "demodb", "chinook-top-customer-spend"); !errors.Is(err, ErrInvalidProjectSnapshot) {
		t.Fatalf("malformed query definition accepted: %v", err)
	}
}

func TestProjectSnapshotBoundsPrivateQueryListing(t *testing.T) {
	ctx := context.Background()
	fake := newSnapshotFake(t)
	content := []byte(`{"title":"` + strings.Repeat("x", maxListedQueryBytes/2) + `"}`)
	for _, name := range []string{"demo-project-1/queries/large-1.query.json", "demo-project-1/queries/large-2.query.json"} {
		oid := testGitBlobOID(content)
		fake.files[oid] = content
		fake.tree = append(fake.tree, githubauth4datatug.GitHubTreeEntry{Path: name, Type: "blob", Mode: "100644", OID: oid, Size: int64(len(content))})
	}
	snapshot, err := OpenProjectSnapshot(ctx, fake, "demo-project-1", "work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.AllQueries(ctx); !errors.Is(err, ErrInvalidProjectSnapshot) {
		t.Fatalf("unbounded private listing accepted: %v", err)
	}
}
