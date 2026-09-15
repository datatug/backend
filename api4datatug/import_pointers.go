package api4datatug

import (
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/sneat-co/sneat-go-core/facade"
	"net/http"
)

// verifyAuthenticatedRequestAndDecodeBody is a seam over apicore's helper (the
// convention used by sneat-go's api4notificator) so handlers stay testable: a
// test swaps it to inject an authenticated user context and a decoded body
// without minting a real auth token.
var verifyAuthenticatedRequestAndDecodeBody = apicore.VerifyAuthenticatedRequestAndDecodeBody

// The seam's shape, kept next to the variable so the two cannot drift.
var _ func(
	w http.ResponseWriter, r *http.Request, options verify.RequestOptions, request facade.Request,
) (facade.ContextWithUser, error) = verifyAuthenticatedRequestAndDecodeBody
