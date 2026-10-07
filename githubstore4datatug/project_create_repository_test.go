package githubstore4datatug

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/githubauth4datatug"
)

const (
	createExpectedHead  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	createCommittedHead = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	createTreeOID       = "cccccccccccccccccccccccccccccccccccccccc"
)

type scopedCreateFake struct {
	head    string
	commit  githubauth4datatug.GitHubCommit
	tree    []githubauth4datatug.GitHubTreeEntry
	created int
}

func (*scopedCreateFake) Scope() githubauth4datatug.RepositoryScope {
	return githubauth4datatug.RepositoryScope{FirebaseUID: "actor", Repository: githubauth4datatug.GitHubRepository{ID: 123, Owner: "owner", Name: "repo"}, Permission: githubauth4datatug.RepositoryWrite}
}
func (f *scopedCreateFake) GetRef(_ context.Context, branch string) (githubauth4datatug.GitHubRef, error) {
	return githubauth4datatug.GitHubRef{Name: branch, OID: f.head}, nil
}
func (f *scopedCreateFake) GetCommit(_ context.Context, oid string) (githubauth4datatug.GitHubCommit, error) {
	if oid == f.commit.OID {
		return f.commit, nil
	}
	return githubauth4datatug.GitHubCommit{}, errors.New("unknown commit")
}
func (f *scopedCreateFake) GetTree(_ context.Context, oid string) ([]githubauth4datatug.GitHubTreeEntry, error) {
	if oid != createTreeOID {
		return nil, errors.New("unknown tree")
	}
	return f.tree, nil
}
func (f *scopedCreateFake) CreateCommitOnBranch(_ context.Context, _, head, message string, changes []githubauth4datatug.GitHubFileChange) (githubauth4datatug.GitHubCommit, error) {
	if head != createExpectedHead || len(changes) != 2 {
		return githubauth4datatug.GitHubCommit{}, errors.New("wrong CAS or file pair")
	}
	f.created++
	f.commit.Message = message
	return f.commit, nil
}

func testGitBlobOID(content []byte) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(content))
	_, _ = h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

func testCreateRepo(files map[string][]byte, marker string) *scopedCreateFake {
	tree := []githubauth4datatug.GitHubTreeEntry{{Path: "datatug", Type: "tree", OID: createTreeOID, Mode: "040000"}}
	for name, content := range files {
		tree = append(tree, githubauth4datatug.GitHubTreeEntry{Path: name, Type: "blob", OID: testGitBlobOID(content), Mode: "100644", Size: int64(len(content))})
	}
	return &scopedCreateFake{
		head: createCommittedHead, tree: tree,
		commit: githubauth4datatug.GitHubCommit{
			OID: createCommittedHead, TreeOID: createTreeOID, Message: "Create DataTug project; DataTug-Operation: " + marker,
			ParentOIDs: []string{createExpectedHead},
		},
	}
}

func TestCreateRepositoryRecoversOnlyExactMarkerParentAndFiles(t *testing.T) {
	files := map[string][]byte{"datatug/datatug-project.json": []byte(`{"id":"project"}`), "datatug/queries/q.query.json": []byte(`{"id":"q"}`)}
	fake := testCreateRepo(files, "marker")
	repo := &createRepository{client: fake}
	if head, err := repo.FindCommitByMarker(context.Background(), "work", createExpectedHead, "marker", files); err != nil || head != createCommittedHead {
		t.Fatalf("exact commit %q: %v", head, err)
	}
	fake.tree[1].OID = testGitBlobOID([]byte("foreign"))
	if _, err := repo.FindCommitByMarker(context.Background(), "work", createExpectedHead, "marker", files); !errors.Is(err, ErrGitHubCreateProof) {
		t.Fatalf("foreign bytes accepted: %v", err)
	}
	fake = testCreateRepo(files, "marker")
	fake.commit.ParentOIDs[0] = "dddddddddddddddddddddddddddddddddddddddd"
	if _, err := (&createRepository{client: fake}).FindCommitByMarker(context.Background(), "work", createExpectedHead, "marker", files); !errors.Is(err, ErrGitHubCreateProof) {
		t.Fatalf("wrong branch-parent accepted: %v", err)
	}
}

func TestCreateRepositoryVerifiesCreatedCommitBeforeReportingSuccess(t *testing.T) {
	files := map[string][]byte{"datatug/datatug-project.json": []byte(`{"id":"project"}`), "datatug/queries/q.query.json": []byte(`{"id":"q"}`)}
	fake := testCreateRepo(files, "marker")
	repo := &createRepository{client: fake}
	if head, err := repo.CreateFilesCommit(context.Background(), "work", createExpectedHead, fake.commit.Message, files); err != nil || head != createCommittedHead || fake.created != 1 {
		t.Fatalf("created %q: %v calls=%d", head, err, fake.created)
	}
	fake.commit.ParentOIDs[0] = "dddddddddddddddddddddddddddddddddddddddd"
	if _, err := repo.CreateFilesCommit(context.Background(), "work", createExpectedHead, fake.commit.Message, files); !errors.Is(err, ErrGitHubCreateProof) {
		t.Fatalf("wrong parent reported success: %v", err)
	}
}

