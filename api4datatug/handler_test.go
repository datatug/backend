package api4datatug

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/sneat-co/sneat-go-core/facade"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"

	"github.com/datatug/backend/const4datatug"
	"github.com/datatug/backend/models4datatug"
)

type fakeIDs struct {
	next string
	err  error
}

func (f fakeIDs) NewID(context.Context) (string, error) { return f.next, f.err }

// stubVerification replaces the auth seam with a fake that decodes the body and
// validates it exactly as apicore does (400 + error on a bad request), then
// returns an authenticated user context over an in-memory database — so the
// handler under test runs the real domain command against real dal-go records.
func stubVerification(t *testing.T, db dal.DB) {
	t.Helper()
	orig := verifyAuthenticatedRequestAndDecodeBody
	t.Cleanup(func() { verifyAuthenticatedRequestAndDecodeBody = orig })
	verifyAuthenticatedRequestAndDecodeBody = func(
		w http.ResponseWriter, r *http.Request, _ verify.RequestOptions, body facade.Request,
	) (facade.ContextWithUser, error) {
		if err := json.NewDecoder(r.Body).Decode(body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return nil, err
		}
		if err := body.Validate(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return nil, err
		}
		ctx := facade.WithSneatDB(r.Context(), db)
		return facade.NewContextWithUser(ctx, facade.NewUserContext("user1")), nil
	}
}

// TestHttpPostCreateProject_storeComesFromQuery pins the client contract: the
// datatug-apps client sends the store as `?store=firestore`, the title in the
// body, and expects the new project id back — with both records written.
func TestHttpPostCreateProject_storeComesFromQuery(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	stubVerification(t, db)

	r := httptest.NewRequest(http.MethodPost,
		"/v0/datatug/projects/create_project?store="+models4datatug.FirestoreStoreID,
		strings.NewReader(`{"title":"My project"}`))
	w := httptest.NewRecorder()

	httpPostCreateProject(fakeIDs{next: "proj1234"})(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	var response CreateProjectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response %q: %v", w.Body.String(), err)
	}
	if response.ID != "proj1234" {
		t.Errorf("response id = %q, want %q", response.ID, "proj1234")
	}

	projectRecord, project := models4datatug.NewProjectRecord(response.ID)
	if err := db.Get(context.Background(), projectRecord); err != nil {
		t.Fatalf("failed to read back the project record: %v", err)
	}
	if project.Title != "My project" {
		t.Errorf("stored project title = %q, want %q", project.Title, "My project")
	}

	userExtRecord, userExt := models4datatug.NewUserExtRecord("user1", const4datatug.ExtensionID)
	if err := db.Get(context.Background(), userExtRecord); err != nil {
		t.Fatalf("failed to read back the user index record: %v", err)
	}
	if _, ok := userExt.Stores[models4datatug.FirestoreStoreID].Projects[response.ID]; !ok {
		t.Errorf("user index is missing project %q: %+v", response.ID, userExt.Stores)
	}
}

// An unsupported store is a client error, not a server fault, and must not
// create anything.
func TestHttpPostCreateProject_rejectsUnsupportedStore(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	stubVerification(t, db)

	r := httptest.NewRequest(http.MethodPost,
		"/v0/datatug/projects/create_project?store=github.com",
		strings.NewReader(`{"title":"My project"}`))
	w := httptest.NewRecorder()

	httpPostCreateProject(fakeIDs{next: "proj1234"})(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	projectRecord, _ := models4datatug.NewProjectRecord("proj1234")
	if err := db.Get(context.Background(), projectRecord); err == nil {
		t.Error("no project must be created for an unsupported store")
	}
}

// The missing store parameter is a client error too.
func TestHttpPostCreateProject_requiresStore(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	stubVerification(t, db)

	r := httptest.NewRequest(http.MethodPost,
		"/v0/datatug/projects/create_project",
		strings.NewReader(`{"title":"My project"}`))
	w := httptest.NewRecorder()

	httpPostCreateProject(fakeIDs{next: "proj1234"})(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// A failing ID port surfaces as a failure, not a silent success.
func TestHttpPostCreateProject_propagatesIDGeneratorFailure(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	stubVerification(t, db)

	r := httptest.NewRequest(http.MethodPost,
		"/v0/datatug/projects/create_project?store="+models4datatug.FirestoreStoreID,
		strings.NewReader(`{"title":"My project"}`))
	w := httptest.NewRecorder()

	httpPostCreateProject(fakeIDs{err: errors.New("no ids left")})(w, r)

	if w.Code == http.StatusCreated {
		t.Errorf("status = %d, want a failure status when the ID port fails", w.Code)
	}
}

// When the host cannot resolve a database, the request fails loudly instead of
// reporting a created project.
func TestHttpPostCreateProject_databaseFailure(t *testing.T) {
	orig := verifyAuthenticatedRequestAndDecodeBody
	t.Cleanup(func() { verifyAuthenticatedRequestAndDecodeBody = orig })
	verifyAuthenticatedRequestAndDecodeBody = func(
		_ http.ResponseWriter, r *http.Request, _ verify.RequestOptions, body facade.Request,
	) (facade.ContextWithUser, error) {
		if err := json.NewDecoder(r.Body).Decode(body); err != nil {
			return nil, err
		}
		ctx := facade.WithSneatDBProvider(r.Context(), func(context.Context) (dal.DB, error) {
			return nil, errors.New("no database configured")
		})
		return facade.NewContextWithUser(ctx, facade.NewUserContext("user1")), nil
	}

	body := strings.NewReader(`{"title":"My project"}`)
	r := httptest.NewRequest(http.MethodPost,
		"/v0/datatug/projects/create_project?store="+models4datatug.FirestoreStoreID, body)
	w := httptest.NewRecorder()

	httpPostCreateProject(fakeIDs{next: "proj1234"})(w, r)

	if w.Code == http.StatusCreated {
		t.Errorf("status = %d, want a failure status when no database is available", w.Code)
	}
}

// TestRegisterHttpRoutes pins the public path: route paths are literal in this
// framework (the host adds no prefix).
func TestRegisterHttpRoutes(t *testing.T) {
	type route struct{ method, path string }
	var routes []route
	RegisterHttpRoutes(func(method, path string, _ http.HandlerFunc) {
		routes = append(routes, route{method, path})
	}, fakeIDs{next: "proj1234"})

	want := route{http.MethodPost, "/v0/datatug/projects/create_project"}
	if len(routes) != 1 || routes[0] != want {
		t.Fatalf("registered routes = %+v, want exactly [%+v]", routes, want)
	}
}
