// Copyright 2026 https://datatug.io/

package githubauth4datatug

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestScopedRepositoryCommitUsesExpectedHeadAndSelectedRepository(t *testing.T) {
	ref := RepositoryRef{ID: 31, Owner: "acme", Name: "private"}
	var receivedURL, receivedAuthorization, receivedBody string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		receivedURL = request.URL.String()
		receivedAuthorization = request.Header.Get("Authorization")
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		receivedBody = string(body)
		return jsonResponse(`{"data":{"createCommitOnBranch":{"commit":{"oid":"1111111111111111111111111111111111111111","message":"save query","tree":{"oid":"2222222222222222222222222222222222222222"}}}}}`), nil
	})
	client := &http.Client{Transport: repoBoundRoundTripper{
		base: bearerRoundTripper{base: transport, token: "hidden-token"}, repository: ref,
		repositoryNodeID: "R_kgDOABC", operation: RepositoryWrite,
	}}
	repository := &AuthorizedGitHubRepository{repository: GitHubRepository{ID: ref.ID, NodeID: "R_kgDOABC", Owner: ref.Owner, Name: ref.Name}, permission: RepositoryWrite, client: client}
	content := []byte("query { ping }")
	commit, err := repository.CreateCommitOnBranch(context.Background(), "main", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "save query", []GitHubFileChange{{Path: "queries/query.json", Content: content}})
	if err != nil {
		t.Fatalf("CreateCommitOnBranch() error %v; url=%q body=%q", err, receivedURL, receivedBody)
	}
	if commit.OID != "1111111111111111111111111111111111111111" || !strings.Contains(receivedURL, "api.github.com/graphql") {
		t.Fatalf("commit = %+v, request URL = %q", commit, receivedURL)
	}
	if receivedAuthorization != "Bearer hidden-token" || strings.Contains(receivedBody, "hidden-token") {
		t.Fatalf("authorization header or GraphQL body is incorrect; token was exposed in body")
	}
	if !strings.Contains(receivedBody, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") || !strings.Contains(receivedBody, base64.StdEncoding.EncodeToString(content)) || !strings.Contains(receivedBody, "acme/private") {
		t.Fatalf("GraphQL mutation does not bind repo, expected head, and raw file bytes: %s", receivedBody)
	}
	var envelope struct {
		Variables struct {
			Input struct {
				ExpectedHeadOID string            `json:"expectedHeadOid"`
				Branch          map[string]string `json:"branch"`
			} `json:"input"`
		} `json:"variables"`
	}
	if err = json.Unmarshal([]byte(receivedBody), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Variables.Input.ExpectedHeadOID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || envelope.Variables.Input.Branch["branchName"] != "main" || envelope.Variables.Input.Branch["expectedHeadOid"] != "" {
		t.Fatalf("mutation input has incorrect CAS branch shape: %+v", envelope.Variables.Input)
	}
}

func TestScopedRepositoryReadOnlyCannotUpdateRefs(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return jsonResponse(`{"data":{"updateRefs":{"clientMutationId":null}}}`), nil
	})}
	repository := &AuthorizedGitHubRepository{permission: RepositoryRead, client: client}
	err := repository.UpdateRefs(context.Background(), []GitHubRefUpdate{{Name: "main", BeforeOID: strings.Repeat("a", 40), AfterOID: strings.Repeat("b", 40)}})
	if err != ErrGitHubPermissionDenied || called {
		t.Fatalf("UpdateRefs() error=%v, transport called=%t; want denied before transport", err, called)
	}
}

func TestPrivateRepositoryReadUsesActorCredentialAndNeverFallsBackToPublic(t *testing.T) {
	var requests int
	var authorization string
	client := &http.Client{Transport: repoBoundRoundTripper{
		base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			requests++
			authorization = request.Header.Get("Authorization")
			response := jsonResponse(`{"message":"Not Found"}`)
			response.StatusCode = http.StatusNotFound
			return response, nil
		}),
		repository: RepositoryRef{ID: 31, Owner: "acme", Name: "private"},
		operation:  RepositoryRead,
	}}
	client.Transport = bearerRoundTripper{base: client.Transport, token: "actor-token"}
	repository := &AuthorizedGitHubRepository{
		repository: GitHubRepository{ID: 31, Owner: "acme", Name: "private"},
		permission: RepositoryRead,
		client:     client,
	}
	if _, err := repository.GetBlob(context.Background(), strings.Repeat("a", 40)); err == nil {
		t.Fatal("private blob read unexpectedly succeeded without actor access")
	}
	if requests != 1 || authorization != "Bearer actor-token" {
		t.Fatalf("private read made %d requests with authorization %q; want one actor-authenticated request and no public fallback", requests, authorization)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
