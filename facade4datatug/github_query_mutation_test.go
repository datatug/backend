package facade4datatug

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/backend/models4datatug"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

const querySavedHead = "cccccccccccccccccccccccccccccccccccccccc"

type fakeGitHubQueryRepo struct {
	scope        GitHubCreateRepositoryScope
	head         string
	marker       string
	commits      int
	loseResponse bool
	findError    error
	commitError  error
	invalidPlan  bool
}

func (r *fakeGitHubQueryRepo) Scope() GitHubCreateRepositoryScope { return r.scope }
func (r *fakeGitHubQueryRepo) PrepareQuerySave(_ context.Context, folder, projectID, branch string, request dto.SaveQueryRequest) (*GitHubQuerySavePlan, error) {
	if r.invalidPlan {
		return &GitHubQuerySavePlan{}, nil
	}
	if folder != "datatug" || projectID == "" || branch != "feature/work" {
		return nil, ErrGitHubQueryInvalid
	}
	return &GitHubQuerySavePlan{Changes: []GitHubQueryFileChange{{Path: "datatug/queries/customers.query.json", Content: []byte(`{"id":"customers"}`)}, {Path: "datatug/queries/customers.query.dtql", Content: []byte(request.Query.Text)}}, Response: dto.SaveQueryResponse{Query: request.Query, Revision: "query-revision"}}, nil
}
func (r *fakeGitHubQueryRepo) CurrentHead(_ context.Context, _ string) (string, error) {
	return r.head, nil
}
func (r *fakeGitHubQueryRepo) CreateQueryCommit(_ context.Context, _, expectedHead, message string, _ *GitHubQuerySavePlan) (string, error) {
	if r.commitError != nil {
		return "", r.commitError
	}
	if r.head != expectedHead {
		return "", ErrGitHubQueryConflict
	}
	r.commits++
	r.head = querySavedHead
	r.marker = strings.TrimPrefix(message[strings.LastIndex(message, "DataTug-Operation: "):], "DataTug-Operation: ")
	if r.loseResponse {
		return "", errors.New("lost response")
	}
	return querySavedHead, nil
}
func (r *fakeGitHubQueryRepo) FindQueryCommit(_ context.Context, _, expectedHead, marker string, _ *GitHubQuerySavePlan) (string, error) {
	if r.findError != nil {
		return "", r.findError
	}
	if expectedHead == createNewHead && marker == r.marker && r.commits == 1 {
		return querySavedHead, nil
	}
	return "", nil
}

func TestGitHubQuerySaveRefusesUnprovableOutcomeWithoutDuplicateCommit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*fakeGitHubQueryRepo)
		want      error
		persisted bool
	}{
		{"invalid immutable plan", func(r *fakeGitHubQueryRepo) { r.invalidPlan = true }, ErrGitHubQueryInvalid, false},
		{"provider observation unavailable", func(r *fakeGitHubQueryRepo) { r.findError = errors.New("provider unavailable") }, ErrGitHubQueryOutcomeUncertain, true},
		{"commit rejected with no exact marker", func(r *fakeGitHubQueryRepo) { r.commitError = errors.New("provider rejected commit") }, ErrGitHubQueryOutcomeUncertain, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, service, _ := paidCreateFixture(t)
			if _, err := service.CreateGitHubProject(context.Background(), githubCommand(), githubRepo()); err != nil {
				t.Fatal(err)
			}
			repo := &fakeGitHubQueryRepo{scope: GitHubCreateRepositoryScope{ActorID: "actor", RepositoryID: 123, Owner: "owner", Name: "repo", Permission: "write"}, head: createNewHead}
			tc.mutate(repo)
			if _, err := service.SaveGitHubQuery(context.Background(), "actor", 123, "owner", "repo", "datatug", querySaveRequest(), repo); !errors.Is(err, tc.want) {
				t.Fatalf("unprovable result: %v", err)
			}
			if repo.commits != 0 {
				t.Fatal("unprovable operation mutated remote twice")
			}
			opRecord, _ := models4datatug.NewGitHubQueryOperationRecord("actor", "save-1")
			err := db.Get(context.Background(), opRecord)
			if (err == nil) != tc.persisted {
				t.Fatalf("intent custody persisted=%t err=%v", tc.persisted, err)
			}
		})
	}
}

func querySaveRequest() dto.SaveQueryRequest {
	return dto.SaveQueryRequest{ProjectRef: dto.ProjectRef{StoreID: models4datatug.GithubStoreID, ProjectID: "repo@owner@datatug"}, Branch: "feature/work", ExpectedBranchHead: createNewHead, OperationID: "save-1", IfNoneMatch: true, Query: datatug.QueryDefWithFolderPath{FolderPath: "~", QueryDef: datatug.QueryDef{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "customers", Title: "Customers"}}, Type: "DTQL", Text: "SELECT CustomerId FROM chinook.Customer"}}}
}

