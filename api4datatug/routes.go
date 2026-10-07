package api4datatug

import (
	"net/http"

	"github.com/datatug/backend/facade4datatug"
	"github.com/sneat-co/sneat-go-core/extension"
)

// RegisterHttpRoutes registers the DataTug module's HTTP routes.
//
// ids is the host's adapter for the domain's IDGenerator port; the host supplies
// it at composition time (sneat-go/pkg/modules/datatug).
//
// Route paths are literal: the host mounts them verbatim, with no module-id or
// /v0/ prefix added. These are the paths the datatug-apps client already calls.
func RegisterHttpRoutes(handle extension.HTTPHandleFunc, ids facade4datatug.IDGenerator) {
	RegisterHttpRoutesWithPlan(handle, ids, PlanRouteOptions{})
}

// RegisterHttpRoutesWithPlan adds the personal plan route with host-injected
// verification and reads. An unbound route fails closed with 503.
func RegisterHttpRoutesWithPlan(handle extension.HTTPHandleFunc, ids facade4datatug.IDGenerator, plan PlanRouteOptions) {
	handle(http.MethodPost, "/v0/datatug/projects/create_project", httpPostCreateProject(ids))
	handle(http.MethodPost, "/v0/datatug/projects/register_github_project", httpPostRegisterGithubProject(ids))
	handle(http.MethodGet, "/v0/datatug/plan", httpGetPlan(plan))
}

// RouteOptions preserve the existing plan binding and explicitly opt a host
// into the new shared route. Old registration APIs retain their exact routes.
type RouteOptions struct {
	Plan                PlanRouteOptions
	SharedProjects      SharedProjectRouteOptions
	GitHubAuthorization GitHubAuthorizationRouteOptions
}

// RegisterHttpRoutesWithOptions mounts the shared command as well as legacy
// routes. An unbound shared service returns 503 without authentication/storage
// effects. Binding this route requires independently reviewed host composition.
func RegisterHttpRoutesWithOptions(handle extension.HTTPHandleFunc, ids facade4datatug.IDGenerator, options RouteOptions) {
	RegisterHttpRoutesWithPlan(handle, ids, options.Plan)
	handle(http.MethodPost, "/v0/datatug/projects/create_shared_project", httpPostCreateSharedProject(options.SharedProjects))
	handle(http.MethodPost, "/v0/datatug/github/authorization/start", httpPostStartGitHubAuthorization(options.GitHubAuthorization))
	handle(http.MethodPost, "/v0/datatug/github/authorization/complete", httpPostCompleteGitHubAuthorization(options.GitHubAuthorization))
	handle(http.MethodGet, "/v0/datatug/github/repositories", httpGetGitHubRepositories(options.GitHubAuthorization))
}
