package githubstore4datatug

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/backend/githubauth4datatug"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

const queryCommitOID = "dddddddddddddddddddddddddddddddddddddddd"
const queryTreeOID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

type queryFake struct {
	*snapshotFake
	newTree     []githubauth4datatug.GitHubTreeEntry
	newCommit   githubauth4datatug.GitHubCommit
	createCount int
}

func (f *queryFake) Scope() githubauth4datatug.RepositoryScope {
	return githubauth4datatug.RepositoryScope{FirebaseUID: "actor", Repository: githubauth4datatug.GitHubRepository{ID: 123, Owner: "owner", Name: "repo"}, Permission: githubauth4datatug.RepositoryWrite}
}
func (f *queryFake) GetRef(_ context.Context, branch string) (githubauth4datatug.GitHubRef, error) {
	f.refReads++
	head := createCommittedHead
	if f.createCount > 0 {
		head = queryCommitOID
	}
	return githubauth4datatug.GitHubRef{Name: branch, OID: head}, nil
}
func (f *queryFake) GetCommit(ctx context.Context, oid string) (githubauth4datatug.GitHubCommit, error) {
	if oid == queryCommitOID {
		return f.newCommit, nil
	}
	return f.snapshotFake.GetCommit(ctx, oid)
}
func (f *queryFake) GetTree(ctx context.Context, oid string) ([]githubauth4datatug.GitHubTreeEntry, error) {
	if oid == queryTreeOID {
		return f.newTree, nil
	}
	return f.snapshotFake.GetTree(ctx, oid)
}
func (f *queryFake) CreateCommitOnBranch(_ context.Context, _, expectedHead, message string, changes []githubauth4datatug.GitHubFileChange) (githubauth4datatug.GitHubCommit, error) {
	if expectedHead != createCommittedHead {
		return githubauth4datatug.GitHubCommit{}, errors.New("stale head")
	}
	paths := make(map[string]githubauth4datatug.GitHubTreeEntry)
	for _, entry := range f.tree {
		if entry.Type != "tree" {
			paths[entry.Path] = entry
		}
	}
	for _, change := range changes {
		if change.Delete {
			delete(paths, change.Path)
			continue
		}
		oid := testGitBlobOID(change.Content)
		f.files[oid] = append([]byte(nil), change.Content...)
		paths[change.Path] = githubauth4datatug.GitHubTreeEntry{Path: change.Path, Type: "blob", OID: oid, Mode: "100644", Size: int64(len(change.Content))}
	}
	f.newTree = make([]githubauth4datatug.GitHubTreeEntry, 0, len(paths))
	for _, entry := range paths {
		f.newTree = append(f.newTree, entry)
	}
	f.newCommit = githubauth4datatug.GitHubCommit{OID: queryCommitOID, TreeOID: queryTreeOID, Message: message, ParentOIDs: []string{expectedHead}}
	f.createCount++
	return f.newCommit, nil
}

func TestQueryRepositorySavesPairAtExpectedHeadAndRecoversExactCommit(t *testing.T) {
	fake := &queryFake{snapshotFake: newSnapshotFake(t)}
	repo := &queryRepository{client: fake}
	request := dto.SaveQueryRequest{ProjectRef: dto.ProjectRef{StoreID: "github.com", ProjectID: "repo@owner@demo-project-1"}, Branch: "work", ExpectedBranchHead: createCommittedHead, OperationID: "save-1", IfNoneMatch: true, Query: datatug.QueryDefWithFolderPath{FolderPath: "~", QueryDef: datatug.QueryDef{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "new-query", Title: "New query"}}, Type: "DTQL", Text: "SELECT CustomerId FROM chinook.Customer"}}}
	plan, err := repo.PrepareQuerySave(context.Background(), "demo-project-1", "datatug-demo-project", "work", request)
	if err != nil || len(plan.Changes) != 2 || plan.Response.Revision == "" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	message := "Save DataTug query; DataTug-Operation: exact-marker"
	head, err := repo.CreateQueryCommit(context.Background(), "work", createCommittedHead, message, plan)
	if err != nil || head != queryCommitOID || fake.createCount != 1 {
		t.Fatalf("commit=%q err=%v count=%d", head, err, fake.createCount)
	}
	recovered, err := repo.FindQueryCommit(context.Background(), "work", createCommittedHead, "exact-marker", plan)
	if err != nil || recovered != queryCommitOID || fake.createCount != 1 {
		t.Fatalf("recover=%q err=%v count=%d", recovered, err, fake.createCount)
	}
	for _, change := range plan.Changes {
		if !strings.HasPrefix(change.Path, "demo-project-1/queries/new-query.query.") {
			t.Fatalf("unexpected change %+v", change)
		}
	}
	fake.newTree = append(fake.newTree, githubauth4datatug.GitHubTreeEntry{Path: "demo-project-1/unrelated.txt", Type: "blob", OID: testGitBlobOID([]byte("foreign")), Mode: "100644", Size: 7})
	if _, err := repo.FindQueryCommit(context.Background(), "work", createCommittedHead, "exact-marker", plan); !errors.Is(err, ErrGitHubQueryCommitProof) {
		t.Fatalf("foreign edit accepted: %v", err)
	}
}
