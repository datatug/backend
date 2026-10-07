package githubauth4datatug

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
)

const (
	credentialCipherPurpose = "datatug/github-app-user-credentials/v1"
	stateCipherPurpose      = "datatug/github-app-oauth-state/v1"
	refreshStateIdle        = "idle"
	refreshStatePending     = "pending"
	refreshStateReconnect   = "reconnect_required"
)

var (
	ErrCredentialMissing       = errors.New("GitHub connection is missing")
	ErrCredentialChanged       = errors.New("GitHub connection changed during refresh")
	ErrRefreshInProgress       = errors.New("GitHub token refresh is in progress")
	ErrReauthorizationRequired = errors.New("GitHub connection must be reauthorized")
	ErrOAuthStateInvalid       = errors.New("GitHub OAuth state is invalid or expired")
)

// OAuthTokens exists only in memory. Its string representations and default
// JSON representation intentionally omit token material.
type OAuthTokens struct {
	accessToken      string
	refreshToken     string
	accessExpiresAt  time.Time
	refreshExpiresAt time.Time
}

func newOAuthTokens(accessToken, refreshToken string, accessExpiresAt, refreshExpiresAt time.Time) (OAuthTokens, error) {
	tokens := OAuthTokens{
		accessToken: accessToken, refreshToken: refreshToken,
		accessExpiresAt: accessExpiresAt, refreshExpiresAt: refreshExpiresAt,
	}
	if err := tokens.validate(); err != nil {
		return OAuthTokens{}, err
	}
	return tokens, nil
}

func (OAuthTokens) String() string   { return "GitHub OAuth tokens [redacted]" }
func (OAuthTokens) GoString() string { return "GitHub OAuth tokens [redacted]" }
func (OAuthTokens) MarshalJSON() ([]byte, error) {
	return []byte(`{"redacted":true}`), nil
}

func (t OAuthTokens) validate() error {
	if t.accessToken == "" || t.refreshToken == "" || t.accessExpiresAt.IsZero() || t.refreshExpiresAt.IsZero() {
		return errors.New("GitHub App must return expiring access and refresh tokens")
	}
	if !t.refreshExpiresAt.After(t.accessExpiresAt) {
		return errors.New("GitHub refresh token expires before access token")
	}
	return nil
}

type tokenEnvelope struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

type sealedValue struct {
	Nonce      []byte `json:"nonce" firestore:"nonce"`
	Ciphertext []byte `json:"ciphertext" firestore:"ciphertext"`
}

type credentialRecord struct {
	ActorID          int64       `json:"actor_id" firestore:"actor_id"`
	ActorLogin       string      `json:"actor_login" firestore:"actor_login"`
	Token            sealedValue `json:"token" firestore:"token"`
	AccessExpiresAt  time.Time   `json:"access_expires_at" firestore:"access_expires_at"`
	RefreshExpiresAt time.Time   `json:"refresh_expires_at" firestore:"refresh_expires_at"`
	Revision         int64       `json:"revision" firestore:"revision"`
	RefreshState     string      `json:"refresh_state" firestore:"refresh_state"`
	RefreshAttemptID string      `json:"refresh_attempt_id,omitempty" firestore:"refresh_attempt_id,omitempty"`
	RefreshStartedAt time.Time   `json:"refresh_started_at,omitempty" firestore:"refresh_started_at,omitempty"`
}

type oauthStateRecord struct {
	Verifier   sealedValue   `json:"verifier" firestore:"verifier"`
	Repository RepositoryRef `json:"repository" firestore:"repository"`
	ExpiresAt  time.Time     `json:"expires_at" firestore:"expires_at"`
}

type credentialSnapshot struct {
	ActorID        int64
	ActorLogin     string
	Tokens         OAuthTokens
	Revision       int64
	RefreshState   string
	RefreshAttempt string
	RefreshStarted time.Time
}

