// Copyright 2026 https://datatug.io/

package githubauth4datatug

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

func TestProviderConstructionAndRepositoryBoundReconnectFailClosed(t *testing.T) {
	ctx := context.Background()
	if provider, err := NewProvider(nil, GitHubAppConfig{}, nil, nil, ProviderOptions{}); provider != nil || !errors.Is(err, ErrGitHubAppNotConfigured) {
		t.Fatalf("NewProvider() with empty config = (%v, %v)", provider, err)
	}
	if _, err := NewProvider(nil, GitHubAppConfig{AppID: DataTugGitHubAppID, ClientID: "id", ClientSecret: "secret", CallbackURL: "https://datatug.app/github/callback"}, bytesOf(1, 32), &fakeInstallation{}, ProviderOptions{}); !errors.Is(err, ErrGitHubAppNotConfigured) {
		t.Fatalf("NewProvider() without database error = %v", err)
	}
	config := GitHubAppConfig{AppID: DataTugGitHubAppID, ClientID: "id", ClientSecret: "secret", CallbackURL: "https://datatug.app/github/callback"}
	if _, err := NewProvider(sneatcoretesting.NewMemoryDB(), config, bytesOf(1, 31), &fakeInstallation{}, ProviderOptions{}); !errors.Is(err, ErrGitHubAppNotConfigured) {
		t.Fatalf("NewProvider() with invalid crypto root error = %v", err)
	}
	if _, err := NewGitHubAppInstallationAuthorizer(DataTugGitHubAppID+1, []byte("invalid-key")); !errors.Is(err, ErrGitHubAppNotConfigured) {
		t.Fatalf("installation authorizer wrong App error = %v", err)
	}

	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	db := sneatcoretesting.NewMemoryDB()
	store, err := NewCredentialStore(db, bytesOf(0x61, 32), func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ref := RepositoryRef{ID: 55, Owner: "acme", Name: "private"}
	app := &fakeOAuthApp{
		actor:        GitHubActor{ID: 101, Login: "alice"},
		repositories: []GitHubRepository{{ID: ref.ID, NodeID: "R_kgDOABC", Owner: ref.Owner, Name: ref.Name, Permissions: GitHubRepositoryPermissions{Pull: true}}},
	}
	provider := newProvider(store, app, &fakeInstallation{access: GitHubAppRepositoryAccess{InstallationID: 9, RepositoryID: ref.ID, ContentsRead: true}}, ProviderOptions{Now: func() time.Time { return now }})
	if _, err = provider.BeginRepositoryAuthorization(ctx, "firebase-A", RepositoryRef{}); !errors.Is(err, ErrGitHubRepositoryDenied) {
		t.Fatalf("BeginRepositoryAuthorization() invalid repository error = %v", err)
	}
	loginURL, err := provider.BeginRepositoryAuthorization(ctx, "firebase-A", ref)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(loginURL)
	if err != nil || parsed.Query().Get("repository_id") != "selected" {
		t.Fatalf("reconnect URL = %q, error = %v", loginURL, err)
	}
	state := parsed.Query().Get("state")
	if err = provider.CompleteAuthorization(ctx, "firebase-B", state, "code"); !errors.Is(err, ErrOAuthStateInvalid) {
		t.Fatalf("cross-actor reconnect state error = %v", err)
	}
	if err = provider.CompleteAuthorization(ctx, "firebase-A", state, ""); !errors.Is(err, ErrOAuthStateInvalid) {
		t.Fatalf("empty callback code error = %v", err)
	}
	if err = provider.CompleteAuthorization(ctx, "firebase-A", state, "single-use-code"); err != nil {
		t.Fatalf("valid repository-bound reconnect: %v", err)
	}
	if err = provider.CompleteAuthorization(ctx, "firebase-A", state, "replayed-code"); !errors.Is(err, ErrOAuthStateInvalid) {
		t.Fatalf("reused callback state error = %v", err)
	}
	if _, err = provider.ListRepositories(ctx, ""); !errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("empty Firebase UID repository listing error = %v", err)
	}
	if got := (*Provider)(nil).String(); !strings.Contains(got, "unconfigured") {
		t.Fatalf("nil provider string = %q", got)
	}
	if got := provider.String(); !strings.Contains(got, DataTugGitHubAppSlug) {
		t.Fatalf("configured provider string = %q", got)
	}
	if got := (*AuthorizedGitHubRepository)(nil).Scope(); got != (RepositoryScope{}) {
		t.Fatalf("nil repository scope = %+v", got)
	}
	if _, err = provider.AuthorizeRepository(ctx, "firebase-A", ref, RepositoryOperation("admin")); !errors.Is(err, ErrGitHubRepositoryDenied) {
		t.Fatalf("unknown operation authorization error = %v", err)
	}
}

func TestProviderRefreshFailureMarksRotatingCredentialForReconnect(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	db := sneatcoretesting.NewMemoryDB()
	store, err := NewCredentialStore(db, bytesOf(0x62, 32), func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.loadCredentials(ctx, ""); !errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("empty UID credential lookup error = %v", err)
	}
	if _, err = store.loadCredentials(ctx, "missing-user"); !errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("missing credential lookup error = %v", err)
	}
	actor := GitHubActor{ID: 77, Login: "alice"}
	stale := testTokens(now.Add(-time.Minute), now.Add(time.Hour))
	if err = store.saveOAuthTokens(ctx, "firebase-A", actor, stale); err != nil {
		t.Fatal(err)
	}
	app := &fakeOAuthApp{rotated: OAuthTokens{}}
	provider := newProvider(store, app, &fakeInstallation{}, ProviderOptions{Now: func() time.Time { return now }, RefreshSkew: time.Second})
	if _, _, err = provider.accessTokens(ctx, "firebase-A"); !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("invalid rotated credentials error = %v", err)
	}
	if _, err = store.loadCredentials(ctx, "firebase-A"); !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("credential after failed single-use refresh = %v, want reconnect required", err)
	}
	if acquired, err := store.beginRefresh(ctx, "firebase-A", 1, "later-attempt"); acquired || !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("refresh after failed rotation = (%t, %v), want reconnect refusal", acquired, err)
	}
}
