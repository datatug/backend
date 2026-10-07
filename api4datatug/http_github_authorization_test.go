// Copyright 2026 https://datatug.io/

package api4datatug

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/datatug/backend/githubauth4datatug"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/sneat-co/sneat-go-core/facade"
)

func TestGitHubAuthorizationRoutesConnectThenListRepositories(t *testing.T) {
	originalDecode := verifyAuthenticatedRequestAndDecodeBody
	originalVerify := verifyAuthenticatedRequest
	t.Cleanup(func() {
		verifyAuthenticatedRequestAndDecodeBody = originalDecode
		verifyAuthenticatedRequest = originalVerify
	})
	verifyAuthenticatedRequestAndDecodeBody = func(w http.ResponseWriter, r *http.Request, _ verify.RequestOptions, request facade.Request) (facade.ContextWithUser, error) {
		if err := json.NewDecoder(r.Body).Decode(request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return nil, err
		}
		if err := request.Validate(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return nil, err
		}
		return githubTestUserContext(r), nil
	}
	verifyAuthenticatedRequest = func(_ http.ResponseWriter, r *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		return githubTestUserContext(r), nil
	}

	service := &fakeGitHubAuthorizationService{authorizationURL: "https://github.com/login/oauth/authorize?state=one-time-state", repositories: []githubauth4datatug.GitHubRepository{{
		ID: 91, NodeID: "R_kgDOABC", Owner: "acme", Name: "private", DefaultBranch: "develop",
		EffectivePermission: githubauth4datatug.RepositoryRead,
	}}}

	start := httptest.NewRecorder()
	httpPostStartGitHubAuthorization(GitHubAuthorizationRouteOptions{Provider: service})(start, httptest.NewRequest(http.MethodPost, "/v0/datatug/github/authorization/start", strings.NewReader(`{}`)))
	if start.Code != http.StatusOK || start.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("start status=%d headers=%v body=%s", start.Code, start.Header(), start.Body.String())
	}
	var startResponse StartGitHubAuthorizationResponse
	if err := json.Unmarshal(start.Body.Bytes(), &startResponse); err != nil {
		t.Fatal(err)
	}
	loginURL, err := url.Parse(startResponse.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}

	complete := httptest.NewRecorder()
	body := `{"code":"one-time-code","state":"` + loginURL.Query().Get("state") + `"}`
	httpPostCompleteGitHubAuthorization(GitHubAuthorizationRouteOptions{Provider: service})(complete, httptest.NewRequest(http.MethodPost, "/v0/datatug/github/authorization/complete", strings.NewReader(body)))
	if complete.Code != http.StatusOK || strings.Contains(complete.Body.String(), "one-time-code") {
		t.Fatalf("complete status=%d body=%s", complete.Code, complete.Body.String())
	}

	list := httptest.NewRecorder()
	httpGetGitHubRepositories(GitHubAuthorizationRouteOptions{Provider: service})(list, httptest.NewRequest(http.MethodGet, "/v0/datatug/github/repositories", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var response ListGitHubRepositoriesResponse
	if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Repositories) != 1 || response.Repositories[0].ID != 91 || response.Repositories[0].DefaultBranch != "develop" || response.Repositories[0].Permission != "read" {
		t.Fatalf("repository options = %+v; want read-only actor/App intersection", response.Repositories)
	}
	if service.firebaseUID != "firebase-actor" || service.completedCode != "one-time-code" || service.completedState != "one-time-state" {
		t.Fatalf("service calls uid=%q code=%q state=%q", service.firebaseUID, service.completedCode, service.completedState)
	}
}

