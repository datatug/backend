// Copyright 2026 https://datatug.io/

package api4datatug

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/datatug/backend/githubauth4datatug"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/sneat-co/sneat-go-core/facade"
	"github.com/strongo/validation"
)

// GitHubAuthorizationRouteOptions opts the host into actor-bound GitHub App
// OAuth routes. A nil provider keeps the routes fail-closed with 503.
type GitHubAuthorizationRouteOptions struct {
	Provider GitHubAuthorizationService
}

// GitHubAuthorizationService is the API layer's narrow host-composition port.
type GitHubAuthorizationService interface {
	BeginAuthorization(context.Context, string) (string, error)
	CompleteAuthorization(context.Context, string, string, string) error
	ListRepositories(context.Context, string) ([]githubauth4datatug.GitHubRepository, error)
}

type startGitHubAuthorizationRequest struct{}

func (startGitHubAuthorizationRequest) Validate() error { return nil }

type StartGitHubAuthorizationResponse struct {
	AuthorizationURL string `json:"authorizationURL"`
}

type CompleteGitHubAuthorizationRequest struct {
	Code  string `json:"code"`
	State string `json:"state"`
}

func (r CompleteGitHubAuthorizationRequest) Validate() error {
	if r.Code == "" {
		return validation.NewErrRequestIsMissingRequiredField("code")
	}
	if r.State == "" {
		return validation.NewErrRequestIsMissingRequiredField("state")
	}
	return nil
}

type CompleteGitHubAuthorizationResponse struct {
	Connected bool `json:"connected"`
}

type GitHubRepositoryOption struct {
	ID            int64  `json:"id"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"defaultBranch"`
	Permission    string `json:"permission"`
}

type ListGitHubRepositoriesResponse struct {
	Repositories []GitHubRepositoryOption `json:"repositories"`
}

func httpPostStartGitHubAuthorization(options GitHubAuthorizationRouteOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if options.Provider == nil {
			githubAuthorizationError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		var request startGitHubAuthorizationRequest
		ctx, err := verifyAuthenticatedRequestAndDecodeBody(w, r, verify.DefaultJsonWithAuthRequired, &request)
		if err != nil {
			return
		}
		firebaseUID, err := verifiedFirebaseUID(ctx)
		if err != nil {
			githubAuthorizationError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		authorizationURL, err := options.Provider.BeginAuthorization(ctx, firebaseUID)
		if err != nil {
			githubAuthorizationError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		writeGitHubJSON(w, http.StatusOK, StartGitHubAuthorizationResponse{AuthorizationURL: authorizationURL})
	}
}

func httpPostCompleteGitHubAuthorization(options GitHubAuthorizationRouteOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if options.Provider == nil {
			githubAuthorizationError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		var request CompleteGitHubAuthorizationRequest
		ctx, err := verifyAuthenticatedRequestAndDecodeBody(w, r, verify.DefaultJsonWithAuthRequired, &request)
		if err != nil {
			return
		}
		firebaseUID, err := verifiedFirebaseUID(ctx)
		if err != nil {
			githubAuthorizationError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err = options.Provider.CompleteAuthorization(ctx, firebaseUID, request.State, request.Code); err != nil {
			status, code := githubAuthorizationStatus(err)
			githubAuthorizationError(w, status, code)
			return
		}
		writeGitHubJSON(w, http.StatusOK, CompleteGitHubAuthorizationResponse{Connected: true})
	}
}

func httpGetGitHubRepositories(options GitHubAuthorizationRouteOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if options.Provider == nil {
			githubAuthorizationError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		ctx, err := verifyAuthenticatedRequest(w, r, verify.NoContentAuthRequired)
		if err != nil {
			return
		}
		firebaseUID, err := verifiedFirebaseUID(ctx)
		if err != nil {
			githubAuthorizationError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		repositories, err := options.Provider.ListRepositories(ctx, firebaseUID)
		if err != nil {
			status, code := githubAuthorizationStatus(err)
			githubAuthorizationError(w, status, code)
			return
		}
		response := ListGitHubRepositoriesResponse{Repositories: make([]GitHubRepositoryOption, 0, len(repositories))}
		for _, repo := range repositories {
			if repo.EffectivePermission != githubauth4datatug.RepositoryRead && repo.EffectivePermission != githubauth4datatug.RepositoryWrite {
				continue
			}
			response.Repositories = append(response.Repositories, GitHubRepositoryOption{
				ID: repo.ID, Owner: repo.Owner, Name: repo.Name,
				DefaultBranch: repo.DefaultBranch, Permission: string(repo.EffectivePermission),
			})
		}
		writeGitHubJSON(w, http.StatusOK, response)
	}
}

func verifiedFirebaseUID(ctx facade.ContextWithUser) (string, error) {
	if ctx == nil || ctx.User() == nil || ctx.User().GetUserID() == "" {
		return "", errors.New("authenticated Firebase user is missing")
	}
	return ctx.User().GetUserID(), nil
}

func githubAuthorizationStatus(err error) (int, string) {
	switch {
	case errors.Is(err, githubauth4datatug.ErrOAuthStateInvalid):
		return http.StatusBadRequest, "invalid_state"
	case errors.Is(err, githubauth4datatug.ErrGitHubActorMismatch):
		return http.StatusForbidden, "actor_mismatch"
	case errors.Is(err, githubauth4datatug.ErrGitHubRepositoryDenied), errors.Is(err, githubauth4datatug.ErrGitHubPermissionDenied):
		return http.StatusForbidden, "repository_denied"
	case errors.Is(err, githubauth4datatug.ErrReauthorizationRequired), errors.Is(err, githubauth4datatug.ErrCredentialMissing):
		return http.StatusConflict, "reauthorization_required"
	default:
		return http.StatusServiceUnavailable, "unavailable"
	}
}

func githubAuthorizationError(w http.ResponseWriter, status int, code string) {
	writeGitHubJSON(w, status, map[string]any{"error": map[string]string{"code": code}})
}

func writeGitHubJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
