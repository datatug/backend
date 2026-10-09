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
	"github.com/datatug/backend/githubstore4datatug"
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
	repo                         *routeRepo
	listErr, resolveErr, authErr error
	listedRepos                  []githubauth4datatug.GitHubRepository
	listed                       int
	resolved                     int
	authorized                   int
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
func (p *routeProvider) ResolveRepositoryByName(_ context.Context, uid, owner, name string) (githubauth4datatug.GitHubRepository, error) {
	p.resolved++
	if uid != "firebase-actor" {
		return githubauth4datatug.GitHubRepository{}, errors.New("wrong actor")
	}
	if p.resolveErr != nil {
		return githubauth4datatug.GitHubRepository{}, p.resolveErr
	}
	if p.listedRepos != nil {
		for _, repo := range p.listedRepos {
			if strings.EqualFold(repo.Owner, owner) && strings.EqualFold(repo.Name, name) {
				return repo, nil
			}
		}
		return githubauth4datatug.GitHubRepository{}, githubauth4datatug.ErrGitHubRepositoryDenied
	}
	if !strings.EqualFold(owner, "owner") || !strings.EqualFold(name, "repo") {
		return githubauth4datatug.GitHubRepository{}, githubauth4datatug.ErrGitHubRepositoryDenied
	}
	return githubauth4datatug.GitHubRepository{ID: 91, Owner: owner, Name: name, DefaultBranch: p.repo.scope.Repository.DefaultBranch}, nil
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
	aiEligibility                                 facade4datatug.ProjectAIEligibility
	aiEligibilityErr                              error
	aiEligibilityCalls                            int
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
func (s *routeService) ReadGitHubProjectAIEligibility(_ context.Context, uid string, id int64, owner, name, folder string) (facade4datatug.ProjectAIEligibility, error) {
	s.aiEligibilityCalls++
	if uid != "firebase-actor" || id != 91 || owner != "owner" || name != "repo" || folder != "datatug" {
		return facade4datatug.ProjectAIEligibility{}, errors.New("wrong project")
	}
	if s.aiEligibilityErr != nil {
		return facade4datatug.ProjectAIEligibility{}, s.aiEligibilityErr
	}
	if s.aiEligibility == (facade4datatug.ProjectAIEligibility{}) {
		return facade4datatug.ProjectAIEligibility{AIAllowed: true}, nil
	}
	return s.aiEligibility, nil
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
		{"projects/connection_catalog", httpGetGitHubConnectionCatalog(options), `"id":"chinook-sqlite"`},
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
	if provider.listed != 0 || provider.resolved != 4 || provider.repo.refCalls != 4 || provider.authorized != 4 || service.readCalls != 4 {
		t.Fatalf("reads list=%d resolve=%d refs=%d auth=%d links=%d", provider.listed, provider.resolved, provider.repo.refCalls, provider.authorized, service.readCalls)
	}
}

func TestHostedGitHubBranchAndCapabilityRoutesReflectCurrentAuthority(t *testing.T) {
	stubRoutePrincipal(t)
	options, provider, service := routeOptions(t, githubauth4datatug.RepositoryWrite)
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
	if provider.listed != 0 || provider.resolved != 2 || service.writeCalls != 1 {
		t.Fatalf("repository discovery list=%d resolve=%d write checks=%d", provider.listed, provider.resolved, service.writeCalls)
	}
	service.writeErr = facade4datatug.ErrSharedProjectUnauthorized
	w := httptest.NewRecorder()
	httpGetGitHubProjectCapabilities(options)(w, httptest.NewRequest(http.MethodGet, "/?storage=github.com&project=repo@owner@datatug", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"querySave":false`) {
		t.Fatalf("ended write capability %d %s", w.Code, w.Body.String())
	}
}

func TestHostedGitHubCapabilitiesIncludeCurrentProjectAIEligibility(t *testing.T) {
	stubRoutePrincipal(t)
	options, provider, service := routeOptions(t, githubauth4datatug.RepositoryRead)
	service.aiEligibility = facade4datatug.ProjectAIEligibility{AIAllowed: false, Reason: "plan_ended"}
	w := httptest.NewRecorder()
	httpGetGitHubProjectCapabilities(options)(w, httptest.NewRequest(http.MethodGet, "/v0/datatug/projects/capabilities?storage=github.com&project=repo@owner@datatug", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"queryRead":true`) || !strings.Contains(w.Body.String(), `"projectAI":{"aiAllowed":false,"reason":"plan_ended"}`) {
		t.Fatalf("capabilities %d %s", w.Code, w.Body.String())
	}
	if provider.listed != 0 || provider.resolved != 1 || provider.authorized != 1 || service.readCalls != 1 || service.aiEligibilityCalls != 1 {
		t.Fatalf("read authorities list=%d resolve=%d github=%d linkage=%d entitlement=%d", provider.listed, provider.resolved, provider.authorized, service.readCalls, service.aiEligibilityCalls)
	}

	service.aiEligibilityErr = facade4datatug.ErrProjectAIEligibilityUnavailable
	w = httptest.NewRecorder()
	httpGetGitHubProjectCapabilities(options)(w, httptest.NewRequest(http.MethodGet, "/v0/datatug/projects/capabilities?storage=github.com&project=repo@owner@datatug", nil))
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), `"plan_ended"`) {
		t.Fatalf("unproved eligibility was reported as an expired plan: %d %s", w.Code, w.Body.String())
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
	options, provider, service := routeOptions(t, githubauth4datatug.RepositoryWrite)
	valid := `{"storage":"github.com","project":"repo@owner@datatug","branch":"work","expectedBranchHead":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","operationId":"save-1","ifNoneMatch":true,"query":{"folderPath":"~","id":"customers","title":"Customers","type":"DTQL","text":"SELECT CustomerId FROM chinook.Customer"}}`
	w := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v0/datatug/queries/save_query", strings.NewReader(valid))
	request.ContentLength = -1
	httpPostGitHubSaveQuery(options)(w, request)
	if w.Code != http.StatusOK || service.saveCalls != 1 || service.received.Query.Text != "SELECT CustomerId FROM chinook.Customer" || provider.listed != 0 || provider.resolved != 1 {
		t.Fatalf("save %d %s calls=%d list=%d resolve=%d", w.Code, w.Body.String(), service.saveCalls, provider.listed, provider.resolved)
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
	body := `{"title":"Owned","spaceID":"space","operationId":"create-1","github":{"repositoryID":91,"owner":"owner","name":"repo","folder":"datatug","branch":"work","expectedBranchHead":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"template":{"id":"demo-project-1","commit":"436350d41371103be11144ffa346c605f85e1342"}}`
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
		{"grant expired", func() { service.readErr = nil; provider.resolveErr = githubauth4datatug.ErrReauthorizationRequired }, http.StatusConflict},
	} {
		test.before()
		w := httptest.NewRecorder()
		httpGetGitHubProjectSummary(options)(w, httptest.NewRequest(http.MethodGet, url, nil))
		if w.Code != test.want || strings.Contains(w.Body.String(), "Owned project") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d body=%s", test.name, w.Code, w.Body.String())
		}
	}
	provider.resolveErr = nil
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
		{"catalog", httpGetGitHubConnectionCatalog, "/?storage=github.com&project=repo@owner@datatug&branch=work"},
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
			if w.Code != http.StatusBadRequest || provider.listed != 0 || provider.resolved != 0 || service.readCalls != 0 {
				t.Fatalf("wrong store=%d provider=%d service=%d", w.Code, provider.listed, service.readCalls)
			}
			w = httptest.NewRecorder()
			tc.makeHandler(options)(w, httptest.NewRequest(http.MethodGet, strings.Replace(tc.url, "repo@owner@datatug", "invalid", 1), nil))
			if w.Code != http.StatusBadRequest || provider.listed != 0 || provider.resolved != 0 {
				t.Fatalf("wrong project=%d provider=%d", w.Code, provider.listed)
			}
			provider.resolveErr = githubauth4datatug.ErrReauthorizationRequired
			w = httptest.NewRecorder()
			tc.makeHandler(options)(w, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if w.Code != http.StatusConflict || provider.authorized != 0 || service.readCalls != 0 {
				t.Fatalf("expired grant=%d authorizations=%d linked=%d", w.Code, provider.authorized, service.readCalls)
			}
		})
	}
}

