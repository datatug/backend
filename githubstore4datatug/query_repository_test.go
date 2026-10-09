package githubstore4datatug

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/githubauth4datatug"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

const queryCommitOID = "dddddddddddddddddddddddddddddddddddddddd"
const queryTreeOID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

type queryFake struct {
	*snapshotFake
	newTree      []githubauth4datatug.GitHubTreeEntry
	newCommit    githubauth4datatug.GitHubCommit
	createCount  int
	createdError error
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
	if f.createdError != nil {
		return githubauth4datatug.GitHubCommit{}, f.createdError
	}
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

func TestQueryRepositoryRejectsStaleBaseAndLossyLegacyDefinition(t *testing.T) {
	fake := &queryFake{snapshotFake: newSnapshotFake(t)}
	repo := &queryRepository{client: fake}
	request := queryRequest()
	request.Query.ID, request.Query.Title = "customers", "Customers"
	request.Branch = "wrong"
	if _, err := repo.PrepareQuerySave(context.Background(), "demo-project-1", "datatug-demo-project", "work", request); !errors.Is(err, facade4datatug.ErrGitHubQueryInvalid) {
		t.Fatalf("branch mismatch: %v", err)
	}
	request.Branch = "work"
	request.ExpectedBranchHead = createCommittedHead
	if _, err := repo.PrepareQuerySave(context.Background(), "demo-project-1", "other-project", "work", request); !errors.Is(err, ErrInvalidProjectSnapshot) {
		t.Fatalf("foreign project manifest accepted: %v", err)
	}
	legacyPath := "demo-project-1/queries/customers.sql.json"
	content := []byte(`{"id":"customers","title":"Legacy"}`)
	oid := testGitBlobOID(content)
	fake.files[oid] = content
	fake.tree = append(fake.tree, githubauth4datatug.GitHubTreeEntry{Path: legacyPath, Type: "blob", OID: oid, Mode: "100644", Size: int64(len(content))})
	if _, err := repo.PrepareQuerySave(context.Background(), "demo-project-1", "datatug-demo-project", "work", request); !errors.Is(err, ErrUnsupportedExistingQuery) {
		t.Fatalf("legacy query overwritten: %v", err)
	}
}

func TestQueryRepositoryRequiresPinnedProjectConnectionForBrowserSQL(t *testing.T) {
	request := queryRequest()
	request.Query.ID, request.Query.Title = "genre-mix", "Genre mix"
	request.Query.Type, request.Query.Text = "SQL", "SELECT 1"
	request.Query.Federation = nil
	request.Query.ConnectionID = "chinook-sqlite"
	request.ExpectedBranchHead = createCommittedHead
	baseCatalog := `{"format":"datatug-demo-connections/v1","connections":[{"id":"chinook-sqlite","dataset":"chinook","storage":"sqlite","readiness":"public-api","source":"https://demodb.dev/ovdb/v1/databases/chinook","fixtureSha256":"7651ba378ac2fcd0dfc3c66fb101f7a7eed3ba39a612ec642b96e20702061f15","browserFixture":{"url":"https://chinook.demodb.dev/data/chinook.sqlite","bytes":1007616}}]}`
	for _, tc := range []struct {
		name, catalog string
		wantOK        bool
	}{
		{"pinned source", baseCatalog, true},
		{"different fixture", strings.Replace(baseCatalog, `"bytes":1007616`, `"bytes":12`, 1), false},
		{"different URL", strings.Replace(baseCatalog, `chinook.demodb.dev`, `private.example`, 1), false},
		{"missing connection", `{"format":"datatug-demo-connections/v1","connections":[]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &queryFake{snapshotFake: newSnapshotFake(t)}
			content := []byte(tc.catalog)
			oid := testGitBlobOID(content)
			fake.files[oid] = content
			for i := range fake.tree {
				if fake.tree[i].Path == "demo-project-1/connections/demo-db.json" {
					fake.tree[i].OID, fake.tree[i].Size = oid, int64(len(content))
				}
			}
			_, err := (&queryRepository{client: fake}).PrepareQuerySave(context.Background(), "demo-project-1", "datatug-demo-project", "work", request)
			if tc.wantOK && err != nil || !tc.wantOK && !errors.Is(err, facade4datatug.ErrGitHubQueryInvalid) {
				t.Fatalf("save validation error=%v", err)
			}
		})
	}
}

func TestQueryRepositoryReportsOnlyVerifiedProviderCommit(t *testing.T) {
	request := queryRequest()
	request.Query.ID, request.Query.Title = "customers", "Customers"
	request.ExpectedBranchHead = createCommittedHead
	for _, tc := range []struct {
		name    string
		corrupt func(*queryFake)
		want    error
	}{
		{"provider error", func(f *queryFake) { f.createdError = errors.New("provider failed") }, nil},
		{"wrong parent", func(f *queryFake) { f.newCommit.ParentOIDs = []string{queryTreeOID} }, ErrGitHubQueryCommitProof},
		{"extra foreign tree entry", func(f *queryFake) {
			f.newTree = append(f.newTree, githubauth4datatug.GitHubTreeEntry{Path: "other.txt", Type: "blob", OID: queryTreeOID, Mode: "100644", Size: 1})
		}, ErrGitHubQueryCommitProof},
		{"duplicate tree entry", func(f *queryFake) { f.newTree = append(f.newTree, f.newTree[0]) }, ErrGitHubQueryCommitProof},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &queryFake{snapshotFake: newSnapshotFake(t)}
			repo := &queryRepository{client: fake}
			plan, err := repo.PrepareQuerySave(context.Background(), "demo-project-1", "datatug-demo-project", "work", request)
			if err != nil {
				t.Fatal(err)
			}
			if tc.name != "provider error" {
				_, err = repo.CreateQueryCommit(context.Background(), "work", createCommittedHead, "Save DataTug query; DataTug-Operation: initial", plan)
				if err != nil {
					t.Fatal(err)
				}
			}
			tc.corrupt(fake)
			if tc.name == "provider error" {
				_, err = repo.CreateQueryCommit(context.Background(), "work", createCommittedHead, "message", plan)
				if err == nil || errors.Is(err, ErrGitHubQueryCommitProof) {
					t.Fatalf("provider error became success/proof error: %v", err)
				}
				return
			}
			if _, err = repo.FindQueryCommit(context.Background(), "work", createCommittedHead, "initial", plan); !errors.Is(err, tc.want) {
				t.Fatalf("unverified commit accepted: %v", err)
			}
		})
	}
}

func TestQueryRepositoryDeniesMissingActorProviderAndUnprovableReceipt(t *testing.T) {
	ctx := context.Background()
	if _, err := AuthorizeQueryRepository(ctx, nil, "actor", 123, "owner", "repo"); !errors.Is(err, githubauth4datatug.ErrGitHubAppNotConfigured) {
		t.Fatalf("missing App provider: %v", err)
	}
	if got := (*queryRepository)(nil).Scope(); got != (facade4datatug.GitHubCreateRepositoryScope{}) {
		t.Fatalf("nil repository acquired scope: %+v", got)
	}
	fake := &queryFake{snapshotFake: newSnapshotFake(t)}
	repo := &queryRepository{client: fake}
	if _, err := repo.CreateQueryCommit(ctx, "work", createCommittedHead, "marker", nil); !errors.Is(err, ErrGitHubQueryCommitProof) {
		t.Fatalf("nil mutation plan: %v", err)
	}
	if _, err := repo.FindQueryCommit(ctx, "work", createCommittedHead, "not-present", &facade4datatug.GitHubQuerySavePlan{Changes: []facade4datatug.GitHubQueryFileChange{{Path: "demo-project-1/queries/q.query.json"}}}); err != nil {
		t.Fatalf("no matching commit should permit safe first attempt: %v", err)
	}
}

func TestQueryRepositoryUpdatesBodyTypeAndProvesSidecarDeletionAtomically(t *testing.T) {
	ctx := context.Background()
	fake := &queryFake{snapshotFake: newSnapshotFake(t)}
	for name, content := range map[string][]byte{
		"demo-project-1/queries/customers.query.json": []byte(`{"id":"customers","title":"Customers","type":"SQL"}`),
		"demo-project-1/queries/customers.query.sql":  []byte("SELECT 1"),
	} {
		oid := testGitBlobOID(content)
		fake.files[oid] = content
		fake.tree = append(fake.tree, githubauth4datatug.GitHubTreeEntry{Path: name, Type: "blob", Mode: "100644", OID: oid, Size: int64(len(content))})
	}
	snapshot, err := OpenProjectSnapshot(ctx, fake, "demo-project-1", "work")
	if err != nil {
		t.Fatal(err)
	}
	read, err := snapshot.ReadQueryRevision(ctx, "~", "customers")
	if err != nil || !read.SaveSupported {
		t.Fatalf("existing query revision %+v %v", read, err)
	}
	request := queryRequest()
	request.Query.ID, request.Query.Title = "customers", "Customers"
	request.Query.Text = "SELECT CustomerId FROM chinook.Customer"
	request.ExpectedBranchHead = createCommittedHead
	request.IfNoneMatch = false
	request.IfMatch = read.Revision
	repo := &queryRepository{client: fake}
	plan, err := repo.PrepareQuerySave(ctx, "demo-project-1", "datatug-demo-project", "work", request)
	if err != nil {
		t.Fatal(err)
	}
	var deletedOld, addedBody bool
	for _, change := range plan.Changes {
		deletedOld = deletedOld || change.Delete && strings.HasSuffix(change.Path, "customers.query.sql")
		addedBody = addedBody || !change.Delete && strings.HasSuffix(change.Path, "customers.query.dtql")
	}
	if !deletedOld || !addedBody {
		t.Fatalf("type transition did not include exact delete and add: %+v", plan.Changes)
	}
	message := "Save DataTug query; DataTug-Operation: type-transition"
	if head, err := repo.CreateQueryCommit(ctx, "work", createCommittedHead, message, plan); err != nil || head != queryCommitOID {
		t.Fatalf("atomic pair update head=%q err=%v", head, err)
	}
	if head, err := repo.FindQueryCommit(ctx, "work", createCommittedHead, "type-transition", plan); err != nil || head != queryCommitOID {
		t.Fatalf("sidecar deletion was not proved head=%q err=%v", head, err)
	}
}

func TestQueryRepositorySavesPairAtExpectedHeadAndRecoversExactCommit(t *testing.T) {
	fake := &queryFake{snapshotFake: newSnapshotFake(t)}
	repo := &queryRepository{client: fake}
	request := dto.SaveQueryRequest{ProjectRef: dto.ProjectRef{StoreID: "github.com", ProjectID: "repo@owner@demo-project-1"}, Branch: "work", ExpectedBranchHead: createCommittedHead, OperationID: "save-1", IfNoneMatch: true, Query: datatug.QueryDefWithFolderPath{FolderPath: "~", QueryDef: datatug.QueryDef{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "new-query", Title: "New query"}}, Type: "DTQL", Text: "SELECT CustomerId FROM chinook.Customer"}}}
	plan, err := repo.PrepareQuerySave(context.Background(), "demo-project-1", "datatug-demo-project", "work", request)
	if err != nil || len(plan.Changes) != 2 || plan.Response.Revision == "" || !plan.BodyChanged {
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
