package api4datatug

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/sneat-co/sneat-go-core/facade"
)

type queryActivityRouteServiceFake struct {
	actorID, spaceID, projectID string
	request                     facade4datatug.QueryActivityReport
	issues, reports             int
	err                         error
}

func (s *queryActivityRouteServiceFake) IssueContext(_ context.Context, actorID, spaceID, projectID string) (facade4datatug.QueryActivityContextResponse, error) {
	s.issues++
	s.actorID, s.spaceID, s.projectID = actorID, spaceID, projectID
	return facade4datatug.QueryActivityContextResponse{ContextID: "server-context", ExpiresAtUTC: time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC)}, s.err
}

func (s *queryActivityRouteServiceFake) Report(_ context.Context, actorID, spaceID string, request facade4datatug.QueryActivityReport) (facade4datatug.QueryActivityReportResult, error) {
	s.reports++
	s.actorID, s.spaceID, s.request = actorID, spaceID, request
	return facade4datatug.QueryActivityReportResult{Accepted: true, ReceiptID: "server-receipt"}, s.err
}

func setQueryActivityVerifier(t *testing.T, verifier func(http.ResponseWriter, *http.Request, verify.RequestOptions) (facade.ContextWithUser, error)) {
	t.Helper()
	original := verifyAuthenticatedRequest
	t.Cleanup(func() { verifyAuthenticatedRequest = original })
	verifyAuthenticatedRequest = verifier
}

func TestQueryActivityContextHTTPValidatesExactScopeAndAuthentication(t *testing.T) {
	service := &queryActivityRouteServiceFake{}
	handler := httpGetQueryActivityContext(QueryActivityRouteOptions{Service: service})
	var authCalls int
	setQueryActivityVerifier(t, func(_ http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		authCalls++
		return facade.NewContextWithUser(context.Background(), facade.NewUserContext("actor-a")), nil
	})
	for _, target := range []string{
		"/v0/datatug/projects/query_activity_context?storage=firestore&spaceID=space&project=project&extra=x",
		"/v0/datatug/projects/query_activity_context?storage=firestore&storage=firestore&spaceID=space&project=project",
	} {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		if target == "/v0/datatug/projects/query_activity_context?storage=firestore&spaceID=space&project=project&extra=x" {
			r.URL.RawQuery = "storage=firestore&spaceID=%ZZ&project=project"
		}
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "project") {
			t.Fatalf("invalid query response=%d body=%q", w.Code, w.Body.String())
		}
	}
	unknown := httptest.NewRecorder()
	handler(unknown, httptest.NewRequest(http.MethodGet, "/v0/datatug/projects/query_activity_context?storage=firestore&spaceID=space&project=project&extra=x", nil))
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown query response=%d body=%q", unknown.Code, unknown.Body.String())
	}
	if authCalls != 0 || service.issues != 0 {
		t.Fatalf("invalid locator reached auth=%d service=%d", authCalls, service.issues)
	}
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest(http.MethodGet, "/v0/datatug/projects/query_activity_context?storage=firestore&spaceID=space&project=project", nil))
	if w.Code != http.StatusOK || service.issues != 1 || service.actorID != "actor-a" || service.spaceID != "space" || service.projectID != "project" || !strings.Contains(w.Body.String(), "server-context") {
		t.Fatalf("context response=%d body=%q call=%+v", w.Code, w.Body.String(), service)
	}
}

func TestQueryActivityContextHTTPFailsClosedForInvalidIdentityAndAuthorityErrors(t *testing.T) {
	service := &queryActivityRouteServiceFake{}
	handler := httpGetQueryActivityContext(QueryActivityRouteOptions{Service: service})
	target := "/v0/datatug/projects/query_activity_context?storage=firestore&spaceID=space&project=project"

	setQueryActivityVerifier(t, func(_ http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		t.Fatal("invalid shared-project identifier reached authentication")
		return nil, nil
	})
	invalid := httptest.NewRecorder()
	handler(invalid, httptest.NewRequest(http.MethodGet, strings.Replace(target, "spaceID=space", "spaceID=space%2Fother", 1), nil))
	if invalid.Code != http.StatusBadRequest || service.issues != 0 {
		t.Fatalf("invalid identity response=%d service calls=%d", invalid.Code, service.issues)
	}

	setQueryActivityVerifier(t, func(w http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		w.WriteHeader(http.StatusUnauthorized)
		return nil, errors.New("invalid credentials")
	})
	authFailure := httptest.NewRecorder()
	handler(authFailure, httptest.NewRequest(http.MethodGet, target, nil))
	if authFailure.Code != http.StatusUnauthorized || service.issues != 0 {
		t.Fatalf("authentication failure response=%d service calls=%d", authFailure.Code, service.issues)
	}

	setQueryActivityVerifier(t, func(_ http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		return facade.NewContextWithUser(context.Background(), nil), nil
	})
	missingUser := httptest.NewRecorder()
	handler(missingUser, httptest.NewRequest(http.MethodGet, target, nil))
	if missingUser.Code != http.StatusUnauthorized || service.issues != 0 {
		t.Fatalf("missing verified user response=%d service calls=%d", missingUser.Code, service.issues)
	}

	service.err = facade4datatug.ErrQueryActivityUnauthorized
	setQueryActivityVerifier(t, func(_ http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		return facade.NewContextWithUser(context.Background(), facade.NewUserContext("verified-user")), nil
	})
	denied := httptest.NewRecorder()
	handler(denied, httptest.NewRequest(http.MethodGet, target, nil))
	if denied.Code != http.StatusForbidden || service.issues != 1 || strings.Contains(denied.Body.String(), "verified-user") {
		t.Fatalf("authority denial response=%d calls=%d body=%q", denied.Code, service.issues, denied.Body.String())
	}
}

