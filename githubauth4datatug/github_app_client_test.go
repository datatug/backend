// Copyright 2026 https://datatug.io/

package githubauth4datatug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGitHubAppClientConfigurationAuthorizationAndOAuthExchange(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	validConfig := GitHubAppConfig{
		AppID: DataTugGitHubAppID, ClientID: "client-id", ClientSecret: "secret",
		CallbackURL: "https://datatug.app/github/callback",
	}
	for _, test := range []struct {
		name   string
		config GitHubAppConfig
	}{
		{name: "wrong app id", config: GitHubAppConfig{AppID: DataTugGitHubAppID + 1, ClientID: "client-id", ClientSecret: "secret", CallbackURL: validConfig.CallbackURL}},
		{name: "missing client id", config: GitHubAppConfig{AppID: DataTugGitHubAppID, ClientSecret: "secret", CallbackURL: validConfig.CallbackURL}},
		{name: "missing secret", config: GitHubAppConfig{AppID: DataTugGitHubAppID, ClientID: "client-id", CallbackURL: validConfig.CallbackURL}},
		{name: "insecure callback", config: GitHubAppConfig{AppID: DataTugGitHubAppID, ClientID: "client-id", ClientSecret: "secret", CallbackURL: "http://datatug.app/callback"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewGitHubAppClient(test.config, nil); !errors.Is(err, ErrGitHubAppNotConfigured) {
				t.Fatalf("NewGitHubAppClient() error = %v, want fail-closed configuration error", err)
			}
		})
	}

	var requests int
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.String() != githubOAuthTokenURL || request.Method != http.MethodPost {
			t.Fatalf("OAuth request = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Accept") != "application/json" || request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatalf("OAuth headers = %+v", request.Header)
		}
		form, err := url.ParseQuery(mustReadBody(t, request))
		if err != nil {
			t.Fatal(err)
		}
		if form.Get("client_id") != "client-id" || form.Get("client_secret") != "secret" {
			t.Fatalf("OAuth client credentials were not sent to the token endpoint")
		}
		if form.Get("code") != "one-use" || form.Get("code_verifier") != "pkce-verifier" || form.Get("redirect_uri") != validConfig.CallbackURL {
			t.Fatalf("OAuth exchange does not bind code, verifier and callback: %v", form)
		}
		return jsonResponse(`{"access_token":"access","refresh_token":"refresh","expires_in":3600,"refresh_token_expires_in":7200}`), nil
	})
	client, err := NewGitHubAppClient(validConfig, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return now }
	if _, err = client.Exchange(context.Background(), "", "pkce-verifier"); !errors.Is(err, ErrOAuthStateInvalid) {
		t.Fatalf("Exchange empty code error = %v", err)
	}
	if _, err = client.Exchange(context.Background(), "one-use", ""); !errors.Is(err, ErrOAuthStateInvalid) {
		t.Fatalf("Exchange empty verifier error = %v", err)
	}
	tokens, err := client.Exchange(context.Background(), "one-use", "pkce-verifier")
	if err != nil || tokens.accessExpiresAt != now.Add(time.Hour) || tokens.refreshExpiresAt != now.Add(2*time.Hour) {
		t.Fatalf("Exchange() = (%v, %v), want validated expiring tokens", tokens, err)
	}
	if requests != 1 {
		t.Fatalf("OAuth requests = %d, want one valid exchange", requests)
	}
	if _, err = client.Refresh(context.Background(), ""); !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("Refresh empty token error = %v", err)
	}
	refreshTransport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		form, readErr := url.ParseQuery(mustReadBody(t, request))
		if readErr != nil || form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "prior-refresh" || form.Get("code") != "" {
			t.Fatalf("refresh request does not use the rotating refresh credential: %v, %v", form, readErr)
		}
		return jsonResponse(`{"access_token":"next-access","refresh_token":"next-refresh","expires_in":1800,"refresh_token_expires_in":3600}`), nil
	})
	refreshClient, err := NewGitHubAppClient(validConfig, &http.Client{Transport: refreshTransport})
	if err != nil {
		t.Fatal(err)
	}
	refreshClient.now = func() time.Time { return now }
	rotated, err := refreshClient.Refresh(context.Background(), "prior-refresh")
	if err != nil || rotated.accessExpiresAt != now.Add(30*time.Minute) || rotated.refreshExpiresAt != now.Add(time.Hour) {
		t.Fatalf("Refresh() = (%v, %v), want new expiring access/refresh tokens", rotated, err)
	}

	authorizationURL, err := client.AuthorizationURL("one-time-state", "challenge", 44)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.Path != "/login/oauth/authorize" || query.Get("client_id") != "client-id" || query.Get("redirect_uri") != validConfig.CallbackURL || query.Get("state") != "one-time-state" || query.Get("code_challenge") != "challenge" || query.Get("code_challenge_method") != "S256" || query.Get("repository_id") != "44" {
		t.Fatalf("AuthorizationURL() = %q", authorizationURL)
	}
	if _, err = client.AuthorizationURL("", "challenge", 0); !errors.Is(err, ErrGitHubAppNotConfigured) {
		t.Fatalf("AuthorizationURL invalid state error = %v", err)
	}
	if _, err = client.AuthorizationURL("state", "challenge", -1); !errors.Is(err, ErrGitHubAppNotConfigured) {
		t.Fatalf("AuthorizationURL invalid repo error = %v", err)
	}
}

