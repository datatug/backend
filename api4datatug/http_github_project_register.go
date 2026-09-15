package api4datatug

import (
	"fmt"
	"net/http"

	"github.com/datatug/backend/facade4datatug"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/sneat-co/sneat-go-core/facade"
	"github.com/strongo/validation"
)

// RegisterGithubProjectRequest is the body of a register-github-project
// request: a project whose files the client has already committed to a GitHub
// repository, which the cloud only needs to record in the user's index.
type RegisterGithubProjectRequest struct {
	Org    string `json:"org"`
	Repo   string `json:"repo"`
	Folder string `json:"folder"`
	Title  string `json:"title"`
}

// Validate validates the request.
func (v RegisterGithubProjectRequest) Validate() error {
	if v.Org == "" {
		return validation.NewErrRequestIsMissingRequiredField("org")
	}
	if v.Repo == "" {
		return validation.NewErrRequestIsMissingRequiredField("repo")
	}
	if v.Title == "" {
		return validation.NewErrRequestIsMissingRequiredField("title")
	}
	return nil
}

// RegisterGithubProjectResponse is the response of a successful call.
type RegisterGithubProjectResponse struct {
	ID string `json:"id"`
}

// httpPostRegisterGithubProject records a GitHub-hosted project in the user's
// DataTug index, so it appears among the user's projects.
//
// Request:  POST /v0/datatug/projects/register_github_project
//
//	Authorization: Bearer <firebase-id-token>   (auth REQUIRED)
//	Content-Type: application/json
//	Body: {"org":"<owner>","repo":"<name>","folder":"<folder>","title":"<title>"}
//
// Response: 201 Created
//
//	{"id":"<repo>@<org>@<folder>"}
//
// The cloud never talks to GitHub: the client that holds the user's GitHub
// credential creates the repository files, then registers them here.
func httpPostRegisterGithubProject(
	ids facade4datatug.IDGenerator,
	githubOAuth facade4datatug.GithubOAuthExchanger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request RegisterGithubProjectRequest
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
		projectID, err := f.RegisterGithubProject(ctx, ctx.User().GetUserID(), facade4datatug.RegisterGithubProjectRequest{
			Org:    request.Org,
			Repo:   request.Repo,
			Folder: request.Folder,
			Title:  request.Title,
		})
		apicore.ReturnJSON(ctx, w, r, http.StatusCreated, err, &RegisterGithubProjectResponse{ID: projectID})
	}
}