func TestGitHubQuerySaveRecoversResponseLossAndBindsActorWideOperation(t *testing.T) {
	db, service, _ := paidCreateFixture(t)
	created, err := service.CreateGitHubProject(context.Background(), githubCommand(), githubRepo())
	if err != nil || created.SharedProjectID == "" {
		t.Fatalf("create %+v %v", created, err)
	}
	repo := &fakeGitHubQueryRepo{scope: GitHubCreateRepositoryScope{ActorID: "actor", RepositoryID: 123, Owner: "owner", Name: "repo", Permission: "write"}, head: createNewHead, loseResponse: true}
	request := querySaveRequest()
	first, err := service.SaveGitHubQuery(context.Background(), "actor", 123, "owner", "repo", "datatug", request, repo)
	if err != nil || first.BranchHead != querySavedHead || first.Revision != "query-revision" || repo.commits != 1 {
		t.Fatalf("save %+v err=%v commits=%d", first, err, repo.commits)
	}
	again, err := service.SaveGitHubQuery(context.Background(), "actor", 123, "owner", "repo", "datatug", request, repo)
	if err != nil || again.BranchHead != first.BranchHead || repo.commits != 1 {
		t.Fatalf("replay %+v err=%v commits=%d", again, err, repo.commits)
	}
	request.Query.Text = "changed payload"
	if _, err := service.SaveGitHubQuery(context.Background(), "actor", 123, "owner", "repo", "datatug", request, repo); !errors.Is(err, ErrGitHubQueryConflict) {
		t.Fatalf("same id changed payload: %v", err)
	}
	opRecord, operation := models4datatug.NewGitHubQueryOperationRecord("actor", "save-1")
	if err := db.Get(context.Background(), opRecord); err != nil || operation.CommittedHead != querySavedHead {
		t.Fatalf("receipt %+v err=%v", operation, err)
	}
}

func TestGitHubQuerySaveRejectsReadOnlyActorBeforeIntent(t *testing.T) {
	db, service, _ := paidCreateFixture(t)
	if _, err := service.CreateGitHubProject(context.Background(), githubCommand(), githubRepo()); err != nil {
		t.Fatal(err)
	}
	repo := &fakeGitHubQueryRepo{scope: GitHubCreateRepositoryScope{ActorID: "actor", RepositoryID: 123, Owner: "owner", Name: "repo", Permission: "read"}, head: createNewHead}
	if _, err := service.SaveGitHubQuery(context.Background(), "actor", 123, "owner", "repo", "datatug", querySaveRequest(), repo); !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatalf("read-only actor: %v", err)
	}
	opRecord, _ := models4datatug.NewGitHubQueryOperationRecord("actor", "save-1")
	if err := db.Get(context.Background(), opRecord); err == nil {
		t.Fatal("read-only actor wrote intent")
	}
}

func TestGitHubQuerySaveStopsAfterPaidTermButRetainsPassiveRead(t *testing.T) {
	db, service, _ := paidCreateFixture(t)
	if _, err := service.CreateGitHubProject(context.Background(), githubCommand(), githubRepo()); err != nil {
		t.Fatal(err)
	}
	paidUpdate(t, db, models4datatug.NewCurrentPlanKey("personal-1"), "paidUntil", sharedTestTime)
	if _, err := service.ResolveGitHubProject(context.Background(), "actor", 123, "owner", "repo", "datatug"); err != nil {
		t.Fatalf("passive read denied after term: %v", err)
	}
	repo := &fakeGitHubQueryRepo{scope: GitHubCreateRepositoryScope{ActorID: "actor", RepositoryID: 123, Owner: "owner", Name: "repo", Permission: "write"}, head: createNewHead}
	if _, err := service.SaveGitHubQuery(context.Background(), "actor", 123, "owner", "repo", "datatug", querySaveRequest(), repo); !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatalf("ended plan write: %v", err)
	}
	opRecord, _ := models4datatug.NewGitHubQueryOperationRecord("actor", "save-1")
	if err := db.Get(context.Background(), opRecord); err == nil {
		t.Fatal("ended plan wrote intent")
	}
}

func TestGitHubQuerySaveReportsObservedStaleBranchWithoutCommit(t *testing.T) {
	_, service, _ := paidCreateFixture(t)
	if _, err := service.CreateGitHubProject(context.Background(), githubCommand(), githubRepo()); err != nil {
		t.Fatal(err)
	}
	repo := &fakeGitHubQueryRepo{scope: GitHubCreateRepositoryScope{ActorID: "actor", RepositoryID: 123, Owner: "owner", Name: "repo", Permission: "write"}, head: querySavedHead}
	if _, err := service.SaveGitHubQuery(context.Background(), "actor", 123, "owner", "repo", "datatug", querySaveRequest(), repo); !errors.Is(err, dto.ErrBranchHeadConflict) || repo.commits != 0 {
		t.Fatalf("stale branch err=%v commits=%d", err, repo.commits)
	}
}

func TestGitHubQueryReceiptReplayRechecksCurrentContact(t *testing.T) {
	db, service, _ := paidCreateFixture(t)
	if _, err := service.CreateGitHubProject(context.Background(), githubCommand(), githubRepo()); err != nil {
		t.Fatal(err)
	}
	repo := &fakeGitHubQueryRepo{scope: GitHubCreateRepositoryScope{ActorID: "actor", RepositoryID: 123, Owner: "owner", Name: "repo", Permission: "write"}, head: createNewHead}
	request := querySaveRequest()
	if _, err := service.SaveGitHubQuery(context.Background(), "actor", 123, "owner", "repo", "datatug", request, repo); err != nil {
		t.Fatal(err)
	}
	contact, _ := models4datatug.NewProjectContactLinkageRecord(contactFixtureRef("space", "owner-contact"))
	paidUpdate(t, db, contact.Key(), "active", false)
	if _, err := service.SaveGitHubQuery(context.Background(), "actor", 123, "owner", "repo", "datatug", request, repo); !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatalf("revoked contact replay: %v", err)
	}
	if repo.commits != 1 {
		t.Fatalf("revoked actor caused extra commit: %d", repo.commits)
	}
}
