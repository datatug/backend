package githubstore4datatug

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/githubauth4datatug"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

const maxQueryCommitAncestry = 64

var ErrGitHubQueryCommitProof = errors.New("GitHub query commit could not be proven")

type scopedQueryClient interface {
	scopedCreateClient
	GetBlob(context.Context, string) ([]byte, error)
}

type queryRepository struct{ client scopedQueryClient }

var _ facade4datatug.GitHubQueryRepository = (*queryRepository)(nil)

func AuthorizeQueryRepository(ctx context.Context, provider *githubauth4datatug.Provider, firebaseUID string, repositoryID int64, owner, name string) (facade4datatug.GitHubQueryRepository, error) {
	if provider == nil {
		return nil, githubauth4datatug.ErrGitHubAppNotConfigured
	}
	client, err := provider.AuthorizeRepository(ctx, firebaseUID, githubauth4datatug.RepositoryRef{ID: repositoryID, Owner: owner, Name: name}, githubauth4datatug.RepositoryWrite)
	if err != nil {
		return nil, err
	}
	return &queryRepository{client: client}, nil
}

func (r *queryRepository) Scope() facade4datatug.GitHubCreateRepositoryScope {
	if r == nil || r.client == nil {
		return facade4datatug.GitHubCreateRepositoryScope{}
	}
	s := r.client.Scope()
	return facade4datatug.GitHubCreateRepositoryScope{ActorID: s.FirebaseUID, RepositoryID: s.Repository.ID, Owner: s.Repository.Owner, Name: s.Repository.Name, Permission: string(s.Permission)}
}

func (r *queryRepository) CurrentHead(ctx context.Context, branch string) (string, error) {
	ref, err := r.client.GetRef(ctx, branch)
	if err != nil || ref.Name != branch || ref.OID == "" {
		return "", ErrGitHubQueryCommitProof
	}
	return ref.OID, nil
}

func (r *queryRepository) PrepareQuerySave(ctx context.Context, folder, projectID, branch string, request dto.SaveQueryRequest) (*facade4datatug.GitHubQuerySavePlan, error) {
	if request.Branch != branch || request.ExpectedBranchHead == "" {
		return nil, facade4datatug.ErrGitHubQueryInvalid
	}
	snapshot, err := OpenProjectSnapshotAtHead(ctx, r.client, folder, branch, request.ExpectedBranchHead)
	if err != nil {
		return nil, err
	}
	summary, err := snapshot.ProjectSummary(ctx)
	if err != nil {
		return nil, err
	}
	var identity struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(summary, &identity) != nil || identity.ID != projectID {
		return nil, ErrInvalidProjectSnapshot
	}
	query := request.Query
	if query.ConnectionID != "" {
		if err := validateBrowserSqliteConnection(ctx, snapshot, query.QueryDef); err != nil {
			return nil, err
		}
	}
	queryFolder := query.FolderPath
	if queryFolder == "~" {
		queryFolder = ""
	}
	queryDir := "queries"
	if queryFolder != "" {
		queryDir += "/" + queryFolder
	}
	prefix := queryDir + "/" + query.ID
	if _, legacy := snapshot.files[prefix+".sql.json"]; legacy {
		return nil, ErrUnsupportedExistingQuery
	}
	selected := make(map[string][]byte)
	for name := range snapshot.files {
		if strings.HasPrefix(name, prefix+".query.") {
			content, err := snapshot.ReadFile(ctx, name)
			if err != nil {
				return nil, err
			}
			selected[name] = content
		}
	}
	preview, err := PreviewQueryMutation(ctx, request, selected)
	if err != nil {
		return nil, err
	}
	plan := &facade4datatug.GitHubQuerySavePlan{Changes: make([]facade4datatug.GitHubQueryFileChange, 0, len(preview.Changes)), ExpectedTree: make(map[string]facade4datatug.GitHubQueryExpectedFile, len(snapshot.allFiles)), Response: preview.Response}
	for name, entry := range snapshot.allFiles {
		plan.ExpectedTree[name] = facade4datatug.GitHubQueryExpectedFile{OID: entry.OID, Mode: entry.Mode, Size: entry.Size}
	}
	for _, change := range preview.Changes {
		plan.Changes = append(plan.Changes, facade4datatug.GitHubQueryFileChange{Path: folder + "/" + change.Path, Content: append([]byte(nil), change.Content...), Delete: change.Delete})
	}
	return plan, nil
}

