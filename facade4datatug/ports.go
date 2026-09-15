package facade4datatug

import "context"

// IDGenerator is a PORT: the module must not import sneat-go-core,
// sneat-core-modules or another extension's backend, so anything it needs from
// outside crosses a small interface defined here and is satisfied by an
// adapter in the host composition root
// (sneat-go/pkg/modules/datatug/adapters.go). Domain tests fake the port — see
// facade_test.go's fakeIDGenerator.
type IDGenerator interface {
	// NewID returns the id of a new record.
	NewID(ctx context.Context) (string, error)
}

// GithubOAuthExchanger exchanges a GitHub OAuth authorization code for an
// access token.
//
// It is a PORT for the same reason, and for one more: the exchange needs the
// OAuth app's client secret, which must never reach the browser or this
// module's repository. The host owns the secret and supplies the adapter; when
// it is not configured, the adapter returns an error and the endpoint reports
// that GitHub sign-in is unavailable.
type GithubOAuthExchanger interface {
	// ExchangeCode trades `code` for a token. redirectURI must be the exact
	// URI the authorization request used — GitHub verifies that they match.
	ExchangeCode(ctx context.Context, code, redirectURI string) (GithubOAuthToken, error)
}

// GithubOAuthToken is what the exchanger returns on success.
type GithubOAuthToken struct {
	AccessToken string
	TokenType   string
	Scope       string
}
