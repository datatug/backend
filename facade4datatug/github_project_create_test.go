package facade4datatug

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/datatug/backend/template4datatug"
)

const (
	createBaseHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	createNewHead  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type fakeGitHubCreateRepo struct {
	scope           GitHubCreateRepositoryScope
	head            string
	committedMarker string
	files           map[string][]byte
	commitCount     int
	responseLost    bool
	folderOccupied  bool
	failBeforeWrite bool
}

func (f *fakeGitHubCreateRepo) Scope() GitHubCreateRepositoryScope { return f.scope }
func (f *fakeGitHubCreateRepo) CurrentHead(_ context.Context, _ string) (string, error) {
	return f.head, nil
}
func (f *fakeGitHubCreateRepo) EnsureFolderEmpty(_ context.Context, _, _ string) error {
	if f.folderOccupied {
		return ErrGitHubProjectConflict
	}
	return nil
}
func (f *fakeGitHubCreateRepo) CreateFilesCommit(_ context.Context, _, expectedHead, message string, files map[string][]byte) (string, error) {
	if f.head != expectedHead {
		return "", ErrGitHubProjectConflict
	}
	if f.failBeforeWrite {
		return "", errors.New("provider unavailable before write")
	}
	f.commitCount++
	f.head = createNewHead
	f.committedMarker = strings.TrimPrefix(message[strings.LastIndex(message, "DataTug-Operation: "):], "DataTug-Operation: ")
	f.files = files
	if f.responseLost {
		return "", errors.New("provider response lost")
	}
	return f.head, nil
}

func TestGitHubCreateUncertainOutcomeKeepsHiddenReservationForExactRetry(t *testing.T) {
	db, service, _ := paidCreateFixture(t)
	repo := githubRepo()
	repo.failBeforeWrite = true
	command := githubCommand()
	if _, err := service.CreateGitHubProject(context.Background(), command, repo); !errors.Is(err, ErrGitHubOutcomeUncertain) {
		t.Fatalf("provider failure: %v", err)
	}
	if quota := paidQuota(t, db); quota.Allocated != 1 {
		t.Fatalf("uncertain outcome released reservation: %+v", quota)
	}
	opRecord, op := models4datatug.NewGitHubProjectCreateOperationRecord("actor", "op")
	if err := db.Get(context.Background(), opRecord); err != nil || op.Status != models4datatug.GitHubProjectInitializing {
		t.Fatalf("intent %+v: %v", op, err)
	}
	pr, project := models4datatug.NewSharedLinkedProjectRecord("space", op.ProjectID)
	if err := db.Get(context.Background(), pr); err != nil || project.Status != models4datatug.GitHubProjectInitializing {
		t.Fatalf("pending project %+v: %v", project, err)
	}
	indexRecord, _ := models4datatug.NewUserExtRecord("actor", "datatug")
	if err := db.Get(context.Background(), indexRecord); !record.IsNotFound(err) {
		t.Fatalf("pending project entered user index: %v", err)
	}
	repo.failBeforeWrite = false
	result, err := service.CreateGitHubProject(context.Background(), command, repo)
	if err != nil || result.SharedProjectID != op.ProjectID || repo.commitCount != 1 || paidQuota(t, db).Allocated != 1 {
		t.Fatalf("exact retry %+v: %v commits=%d", result, err, repo.commitCount)
	}
}
func (f *fakeGitHubCreateRepo) FindCommitByMarker(_ context.Context, _, expectedHead, marker string, files map[string][]byte) (string, error) {
	if expectedHead == createBaseHead && f.committedMarker == marker && len(files) == len(f.files) {
		return createNewHead, nil
	}
	return "", nil
}

func githubCommand() GitHubProjectCreateCommand {
	return GitHubProjectCreateCommand{
		ActorID: "actor", SpaceID: "space", OperationID: "op", Title: "Customer queries",
		Source: models4datatug.GitHubCreateSource{
			Binding: models4datatug.GitHubProjectBinding{
				RepositoryID: 123, Owner: "owner", Name: "repo", Folder: "datatug", Branch: "feature/work",
			},
			ExpectedHead: createBaseHead, TemplateID: template4datatug.DemoProjectID, TemplateCommit: template4datatug.DemoProjectCommit,
		},
	}
}

func githubRepo() *fakeGitHubCreateRepo {
	return &fakeGitHubCreateRepo{
		head:  createBaseHead,
		scope: GitHubCreateRepositoryScope{ActorID: "actor", RepositoryID: 123, Owner: "owner", Name: "repo", Permission: "write"},
	}
}

func TestGitHubCreateReservesBeforeCommitAndReconcilesLostResponse(t *testing.T) {
	db, service, _ := paidCreateFixture(t)
	repo := githubRepo()
	repo.responseLost = true
	command := githubCommand()
	result, err := service.CreateGitHubProject(context.Background(), command, repo)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "repo@owner@datatug" || result.BranchHead != createNewHead || result.TemplateCommit != template4datatug.DemoProjectCommit || repo.commitCount != 1 || len(repo.files) != 631 {
		t.Fatalf("unexpected result %+v commits=%d files=%d", result, repo.commitCount, len(repo.files))
	}
	if quota := paidQuota(t, db); quota.Allocated != 1 {
		t.Fatalf("quota %+v", quota)
	}
	pr, project := models4datatug.NewSharedLinkedProjectRecord("space", result.SharedProjectID)
	if err := db.Get(context.Background(), pr); err != nil || project.Status != models4datatug.GitHubProjectReady || project.Storage != models4datatug.GithubStoreID || project.GitHub.RepositoryID != 123 {
		t.Fatalf("project %+v: %v", project, err)
	}
	indexRecord, index := models4datatug.NewUserExtRecord("actor", "datatug")
	if err := db.Get(context.Background(), indexRecord); err != nil {
		t.Fatal(err)
	}
	brief := index.Stores[models4datatug.GithubStoreID].Projects[result.ID]
	if brief == nil || brief.ProjectAPI != "cloud" || brief.Branch != "feature/work" {
		t.Fatalf("index brief %+v", brief)
	}
	again, err := service.CreateGitHubProject(context.Background(), command, repo)
	if err != nil || again != result || repo.commitCount != 1 || paidQuota(t, db).Allocated != 1 {
		t.Fatalf("replay %+v %v commits=%d", again, err, repo.commitCount)
	}
	command.Title = "Different content"
	if _, err := service.CreateGitHubProject(context.Background(), command, repo); !errors.Is(err, ErrGitHubProjectConflict) {
		t.Fatalf("same operation changed payload: %v", err)
	}
}

func TestGitHubCreateRejectsUnwritableRepoBeforeQuota(t *testing.T) {
	db, service, _ := paidCreateFixture(t)
	repo := githubRepo()
	repo.scope.Permission = "read"
	if _, err := service.CreateGitHubProject(context.Background(), githubCommand(), repo); !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatalf("read-only repository actor: %v", err)
	}
	if paidQuota(t, db).Allocated != 0 || repo.commitCount != 0 {
		t.Fatal("read-only actor changed quota or repository")
	}
	repo.scope.Permission = "write"
	repo.folderOccupied = true
	if _, err := service.CreateGitHubProject(context.Background(), githubCommand(), repo); !errors.Is(err, ErrGitHubProjectConflict) {
		t.Fatalf("occupied folder: %v", err)
	}
	if paidQuota(t, db).Allocated != 0 {
		t.Fatal("occupied folder reserved quota")
	}
	opRecord, _ := models4datatug.NewGitHubProjectCreateOperationRecord("actor", "op")
	if err := db.Get(context.Background(), record.NewRecordWithData(opRecord.Key(), new(models4datatug.GitHubProjectCreateOperation))); !record.IsNotFound(err) {
		t.Fatalf("unpaid operation leaked: %v", err)
	}
}