type credentialCipher struct {
	aead    cipher.AEAD
	purpose string
}

func newCredentialCipher(platformCryptoRoot []byte, purpose string) (*credentialCipher, error) {
	if len(platformCryptoRoot) != 32 {
		return nil, errors.New("platform crypto root must be 32 bytes")
	}
	key, err := hkdf.Key(sha256.New, platformCryptoRoot, nil, purpose, 32)
	if err != nil {
		return nil, fmt.Errorf("derive DataTug GitHub encryption key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create DataTug GitHub encryption cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create DataTug GitHub authenticated cipher: %w", err)
	}
	return &credentialCipher{aead: aead, purpose: purpose}, nil
}

func (c *credentialCipher) seal(userID string, plaintext []byte) (sealedValue, error) {
	if userID == "" || len(plaintext) == 0 {
		return sealedValue{}, errors.New("cannot encrypt empty GitHub credential data")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return sealedValue{}, errors.New("cannot create GitHub credential nonce")
	}
	return sealedValue{
		Nonce:      nonce,
		Ciphertext: c.aead.Seal(nil, nonce, plaintext, credentialAAD(c.purpose, userID)),
	}, nil
}

func (c *credentialCipher) open(userID string, value sealedValue) ([]byte, error) {
	if userID == "" || len(value.Nonce) != c.aead.NonceSize() || len(value.Ciphertext) == 0 {
		return nil, ErrReauthorizationRequired
	}
	plaintext, err := c.aead.Open(nil, value.Nonce, value.Ciphertext, credentialAAD(c.purpose, userID))
	if err != nil {
		return nil, ErrReauthorizationRequired
	}
	return plaintext, nil
}

func credentialAAD(purpose, userID string) []byte {
	return []byte(purpose + "\x00" + userID)
}

// CredentialStore persists only purpose-encrypted GitHub tokens. Refresh state
// is updated in DAL transactions so concurrent Cloud API instances cannot both
// consume the same rotating refresh token.
type CredentialStore struct {
	db           dal.DB
	tokenCipher  *credentialCipher
	stateCipher  *credentialCipher
	now          func() time.Time
	refreshLease time.Duration
}

func NewCredentialStore(db dal.DB, platformCryptoRoot []byte, now func() time.Time, refreshLease time.Duration) (*CredentialStore, error) {
	if db == nil {
		return nil, errors.New("DataTug GitHub credential database is required")
	}
	tokenCipher, err := newCredentialCipher(platformCryptoRoot, credentialCipherPurpose)
	if err != nil {
		return nil, err
	}
	stateCipher, err := newCredentialCipher(platformCryptoRoot, stateCipherPurpose)
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	if refreshLease <= 0 {
		refreshLease = 30 * time.Second
	}
	return &CredentialStore{db: db, tokenCipher: tokenCipher, stateCipher: stateCipher, now: now, refreshLease: refreshLease}, nil
}

func (s *CredentialStore) saveOAuthState(ctx context.Context, userID, state, verifier string, repo RepositoryRef, expiresAt time.Time) error {
	if userID == "" || state == "" || verifier == "" || !expiresAt.After(s.now()) {
		return ErrOAuthStateInvalid
	}
	sealed, err := s.stateCipher.seal(userID, []byte(verifier))
	if err != nil {
		return err
	}
	key := models4datatug.NewGithubOAuthStateKey(userID, stateDigest(state))
	value := &oauthStateRecord{Verifier: sealed, Repository: repo, ExpiresAt: expiresAt}
	return s.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Insert(ctx, record.NewRecordWithData(key, value))
	})
}

