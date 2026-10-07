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
	"time"

	wbghapp "github.com/sneat-dev/wb/hub"
)

// GitHubAppInstallationAuthorizer checks the dedicated DataTug App's current
// installation for the selected repository. Provider first verifies the
// immutable repository ID, owner, and name with the GitHub user actor token;
// this App-JWT request then proves that the same owner/repository path is in
// the App installation and returns its effective Contents permission.
type GitHubAppInstallationAuthorizer struct {
	appID         int64
	privateKeyPEM []byte
	client        *http.Client
	apiBaseURL    string
	now           func() time.Time
}

func NewGitHubAppInstallationAuthorizer(appID int64, privateKeyPEM []byte) (*GitHubAppInstallationAuthorizer, error) {
	return newGitHubAppInstallationAuthorizer(appID, privateKeyPEM, nil, githubAPIURL, time.Now)
}

func newGitHubAppInstallationAuthorizer(appID int64, privateKeyPEM []byte, client *http.Client, apiBaseURL string, now func() time.Time) (*GitHubAppInstallationAuthorizer, error) {
	if appID != DataTugGitHubAppID || len(privateKeyPEM) == 0 || !validGitHubAPIBaseURL(apiBaseURL) {
		return nil, ErrGitHubAppNotConfigured
	}
	if _, err := wbghapp.BuildAppJWT(appID, privateKeyPEM, now); err != nil {
		return nil, ErrGitHubAppNotConfigured
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	return &GitHubAppInstallationAuthorizer{
		appID: appID, privateKeyPEM: append([]byte(nil), privateKeyPEM...),
		client: client, apiBaseURL: strings.TrimRight(apiBaseURL, "/"), now: now,
	}, nil
}

func (a *GitHubAppInstallationAuthorizer) AuthorizeDataTugAppRepository(ctx context.Context, repo RepositoryRef) (GitHubAppRepositoryAccess, error) {
	if a == nil || a.appID != DataTugGitHubAppID || len(a.privateKeyPEM) == 0 || repo.Validate() != nil {
		return GitHubAppRepositoryAccess{}, ErrGitHubRepositoryDenied
	}
	appJWT, err := wbghapp.BuildAppJWT(a.appID, a.privateKeyPEM, a.now)
	if err != nil {
		return GitHubAppRepositoryAccess{}, ErrGitHubRepositoryDenied
	}
	path := "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name) + "/installation"
	var installation struct {
		ID      int64 `json:"id"`
		AppID   int64 `json:"app_id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
		SuspendedAt *time.Time `json:"suspended_at"`
		Permissions struct {
			Contents string `json:"contents"`
		} `json:"permissions"`
	}
	if err = a.appRequest(ctx, path, appJWT, &installation); err != nil {
		return GitHubAppRepositoryAccess{}, ErrGitHubRepositoryDenied
	}
	if installation.ID <= 0 || installation.AppID != DataTugGitHubAppID || installation.SuspendedAt != nil || !strings.EqualFold(installation.Account.Login, repo.Owner) {
		return GitHubAppRepositoryAccess{}, ErrGitHubRepositoryDenied
	}
	contents := strings.ToLower(installation.Permissions.Contents)
	if contents != "read" && contents != "write" {
		return GitHubAppRepositoryAccess{}, ErrGitHubRepositoryDenied
	}
	return GitHubAppRepositoryAccess{
		InstallationID: installation.ID,
		RepositoryID:   repo.ID,
		ContentsRead:   true,
		ContentsWrite:  contents == "write",
	}, nil
}

func (a *GitHubAppInstallationAuthorizer) appRequest(ctx context.Context, path, appJWT string, target any) error {
	u, err := url.Parse(a.apiBaseURL + path)
	if err != nil || u.Scheme != "https" || a.apiBaseURL == githubAPIURL && !strings.EqualFold(u.Host, "api.github.com") {
		return ErrGitHubRepositoryDenied
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ErrGitHubRepositoryDenied
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+appJWT)
	request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	response, err := doWithoutRedirects(a.client, request)
	if err != nil {
		return ErrGitHubRepositoryDenied
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: DataTug App request status %d", ErrGitHubRepositoryDenied, response.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, maxGitHubResponseBytes)).Decode(target); err != nil {
		return errors.New("DataTug App authorization response is invalid")
	}
	return nil
}

func validGitHubAPIBaseURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