func validateBrowserSqliteConnection(ctx context.Context, snapshot *ProjectSnapshot, query datatug.QueryDef) error {
	if query.Type != "SQL" || query.Federation != nil || len(query.Parameters) != 0 || query.ConnectionID != "chinook-sqlite" {
		return facade4datatug.ErrGitHubQueryInvalid
	}
	content, err := snapshot.ReadFile(ctx, "connections/demo-db.json")
	if err != nil || len(content) > 256<<10 {
		return facade4datatug.ErrGitHubQueryInvalid
	}
	var catalog struct {
		Format      string `json:"format"`
		Connections []struct {
			ID             string `json:"id"`
			Dataset        string `json:"dataset"`
			Storage        string `json:"storage"`
			Readiness      string `json:"readiness"`
			Source         string `json:"source"`
			FixtureSHA256  string `json:"fixtureSha256"`
			BrowserFixture struct {
				URL   string `json:"url"`
				Bytes int64  `json:"bytes"`
			} `json:"browserFixture"`
		} `json:"connections"`
	}
	if json.Unmarshal(content, &catalog) != nil || catalog.Format != datatug.ConnectionCatalogFormat {
		return facade4datatug.ErrGitHubQueryInvalid
	}
	count := 0
	for _, connection := range catalog.Connections {
		if connection.ID != query.ConnectionID {
			continue
		}
		count++
		if connection.Dataset != "chinook" || connection.Storage != "sqlite" || connection.Readiness != "public-api" ||
			connection.Source != "https://demodb.dev/ovdb/v1/databases/chinook" ||
			connection.FixtureSHA256 != "7651ba378ac2fcd0dfc3c66fb101f7a7eed3ba39a612ec642b96e20702061f15" ||
			connection.BrowserFixture.URL != "https://chinook.demodb.dev/data/chinook.sqlite" || connection.BrowserFixture.Bytes != 1007616 {
			return facade4datatug.ErrGitHubQueryInvalid
		}
	}
	if count != 1 {
		return facade4datatug.ErrGitHubQueryInvalid
	}
	return nil
}

func (r *queryRepository) CreateQueryCommit(ctx context.Context, branch, expectedHead, message string, plan *facade4datatug.GitHubQuerySavePlan) (string, error) {
	if plan == nil || len(plan.Changes) == 0 {
		return "", ErrGitHubQueryCommitProof
	}
	changes := make([]githubauth4datatug.GitHubFileChange, 0, len(plan.Changes))
	for _, change := range plan.Changes {
		changes = append(changes, githubauth4datatug.GitHubFileChange{Path: change.Path, Content: append([]byte(nil), change.Content...), Delete: change.Delete})
	}
	created, err := r.client.CreateCommitOnBranch(ctx, branch, expectedHead, message, changes)
	if err != nil {
		return "", err
	}
	commit, err := r.client.GetCommit(ctx, created.OID)
	if err != nil || commit.OID != created.OID || len(commit.ParentOIDs) != 1 || !strings.EqualFold(commit.ParentOIDs[0], expectedHead) || commit.Message != message || r.verifyQueryCommit(ctx, commit, plan) != nil {
		return "", ErrGitHubQueryCommitProof
	}
	return commit.OID, nil
}

func (r *queryRepository) FindQueryCommit(ctx context.Context, branch, expectedHead, marker string, plan *facade4datatug.GitHubQuerySavePlan) (string, error) {
	head, err := r.CurrentHead(ctx, branch)
	if err != nil {
		return "", err
	}
	queue := []string{head}
	seen := map[string]bool{}
	for len(queue) > 0 && len(seen) < maxQueryCommitAncestry {
		oid := queue[0]
		queue = queue[1:]
		if seen[oid] || strings.EqualFold(oid, expectedHead) {
			continue
		}
		seen[oid] = true
		commit, err := r.client.GetCommit(ctx, oid)
		if err != nil || !strings.EqualFold(commit.OID, oid) {
			return "", ErrGitHubQueryCommitProof
		}
		if commit.Message == "Save DataTug query; DataTug-Operation: "+marker {
			if len(commit.ParentOIDs) != 1 || !strings.EqualFold(commit.ParentOIDs[0], expectedHead) || r.verifyQueryCommit(ctx, commit, plan) != nil {
				return "", ErrGitHubQueryCommitProof
			}
			return commit.OID, nil
		}
		queue = append(queue, commit.ParentOIDs...)
	}
	if len(queue) > 0 {
		return "", ErrGitHubQueryCommitProof
	}
	return "", nil
}

func (r *queryRepository) verifyQueryCommit(ctx context.Context, commit githubauth4datatug.GitHubCommit, plan *facade4datatug.GitHubQuerySavePlan) error {
	if plan == nil || len(plan.Changes) == 0 {
		return ErrGitHubQueryCommitProof
	}
	tree, err := r.client.GetTree(ctx, commit.TreeOID)
	if err != nil {
		return ErrGitHubQueryCommitProof
	}
	actual := make(map[string]githubauth4datatug.GitHubTreeEntry, len(tree))
	for _, entry := range tree {
		if entry.Type == "tree" {
			continue
		}
		if _, duplicate := actual[entry.Path]; duplicate {
			return ErrGitHubQueryCommitProof
		}
		actual[entry.Path] = entry
	}
	expected := make(map[string]facade4datatug.GitHubQueryExpectedFile, len(plan.ExpectedTree)+len(plan.Changes))
	for name, file := range plan.ExpectedTree {
		expected[name] = file
	}
	for _, change := range plan.Changes {
		if change.Delete {
			delete(expected, change.Path)
			continue
		}
		entry, exists := actual[change.Path]
		if !exists || entry.Type != "blob" || entry.Mode != "100644" || entry.Size != int64(len(change.Content)) || !gitBlobOIDMatches(entry.OID, change.Content) {
			return ErrGitHubQueryCommitProof
		}
		expected[change.Path] = facade4datatug.GitHubQueryExpectedFile{OID: entry.OID, Mode: entry.Mode, Size: entry.Size}
	}
	if len(actual) != len(expected) {
		return ErrGitHubQueryCommitProof
	}
	for name, file := range expected {
		entry, exists := actual[name]
		if !exists || !strings.EqualFold(entry.OID, file.OID) || entry.Mode != file.Mode || entry.Size != file.Size {
			return ErrGitHubQueryCommitProof
		}
	}
	return nil
}
