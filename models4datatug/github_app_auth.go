package models4datatug

import "github.com/dal-go/record"

const (
	GithubAppCredentialsCollection = "github_app_credentials"
	GithubOAuthStatesCollection    = "github_oauth_states"
	GithubAppCredentialID          = "current"
)

// NewGithubAppCredentialKey addresses the single DataTug GitHub App connection
// owned by one Firebase user. The record is a child of that user's DataTug
// extension record and contains ciphertext only.
func NewGithubAppCredentialKey(userID, extID string) *record.Key {
	parent := NewUserExtKey(userID, extID)
	return record.NewKeyWithParentAndID(parent, GithubAppCredentialsCollection, GithubAppCredentialID)
}

// NewGithubOAuthStateKey addresses one pending OAuth state under its Firebase
// user's DataTug extension record. Callers store only a digest of the state as
// the record id.
func NewGithubOAuthStateKey(userID, extID, stateDigest string) *record.Key {
	parent := NewUserExtKey(userID, extID)
	return record.NewKeyWithParentAndID(parent, GithubOAuthStatesCollection, stateDigest)
}
