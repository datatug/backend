// Package api4datatug is the DataTug module's HTTP API layer. It translates
// HTTP into domain commands and back; the business logic lives in
// facade4datatug and the storage access in dal-go.
package api4datatug

import (
	"fmt"
	"net/http"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/sneat-co/sneat-go-core/facade"
	"github.com/strongo/validation"
)

// CreateProjectRequest is the body of a create-project request. The store is
// taken from the `?store=` query parameter (the same shape the datatug CLI
// agent's create_project endpoint uses), so it is not part of the JSON body.
type CreateProjectRequest struct {
	StoreID string `json:"-"`
	Title   string `json:"title"`
}

// Validate validates the request.
func (v CreateProjectRequest) Validate() error {
	if v.StoreID == "" {
		return validation.NewErrRequestIsMissingRequiredField("store")
	}
	if v.StoreID != models4datatug.FirestoreStoreID {
		return validation.NewErrBadRequestFieldValue("store",
			fmt.Sprintf("only the %s store is served by this API", models4datatug.FirestoreStoreID))
	}
	if v.Title == "" {
		return validation.NewErrRequestIsMissingRequiredField("title")
	}
	return nil
}

// CreateProjectResponse is the response of a successful create-project call.
type CreateProjectResponse struct {
	ID string `json:"id"`
}

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
// Creating a project in a GitHub repo is deliberately not served here: the
// client that holds the user's GitHub credential (datatug-apps, with a user
// PAT) creates those files itself.
func httpPostCreateProject(
	ids facade4datatug.IDGenerator,
	githubOAuth facade4datatug.GithubOAuthExchanger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request CreateProjectRequest
		request.StoreID = r.URL.Query().Get("store")
		ctx, err := verifyAuthenticatedRequestAndDecodeBody(w, r, verify.DefaultJsonWithAuthRequired, &request)
		if err != nil {
			return
		}
		db, err := facade.GetSneatDB(ctx)
		if err != nil {
			err = fmt.Errorf("failed to get a database: %w", err)
			apicore.ReturnJSON(ctx, w, r, http.StatusCreated, err, nil)
			return
		}
		f := facade4datatug.NewFacade(db, ids, githubOAuth)
		projectID, err := f.CreateProject(ctx, ctx.User().GetUserID(), request.StoreID, request.Title)
		apicore.ReturnJSON(ctx, w, r, http.StatusCreated, err, &CreateProjectResponse{ID: projectID})
	}
}