func TestQueryActivityReportHTTPStrictBodyAndScopedActor(t *testing.T) {
	service := &queryActivityRouteServiceFake{}
	handler := httpPostQueryActivityReport(QueryActivityRouteOptions{Service: service})
	setQueryActivityVerifier(t, func(_ http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		return facade.NewContextWithUser(context.Background(), facade.NewUserContext("verified-actor")), nil
	})
	query := "?storage=firestore&spaceID=space"
	base := "/v0/datatug/projects/query_activity_report" + query
	for _, test := range []struct {
		name, target, contentType, body string
		wantStatus                      int
	}{
		{"valid", base, "application/json; charset=utf-8", `{"contextID":"server-context","operationID":"operation-1","kind":"query_edit"}`, http.StatusAccepted},
		{"unknown body field", base, "application/json", `{"contextID":"server-context","operationID":"operation-1","kind":"query_edit","actorID":"forged"}`, http.StatusBadRequest},
		{"duplicate JSON member", base, "application/json", `{"contextID":"server-context","contextID":"other","operationID":"operation-1","kind":"query_edit"}`, http.StatusBadRequest},
		{"duplicate nested JSON member", base, "application/json", `{"contextID":"server-context","operationID":"operation-1","kind":"query_edit","metadata":{"x":1,"x":2}}`, http.StatusBadRequest},
		{"duplicate member inside JSON array", base, "application/json", `{"contextID":"server-context","operationID":"operation-1","kind":"query_edit","metadata":[{"x":1,"x":2}]}`, http.StatusBadRequest},
		{"invalid kind", base, "application/json", `{"contextID":"server-context","operationID":"operation-1","kind":"query_text"}`, http.StatusBadRequest},
		{"trailing JSON", base, "application/json", `{"contextID":"server-context","operationID":"operation-1","kind":"query_edit"}{}`, http.StatusBadRequest},
		{"wrong content type", base, "application/jsonfoo", `{"contextID":"server-context","operationID":"operation-1","kind":"query_edit"}`, http.StatusUnsupportedMediaType},
		{"unknown URL query", base + "&project=project", "application/json", `{}`, http.StatusBadRequest},
		{"duplicate scope", base + "&spaceID=other", "application/json", `{}`, http.StatusBadRequest},
		{"malformed URL query", "/v0/datatug/projects/query_activity_report?storage=firestore&spaceID=%ZZ", "application/json", `{}`, http.StatusBadRequest},
		{"oversized body", base, "application/json", `{"padding":"` + strings.Repeat("x", queryActivityBodyLimit) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := service.reports
			r := httptest.NewRequest(http.MethodPost, test.target, strings.NewReader(test.body))
			r.Header.Set("Content-Type", test.contentType)
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != test.wantStatus || strings.Contains(w.Body.String(), "forged") || strings.Contains(w.Body.String(), "padding") {
				t.Fatalf("response=%d body=%q", w.Code, w.Body.String())
			}
			if test.name == "valid" {
				if service.reports != before+1 || service.actorID != "verified-actor" || service.spaceID != "space" || service.request.ContextID != "server-context" || service.request.OperationID != "operation-1" || service.request.Kind != models4datatug.QueryActivityEdit {
					t.Fatalf("unexpected service input: %+v", service)
				}
			} else if service.reports != before {
				t.Fatalf("invalid request reached service: %+v", service)
			}
		})
	}
}