func TestGitHubAppClientRejectsInvalidOAuthResponsesAndRedirects(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		body      string
		transport error
	}{
		{name: "server error", status: http.StatusUnauthorized, body: `{"error":"bad_verification_code"}`},
		{name: "malformed json", status: http.StatusOK, body: `{`},
		{name: "provider error", status: http.StatusOK, body: `{"error":"bad_verification_code","expires_in":3600,"refresh_token_expires_in":7200}`},
		{name: "missing tokens", status: http.StatusOK, body: `{"expires_in":3600,"refresh_token_expires_in":7200}`},
		{name: "non-expiring token", status: http.StatusOK, body: `{"access_token":"access","refresh_token":"refresh","expires_in":0,"refresh_token_expires_in":7200}`},
		{name: "redirect", status: http.StatusFound, body: `{"access_token":"access","refresh_token":"refresh","expires_in":3600,"refresh_token_expires_in":7200}`},
		{name: "network failure", transport: errors.New("private transport failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				if test.transport != nil {
					return nil, test.transport
				}
				response := jsonResponse(test.body)
				response.StatusCode = test.status
				return response, nil
			})
			client, err := NewGitHubAppClient(GitHubAppConfig{AppID: DataTugGitHubAppID, ClientID: "id", ClientSecret: "secret", CallbackURL: "https://datatug.app/github/callback"}, &http.Client{Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = client.Exchange(context.Background(), "code", "verifier"); !errors.Is(err, ErrReauthorizationRequired) {
				t.Fatalf("Exchange() error = %v, want reauthorization required", err)
			}
		})
	}
}

