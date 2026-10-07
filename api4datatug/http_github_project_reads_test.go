package api4datatug

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubSnapshotHeaderIsReadableByBrowserAndRoutesFailClosed(t *testing.T) {
	w := httptest.NewRecorder()
	writeGitHubSnapshotJSON(w, "abc123", map[string]string{"id": "project"})
	response := w.Result()
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Datatug-Branch-Head") != "abc123" || !strings.Contains(response.Header.Get("Access-Control-Expose-Headers"), "X-Datatug-Branch-Head") || !strings.Contains(w.Body.String(), `"id":"project"`) {
		t.Fatalf("response status=%d header=%v body=%s", response.StatusCode, response.Header, w.Body.String())
	}
	for _, handler := range []http.HandlerFunc{httpGetGitHubProjectSummary(GitHubProjectRouteOptions{}), httpGetGitHubAllQueries(GitHubProjectRouteOptions{}), httpGetGitHubQueryRevision(GitHubProjectRouteOptions{})} {
		w = httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, "/?storage=github.com&project=repo@owner@datatug&branch=work", nil))
		if w.Code != http.StatusServiceUnavailable || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unconfigured route status=%d", w.Code)
		}
	}
}
