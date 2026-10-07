// Copyright 2026 https://datatug.io/

package githubauth4datatug

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGitHubAppInstallationAuthorizerChecksSelectedRepositoryInstallation(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		appID      int64
		account    string
		suspended  bool
		contents   string
		status     int
		wantDenied bool
		wantWrite  bool
	}{
		{name: "installed with contents write", appID: DataTugGitHubAppID, account: "acme", contents: "write", wantWrite: true},
		{name: "wrong App", appID: DataTugGitHubAppID + 1, account: "acme", contents: "write", wantDenied: true},
		{name: "wrong installation account", appID: DataTugGitHubAppID, account: "other-org", contents: "write", wantDenied: true},
		{name: "suspended installation", appID: DataTugGitHubAppID, account: "acme", suspended: true, contents: "write", wantDenied: true},
		{name: "App removed from repository", appID: DataTugGitHubAppID, account: "acme", status: http.StatusNotFound, wantDenied: true},
		{name: "redirect is not followed", appID: DataTugGitHubAppID, account: "acme", status: http.StatusMovedPermanently, wantDenied: true},
		{name: "contents read only", appID: DataTugGitHubAppID, account: "acme", contents: "read"},
		{name: "contents missing", appID: DataTugGitHubAppID, account: "acme", contents: "none", wantDenied: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
			if err != nil {
				t.Fatal(err)
			}
			privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
			var requests int
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if request.Method != http.MethodGet || request.URL.Path != "/repos/acme/private/installation" || request.URL.Scheme != "https" {
					t.Errorf("installation request %s %s; want direct selected-repository installation lookup", request.Method, request.URL.String())
				}
				if !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") || request.Header.Get("Authorization") == "Bearer " {
					t.Errorf("missing App JWT authorization header")
				}
				if test.status != 0 {
					response := jsonResponse(`{"message":"denied"}`)
					response.StatusCode = test.status
					return response, nil
				}
				suspended := "null"
				if test.suspended {
					suspended = `"2026-10-06T12:00:00Z"`
				}
				body := `{"id":501,"app_id":` + strconv.FormatInt(test.appID, 10) + `,"account":{"login":"` + test.account + `"},"suspended_at":` + suspended + `,"permissions":{"contents":"` + test.contents + `"}}`
				return jsonResponse(body), nil
			})}
			authorizer, err := newGitHubAppInstallationAuthorizer(DataTugGitHubAppID, privateKeyPEM, client, "https://api.github.test", func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			access, err := authorizer.AuthorizeDataTugAppRepository(context.Background(), RepositoryRef{ID: 77, Owner: "acme", Name: "private"})
			if test.wantDenied {
				if err == nil {
					t.Fatalf("authorization returned %+v, want denial", access)
				}
			} else if err != nil || access.InstallationID != 501 || access.RepositoryID != 77 || !access.ContentsRead || access.ContentsWrite != test.wantWrite {
				t.Fatalf("authorization = (%+v, %v), want selected App installation permission", access, err)
			}
			if requests != 1 {
				t.Fatalf("App installation requests=%d, want one direct lookup", requests)
			}
		})
	}
}
