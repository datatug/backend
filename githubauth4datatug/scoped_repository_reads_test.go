// Copyright 2026 https://datatug.io/

package githubauth4datatug

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestScopedRepositoryReadsBranchesRefsTreesAndBlobs(t *testing.T) {
	const commitOID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const treeOID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const blobOID = "cccccccccccccccccccccccccccccccccccccccc"
	var requests []string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.Path+"?"+request.URL.RawQuery)
		switch request.URL.Path {
		case "/repos/acme/private/branches":
			if request.URL.Query().Get("page") == "1" {
				return jsonResponse(`[{"name":"main","commit":{"sha":"` + commitOID + `"}}]`), nil
			}
			return jsonResponse(`[]`), nil
		case "/repos/acme/private/git/ref/heads/feature/x":
			return jsonResponse(`{"ref":"refs/heads/feature/x","object":{"sha":"` + commitOID + `"}}`), nil
		case "/repos/acme/private/git/trees/" + treeOID:
			if request.URL.Query().Get("recursive") != "1" {
				t.Fatalf("tree request missing recursive traversal: %s", request.URL)
			}
			return jsonResponse(`{"truncated":false,"tree":[{"path":"queries/sub/query.dtql","mode":"100644","type":"blob","sha":"` + blobOID + `","size":12},{"path":"queries/sub","mode":"040000","type":"tree","sha":"` + treeOID + `","size":0}]}`), nil
		case "/repos/acme/private/git/blobs/" + blobOID:
			return jsonResponse(`{"sha":"` + blobOID + `","encoding":"base64","content":"cXVlcnkgeyBwaW5nIH0="}`), nil
		default:
			t.Fatalf("unexpected scoped API request %s", request.URL)
			return nil, errors.New("unexpected request")
		}
	})}
	repository := &AuthorizedGitHubRepository{
		repository: GitHubRepository{ID: 31, NodeID: "R_kgDOABC", Owner: "acme", Name: "private"},
		permission: RepositoryRead, client: client,
	}
	branches, err := repository.ListBranches(context.Background())
	if err != nil || len(branches) != 1 || branches[0] != (GitHubBranch{Name: "main", OID: commitOID}) {
		t.Fatalf("ListBranches() = (%+v, %v)", branches, err)
	}
	ref, err := repository.GetRef(context.Background(), "feature/x")
	if err != nil || ref != (GitHubRef{Name: "feature/x", OID: commitOID}) {
		t.Fatalf("GetRef() = (%+v, %v)", ref, err)
	}
	tree, err := repository.GetTree(context.Background(), treeOID)
	if err != nil || len(tree) != 2 || tree[0].Path != "queries/sub/query.dtql" || tree[0].Type != "blob" || tree[0].OID != blobOID || tree[1].Type != "tree" {
		t.Fatalf("GetTree() = (%+v, %v)", tree, err)
	}
	blob, err := repository.GetBlob(context.Background(), blobOID)
	if err != nil || string(blob) != "query { ping }" {
		t.Fatalf("GetBlob() = (%q, %v)", blob, err)
	}
	if len(requests) != 4 {
		t.Fatalf("scoped read requests = %v, want branch/ref/tree/blob", requests)
	}
}

