// Copyright 2026 Sneat.co
package api4datatug

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/sneat-co/sneat-go-core/facade"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

func setAIEligibilityRequestVerifier(t *testing.T, verifier func(http.ResponseWriter, *http.Request, verify.RequestOptions) (facade.ContextWithUser, error)) {
	t.Helper()
	original := verifyAuthenticatedRequest
	t.Cleanup(func() { verifyAuthenticatedRequest = original })
	verifyAuthenticatedRequest = verifier
}

func getSharedProjectAIEligibility(handler http.HandlerFunc, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

func TestSharedProjectAIEligibilityHTTPRejectsInvalidLocatorsBeforeAuthentication(t *testing.T) {
	options := newSharedHTTPOptions(t, sneatcoretesting.NewMemoryDB(), &sharedHTTPAuthority{issuedAt: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)})
	var authCalls int
	setAIEligibilityRequestVerifier(t, func(http.ResponseWriter, *http.Request, verify.RequestOptions) (facade.ContextWithUser, error) {
		authCalls++
		return nil, errors.New("must not authenticate invalid locator")
	})
	handler := httpGetSharedProjectAIEligibility(options)
	for _, test := range []struct {
		name, target, code string
	}{
		{name: "wrong storage", target: "/v0/datatug/projects/ai_eligibility?storage=github.com&spaceID=space&project=project", code: "invalid_store"},
		{name: "invalid space", target: "/v0/datatug/projects/ai_eligibility?storage=firestore&spaceID=../other&project=project", code: "invalid_project"},
		{name: "invalid project", target: "/v0/datatug/projects/ai_eligibility?storage=firestore&spaceID=space&project=", code: "invalid_project"},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := getSharedProjectAIEligibility(handler, test.target)
			if w.Code != http.StatusBadRequest || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
			}
		})
	}
	if authCalls != 0 {
		t.Fatalf("invalid locator reached authentication %d times", authCalls)
	}
}

func TestSharedProjectAIEligibilityHTTPMapsAuthAndProofFailures(t *testing.T) {
	options := newSharedHTTPOptions(t, sneatcoretesting.NewMemoryDB(), &sharedHTTPAuthority{issuedAt: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)})
	handler := httpGetSharedProjectAIEligibility(options)
	validTarget := "/v0/datatug/projects/ai_eligibility?storage=firestore&spaceID=space&project=project"

	setAIEligibilityRequestVerifier(t, func(w http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		w.WriteHeader(http.StatusUnauthorized)
		return nil, errors.New("invalid credential")
	})
	if w := getSharedProjectAIEligibility(handler, validTarget); w.Code != http.StatusUnauthorized || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("authentication response=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
	}

	setAIEligibilityRequestVerifier(t, func(_ http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		return facade.NewContextWithUser(context.Background(), nil), nil
	})
	if w := getSharedProjectAIEligibility(handler, validTarget); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"unauthorized"`) {
		t.Fatalf("missing actor response=%d body=%q", w.Code, w.Body.String())
	}

	setAIEligibilityRequestVerifier(t, func(_ http.ResponseWriter, _ *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		return facade.NewContextWithUser(context.Background(), facade.NewUserContext("user1")), nil
	})
	w := getSharedProjectAIEligibility(handler, validTarget)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"project_ai_unavailable"`) || strings.Contains(w.Body.String(), "personal-1") {
		t.Fatalf("unproved project response=%d body=%q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("cache policy missing: %v", w.Header())
	}
}
