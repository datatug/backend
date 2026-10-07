package api4datatug

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/githubauth4datatug"
	"github.com/datatug/backend/githubstore4datatug"
	"github.com/datatug/backend/models4datatug"
	"github.com/datatug/backend/template4datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
	"github.com/strongo/validation"
)

// GitHubProjectRouteOptions are injected by the host after configuring the
// dedicated DataTug GitHub App and paid Space service. A missing port is 503.
// AuthorizeRepository must freshly bind the Firebase UID to the current
// GitHub user, selected installation, immutable repository and write grant.
type GitHubProjectRouteOptions struct {
	Service             *facade4datatug.SharedProjectService
	Provider            *githubauth4datatug.Provider
	AuthorizeRepository func(context.Context, string, int64, string, string) (facade4datatug.GitHubCreateRepository, error)
	// These package-private ports make the transport testable without exposing
	// a production route option that could bypass the dedicated GitHub App.
	serviceOverride         githubProjectService
	providerOverride        githubProjectProvider
	queryRepositoryOverride func(context.Context, string, int64, string, string) (facade4datatug.GitHubQueryRepository, error)
}

type githubProjectService interface {
	CreateGitHubProject(context.Context, facade4datatug.GitHubProjectCreateCommand, facade4datatug.GitHubCreateRepository) (facade4datatug.GitHubProjectCreateResult, error)
	ResolveGitHubProject(context.Context, string, int64, string, string, string) (facade4datatug.GitHubProjectAccess, error)
	AuthorizeGitHubProjectWrite(context.Context, string, int64, string, string, string) (facade4datatug.GitHubProjectAccess, error)
	SaveGitHubQuery(context.Context, string, int64, string, string, string, dto.SaveQueryRequest, facade4datatug.GitHubQueryRepository) (*dto.SaveQueryResponse, error)
}

type githubProjectReadRepository interface {
	Scope() githubauth4datatug.RepositoryScope
	ListBranches(context.Context) ([]githubauth4datatug.GitHubBranch, error)
	GetRef(context.Context, string) (githubauth4datatug.GitHubRef, error)
	GetCommit(context.Context, string) (githubauth4datatug.GitHubCommit, error)
	GetTree(context.Context, string) ([]githubauth4datatug.GitHubTreeEntry, error)
	GetBlob(context.Context, string) ([]byte, error)
}
type githubProjectProvider interface {
	ListRepositories(context.Context, string) ([]githubauth4datatug.GitHubRepository, error)
	AuthorizeReadRepository(context.Context, string, githubauth4datatug.RepositoryRef) (githubProjectReadRepository, error)
}
type githubProjectProviderAdapter struct{ provider *githubauth4datatug.Provider }

func (a githubProjectProviderAdapter) ListRepositories(ctx context.Context, uid string) ([]githubauth4datatug.GitHubRepository, error) {
	return a.provider.ListRepositories(ctx, uid)
}
func (a githubProjectProviderAdapter) AuthorizeReadRepository(ctx context.Context, uid string, ref githubauth4datatug.RepositoryRef) (githubProjectReadRepository, error) {
	return a.provider.AuthorizeRepository(ctx, uid, ref, githubauth4datatug.RepositoryRead)
}

func (o GitHubProjectRouteOptions) service() githubProjectService {
	if o.serviceOverride != nil {
		return o.serviceOverride
	}
	if o.Service == nil {
		return nil
	}
	return o.Service
}
func (o GitHubProjectRouteOptions) provider() githubProjectProvider {
	if o.providerOverride != nil {
		return o.providerOverride
	}
	if o.Provider != nil {
		return githubProjectProviderAdapter{provider: o.Provider}
	}
	return nil
}
func (o GitHubProjectRouteOptions) queryRepository(ctx context.Context, uid string, id int64, owner, name string) (facade4datatug.GitHubQueryRepository, error) {
	if o.queryRepositoryOverride != nil {
		return o.queryRepositoryOverride(ctx, uid, id, owner, name)
	}
	return githubstore4datatug.AuthorizeQueryRepository(ctx, o.Provider, uid, id, owner, name)
}

func httpPostCreateProjectByStore(ids facade4datatug.IDGenerator, github GitHubProjectRouteOptions) http.HandlerFunc {
	cloudHandler := httpPostCreateProject(ids)
	githubHandler := httpPostCreateGitHubProject(github)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("store") == models4datatug.GithubStoreID {
			githubHandler(w, r)
			return
		}
		cloudHandler(w, r)
	}
}

