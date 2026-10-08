package api4datatug

import (
	"net/http"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/githubauth4datatug"
	"github.com/datatug/backend/models4datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
)

func httpGetGitHubProjectCapabilities(options GitHubProjectRouteOptions) http.HandlerFunc {
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
		repo, owner, folder, ok := parseGitHubProjectKey(query.Get("project"))
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
		selected, client, _, err := authorizeRegisteredGitHubProject(ctx, options, actorID, repo, owner, folder)
		if err != nil {
			githubReadError(w, err)
			return
		}
		projectAI, err := options.service().ReadGitHubProjectAIEligibility(ctx, actorID, selected.ID, owner, repo, folder)
		if err != nil {
			githubReadError(w, err)
			return
		}
		result := struct {
			dto.ProjectCapabilities
			ProjectAI facade4datatug.ProjectAIEligibility `json:"projectAI"`
		}{ProjectCapabilities: dto.ProjectCapabilities{QueryRead: true, Branches: true}, ProjectAI: projectAI}
		if client.Scope().Permission == githubauth4datatug.RepositoryWrite {
			_, err = options.service().AuthorizeGitHubProjectWrite(ctx, actorID, selected.ID, owner, repo, folder)
			result.QuerySave = err == nil
		}
		writeGitHubJSON(w, http.StatusOK, result)
	}
}