func TestGitHubUserClientListsOnlyDataTugInstallationRepositoriesAndIntersectsPermissions(t *testing.T) {
	var requests []string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.Path+"?"+request.URL.RawQuery)
		if request.Header.Get("Authorization") != "Bearer access-secret" {
			t.Fatalf("request authorization = %q", request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/user":
			return jsonResponse(`{"id":101,"login":"alice"}`), nil
		case "/repos/acme/private":
			return jsonResponse(`{"id":44,"node_id":"R_kgDOABC","name":"private","full_name":"acme/private","default_branch":"main","owner":{"login":"acme"},"permissions":{"pull":true,"push":true}}`), nil
		case "/user/installations":
			return jsonResponse(`{"total_count":4,"installations":[{"id":501,"app_id":5223634,"suspended_at":null,"permissions":{"contents":"write"}},{"id":502,"app_id":123,"suspended_at":null,"permissions":{"contents":"write"}},{"id":503,"app_id":5223634,"suspended_at":null,"permissions":{"contents":"read"}},{"id":504,"app_id":5223634,"suspended_at":"2026-10-01T00:00:00Z","permissions":{"contents":"write"}}]}`), nil
		case "/user/installations/501/repositories":
			return jsonResponse(`{"total_count":2,"repositories":[{"id":44,"node_id":"R_kgDOABC","name":"private","full_name":"acme/private","default_branch":"main","owner":{"login":"acme"},"permissions":{"pull":true,"push":true,"admin":false}},{"id":46,"node_id":"R_kgDOABF","name":"user-read-only","full_name":"acme/user-read-only","default_branch":"main","owner":{"login":"acme"},"permissions":{"pull":true,"push":false,"admin":false}}]}`), nil
		case "/user/installations/503/repositories":
			return jsonResponse(`{"total_count":1,"repositories":[{"id":45,"node_id":"R_kgDOABD","name":"read-only","full_name":"acme/read-only","default_branch":"main","owner":{"login":"acme"},"permissions":{"pull":true,"push":true,"admin":false}}]}`), nil
		default:
			t.Fatalf("unexpected GitHub API path %q", request.URL.Path)
			return nil, errors.New("unexpected path")
		}
	})
	appClient, err := NewGitHubAppClient(GitHubAppConfig{AppID: DataTugGitHubAppID, ClientID: "id", ClientSecret: "secret", CallbackURL: "https://datatug.app/github/callback"}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	tokens := testTokens(time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	user := appClient.UserClient(tokens)
	actor, err := user.CurrentUser(context.Background())
	if err != nil || actor != (GitHubActor{ID: 101, Login: "alice"}) {
		t.Fatalf("CurrentUser() = (%+v, %v)", actor, err)
	}
	ref := RepositoryRef{ID: 44, Owner: "acme", Name: "private"}
	repository, err := user.Repository(context.Background(), ref)
	if err != nil || repository.DefaultBranch != "main" || !repository.Permissions.Push {
		t.Fatalf("Repository() = (%+v, %v)", repository, err)
	}
	if _, err = user.Repository(context.Background(), RepositoryRef{ID: 45, Owner: "acme", Name: "private"}); !errors.Is(err, ErrGitHubRepositoryDenied) {
		t.Fatalf("Repository immutable ID mismatch error = %v", err)
	}
	repositories, err := user.DataTugRepositories(context.Background())
	if err != nil || len(repositories) != 3 || repositories[0].ID != ref.ID || repositories[0].EffectivePermission != RepositoryWrite || repositories[1].EffectivePermission != RepositoryRead || repositories[2].EffectivePermission != RepositoryRead {
		t.Fatalf("DataTugRepositories() returned %+v, err=%v", repositories, err)
	}
	if len(requests) != 6 || requests[3] != "/user/installations?per_page=100&page=1" || requests[4] != "/user/installations/501/repositories?per_page=100&page=1" || requests[5] != "/user/installations/503/repositories?per_page=100&page=1" {
		t.Fatalf("GitHub API requests = %v", requests)
	}
}

func TestGitHubUserClientRejectsMalformedRepositoryListings(t *testing.T) {
	for _, test := range []struct {
		name      string
		path      string
		status    int
		body      string
		transport error
	}{
		{name: "API denial", path: "/user/installations", status: http.StatusForbidden, body: `{}`},
		{name: "invalid JSON", path: "/user/installations", status: http.StatusOK, body: `[`},
		{name: "missing suspension state", path: "/user/installations", status: http.StatusOK, body: `{"total_count":1,"installations":[{"id":501,"app_id":5223634,"permissions":{"contents":"write"}}]}`},
		{name: "trailing JSON", path: "/user/installations", status: http.StatusOK, body: `{"total_count":0,"installations":[]} {}`},
		{name: "oversized response", path: "/user/installations", status: http.StatusOK, body: `{"total_count":0,"installations":[]}` + strings.Repeat(" ", maxGitHubResponseBytes+1)},
		{name: "incomplete installation page", path: "/user/installations", status: http.StatusOK, body: `{"total_count":1,"installations":[]}`},
		{name: "missing immutable node ID", path: "/user/installations/501/repositories", status: http.StatusOK, body: `{"total_count":1,"repositories":[{"id":44,"name":"private","full_name":"acme/private","default_branch":"main","owner":{"login":"acme"},"permissions":{"pull":true,"push":true,"admin":false}}]}`},
		{name: "invalid repository name", path: "/user/installations/501/repositories", status: http.StatusOK, body: `{"total_count":1,"repositories":[{"id":44,"node_id":"R_kgDOABC","name":"bad/name","full_name":"acme/bad/name","default_branch":"main","owner":{"login":"acme"},"permissions":{"pull":true,"push":true,"admin":false}}]}`},
		{name: "missing default branch metadata", path: "/user/installations/501/repositories", status: http.StatusOK, body: `{"total_count":1,"repositories":[{"id":44,"node_id":"R_kgDOABC","name":"private","full_name":"acme/private","owner":{"login":"acme"},"permissions":{"pull":true,"push":true,"admin":false}}]}`},
		{name: "invalid default branch", path: "/user/installations/501/repositories", status: http.StatusOK, body: `{"total_count":1,"repositories":[{"id":44,"node_id":"R_kgDOABC","name":"private","full_name":"acme/private","default_branch":"bad branch","owner":{"login":"acme"},"permissions":{"pull":true,"push":true,"admin":false}}]}`},
		{name: "missing user permissions", path: "/user/installations/501/repositories", status: http.StatusOK, body: `{"total_count":1,"repositories":[{"id":44,"node_id":"R_kgDOABC","name":"private","full_name":"acme/private","default_branch":"main","owner":{"login":"acme"},"permissions":{"pull":true,"push":true}}]}`},
		{name: "network failure", path: "/user/installations", transport: errors.New("private transport failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewGitHubAppClient(GitHubAppConfig{
				AppID: DataTugGitHubAppID, ClientID: "client-id", ClientSecret: "client-secret",
				CallbackURL: "https://datatug.app/github/callback",
			}, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != "/user/installations" && request.URL.Path != "/user/installations/501/repositories" {
					t.Fatalf("repository listing requested unexpected path %q", request.URL.Path)
				}
				if test.transport != nil {
					return nil, test.transport
				}
				body := test.body
				if request.URL.Path != test.path {
					body = `{"total_count":1,"installations":[{"id":501,"app_id":5223634,"suspended_at":null,"permissions":{"contents":"write"}}]}`
				}
				response := jsonResponse(body)
				response.StatusCode = test.status
				return response, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			repositories, err := client.UserClient(testTokens(time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))).DataTugRepositories(context.Background())
			if !errors.Is(err, ErrGitHubRepositoryDenied) || repositories != nil {
				t.Fatalf("DataTugRepositories() = (%+v, %v), want no partial authorization", repositories, err)
			}
		})
	}
}

