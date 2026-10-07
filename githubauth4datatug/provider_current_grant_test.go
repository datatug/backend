package githubauth4datatug

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

func TestRepositoryListAndMutationIntersectLiveUserAndAppGrants(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	store, err := NewCredentialStore(sneatcoretesting.NewMemoryDB(), bytesOf(0x47, 32), func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	actor := GitHubActor{ID: 500, Login: "alice"}
	if err := store.saveOAuthTokens(ctx, "firebase-A", actor, testTokens(now.Add(time.Hour), now.Add(24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	repo := GitHubRepository{ID: 55, NodeID: "R_kgDOABC", Owner: "acme", Name: "private", DefaultBranch: "main", Permissions: GitHubRepositoryPermissions{Pull: true, Push: true}}
	app := &fakeOAuthApp{actor: actor, repositories: []GitHubRepository{repo}}
	installation := &fakeInstallation{access: GitHubAppRepositoryAccess{InstallationID: 9, RepositoryID: 55, ContentsRead: true, ContentsWrite: false}}
	provider := newProvider(store, app, installation, ProviderOptions{Now: func() time.Time { return now }})
	ref := RepositoryRef{ID: repo.ID, Owner: repo.Owner, Name: repo.Name}
	listed, err := provider.ListRepositories(ctx, "firebase-A")
	if err != nil || len(listed) != 1 || listed[0].EffectivePermission != RepositoryRead {
		t.Fatalf("read-only App grant listed as writable: %+v %v", listed, err)
	}
	if _, err := provider.AuthorizeRepository(ctx, "firebase-A", ref, RepositoryWrite); !errors.Is(err, ErrGitHubPermissionDenied) {
		t.Fatalf("App grant unexpectedly allowed write: %v", err)
	}
	installation.access.ContentsWrite = true
	listed, err = provider.ListRepositories(ctx, "firebase-A")
	if err != nil || len(listed) != 1 || listed[0].EffectivePermission != RepositoryWrite {
		t.Fatalf("joint write grant not recognized: %+v %v", listed, err)
	}
	if _, err := provider.AuthorizeRepository(ctx, "firebase-A", ref, RepositoryWrite); err != nil {
		t.Fatalf("joint current grant denied write: %v", err)
	}
	app.repositories[0].Permissions.Push = false
	if _, err := provider.AuthorizeRepository(ctx, "firebase-A", ref, RepositoryWrite); !errors.Is(err, ErrGitHubPermissionDenied) {
		t.Fatalf("revoked user push still allowed write: %v", err)
	}
	app.repositories[0].Permissions.Pull = false
	listed, err = provider.ListRepositories(ctx, "firebase-A")
	if err != nil || len(listed) != 0 {
		t.Fatalf("revoked read still listed: %+v %v", listed, err)
	}
}
