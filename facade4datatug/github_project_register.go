package facade4datatug

import (
	"context"
	"errors"

	"github.com/dal-go/dalgo/dal"

	"github.com/datatug/backend/models4datatug"
)

var (
	// ErrOrgRequired is returned when a GitHub project is registered without
	// its repository owner.
	ErrOrgRequired = errors.New("github org is required")

	// ErrRepoRequired is returned when a GitHub project is registered without
	// its repository name.
	ErrRepoRequired = errors.New("github repo is required")
)

// RegisterGithubProjectRequest describes a project that already exists in a
// GitHub repository.
type RegisterGithubProjectRequest struct {
	Org    string `json:"org"`
	Repo   string `json:"repo"`
	Folder string `json:"folder"`
	Title  string `json:"title"`
}

// Validate validates the request.
func (v RegisterGithubProjectRequest) Validate() error {
	if v.Org == "" {
		return ErrOrgRequired
	}
	if v.Repo == "" {
		return ErrRepoRequired
	}
	if v.Title == "" {
		return ErrTitleRequired
	}
	return nil
}

// RegisterGithubProject records a project that lives in a GitHub repository in
// the user's DataTug index, so it shows up among the user's projects.
//
// The project files themselves are created by the client that holds the user's
// GitHub credential — this command only keeps the index; the cloud never talks
// to GitHub. It returns the project id the web client addresses the project by
// (`repo@org@folder`).
func (f Facade) RegisterGithubProject(
	ctx context.Context, userID string, request RegisterGithubProjectRequest,
) (projectID string, err error) {
	if userID == "" {
		return "", ErrUserIDRequired
	}
	if err = request.Validate(); err != nil {
		return "", err
	}
	folder := request.Folder
	if folder == "" {
		folder = models4datatug.DefaultGithubProjectFolder
	}
	projectID = models4datatug.NewGithubProjectID(request.Org, request.Repo, folder)
	brief := &models4datatug.ProjectBrief{
		Title:  request.Title,
		Access: models4datatug.AccessPrivate,
	}
	index := storeIndex{
		ID:    models4datatug.GithubStoreID,
		Type:  models4datatug.GithubStoreType,
		Title: models4datatug.GithubStoreTitle,
	}
	if err = f.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		userExtRecord, userExt, indexExists, readErr := readProjectIndex(ctx, tx, userID)
		if readErr != nil {
			return readErr
		}
		return writeProjectBrief(ctx, tx, userExtRecord, userExt, indexExists, index, projectID, brief)
	}); err != nil {
		return "", err
	}
	return projectID, nil
}