func TestGitHubAuthorizationRoutesFailClosedWithoutProvider(t *testing.T) {
	options := GitHubAuthorizationRouteOptions{}
	for _, handler := range []http.HandlerFunc{
		httpPostStartGitHubAuthorization(options),
		httpPostCompleteGitHubAuthorization(options),
		httpGetGitHubRepositories(options),
	} {
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"unavailable"`) {
			t.Fatalf("unconfigured route status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestGitHubAuthorizationErrorsMapToStableHTTPStatus(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
		code string
	}{
		{name: "invalid OAuth state", err: githubauth4datatug.ErrOAuthStateInvalid, want: http.StatusBadRequest, code: "invalid_state"},
		{name: "actor mismatch", err: githubauth4datatug.ErrGitHubActorMismatch, want: http.StatusForbidden, code: "actor_mismatch"},
		{name: "repository denied", err: githubauth4datatug.ErrGitHubRepositoryDenied, want: http.StatusForbidden, code: "repository_denied"},
		{name: "permission denied", err: githubauth4datatug.ErrGitHubPermissionDenied, want: http.StatusForbidden, code: "repository_denied"},
		{name: "missing credential", err: githubauth4datatug.ErrCredentialMissing, want: http.StatusConflict, code: "reauthorization_required"},
		{name: "expired credential", err: githubauth4datatug.ErrReauthorizationRequired, want: http.StatusConflict, code: "reauthorization_required"},
		{name: "internal failure", err: errors.New("private provider error"), want: http.StatusServiceUnavailable, code: "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, code := githubAuthorizationStatus(test.err)
			if status != test.want || code != test.code {
				t.Fatalf("githubAuthorizationStatus() = (%d, %q), want (%d, %q)", status, code, test.want, test.code)
			}
		})
	}
}

func TestGitHubAuthorizationRoutesMapProviderFailuresWithoutDetails(t *testing.T) {
	originalDecode := verifyAuthenticatedRequestAndDecodeBody
	originalVerify := verifyAuthenticatedRequest
	t.Cleanup(func() {
		verifyAuthenticatedRequestAndDecodeBody = originalDecode
		verifyAuthenticatedRequest = originalVerify
	})
	verifyAuthenticatedRequest = func(_ http.ResponseWriter, r *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		return githubTestUserContext(r), nil
	}
	service := &fakeGitHubAuthorizationService{}
	options := GitHubAuthorizationRouteOptions{Provider: service}
	for _, test := range []struct {
		name    string
		handler http.HandlerFunc
		request *http.Request
		err     error
		status  int
	}{
		{name: "start unavailable", handler: httpPostStartGitHubAuthorization(options), request: httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)), err: errors.New("provider error must not escape"), status: http.StatusServiceUnavailable},
		{name: "complete invalid state", handler: httpPostCompleteGitHubAuthorization(options), request: httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"code":"secret-code","state":"opaque"}`)), err: githubauth4datatug.ErrOAuthStateInvalid, status: http.StatusBadRequest},
		{name: "repository listing needs reconnect", handler: httpGetGitHubRepositories(options), request: httptest.NewRequest(http.MethodGet, "/", nil), err: githubauth4datatug.ErrCredentialMissing, status: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			service.err = test.err
			response := httptest.NewRecorder()
			test.handler(response, test.request)
			if response.Code != test.status || strings.Contains(response.Body.String(), "provider error") || strings.Contains(response.Body.String(), "secret-code") {
				t.Fatalf("status=%d response=%q; want generic status %d", response.Code, response.Body.String(), test.status)
			}
		})
	}
	if _, err := verifiedFirebaseUID(nil); err == nil {
		t.Fatal("verifiedFirebaseUID() accepted missing verified context")
	}
}

