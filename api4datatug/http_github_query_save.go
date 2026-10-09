package api4datatug

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/githubauth4datatug"
	"github.com/datatug/backend/githubstore4datatug"
	"github.com/datatug/backend/models4datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/strongo/validation"
)

type SaveGitHubQueryRequest struct{ dto.SaveQueryRequest }

const maxSaveGitHubQueryRequestBytes = 5 << 20

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
		if options.provider() == nil || options.service() == nil {
			sharedProjectError(w, http.StatusServiceUnavailable, "github_unavailable")
			return
		}
		ctx, err := verifyAuthenticatedRequest(w, r, verify.Request(verify.AuthenticationRequired(true), verify.MaximumContentLength(-1)))
		if err != nil {
			return
		}
		actorID, err := verifiedFirebaseUID(ctx)
		if err != nil {
			sharedProjectError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if r.ContentLength > maxSaveGitHubQueryRequestBytes {
			sharedProjectError(w, http.StatusRequestEntityTooLarge, "query_too_large")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxSaveGitHubQueryRequestBytes+1))
		if err != nil {
			sharedProjectError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		if len(body) > maxSaveGitHubQueryRequestBytes {
			sharedProjectError(w, http.StatusRequestEntityTooLarge, "query_too_large")
			return
		}
		var request SaveGitHubQueryRequest
		if len(body) == 0 || json.Unmarshal(body, &request) != nil || request.Validate() != nil {
			sharedProjectError(w, http.StatusBadRequest, "invalid_query")
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
		selected, err := options.provider().ResolveRepositoryByName(ctx, actorID, owner, repoName)
		if err != nil {
			status, code := githubAuthorizationStatus(err)
			sharedProjectError(w, status, code)
			return
		}
		repository, err := options.queryRepository(ctx, actorID, selected.ID, owner, repoName)
		if err != nil {
			status, code := githubAuthorizationStatus(err)
			sharedProjectError(w, status, code)
			return
		}
		result, err := options.service().SaveGitHubQuery(ctx, actorID, selected.ID, owner, repoName, folder, request.SaveQueryRequest, repository)
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
