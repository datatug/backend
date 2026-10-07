package api4datatug

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/githubauth4datatug"
	"github.com/datatug/backend/template4datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/sneat-co/sneat-go-core/facade"
)

const routeHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const routeTree = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type routeRepo struct {
	scope     githubauth4datatug.RepositoryScope
	tree      []githubauth4datatug.GitHubTreeEntry
	blobs     map[string][]byte
	refCalls  int
	branches  []githubauth4datatug.GitHubBranch
	branchErr error
}

func (r *routeRepo) Scope() githubauth4datatug.RepositoryScope { return r.scope }
func (r *routeRepo) ListBranches(context.Context) ([]githubauth4datatug.GitHubBranch, error) {
	if r.branchErr != nil {
		return nil, r.branchErr
	}
	if r.branches != nil {
		return r.branches, nil
	}
	return []githubauth4datatug.GitHubBranch{{Name: "work", OID: routeHead}}, nil
}
func (r *routeRepo) GetRef(_ context.Context, branch string) (githubauth4datatug.GitHubRef, error) {
	r.refCalls++
	return githubauth4datatug.GitHubRef{Name: branch, OID: routeHead}, nil
}
func (r *routeRepo) GetCommit(_ context.Context, oid string) (githubauth4datatug.GitHubCommit, error) {
	if oid != routeHead {
		return githubauth4datatug.GitHubCommit{}, errors.New("wrong head")
	}
	return githubauth4datatug.GitHubCommit{OID: routeHead, TreeOID: routeTree}, nil
}
func (r *routeRepo) GetTree(_ context.Context, oid string) ([]githubauth4datatug.GitHubTreeEntry, error) {
	if oid != routeTree {
		return nil, errors.New("wrong tree")
	}
	return r.tree, nil
}
func (r *routeRepo) GetBlob(_ context.Context, oid string) ([]byte, error) {
	value, ok := r.blobs[oid]
	if !ok {
		return nil, errors.New("wrong blob")
	}
	return value, nil
}
func routeBlobOID(content []byte) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(content))
	_, _ = h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}
