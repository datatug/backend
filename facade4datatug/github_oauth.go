package facade4datatug

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrOAuthCodeRequired is returned when the exchange is called without the
	// authorization code GitHub issued.
	ErrOAuthCodeRequired = errors.New("github oauth code is required")

	// ErrOAuthRedirectURIRequired is returned when the exchange is called
	// without the redirect URI the authorization request used — GitHub verifies
	// that the two match.
	ErrOAuthRedirectURIRequired = errors.New("github oauth redirect uri is required")

	// ErrGithubOAuthNotConfigured is returned when the host has no GitHub OAuth
	// app configured (no client id/secret), so sign-in cannot be completed.
	ErrGithubOAuthNotConfigured = errors.New("github sign-in is not configured on this server")
)

// ExchangeGithubOAuthCodeRequest is the body of a token exchange: the code
// GitHub handed the client at the redirect URI, plus that same URI.
type ExchangeGithubOAuthCodeRequest struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirectUri"`
}

// Validate validates the request.
func (v ExchangeGithubOAuthCodeRequest) Validate() error {
	if v.Code == "" {
		return ErrOAuthCodeRequired
	}
	if v.RedirectURI == "" {
		return ErrOAuthRedirectURIRequired
	}
	return nil
}

// GithubOAuthTokenResponse is the token the client uses to call GitHub. It is
// never persisted server-side.
type GithubOAuthTokenResponse struct {
	AccessToken string `json:"accessToken"`
	TokenType   string `json:"tokenType"`
	Scope       string `json:"scope,omitempty"`
}

// ExchangeGithubOAuthCode completes GitHub sign-in: the client sends the code
// GitHub gave it, the host's exchanger trades it for an access token using the
// OAuth app's client secret, and the client receives the token to call GitHub
// with. The secret never leaves the server, and the token is never stored here.
func (f Facade) ExchangeGithubOAuthCode(
	ctx context.Context, request ExchangeGithubOAuthCodeRequest,
) (response GithubOAuthTokenResponse, err error) {
	if err = request.Validate(); err != nil {
		return response, err
	}
	if f.githubOAuth == nil {
		return response, ErrGithubOAuthNotConfigured
	}
	token, err := f.githubOAuth.ExchangeCode(ctx, request.Code, request.RedirectURI)
	if err != nil {
		return response, fmt.Errorf("failed to exchange the GitHub authorization code: %w", err)
	}
	if token.AccessToken == "" {
		return response, errors.New("github returned an empty access token")
	}
	return GithubOAuthTokenResponse{
		AccessToken: token.AccessToken,
		TokenType:   token.TokenType,
		Scope:       token.Scope,
	}, nil
}
