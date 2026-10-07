package api4datatug

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/githubauth4datatug"
	"github.com/datatug/backend/githubstore4datatug"
	"github.com/datatug/backend/models4datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/strongo/validation"
)

type SaveGitHubQueryRequest struct{ dto.SaveQueryRequest }

func (v *SaveGitHubQueryRequest) UnmarshalJSON(data []byte) error {
	var parsed dto.SaveQueryRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return validation.NewErrBadRequestFieldValue("body", "one JSON object is required")
	}
	v.SaveQueryRequest = parsed
	return nil
}
func (v SaveGitHubQueryRequest) Validate() error { return v.SaveQueryRequest.Validate() }

func httpPostGitHubSaveQuery(options GitHubProjectRouteOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if options.Provider == nil || options.Service == nil {
			sharedProjectError(w, http.StatusServiceUnavailable, "github_unavailable")
			return
		}
		var request SaveGitHubQueryRequest
		ctx, err := verifyAuthenticatedRequestAndDecodeBody(w, r, verify.DefaultJsonWithAuthRequired, &request)
		if err != nil {
			return
		}
		actorID, err := verifiedFirebaseUID(ctx)
		if err != nil {
			sharedProjectError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if request.StoreID != models4datatug.GithubStoreID {
			sharedProjectError(w, http.StatusBadRequest, "invalid_store")
			return
		}
		repoName, owner, folder, ok := parseGitHubProjectKey(request.ProjectID)
		if !ok || request.Branch == "" || request.ExpectedBranchHead == "" {
			sharedProjectError(w, http.StatusBadRequest, "invalid_project")
			return
		}
		accessible, err := options.Provider.ListRepositories(ctx, actorID)
		if err != nil {
			status, code := githubAuthorizationStatus(err)
			sharedProjectError(w, status, code)
			return
		}
		var selected *githubauth4datatug.GitHubRepository
		for i := range accessible {
			if strings.EqualFold(accessible[i].Owner, owner) && strings.EqualFold(accessible[i].Name, repoName) {
				selected = &accessible[i]
				break
			}
		}
		if selected == nil {
			sharedProjectError(w, http.StatusForbidden, "repository_denied")
			return
		}
		repository, err := githubstore4datatug.AuthorizeQueryRepository(ctx, options.Provider, actorID, selected.ID, owner, repoName)
		if err != nil {
			status, code := githubAuthorizationStatus(err)
			sharedProjectError(w, status, code)
			return
		}
		result, err := options.Service.SaveGitHubQuery(ctx, actorID, selected.ID, owner, repoName, folder, request.SaveQueryRequest, repository)
		if err != nil {
			status, code := http.StatusServiceUnavailable, "github_unavailable"
			switch {
			case errors.Is(err, facade4datatug.ErrSharedProjectUnauthorized), errors.Is(err, githubauth4datatug.ErrGitHubPermissionDenied):
				status, code = http.StatusForbidden, "project_denied"
			case errors.Is(err, facade4datatug.ErrGitHubQueryInvalid), errors.Is(err, githubstore4datatug.ErrUnsupportedExistingQuery):
				status, code = http.StatusBadRequest, "unsupported_query"
			case errors.Is(err, facade4datatug.ErrGitHubQueryConflict), errors.Is(err, dto.ErrOperationConflict), errors.Is(err, dto.ErrQueryRevisionConflict), errors.Is(err, dto.ErrBranchHeadConflict):
				status, code = http.StatusConflict, "conflict"
			case errors.Is(err, facade4datatug.ErrGitHubQueryOutcomeUncertain):
				status, code = http.StatusConflict, "outcome_uncertain"
			}
			sharedProjectError(w, status, code)
			return
		}
		writeGitHubJSON(w, http.StatusOK, result)
	}
}