func newRouteRepo(t *testing.T, permission githubauth4datatug.RepositoryOperation) *routeRepo {
	t.Helper()
	files, err := template4datatug.CloneDemoProject("datatug", "shared-project", "Owned project", time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	repo := &routeRepo{scope: githubauth4datatug.RepositoryScope{FirebaseUID: "firebase-actor", Repository: githubauth4datatug.GitHubRepository{ID: 91, Owner: "owner", Name: "repo", DefaultBranch: "main"}, Permission: permission}, blobs: map[string][]byte{}}
	for name, content := range files {
		oid := routeBlobOID(content)
		repo.blobs[oid] = content
		repo.tree = append(repo.tree, githubauth4datatug.GitHubTreeEntry{Path: name, Type: "blob", OID: oid, Mode: "100644", Size: int64(len(content))})
	}
	return repo
}

type routeProvider struct {
	repo             *routeRepo
	listErr, authErr error
	listedRepos      []githubauth4datatug.GitHubRepository
	listed           int
	authorized       int
}

func (p *routeProvider) ListRepositories(_ context.Context, uid string) ([]githubauth4datatug.GitHubRepository, error) {
	p.listed++
	if uid != "firebase-actor" {
		return nil, errors.New("wrong actor")
	}
	if p.listErr != nil {
		return nil, p.listErr
	}
	if p.listedRepos != nil {
		return p.listedRepos, nil
	}
	return []githubauth4datatug.GitHubRepository{{ID: 91, Owner: "owner", Name: "repo", DefaultBranch: p.repo.scope.Repository.DefaultBranch, EffectivePermission: p.repo.scope.Permission}}, nil
}
func (p *routeProvider) AuthorizeReadRepository(_ context.Context, uid string, ref githubauth4datatug.RepositoryRef) (githubProjectReadRepository, error) {
	p.authorized++
	if p.authErr != nil {
		return nil, p.authErr
	}
	if uid != "firebase-actor" || ref.ID != 91 {
		return nil, errors.New("wrong repo")
	}
	return p.repo, nil
}

type routeService struct {
	readErr, writeErr, saveErr, errorCreate       error
	readCalls, writeCalls, saveCalls, createCalls int
	createCommand                                 facade4datatug.GitHubProjectCreateCommand
	received                                      dto.SaveQueryRequest
}

func (s *routeService) ResolveGitHubProject(_ context.Context, uid string, id int64, owner, name, folder string) (facade4datatug.GitHubProjectAccess, error) {
	s.readCalls++
	if s.readErr != nil {
		return facade4datatug.GitHubProjectAccess{}, s.readErr
	}
	if uid != "firebase-actor" || id != 91 || owner != "owner" || name != "repo" || folder != "datatug" {
		return facade4datatug.GitHubProjectAccess{}, errors.New("wrong project")
	}
	return facade4datatug.GitHubProjectAccess{SpaceID: "space", SharedProjectID: "shared-project"}, nil
}
func (s *routeService) AuthorizeGitHubProjectWrite(_ context.Context, _ string, _ int64, _, _, _ string) (facade4datatug.GitHubProjectAccess, error) {
	s.writeCalls++
	return facade4datatug.GitHubProjectAccess{}, s.writeErr
}
func (s *routeService) SaveGitHubQuery(_ context.Context, _ string, _ int64, _, _, _ string, request dto.SaveQueryRequest, _ facade4datatug.GitHubQueryRepository) (*dto.SaveQueryResponse, error) {
	s.saveCalls++
	s.received = request
	if s.saveErr != nil {
		return nil, s.saveErr
	}
	return &dto.SaveQueryResponse{Query: request.Query, Revision: "revision", BranchHead: "cccccccccccccccccccccccccccccccccccccccc"}, nil
}
func (s *routeService) CreateGitHubProject(_ context.Context, command facade4datatug.GitHubProjectCreateCommand, _ facade4datatug.GitHubCreateRepository) (facade4datatug.GitHubProjectCreateResult, error) {
	s.createCalls++
	s.createCommand = command
	if s.errorCreate != nil {
		return facade4datatug.GitHubProjectCreateResult{}, s.errorCreate
	}
	return facade4datatug.GitHubProjectCreateResult{ID: "repo@owner@datatug", Project: "repo@owner@datatug", Storage: "github.com"}, nil
}

type routeCreateRepo struct{}

func (*routeCreateRepo) Scope() facade4datatug.GitHubCreateRepositoryScope {
	return facade4datatug.GitHubCreateRepositoryScope{ActorID: "firebase-actor", RepositoryID: 91, Owner: "owner", Name: "repo", Permission: "write"}
}
func (*routeCreateRepo) CurrentHead(context.Context, string) (string, error)     { return routeHead, nil }
func (*routeCreateRepo) EnsureFolderEmpty(context.Context, string, string) error { return nil }
func (*routeCreateRepo) CreateFilesCommit(context.Context, string, string, string, map[string][]byte) (string, error) {
	return "", nil
}
func (*routeCreateRepo) FindCommitByMarker(context.Context, string, string, string, map[string][]byte) (string, error) {
	return "", nil
}

type routeQueryRepository struct{}

func (*routeQueryRepository) Scope() facade4datatug.GitHubCreateRepositoryScope {
	return facade4datatug.GitHubCreateRepositoryScope{ActorID: "firebase-actor", RepositoryID: 91, Owner: "owner", Name: "repo", Permission: "write"}
}
func (*routeQueryRepository) PrepareQuerySave(context.Context, string, string, string, dto.SaveQueryRequest) (*facade4datatug.GitHubQuerySavePlan, error) {
	return nil, nil
}
func (*routeQueryRepository) CurrentHead(context.Context, string) (string, error) { return "", nil }
func (*routeQueryRepository) CreateQueryCommit(context.Context, string, string, string, *facade4datatug.GitHubQuerySavePlan) (string, error) {
	return "", nil
}
func (*routeQueryRepository) FindQueryCommit(context.Context, string, string, string, *facade4datatug.GitHubQuerySavePlan) (string, error) {
	return "", nil
}

func routeOptions(t *testing.T, permission githubauth4datatug.RepositoryOperation) (GitHubProjectRouteOptions, *routeProvider, *routeService) {
	t.Helper()
	repo := newRouteRepo(t, permission)
	provider := &routeProvider{repo: repo}
	service := &routeService{}
	return GitHubProjectRouteOptions{providerOverride: provider, serviceOverride: service, queryRepositoryOverride: func(context.Context, string, int64, string, string) (facade4datatug.GitHubQueryRepository, error) {
		return &routeQueryRepository{}, nil
	}}, provider, service
}
func stubRoutePrincipal(t *testing.T) {
	t.Helper()
	old := verifyAuthenticatedRequest
	t.Cleanup(func() { verifyAuthenticatedRequest = old })
	verifyAuthenticatedRequest = func(_ http.ResponseWriter, r *http.Request, _ verify.RequestOptions) (facade.ContextWithUser, error) {
		return githubTestUserContext(r), nil
	}
}

func TestHostedGitHubProjectReadRoutesPinOneHeadAndPreserveRichQuery(t *testing.T) {
	stubRoutePrincipal(t)
	options, provider, service := routeOptions(t, githubauth4datatug.RepositoryRead)
	for _, test := range []struct {
		path     string
		handler  http.HandlerFunc
		contains string
	}{
		{"projects/project_summary", httpGetGitHubProjectSummary(options), `"title":"Owned project"`},
		{"queries/all_queries", httpGetGitHubAllQueries(options), `"recordsets"`},
		{"queries/query_revision", httpGetGitHubQueryRevision(options), `"saveSupported":false`},
	} {
		url := "/v0/datatug/" + test.path + "?storage=github.com&project=repo@owner@datatug&branch=work"
		if strings.HasSuffix(test.path, "query_revision") {
			url += "&id=demodb/chinook-top-customer-spend"
		}
		w := httptest.NewRecorder()
		test.handler(w, httptest.NewRequest(http.MethodGet, url, nil))
		if w.Code != http.StatusOK || w.Header().Get("X-Datatug-Branch-Head") != routeHead || !strings.Contains(w.Body.String(), test.contains) {
			t.Fatalf("%s status=%d header=%v body=%s", test.path, w.Code, w.Header(), w.Body.String())
		}
	}
	if provider.repo.refCalls != 3 || provider.authorized != 3 || service.readCalls != 3 {
		t.Fatalf("reads refs=%d auth=%d links=%d", provider.repo.refCalls, provider.authorized, service.readCalls)
	}
}

func TestHostedGitHubBranchAndCapabilityRoutesReflectCurrentAuthority(t *testing.T) {
	stubRoutePrincipal(t)
	options, _, service := routeOptions(t, githubauth4datatug.RepositoryWrite)
	for _, test := range []struct {
		handler        http.HandlerFunc
		path, contains string
	}{
		{httpGetGitHubProjectBranches(options), "branches", `"defaultBranch":"main"`},
		{httpGetGitHubProjectCapabilities(options), "capabilities", `"querySave":true`},
	} {
		w := httptest.NewRecorder()
		test.handler(w, httptest.NewRequest(http.MethodGet, "/v0/datatug/projects/"+test.path+"?storage=github.com&project=repo@owner@datatug", nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), test.contains) {
			t.Fatalf("%s %d %s", test.path, w.Code, w.Body.String())
		}
	}
	if service.writeCalls != 1 {
		t.Fatalf("write checks=%d", service.writeCalls)
	}
	service.writeErr = facade4datatug.ErrSharedProjectUnauthorized
	w := httptest.NewRecorder()
	httpGetGitHubProjectCapabilities(options)(w, httptest.NewRequest(http.MethodGet, "/?storage=github.com&project=repo@owner@datatug", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"querySave":false`) {
		t.Fatalf("ended write capability %d %s", w.Code, w.Body.String())
	}
}

func TestHostedGitHubSaveRouteBoundsUnknownLengthAndAvoidsSharedBodyDecoder(t *testing.T) {
	stubRoutePrincipal(t)
	oldDecode := verifyAuthenticatedRequestAndDecodeBody
	t.Cleanup(func() { verifyAuthenticatedRequestAndDecodeBody = oldDecode })
	verifyAuthenticatedRequestAndDecodeBody = func(http.ResponseWriter, *http.Request, verify.RequestOptions, facade.Request) (facade.ContextWithUser, error) {
		t.Fatal("shared body decoder called")
		return nil, errors.New("forbidden")
	}
	options, _, service := routeOptions(t, githubauth4datatug.RepositoryWrite)
	valid := `{"storage":"github.com","project":"repo@owner@datatug","branch":"work","expectedBranchHead":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","operationId":"save-1","ifNoneMatch":true,"query":{"folderPath":"~","id":"customers","title":"Customers","type":"DTQL","text":"SELECT CustomerId FROM chinook.Customer"}}`
	w := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v0/datatug/queries/save_query", strings.NewReader(valid))
	request.ContentLength = -1
	httpPostGitHubSaveQuery(options)(w, request)
	if w.Code != http.StatusOK || service.saveCalls != 1 || service.received.Query.Text != "SELECT CustomerId FROM chinook.Customer" {
		t.Fatalf("save %d %s calls=%d", w.Code, w.Body.String(), service.saveCalls)
	}
	for _, test := range []struct {
		body string
		want int
	}{{"{", http.StatusBadRequest}, {strings.Repeat("x", maxSaveGitHubQueryRequestBytes+2), http.StatusRequestEntityTooLarge}} {
		w = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodPost, "/v0/datatug/queries/save_query", strings.NewReader(test.body))
		request.ContentLength = -1
		httpPostGitHubSaveQuery(options)(w, request)
		if w.Code != test.want || service.saveCalls != 1 || strings.Contains(w.Body.String(), "SELECT") {
			t.Fatalf("bounded decode status=%d body=%s calls=%d", w.Code, w.Body.String(), service.saveCalls)
		}
	}
}

func TestHostedGitHubCreateRoutePassesVerifiedActorAndSelectedHead(t *testing.T) {
	stubRoutePrincipal(t)
	oldDecode := verifyAuthenticatedRequestAndDecodeBody
	t.Cleanup(func() { verifyAuthenticatedRequestAndDecodeBody = oldDecode })
	verifyAuthenticatedRequestAndDecodeBody = func(w http.ResponseWriter, r *http.Request, _ verify.RequestOptions, request facade.Request) (facade.ContextWithUser, error) {
		if err := json.NewDecoder(r.Body).Decode(request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return nil, err
		}
		if err := request.Validate(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return nil, err
		}
		return githubTestUserContext(r), nil
	}
	options, _, service := routeOptions(t, githubauth4datatug.RepositoryWrite)
	options.AuthorizeRepository = func(_ context.Context, uid string, id int64, owner, name string) (facade4datatug.GitHubCreateRepository, error) {
		if uid != "firebase-actor" || id != 91 || owner != "owner" || name != "repo" {
			return nil, errors.New("wrong authority")
		}
		return &routeCreateRepo{}, nil
	}
	body := `{"title":"Owned","spaceID":"space","operationId":"create-1","github":{"repositoryID":91,"owner":"owner","name":"repo","folder":"datatug","branch":"work","expectedBranchHead":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"template":{"id":"demo-project-1","commit":"51716f3a4d682d5cb7ef70a7fd37f42e5418fd3d"}}`
	w := httptest.NewRecorder()
	httpPostCreateGitHubProject(options)(w, httptest.NewRequest(http.MethodPost, "/v0/datatug/projects/create_project?store=github.com", strings.NewReader(body)))
	if w.Code != http.StatusCreated || service.createCalls != 1 || service.createCommand.ActorID != "firebase-actor" || service.createCommand.Source.ExpectedHead != routeHead {
		t.Fatalf("create status=%d body=%s calls=%d command=%+v", w.Code, w.Body.String(), service.createCalls, service.createCommand)
	}
	service.errorCreate = facade4datatug.ErrGitHubProjectConflict
	w = httptest.NewRecorder()
	httpPostCreateGitHubProject(options)(w, httptest.NewRequest(http.MethodPost, "/v0/datatug/projects/create_project?store=github.com", strings.NewReader(body)))
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), "Owned") {
		t.Fatalf("conflict status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHostedGitHubReadsRefuseRevokedOrUnlinkedActor(t *testing.T) {
	stubRoutePrincipal(t)
	options, provider, service := routeOptions(t, githubauth4datatug.RepositoryRead)
	url := "/v0/datatug/projects/project_summary?storage=github.com&project=repo@owner@datatug&branch=work"
	for _, test := range []struct {
		name   string
		before func()
		want   int
	}{
		{"repository access revoked", func() { provider.authErr = githubauth4datatug.ErrGitHubPermissionDenied }, http.StatusForbidden},
		{"contact unlinked", func() { provider.authErr = nil; service.readErr = facade4datatug.ErrSharedProjectUnauthorized }, http.StatusForbidden},
		{"grant expired", func() { service.readErr = nil; provider.listErr = githubauth4datatug.ErrReauthorizationRequired }, http.StatusConflict},
	} {
		test.before()
		w := httptest.NewRecorder()
		httpGetGitHubProjectSummary(options)(w, httptest.NewRequest(http.MethodGet, url, nil))
		if w.Code != test.want || strings.Contains(w.Body.String(), "Owned project") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d body=%s", test.name, w.Code, w.Body.String())
		}
	}
	provider.listErr = nil
	w := httptest.NewRecorder()
	httpGetGitHubProjectSummary(options)(w, httptest.NewRequest(http.MethodGet, "/?storage=github.com&project=repo@owner@datatug", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing selected branch status=%d", w.Code)
	}
}

func TestHostedGitHubProspectiveBranchesRequireRealRepositoryAndInitialization(t *testing.T) {
	stubRoutePrincipal(t)
	options, provider, service := routeOptions(t, githubauth4datatug.RepositoryRead)
	service.readErr = facade4datatug.ErrGitHubProjectNotRegistered
	w := httptest.NewRecorder()
	httpGetGitHubProjectBranches(options)(w, httptest.NewRequest(http.MethodGet, "/?storage=github.com&project=repo@owner@new-folder", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"branches"`) {
		t.Fatalf("prospective branches %d %s", w.Code, w.Body.String())
	}
	provider.repo.scope.Repository.DefaultBranch = ""
	w = httptest.NewRecorder()
	httpGetGitHubProjectBranches(options)(w, httptest.NewRequest(http.MethodGet, "/?storage=github.com&project=repo@owner@new-folder", nil))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "initialization_required") {
		t.Fatalf("empty repo %d %s", w.Code, w.Body.String())
	}
	provider.repo.scope.Repository.DefaultBranch = "main"
	provider.repo.branchErr = errors.New("provider unavailable")
	w = httptest.NewRecorder()
	httpGetGitHubProjectBranches(options)(w, httptest.NewRequest(http.MethodGet, "/?storage=github.com&project=repo@owner@new-folder", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("branch error %d", w.Code)
	}
}

func TestHostedGitHubSaveMapsRevisionConflictWithoutLeakingQuery(t *testing.T) {
	stubRoutePrincipal(t)
	options, _, service := routeOptions(t, githubauth4datatug.RepositoryWrite)
	service.saveErr = dto.ErrQueryRevisionConflict
	body := `{"storage":"github.com","project":"repo@owner@datatug","branch":"work","expectedBranchHead":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","operationId":"save-1","ifNoneMatch":true,"query":{"folderPath":"~","id":"customers","title":"Customers","type":"DTQL","text":"SECRET_QUERY_BODY"}}`
	w := httptest.NewRecorder()
	httpPostGitHubSaveQuery(options)(w, httptest.NewRequest(http.MethodPost, "/v0/datatug/queries/save_query", strings.NewReader(body)))
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), "SECRET_QUERY_BODY") || service.saveCalls != 1 {
		t.Fatalf("conflict status=%d body=%s calls=%d", w.Code, w.Body.String(), service.saveCalls)
	}
}

func TestHostedGitHubRoutesFailClosedBeforePrivateProviderRead(t *testing.T) {
	stubRoutePrincipal(t)
	for _, tc := range []struct {
		name        string
		makeHandler func(GitHubProjectRouteOptions) http.HandlerFunc
		url         string
	}{
		{"summary", httpGetGitHubProjectSummary, "/?storage=github.com&project=repo@owner@datatug&branch=work"},
		{"queries", httpGetGitHubAllQueries, "/?storage=github.com&project=repo@owner@datatug&branch=work"},
		{"revision", httpGetGitHubQueryRevision, "/?storage=github.com&project=repo@owner@datatug&branch=work&id=demodb/chinook-top-customer-spend"},
		{"branches", httpGetGitHubProjectBranches, "/?storage=github.com&project=repo@owner@datatug"},
		{"capabilities", httpGetGitHubProjectCapabilities, "/?storage=github.com&project=repo@owner@datatug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options, provider, service := routeOptions(t, githubauth4datatug.RepositoryRead)
			w := httptest.NewRecorder()
			tc.makeHandler(GitHubProjectRouteOptions{})(w, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("unconfigured=%d", w.Code)
			}
			w = httptest.NewRecorder()
			tc.makeHandler(options)(w, httptest.NewRequest(http.MethodGet, strings.Replace(tc.url, "storage=github.com", "storage=other", 1), nil))
			if w.Code != http.StatusBadRequest || provider.listed != 0 || service.readCalls != 0 {
				t.Fatalf("wrong store=%d provider=%d service=%d", w.Code, provider.listed, service.readCalls)
			}
			w = httptest.NewRecorder()
			tc.makeHandler(options)(w, httptest.NewRequest(http.MethodGet, strings.Replace(tc.url, "repo@owner@datatug", "invalid", 1), nil))
			if w.Code != http.StatusBadRequest || provider.listed != 0 {
				t.Fatalf("wrong project=%d provider=%d", w.Code, provider.listed)
			}
			provider.listErr = githubauth4datatug.ErrReauthorizationRequired
			w = httptest.NewRecorder()
			tc.makeHandler(options)(w, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if w.Code != http.StatusConflict || provider.authorized != 0 || service.readCalls != 0 {
				t.Fatalf("expired grant=%d authorizations=%d linked=%d", w.Code, provider.authorized, service.readCalls)
			}
		})
	}
}

func TestHostedGitHubSaveRejectsUnknownFieldsAndLostAuthorityWithoutMutation(t *testing.T) {
	stubRoutePrincipal(t)
	options, provider, service := routeOptions(t, githubauth4datatug.RepositoryWrite)
	valid := `{"storage":"github.com","project":"repo@owner@datatug","branch":"work","expectedBranchHead":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","operationId":"save-1","ifNoneMatch":true,"query":{"folderPath":"~","id":"customers","title":"Customers","type":"DTQL","text":"SELECT 1"}}`
	for _, tc := range []struct {
		name, body string
		before     func()
		want       int
	}{
		{"unknown nested metadata", strings.Replace(valid, `"text":"SELECT 1"`, `"text":"SELECT 1","secretOtherMetadata":"discard-me"`, 1), func() {}, http.StatusBadRequest},
		{"changed store", strings.Replace(valid, `"storage":"github.com"`, `"storage":"other"`, 1), func() {}, http.StatusBadRequest},
		{"unlisted repository", valid, func() {
			provider.listedRepos = []githubauth4datatug.GitHubRepository{{ID: 92, Owner: "other", Name: "repo"}}
		}, http.StatusForbidden},
		{"revoked write token", valid, func() {
			provider.listedRepos = nil
			options.queryRepositoryOverride = func(context.Context, string, int64, string, string) (facade4datatug.GitHubQueryRepository, error) {
				return nil, githubauth4datatug.ErrGitHubPermissionDenied
			}
		}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.before()
			w := httptest.NewRecorder()
			httpPostGitHubSaveQuery(options)(w, httptest.NewRequest(http.MethodPost, "/v0/datatug/queries/save_query", strings.NewReader(tc.body)))
			if w.Code != tc.want || service.saveCalls != 0 || strings.Contains(w.Body.String(), "discard-me") {
				t.Fatalf("status=%d calls=%d response=%s", w.Code, service.saveCalls, w.Body.String())
			}
		})
	}
}
