package datatugext

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datatug/backend/api4datatug"
	"github.com/datatug/backend/const4datatug"
	"github.com/sneat-co/sneat-go-core/extension"
)

func TestSharedProjectExtensionOptionsAreAdditive(t *testing.T) {
	config := ExtensionWithOptions(fakeIDs{}, api4datatug.RouteOptions{})
	extension.AssertExtension(t, config, extension.Expected{ExtID: const4datatug.ExtensionID, HandlersCount: 17})

	routes := make(map[string]http.HandlerFunc)
	config.Register(extension.NewModuleRegistrationArgs(func(method, path string, handler http.HandlerFunc) {
		route := method + " " + path
		if _, exists := routes[route]; exists {
			t.Errorf("duplicate route registration: %s", route)
		}
		routes[route] = handler
	}, nil))
	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/v0/datatug/projects/query_activity_context"},
		{method: http.MethodPost, path: "/v0/datatug/projects/query_activity_report"},
	} {
		key := route.method + " " + route.path
		handler := routes[key]
		if handler == nil {
			t.Fatalf("missing query-activity route %s", key)
		}
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusServiceUnavailable || response.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("unconfigured query-activity route %s returned status=%d headers=%v; want fail-closed 503 with no-store", key, response.Code, response.Header())
		}
	}
}
