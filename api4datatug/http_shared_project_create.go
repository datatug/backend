package api4datatug

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/strongo/validation"
)

// SharedProjectRouteOptions are supplied only by RegisterHttpRoutesWithOptions;
// the legacy route registration leaves shared-project routes unmounted. A
// configured service proves current linked membership and sponsor status for
// AI eligibility, while project writes still require mutation-time paid checks.
// Host activation also requires reviewed Firebase rules: today's broad Space
// member grants do not make the separate create-receipt subtree private.
type SharedProjectRouteOptions struct {
	Service *facade4datatug.SharedProjectService
}

type CreateSharedProjectRequest struct {
	SpaceID       string                                    `json:"spaceID"`
	CommandID     string                                    `json:"commandID"`
	Title         string                                    `json:"title"`
	BillingIntent facade4datatug.SharedProjectBillingIntent `json:"billingIntent,omitempty"`
}

func (r CreateSharedProjectRequest) Validate() error {
	if r.BillingIntent.Validate() != nil {
		return validation.NewErrBadRequestFieldValue("billingIntent", "unsupported billing intent")
	}
	for _, field := range []struct{ name, value string }{{"spaceID", r.SpaceID}, {"commandID", r.CommandID}} {
		if err := models4datatug.ValidateSharedProjectIdentifier(field.value); err != nil {
			return validation.NewErrBadRequestFieldValue(field.name, err.Error())
		}
	}
	if err := models4datatug.ValidateSharedProjectTitle(r.Title); err != nil {
		return validation.NewErrBadRequestFieldValue("title", err.Error())
	}
	return nil
}

func httpPostCreateSharedProject(options SharedProjectRouteOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if options.Service == nil {
			sharedProjectError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		var request CreateSharedProjectRequest
		ctx, err := verifyAuthenticatedRequestAndDecodeBody(w, r, verify.DefaultJsonWithAuthRequired, &request)
		if err != nil {
			return
		}
		if ctx == nil || ctx.User() == nil || ctx.User().GetUserID() == "" {
			sharedProjectError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		response, err := options.Service.Create(ctx, facade4datatug.SharedProjectCreateCommand{
			ActorID: ctx.User().GetUserID(), SpaceID: request.SpaceID, CommandID: request.CommandID, Title: request.Title, BillingIntent: request.BillingIntent,
		})
		if err != nil {
			status, code := http.StatusServiceUnavailable, "unavailable"
			switch {
			case errors.Is(err, facade4datatug.ErrSharedProjectUnauthorized):
				status, code = http.StatusForbidden, "unauthorized"
			case errors.Is(err, facade4datatug.ErrSharedProjectConflict):
				status, code = http.StatusConflict, "conflict"
			case errors.Is(err, facade4datatug.ErrSharedProjectInvalid):
				status, code = http.StatusBadRequest, "invalid"
			}
			sharedProjectError(w, status, code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(response)
	}
}

func sharedProjectError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
}
