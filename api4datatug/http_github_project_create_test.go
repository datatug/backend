package api4datatug

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubCreateRequestRefusesUnknownNestedFields(t *testing.T) {
	valid := `{"title":"Queries","spaceID":"space","operationId":"op","github":{"repositoryID":123,"owner":"owner","name":"repo","folder":"datatug","branch":"work","expectedBranchHead":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"template":{"id":"demo-project-1","commit":"51716f3a4d682d5cb7ef70a7fd37f42e5418fd3d"}}`
	for _, body := range []string{
		strings.Replace(valid, `"folder":"datatug"`, `"folder":"datatug","accessToken":"secret"`, 1),
		strings.Replace(valid, `"title":"Queries"`, `"title":"Queries","unknownMetadata":{"bounds":[1,2]}`, 1),
		valid + ` {"title":"second"}`,
	} {
		var request CreateGitHubProjectRequest
		if err := json.Unmarshal([]byte(body), &request); err == nil {
			t.Fatalf("accepted unsupported request %s", body)
		}
	}
	var request CreateGitHubProjectRequest
	if err := json.Unmarshal([]byte(valid), &request); err != nil || request.Validate() != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
}

func TestGitHubCreateRouteFailsClosedWithoutHostBindings(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v0/datatug/projects/create_project?store=github.com", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	httpPostCreateGitHubProject(GitHubProjectRouteOptions{})(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unbound route status=%d headers=%v", response.Code, response.Header())
	}
}
