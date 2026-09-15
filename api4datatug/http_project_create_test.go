package api4datatug

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/sneat-co/sneat-go-core/facade"

	"github.com/datatug/backend/dbo4datatug"
	datatugfacade "github.com/datatug/backend/facade4datatug"
)

var errNoStoreInRequest = errors.New("handler did not put the store into the request")

// TestHttpPostCreateProject_storeComesFromQuery pins the client contract: the
// datatug-apps client sends the store as `?store=firestore` (exactly like the
// datatug CLI agent's create_project endpoint) and expects the new id back.
func TestHttpPostCreateProject_storeComesFromQuery(t *testing.T) {
	origVerify, origCreate := verifyAuthenticatedRequestAndDecodeBody, createProject
	t.Cleanup(func() {
		verifyAuthenticatedRequestAndDecodeBody, createProject = origVerify, origCreate
	})

	const projectID = "proj1234"
	var seenRequest datatugfacade.CreateProjectRequest
	verifyAuthenticatedRequestAndDecodeBody = func(
		_ http.ResponseWriter, r *http.Request, _ verify.RequestOptions, request facade.Request,
	) (facade.ContextWithUser, error) {
		createRequest := request.(*datatugfacade.CreateProjectRequest)
		if err := json.NewDecoder(r.Body).Decode(createRequest); err != nil {
			return nil, err
		}
		if createRequest.StoreID == "" {
			return nil, errNoStoreInRequest
		}
		seenRequest = *createRequest
		return facade.NewContextWithUserID(context.Background(), "user1"), nil
	}
	createProject = func(_ facade.ContextWithUser, request datatugfacade.CreateProjectRequest) (datatugfacade.CreateProjectResponse, error) {
		if request != seenRequest {
			t.Errorf("facade got %+v, handler decoded %+v", request, seenRequest)
		}
		return datatugfacade.CreateProjectResponse{ID: projectID}, nil
	}

	r := httptest.NewRequest(http.MethodPost,
		"/v0/datatug/projects/create_project?store="+dbo4datatug.FirestoreStoreType,
		strings.NewReader(`{"title":"My project"}`))
	w := httptest.NewRecorder()

	httpPostCreateProject(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	if seenRequest.StoreID != dbo4datatug.FirestoreStoreType {
		t.Errorf("store in request = %q, want %q", seenRequest.StoreID, dbo4datatug.FirestoreStoreType)
	}
	if seenRequest.Title != "My project" {
		t.Errorf("title in request = %q, want %q", seenRequest.Title, "My project")
	}
	var response datatugfacade.CreateProjectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response %q: %v", w.Body.String(), err)
	}
	if response.ID != projectID {
		t.Errorf("response id = %q, want %q", response.ID, projectID)
	}
}

// TestRegisterHttpRoutes pins the public path: route paths are literal in this
// framework (no module prefix is added by the host).
func TestRegisterHttpRoutes(t *testing.T) {
	type route struct{ method, path string }
	var routes []route
	RegisterHttpRoutes(func(method, path string, _ http.HandlerFunc) {
		routes = append(routes, route{method, path})
	})
	want := route{http.MethodPost, "/v0/datatug/projects/create_project"}
	if len(routes) != 1 || routes[0] != want {
		t.Fatalf("registered routes = %+v, want exactly [%+v]", routes, want)
	}
}

// When verification rejects the request (no/invalid auth token), the handler
// must not reach the facade and must leave the status apicore wrote alone.
func TestHttpPostCreateProject_doesNotCallFacadeWhenVerificationFails(t *testing.T) {
	origVerify, origCreate := verifyAuthenticatedRequestAndDecodeBody, createProject
	t.Cleanup(func() {
		verifyAuthenticatedRequestAndDecodeBody, createProject = origVerify, origCreate
	})

	verifyAuthenticatedRequestAndDecodeBody = func(
		w http.ResponseWriter, _ *http.Request, _ verify.RequestOptions, _ facade.Request,
	) (facade.ContextWithUser, error) {
		w.WriteHeader(http.StatusUnauthorized)
		return nil, facade.ErrUnauthorized
	}
	facadeCalled := false
	createProject = func(facade.ContextWithUser, datatugfacade.CreateProjectRequest) (datatugfacade.CreateProjectResponse, error) {
		facadeCalled = true
		return datatugfacade.CreateProjectResponse{}, nil
	}

	r := httptest.NewRequest(http.MethodPost,
		"/v0/datatug/projects/create_project?store="+dbo4datatug.FirestoreStoreType,
		strings.NewReader(`{"title":"My project"}`))
	w := httptest.NewRecorder()

	httpPostCreateProject(w, r)

	if facadeCalled {
		t.Error("the facade must not be called when request verification fails")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
