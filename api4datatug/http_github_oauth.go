package api4datatug

import (
	"fmt"
	"net/http"

	"github.com/datatug/backend/facade4datatug"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/sneat-co/sneat-go-core/facade"
	"github.com/strongo/validation"
)

// ExchangeGithubOAuthCodeRequest is the body of a GitHub OAuth token exchange.
type ExchangeGithubOAuthCodeRequest struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirectUri"`
}

// Validate validates the request.
func (v ExchangeGithubOAuthCodeRequest) Validate() error {
	if v.Code == "" {
		return validation.NewErrRequestIsMissingRequiredField("code")
	}
	if v.RedirectURI == "" {
		return validation.NewErrRequestIsMissingRequiredField("redirectUri")
	}
	return nil
}

// httpPostExchangeGithubOAuthCode completes GitHub sign-in.
//
// Request:  POST /v0/datatug/github/oauth_token
//
//	Authorization: Bearer <firebase-id-token>   (auth REQUIRED)
//	Content-Type: application/json
//	Body: {"code":"<code from GitHub>","redirectUri":"<the one we sent>"}
//
// Response: 200 OK
//
//	{"accessToken":"...","tokenType":"bearer","scope":"repo"}
//
// The OAuth app's client secret lives only in the host's configuration: this
// endpoint exchanges the code server-side so the browser never sees it. The
// returned token is the user's own GitHub token, kept client-side.
func httpPostExchangeGithubOAuthCode(
	ids facade4datatug.IDGenerator,
	githubOAuth facade4datatug.GithubOAuthExchanger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request ExchangeGithubOAuthCodeRequest
		ctx, err := verifyAuthenticatedRequestAndDecodeBody(w, r, verify.DefaultJsonWithAuthRequired, &request)
		if err != nil {
			return
		}
		db, err := facade.GetSneatDB(ctx)
		if err != nil {
			err = fmt.Errorf("failed to get a database: %w", err)
			apicore.ReturnJSON(ctx, w, r, http.StatusOK, err, nil)
			return
		}
		f := facade4datatug.NewFacade(db, ids, githubOAuth)
		response, err := f.ExchangeGithubOAuthCode(ctx, facade4datatug.ExchangeGithubOAuthCodeRequest{
			Code:        request.Code,
			RedirectURI: request.RedirectURI,
		})
		apicore.ReturnJSON(ctx, w, r, http.StatusOK, err, &response)
	}
}
