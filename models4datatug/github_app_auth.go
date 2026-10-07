package models4datatug

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/dal-go/record"
)

const (
	GithubAppCredentialsCollection = "datatug_github_app_credentials"
	GithubOAuthStatesCollection    = "datatug_github_oauth_states"
)

// NewGithubAppCredentialKey addresses a private root-collection record for one
// Firebase user. It deliberately lives outside users/{uid}, whose recursive
// client-read rule would otherwise expose even encrypted credential metadata.
func NewGithubAppCredentialKey(userID string) *record.Key {
	return record.NewKeyWithID(GithubAppCredentialsCollection, userRecordID(userID))
}

// NewGithubOAuthStateKey addresses a single-use state in the private root
// collection. The id contains hashes of both the Firebase UID and OAuth state.
func NewGithubOAuthStateKey(userID, stateDigest string) *record.Key {
	return record.NewKeyWithID(GithubOAuthStatesCollection, userRecordID(userID)+"-"+stateDigest)
}

func userRecordID(userID string) string {
	sum := sha256.Sum256([]byte(userID))
	return hex.EncodeToString(sum[:])
}
