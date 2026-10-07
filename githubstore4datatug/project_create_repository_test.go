package githubstore4datatug

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

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
