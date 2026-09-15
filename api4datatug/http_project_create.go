// Package api4datatug exposes the DataTug module's HTTP API.
package api4datatug

import (
	"net/http"

	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/apicore/verify"

	datatugfacade "github.com/datatug/backend/facade4datatug"
)

// createProject is a seam over the facade so the handler can be unit-tested
// without a database (same pattern as the other Sneat modules).
var createProject = datatugfacade.CreateProject

// verifyAuthenticatedRequestAndDecodeBody is a seam over apicore's helper, so
// the handler can be tested without minting a real auth token.
var verifyAuthenticatedRequestAndDecodeBody = apicore.VerifyAuthenticatedRequestAndDecodeBody

// httpPostCreateProject creates a DataTug project in the DataTug cloud store.
//
// Request:  POST /v0/datatug/projects/create_project?store=firestore
//
//	Authorization: Bearer <firebase-id-token>   (auth REQUIRED)
//	Content-Type: application/json
//	Body: {"title":"<project title>"}
//
// Response: 201 Created
//
//	{"id":"<projectID>"}
//
// The client (datatug-apps' ProjectService.createNewProject) sends the store as
// a `?store=` query parameter, exactly like the datatug CLI agent's
// create_project endpoint, so it is read from there and fed into the request.
func httpPostCreateProject(w http.ResponseWriter, r *http.Request) {
	var request datatugfacade.CreateProjectRequest
	if storeID := r.URL.Query().Get("store"); storeID != "" {
		request.StoreID = storeID
	}
	ctx, err := verifyAuthenticatedRequestAndDecodeBody(w, r, verify.DefaultJsonWithAuthRequired, &request)
	if err != nil {
		return
	}
	response, err := createProject(ctx, request)
	apicore.ReturnJSON(ctx, w, r, http.StatusCreated, err, &response)
}
