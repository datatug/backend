package githubauth4datatug

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestScopedRepositoryNeverReturnsProviderErrorBodiesAsProjectData(t *testing.T) {
	for _, tc := range []struct {
		name         string
		method       string
		transportErr error
		status       int
		body         string
	}{
		{"read transport failure", "read", errors.New("connection reset"), 0, ""},
		{"read permission revoked", "read", nil, http.StatusForbidden, `{"message":"private details"}`},
		{"read malformed JSON", "read", nil, http.StatusOK, `{"private":"truncated"`},
		{"write transport failure", "write", errors.New("connection reset"), 0, ""},
		{"write permission revoked", "write", nil, http.StatusForbidden, `{"message":"private details"}`},
		{"write malformed JSON", "write", nil, http.StatusOK, `{"private":"truncated"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				if tc.transportErr != nil {
					return nil, tc.transportErr
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			repo := &AuthorizedGitHubRepository{repository: GitHubRepository{ID: 31, NodeID: "R_kgDOABC", Owner: "acme", Name: "private"}, permission: RepositoryWrite, client: client}
			var err error
			if tc.method == "read" {
				_, err = repo.GetRef(context.Background(), "main")
			} else {
				err = repo.UpdateRefs(context.Background(), []GitHubRefUpdate{{Name: "main", BeforeOID: strings.Repeat("a", 40), AfterOID: strings.Repeat("b", 40)}})
			}
			if !errors.Is(err, ErrGitHubRepositoryDenied) {
				t.Fatalf("provider failure exposed project data: %v", err)
			}
		})
	}
}

func TestScopedRepositoryRejectsUnpinnedOrUnsafeGitObjectsWithoutNetwork(t *testing.T) {
	called := false
	repo := &AuthorizedGitHubRepository{repository: GitHubRepository{ID: 31, Owner: "acme", Name: "private"}, permission: RepositoryWrite, client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected network")
	})}}
	if _, err := repo.GetCommit(context.Background(), "untrusted-head"); !errors.Is(err, ErrGitHubRepositoryDenied) {
		t.Fatalf("invalid commit OID read: %v", err)
	}
	if _, err := repo.GetRef(context.Background(), "../other"); !errors.Is(err, ErrGitHubRepositoryDenied) {
		t.Fatalf("invalid ref read: %v", err)
	}
	if _, err := repo.GetBlob(context.Background(), "untrusted-blob"); !errors.Is(err, ErrGitHubRepositoryDenied) {
		t.Fatalf("invalid blob read: %v", err)
	}
	if _, err := repo.CreateCommitOnBranch(context.Background(), "main", strings.Repeat("a", 40), "message", nil); !errors.Is(err, ErrGitHubRepositoryDenied) {
		t.Fatalf("empty commit accepted: %v", err)
	}
	if err := repo.UpdateRefs(context.Background(), nil); !errors.Is(err, ErrGitHubRepositoryDenied) {
		t.Fatalf("empty CAS batch accepted: %v", err)
	}
	if called {
		t.Fatal("unsafe request reached provider")
	}
}

func TestScopedRepositoryRejectsMalformedProviderGitObjects(t *testing.T) {
	oid := strings.Repeat("a", 40)
	treeOID := strings.Repeat("b", 40)
	for _, tc := range []struct {
		name, body string
		read       func(*AuthorizedGitHubRepository) error
	}{
		{"invalid commit parent", `{"sha":"` + oid + `","tree":{"sha":"` + treeOID + `"},"parents":[{"sha":"not-an-oid"}]}`, func(r *AuthorizedGitHubRepository) error {
			_, err := r.GetCommit(context.Background(), oid)
			return err
		}},
		{"truncated recursive tree", `{"truncated":true,"tree":[]}`, func(r *AuthorizedGitHubRepository) error {
			_, err := r.GetTree(context.Background(), treeOID)
			return err
		}},
		{"invalid tree entry", `{"truncated":false,"tree":[{"path":"../private","sha":"` + oid + `","type":"blob","mode":"100644"}]}`, func(r *AuthorizedGitHubRepository) error {
			_, err := r.GetTree(context.Background(), treeOID)
			return err
		}},
		{"invalid base64 blob", `{"sha":"` + oid + `","encoding":"base64","content":"%%%"}`, func(r *AuthorizedGitHubRepository) error { _, err := r.GetBlob(context.Background(), oid); return err }},
		{"missing CAS result", `{"data":{"updateRefs":null}}`, func(r *AuthorizedGitHubRepository) error {
			return r.UpdateRefs(context.Background(), []GitHubRefUpdate{{Name: "main", BeforeOID: oid, AfterOID: treeOID}})
		}},
		{"provider CAS rejection", `{"errors":[{"message":"branch changed"}]}`, func(r *AuthorizedGitHubRepository) error {
			return r.UpdateRefs(context.Background(), []GitHubRefUpdate{{Name: "main", BeforeOID: oid, AfterOID: treeOID}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &AuthorizedGitHubRepository{repository: GitHubRepository{ID: 31, NodeID: "R_kgDOABC", Owner: "acme", Name: "private"}, permission: RepositoryWrite, client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}}
			if err := tc.read(repo); !errors.Is(err, ErrGitHubRepositoryDenied) {
				t.Fatalf("malformed provider data accepted: %v", err)
			}
		})
	}
}