func TestQueryActivityReportHTTPMapsRateLimitAndAuthenticationFailure(t *testing.T) {
	service := &queryActivityRouteServiceFake{err: facade4datatug.ErrQueryActivityRateLimited}
	handler := httpPostQueryActivityReport(QueryActivityRouteOptions{Service: service})
	target := "/v0/datatug/projects/query_activity_report?storage=firestore&spaceID=space"
	setQueryActivityVerifier(t, func(_ http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		return facade.NewContextWithUser(context.Background(), facade.NewUserContext("actor")), nil
	})
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"contextID":"ctx","operationID":"op","kind":"query_edit"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != http.StatusTooManyRequests || strings.Contains(w.Body.String(), "actor") {
		t.Fatalf("rate response=%d body=%q", w.Code, w.Body.String())
	}

	service.err = nil
	before := service.reports
	setQueryActivityVerifier(t, func(w http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		w.WriteHeader(http.StatusUnauthorized)
		return nil, errors.New("bad credentials")
	})
	r = httptest.NewRequest(http.MethodPost, target, strings.NewReader(fmt.Sprintf(`{"contextID":"%s","operationID":"op","kind":"query_edit"}`, strings.Repeat("sensitive", 4))))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler(w, r)
	if w.Code != http.StatusUnauthorized || service.reports != before || strings.Contains(w.Body.String(), "sensitive") {
		t.Fatalf("auth response=%d body=%q service=%+v", w.Code, w.Body.String(), service)
	}
	setQueryActivityVerifier(t, func(_ http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		return facade.NewContextWithUser(context.Background(), nil), nil
	})
	r = httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"contextID":"ctx","operationID":"op","kind":"query_edit"}`))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler(w, r)
	if w.Code != http.StatusUnauthorized || service.reports != before || strings.Contains(w.Body.String(), "ctx") {
		t.Fatalf("missing verified actor response=%d body=%q service=%+v", w.Code, w.Body.String(), service)
	}
}

func TestQueryActivityReportRejectsMalformedJSONContainers(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"empty", ""},
		{"array root", `[]`},
		{"missing object close", `{"contextID":"ctx","operationID":"op","kind":"query_edit"`},
		{"malformed array", `{"contextID":"ctx","operationID":"op","kind":[}`},
		{"malformed nested object", `{"contextID":"ctx","operationID":"op","kind":{"nested":`},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &queryActivityRouteServiceFake{}
			handler := httpPostQueryActivityReport(QueryActivityRouteOptions{Service: service})
			setQueryActivityVerifier(t, func(_ http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
				return facade.NewContextWithUser(context.Background(), facade.NewUserContext("actor")), nil
			})
			r := httptest.NewRequest(http.MethodPost, "/v0/datatug/projects/query_activity_report?storage=firestore&spaceID=space", strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != http.StatusBadRequest || service.reports != 0 || test.body != "" && strings.Contains(w.Body.String(), test.body) {
				t.Fatalf("malformed body response=%d body=%q reports=%d", w.Code, w.Body.String(), service.reports)
			}
		})
	}
}

type queryActivityReadFailureBody struct{}

func (queryActivityReadFailureBody) Read([]byte) (int, error) {
	return 0, errors.New("body read failed")
}
func (queryActivityReadFailureBody) Close() error { return nil }

func TestQueryActivityReportMapsBodyReadFailureWithoutDetails(t *testing.T) {
	service := &queryActivityRouteServiceFake{}
	handler := httpPostQueryActivityReport(QueryActivityRouteOptions{Service: service})
	setQueryActivityVerifier(t, func(_ http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		return facade.NewContextWithUser(context.Background(), facade.NewUserContext("actor")), nil
	})
	r := httptest.NewRequest(http.MethodPost, "/v0/datatug/projects/query_activity_report?storage=firestore&spaceID=space", nil)
	r.Body = queryActivityReadFailureBody{}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != http.StatusBadRequest || service.reports != 0 || strings.Contains(w.Body.String(), "body read failed") {
		t.Fatalf("body read failure response=%d body=%q reports=%d", w.Code, w.Body.String(), service.reports)
	}
}

func TestQueryActivityReportHTTPMapsDomainErrorsWithoutDetails(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"invalid request", facade4datatug.ErrQueryActivityInvalid, http.StatusBadRequest},
		{"revoked authority", facade4datatug.ErrQueryActivityUnauthorized, http.StatusForbidden},
		{"receipt conflict", facade4datatug.ErrQueryActivityConflict, http.StatusConflict},
		{"storage unavailable", errors.New("private datastore details"), http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &queryActivityRouteServiceFake{err: test.err}
			handler := httpPostQueryActivityReport(QueryActivityRouteOptions{Service: service})
			setQueryActivityVerifier(t, func(_ http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
				return facade.NewContextWithUser(context.Background(), facade.NewUserContext("verified-user")), nil
			})
			r := httptest.NewRequest(http.MethodPost, "/v0/datatug/projects/query_activity_report?storage=firestore&spaceID=space",
				strings.NewReader(`{"contextID":"server-context","operationID":"operation-1","kind":"query_edit"}`))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != test.want || strings.Contains(w.Body.String(), "private datastore details") || strings.Contains(w.Body.String(), "verified-user") {
				t.Fatalf("response=%d body=%q, want status=%d without private details", w.Code, w.Body.String(), test.want)
			}
		})
	}
}
