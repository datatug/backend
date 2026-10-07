package models4datatug

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestGitHubCredentialAndOAuthStateKeysArePrivateAndActorBound(t *testing.T) {
	uid := "firebase-user-123"
	wantActorKey := hex.EncodeToString(sha256Bytes([]byte(uid)))
	credential := NewGithubAppCredentialKey(uid)
	if credential.Collection() != GithubAppCredentialsCollection || credential.ID != wantActorKey {
		t.Fatalf("credential key = %q, want private collection and UID digest", credential.String())
	}
	if strings.Contains(credential.String(), uid) {
		t.Fatal("credential key exposes Firebase UID")
	}
	otherCredential := NewGithubAppCredentialKey("another-user")
	if credential.String() == otherCredential.String() {
		t.Fatal("credential keys collide across Firebase users")
	}

	stateDigest := hex.EncodeToString(sha256Bytes([]byte("raw-oauth-state")))
	state := NewGithubOAuthStateKey(uid, stateDigest)
	if state.Collection() != GithubOAuthStatesCollection || state.ID != wantActorKey+"-"+stateDigest {
		t.Fatalf("OAuth state key = %q, want actor and state digest binding", state.String())
	}
	if strings.Contains(state.String(), uid) || strings.Contains(state.String(), "raw-oauth-state") {
		t.Fatal("OAuth state key exposes UID or raw state")
	}
	if state.String() == NewGithubOAuthStateKey("another-user", stateDigest).String() || state.String() == NewGithubOAuthStateKey(uid, "other-digest").String() {
		t.Fatal("OAuth state key is not unique to actor and state digest")
	}
}

func sha256Bytes(value []byte) []byte {
	sum := sha256.Sum256(value)
	return sum[:]
}
