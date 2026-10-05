package api4datatug

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/datatug/backend/facade4datatug"
)

// VerifiedPlanCaller is implemented by the host's credential verifier. It
// must check token signature, audience, scope and active grant as applicable;
// this route never regards the Authorization header itself as proof.
type VerifiedPlanCaller interface {
	VerifyPlanCaller(context.Context, *http.Request) (string, error)
}

type PlanRouteOptions struct {
	Verifier VerifiedPlanCaller
	Service  facade4datatug.PersonalPlanService
}

func httpGetPlan(options PlanRouteOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if options.Verifier == nil {
			planError(w, http.StatusServiceUnavailable, "upstream")
			return
		}
		callerID, err := options.Verifier.VerifyPlanCaller(r.Context(), r)
		if err != nil || callerID == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		response, err := options.Service.Read(r.Context(), callerID, r.URL.Query().Get("account"), r.URL.Query().Get("project"))
		if errors.Is(err, facade4datatug.ErrForeignAccount) {
			planError(w, http.StatusForbidden, "not_a_member")
			return
		}
		if err != nil {
			planError(w, http.StatusServiceUnavailable, "upstream")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
}

func planError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": "Plan information is unavailable."}})
}
