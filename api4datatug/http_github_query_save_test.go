package api4datatug

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubSaveQueryStrictlyDecodesSupportedBodyAndFailsClosed(t *testing.T) {
	valid := `{"storage":"github.com","project":"repo@owner@datatug","branch":"work","expectedBranchHead":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","operationId":"save-1","ifNoneMatch":true,"query":{"folderPath":"~","id":"customers","title":"Customers","type":"DTQL","text":"SELECT CustomerId FROM chinook.Customer","federation":{"ovdbBaseUrl":"https://demodb.dev/ovdb","tables":[{"database":"chinook","name":"Customer","fields":["CustomerId"]}]}}}`
	var request SaveGitHubQueryRequest
	if err := json.Unmarshal([]byte(valid), &request); err != nil || request.Validate() != nil || request.Query.Federation == nil {
		t.Fatalf("valid body %+v err=%v", request, err)
	}
	for _, body := range []string{
		strings.Replace(valid, `"fields":["CustomerId"]`, `"fields":["CustomerId"],"expectedServerIdentity":"secret"`, 1),
		strings.Replace(valid, `"ovdbBaseUrl":"https://demodb.dev/ovdb"`, `"ovdbBaseUrl":"https://demodb.dev/ovdb","bounds":{"limit":1}`, 1),
		strings.Replace(valid, `"operationId":"save-1"`, `"operationId":"save-1","actorID":"foreign"`, 1),
	} {
		if err := json.Unmarshal([]byte(body), &request); err == nil {
			t.Fatalf("unsupported field accepted: %s", body)
		}
	}
	w := httptest.NewRecorder()
	httpPostGitHubSaveQuery(GitHubProjectRouteOptions{})(w, httptest.NewRequest(http.MethodPost, "/v0/datatug/queries/save_query", strings.NewReader(valid)))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unconfigured route=%d", w.Code)
	}
}