func TestRepoBoundTransportAllowsOnlySelectedRepositoryOperations(t *testing.T) {
	ref := RepositoryRef{ID: 44, Owner: "acme", Name: "private"}
	readQuery := `{"operationName":"GetRepo","query":"query GetRepo { repository(owner: \"acme\", name: \"private\") { id } }","variables":{"input":{"owner":"acme","name":"private"}}}`
	writeCommit := `{"operationName":"CreateCommitOnBranch","query":"mutation CreateCommitOnBranch { createCommitOnBranch(input: $input) { commit { oid } } }","variables":{"input":{"branch":{"repositoryNameWithOwner":"acme/private"},"expectedHeadOid":"` + strings.Repeat("a", 40) + `"}}}`
	updateRefs := `{"operationName":"UpdateRefs","query":"mutation UpdateRefs { updateRefs(input: $input) { clientMutationId } }","variables":{"input":{"repositoryId":"R_kgDOABC","refUpdates":[{"name":"refs/heads/main","beforeOid":"a","afterOid":"b","force":false}]}}}`
	createRef := `{"operationName":"CreateRef","query":"mutation CreateRef { createRef(input: $input) { ref { id } } }","variables":{"input":{"repositoryId":"R_kgDOABC","name":"refs/heads/main","oid":"a"}}}`
	tests := []struct {
		name      string
		method    string
		url       string
		operation RepositoryOperation
		body      string
		allowed   bool
	}{
		{name: "selected repository read", method: http.MethodGet, url: "https://api.github.com/repos/acme/private/git/trees/main", operation: RepositoryRead, allowed: true},
		{name: "different repository", method: http.MethodGet, url: "https://api.github.com/repos/acme/other/git/trees/main", operation: RepositoryRead},
		{name: "non GitHub host", method: http.MethodGet, url: "https://api.github.test/repos/acme/private/git/trees/main", operation: RepositoryRead},
		{name: "read cannot post", method: http.MethodPost, url: "https://api.github.com/repos/acme/private/git/refs", operation: RepositoryRead},
		{name: "write selected repository mutation", method: http.MethodPost, url: "https://api.github.com/repos/acme/private/git/refs", operation: RepositoryWrite, allowed: true},
		{name: "read client cannot post GraphQL", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryRead, body: readQuery},
		{name: "write-scoped client can issue selected repository query", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: readQuery, allowed: true},
		{name: "write actor cannot mutate through read client", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryRead, body: writeCommit},
		{name: "selected repository create commit", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: writeCommit, allowed: true},
		{name: "nested commit target in variables array is scoped", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: `{"operationName":"CreateCommitOnBranch","query":"mutation CreateCommitOnBranch { createCommitOnBranch(input: $input) { commit { oid } } }","variables":{"input":{"changes":[{"target":{"repositoryNameWithOwner":"acme/private"}}],"expectedHeadOid":"` + strings.Repeat("a", 40) + `"}}}`, allowed: true},
		{name: "nested commit target for another repository is denied", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: `{"operationName":"CreateCommitOnBranch","query":"mutation CreateCommitOnBranch { createCommitOnBranch(input: $input) { commit { oid } } }","variables":{"input":{"changes":[{"target":{"repositoryNameWithOwner":"acme/other"}}],"expectedHeadOid":"` + strings.Repeat("a", 40) + `"}}}`},
		{name: "nested selected repository query in variables array is scoped", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: `{"operationName":"GetRepo","query":"query GetRepo { repository { id } }","variables":{"filters":[{"target":{"owner":"acme","name":"private"}}]}}`, allowed: true},
		{name: "nested query for another repository is denied", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: `{"operationName":"GetRepo","query":"query GetRepo { repository { id } }","variables":{"filters":[{"target":{"owner":"acme","name":"other"}}]}}`},
		{name: "create commit without operation name is parsed from query", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: strings.Replace(writeCommit, `"operationName":"CreateCommitOnBranch"`, `"operationName":""`, 1), allowed: true},
		{name: "create commit cannot target another repository", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: strings.Replace(writeCommit, "acme/private", "acme/other", 1)},
		{name: "create commit requires expected head", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: strings.Replace(writeCommit, `,"expectedHeadOid":"`+strings.Repeat("a", 40)+`"`, "", 1)},
		{name: "selected repository multi ref CAS", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: updateRefs, allowed: true},
		{name: "query cannot smuggle force ref update", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: strings.Replace(updateRefs, `"force":false`, `"force":true`, 1)},
		{name: "multi ref CAS requires pinned repository ID", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: strings.Replace(updateRefs, "R_kgDOABC", "R_kgDOOTHER", 1)},
		{name: "multi ref CAS requires at least one ref", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: strings.Replace(updateRefs, `"refUpdates":[{"name":"refs/heads/main","beforeOid":"a","afterOid":"b","force":false}]`, `"refUpdates":[]`, 1)},
		{name: "multi ref CAS rejects malformed ref update", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: strings.Replace(updateRefs, `"refUpdates":[{"name":"refs/heads/main","beforeOid":"a","afterOid":"b","force":false}]`, `"refUpdates":[null]`, 1)},
		{name: "selected repository create ref", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: createRef, allowed: true},
		{name: "create ref rejects another repository", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: strings.Replace(createRef, "R_kgDOABC", "R_kgDOOTHER", 1)},
		{name: "unrecognized GraphQL mutation is denied", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: `{"operationName":"DeleteRepository","query":"mutation DeleteRepository { deleteRepository(input: $input) { clientMutationId } }","variables":{"input":{"owner":"acme","name":"private"}}}`},
		{name: "malformed GraphQL JSON is denied", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite, body: `not-json`},
		{name: "GraphQL GET is denied", method: http.MethodGet, url: "https://api.github.com/graphql", operation: RepositoryRead, body: readQuery},
		{name: "empty GraphQL body", method: http.MethodPost, url: "https://api.github.com/graphql", operation: RepositoryWrite},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var roundTrips int
			transport := repoBoundRoundTripper{
				base: roundTripFunc(func(*http.Request) (*http.Response, error) {
					roundTrips++
					return jsonResponse(`{}`), nil
				}),
				repository: ref, repositoryNodeID: "R_kgDOABC", operation: test.operation,
			}
			request, err := http.NewRequest(test.method, test.url, strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			_, err = transport.RoundTrip(request)
			if (err == nil) != test.allowed || (roundTrips == 1) != test.allowed {
				t.Fatalf("RoundTrip() error=%v base calls=%d allowed=%t", err, roundTrips, test.allowed)
			}
		})
	}
	if got := graphQLOperationName("mutation UpdateRefs($input: Input!) { updateRefs(input: $input) }"); got != "UpdateRefs" {
		t.Fatalf("graphQLOperationName() = %q", got)
	}
	if got := graphQLOperationName("query { viewer { login } }"); got != "" {
		t.Fatalf("anonymous graphQLOperationName() = %q", got)
	}
}