func (s *CredentialStore) consumeOAuthState(ctx context.Context, userID, state string) (repo RepositoryRef, verifier string, err error) {
	if userID == "" || state == "" {
		return RepositoryRef{}, "", ErrOAuthStateInvalid
	}
	key := models4datatug.NewGithubOAuthStateKey(userID, stateDigest(state))
	var expired bool
	err = s.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		value := new(oauthStateRecord)
		rec := record.NewRecordWithData(key, value)
		if getErr := tx.Get(ctx, rec); getErr != nil {
			return ErrOAuthStateInvalid
		}
		if !value.ExpiresAt.After(s.now()) {
			expired = true
		}
		var plaintext []byte
		if !expired {
			var openErr error
			plaintext, openErr = s.stateCipher.open(userID, value.Verifier)
			if openErr != nil {
				return ErrOAuthStateInvalid
			}
		}
		if deleteErr := tx.Delete(ctx, key); deleteErr != nil {
			return deleteErr
		}
		repo = value.Repository
		verifier = string(plaintext)
		return nil
	})
	if err != nil || expired {
		return RepositoryRef{}, "", ErrOAuthStateInvalid
	}
	return repo, verifier, nil
}

func (s *CredentialStore) saveOAuthTokens(ctx context.Context, userID string, actor GitHubActor, tokens OAuthTokens) error {
	if userID == "" || actor.ID <= 0 || strings.TrimSpace(actor.Login) == "" {
		return ErrGitHubActorMismatch
	}
	if err := tokens.validate(); err != nil {
		return err
	}
	sealed, err := s.sealTokens(userID, tokens)
	if err != nil {
		return err
	}
	key := models4datatug.NewGithubAppCredentialKey(userID)
	return s.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		value := new(credentialRecord)
		err := tx.Get(ctx, record.NewRecordWithData(key, value))
		if err != nil && !errors.Is(err, record.ErrRecordNotFound) {
			return err
		}
		if err == nil {
			value.Revision++
		} else {
			value.Revision = 1
		}
		value.ActorID = actor.ID
		value.ActorLogin = actor.Login
		value.Token = sealed
		value.AccessExpiresAt = tokens.accessExpiresAt
		value.RefreshExpiresAt = tokens.refreshExpiresAt
		value.RefreshState = refreshStateIdle
		value.RefreshAttemptID = ""
		value.RefreshStartedAt = time.Time{}
		return tx.Set(ctx, record.NewRecordWithData(key, value))
	})
}

func (s *CredentialStore) loadCredentials(ctx context.Context, userID string) (credentialSnapshot, error) {
	if userID == "" {
		return credentialSnapshot{}, ErrCredentialMissing
	}
	value := new(credentialRecord)
	key := models4datatug.NewGithubAppCredentialKey(userID)
	if err := s.db.Get(ctx, record.NewRecordWithData(key, value)); err != nil {
		if errors.Is(err, record.ErrRecordNotFound) {
			return credentialSnapshot{}, ErrCredentialMissing
		}
		return credentialSnapshot{}, err
	}
	if value.RefreshState == refreshStateReconnect {
		return credentialSnapshot{}, ErrReauthorizationRequired
	}
	plaintext, err := s.tokenCipher.open(userID, value.Token)
	if err != nil {
		return credentialSnapshot{}, ErrReauthorizationRequired
	}
	var envelope tokenEnvelope
	if json.Unmarshal(plaintext, &envelope) != nil {
		return credentialSnapshot{}, ErrReauthorizationRequired
	}
	tokens, err := newOAuthTokens(envelope.AccessToken, envelope.RefreshToken, envelope.AccessExpiresAt, envelope.RefreshExpiresAt)
	if err != nil {
		return credentialSnapshot{}, ErrReauthorizationRequired
	}
	return credentialSnapshot{
		ActorID: value.ActorID, ActorLogin: value.ActorLogin, Tokens: tokens,
		Revision: value.Revision, RefreshState: value.RefreshState,
		RefreshAttempt: value.RefreshAttemptID, RefreshStarted: value.RefreshStartedAt,
	}, nil
}

