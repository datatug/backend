package facade4datatug

import (
	"context"
	"errors"
	"testing"

	"github.com/sneat-co/sneat-go-core/sneatcoretesting"

	"github.com/datatug/backend/const4datatug"
	"github.com/datatug/backend/models4datatug"
)

// Registering a GitHub-hosted project records its brief under the github.com
// store, addressed by the same `repo@org@folder` id the web client uses.
func TestFacade_RegisterGithubProject(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	f := NewFacade(db, fakeIDGenerator{next: "unused"})
	ctx := context.Background()

	id, err := f.RegisterGithubProject(ctx, testUserID, RegisterGithubProjectRequest{
		Org:    "datatug",
		Repo:   "demo-projects",
		Title:  "My project",
		Folder: "my-folder",
	})
	if err != nil {
		t.Fatalf("RegisterGithubProject(): %v", err)
	}
	if want := "demo-projects@datatug@my-folder"; id != want {
		t.Fatalf("project id = %q, want %q", id, want)
	}

	userExtRecord, userExt := models4datatug.NewUserExtRecord(testUserID, const4datatug.ExtensionID)
	if err = db.Get(ctx, userExtRecord); err != nil {
		t.Fatalf("failed to read back the user index: %v", err)
	}
	store, ok := userExt.Stores[models4datatug.GithubStoreID]
	if !ok {
		t.Fatalf("user index is missing the %q store: %+v", models4datatug.GithubStoreID, userExt.Stores)
	}
	if store.Type != models4datatug.GithubStoreType || store.Title != models4datatug.GithubStoreTitle {
		t.Errorf("store brief = %+v, want the GitHub store's type and title", store)
	}
	brief, ok := store.Projects[id]
	if !ok {
		t.Fatalf("user index is missing project %q: %+v", id, store.Projects)
	}
	if brief.Title != "My project" || brief.Access != models4datatug.AccessPrivate {
		t.Errorf("project brief = %+v, want title/access of the registered project", brief)
	}
}

// Registering a GitHub project must not disturb the cloud store's briefs.
func TestFacade_RegisterGithubProject_keepsCloudStore(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	ctx := context.Background()

	cloudID, err := NewFacade(db, fakeIDGenerator{next: "proj1234"}).
		CreateProject(ctx, testUserID, models4datatug.FirestoreStoreID, "Cloud project")
	if err != nil {
		t.Fatalf("CreateProject(): %v", err)
	}
	githubID, err := NewFacade(db, fakeIDGenerator{next: "unused"}).
		RegisterGithubProject(ctx, testUserID, RegisterGithubProjectRequest{
			Org: "datatug", Repo: "demo-projects", Title: "GitHub project",
		})
	if err != nil {
		t.Fatalf("RegisterGithubProject(): %v", err)
	}

	userExtRecord, userExt := models4datatug.NewUserExtRecord(testUserID, const4datatug.ExtensionID)
	if err = db.Get(ctx, userExtRecord); err != nil {
		t.Fatalf("failed to read back the user index: %v", err)
	}
	if len(userExt.Stores) != 2 {
		t.Fatalf("user index holds %d stores, want 2: %+v", len(userExt.Stores), userExt.Stores)
	}
	if _, ok := userExt.Stores[models4datatug.FirestoreStoreID].Projects[cloudID]; !ok {
		t.Errorf("the cloud store lost its project %q", cloudID)
	}
	if _, ok := userExt.Stores[models4datatug.GithubStoreID].Projects[githubID]; !ok {
		t.Errorf("the GitHub store is missing its project %q", githubID)
	}
}

func TestFacade_RegisterGithubProject_validation(t *testing.T) {
	orgErr := ErrOrgRequired
	repoErr := ErrRepoRequired
	titleErr := ErrTitleRequired
	for name, tc := range map[string]struct {
		userID  string
		request RegisterGithubProjectRequest
		wantErr error
	}{
		"no user": {
			request: RegisterGithubProjectRequest{Org: "datatug", Repo: "demo", Title: "T"},
			wantErr: ErrUserIDRequired,
		},
		"no org": {
			userID:  testUserID,
			request: RegisterGithubProjectRequest{Repo: "demo", Title: "T"},
			wantErr: orgErr,
		},
		"no repo": {
			userID:  testUserID,
			request: RegisterGithubProjectRequest{Org: "datatug", Title: "T"},
			wantErr: repoErr,
		},
		"no title": {
			userID:  testUserID,
			request: RegisterGithubProjectRequest{Org: "datatug", Repo: "demo"},
			wantErr: titleErr,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newTestFacade(t, "unused")
			_, err := f.RegisterGithubProject(context.Background(), tc.userID, tc.request)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("RegisterGithubProject() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