func TestGitHubTokenValueFormattingNeverExposesCredentials(t *testing.T) {
	tokens := testTokens(time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	for label, got := range map[string]string{
		"String": tokens.String(), "GoString": tokens.GoString(),
		"fmt Stringer": fmt.Sprint(tokens), "fmt GoStringer": fmt.Sprintf("%#v", tokens),
	} {
		if strings.Contains(got, "access-secret") || strings.Contains(got, "refresh-secret") {
			t.Errorf("%s() leaked OAuth token: %q", label, got)
		}
	}
	encoded, err := json.Marshal(tokens)
	if err != nil || string(encoded) != `{"redacted":true}` {
		t.Fatalf("MarshalJSON() = %s, %v", encoded, err)
	}
}

func TestScopedHTTPClientBindsActorTokenAndStopsRedirects(t *testing.T) {
	var requests int
	var authorization string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		authorization = request.Header.Get("Authorization")
		response := jsonResponse(`{}`)
		response.StatusCode = http.StatusFound
		response.Header.Set("Location", "https://attacker.example/private")
		return response, nil
	})
	app, err := NewGitHubAppClient(GitHubAppConfig{AppID: DataTugGitHubAppID, ClientID: "id", ClientSecret: "secret", CallbackURL: "https://datatug.app/github/callback"}, &http.Client{Transport: transport, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	repository := RepositoryRef{ID: 44, Owner: "acme", Name: "private"}
	client := app.ScopedHTTPClient(testTokens(time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)), repository, "R_kgDOABC", RepositoryRead)
	response, err := client.Get("https://api.github.com/repos/acme/private/git/refs/heads/main")
	if err != nil {
		t.Fatalf("scoped GET with non-followed response error = %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusFound || requests != 1 || authorization != "Bearer access-secret" {
		t.Fatalf("scoped response status=%d requests=%d authorization=%q", response.StatusCode, requests, authorization)
	}
	if _, err = client.Get("https://api.github.com/repos/acme/other/git/refs/heads/main"); !errors.Is(err, ErrGitHubRepositoryDenied) {
		t.Fatalf("scoped client access to another repository error = %v", err)
	}
	if strings.Contains(app.String(), "secret") {
		t.Fatalf("GitHub App client String() exposed config: %q", app.String())
	}
}

func mustReadBody(t *testing.T, request *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
