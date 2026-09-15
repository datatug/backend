package api4datatug

import (
	"net/http"

	"github.com/sneat-co/sneat-go-core/extension"
)

// RegisterHttpRoutes registers the DataTug module's HTTP routes.
//
// Route paths are literal: the host mounts them verbatim, with no module-ID or
// `/v0/` prefix added (see sneat-go's sneatgaeapp `handle`). They are the paths
// the datatug-apps client already calls.
func RegisterHttpRoutes(handle extension.HTTPHandleFunc) {
	handle(http.MethodPost, "/v0/datatug/projects/create_project", httpPostCreateProject)
}
