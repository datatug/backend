package githubauth4datatug

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	githubOAuthAuthorizeURL = "https://github.com/login/oauth/authorize"
	githubOAuthTokenURL     = "https://github.com/login/oauth/access_token"
	githubAPIURL            = "https://api.github.com"
	githubAPIVersion        = "2022-11-28"
	maxGitHubResponseBytes  = 2 << 20
)

type GitHubAppClient struct {
	config     GitHubAppConfig
	httpClient *http.Client
	now        func() time.Time
}

func NewGitHubAppClient(config GitHubAppConfig, client *http.Client) (*GitHubAppClient, error) {
	if config.AppID != DataTugGitHubAppID || strings.TrimSpace(config.ClientID) == "" || strings.TrimSpace(config.ClientSecret) == "" || !validCallbackURL(config.CallbackURL) {
		return nil, ErrGitHubAppNotConfigured
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &GitHubAppClient{config: config, httpClient: client, now: time.Now}, nil
}

func (c *GitHubAppClient) AuthorizationURL(state, codeChallenge string, repositoryID int64) (string, error) {
	if state == "" || codeChallenge == "" || repositoryID < 0 {
		return "", ErrGitHubAppNotConfigured
	}
	u, err := url.Parse(githubOAuthAuthorizeURL)
	if err != nil {
		return "", ErrGitHubAppNotConfigured
	}
	q := u.Query()
	q.Set("client_id", c.config.ClientID)
	q.Set("redirect_uri", c.config.CallbackURL)
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	if repositoryID > 0 {
		q.Set("repository_id", strconv.FormatInt(repositoryID, 10))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *GitHubAppClient) Exchange(ctx context.Context, code, verifier string) (OAuthTokens, error) {
	if code == "" || verifier == "" {
		return OAuthTokens{}, ErrOAuthStateInvalid
	}
	return c.exchange(ctx, url.Values{
		"client_id":     {c.config.ClientID},
		"client_secret": {c.config.ClientSecret},
		"code":          {code},
		"redirect_uri":  {c.config.CallbackURL},
		"code_verifier": {verifier},
	})
}

func (c *GitHubAppClient) Refresh(ctx context.Context, refreshToken string) (OAuthTokens, error) {
	if refreshToken == "" {
		return OAuthTokens{}, ErrReauthorizationRequired
	}
	return c.exchange(ctx, url.Values{
		"client_id":     {c.config.ClientID},
		"client_secret": {c.config.ClientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	})
}

func (c *GitHubAppClient) exchange(ctx context.Context, form url.Values) (OAuthTokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthTokens{}, ErrReauthorizationRequired
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := doWithoutRedirects(c.httpClient, req)
	if err != nil {
		return OAuthTokens{}, ErrReauthorizationRequired
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return OAuthTokens{}, ErrReauthorizationRequired
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGitHubResponseBytes))
	if err != nil {
		return OAuthTokens{}, ErrReauthorizationRequired
	}
	var wire struct {
		AccessToken           string `json:"access_token"`
		RefreshToken          string `json:"refresh_token"`
		ExpiresIn             int64  `json:"expires_in"`
		RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
		Error                 string `json:"error"`
	}
	maxSeconds := int64((1<<63 - 1) / int64(time.Second))
	if json.Unmarshal(body, &wire) != nil || wire.Error != "" || wire.ExpiresIn <= 0 || wire.RefreshTokenExpiresIn <= 0 || wire.ExpiresIn > maxSeconds || wire.RefreshTokenExpiresIn > maxSeconds {
		return OAuthTokens{}, ErrReauthorizationRequired
	}
	now := c.now()
	return newOAuthTokens(
		wire.AccessToken,
		wire.RefreshToken,
		now.Add(time.Duration(wire.ExpiresIn)*time.Second),
		now.Add(time.Duration(wire.RefreshTokenExpiresIn)*time.Second),
	)
}

func (c *GitHubAppClient) UserClient(tokens OAuthTokens) repositoryUserClient {
	return &githubUserClient{client: c.httpClient, accessToken: tokens.accessToken}
}

func (c *GitHubAppClient) ScopedHTTPClient(tokens OAuthTokens, repo RepositoryRef, nodeID string, operation RepositoryOperation) *http.Client {
	base := c.httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{
		Transport: repoBoundRoundTripper{
			base:             bearerRoundTripper{base: base, token: tokens.accessToken},
			repository:       repo,
			repositoryNodeID: nodeID,
			operation:        operation,
		},
		Timeout: c.httpClient.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type githubUserClient struct {
	client      *http.Client
	accessToken string
}

func (c *githubUserClient) CurrentUser(ctx context.Context) (GitHubActor, error) {
	var wire struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := c.get(ctx, "/user", &wire); err != nil || wire.ID <= 0 || strings.TrimSpace(wire.Login) == "" {
		return GitHubActor{}, ErrGitHubActorMismatch
	}
	return GitHubActor{ID: wire.ID, Login: wire.Login}, nil
}

func (c *githubUserClient) Repository(ctx context.Context, ref RepositoryRef) (GitHubRepository, error) {
	if err := ref.Validate(); err != nil {
		return GitHubRepository{}, ErrGitHubRepositoryDenied
	}
	path := "/repos/" + url.PathEscape(ref.Owner) + "/" + url.PathEscape(ref.Name)
	var wire struct {
		ID            int64  `json:"id"`
		NodeID        string `json:"node_id"`
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
		Permissions GitHubRepositoryPermissions `json:"permissions"`
	}
	if err := c.get(ctx, path, &wire); err != nil {
		return GitHubRepository{}, ErrGitHubRepositoryDenied
	}
	owner := wire.Owner.Login
	if owner == "" {
		owner, _, _ = strings.Cut(wire.FullName, "/")
	}
	if wire.ID != ref.ID || wire.NodeID == "" || !strings.EqualFold(owner, ref.Owner) || !strings.EqualFold(wire.Name, ref.Name) || (wire.DefaultBranch != "" && !validBranchName(wire.DefaultBranch)) {
		return GitHubRepository{}, ErrGitHubRepositoryDenied
	}
	return GitHubRepository{ID: wire.ID, NodeID: wire.NodeID, Owner: owner, Name: wire.Name, DefaultBranch: wire.DefaultBranch, Permissions: wire.Permissions}, nil
}

func (c *githubUserClient) Repositories(ctx context.Context) ([]GitHubRepository, error) {
	repositories := make([]GitHubRepository, 0)
	for page := 1; page <= maxGitHubPages; page++ {
		path := fmt.Sprintf("/user/repos?affiliation=owner,collaborator,organization_member&per_page=100&page=%d", page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIURL+path, nil)
		if err != nil {
			return nil, ErrGitHubRepositoryDenied
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
		resp, err := doWithoutRedirects(c.client, req)
		if err != nil {
			return nil, ErrGitHubRepositoryDenied
		}
		var wire []struct {
			ID            int64  `json:"id"`
			NodeID        string `json:"node_id"`
			Name          string `json:"name"`
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
			Owner         struct {
				Login string `json:"login"`
			} `json:"owner"`
			Permissions GitHubRepositoryPermissions `json:"permissions"`
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 || json.NewDecoder(io.LimitReader(resp.Body, maxGitHubResponseBytes)).Decode(&wire) != nil {
			resp.Body.Close()
			return nil, ErrGitHubRepositoryDenied
		}
		resp.Body.Close()
		for _, item := range wire {
			owner := item.Owner.Login
			if owner == "" {
				owner, _, _ = strings.Cut(item.FullName, "/")
			}
			if item.ID <= 0 || item.NodeID == "" || !validGitHubName(owner) || !validGitHubName(item.Name) {
				return nil, ErrGitHubRepositoryDenied
			}
			if item.DefaultBranch != "" && !validBranchName(item.DefaultBranch) {
				return nil, ErrGitHubRepositoryDenied
			}
			repositories = append(repositories, GitHubRepository{ID: item.ID, NodeID: item.NodeID, Owner: owner, Name: item.Name, DefaultBranch: item.DefaultBranch, Permissions: item.Permissions})
		}
		if len(wire) < 100 {
			return repositories, nil
		}
	}
	return nil, ErrGitHubRepositoryDenied
}

func (c *githubUserClient) get(ctx context.Context, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIURL+path, nil)
	if err != nil {
		return errors.New("GitHub request could not be created")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	resp, err := doWithoutRedirects(c.client, req)
	if err != nil {
		return errors.New("GitHub authorization check failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("GitHub authorization check was denied")
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, maxGitHubResponseBytes)).Decode(target); err != nil {
		return errors.New("GitHub authorization response was invalid")
	}
	return nil
}

func doWithoutRedirects(client *http.Client, request *http.Request) (*http.Response, error) {
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return copyClient.Do(request)
}

type bearerRoundTripper struct {
	base  http.RoundTripper
	token string
}

func (t bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token == "" {
		return nil, ErrReauthorizationRequired
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

type repoBoundRoundTripper struct {
	base             http.RoundTripper
	repository       RepositoryRef
	repositoryNodeID string
	operation        RepositoryOperation
}

func (t repoBoundRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.allowed(req) {
		return nil, ErrGitHubRepositoryDenied
	}
	return t.base.RoundTrip(req)
}

func (t repoBoundRoundTripper) allowed(req *http.Request) bool {
	if req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Host, "api.github.com") || (req.Method != http.MethodGet && req.Method != http.MethodHead && t.operation != RepositoryWrite) {
		return false
	}
	if req.URL.Path == "/graphql" {
		return t.allowedGraphQL(req)
	}
	prefix := "/repos/" + t.repository.Owner + "/" + t.repository.Name + "/"
	if !strings.HasPrefix(strings.ToLower(req.URL.EscapedPath()), strings.ToLower(prefix)) {
		return false
	}
	if t.operation == RepositoryRead && req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}
	return true
}

func (t repoBoundRoundTripper) allowedGraphQL(req *http.Request) bool {
	if req.Method != http.MethodPost {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		return false
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	var envelope struct {
		Query         string         `json:"query"`
		OperationName string         `json:"operationName"`
		Variables     map[string]any `json:"variables"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Query == "" {
		return false
	}
	operation := envelope.OperationName
	if operation == "" {
		operation = graphQLOperationName(envelope.Query)
	}
	if strings.Contains(strings.ToLower(envelope.Query), "mutation") && t.operation != RepositoryWrite {
		return false
	}
	switch strings.ToLower(operation) {
	case "createcommitonbranch":
		return t.operation == RepositoryWrite && hasRepositoryName(envelope.Variables, t.repository.Owner+"/"+t.repository.Name) && containsNonEmptyField(envelope.Variables, "expectedHeadOid")
	case "updaterefs":
		return t.operation == RepositoryWrite && hasRepositoryNodeID(envelope.Variables, t.repositoryNodeID) && validRefUpdates(envelope.Variables)
	case "createref":
		return t.operation == RepositoryWrite && hasRepositoryNodeID(envelope.Variables, t.repositoryNodeID)
	default:
		return !strings.Contains(strings.ToLower(envelope.Query), "mutation") && hasOwnerAndName(envelope.Variables, t.repository.Owner, t.repository.Name)
	}
}

func graphQLOperationName(query string) string {
	fields := strings.Fields(query)
	for i, field := range fields {
		if strings.EqualFold(field, "mutation") || strings.EqualFold(field, "query") {
			if i+1 < len(fields) && !strings.HasPrefix(fields[i+1], "{") {
				return strings.Trim(fields[i+1], "{(")
			}
			return ""
		}
	}
	return ""
}

func hasRepositoryName(value any, want string) bool {
	switch m := value.(type) {
	case map[string]any:
		if got, ok := m["repositoryNameWithOwner"].(string); ok && strings.EqualFold(got, want) {
			return true
		}
		for _, child := range m {
			if hasRepositoryName(child, want) {
				return true
			}
		}
	case []any:
		for _, child := range m {
			if hasRepositoryName(child, want) {
				return true
			}
		}
	}
	return hasOwnerAndName(value, strings.SplitN(want, "/", 2)[0], strings.SplitN(want, "/", 2)[1])
}

func hasOwnerAndName(value any, owner, name string) bool {
	switch v := value.(type) {
	case map[string]any:
		if gotOwner, ok := v["owner"].(string); ok {
			if gotName, ok := v["name"].(string); ok && strings.EqualFold(gotOwner, owner) && strings.EqualFold(gotName, name) {
				return true
			}
		}
		for _, child := range v {
			if hasOwnerAndName(child, owner, name) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasOwnerAndName(child, owner, name) {
				return true
			}
		}
	}
	return false
}

func hasRepositoryNodeID(value any, nodeID string) bool {
	if nodeID == "" {
		return false
	}
	switch v := value.(type) {
	case map[string]any:
		if got, ok := v["repositoryId"].(string); ok && got == nodeID {
			return true
		}
		for _, child := range v {
			if hasRepositoryNodeID(child, nodeID) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasRepositoryNodeID(child, nodeID) {
				return true
			}
		}
	}
	return false
}

func validRefUpdates(value any) bool {
	variables, ok := value.(map[string]any)
	if !ok {
		return false
	}
	input, ok := variables["input"].(map[string]any)
	if !ok {
		return false
	}
	updates, ok := input["refUpdates"].([]any)
	if !ok || len(updates) == 0 {
		return false
	}
	for _, update := range updates {
		fields, ok := update.(map[string]any)
		if !ok || strings.TrimSpace(stringField(fields, "name")) == "" || strings.TrimSpace(stringField(fields, "beforeOid")) == "" || strings.TrimSpace(stringField(fields, "afterOid")) == "" {
			return false
		}
		if force, ok := fields["force"].(bool); ok && force {
			return false
		}
	}
	return true
}

func containsNonEmptyField(value any, field string) bool {
	switch v := value.(type) {
	case map[string]any:
		if stringField(v, field) != "" {
			return true
		}
		for _, child := range v {
			if containsNonEmptyField(child, field) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if containsNonEmptyField(child, field) {
				return true
			}
		}
	}
	return false
}

func stringField(fields map[string]any, name string) string {
	value, _ := fields[name].(string)
	return value
}

func validCallbackURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

var _ appOAuthClient = (*GitHubAppClient)(nil)

func (c *GitHubAppClient) String() string {
	return fmt.Sprintf("GitHub App client [%s]", DataTugGitHubAppSlug)
}