func (s *CredentialStore) beginRefresh(ctx context.Context, userID string, revision int64, attemptID string) (bool, error) {
	if userID == "" || attemptID == "" {
		return false, ErrCredentialMissing
	}
	key := models4datatug.NewGithubAppCredentialKey(userID)
	var busy, changed, reconnect bool
	err := s.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		value := new(credentialRecord)
		if err := tx.Get(ctx, record.NewRecordWithData(key, value)); err != nil {
			return ErrCredentialMissing
		}
		if value.RefreshState == refreshStateReconnect {
			reconnect = true
			return nil
		}
		if value.Revision != revision {
			changed = true
			return nil
		}
		now := s.now()
		if value.RefreshState == refreshStatePending {
			if now.Sub(value.RefreshStartedAt) <= s.refreshLease {
				busy = true
				return nil
			}
			value.RefreshState = refreshStateReconnect
			value.RefreshAttemptID = ""
			if err := tx.Set(ctx, record.NewRecordWithData(key, value)); err != nil {
				return err
			}
			reconnect = true
			return nil
		}
		value.RefreshState = refreshStatePending
		value.RefreshAttemptID = attemptID
		value.RefreshStartedAt = now
		if err := tx.Set(ctx, record.NewRecordWithData(key, value)); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	switch {
	case reconnect:
		return false, ErrReauthorizationRequired
	case busy:
		return false, ErrRefreshInProgress
	case changed:
		return false, ErrCredentialChanged
	default:
		return true, nil
	}
}

func (s *CredentialStore) completeRefresh(ctx context.Context, userID, attemptID string, revision int64, tokens OAuthTokens) error {
	if err := tokens.validate(); err != nil {
		return err
	}
	sealed, err := s.sealTokens(userID, tokens)
	if err != nil {
		return err
	}
	key := models4datatug.NewGithubAppCredentialKey(userID)
	return s.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		value := new(credentialRecord)
		if err := tx.Get(ctx, record.NewRecordWithData(key, value)); err != nil {
			return ErrReauthorizationRequired
		}
		if value.Revision != revision || value.RefreshState != refreshStatePending || value.RefreshAttemptID != attemptID {
			return ErrCredentialChanged
		}
		value.Token = sealed
		value.AccessExpiresAt = tokens.accessExpiresAt
		value.RefreshExpiresAt = tokens.refreshExpiresAt
		value.Revision++
		value.RefreshState = refreshStateIdle
		value.RefreshAttemptID = ""
		value.RefreshStartedAt = time.Time{}
		return tx.Set(ctx, record.NewRecordWithData(key, value))
	})
}

func (s *CredentialStore) requireReconnect(ctx context.Context, userID, attemptID string, revision int64) {
	key := models4datatug.NewGithubAppCredentialKey(userID)
	_ = s.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		value := new(credentialRecord)
		if err := tx.Get(ctx, record.NewRecordWithData(key, value)); err != nil {
			return err
		}
		if value.Revision != revision || value.RefreshState != refreshStatePending || value.RefreshAttemptID != attemptID {
			return nil
		}
		value.RefreshState = refreshStateReconnect
		value.RefreshAttemptID = ""
		return tx.Set(ctx, record.NewRecordWithData(key, value))
	})
}

func (s *CredentialStore) sealTokens(userID string, tokens OAuthTokens) (sealedValue, error) {
	if err := tokens.validate(); err != nil {
		return sealedValue{}, err
	}
	plaintext, err := json.Marshal(tokenEnvelope{
		AccessToken: tokens.accessToken, RefreshToken: tokens.refreshToken,
		AccessExpiresAt: tokens.accessExpiresAt, RefreshExpiresAt: tokens.refreshExpiresAt,
	})
	if err != nil {
		return sealedValue{}, errors.New("cannot encode GitHub credentials")
	}
	return s.tokenCipher.seal(userID, plaintext)
}

func stateDigest(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

func validateUserID(userID string) error {
	if strings.TrimSpace(userID) == "" {
		return ErrCredentialMissing
	}
	return nil
}
