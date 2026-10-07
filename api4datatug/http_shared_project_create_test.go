package api4datatug

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/sneat-co/sneat-go-core/facade"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
	"github.com/strongo/validation"
)

type sharedHTTPAuthority struct {
	issuedAt  time.Time
	err       error
	validated int
}

func (a *sharedHTTPAuthority) PrepareSharedProjectCreate(ctx context.Context, binding facade4datatug.SharedProjectCreateBinding) (facade4datatug.PreparedSharedProjectCreate, error) {
	caller, ok := ctx.(facade.ContextWithUser)
	if !ok || caller.User().GetUserID() != binding.ActorID || binding.ActorID != "user1" {
		return facade4datatug.PreparedSharedProjectCreate{}, errors.New("caller mismatch")
	}
	return facade4datatug.PreparedSharedProjectCreate{Binding: binding, IssuedAt: a.issuedAt, Validator: a}, nil
}

func (a *sharedHTTPAuthority) ValidateSharedProjectCreateInTransaction(_ context.Context, _ dal.ReadwriteTransaction, _ facade4datatug.SharedProjectCreateBinding, _ time.Time) error {
	a.validated++
	return a.err
}

func newSharedHTTPOptions(t *testing.T, db dal.DB, a *sharedHTTPAuthority) SharedProjectRouteOptions {
	t.Helper()
	s, err := facade4datatug.NewSharedProjectService(db, fakeIDs{next: "shared-project"}, a, func() time.Time { return a.issuedAt })
	if err != nil {
		t.Fatal(err)
	}
	return SharedProjectRouteOptions{Service: s}
}

func postShared(handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest(http.MethodPost, "/v0/datatug/projects/create_shared_project", strings.NewReader(body)))
	return w
}

func TestSharedProjectHTTPVerifiedActorReplayAndErrors(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	stubVerification(t, db)
	a := &sharedHTTPAuthority{issuedAt: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	h := httpPostCreateSharedProject(newSharedHTTPOptions(t, db, a))
	body := `{"spaceID":"space","commandID":"command","title":"Shared","actorID":"foreign","userIDs":["foreign"],"payerID":"forged"}`
	first := postShared(h, body)
	if first.Code != http.StatusCreated || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response %d %s", first.Code, first.Body.String())
	}
	var ref models4datatug.SharedProjectRef
	if err := json.Unmarshal(first.Body.Bytes(), &ref); err != nil || ref != (models4datatug.SharedProjectRef{StoreID: "firestore", SpaceID: "space", ProjectID: "shared-project"}) {
		t.Fatalf("ref %+v: %v", ref, err)
	}
	r, receipt := models4datatug.NewSharedProjectCreateReceiptRecord("space", "command")
	if err := db.Get(context.Background(), r); err != nil || receipt.ActorID != "user1" {
		t.Fatalf("receipt %+v: %v", receipt, err)
	}
	if replay := postShared(h, body); replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() || a.validated != 2 {
		t.Fatalf("replay %d %s, validation %d", replay.Code, replay.Body.String(), a.validated)
	}
	if conflict := postShared(h, `{"spaceID":"space","commandID":"command","title":"Changed"}`); conflict.Code != http.StatusConflict {
		t.Fatalf("conflict %d %s", conflict.Code, conflict.Body.String())
	}
	a.err = errors.New("secret internal role failure")
	if denied := postShared(h, body); denied.Code != http.StatusForbidden || strings.Contains(denied.Body.String(), "secret") {
		t.Fatalf("denied %d %s", denied.Code, denied.Body.String())
	}
	for _, unsafe := range []string{`{"spaceID":"../foreign","commandID":"command","title":"T"}`, `{"spaceID":"space","commandID":"","title":"T"}`, `{"spaceID":"space","commandID":"command","title":" "}`, `not-json`} {
		if rejected := postShared(h, unsafe); rejected.Code != http.StatusBadRequest {
			t.Fatalf("invalid %q: %d", unsafe, rejected.Code)
		}
	}
}

func TestSharedProjectHTTPDisabledAndAuthenticationFailure(t *testing.T) {
	original := verifyAuthenticatedRequestAndDecodeBody
	t.Cleanup(func() { verifyAuthenticatedRequestAndDecodeBody = original })
	verifyAuthenticatedRequestAndDecodeBody = func(_ http.ResponseWriter, _ *http.Request, _ verify.RequestOptions, _ facade.Request) (facade.ContextWithUser, error) {
		t.Fatal("disabled handler attempted authentication")
		return nil, nil
	}
	if w := postShared(httpPostCreateSharedProject(SharedProjectRouteOptions{}), `{}`); w.Code != http.StatusServiceUnavailable {
		t.Fatal(w.Code)
	}
	db := sneatcoretesting.NewMemoryDB()
	a := &sharedHTTPAuthority{issuedAt: time.Now().UTC()}
	h := httpPostCreateSharedProject(newSharedHTTPOptions(t, db, a))
	verifyAuthenticatedRequestAndDecodeBody = func(w http.ResponseWriter, _ *http.Request, _ verify.RequestOptions, _ facade.Request) (facade.ContextWithUser, error) {
		w.WriteHeader(http.StatusUnauthorized)
		return nil, errors.New("invalid token")
	}
	if w := postShared(h, `{}`); w.Code != http.StatusUnauthorized || a.validated != 0 {
		t.Fatalf("auth %d validations=%d", w.Code, a.validated)
	}
	verifyAuthenticatedRequestAndDecodeBody = func(_ http.ResponseWriter, _ *http.Request, _ verify.RequestOptions, _ facade.Request) (facade.ContextWithUser, error) {
		return nil, nil
	}
	if w := postShared(h, `{}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("missing verified context %d", w.Code)
	}
}

func TestSharedProjectRouteOptionsAreAdditiveAndDisabled(t *testing.T) {
	routes := map[string]http.HandlerFunc{}
	RegisterHttpRoutesWithOptions(func(method, path string, h http.HandlerFunc) { routes[method+" "+path] = h }, fakeIDs{}, RouteOptions{})
	if len(routes) != 7 {
		t.Fatalf("routes %+v", routes)
	}
	h := routes["POST /v0/datatug/projects/create_shared_project"]
	if h == nil {
		t.Fatal("missing shared route")
	}
	if w := postShared(h, `{}`); w.Code != http.StatusServiceUnavailable {
		t.Fatal(w.Code)
	}
	if routes["POST /v0/datatug/github/authorization/start"] == nil || routes["POST /v0/datatug/github/authorization/complete"] == nil || routes["GET /v0/datatug/github/repositories"] == nil {
		t.Fatalf("missing additive GitHub authorization routes: %+v", routes)
	}
	for _, route := range []string{"POST /v0/datatug/github/authorization/start", "POST /v0/datatug/github/authorization/complete", "GET /v0/datatug/github/repositories"} {
		w := httptest.NewRecorder()
		routes[route](w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("unconfigured %s route status=%d", route, w.Code)
		}
	}
	for _, field := range []CreateSharedProjectRequest{{SpaceID: "../bad", CommandID: "command", Title: "T"}, {SpaceID: "space", CommandID: "command", Title: ""}} {
		if err := field.Validate(); !validation.IsBadRequestError(err) {
			t.Fatalf("validation is not a client error: %v", err)
		}
	}
}
