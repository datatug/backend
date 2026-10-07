// Copyright 2026 https://datatug.io/

package githubauth4datatug

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

func TestCredentialStoreRejectsInvalidInputsAndExpiredState(t *testing.T) {
	ctx := context.Background()
	if _, err := NewCredentialStore(nil, bytesOf(1, 32), nil, time.Second); err == nil {
		t.Fatal("NewCredentialStore() accepted nil database")
	}
	if _, err := NewCredentialStore(sneatcoretesting.NewMemoryDB(), bytesOf(1, 31), nil, time.Second); err == nil {
		t.Fatal("NewCredentialStore() accepted malformed crypto root")
	}
	if _, err := newCredentialCipher(bytesOf(1, 31), credentialCipherPurpose); err == nil {
		t.Fatal("newCredentialCipher() accepted malformed crypto root")
	}
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	db := sneatcoretesting.NewMemoryDB()
	store, err := NewCredentialStore(db, bytesOf(0x71, 32), func() time.Time { return now }, 0)
	if err != nil || store.refreshLease <= 0 {
		t.Fatalf("NewCredentialStore() default lease = %v, %v", store, err)
	}
	if _, err = store.tokenCipher.seal("", []byte("token")); err == nil {
		t.Fatal("seal() accepted empty actor")
	}
	if _, err = store.tokenCipher.seal("actor", nil); err == nil {
		t.Fatal("seal() accepted empty plaintext")
	}
	if _, err = store.tokenCipher.open("actor", sealedValue{Nonce: []byte{1}, Ciphertext: []byte{1}}); !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("open() malformed ciphertext error = %v", err)
	}
	if err = store.saveOAuthState(ctx, "actor", "", "verifier", RepositoryRef{}, now.Add(time.Minute)); !errors.Is(err, ErrOAuthStateInvalid) {
		t.Fatalf("saveOAuthState() empty state error = %v", err)
	}
	if err = store.saveOAuthState(ctx, "actor", "expired-state", "verifier", RepositoryRef{}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, _, err = store.consumeOAuthState(ctx, "actor", "expired-state"); !errors.Is(err, ErrOAuthStateInvalid) {
		t.Fatalf("consumeOAuthState() expired state error = %v", err)
	}
	if _, _, err = store.consumeOAuthState(ctx, "actor", "missing-state"); !errors.Is(err, ErrOAuthStateInvalid) {
		t.Fatalf("consumeOAuthState() missing state error = %v", err)
	}
}

func TestCredentialStoreRefreshLeaseAndCredentialCorruptionFailClosed(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	db := sneatcoretesting.NewMemoryDB()
	store, err := NewCredentialStore(db, bytesOf(0x72, 32), func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.saveOAuthTokens(ctx, "actor-A", GitHubActor{}, testTokens(now.Add(time.Hour), now.Add(2*time.Hour))); !errors.Is(err, ErrGitHubActorMismatch) {
		t.Fatalf("saveOAuthTokens() invalid actor error = %v", err)
	}
	if err = store.saveOAuthTokens(ctx, "actor-A", GitHubActor{ID: 1, Login: "alice"}, OAuthTokens{}); err == nil {
		t.Fatal("saveOAuthTokens() accepted non-expiring credentials")
	}
	if err = store.saveOAuthTokens(ctx, "actor-A", GitHubActor{ID: 1, Login: "alice"}, testTokens(now.Add(time.Hour), now.Add(2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err = store.loadCredentials(ctx, "actor-A"); err != nil {
		t.Fatal(err)
	}
	if acquired, err := store.beginRefresh(ctx, "actor-A", 99, "changed"); acquired || !errors.Is(err, ErrCredentialChanged) {
		t.Fatalf("beginRefresh() changed revision = (%t, %v)", acquired, err)
	}
	if acquired, err := store.beginRefresh(ctx, "actor-A", 1, "attempt-A"); !acquired || err != nil {
		t.Fatalf("beginRefresh() = (%t, %v)", acquired, err)
	}
	if acquired, err := store.beginRefresh(ctx, "actor-A", 1, "attempt-B"); acquired || !errors.Is(err, ErrRefreshInProgress) {
		t.Fatalf("second beginRefresh() = (%t, %v)", acquired, err)
	}
	if err = store.completeRefresh(ctx, "actor-A", "wrong-attempt", 1, testTokens(now.Add(time.Hour), now.Add(2*time.Hour))); !errors.Is(err, ErrCredentialChanged) {
		t.Fatalf("completeRefresh() wrong attempt error = %v", err)
	}
	if err = store.completeRefresh(ctx, "actor-A", "attempt-A", 1, OAuthTokens{}); err == nil {
		t.Fatal("completeRefresh() accepted invalid rotated credentials")
	}
	store.requireReconnect(ctx, "actor-A", "attempt-A", 1)
	if _, err = store.loadCredentials(ctx, "actor-A"); !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("loadCredentials() reconnect state error = %v", err)
	}
	if acquired, err := store.beginRefresh(ctx, "actor-A", 1, "attempt-C"); acquired || !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("beginRefresh() after reconnect = (%t, %v)", acquired, err)
	}

	// A fresh record with damaged authenticated ciphertext cannot be recovered.
	if err = store.saveOAuthTokens(ctx, "actor-B", GitHubActor{ID: 2, Login: "bob"}, testTokens(now.Add(time.Hour), now.Add(2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	key := models4datatug.NewGithubAppCredentialKey("actor-B")
	if err = db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		value := new(credentialRecord)
		if err := tx.Get(ctx, record.NewRecordWithData(key, value)); err != nil {
			return err
		}
		value.Token.Ciphertext[0] ^= 0xff
		return tx.Set(ctx, record.NewRecordWithData(key, value))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.loadCredentials(ctx, "actor-B"); !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("loadCredentials() tampered ciphertext error = %v", err)
	}
}