func TestCreateRepositoryRejectsNonTreeAncestorBeforeMutation(t *testing.T) {
	fake := &scopedCreateFake{head: createExpectedHead, commit: githubauth4datatug.GitHubCommit{OID: createExpectedHead, TreeOID: createTreeOID}, tree: []githubauth4datatug.GitHubTreeEntry{{Path: "a", Type: "blob", Mode: "100644", OID: testGitBlobOID([]byte("occupied")), Size: 8}}}
	repo := &createRepository{client: fake}
	if err := repo.EnsureFolderEmpty(context.Background(), createExpectedHead, "a/b"); !errors.Is(err, facade4datatug.ErrGitHubProjectConflict) {
		t.Fatalf("blob ancestor passed preflight: %v", err)
	}
	if fake.created != 0 {
		t.Fatal("provider commit attempted")
	}
	fake.tree[0].Type = "commit"
	fake.tree[0].Mode = "160000"
	if err := repo.EnsureFolderEmpty(context.Background(), createExpectedHead, "a/b"); !errors.Is(err, facade4datatug.ErrGitHubProjectConflict) {
		t.Fatalf("submodule ancestor passed preflight: %v", err)
	}
	fake.tree[0].Type = "tree"
	fake.tree[0].Mode = "040000"
	if err := repo.EnsureFolderEmpty(context.Background(), createExpectedHead, "a/b"); err != nil {
		t.Fatalf("directory ancestor rejected: %v", err)
	}
}

func TestCreateRepositoryRefusesProviderProofGaps(t *testing.T) {
	files := map[string][]byte{"datatug/datatug-project.json": []byte(`{"id":"project"}`), "datatug/queries/q.query.json": []byte(`{"id":"q"}`)}
	for _, tc := range []struct {
		name   string
		mutate func(*scopedCreateFake)
		marker string
		want   error
	}{
		{"marker missing", func(*scopedCreateFake) {}, "other-marker", nil},
		{"unexpected extra file", func(f *scopedCreateFake) {
			f.tree = append(f.tree, githubauth4datatug.GitHubTreeEntry{Path: "datatug/foreign", Type: "blob", Mode: "100644", OID: testGitBlobOID([]byte("x")), Size: 1})
		}, "marker", ErrGitHubCreateProof},
		{"file mode changed", func(f *scopedCreateFake) { f.tree[1].Mode = "100755" }, "marker", ErrGitHubCreateProof},
		{"file omitted", func(f *scopedCreateFake) { f.tree = f.tree[:2] }, "marker", ErrGitHubCreateProof},
		{"tree lookup fails", func(f *scopedCreateFake) { f.commit.TreeOID = "unknown" }, "marker", ErrGitHubCreateProof},
		{"wrong OID reply", func(f *scopedCreateFake) { f.commit.OID = "ffffffffffffffffffffffffffffffffffffffff" }, "marker", ErrGitHubCreateProof},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := testCreateRepo(files, "marker")
			tc.mutate(fake)
			head, err := (&createRepository{client: fake}).FindCommitByMarker(context.Background(), "work", createExpectedHead, tc.marker, files)
			if tc.want == nil {
				if err != nil || head != "" {
					t.Fatalf("unrelated marker returned %q: %v", head, err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("provider proof accepted %q: %v", head, err)
			}
		})
	}
}

func TestCreateRepositoryRejectsEmptyCommitAndStaleHead(t *testing.T) {
	if _, err := AuthorizeCreateRepository(context.Background(), nil, "actor", 123, "owner", "repo"); !errors.Is(err, githubauth4datatug.ErrGitHubAppNotConfigured) {
		t.Fatalf("missing dedicated App accepted: %v", err)
	}
	if got := (*createRepository)(nil).Scope(); got != (facade4datatug.GitHubCreateRepositoryScope{}) {
		t.Fatalf("nil repository acquired scope: %+v", got)
	}
	fake := testCreateRepo(map[string][]byte{"datatug/datatug-project.json": []byte(`{"id":"project"}`), "datatug/queries/q.query.json": []byte(`{"id":"q"}`)}, "marker")
	repo := &createRepository{client: fake}
	if _, err := repo.CreateFilesCommit(context.Background(), "work", createExpectedHead, "message", nil); !errors.Is(err, ErrGitHubCreateProof) {
		t.Fatalf("empty commit: %v", err)
	}
	if err := repo.EnsureFolderEmpty(context.Background(), "wrong-head", "datatug"); !errors.Is(err, ErrGitHubCreateProof) {
		t.Fatalf("wrong head preflight: %v", err)
	}
	if _, err := repo.CurrentHead(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	fake.head = ""
	if _, err := repo.CurrentHead(context.Background(), "work"); !errors.Is(err, ErrGitHubCreateProof) {
		t.Fatalf("missing head accepted: %v", err)
	}
}