func TestScopedRepositoryRejectsIncompleteTreeAndMalformedObjects(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		body string
		call func(*AuthorizedGitHubRepository) error
	}{
		{name: "bad branch SHA", path: "/repos/acme/private/branches", body: `[{"name":"main","commit":{"sha":"bad"}}]`, call: func(r *AuthorizedGitHubRepository) error { _, err := r.ListBranches(context.Background()); return err }},
		{name: "ref mismatch", path: "/repos/acme/private/git/ref/heads/main", body: `{"ref":"refs/heads/other","object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`, call: func(r *AuthorizedGitHubRepository) error {
			_, err := r.GetRef(context.Background(), "main")
			return err
		}},
		{name: "truncated tree", path: "/repos/acme/private/git/trees/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", body: `{"truncated":true,"tree":[]}`, call: func(r *AuthorizedGitHubRepository) error {
			_, err := r.GetTree(context.Background(), strings.Repeat("a", 40))
			return err
		}},
		{name: "unsafe tree path", path: "/repos/acme/private/git/trees/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", body: `{"truncated":false,"tree":[{"path":"../secret","type":"blob","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}`, call: func(r *AuthorizedGitHubRepository) error {
			_, err := r.GetTree(context.Background(), strings.Repeat("a", 40))
			return err
		}},
		{name: "blob encoding", path: "/repos/acme/private/git/blobs/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", body: `{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","encoding":"utf-8","content":"secret"}`, call: func(r *AuthorizedGitHubRepository) error {
			_, err := r.GetBlob(context.Background(), strings.Repeat("a", 40))
			return err
		}},
		{name: "blob base64", path: "/repos/acme/private/git/blobs/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", body: `{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","encoding":"base64","content":"%%%"}`, call: func(r *AuthorizedGitHubRepository) error {
			_, err := r.GetBlob(context.Background(), strings.Repeat("a", 40))
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != test.path {
					t.Fatalf("request path = %q, want %q", request.URL.Path, test.path)
				}
				return jsonResponse(test.body), nil
			})}
			repository := &AuthorizedGitHubRepository{
				repository: GitHubRepository{ID: 31, Owner: "acme", Name: "private"}, permission: RepositoryRead, client: client,
			}
			if err := test.call(repository); !errors.Is(err, ErrGitHubRepositoryDenied) {
				t.Fatalf("malformed object error = %v, want repository denied", err)
			}
		})
	}
	if _, err := (*AuthorizedGitHubRepository)(nil).GetTree(context.Background(), strings.Repeat("a", 40)); !errors.Is(err, ErrGitHubRepositoryDenied) {
		t.Fatalf("nil repository tree error = %v", err)
	}
}

func TestScopedRepositoryUpdateRefsUsesMultiRefNonForceCAS(t *testing.T) {
	var receivedBody string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		receivedBody = string(body)
		return jsonResponse(`{"data":{"updateRefs":{"clientMutationId":"accepted"}}}`), nil
	})}
	repository := &AuthorizedGitHubRepository{
		repository: GitHubRepository{ID: 31, NodeID: "R_kgDOABC", Owner: "acme", Name: "private"},
		permission: RepositoryWrite, client: client,
	}
	updates := []GitHubRefUpdate{
		{Name: "source", BeforeOID: strings.Repeat("a", 40), AfterOID: strings.Repeat("b", 40)},
		{Name: "target", BeforeOID: strings.Repeat("c", 40), AfterOID: strings.Repeat("d", 40)},
	}
	if err := repository.UpdateRefs(context.Background(), updates); err != nil {
		t.Fatalf("UpdateRefs() = %v", err)
	}
	var envelope struct {
		OperationName string `json:"operationName"`
		Variables     struct {
			Input struct {
				RepositoryID string `json:"repositoryId"`
				RefUpdates   []struct {
					Name      string `json:"name"`
					BeforeOID string `json:"beforeOid"`
					AfterOID  string `json:"afterOid"`
					Force     bool   `json:"force"`
				} `json:"refUpdates"`
			} `json:"input"`
		} `json:"variables"`
	}
	if err := json.Unmarshal([]byte(receivedBody), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OperationName != "UpdateRefs" || envelope.Variables.Input.RepositoryID != "R_kgDOABC" || len(envelope.Variables.Input.RefUpdates) != 2 || envelope.Variables.Input.RefUpdates[0].Name != "refs/heads/source" || envelope.Variables.Input.RefUpdates[1].Name != "refs/heads/target" || envelope.Variables.Input.RefUpdates[0].Force || envelope.Variables.Input.RefUpdates[1].Force {
		t.Fatalf("UpdateRefs() GraphQL payload = %s", receivedBody)
	}
	for _, invalid := range [][]GitHubRefUpdate{
		{},
		{{Name: "main", BeforeOID: "bad", AfterOID: strings.Repeat("b", 40)}},
		{{Name: "main", BeforeOID: strings.Repeat("a", 40), AfterOID: strings.Repeat("b", 40)}, {Name: "main", BeforeOID: strings.Repeat("b", 40), AfterOID: strings.Repeat("c", 40)}},
	} {
		if err := repository.UpdateRefs(context.Background(), invalid); !errors.Is(err, ErrGitHubRepositoryDenied) {
			t.Fatalf("UpdateRefs(%+v) error = %v", invalid, err)
		}
	}
}
