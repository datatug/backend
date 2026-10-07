package models4datatug

import (
	"strings"
	"testing"
	"time"
)

func TestGitHubLocatorIsUniqueByImmutableRepositoryAndFolder(t *testing.T) {
	a, _ := NewGitHubProjectLocatorRecord(123, "team/datatug")
	b, _ := NewGitHubProjectLocatorRecord(123, "team/datatug")
	c, _ := NewGitHubProjectLocatorRecord(124, "team/datatug")
	d, _ := NewGitHubProjectLocatorRecord(123, "team/other")
	if a.Key().String() != b.Key().String() || a.Key().String() == c.Key().String() || a.Key().String() == d.Key().String() {
		t.Fatal("locator key did not bind immutable repository and folder")
	}
}

func TestGitHubQueryOperationIDIsActorWideAndPrivateKeySafe(t *testing.T) {
	a, _ := NewGitHubQueryOperationRecord("actor", "retry-id")
	b, _ := NewGitHubQueryOperationRecord("actor", "retry-id")
	c, _ := NewGitHubQueryOperationRecord("actor", "another-id")
	d, _ := NewGitHubQueryOperationRecord("other-actor", "retry-id")
	if a.Key().String() != b.Key().String() || a.Key().String() == c.Key().String() || a.Key().String() == d.Key().String() {
		t.Fatal("query operation ID was not actor-wide")
	}
	malicious, _ := NewGitHubQueryOperationRecord("actor", "line\nsecret")
	if strings.Contains(malicious.Key().String(), "secret") || strings.Contains(malicious.Key().String(), "\n") {
		t.Fatal("attacker controlled operation ID escaped into storage key")
	}
	op := GitHubQueryOperation{Version: 1, ActorID: "actor", OperationID: "retry-id", RequestDigest: "digest", RepositoryID: 123, Folder: "datatug", Branch: "work", ExpectedHead: "head", QueryPath: "queries/q", CreatedAt: time.Now().UTC()}
	if err := op.Validate(); err != nil {
		t.Fatal(err)
	}
	op.RepositoryID = 0
	if err := op.Validate(); err == nil {
		t.Fatal("operation with no immutable repository accepted")
	}
	op.RepositoryID = 123
	op.CreatedAt = time.Time{}
	if err := op.Validate(); err == nil {
		t.Fatal("operation with no creation time accepted")
	}
}

func TestGitHubBindingAndLocatorRejectAmbiguousPaths(t *testing.T) {
	binding := GitHubProjectBinding{RepositoryID: 123, Owner: "owner", Name: "repo", Folder: "team/project", Branch: "feature/work"}
	if err := binding.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{"../secret", "team//project", "team/@other", "team/.", "team/.."} {
		binding.Folder = folder
		if err := binding.Validate(); err == nil {
			t.Fatalf("ambiguous folder %q accepted", folder)
		}
	}
	binding.Folder = "team/project"
	binding.Branch = "work\nwrong"
	if err := binding.Validate(); err == nil {
		t.Fatal("branch with newline accepted")
	}
	locator := GitHubProjectLocator{Version: 1, RepositoryID: 123, Folder: "team/project", SpaceID: "space", ProjectID: "project", Status: GitHubProjectReady, CreatedAt: time.Now().UTC()}
	if err := locator.Validate(); err != nil {
		t.Fatal(err)
	}
	locator.Folder = "team/../other"
	if err := locator.Validate(); err == nil {
		t.Fatal("locator with ambiguous folder accepted")
	}
	locator.Folder = "team/project"
	locator.Status = "unknown"
	if err := locator.Validate(); err == nil {
		t.Fatal("unknown locator state accepted")
	}
	locator.Status = GitHubProjectReady
	locator.CreatedAt = time.Now().In(time.FixedZone("not UTC", 3600))
	if err := locator.Validate(); err == nil {
		t.Fatal("non-UTC locator timestamp accepted")
	}
}

func TestGitHubOperationRejectsChangedScopeAndUnfinishedReadyState(t *testing.T) {
	a, _ := NewGitHubProjectCreateOperationRecord("actor", "op")
	b, _ := NewGitHubProjectCreateOperationRecord("actor", "op")
	c, _ := NewGitHubProjectCreateOperationRecord("actor", "another")
	if a.Key().String() != b.Key().String() || a.Key().String() == c.Key().String() {
		t.Fatal("operation key did not bind actor and operation ID")
	}
	op := GitHubProjectCreateOperation{
		Version: 1, ActorID: "actor", OperationID: "op", SpaceID: "space", ProjectID: "project",
		RequestDigest: "digest", Binding: GitHubProjectBinding{RepositoryID: 123, Owner: "owner", Name: "repo", Folder: "team/datatug", Branch: "feature/work"},
		ExpectedHead: "abc", TemplateID: "demo-project-1", TemplateCommit: "commit", Status: GitHubProjectInitializing,
		CreatedAt: time.Date(2026, 10, 7, 14, 0, 0, 0, time.UTC),
	}
	if err := op.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := op.Match("actor", "op", "other-space", "digest"); err == nil {
		t.Fatal("reused operation ID with a different Space was accepted")
	}
	if err := op.Match("actor", "op", "space", "different-body"); err == nil {
		t.Fatal("reused operation ID with a different payload was accepted")
	}
	op.Status = GitHubProjectReady
	if err := op.Validate(); err == nil {
		t.Fatal("ready operation without provider commit was accepted")
	}
	op.CommittedHead = "def"
	if err := op.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubSharedReceiptBindsRepositoryBranchHeadAndTemplate(t *testing.T) {
	source := GitHubCreateSource{
		Binding:      GitHubProjectBinding{RepositoryID: 123, Owner: "owner", Name: "repo", Folder: "datatug", Branch: "feature/work"},
		ExpectedHead: "abc", TemplateID: "demo-project-1", TemplateCommit: "template-commit",
	}
	receipt := SharedProjectCreateReceipt{
		Version: 2, ActorID: "actor", SpaceID: "space", CommandID: "op", Title: "Queries", ProjectID: "project",
		PayerID: "payer", Mode: "live", Product: "datatug", GitHub: &source,
		CreatedAt:     time.Date(2026, 10, 7, 14, 0, 0, 0, time.UTC),
		RequestDigest: GitHubSharedProjectCreateDigest("actor", "space", "op", "Queries", "payer", "live", "datatug", source),
	}
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	receipt.GitHub.ExpectedHead = "other"
	if err := receipt.Validate(); err == nil {
		t.Fatal("receipt accepted a changed branch head")
	}
}