func TestCompleteGitHubAuthorizationUsesStrictBoundedSecretSafeDecoder(t *testing.T) {
	originalDecode := verifyAuthenticatedRequestAndDecodeBody
	originalVerify := verifyAuthenticatedRequest
	t.Cleanup(func() {
		verifyAuthenticatedRequestAndDecodeBody = originalDecode
		verifyAuthenticatedRequest = originalVerify
	})
	verifyAuthenticatedRequestAndDecodeBody = func(http.ResponseWriter, *http.Request, verify.RequestOptions, facade.Request) (facade.ContextWithUser, error) {
		panic("credential-bearing OAuth request reached shared body decoder")
	}
	var authCalls int
	verifyAuthenticatedRequest = func(_ http.ResponseWriter, r *http.Request, options verify.RequestOptions) (facade.ContextWithUser, error) {
		authCalls++
		if !options.AuthenticationRequired() || options.MaximumContentLength() >= 0 {
			t.Fatalf("auth-only verification options = %+v; want authenticated with body bounds deferred to private decoder", options)
		}
		return githubTestUserContext(r), nil
	}
	service := &fakeGitHubAuthorizationService{}
	handler := httpPostCompleteGitHubAuthorization(GitHubAuthorizationRouteOptions{Provider: service})
	secretCode := "unique-oauth-code-secret"
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{"code":"` + secretCode + `","state":`},
		{name: "unknown field", body: `{"code":"` + secretCode + `","state":"opaque","secret":"do-not-echo"}`},
		{name: "missing code", body: `{"state":"opaque"}`},
		{name: "missing state", body: `{"code":"` + secretCode + `"}`},
		{name: "trailing JSON value", body: `{"code":"` + secretCode + `","state":"opaque"} {}`},
		{name: "oversized body", body: `{"code":"` + secretCode + `","state":"` + strings.Repeat("x", maxGitHubAuthorizationRequestBytes) + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://local-api.sneat.ws/v0/datatug/github/authorization/complete", strings.NewReader(test.body))
			response := httptest.NewRecorder()
			handler(response, request)
			if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), secretCode) || strings.Contains(response.Body.String(), "do-not-echo") {
				t.Fatalf("status=%d response=%q; want fixed error with no request contents", response.Code, response.Body.String())
			}
		})
	}
	valid := httptest.NewRequest(http.MethodPost, "http://local-api.sneat.ws/v0/datatug/github/authorization/complete", strings.NewReader(`{"code":"`+secretCode+`","state":"one-time-state"}`))
	response := httptest.NewRecorder()
	handler(response, valid)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), secretCode) || service.completedCode != secretCode {
		t.Fatalf("local callback status=%d response=%q service code=%q", response.Code, response.Body.String(), service.completedCode)
	}
	if authCalls != len(tests)+1 {
		t.Fatalf("auth verification calls=%d; want %d", authCalls, len(tests)+1)
	}
}

func TestVerifyAndDecodeGitHubAuthorizationRequestRejectsInvalidTransportShapes(t *testing.T) {
	originalVerify := verifyAuthenticatedRequest
	t.Cleanup(func() { verifyAuthenticatedRequest = originalVerify })
	verifyAuthenticatedRequest = func(_ http.ResponseWriter, r *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		return githubTestUserContext(r), nil
	}
	validBody := `{"code":"one-use","state":"opaque"}`
	tests := []struct {
		name    string
		request *http.Request
		decode  facade.Request
	}{
		{name: "wrong method", request: httptest.NewRequest(http.MethodGet, "/", strings.NewReader(validBody)), decode: &CompleteGitHubAuthorizationRequest{}},
		{name: "missing body", request: &http.Request{Method: http.MethodPost}, decode: &CompleteGitHubAuthorizationRequest{}},
		{name: "missing decoder target", request: httptest.NewRequest(http.MethodPost, "/", strings.NewReader(validBody))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			ctx, err := verifyAndDecodeGitHubAuthorizationRequest(response, test.request, test.decode)
			if err == nil || ctx != nil || response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "one-use") {
				t.Fatalf("decoder = (%v, %v), response=%d %q; want fixed bad request", ctx, err, response.Code, response.Body.String())
			}
		})
	}
}

func githubTestUserContext(r *http.Request) facade.ContextWithUser {
	return facade.NewContextWithUser(context.Background(), facade.NewUserContext("firebase-actor"))
}

type fakeGitHubAuthorizationService struct {
	authorizationURL string
	completedCode    string
	completedState   string
	firebaseUID      string
	repositories     []githubauth4datatug.GitHubRepository
	err              error
}

func (f *fakeGitHubAuthorizationService) BeginAuthorization(_ context.Context, uid string) (string, error) {
	f.firebaseUID = uid
	return f.authorizationURL, f.err
}
func (f *fakeGitHubAuthorizationService) CompleteAuthorization(_ context.Context, uid, state, code string) error {
	f.firebaseUID, f.completedState, f.completedCode = uid, state, code
	return f.err
}
func (f *fakeGitHubAuthorizationService) ListRepositories(_ context.Context, uid string) ([]githubauth4datatug.GitHubRepository, error) {
	f.firebaseUID = uid
	return append([]githubauth4datatug.GitHubRepository(nil), f.repositories...), f.err
}
