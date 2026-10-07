package githubauth4datatug

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
)

const (
	DataTugGitHubAppSlug = "datatug"
	DataTugGitHubAppID   = int64(5223634)
)

var (
	ErrGitHubAppNotConfigured = errors.New("DataTug GitHub App authorization is not configured")
	ErrGitHubActorMismatch    = errors.New("GitHub account does not match the connected Firebase user")
	ErrGitHubRepositoryDenied = errors.New("GitHub repository is not authorized")
	ErrGitHubPermissionDenied = errors.New("GitHub user does not have the required repository permission")
)

// GitHubAppConfig contains server-side settings for the existing DataTug GitHub
// App. Secret values are accepted only by the server and are never persisted by
// this package.
type GitHubAppConfig struct {
	AppID         int64
	ClientID      string
	ClientSecret  string
	PrivateKeyPEM []byte
	CallbackURL   string
}

type RepositoryOperation string

const (
	RepositoryRead  RepositoryOperation = "read"
	RepositoryWrite RepositoryOperation = "write"
)

type RepositoryRef struct {
	ID    int64  `json:"id" firestore:"id"`
	Owner string `json:"owner" firestore:"owner"`
	Name  string `json:"name" firestore:"name"`
}

func (r RepositoryRef) Validate() error {
	if r.ID <= 0 || !validGitHubName(r.Owner) || !validGitHubName(r.Name) {
		return ErrGitHubRepositoryDenied
	}
	return nil
}

type GitHubActor struct {
	ID    int64
	Login string
}

type GitHubRepositoryPermissions struct {
	Pull  bool
	Push  bool
	Admin bool
}

type GitHubRepository struct {
	ID                  int64
	NodeID              string
	Owner               string
	Name                string
	DefaultBranch       string
	Permissions         GitHubRepositoryPermissions
	EffectivePermission RepositoryOperation
}

// GitHubBranch is a branch name and its current immutable commit identifier.
type GitHubBranch struct {
	Name string
	OID  string
}

// GitHubRef is an immutable reference snapshot. OID is the observed target.
type GitHubRef struct {
	Name string
	OID  string
}

type GitHubCommit struct {
	OID        string
	TreeOID    string
	Message    string
	ParentOIDs []string
}

// GitHubTreeEntry identifies one path in a complete recursive repository tree.
type GitHubTreeEntry struct {
	Path string
	Type string
	OID  string
	Mode string
	Size int64
}

// GitHubFileChange adds/replaces a file with raw bytes, or deletes it when Delete is true.
type GitHubFileChange struct {
	Path    string
	Content []byte
	Delete  bool
}

// GitHubRefUpdate is an atomic compare-and-swap update for one branch ref.
type GitHubRefUpdate struct {
	Name      string
	BeforeOID string
	AfterOID  string
}

type GitHubAppRepositoryAccess struct {
	InstallationID int64
	RepositoryID   int64
	ContentsRead   bool
	ContentsWrite  bool
}

// InstallationAuthorizer proves that the existing DataTug GitHub App's
// selected-repository installation currently includes a project repository.
// Its adapter may use App authentication, but an installation token is never
// returned as the project actor.
type InstallationAuthorizer interface {
	AuthorizeDataTugAppRepository(context.Context, RepositoryRef) (GitHubAppRepositoryAccess, error)
}

type repositoryUserClient interface {
	CurrentUser(context.Context) (GitHubActor, error)
	Repository(context.Context, RepositoryRef) (GitHubRepository, error)
	Repositories(context.Context) ([]GitHubRepository, error)
}

type appOAuthClient interface {
	AuthorizationURL(state, codeChallenge string, repositoryID int64) (string, error)
	Exchange(context.Context, string, string) (OAuthTokens, error)
	Refresh(context.Context, string) (OAuthTokens, error)
	UserClient(OAuthTokens) repositoryUserClient
	ScopedHTTPClient(OAuthTokens, RepositoryRef, string, RepositoryOperation) *http.Client
}

