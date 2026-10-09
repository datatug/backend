package models4datatug

import (
	"strings"
	"testing"
	"time"
)

func TestQueryEditCandidateKeepsServerEventSpaceBoundedAndUnclassified(t *testing.T) {
	id := NewQueryEditCandidateID("actor", "op")
	candidate := QueryEditCandidate{Version: 1, SourceID: GitHubQueryEditSourceID, EventID: id, ActorID: "actor", OperationID: "op", SpaceID: "space", ProjectID: "project", RepositoryID: 123, Folder: "project", ExpectedHead: strings.Repeat("a", 40), QueryPath: "customers", BodyChanged: true, Classification: QueryEditUnclassified, AcceptedAtUTC: time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)}
	if err := candidate.Validate(); err != nil {
		t.Fatal(err)
	}
	a, _ := NewQueryEditCandidateRecord("space", "actor", "op")
	b, _ := NewQueryEditCandidateRecord("other-space", "actor", "op")
	if a.Key().String() == b.Key().String() || !strings.Contains(a.Key().String(), "spaces") || !strings.Contains(a.Key().String(), QueryEditCandidateCollection) {
		t.Fatalf("candidate key is not Space bounded: %s %s", a.Key(), b.Key())
	}
	candidate.BodyChanged = false
	if err := candidate.Validate(); err == nil {
		t.Fatal("unchanged body acquired an edit candidate")
	}
	candidate.BodyChanged = true
	candidate.EventID = NewQueryEditCandidateID("another-actor", "op")
	if err := candidate.Validate(); err == nil {
		t.Fatal("foreign actor event acquired this candidate")
	}
}
