package api4datatug

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/githubauth4datatug"
	"github.com/datatug/backend/githubstore4datatug"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
)

func openAuthorizedGitHubSnapshot(ctx context.Context, options GitHubProjectRouteOptions, actorID, project, branch string) (*githubstore4datatug.ProjectSnapshot, error) {
	if options.provider() == nil || options.service() == nil {
		return nil, githubauth4datatug.ErrGitHubAppNotConfigured
	}
	repo, owner, folder, ok := parseGitHubProjectKey(project)
	if !ok || branch == "" {
		return nil, githubstore4datatug.ErrInvalidProjectSnapshot
	}
	_, client, access, err := authorizeRegisteredGitHubProject(ctx, options, actorID, repo, owner, folder)
	if err != nil {
		return nil, err
	}
	snapshot, err := githubstore4datatug.OpenProjectSnapshot(ctx, client, folder, branch)
	if err != nil {
		return nil, err
	}
	summary, err := snapshot.ProjectSummary(ctx)
	if err != nil {
		return nil, err
	}
	var identity struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(summary, &identity) != nil || identity.ID != access.SharedProjectID {
		return nil, githubstore4datatug.ErrInvalidProjectSnapshot
	}
	return snapshot, nil
}

func authorizeRegisteredGitHubProject(ctx context.Context, options GitHubProjectRouteOptions, actorID, repo, owner, folder string) (*githubauth4datatug.GitHubRepository, githubProjectReadRepository, facade4datatug.GitHubProjectAccess, error) {
	var noAccess facade4datatug.GitHubProjectAccess
	repositories, err := options.provider().ListRepositories(ctx, actorID)
	if err != nil {
		return nil, nil, noAccess, err
	}
	var selected *githubauth4datatug.GitHubRepository
	for i := range repositories {
		if strings.EqualFold(repositories[i].Owner, owner) && strings.EqualFold(repositories[i].Name, repo) {
			selected = &repositories[i]
			break
		}
	}
	if selected == nil {
		return nil, nil, noAccess, githubauth4datatug.ErrGitHubRepositoryDenied
	}
	client, err := options.provider().AuthorizeReadRepository(ctx, actorID, githubauth4datatug.RepositoryRef{ID: selected.ID, Owner: selected.Owner, Name: selected.Name})
	if err != nil {
		return nil, nil, noAccess, err
	}
	access, err := options.service().ResolveGitHubProject(ctx, actorID, selected.ID, owner, repo, folder)
	if err != nil {
		return nil, nil, noAccess, err
	}
	return selected, client, access, nil
}

func githubReadError(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "github_unavailable"
	switch {
	case errors.Is(err, githubstore4datatug.ErrInvalidProjectSnapshot):
		status, code = http.StatusBadRequest, "invalid_project"
	case errors.Is(err, githubstore4datatug.ErrProjectFileMissing):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, facade4datatug.ErrSharedProjectUnauthorized), errors.Is(err, facade4datatug.ErrGitHubProjectNotRegistered), errors.Is(err, githubauth4datatug.ErrGitHubRepositoryDenied):
		status, code = http.StatusForbidden, "project_denied"
	default:
		if errors.Is(err, githubauth4datatug.ErrGitHubPermissionDenied) || errors.Is(err, githubauth4datatug.ErrCredentialMissing) || errors.Is(err, githubauth4datatug.ErrReauthorizationRequired) || errors.Is(err, githubauth4datatug.ErrGitHubAppNotConfigured) {
			status, code = githubAuthorizationStatus(err)
		}
	}
	sharedProjectError(w, status, code)
}

func withGitHubProjectSnapshot(options GitHubProjectRouteOptions, operation func(http.ResponseWriter, *http.Request, *githubstore4datatug.ProjectSnapshot)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if options.provider() == nil || options.service() == nil {
			sharedProjectError(w, http.StatusServiceUnavailable, "github_unavailable")
			return
		}
		query := r.URL.Query()
		if query.Get("storage") != models4datatug.GithubStoreID {
			sharedProjectError(w, http.StatusBadRequest, "invalid_store")
			return
		}
		ctx, err := verifyAuthenticatedRequest(w, r, verify.NoContentAuthRequired)
		if err != nil {
			return
		}
		actorID, err := verifiedFirebaseUID(ctx)
		if err != nil {
			sharedProjectError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		snapshot, err := openAuthorizedGitHubSnapshot(ctx, options, actorID, query.Get("project"), query.Get("branch"))
		if err != nil {
			githubReadError(w, err)
			return
		}
		operation(w, r, snapshot)
	}
}

func writeGitHubSnapshotJSON(w http.ResponseWriter, head string, payload any) {
	w.Header().Set("X-Datatug-Branch-Head", head)
	w.Header().Set("Access-Control-Expose-Headers", "X-Datatug-Branch-Head")
	writeGitHubJSON(w, http.StatusOK, payload)
}

func httpGetGitHubProjectSummary(options GitHubProjectRouteOptions) http.HandlerFunc {
	return withGitHubProjectSnapshot(options, func(w http.ResponseWriter, r *http.Request, snapshot *githubstore4datatug.ProjectSnapshot) {
		manifest, err := snapshot.ProjectSummary(r.Context())
		if err != nil {
			githubReadError(w, err)
			return
		}
		writeGitHubSnapshotJSON(w, snapshot.Head, manifest)
	})
}

func httpGetGitHubAllQueries(options GitHubProjectRouteOptions) http.HandlerFunc {
	return withGitHubProjectSnapshot(options, func(w http.ResponseWriter, r *http.Request, snapshot *githubstore4datatug.ProjectSnapshot) {
		folder, err := snapshot.AllQueries(r.Context())
		if err != nil {
			githubReadError(w, err)
			return
		}
		writeGitHubSnapshotJSON(w, snapshot.Head, folder)
	})
}

// The browser SQL runner needs the project's source declaration even when its
// GitHub repository is private. Expose this one fixed, bounded file through
// the same membership and GitHub App authorization as query reads.
func httpGetGitHubConnectionCatalog(options GitHubProjectRouteOptions) http.HandlerFunc {
	return withGitHubProjectSnapshot(options, func(w http.ResponseWriter, r *http.Request, snapshot *githubstore4datatug.ProjectSnapshot) {
		content, err := snapshot.ReadFile(r.Context(), "connections/demo-db.json")
		if err != nil {
			githubReadError(w, err)
			return
		}
		if len(content) > 256<<10 || !json.Valid(content) {
			sharedProjectError(w, http.StatusUnprocessableEntity, "invalid_connection_catalog")
			return
		}
		writeGitHubSnapshotJSON(w, snapshot.Head, json.RawMessage(content))
	})
}

func httpGetGitHubQueryRevision(options GitHubProjectRouteOptions) http.HandlerFunc {
	return withGitHubProjectSnapshot(options, func(w http.ResponseWriter, r *http.Request, snapshot *githubstore4datatug.ProjectSnapshot) {
		location := r.URL.Query().Get("id")
		if location == "" || strings.HasPrefix(location, "/") || strings.HasSuffix(location, "/") || strings.Contains(location, "\\") {
			sharedProjectError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		parts := strings.Split(location, "/")
		folder := "~"
		if len(parts) > 1 {
			folder = strings.Join(parts[:len(parts)-1], "/")
		}
		result, err := snapshot.ReadQueryRevision(r.Context(), folder, parts[len(parts)-1])
		if err != nil {
			githubReadError(w, err)
			return
		}
		writeGitHubSnapshotJSON(w, snapshot.Head, result)
	})
}
