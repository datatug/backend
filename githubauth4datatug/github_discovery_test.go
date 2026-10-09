package githubauth4datatug

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func newDiscoveryUserClient(t *testing.T, transport http.RoundTripper) *githubUserClient {
	t.Helper()
	app, err := NewGitHubAppClient(GitHubAppConfig{
		AppID: DataTugGitHubAppID, ClientID: "client-id", ClientSecret: "client-secret",
		CallbackURL: "https://datatug.app/github/callback",
	}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	tokens := testTokens(time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	return app.UserClient(tokens).(*githubUserClient)
}

func TestGitHubUserClientPaginatesInstallationRepositories(t *testing.T) {
	var requested []string
	client := newDiscoveryUserClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requested = append(requested, request.URL.Path+"?"+request.URL.RawQuery)
		if request.URL.Path == "/user/installations" {
			return jsonResponse(`{"total_count":1,"installations":[{"id":501,"app_id":5223634,"suspended_at":null,"permissions":{"contents":"write"}}]}`), nil
		}
		if request.URL.Path != "/user/installations/501/repositories" {
			return nil, fmt.Errorf("unexpected path %s", request.URL.Path)
		}
		if request.URL.Query().Get("page") == "2" {
			return jsonResponse(`{"total_count":101,"repositories":[{"id":200,"node_id":"R_200","name":"repo200","full_name":"acme/repo200","default_branch":"main","owner":{"login":"acme"},"permissions":{"pull":true,"push":false,"admin":true}}]}`), nil
		}
		var body strings.Builder
		body.WriteString(`{"total_count":101,"repositories":[`)
		for i := 0; i < 100; i++ {
			if i > 0 {
				body.WriteByte(',')
			}
			fmt.Fprintf(&body, `{"id":%d,"node_id":"R_%d","name":"repo%d","full_name":"acme/repo%d","default_branch":"main","owner":{"login":"acme"},"permissions":{"pull":true,"push":true,"admin":false}}`, i+100, i+100, i+100, i+100)
		}
		body.WriteString(`]}`)
		return jsonResponse(body.String()), nil
	}))

	repositories, err := client.DataTugRepositories(context.Background())
	if err != nil || len(repositories) != 101 || repositories[0].EffectivePermission != RepositoryWrite || repositories[100].EffectivePermission != RepositoryWrite {
		t.Fatalf("DataTugRepositories() returned %d entries, err=%v", len(repositories), err)
	}
	if len(requested) != 3 || !strings.HasSuffix(requested[1], "page=1") || !strings.HasSuffix(requested[2], "page=2") {
		t.Fatalf("requests = %v, want installation page and two repository pages", requested)
	}
}

func TestGitHubUserClientFailsClosedOnPageExhaustionFailureAndCancellation(t *testing.T) {
	t.Run("configured inventory limit", func(t *testing.T) {
		client := newDiscoveryUserClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return jsonResponse(fmt.Sprintf(`{"total_count":%d,"installations":[]}`, maxGitHubInventoryItems+1)), nil
		}))
		if repositories, err := client.DataTugRepositories(context.Background()); !errors.Is(err, ErrGitHubRepositoryDenied) || repositories != nil {
			t.Fatalf("oversized installation inventory = (%v, %v), want no partial list", repositories, err)
		}
	})

	t.Run("later page failure", func(t *testing.T) {
		client := newDiscoveryUserClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path == "/user/installations" {
				return jsonResponse(`{"total_count":1,"installations":[{"id":501,"app_id":5223634,"suspended_at":null,"permissions":{"contents":"read"}}]}`), nil
			}
			if request.URL.Query().Get("page") == "2" {
				response := jsonResponse(`{}`)
				response.StatusCode = http.StatusServiceUnavailable
				return response, nil
			}
			var body strings.Builder
			body.WriteString(`{"total_count":101,"repositories":[`)
			for i := 0; i < 100; i++ {
				if i > 0 {
					body.WriteByte(',')
				}
				fmt.Fprintf(&body, `{"id":%d,"node_id":"R_%d","name":"repo%d","full_name":"acme/repo%d","default_branch":"main","owner":{"login":"acme"},"permissions":{"pull":true,"push":false,"admin":false}}`, i+100, i+100, i+100, i+100)
			}
			body.WriteString(`]}`)
			return jsonResponse(body.String()), nil
		}))
		if repositories, err := client.DataTugRepositories(context.Background()); !errors.Is(err, ErrGitHubRepositoryDenied) || repositories != nil {
			t.Fatalf("failed second page returned %d entries, err=%v", len(repositories), err)
		}
	})

	t.Run("canceled request", func(t *testing.T) {
		client := newDiscoveryUserClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, request.Context().Err()
		}))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if repositories, err := client.DataTugRepositories(ctx); !errors.Is(err, ErrGitHubRepositoryDenied) || repositories != nil {
			t.Fatalf("canceled inventory returned %v, err=%v", repositories, err)
		}
	})
}

func TestGitHubUserClientResolvesRepositoryByNameToCurrentImmutableID(t *testing.T) {
	client := newDiscoveryUserClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/repos/acme/private" {
			return nil, fmt.Errorf("unexpected selected-repository path %s", request.URL.Path)
		}
		return jsonResponse(`{"id":44,"node_id":"R_kgDOABC","name":"private","full_name":"acme/private","default_branch":"main","owner":{"login":"acme"},"permissions":{"pull":true,"push":false,"admin":false}}`), nil
	}))
	repo, err := client.RepositoryByName(context.Background(), "acme", "private")
	if err != nil || repo.ID != 44 || repo.Owner != "acme" || repo.Name != "private" {
		t.Fatalf("RepositoryByName() = (%+v, %v)", repo, err)
	}
}
