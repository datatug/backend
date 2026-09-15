package models4datatug

import (
	"encoding/json"
	"testing"
)

func TestProjectKeyAndRecord(t *testing.T) {
	record, project := NewProjectRecord("proj1234")
	if got := record.Key().String(); got != ProjectsCollection+"/proj1234" {
		t.Errorf("project key = %q, want %q", got, ProjectsCollection+"/proj1234")
	}
	if record.Data() != project {
		t.Error("NewProjectRecord must return the DBO it wrapped in the record")
	}
}

func TestUserExtRecord(t *testing.T) {
	record, userExt := NewUserExtRecord("user1", "datatug")
	want := UsersCollection + "/user1/" + UserExtCollection + "/datatug"
	if got := record.Key().String(); got != want {
		t.Errorf("user ext key = %q, want %q", got, want)
	}
	if record.Data() != userExt {
		t.Error("NewUserExtRecord must return the DBO it wrapped in the record")
	}
}

// The json tags are the wire contract with the datatug-apps client
// (IDatatugStoreBrief / IProjectBrief in libs/datatug/main models/interfaces.ts)
// and the firestore tags are what the cloud database stores. Both are pinned
// here: renaming either silently breaks project listing in the app.
func TestUserExtJSONContract(t *testing.T) {
	userExt := UserExt{Stores: map[string]*StoreBrief{
		FirestoreStoreID: {
			Title: FirestoreStoreTitle,
			Type:  FirestoreStoreID,
			Projects: map[string]*ProjectBrief{
				"proj1234": {Title: "My project", Access: AccessPrivate},
			},
		},
	}}
	encoded, err := json.Marshal(userExt)
	if err != nil {
		t.Fatalf("failed to marshal the user index: %v", err)
	}
	const want = `{"stores":{"firestore":{"title":"DataTug cloud","type":"firestore",` +
		`"projects":{"proj1234":{"title":"My project","access":"private"}}}}}`
	if string(encoded) != want {
		t.Errorf("user index JSON =\n%s\nwant\n%s", encoded, want)
	}
}

func TestProjectJSONContract(t *testing.T) {
	project := Project{Title: "My project", Access: AccessPrivate, UserIDs: []string{"user1"}}
	encoded, err := json.Marshal(project)
	if err != nil {
		t.Fatalf("failed to marshal the project: %v", err)
	}
	const want = `{"title":"My project","access":"private","userIDs":["user1"]}`
	if string(encoded) != want {
		t.Errorf("project JSON =\n%s\nwant\n%s", encoded, want)
	}
}
