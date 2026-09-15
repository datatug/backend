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
// /v0/ prefix added. This is the path the datatug-apps client already calls.
func RegisterHttpRoutes(handle extension.HTTPHandleFunc, ids facade4datatug.IDGenerator) {
	handle(http.MethodPost, "/v0/datatug/projects/create_project", httpPostCreateProject(ids))
}
