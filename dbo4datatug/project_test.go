package dbo4datatug

import (
	"encoding/json"
	"testing"
)

func TestProjectValidate(t *testing.T) {
	if err := (&Project{Title: "My project"}).Validate(); err != nil {
		t.Errorf("Validate() rejected a titled project: %v", err)
	}
	if err := (&Project{}).Validate(); err == nil {
		t.Error("Validate() must reject a project without a title")
	}
}

func TestUserExtValidate(t *testing.T) {
	var nilExt *UserExt
	if err := nilExt.Validate(); err == nil {
		t.Error("Validate() must reject a nil user ext record")
	}
	if err := (&UserExt{}).Validate(); err != nil {
		t.Errorf("Validate() rejected an empty (new) user ext record: %v", err)
	}
	valid := &UserExt{Stores: map[string]*StoreBrief{
		FirestoreStoreType: {
			Title: FirestoreStoreTitle,
			Type:  FirestoreStoreType,
			Projects: map[string]*ProjectBrief{
				"proj1234": {Title: "My project", Access: AccessPrivate},
			},
		},
	}}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() rejected a valid user ext record: %v", err)
	}
	invalid := &UserExt{Stores: map[string]*StoreBrief{FirestoreStoreType: nil}}
	if err := invalid.Validate(); err == nil {
		t.Error("Validate() must reject a null store brief")
	}
}

// The JSON tags are the wire contract with the datatug-apps client
// (IDatatugStoreBrief / IProjectBrief in libs/datatug/main models/interfaces.ts)
// and the firestore tags are what the cloud database stores, so both are pinned
// here: renaming either silently breaks project listing in the app.
func TestStoreBriefJSONContract(t *testing.T) {
	ext := UserExt{Stores: map[string]*StoreBrief{
		FirestoreStoreType: {
			Title: FirestoreStoreTitle,
			Type:  FirestoreStoreType,
			Projects: map[string]*ProjectBrief{
				"proj1234": {Title: "My project", Access: AccessPrivate},
			},
		},
	}}
	encoded, err := json.Marshal(ext)
	if err != nil {
		t.Fatalf("failed to marshal user ext record: %v", err)
	}
	const want = `{"stores":{"firestore":{"title":"DataTug cloud","type":"firestore",` +
		`"projects":{"proj1234":{"title":"My project","access":"private"}}}}}`
	if string(encoded) != want {
		t.Errorf("user ext record JSON =\n%s\nwant\n%s", encoded, want)
	}
}
