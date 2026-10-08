package api4datatug

import (
	"errors"
	"net/http"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
)

func httpGetSharedProjectAIEligibility(options SharedProjectRouteOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if options.Service == nil {
			sharedProjectError(w, http.StatusServiceUnavailable, "project_ai_unavailable")
			return
		}
		query := r.URL.Query()
		if query.Get("storage") != models4datatug.FirestoreStoreID {
			sharedProjectError(w, http.StatusBadRequest, "invalid_store")
			return
		}
		spaceID, projectID := query.Get("spaceID"), query.Get("project")
		if models4datatug.ValidateSharedProjectIdentifier(spaceID) != nil || models4datatug.ValidateSharedProjectIdentifier(projectID) != nil {
			sharedProjectError(w, http.StatusBadRequest, "invalid_project")
			return
		}
		ctx, err := verifyAuthenticatedRequest(w, r, verify.NoContentAuthRequired)
		if err != nil {
			return
		}
		actorID, err := verifiedFirebaseUID(ctx)
		if err != nil {
			sharedProjectError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		result, err := options.Service.ReadSharedProjectAIEligibility(ctx, actorID, spaceID, projectID)
		if err != nil {
			status, code := http.StatusServiceUnavailable, "project_ai_unavailable"
			if errors.Is(err, facade4datatug.ErrSharedProjectUnauthorized) {
				status, code = http.StatusForbidden, "project_denied"
			}
			sharedProjectError(w, status, code)
			return
		}
		writeGitHubJSON(w, http.StatusOK, result)
	}
}
