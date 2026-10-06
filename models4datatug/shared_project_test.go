package models4datatug

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSharedProjectPathsAndSafePayload(t *testing.T) {
	projectRecord, project := NewSharedProjectRecord("space-A", "project-A")
	if got := projectRecord.Key().String(); got != "spaces/space-A/ext/datatug/projects/project-A" {
		t.Fatal(got)
	}
	project.Title, project.Access = "Shared", AccessProtected
	b, err := json.Marshal(project)
	if err != nil || strings.Contains(string(b), "userIDs") || string(b) != `{"title":"Shared","access":"protected"}` {
		t.Fatalf("payload %s, err %v", b, err)
	}
	a, _ := NewSharedProjectCreateReceiptRecord("space-A", "command-A")
	bRecord, _ := NewSharedProjectCreateReceiptRecord("space-B", "command-A")
	if !strings.HasPrefix(a.Key().String(), "spaces/space-A/ext/datatug/projectCreates/") || a.Key().String() == bRecord.Key().String() || strings.HasSuffix(a.Key().String(), "command-A") {
		t.Fatalf("receipt keys %s / %s", a.Key(), bRecord.Key())
	}
}

func TestSharedProjectReceiptIntegrity(t *testing.T) {
	r := SharedProjectCreateReceipt{Version: 1, ActorID: "actor", SpaceID: "space", CommandID: "command", Title: "Title", ProjectID: "project", CreatedAt: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	r.RequestDigest = SharedProjectCreateDigest(r.ActorID, r.SpaceID, r.CommandID, r.Title)
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*SharedProjectCreateReceipt){
		func(v *SharedProjectCreateReceipt) { v.Version = 2 },
		func(v *SharedProjectCreateReceipt) { v.ActorID = "other" },
		func(v *SharedProjectCreateReceipt) { v.SpaceID = "other" },
		func(v *SharedProjectCreateReceipt) { v.CommandID = "other" },
		func(v *SharedProjectCreateReceipt) { v.Title = "other" },
		func(v *SharedProjectCreateReceipt) { v.ProjectID = "../escape" },
		func(v *SharedProjectCreateReceipt) { v.CreatedAt = time.Time{} },
	} {
		copy := r
		mutate(&copy)
		if err := copy.Validate(); err == nil {
			t.Fatalf("accepted corrupt receipt %+v", copy)
		}
	}
}

func TestSharedProjectBoundedInput(t *testing.T) {
	for _, id := range []string{"", ".", "..", "a/b", "a%2Fb", " a", "a\x00", strings.Repeat("x", 129)} {
		if err := ValidateSharedProjectIdentifier(id); err == nil {
			t.Fatalf("accepted unsafe id %q", id)
		}
	}
	for _, title := range []string{"", " ", " title", "title\n", "x\x00", string([]byte{0xff}), strings.Repeat("界", 201)} {
		if err := ValidateSharedProjectTitle(title); err == nil {
			t.Fatalf("accepted title %q", title)
		}
	}
}
