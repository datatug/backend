package githubauth4datatug

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

type failingUserClient struct {
	base                       fakeUserClient
	actor                      GitHubActor
	actorErr, repoErr, listErr error
}

func (f *failingUserClient) CurrentUser(ctx context.Context) (GitHubActor, error) {
	if f.actorErr != nil {
		return GitHubActor{}, f.actorErr
	}
	if f.actor.ID != 0 {
		return f.actor, nil
	}
	return f.base.CurrentUser(ctx)
}
func (f *failingUserClient) Repository(ctx context.Context, ref RepositoryRef) (GitHubRepository, error) {
	if f.repoErr != nil {
		return GitHubRepository{}, f.repoErr
	}
	return f.base.Repository(ctx, ref)
}
func (f *failingUserClient) RepositoryByName(ctx context.Context, owner, name string) (GitHubRepository, error) {
	if f.repoErr != nil {
		return GitHubRepository{}, f.repoErr
	}
	return f.base.RepositoryByName(ctx, owner, name)
}
func (f *failingUserClient) DataTugRepositories(ctx context.Context) ([]GitHubRepository, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.base.DataTugRepositories(ctx)
}

type failingOAuthApp struct {
	*fakeOAuthApp
	client                  *failingUserClient
	authURLErr, exchangeErr error
	invalidExchange         bool
}

func (f *failingOAuthApp) AuthorizationURL(state, challenge string, repositoryID int64) (string, error) {
	if f.authURLErr != nil {
		return "", f.authURLErr
	}
	return f.fakeOAuthApp.AuthorizationURL(state, challenge, repositoryID)
}
func (f *failingOAuthApp) Exchange(ctx context.Context, code, verifier string) (OAuthTokens, error) {
	if f.exchangeErr != nil {
		return OAuthTokens{}, f.exchangeErr
	}
	if f.invalidExchange {
		return OAuthTokens{}, nil
	}
	return f.fakeOAuthApp.Exchange(ctx, code, verifier)
}
func (f *failingOAuthApp) UserClient(OAuthTokens) repositoryUserClient { return f.client }
func (f *failingOAuthApp) ScopedHTTPClient(tokens OAuthTokens, repo RepositoryRef, nodeID string, op RepositoryOperation) *http.Client {
	return f.fakeOAuthApp.ScopedHTTPClient(tokens, repo, nodeID, op)
}

func newFailingProviderFixture(t *testing.T) (*Provider, *failingOAuthApp, *fakeInstallation, *CredentialStore, RepositoryRef) {
	t.Helper()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	store, err := NewCredentialStore(sneatcoretesting.NewMemoryDB(), bytesOf(0x59, 32), func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ref := RepositoryRef{ID: 77, Owner: "acme", Name: "private"}
	base := &fakeOAuthApp{actor: GitHubActor{ID: 101, Login: "alice"}, repositories: []GitHubRepository{{ID: ref.ID, NodeID: "R_kgDOABC", Owner: ref.Owner, Name: ref.Name, Permissions: GitHubRepositoryPermissions{Pull: true, Push: true}}}}
	app := &failingOAuthApp{fakeOAuthApp: base, client: &failingUserClient{base: fakeUserClient{app: base}}}
	installation := &fakeInstallation{access: GitHubAppRepositoryAccess{InstallationID: 9, RepositoryID: ref.ID, ContentsRead: true, ContentsWrite: true}}
	if err := store.saveOAuthTokens(context.Background(), "firebase-A", base.actor, testTokens(now.Add(time.Hour), now.Add(24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	return newProvider(store, app, installation, ProviderOptions{Now: func() time.Time { return now }}), app, installation, store, ref
}

func TestProviderCallbackDoesNotPersistInvalidGitHubIdentityOrGrant(t *testing.T) {
	for _, tc := range []struct {
		name      string
		repoBound bool
		mutate    func(*failingOAuthApp, *fakeInstallation)
		want      error
	}{
		{"code exchange fails", false, func(a *failingOAuthApp, _ *fakeInstallation) { a.exchangeErr = errors.New("provider unavailable") }, ErrReauthorizationRequired},
		{"invalid rotated credentials", false, func(a *failingOAuthApp, _ *fakeInstallation) { a.invalidExchange = true }, ErrReauthorizationRequired},
		{"actor lookup fails", false, func(a *failingOAuthApp, _ *fakeInstallation) { a.client.actorErr = errors.New("provider unavailable") }, ErrReauthorizationRequired},
		{"selected repo lookup fails", true, func(a *failingOAuthApp, _ *fakeInstallation) { a.client.repoErr = errors.New("provider unavailable") }, ErrGitHubRepositoryDenied},
		{"selected repo read revoked", true, func(a *failingOAuthApp, _ *fakeInstallation) { a.repositories[0].Permissions.Pull = false }, ErrGitHubRepositoryDenied},
		{"dedicated App removed", true, func(_ *failingOAuthApp, i *fakeInstallation) { i.err = errors.New("installation removed") }, ErrGitHubRepositoryDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, app, installation, store, ref := newFailingProviderFixture(t)
			var bound RepositoryRef
			if tc.repoBound {
				bound = ref
			}
			if err := store.saveOAuthState(context.Background(), "firebase-A", "once", "verifier", bound, provider.options.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			tc.mutate(app, installation)
			if err := provider.CompleteAuthorization(context.Background(), "firebase-A", "once", "one-time-code"); !errors.Is(err, tc.want) {
				t.Fatalf("invalid callback accepted: %v", err)
			}
			if err := provider.CompleteAuthorization(context.Background(), "firebase-A", "once", "replay"); !errors.Is(err, ErrOAuthStateInvalid) {
				t.Fatalf("callback state replay accepted: %v", err)
			}
		})
	}
}

func TestProviderDoesNotExposeRepositoriesAfterProviderReadFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*failingOAuthApp)
		read   bool
		want   error
	}{
		{"repository listing failed", func(a *failingOAuthApp) { a.client.listErr = errors.New("provider unavailable") }, false, ErrGitHubRepositoryDenied},
		{"selected repository lookup failed", func(a *failingOAuthApp) { a.client.repoErr = errors.New("provider unavailable") }, true, ErrGitHubRepositoryDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, app, _, _, ref := newFailingProviderFixture(t)
			tc.mutate(app)
			var err error
			if tc.read {
				_, err = provider.AuthorizeRepository(context.Background(), "firebase-A", ref, RepositoryRead)
			} else {
				_, err = provider.ListRepositories(context.Background(), "firebase-A")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("provider read failure exposed repository: %v", err)
			}
		})
	}
}