type ProviderOptions struct {
	Now                 func() time.Time
	OAuthStateLifetime  time.Duration
	RefreshSkew         time.Duration
	RefreshLease        time.Duration
	RefreshPollInterval time.Duration
	RefreshWaitLimit    time.Duration
}

type Provider struct {
	store        *CredentialStore
	app          appOAuthClient
	installation InstallationAuthorizer
	options      ProviderOptions
}

func NewProvider(db dal.DB, config GitHubAppConfig, platformCryptoRoot []byte, installation InstallationAuthorizer, options ProviderOptions) (*Provider, error) {
	if config.AppID != DataTugGitHubAppID || strings.TrimSpace(config.ClientID) == "" || strings.TrimSpace(config.ClientSecret) == "" || strings.TrimSpace(config.CallbackURL) == "" {
		return nil, ErrGitHubAppNotConfigured
	}
	if installation == nil {
		var err error
		installation, err = NewGitHubAppInstallationAuthorizer(config.AppID, config.PrivateKeyPEM)
		if err != nil {
			return nil, ErrGitHubAppNotConfigured
		}
	}
	store, err := NewCredentialStore(db, platformCryptoRoot, options.Now, options.RefreshLease)
	if err != nil {
		return nil, ErrGitHubAppNotConfigured
	}
	app, err := NewGitHubAppClient(config, nil)
	if err != nil {
		return nil, ErrGitHubAppNotConfigured
	}
	return newProvider(store, app, installation, options), nil
}

func newProvider(store *CredentialStore, app appOAuthClient, installation InstallationAuthorizer, options ProviderOptions) *Provider {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.OAuthStateLifetime <= 0 {
		options.OAuthStateLifetime = 10 * time.Minute
	}
	if options.RefreshSkew <= 0 {
		options.RefreshSkew = 2 * time.Minute
	}
	if options.RefreshLease <= 0 {
		options.RefreshLease = 30 * time.Second
	}
	if options.RefreshPollInterval <= 0 {
		options.RefreshPollInterval = 20 * time.Millisecond
	}
	if options.RefreshWaitLimit <= 0 {
		options.RefreshWaitLimit = 45 * time.Second
	}
	return &Provider{store: store, app: app, installation: installation, options: options}
}

// BeginAuthorization connects a GitHub actor before repository selection.
// The returned URL contains only short-lived state and a PKCE challenge.
func (p *Provider) BeginAuthorization(ctx context.Context, firebaseUID string) (string, error) {
	return p.beginAuthorization(ctx, firebaseUID, RepositoryRef{})
}

// BeginRepositoryAuthorization starts a repository-bound reconnection for an
// already selected project. Initial connection uses BeginAuthorization.
func (p *Provider) BeginRepositoryAuthorization(ctx context.Context, firebaseUID string, repo RepositoryRef) (string, error) {
	if err := repo.Validate(); err != nil {
		return "", err
	}
	return p.beginAuthorization(ctx, firebaseUID, repo)
}

func (p *Provider) beginAuthorization(ctx context.Context, firebaseUID string, repo RepositoryRef) (string, error) {
	if err := p.ready(); err != nil {
		return "", err
	}
	if err := validateUserID(firebaseUID); err != nil {
		return "", err
	}
	if repo.ID != 0 {
		if err := repo.Validate(); err != nil {
			return "", err
		}
	}
	state, err := randomURLToken(32)
	if err != nil {
		return "", errors.New("could not start GitHub authorization")
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		return "", errors.New("could not start GitHub authorization")
	}
	expiresAt := p.options.Now().Add(p.options.OAuthStateLifetime)
	if err = p.store.saveOAuthState(ctx, firebaseUID, state, verifier, repo, expiresAt); err != nil {
		return "", errors.New("could not start GitHub authorization")
	}
	challenge := codeChallenge(verifier)
	authorizationURL, err := p.app.AuthorizationURL(state, challenge, repo.ID)
	if err != nil {
		return "", errors.New("could not start GitHub authorization")
	}
	return authorizationURL, nil
}

