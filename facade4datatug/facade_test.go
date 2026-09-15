package facade4datatug

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sneat-co/sneat-go-core/sneatcoretesting"

	"github.com/datatug/backend/const4datatug"
	"github.com/datatug/backend/models4datatug"
)

// fakeIDGenerator is a trivial IDGenerator fake — a one-method port needs no
// mocking framework.
type fakeIDGenerator struct {
	next string
	err  error
}

func (f fakeIDGenerator) NewID(context.Context) (string, error) { return f.next, f.err }

const testUserID = "user1"

// newTestFacade returns a Facade over a real in-memory dal-go database, so the
// commands are exercised through actual transactions and records — no Firestore
// emulator, no platform bootstrapping.
func newTestFacade(t *testing.T, projectID string) Facade {
	t.Helper()
	return NewFacade(sneatcoretesting.NewMemoryDB(), fakeIDGenerator{next: projectID})
}

func TestFacade_CreateProject(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	f := NewFacade(db, fakeIDGenerator{next: "proj1234"})
	ctx := context.Background()

	id, err := f.CreateProject(ctx, testUserID, models4datatug.FirestoreStoreID, "My project")
	if err != nil {
		t.Fatalf("CreateProject(): %v", err)
	}
	if id != "proj1234" {
		t.Fatalf("project id = %q, want %q", id, "proj1234")
	}

	// The project record itself.
	projectRecord, project := models4datatug.NewProjectRecord(id)
	if err = db.Get(ctx, projectRecord); err != nil {
		t.Fatalf("failed to read back the project record: %v", err)
	}
	if project.Title != "My project" {
		t.Errorf("project title = %q, want %q", project.Title, "My project")
	}
	if project.Access != models4datatug.AccessPrivate {
		t.Errorf("project access = %q, want %q", project.Access, models4datatug.AccessPrivate)
	}
	if len(project.UserIDs) != 1 || project.UserIDs[0] != testUserID {
		t.Errorf("project userIDs = %v, want [%s]", project.UserIDs, testUserID)
	}
	if project.Created == nil || project.Created.At.IsZero() {
		t.Error("project created.at must be set")
	}

	// The user's DataTug index: users/{userID}/ext/datatug.
	userExtRecord, userExt := models4datatug.NewUserExtRecord(testUserID, const4datatug.ExtensionID)
	if wantKey := "users/" + testUserID + "/ext/" + const4datatug.ExtensionID; userExtRecord.Key().String() != wantKey {
		t.Fatalf("user index key = %q, want %q", userExtRecord.Key().String(), wantKey)
	}
	if err = db.Get(ctx, userExtRecord); err != nil {
		t.Fatalf("failed to read back the user index record: %v", err)
	}
	store, ok := userExt.Stores[models4datatug.FirestoreStoreID]
	if !ok {
		t.Fatalf("user index is missing the %q store: %+v", models4datatug.FirestoreStoreID, userExt.Stores)
	}
	if store.Type != models4datatug.FirestoreStoreID || store.Title != models4datatug.FirestoreStoreTitle {
		t.Errorf("store brief = %+v, want the cloud store's type and title", store)
	}
	brief, ok := store.Projects[id]
	if !ok {
		t.Fatalf("user index is missing project %q: %+v", id, store.Projects)
	}
	if brief.Title != "My project" || brief.Access != models4datatug.AccessPrivate {
		t.Errorf("project brief = %+v, want title/access of the new project", brief)
	}
}

// The second project for the same user must take the update path and leave the
// first project's brief intact.
func TestFacade_CreateProject_keepsExistingBriefs(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	ctx := context.Background()

	first, err := NewFacade(db, fakeIDGenerator{next: "proj0001"}).
		CreateProject(ctx, testUserID, models4datatug.FirestoreStoreID, "First project")
	if err != nil {
		t.Fatalf("first CreateProject(): %v", err)
	}
	second, err := NewFacade(db, fakeIDGenerator{next: "proj0002"}).
		CreateProject(ctx, testUserID, models4datatug.FirestoreStoreID, "Second project")
	if err != nil {
		t.Fatalf("second CreateProject(): %v", err)
	}

	userExtRecord, userExt := models4datatug.NewUserExtRecord(testUserID, const4datatug.ExtensionID)
	if err = db.Get(ctx, userExtRecord); err != nil {
		t.Fatalf("failed to read back the user index record: %v", err)
	}
	projects := userExt.Stores[models4datatug.FirestoreStoreID].Projects
	if len(projects) != 2 {
		t.Fatalf("user index holds %d projects, want 2: %+v", len(projects), projects)
	}
	for id, wantTitle := range map[string]string{
		first:  "First project",
		second: "Second project",
	} {
		if brief, ok := projects[id]; !ok {
			t.Errorf("user index is missing project %q", id)
		} else if brief.Title != wantTitle {
			t.Errorf("project %q brief title = %q, want %q", id, brief.Title, wantTitle)
		}
	}
}

func TestFacade_CreateProject_validation(t *testing.T) {
	for name, tc := range map[string]struct {
		userID, storeID, title string
		wantErr                error
	}{
		"no user":           {storeID: models4datatug.FirestoreStoreID, title: "T", wantErr: ErrUserIDRequired},
		"no title":          {userID: testUserID, storeID: models4datatug.FirestoreStoreID, wantErr: ErrTitleRequired},
		"unsupported store": {userID: testUserID, storeID: "github.com", title: "T", wantErr: ErrUnsupportedStore},
	} {
		t.Run(name, func(t *testing.T) {
			f := newTestFacade(t, "proj1234")
			_, err := f.CreateProject(context.Background(), tc.userID, tc.storeID, tc.title)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("CreateProject() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestFacade_CreateProject_idGeneratorFailure(t *testing.T) {
	genErr := errors.New("no ids left")
	f := NewFacade(sneatcoretesting.NewMemoryDB(), fakeIDGenerator{err: genErr})

	_, err := f.CreateProject(context.Background(), testUserID, models4datatug.FirestoreStoreID, "T")
	if !errors.Is(err, genErr) {
		t.Fatalf("CreateProject() error = %v, want it to wrap %v", err, genErr)
	}
}

// TestUserExtKey_pinsPlatformPath pins the users/{userID}/ext/{extID} shape the
// module builds itself instead of calling dal4userus.NewUserExtKey (the module
// must depend on dal-go only).
func TestUserExtKey_pinsPlatformPath(t *testing.T) {
	got := models4datatug.NewUserExtKey("u1", "datatug").String()
	want := fmt.Sprintf("%s/u1/%s/datatug", models4datatug.UsersCollection, models4datatug.UserExtCollection)
	if got != want {
		t.Errorf("user ext key = %q, want %q", got, want)
	}
}
