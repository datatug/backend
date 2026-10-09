package api4datatug

import (
	"errors"
	"net/http"
	"strings"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/githubauth4datatug"
	"github.com/datatug/backend/models4datatug"
	"github.com/datatug/backend/template4datatug"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
)

type GitHubProjectBranch struct {
	Name string `json:"name"`
	Head string `json:"head"`
}

type GitHubBranchCapabilities struct {
	BranchList   bool `json:"branchList"`
	BranchCreate bool `json:"branchCreate"`
	BranchDiff   bool `json:"branchDiff"`
	BranchMerge  bool `json:"branchMerge"`
}

type GitHubProjectBranchesResponse struct {
	Branches      []GitHubProjectBranch    `json:"branches"`
	DefaultBranch string                   `json:"defaultBranch"`
	CurrentBranch string                   `json:"currentBranch,omitempty"`
	Capabilities  GitHubBranchCapabilities `json:"capabilities"`
}

// Prospective folders need branch discovery before their first manifest. A
// ready registered folder additionally requires current linked-contact proof.
func httpGetGitHubProjectBranches(options GitHubProjectRouteOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		provider, service := options.provider(), options.service()
		if provider == nil || service == nil {
			sharedProjectError(w, http.StatusServiceUnavailable, "github_unavailable")
			return
		}
		query := r.URL.Query()
		if query.Get("storage") != models4datatug.GithubStoreID {
			sharedProjectError(w, http.StatusBadRequest, "invalid_store")
			return
		}
		repoName, owner, folder, ok := parseGitHubProjectKey(query.Get("project"))
		if !ok {
			sharedProjectError(w, http.StatusBadRequest, "invalid_project")
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
		selected, err := provider.ResolveRepositoryByName(ctx, actorID, owner, repoName)
		if err != nil {
			status, code := githubAuthorizationStatus(err)
			sharedProjectError(w, status, code)
			return
		}
		client, err := provider.AuthorizeReadRepository(ctx, actorID, githubauth4datatug.RepositoryRef{ID: selected.ID, Owner: selected.Owner, Name: selected.Name})
		if err != nil {
			status, code := githubAuthorizationStatus(err)
			sharedProjectError(w, status, code)
			return
		}
		access, err := service.ResolveGitHubProject(ctx, actorID, selected.ID, owner, repoName, folder)
		if err != nil && !errors.Is(err, facade4datatug.ErrGitHubProjectNotRegistered) {
			sharedProjectError(w, http.StatusForbidden, "project_denied")
			return
		}
		registered := err == nil
		branches, err := client.ListBranches(ctx)
		if err != nil {
			sharedProjectError(w, http.StatusServiceUnavailable, "github_unavailable")
			return
		}
		if selected.DefaultBranch == "" || len(branches) == 0 {
			sharedProjectError(w, http.StatusConflict, "initialization_required")
			return
		}
		response := GitHubProjectBranchesResponse{
			Branches:      make([]GitHubProjectBranch, 0, len(branches)),
			DefaultBranch: selected.DefaultBranch,
			Capabilities:  GitHubBranchCapabilities{BranchList: true},
		}
		if registered {
			response.CurrentBranch = access.Binding.Branch
		}
		for _, branch := range branches {
			response.Branches = append(response.Branches, GitHubProjectBranch{Name: branch.Name, Head: branch.OID})
		}
		writeGitHubJSON(w, http.StatusOK, response)
	}
}

func parseGitHubProjectKey(project string) (repo, owner, folder string, ok bool) {
	parts := strings.Split(project, "@")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || template4datatug.ValidateFolder(parts[2]) != nil {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}
