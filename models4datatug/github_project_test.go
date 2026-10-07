package models4datatug

import (
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
