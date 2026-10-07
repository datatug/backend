// Copyright 2026 https://datatug.io/

package githubauth4datatug

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

func TestCredentialStoreEncryptsActorBoundTokenAndOneTimeState(t *testing.T) {
	ctx := context.Background()
	db := sneatcoretesting.NewMemoryDB()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	store, err := NewCredentialStore(db, bytesOf(0x31, 32), func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tokens := testTokens(now.Add(time.Hour), now.Add(24*time.Hour))
	if err = store.saveOAuthTokens(ctx, "firebase-A", GitHubActor{ID: 101, Login: "alice"}, tokens); err != nil {
		t.Fatal(err)
	}
	key := models4datatug.NewGithubAppCredentialKey("firebase-A")
	if strings.Contains(key.String(), "users/") || strings.Contains(key.String(), "firebase-A") || !strings.Contains(key.String(), models4datatug.GithubAppCredentialsCollection) {
		t.Fatalf("credential path is not private/root-scoped: %q", key.String())
	}
	var persisted credentialRecord
	if err = db.Get(ctx, record.NewRecordWithData(key, &persisted)); err != nil {
		t.Fatal(err)
	}
	ciphertext := string(persisted.Token.Ciphertext)
	if strings.Contains(ciphertext, tokens.accessToken) || strings.Contains(ciphertext, tokens.refreshToken) {
		t.Fatal("credential record contains a plaintext token")
	}
	if _, err = store.tokenCipher.open("firebase-B", persisted.Token); !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("cross-actor decryption error = %v, want reauthorization required", err)
	}

	repo := RepositoryRef{ID: 44, Owner: "acme", Name: "private"}
	if err = store.saveOAuthState(ctx, "firebase-A", "one-time-state", "pkce-verifier", repo, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	stateKey := models4datatug.NewGithubOAuthStateKey("firebase-A", stateDigest("one-time-state"))
	if strings.Contains(stateKey.String(), "users/") || strings.Contains(stateKey.String(), "firebase-A") || !strings.Contains(stateKey.String(), models4datatug.GithubOAuthStatesCollection) {
		t.Fatalf("OAuth state path is not private/root-scoped: %q", stateKey.String())
	}
	var persistedState oauthStateRecord
	if err = db.Get(ctx, record.NewRecordWithData(stateKey, &persistedState)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stateKey.String(), "one-time-state") || strings.Contains(string(persistedState.Verifier.Ciphertext), "pkce-verifier") {
		t.Fatal("OAuth state record exposes the state or PKCE verifier")
	}
	if _, _, err = store.consumeOAuthState(ctx, "firebase-B", "one-time-state"); !errors.Is(err, ErrOAuthStateInvalid) {
		t.Fatalf("cross-actor state consume error = %v, want invalid state", err)
	}
	gotRepo, verifier, err := store.consumeOAuthState(ctx, "firebase-A", "one-time-state")
	if err != nil || gotRepo != repo || verifier != "pkce-verifier" {
		t.Fatalf("consume state = (%+v, %q, %v), want saved repo/verifier", gotRepo, verifier, err)
	}
	if _, _, err = store.consumeOAuthState(ctx, "firebase-A", "one-time-state"); !errors.Is(err, ErrOAuthStateInvalid) {
		t.Fatalf("reused state error = %v, want invalid state", err)
	}
}

func TestProviderConnectsBeforeRepositorySelectionAndEnforcesLiveActorPermission(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	db := sneatcoretesting.NewMemoryDB()
	store, err := NewCredentialStore(db, bytesOf(0x41, 32), func() time.Time { return now }, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	repo := GitHubRepository{ID: 55, NodeID: "R_kgDOABC", Owner: "acme", Name: "private", DefaultBranch: "main", Permissions: GitHubRepositoryPermissions{Pull: true}}
	app := &fakeOAuthApp{actor: GitHubActor{ID: 501, Login: "alice"}, repositories: []GitHubRepository{repo}}
	installation := &fakeInstallation{access: GitHubAppRepositoryAccess{InstallationID: 8, RepositoryID: repo.ID, ContentsRead: true, ContentsWrite: true}}
	provider := newProvider(store, app, installation, ProviderOptions{Now: func() time.Time { return now }})

	loginURL, err := provider.BeginAuthorization(ctx, "firebase-A")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(loginURL)
	if err != nil || parsed.Query().Get("repository_id") != "" {
		t.Fatalf("initial authorization URL = %q, error %v; want no repository restriction", loginURL, err)
	}
	state := parsed.Query().Get("state")
	if err = provider.CompleteAuthorization(ctx, "firebase-A", state, "single-use-code"); err != nil {
		t.Fatalf("complete initial authorization: %v", err)
	}
	listed, err := provider.ListRepositories(ctx, "firebase-A")
	if err != nil || len(listed) != 1 || listed[0].ID != repo.ID {
		t.Fatalf("ListRepositories() = %v, %v; want one App-readable repo", listed, err)
	}
	if _, err = provider.AuthorizeRepository(ctx, "firebase-A", RepositoryRef{ID: repo.ID, Owner: repo.Owner, Name: repo.Name}, RepositoryWrite); !errors.Is(err, ErrGitHubPermissionDenied) {
		t.Fatalf("read-only user write authorization error = %v, want permission denied despite App write grant", err)
	}
	if _, err = provider.AuthorizeRepository(ctx, "firebase-A", RepositoryRef{ID: repo.ID, Owner: repo.Owner, Name: repo.Name}, RepositoryRead); err != nil {
		t.Fatalf("read-only user read authorization: %v", err)
	}
	if _, err = provider.AuthorizeRepository(ctx, "firebase-A", RepositoryRef{ID: repo.ID + 1, Owner: repo.Owner, Name: repo.Name}, RepositoryRead); !errors.Is(err, ErrGitHubRepositoryDenied) {
		t.Fatalf("immutable repository ID mismatch error = %v, want repository denied", err)
	}
	app.repositories[0].Permissions.Pull = false
	if _, err = provider.AuthorizeRepository(ctx, "firebase-A", RepositoryRef{ID: repo.ID, Owner: repo.Owner, Name: repo.Name}, RepositoryRead); !errors.Is(err, ErrGitHubPermissionDenied) {
		t.Fatalf("revoked repository access error = %v, want permission denied", err)
	}

	app.actor = GitHubActor{ID: 777, Login: "bob"}
	if _, err = provider.AuthorizeRepository(ctx, "firebase-A", RepositoryRef{ID: repo.ID, Owner: repo.Owner, Name: repo.Name}, RepositoryRead); !errors.Is(err, ErrGitHubActorMismatch) {
		t.Fatalf("revoked/changed actor authorization error = %v, want actor mismatch", err)
	}
}

func TestProviderDeniesRepositoryOutsideDedicatedAppInstallation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	db := sneatcoretesting.NewMemoryDB()
	store, err := NewCredentialStore(db, bytesOf(0x43, 32), func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	repo := GitHubRepository{ID: 65, NodeID: "R_kgDOXYZ", Owner: "acme", Name: "private", Permissions: GitHubRepositoryPermissions{Pull: true, Push: true}}
	app := &fakeOAuthApp{actor: GitHubActor{ID: 502, Login: "alice"}, repositories: []GitHubRepository{repo}}
	installation := &fakeInstallation{access: GitHubAppRepositoryAccess{InstallationID: 9, RepositoryID: repo.ID + 1, ContentsRead: true, ContentsWrite: true}}
	provider := newProvider(store, app, installation, ProviderOptions{Now: func() time.Time { return now }})
	if err = store.saveOAuthTokens(ctx, "firebase-A", app.actor, testTokens(now.Add(time.Hour), now.Add(24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	ref := RepositoryRef{ID: repo.ID, Owner: repo.Owner, Name: repo.Name}
	if _, err = provider.AuthorizeRepository(ctx, "firebase-A", ref, RepositoryRead); !errors.Is(err, ErrGitHubRepositoryDenied) {
		t.Fatalf("repository authorization = %v, want denial when the dedicated App installation proves a different repository", err)
	}
	listed, err := provider.ListRepositories(ctx, "firebase-A")
	if err != nil || len(listed) != 0 {
		t.Fatalf("repository listing = (%+v, %v), want no repositories outside the dedicated App installation", listed, err)
	}
}

func TestCredentialStoreSerializesRotatingRefreshAndFailsClosedAfterCrash(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	db := sneatcoretesting.NewMemoryDB()
	store, err := NewCredentialStore(db, bytesOf(0x51, 32), func() time.Time { return now }, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	old := testTokens(now.Add(-time.Minute), now.Add(time.Hour))
	if err = store.saveOAuthTokens(ctx, "firebase-A", GitHubActor{ID: 88, Login: "alice"}, old); err != nil {
		t.Fatal(err)
	}
	app := &fakeOAuthApp{
		actor:          GitHubActor{ID: 88, Login: "alice"},
		repositories:   []GitHubRepository{{ID: 77, NodeID: "R_kgDOABC", Owner: "acme", Name: "private", Permissions: GitHubRepositoryPermissions{Pull: true}}},
		refreshEntered: make(chan struct{}, 2), refreshRelease: make(chan struct{}),
		rotated: testTokens(now.Add(time.Hour), now.Add(48*time.Hour)),
	}
	installation := &fakeInstallation{access: GitHubAppRepositoryAccess{InstallationID: 9, RepositoryID: 77, ContentsRead: true}}
	provider := newProvider(store, app, installation, ProviderOptions{Now: func() time.Time { return now }, RefreshSkew: time.Second, RefreshLease: time.Minute, RefreshPollInterval: time.Millisecond, RefreshWaitLimit: time.Second})
	ref := RepositoryRef{ID: 77, Owner: "acme", Name: "private"}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, authErr := provider.AuthorizeRepository(ctx, "firebase-A", ref, RepositoryRead)
			errs <- authErr
		}()
	}
	<-app.refreshEntered
	close(app.refreshRelease)
	wg.Wait()
	close(errs)
	for authErr := range errs {
		if authErr != nil {
			t.Fatalf("concurrent authorization after rotation: %v", authErr)
		}
	}
	if got := app.refreshCalls; got != 1 {
		t.Fatalf("refresh calls = %d, want exactly one", got)
	}

	// Simulate process loss after the one-time refresh token was consumed but
	// before its replacement reached durable storage: a stale lease must reauth.
	snapshot, err := store.loadCredentials(ctx, "firebase-A")
	if err != nil {
		t.Fatal(err)
	}
	if acquired, err := store.beginRefresh(ctx, "firebase-A", snapshot.Revision, "crashed-process"); err != nil || !acquired {
		t.Fatalf("begin simulated crashed refresh = %v, %v", acquired, err)
	}
	now = now.Add(2 * time.Minute)
	if _, _, err = provider.accessTokens(ctx, "firebase-A"); !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("stale refresh lease result = %v, want reauthorization required", err)
	}
}

func TestScrubOAuthCallbackURLRemovesQueryBeforeReturningCode(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://api.sneat.cloud/v0/datatug/github/callback?code=one-time&state=opaque&tracking=private", nil)
	if err != nil {
		t.Fatal(err)
	}
	code, state, err := ScrubOAuthCallbackURL(request)
	if err != nil || code != "one-time" || state != "opaque" {
		t.Fatalf("ScrubOAuthCallbackURL() = %q, %q, %v", code, state, err)
	}
	if request.URL.RawQuery != "" || strings.Contains(request.RequestURI, "one-time") || strings.Contains(request.RequestURI, "opaque") {
		t.Fatalf("callback request retained credentials: URL=%q RequestURI=%q", request.URL.String(), request.RequestURI)
	}
	if _, _, err = ScrubOAuthCallbackURL(nil); !errors.Is(err, ErrOAuthStateInvalid) {
		t.Fatalf("nil request error = %v, want invalid state", err)
	}
}

type fakeInstallation struct {
	access GitHubAppRepositoryAccess
	err    error
}

func (f *fakeInstallation) AuthorizeDataTugAppRepository(_ context.Context, repo RepositoryRef) (GitHubAppRepositoryAccess, error) {
	if f.err != nil {
		return GitHubAppRepositoryAccess{}, f.err
	}
	access := f.access
	if access.RepositoryID == 0 {
		access.RepositoryID = repo.ID
	}
	return access, nil
}

type fakeOAuthApp struct {
	actor          GitHubActor
	repositories   []GitHubRepository
	refreshEntered chan struct{}
	refreshRelease chan struct{}
	rotated        OAuthTokens
	refreshCalls   int
	mu             sync.Mutex
}

func (f *fakeOAuthApp) AuthorizationURL(state, challenge string, repositoryID int64) (string, error) {
	query := url.Values{"state": {state}, "code_challenge": {challenge}}
	if repositoryID > 0 {
		query.Set("repository_id", "selected")
	}
	return "https://github.com/login/oauth/authorize?" + query.Encode(), nil
}
func (f *fakeOAuthApp) Exchange(context.Context, string, string) (OAuthTokens, error) {
	return testTokens(time.Now().Add(time.Hour), time.Now().Add(24*time.Hour)), nil
}
func (f *fakeOAuthApp) Refresh(context.Context, string) (OAuthTokens, error) {
	f.mu.Lock()
	f.refreshCalls++
	f.mu.Unlock()
	if f.refreshEntered != nil {
		f.refreshEntered <- struct{}{}
		<-f.refreshRelease
	}
	return f.rotated, nil
}
func (f *fakeOAuthApp) UserClient(OAuthTokens) repositoryUserClient { return fakeUserClient{app: f} }
func (f *fakeOAuthApp) ScopedHTTPClient(OAuthTokens, RepositoryRef, string, RepositoryOperation) *http.Client {
	return &http.Client{}
}

type fakeUserClient struct{ app *fakeOAuthApp }

func (f fakeUserClient) CurrentUser(context.Context) (GitHubActor, error) { return f.app.actor, nil }
func (f fakeUserClient) Repository(_ context.Context, ref RepositoryRef) (GitHubRepository, error) {
	for _, repo := range f.app.repositories {
		if repo.ID == ref.ID && strings.EqualFold(repo.Owner, ref.Owner) && strings.EqualFold(repo.Name, ref.Name) {
			return repo, nil
		}
	}
	return GitHubRepository{}, ErrGitHubRepositoryDenied
}
func (f fakeUserClient) Repositories(context.Context) ([]GitHubRepository, error) {
	return append([]GitHubRepository(nil), f.app.repositories...), nil
}

func testTokens(accessExpiry, refreshExpiry time.Time) OAuthTokens {
	tokens, _ := newOAuthTokens("access-secret", "refresh-secret", accessExpiry, refreshExpiry)
	return tokens
}

func bytesOf(value byte, count int) []byte {
	bytes := make([]byte, count)
	for index := range bytes {
		bytes[index] = value
	}
	return bytes
}