// CompleteAuthorization consumes the actor-bound one-time state before
// exchanging the callback code. Callers must scrub code and state from the
// callback URL before routing, logging, or analytics.
func (p *Provider) CompleteAuthorization(ctx context.Context, firebaseUID, state, code string) error {
	if err := p.ready(); err != nil {
		return err
	}
	if err := validateUserID(firebaseUID); err != nil || state == "" || code == "" {
		return ErrOAuthStateInvalid
	}
	repo, verifier, err := p.store.consumeOAuthState(ctx, firebaseUID, state)
	if err != nil {
		return ErrOAuthStateInvalid
	}
	tokens, err := p.app.Exchange(ctx, code, verifier)
	if err != nil || tokens.validate() != nil {
		return ErrReauthorizationRequired
	}
	userClient := p.app.UserClient(tokens)
	actor, err := userClient.CurrentUser(ctx)
	if err != nil || actor.ID <= 0 || strings.TrimSpace(actor.Login) == "" {
		return ErrReauthorizationRequired
	}
	if repo.ID != 0 {
		githubRepo, repoErr := userClient.Repository(ctx, repo)
		if repoErr != nil || !repositoryMatches(githubRepo, repo) || !githubRepo.Permissions.Pull {
			return ErrGitHubRepositoryDenied
		}
		appAccess, accessErr := p.installation.AuthorizeDataTugAppRepository(ctx, repo)
		if accessErr != nil || appAccess.InstallationID <= 0 || appAccess.RepositoryID != repo.ID || !appAccess.ContentsRead {
			return ErrGitHubRepositoryDenied
		}
	}
	if err = p.store.saveOAuthTokens(ctx, firebaseUID, actor, tokens); err != nil {
		return ErrReauthorizationRequired
	}
	return nil
}

// ListRepositories returns only repositories the connected actor can read and
// the dedicated DataTug App currently has installed. It conveys no mutation
// authority; project operations still call AuthorizeRepository every time.
func (p *Provider) ListRepositories(ctx context.Context, firebaseUID string) ([]GitHubRepository, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if err := validateUserID(firebaseUID); err != nil {
		return nil, err
	}
	snapshot, tokens, err := p.accessTokens(ctx, firebaseUID)
	if err != nil {
		return nil, err
	}
	client := p.app.UserClient(tokens)
	actor, err := client.CurrentUser(ctx)
	if err != nil || actor.ID != snapshot.ActorID || !strings.EqualFold(actor.Login, snapshot.ActorLogin) {
		return nil, ErrGitHubActorMismatch
	}
	repositories, err := client.Repositories(ctx)
	if err != nil {
		return nil, ErrGitHubRepositoryDenied
	}
	result := make([]GitHubRepository, 0, len(repositories))
	for _, repo := range repositories {
		if repo.ID <= 0 || !validGitHubName(repo.Owner) || !validGitHubName(repo.Name) || !repo.Permissions.Pull {
			continue
		}
		ref := RepositoryRef{ID: repo.ID, Owner: repo.Owner, Name: repo.Name}
		access, accessErr := p.installation.AuthorizeDataTugAppRepository(ctx, ref)
		if accessErr == nil && access.InstallationID > 0 && access.RepositoryID == repo.ID && access.ContentsRead {
			repo.EffectivePermission = RepositoryRead
			if (repo.Permissions.Push || repo.Permissions.Admin) && access.ContentsWrite {
				repo.EffectivePermission = RepositoryWrite
			}
			result = append(result, repo)
		}
	}
	return result, nil
}

// AuthorizedGitHubRepository is an ephemeral, actor-bound API client. The
// underlying user token remains private in the transport and the client is
// restricted to the selected repository and operation permission.
type AuthorizedGitHubRepository struct {
	firebaseUID string
	actor       GitHubActor
	repository  GitHubRepository
	permission  RepositoryOperation
	client      *http.Client
}

// RepositoryScope returns a value snapshot of the verified actor and immutable
// repository identity. Mutating the returned value cannot widen the client.
type RepositoryScope struct {
	FirebaseUID string
	Actor       GitHubActor
	Repository  GitHubRepository
	Permission  RepositoryOperation
}

