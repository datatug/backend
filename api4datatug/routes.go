package api4datatug

import (
	"net/http"

	"github.com/datatug/backend/facade4datatug"
	"github.com/sneat-co/sneat-go-core/extension"
)

// RegisterHttpRoutes registers the DataTug module's HTTP routes.
//
// ids and githubOAuth are the host's adapters for the domain's ports; the host
// supplies them at composition time (sneat-go/pkg/modules/datatug). The OAuth
// exchanger is what keeps the GitHub client secret out of the browser and out
// of this repository.
//
// Route paths are literal: the host mounts them verbatim, with no module-id or
// /v0/ prefix added. These are the paths the datatug-apps client already calls.
func RegisterHttpRoutes(
	handle extension.HTTPHandleFunc,
	ids facade4datatug.IDGenerator,
	githubOAuth facade4datatug.GithubOAuthExchanger,
) {
	handle(http.MethodPost, "/v0/datatug/projects/create_project", httpPostCreateProject(ids, githubOAuth))
	handle(http.MethodPost, "/v0/datatug/projects/register_github_project", httpPostRegisterGithubProject(ids, githubOAuth))
	handle(http.MethodPost, "/v0/datatug/github/oauth_token", httpPostExchangeGithubOAuthCode(ids, githubOAuth))
}