func TestHostedGitHubRoutesRefuseConfiguredProviderWithoutPaidService(t *testing.T) {
	options := GitHubProjectRouteOptions{Provider: &githubauth4datatug.Provider{}, AuthorizeRepository: func(context.Context, string, int64, string, string) (facade4datatug.GitHubCreateRepository, error) {
		t.Fatal("provider called without paid service")
		return nil, nil
	}}
	for _, tc := range []struct {
		name        string
		handler     http.HandlerFunc
		method, url string
	}{
		{"summary", httpGetGitHubProjectSummary(options), http.MethodGet, "/?storage=github.com&project=repo@owner@datatug&branch=work"},
		{"catalog", httpGetGitHubConnectionCatalog(options), http.MethodGet, "/?storage=github.com&project=repo@owner@datatug&branch=work"},
		{"branches", httpGetGitHubProjectBranches(options), http.MethodGet, "/?storage=github.com&project=repo@owner@datatug"},
		{"capabilities", httpGetGitHubProjectCapabilities(options), http.MethodGet, "/?storage=github.com&project=repo@owner@datatug"},
		{"query listing", httpGetGitHubAllQueries(options), http.MethodGet, "/?storage=github.com&project=repo@owner@datatug&branch=work"},
		{"query save", httpPostGitHubSaveQuery(options), http.MethodPost, "/v0/datatug/queries/save_query"},
		{"project create", httpPostCreateGitHubProject(options), http.MethodPost, "/v0/datatug/projects/create_project?store=github.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.handler(w, httptest.NewRequest(tc.method, tc.url, nil))
			if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "github_unavailable") {
				t.Fatalf("missing paid service status=%d body=%s", w.Code, w.Body.String())
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
		{"unsupported federation metadata", strings.Replace(valid, `"text":"SELECT 1"`, `"text":"SELECT 1","federation":{"ovdbBaseUrl":"https://demodb.dev/ovdb","expectedServerIdentity":"must-not-discard"}`, 1), func() {}, http.StatusBadRequest},
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

func TestHostedGitHubCreateRejectsUnknownInputAndUnavailableApp(t *testing.T) {
	valid := `{"title":"Owned","spaceID":"space","operationId":"create-1","github":{"repositoryID":91,"owner":"owner","name":"repo","folder":"datatug","branch":"work","expectedBranchHead":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"template":{"id":"demo-project-1","commit":"436350d41371103be11144ffa346c605f85e1342"}}`
	for _, body := range []string{
		strings.Replace(valid, `"branch":"work"`, `"branch":"work","unhandledGrant":"x"`, 1),
		strings.Replace(valid, `"title":"Owned"`, `"title":"Owned","unknown":"x"`, 1),
		valid + `{}`,
	} {
		var request CreateGitHubProjectRequest
		if err := json.Unmarshal([]byte(body), &request); err == nil {
			t.Fatalf("unknown/extra create input accepted: %s", body)
		}
	}
	var request CreateGitHubProjectRequest
	if err := json.Unmarshal([]byte(valid), &request); err != nil || request.Validate() != nil {
		t.Fatalf("valid create input rejected: %v %+v", err, request)
	}
	request.GitHub.Folder = "../other"
	if request.Validate() == nil {
		t.Fatal("path traversal accepted")
	}
	w := httptest.NewRecorder()
	httpPostCreateGitHubProject(GitHubProjectRouteOptions{})(w, httptest.NewRequest(http.MethodPost, "/?store=github.com", strings.NewReader(valid)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured create=%d", w.Code)
	}
	options, _, _ := routeOptions(t, githubauth4datatug.RepositoryWrite)
	options.AuthorizeRepository = func(context.Context, string, int64, string, string) (facade4datatug.GitHubCreateRepository, error) {
		return &routeCreateRepo{}, nil
	}
	w = httptest.NewRecorder()
	httpPostCreateGitHubProject(options)(w, httptest.NewRequest(http.MethodPost, "/?store=other", strings.NewReader(valid)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid store create=%d", w.Code)
	}
}

func TestHostedGitHubCreateMapsProviderAndAdmissionFailures(t *testing.T) {
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
	body := `{"title":"Owned","spaceID":"space","operationId":"create-1","github":{"repositoryID":91,"owner":"owner","name":"repo","folder":"datatug","branch":"work","expectedBranchHead":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"template":{"id":"demo-project-1","commit":"436350d41371103be11144ffa346c605f85e1342"}}`
	for _, tc := range []struct {
		name               string
		authErr, createErr error
		want               int
	}{
		{"lost GitHub write", githubauth4datatug.ErrGitHubPermissionDenied, nil, http.StatusForbidden},
		{"paid admission lost", nil, facade4datatug.ErrSharedProjectUnauthorized, http.StatusForbidden},
		{"paid proof unproved", nil, facade4datatug.ErrPlanEffectUnproved, http.StatusServiceUnavailable},
		{"owner database unavailable", nil, errors.New("owner database unavailable"), http.StatusServiceUnavailable},
		{"quota filled", nil, facade4datatug.ErrProtectedProjectQuota, http.StatusConflict},
		{"commit uncertain", nil, facade4datatug.ErrGitHubOutcomeUncertain, http.StatusConflict},
		{"invalid create command", nil, facade4datatug.ErrGitHubProjectInvalid, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options, _, service := routeOptions(t, githubauth4datatug.RepositoryWrite)
			options.AuthorizeRepository = func(context.Context, string, int64, string, string) (facade4datatug.GitHubCreateRepository, error) {
				if tc.authErr != nil {
					return nil, tc.authErr
				}
				return &routeCreateRepo{}, nil
			}
			service.errorCreate = tc.createErr
			w := httptest.NewRecorder()
			httpPostCreateGitHubProject(options)(w, httptest.NewRequest(http.MethodPost, "/?store=github.com", strings.NewReader(body)))
			if w.Code != tc.want || strings.Contains(w.Body.String(), "Owned") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.authErr != nil && service.createCalls != 0 {
				t.Fatal("create called after permission loss")
			}
		})
	}
}

func TestHostedGitHubPrivateReadsRefuseWrongManifestAndMissingQueryBody(t *testing.T) {
	stubRoutePrincipal(t)
	for _, tc := range []struct {
		name, path string
		mutate     func(*routeRepo)
		want       int
	}{
		{"wrong registered identity", "/v0/datatug/projects/project_summary?storage=github.com&project=repo@owner@datatug&branch=work", func(r *routeRepo) {
			for i, entry := range r.tree {
				if entry.Path == "datatug/datatug-project.json" {
					content := []byte(`{"id":"foreign","title":"Foreign project","access":"protected"}`)
					oid := routeBlobOID(content)
					r.blobs[oid] = content
					r.tree[i].OID = oid
					r.tree[i].Size = int64(len(content))
					break
				}
			}
		}, http.StatusBadRequest},
		{"missing query body", "/v0/datatug/queries/query_revision?storage=github.com&project=repo@owner@datatug&branch=work&id=demodb/chinook-top-customer-spend", func(r *routeRepo) {
			for i, entry := range r.tree {
				if entry.Path == "datatug/queries/demodb/chinook-top-customer-spend.query.sql" {
					r.tree = append(r.tree[:i], r.tree[i+1:]...)
					break
				}
			}
		}, http.StatusNotFound},
		{"invalid query ID", "/v0/datatug/queries/query_revision?storage=github.com&project=repo@owner@datatug&branch=work&id=/absolute", func(*routeRepo) {}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options, provider, _ := routeOptions(t, githubauth4datatug.RepositoryRead)
			tc.mutate(provider.repo)
			var handler http.HandlerFunc
			if strings.Contains(tc.path, "project_summary") {
				handler = httpGetGitHubProjectSummary(options)
			} else {
				handler = httpGetGitHubQueryRevision(options)
			}
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if w.Code != tc.want || strings.Contains(w.Body.String(), "Foreign project") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestHostedGitHubSaveReturnsTypedRetryAndUnsupportedErrors(t *testing.T) {
	stubRoutePrincipal(t)
	body := `{"storage":"github.com","project":"repo@owner@datatug","branch":"work","expectedBranchHead":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","operationId":"save-1","ifNoneMatch":true,"query":{"folderPath":"~","id":"customers","title":"Customers","type":"DTQL","text":"SELECT 1"}}`
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"changed branch", dto.ErrBranchHeadConflict, http.StatusConflict, "conflict"},
		{"changed operation payload", dto.ErrOperationConflict, http.StatusConflict, "conflict"},
		{"unsupported rich query", githubstore4datatug.ErrUnsupportedExistingQuery, http.StatusBadRequest, "unsupported_query"},
		{"contact lost", facade4datatug.ErrSharedProjectUnauthorized, http.StatusForbidden, "project_denied"},
		{"commit outcome uncertain", facade4datatug.ErrGitHubQueryOutcomeUncertain, http.StatusConflict, "outcome_uncertain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options, _, service := routeOptions(t, githubauth4datatug.RepositoryWrite)
			service.saveErr = tc.err
			w := httptest.NewRecorder()
			httpPostGitHubSaveQuery(options)(w, httptest.NewRequest(http.MethodPost, "/v0/datatug/queries/save_query", strings.NewReader(body)))
			if w.Code != tc.status || !strings.Contains(w.Body.String(), `"`+tc.code+`"`) || strings.Contains(w.Body.String(), "SELECT 1") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestHostedGitHubSaveRejectsEmptyOversizedAndUnconfiguredRequestsBeforeProvider(t *testing.T) {
	stubRoutePrincipal(t)
	options, provider, service := routeOptions(t, githubauth4datatug.RepositoryWrite)
	for _, tc := range []struct {
		name, body string
		length     int64
		want       int
	}{
		{"empty body", "", 0, http.StatusBadRequest},
		{"known oversized body", "{}", maxSaveGitHubQueryRequestBytes + 1, http.StatusRequestEntityTooLarge},
		{"missing selected project", `{"storage":"github.com","project":"bad","branch":"work","expectedBranchHead":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","operationId":"save-1","ifNoneMatch":true,"query":{"folderPath":"~","id":"customers","title":"Customers","type":"DTQL","text":"SELECT 1"}}`, -1, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v0/datatug/queries/save_query", strings.NewReader(tc.body))
			r.ContentLength = tc.length
			w := httptest.NewRecorder()
			httpPostGitHubSaveQuery(options)(w, r)
			if w.Code != tc.want || provider.listed != 0 || service.saveCalls != 0 {
				t.Fatalf("untrusted request reached provider status=%d list=%d save=%d", w.Code, provider.listed, service.saveCalls)
			}
		})
	}
	w := httptest.NewRecorder()
	httpPostGitHubSaveQuery(GitHubProjectRouteOptions{})(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured save=%d", w.Code)
	}
}