func (r *AuthorizedGitHubRepository) Scope() RepositoryScope {
	if r == nil {
		return RepositoryScope{}
	}
	return RepositoryScope{FirebaseUID: r.firebaseUID, Actor: r.actor, Repository: r.repository, Permission: r.permission}
}

func (r *AuthorizedGitHubRepository) ListBranches(ctx context.Context) ([]GitHubBranch, error) {
	return r.listBranches(ctx)
}

func (r *AuthorizedGitHubRepository) GetRef(ctx context.Context, branch string) (GitHubRef, error) {
	return r.getRef(ctx, branch)
}

func (r *AuthorizedGitHubRepository) GetCommit(ctx context.Context, oid string) (GitHubCommit, error) {
	return r.getCommit(ctx, oid)
}

// GetTree returns all descendants of treeOID, recursively. A truncated GitHub response is an error.
func (r *AuthorizedGitHubRepository) GetTree(ctx context.Context, treeOID string) ([]GitHubTreeEntry, error) {
	return r.getTree(ctx, treeOID)
}

func (r *AuthorizedGitHubRepository) GetBlob(ctx context.Context, blobOID string) ([]byte, error) {
	return r.getBlob(ctx, blobOID)
}

func (r *AuthorizedGitHubRepository) CreateCommitOnBranch(ctx context.Context, branch, expectedHeadOID, message string, changes []GitHubFileChange) (GitHubCommit, error) {
	if r == nil || r.permission != RepositoryWrite {
		return GitHubCommit{}, ErrGitHubPermissionDenied
	}
	return r.createCommitOnBranch(ctx, branch, expectedHeadOID, message, changes)
}

func (r *AuthorizedGitHubRepository) UpdateRefs(ctx context.Context, updates []GitHubRefUpdate) error {
	if r == nil || r.permission != RepositoryWrite {
		return ErrGitHubPermissionDenied
	}
	return r.updateRefs(ctx, updates)
}

// AuthorizeRepository rechecks Firebase UID to GitHub actor binding, selected
// App installation membership, repository identity, and the requested current
// permission on every read or write.
func (p *Provider) AuthorizeRepository(ctx context.Context, firebaseUID string, repoRef RepositoryRef, operation RepositoryOperation) (*AuthorizedGitHubRepository, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if err := validateUserID(firebaseUID); err != nil || repoRef.Validate() != nil || (operation != RepositoryRead && operation != RepositoryWrite) {
		return nil, ErrGitHubRepositoryDenied
	}
	snapshot, tokens, err := p.accessTokens(ctx, firebaseUID)
	if err != nil {
		return nil, err
	}
	userClient := p.app.UserClient(tokens)
	actor, err := userClient.CurrentUser(ctx)
	if err != nil || actor.ID != snapshot.ActorID || !strings.EqualFold(actor.Login, snapshot.ActorLogin) {
		return nil, ErrGitHubActorMismatch
	}
	repo, err := userClient.Repository(ctx, repoRef)
	if err != nil || !repositoryMatches(repo, repoRef) {
		return nil, ErrGitHubRepositoryDenied
	}
	if !repo.Permissions.Pull {
		return nil, ErrGitHubPermissionDenied
	}
	if operation == RepositoryWrite && !repo.Permissions.Push && !repo.Permissions.Admin {
		return nil, ErrGitHubPermissionDenied
	}
	appAccess, err := p.installation.AuthorizeDataTugAppRepository(ctx, repoRef)
	if err != nil || appAccess.InstallationID <= 0 || appAccess.RepositoryID != repoRef.ID || !appAccess.ContentsRead {
		return nil, ErrGitHubRepositoryDenied
	}
	if operation == RepositoryWrite && !appAccess.ContentsWrite {
		return nil, ErrGitHubPermissionDenied
	}
	repo.EffectivePermission = operation
	return &AuthorizedGitHubRepository{
		firebaseUID: firebaseUID,
		actor:       actor,
		repository:  repo,
		permission:  operation,
		client:      p.app.ScopedHTTPClient(tokens, repoRef, repo.NodeID, operation),
	}, nil
}

