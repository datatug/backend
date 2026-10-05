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