func TestProviderRepositoryDiscoveryFiltersUntrustedAndUninstalledEntries(t *testing.T) {
	provider, app, _, _, ref := newFailingProviderFixture(t)
	app.repositories = append(app.repositories,
		GitHubRepository{ID: 0, Owner: "acme", Name: "unidentified", Permissions: GitHubRepositoryPermissions{Pull: true}},
		GitHubRepository{ID: 78, NodeID: "R_kgDOABD", Owner: "../other", Name: "unsafe", Permissions: GitHubRepositoryPermissions{Pull: true}},
	)
	listed, err := provider.ListRepositories(context.Background(), "firebase-A")
	if !errors.Is(err, ErrGitHubRepositoryDenied) || listed != nil {
		t.Fatalf("malformed repository inventory returned partial data: %+v %v", listed, err)
	}
	app.repositories = []GitHubRepository{
		{ID: ref.ID, NodeID: "R_kgDOABC", Owner: ref.Owner, Name: ref.Name, Permissions: GitHubRepositoryPermissions{Pull: true, Push: true}},
		{ID: 79, NodeID: "R_kgDOABE", Owner: "acme", Name: "revoked", Permissions: GitHubRepositoryPermissions{Pull: false}},
	}
	listed, err = provider.ListRepositories(context.Background(), "firebase-A")
	if err != nil || len(listed) != 1 || listed[0].ID != ref.ID {
		t.Fatalf("user without read permission was not filtered: %+v %v", listed, err)
	}
}

func TestProviderResolvesOnlySelectedRepositoryWithCurrentActor(t *testing.T) {
	provider, app, _, _, ref := newFailingProviderFixture(t)
	app.client.listErr = errors.New("full inventory must not be called")
	repo, err := provider.ResolveRepositoryByName(context.Background(), "firebase-A", ref.Owner, ref.Name)
	if err != nil || repo.ID != ref.ID || repo.Owner != ref.Owner || repo.Name != ref.Name {
		t.Fatalf("ResolveRepositoryByName() = (%+v, %v)", repo, err)
	}
	app.client.actor = GitHubActor{ID: 999, Login: "mallory"}
	if _, err = provider.ResolveRepositoryByName(context.Background(), "firebase-A", ref.Owner, ref.Name); !errors.Is(err, ErrGitHubActorMismatch) {
		t.Fatalf("changed actor resolution error = %v, want actor mismatch", err)
	}
}

func TestProviderAuthorizationURLFailureDoesNotCreateUsableGrant(t *testing.T) {
	provider, app, _, _, ref := newFailingProviderFixture(t)
	app.authURLErr = errors.New("provider unavailable")
	if _, err := provider.BeginRepositoryAuthorization(context.Background(), "firebase-A", ref); err == nil {
		t.Fatal("authorization URL failure reported success")
	}
	if _, err := provider.AuthorizeRepository(context.Background(), "firebase-A", ref, RepositoryOperation("admin")); !errors.Is(err, ErrGitHubRepositoryDenied) {
		t.Fatalf("invalid operation widened grant: %v", err)
	}
}