func (p *Provider) accessTokens(ctx context.Context, firebaseUID string) (credentialSnapshot, OAuthTokens, error) {
	// Refresh coordination waits are bounded by real elapsed time even when a
	// test injects a fixed logical clock for token expiry assertions.
	deadline := time.Now().Add(p.options.RefreshWaitLimit)
	for {
		snapshot, err := p.store.loadCredentials(ctx, firebaseUID)
		if err != nil {
			return credentialSnapshot{}, OAuthTokens{}, err
		}
		now := p.options.Now()
		if snapshot.RefreshState == refreshStatePending {
			if now.Sub(snapshot.RefreshStarted) > p.options.RefreshLease {
				_, _ = p.store.beginRefresh(ctx, firebaseUID, snapshot.Revision, "recovery-check")
				return credentialSnapshot{}, OAuthTokens{}, ErrReauthorizationRequired
			}
			if err = p.waitForRefresh(ctx, deadline); err != nil {
				return credentialSnapshot{}, OAuthTokens{}, err
			}
			continue
		}
		if snapshot.Tokens.accessExpiresAt.After(now.Add(p.options.RefreshSkew)) {
			return snapshot, snapshot.Tokens, nil
		}
		if !snapshot.Tokens.refreshExpiresAt.After(now) {
			return credentialSnapshot{}, OAuthTokens{}, ErrReauthorizationRequired
		}
		attemptID, idErr := randomURLToken(18)
		if idErr != nil {
			return credentialSnapshot{}, OAuthTokens{}, ErrReauthorizationRequired
		}
		acquired, beginErr := p.store.beginRefresh(ctx, firebaseUID, snapshot.Revision, attemptID)
		if errors.Is(beginErr, ErrCredentialChanged) {
			continue
		}
		if errors.Is(beginErr, ErrRefreshInProgress) {
			if err = p.waitForRefresh(ctx, deadline); err != nil {
				return credentialSnapshot{}, OAuthTokens{}, err
			}
			continue
		}
		if beginErr != nil || !acquired {
			return credentialSnapshot{}, OAuthTokens{}, ErrReauthorizationRequired
		}
		rotated, refreshErr := p.app.Refresh(ctx, snapshot.Tokens.refreshToken)
		if refreshErr != nil || rotated.validate() != nil {
			p.store.requireReconnect(ctx, firebaseUID, attemptID, snapshot.Revision)
			return credentialSnapshot{}, OAuthTokens{}, ErrReauthorizationRequired
		}
		if err = p.store.completeRefresh(ctx, firebaseUID, attemptID, snapshot.Revision, rotated); err != nil {
			p.store.requireReconnect(ctx, firebaseUID, attemptID, snapshot.Revision)
			return credentialSnapshot{}, OAuthTokens{}, ErrReauthorizationRequired
		}
		snapshot.Tokens = rotated
		snapshot.Revision++
		snapshot.RefreshState = refreshStateIdle
		snapshot.RefreshAttempt = ""
		snapshot.RefreshStarted = time.Time{}
		return snapshot, rotated, nil
	}
}

func (p *Provider) waitForRefresh(ctx context.Context, deadline time.Time) error {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return ErrReauthorizationRequired
	}
	delay := p.options.RefreshPollInterval
	if delay > remaining {
		delay = remaining
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ErrReauthorizationRequired
	case <-timer.C:
		return nil
	}
}

func (p *Provider) ready() error {
	if p == nil || p.store == nil || p.app == nil || p.installation == nil {
		return ErrGitHubAppNotConfigured
	}
	return nil
}

func repositoryMatches(actual GitHubRepository, expected RepositoryRef) bool {
	return actual.ID == expected.ID && strings.EqualFold(actual.Owner, expected.Owner) && strings.EqualFold(actual.Name, expected.Name) && actual.NodeID != ""
}

func validGitHubName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func randomURLToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func codeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (p *Provider) String() string {
	if p == nil {
		return "DataTug GitHub actor provider [unconfigured]"
	}
	return fmt.Sprintf("DataTug GitHub actor provider [%s]", DataTugGitHubAppSlug)
}
