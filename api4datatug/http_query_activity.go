package api4datatug

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
)

const queryActivityBodyLimit = 2048

type QueryActivityRouteOptions struct {
	Service QueryActivityRouteService
}

type QueryActivityRouteService interface {
	IssueContext(context.Context, string, string, string) (facade4datatug.QueryActivityContextResponse, error)
	Report(context.Context, string, string, facade4datatug.QueryActivityReport) (facade4datatug.QueryActivityReportResult, error)
}

func httpGetQueryActivityContext(options QueryActivityRouteOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if options.Service == nil {
			sharedProjectError(w, http.StatusServiceUnavailable, "activity_unavailable")
			return
		}
		query, err := strictActivityQuery(r, "storage", "spaceID", "project")
		if err != nil || !singleQueryValue(query, "storage") || query.Get("storage") != models4datatug.FirestoreStoreID || !singleQueryValue(query, "spaceID") || !singleQueryValue(query, "project") {
			sharedProjectError(w, http.StatusBadRequest, "invalid_activity_scope")
			return
		}
		spaceID, projectID := query.Get("spaceID"), query.Get("project")
		if models4datatug.ValidateSharedProjectIdentifier(spaceID) != nil || models4datatug.ValidateSharedProjectIdentifier(projectID) != nil {
			sharedProjectError(w, http.StatusBadRequest, "invalid_activity_scope")
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
		response, err := options.Service.IssueContext(ctx, actorID, spaceID, projectID)
		if err != nil {
			writeQueryActivityError(w, err)
			return
		}
		writeGitHubJSON(w, http.StatusOK, response)
	}
}

func httpPostQueryActivityReport(options QueryActivityRouteOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if options.Service == nil {
			sharedProjectError(w, http.StatusServiceUnavailable, "activity_unavailable")
			return
		}
		query, err := strictActivityQuery(r, "storage", "spaceID")
		if err != nil || !singleQueryValue(query, "storage") || query.Get("storage") != models4datatug.FirestoreStoreID || !singleQueryValue(query, "spaceID") ||
			models4datatug.ValidateSharedProjectIdentifier(query.Get("spaceID")) != nil {
			sharedProjectError(w, http.StatusBadRequest, "invalid_activity_scope")
			return
		}
		contentType, _, contentTypeErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if contentTypeErr != nil || contentType != "application/json" {
			sharedProjectError(w, http.StatusUnsupportedMediaType, "json_required")
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
		var request facade4datatug.QueryActivityReport
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, queryActivityBodyLimit))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				sharedProjectError(w, http.StatusRequestEntityTooLarge, "activity_report_too_large")
				return
			}
			sharedProjectError(w, http.StatusBadRequest, "invalid_activity_report")
			return
		}
		if err := validateUniqueJSONMembers(body); err != nil {
			sharedProjectError(w, http.StatusBadRequest, "invalid_activity_report")
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || ensureJSONEnd(decoder) != nil {
			sharedProjectError(w, http.StatusBadRequest, "invalid_activity_report")
			return
		}
		if !request.Kind.Valid() {
			sharedProjectError(w, http.StatusBadRequest, "invalid_activity_report")
			return
		}
		result, err := options.Service.Report(ctx, actorID, query.Get("spaceID"), request)
		if err != nil {
			writeQueryActivityError(w, err)
			return
		}
		writeGitHubJSON(w, http.StatusAccepted, result)
	}
}

func singleQueryValue(values url.Values, key string) bool {
	return len(values[key]) == 1 && values[key][0] != ""
}

func strictActivityQuery(r *http.Request, allowed ...string) (url.Values, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	allow := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allow[key] = struct{}{}
	}
	for key := range query {
		if _, ok := allow[key]; !ok {
			return nil, errors.New("unexpected query parameter")
		}
	}
	return query, nil
}

func validateUniqueJSONMembers(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	opening, ok := token.(json.Delim)
	if !ok || opening != '{' {
		return errors.New("activity report must be an object")
	}
	if err := consumeJSONMembers(decoder, '}'); err != nil {
		return err
	}
	return ensureJSONEnd(decoder)
}

func consumeJSONMembers(decoder *json.Decoder, closing json.Delim) error {
	if closing == '}' {
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid object key")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate JSON member")
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	} else {
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	}
	end, err := decoder.Token()
	if err != nil || end != closing {
		return errors.New("invalid JSON container")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			return consumeJSONMembers(decoder, '}')
		case '[':
			return consumeJSONMembers(decoder, ']')
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	return nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}

func writeQueryActivityError(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "activity_unavailable"
	switch {
	case errors.Is(err, facade4datatug.ErrQueryActivityInvalid):
		status, code = http.StatusBadRequest, "invalid_activity"
	case errors.Is(err, facade4datatug.ErrQueryActivityUnauthorized):
		status, code = http.StatusForbidden, "activity_denied"
	case errors.Is(err, facade4datatug.ErrQueryActivityConflict):
		status, code = http.StatusConflict, "activity_conflict"
	case errors.Is(err, facade4datatug.ErrQueryActivityRateLimited):
		status, code = http.StatusTooManyRequests, "activity_rate_limited"
	}
	sharedProjectError(w, status, code)
}
