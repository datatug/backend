package facade4datatug

import (
	"context"
	"errors"
	"testing"

	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

const testRedirectURI = "https://datatug.app/github/oauth/callback"

type fakeGithubOAuth struct {
	token       GithubOAuthToken
	err         error
	code        string
	redirectURI string
}

func (f *fakeGithubOAuth) ExchangeCode(_ context.Context, code, redirectURI string) (GithubOAuthToken, error) {
	f.code, f.redirectURI = code, redirectURI
	return f.token, f.err
}

func newOAuthFacade(port *fakeGithubOAuth) Facade {
	return NewFacade(sneatcoretesting.NewMemoryDB(), fakeIDGenerator{}, port)
}

// The command hands the code and the redirect URI to the host's exchanger and
// returns the token to the client — the secret stays on the server.
func TestFacade_ExchangeGithubOAuthCode(t *testing.T) {
	port := &fakeGithubOAuth{token: GithubOAuthToken{
		AccessToken: "gho_test",
		TokenType:   "bearer",
		Scope:       "repo",
	}}

	response, err := newOAuthFacade(port).ExchangeGithubOAuthCode(
		context.Background(),
		ExchangeGithubOAuthCodeRequest{Code: "code1", RedirectURI: testRedirectURI},
	)
	if err != nil {
		t.Fatalf("ExchangeGithubOAuthCode(): %v", err)
	}
	if port.code != "code1" || port.redirectURI != testRedirectURI {
		t.Errorf("exchanger got code=%q redirectURI=%q, want code1 / %q", port.code, port.redirectURI, testRedirectURI)
	}
	if response.AccessToken != "gho_test" || response.TokenType != "bearer" || response.Scope != "repo" {
		t.Errorf("response = %+v, want the token the exchanger returned", response)
	}
}

func TestFacade_ExchangeGithubOAuthCode_validation(t *testing.T) {
	port := &fakeGithubOAuth{}
	for name, tc := range map[string]struct {
		request ExchangeGithubOAuthCodeRequest
		wantErr error
	}{
		"no code":     {request: ExchangeGithubOAuthCodeRequest{RedirectURI: testRedirectURI}, wantErr: ErrOAuthCodeRequired},
		"no redirect": {request: ExchangeGithubOAuthCodeRequest{Code: "code1"}, wantErr: ErrOAuthRedirectURIRequired},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newOAuthFacade(port).ExchangeGithubOAuthCode(context.Background(), tc.request)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if port.code != "" {
				t.Error("an invalid request must not reach the exchanger")
			}
		})
	}
}

// A host without a configured GitHub OAuth app reports that sign-in is
// unavailable, rather than failing obscurely.
func TestFacade_ExchangeGithubOAuthCode_notConfigured(t *testing.T) {
	// An untyped nil: a typed nil pointer in the interface would not be == nil.
	f := NewFacade(sneatcoretesting.NewMemoryDB(), fakeIDGenerator{}, nil)
	_, err := f.ExchangeGithubOAuthCode(context.Background(),
		ExchangeGithubOAuthCodeRequest{Code: "code1", RedirectURI: testRedirectURI})
	if !errors.Is(err, ErrGithubOAuthNotConfigured) {
		t.Fatalf("error = %v, want %v", err, ErrGithubOAuthNotConfigured)
	}
}

// GitHub's failure (bad/expired code, wrong redirect URI) is wrapped, not
// swallowed.
func TestFacade_ExchangeGithubOAuthCode_portFailure(t *testing.T) {
	githubErr := errors.New("bad_verification_code")
	port := &fakeGithubOAuth{err: githubErr}

	_, err := newOAuthFacade(port).ExchangeGithubOAuthCode(context.Background(),
		ExchangeGithubOAuthCodeRequest{Code: "code1", RedirectURI: testRedirectURI})
	if !errors.Is(err, githubErr) {
		t.Fatalf("error = %v, want it to wrap %v", err, githubErr)
	}
}

// A 200 from GitHub without a token is a failure, not a silent empty sign-in.
func TestFacade_ExchangeGithubOAuthCode_emptyToken(t *testing.T) {
	port := &fakeGithubOAuth{}

	response, err := newOAuthFacade(port).ExchangeGithubOAuthCode(context.Background(),
		ExchangeGithubOAuthCodeRequest{Code: "code1", RedirectURI: testRedirectURI})
	if err == nil {
		t.Fatalf("expected an error, got response %+v", response)
	}
	if response.AccessToken != "" {
		t.Errorf("response must not carry a token: %+v", response)
	}
}