type CreateGitHubProjectRequest struct {
	Title       string `json:"title"`
	SpaceID     string `json:"spaceID"`
	OperationID string `json:"operationId"`
	GitHub      struct {
		RepositoryID       int64  `json:"repositoryID"`
		Owner              string `json:"owner"`
		Name               string `json:"name"`
		Folder             string `json:"folder"`
		Branch             string `json:"branch"`
		ExpectedBranchHead string `json:"expectedBranchHead"`
	} `json:"github"`
	Template struct {
		ID     string `json:"id"`
		Commit string `json:"commit"`
	} `json:"template"`
}

// UnmarshalJSON refuses unknown fields recursively. The common mutation API
// must never silently discard richer client metadata before computing a
// request digest or writing to GitHub.
func (v *CreateGitHubProjectRequest) UnmarshalJSON(data []byte) error {
	type raw CreateGitHubProjectRequest
	var parsed raw
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return validation.NewErrBadRequestFieldValue("body", "one JSON object is required")
	}
	*v = CreateGitHubProjectRequest(parsed)
	return nil
}

func (v CreateGitHubProjectRequest) Validate() error {
	if models4datatug.ValidateSharedProjectTitle(v.Title) != nil || models4datatug.ValidateSharedProjectIdentifier(v.SpaceID) != nil || models4datatug.ValidateSharedProjectIdentifier(v.OperationID) != nil || v.GitHub.RepositoryID < 1 || v.GitHub.Owner == "" || v.GitHub.Name == "" || template4datatug.ValidateFolder(v.GitHub.Folder) != nil || v.GitHub.Branch == "" || v.GitHub.ExpectedBranchHead == "" || v.Template.ID != template4datatug.DemoProjectID || v.Template.Commit != template4datatug.DemoProjectCommit {
		return validation.NewErrBadRequestFieldValue("body", "invalid GitHub project create request")
	}
	return nil
}

func httpPostCreateGitHubProject(options GitHubProjectRouteOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if options.service() == nil || options.AuthorizeRepository == nil {
			sharedProjectError(w, http.StatusServiceUnavailable, "github_unavailable")
			return
		}
		if r.URL.Query().Get("store") != models4datatug.GithubStoreID {
			sharedProjectError(w, http.StatusBadRequest, "invalid_store")
			return
		}
		var request CreateGitHubProjectRequest
		ctx, err := verifyAuthenticatedRequestAndDecodeBody(w, r, verify.DefaultJsonWithAuthRequired, &request)
		if err != nil {
			return
		}
		if ctx == nil || ctx.User() == nil || ctx.User().GetUserID() == "" {
			sharedProjectError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		actorID := ctx.User().GetUserID()
		repo, err := options.AuthorizeRepository(ctx, actorID, request.GitHub.RepositoryID, request.GitHub.Owner, request.GitHub.Name)
		if err != nil {
			status, code := githubAuthorizationStatus(err)
			sharedProjectError(w, status, code)
			return
		}
		command := facade4datatug.GitHubProjectCreateCommand{
			ActorID: actorID, SpaceID: request.SpaceID, OperationID: request.OperationID, Title: request.Title,
			Source: models4datatug.GitHubCreateSource{
				Binding: models4datatug.GitHubProjectBinding{
					RepositoryID: request.GitHub.RepositoryID, Owner: request.GitHub.Owner, Name: request.GitHub.Name,
					Folder: request.GitHub.Folder, Branch: request.GitHub.Branch,
				},
				ExpectedHead: request.GitHub.ExpectedBranchHead, TemplateID: request.Template.ID, TemplateCommit: request.Template.Commit,
			},
		}
		response, err := options.service().CreateGitHubProject(ctx, command, repo)
		if err != nil {
			status, code := http.StatusServiceUnavailable, "github_unavailable"
			switch {
			case errors.Is(err, facade4datatug.ErrGitHubProjectInvalid):
				status, code = http.StatusBadRequest, "invalid"
			case errors.Is(err, facade4datatug.ErrSharedProjectUnauthorized):
				status, code = http.StatusForbidden, "unauthorized"
			case errors.Is(err, facade4datatug.ErrGitHubProjectConflict), errors.Is(err, facade4datatug.ErrSharedProjectConflict), errors.Is(err, facade4datatug.ErrProtectedProjectQuota):
				status, code = http.StatusConflict, "conflict"
			case errors.Is(err, facade4datatug.ErrGitHubOutcomeUncertain):
				status, code = http.StatusConflict, "outcome_uncertain"
			}
			sharedProjectError(w, status, code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(response)
	}
}
