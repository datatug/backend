package api4datatug

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/models4datatug"
	"github.com/datatug/backend/template4datatug"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/strongo/validation"
)

// GitHubProjectRouteOptions are injected by the host after configuring the
// dedicated DataTug GitHub App and paid Space service. A missing port is 503.
// AuthorizeRepository must freshly bind the Firebase UID to the current
// GitHub user, selected installation, immutable repository and write grant.
type GitHubProjectRouteOptions struct {
	Service             *facade4datatug.SharedProjectService
	AuthorizeRepository func(context.Context, string, int64, string, string) (facade4datatug.GitHubCreateRepository, error)
}

type CreateGitHubProjectRequest struct {
	Title       string `json:"title"`
	SpaceID     string `json:"spaceID"`
	OperationID string `json:"operationId"`
	GitHub      struct {
		RepositoryID       int64  `json:"repositoryID"`
		Owner              string `json:"owner"`
		Name               string `json:"name"`
		Folder             string `json:"folder"`
		Branch             string `json:"branch"`
		ExpectedBranchHead string `json:"expectedBranchHead"`
	} `json:"github"`
	Template struct {
		ID     string `json:"id"`
		Commit string `json:"commit"`
	} `json:"template"`
}

// UnmarshalJSON refuses unknown fields recursively. The common mutation API
// must never silently discard richer client metadata before computing a
// request digest or writing to GitHub.
func (v *CreateGitHubProjectRequest) UnmarshalJSON(data []byte) error {
	type raw CreateGitHubProjectRequest
	var parsed raw
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return validation.NewErrBadRequestFieldValue("body", "one JSON object is required")
	}
	*v = CreateGitHubProjectRequest(parsed)
	return nil
}

func (v CreateGitHubProjectRequest) Validate() error {
	if models4datatug.ValidateSharedProjectTitle(v.Title) != nil || models4datatug.ValidateSharedProjectIdentifier(v.SpaceID) != nil || models4datatug.ValidateSharedProjectIdentifier(v.OperationID) != nil || v.GitHub.RepositoryID < 1 || v.GitHub.Owner == "" || v.GitHub.Name == "" || template4datatug.ValidateFolder(v.GitHub.Folder) != nil || v.GitHub.Branch == "" || v.GitHub.ExpectedBranchHead == "" || v.Template.ID != template4datatug.DemoProjectID || v.Template.Commit != template4datatug.DemoProjectCommit {
		return validation.NewErrBadRequestFieldValue("body", "invalid GitHub project create request")
	}
	return nil
}

func httpPostCreateGitHubProject(options GitHubProjectRouteOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if options.Service == nil || options.AuthorizeRepository == nil {
			sharedProjectError(w, http.StatusServiceUnavailable, "github_unavailable")
			return
		}
		if r.URL.Query().Get("store") != models4datatug.GithubStoreID {
			sharedProjectError(w, http.StatusBadRequest, "invalid_store")
			return
		}
		var request CreateGitHubProjectRequest
		ctx, err := verifyAuthenticatedRequestAndDecodeBody(w, r, verify.DefaultJsonWithAuthRequired, &request)
		if err != nil {
			return
		}
		if ctx == nil || ctx.User() == nil || ctx.User().GetUserID() == "" {
			sharedProjectError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		actorID := ctx.User().GetUserID()
		repo, err := options.AuthorizeRepository(ctx, actorID, request.GitHub.RepositoryID, request.GitHub.Owner, request.GitHub.Name)
		if err != nil {
			sharedProjectError(w, http.StatusForbidden, "github_denied")
			return
		}
		command := facade4datatug.GitHubProjectCreateCommand{
			ActorID: actorID, SpaceID: request.SpaceID, OperationID: request.OperationID, Title: request.Title,
			Source: models4datatug.GitHubCreateSource{
				Binding: models4datatug.GitHubProjectBinding{
					RepositoryID: request.GitHub.RepositoryID, Owner: request.GitHub.Owner, Name: request.GitHub.Name,
					Folder: request.GitHub.Folder, Branch: request.GitHub.Branch,
				},
				ExpectedHead: request.GitHub.ExpectedBranchHead, TemplateID: request.Template.ID, TemplateCommit: request.Template.Commit,
			},
		}
		response, err := options.Service.CreateGitHubProject(ctx, command, repo)
		if err != nil {
			status, code := http.StatusServiceUnavailable, "github_unavailable"
			switch {
			case errors.Is(err, facade4datatug.ErrGitHubProjectInvalid):
				status, code = http.StatusBadRequest, "invalid"
			case errors.Is(err, facade4datatug.ErrSharedProjectUnauthorized):
				status, code = http.StatusForbidden, "unauthorized"
			case errors.Is(err, facade4datatug.ErrGitHubProjectConflict), errors.Is(err, facade4datatug.ErrSharedProjectConflict), errors.Is(err, facade4datatug.ErrProtectedProjectQuota):
				status, code = http.StatusConflict, "conflict"
			case errors.Is(err, facade4datatug.ErrGitHubOutcomeUncertain):
				status, code = http.StatusConflict, "outcome_uncertain"
			}
			sharedProjectError(w, status, code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(response)
	}
}
