// Package datatugfacade holds the DataTug business logic.
//
// It is storage-agnostic by construction: the only persistence API it uses is
// dal-go (dal.DB / dal.ReadwriteTransaction via sneat-go-core's facade), so it
// never imports a Firestore (or any other) client and can run against any
// dal-go backed database.
package datatugfacade

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"github.com/sneat-co/sneat-core-modules/userus/dal4userus"
	"github.com/sneat-co/sneat-go-core/facade"
	"github.com/strongo/validation"

	"github.com/datatug/backend/const4datatug"
	"github.com/datatug/backend/dbo4datatug"
)

const (
	projectIDLength   = 8
	projectIDAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
)

// CreateProjectRequest is the request to create a DataTug project.
type CreateProjectRequest struct {
	// StoreID is the store the project is created in. Only the DataTug cloud
	// (Firestore-backed) store is served by this API today: a project in a
	// GitHub repo is created by the client, which owns the user's GitHub
	// credential, and a project on a local filesystem is created by the
	// datatug CLI agent.
	StoreID string `json:"storeID"`
	Title   string `json:"title"`
}

// Validate validates the request.
func (v CreateProjectRequest) Validate() error {
	if v.StoreID == "" {
		return validation.NewErrRequestIsMissingRequiredField("storeID")
	}
	if v.StoreID != dbo4datatug.FirestoreStoreType {
		return fmt.Errorf("store is not supported by the DataTug cloud API: %s", v.StoreID)
	}
	if v.Title == "" {
		return validation.NewErrRequestIsMissingRequiredField("title")
	}
	return nil
}

// CreateProjectResponse is returned when a project has been created.
type CreateProjectResponse struct {
	ID string `json:"id"`
}

// CreateProject creates the project record and registers its brief in the
// user's DataTug index (users/{userID}/ext/datatug), both in one transaction.
func CreateProject(ctx facade.ContextWithUser, request CreateProjectRequest) (response CreateProjectResponse, err error) {
	if err = request.Validate(); err != nil {
		return response, err
	}
	userID := ctx.User().GetUserID()
	if userID == "" {
		return response, facade.ErrUnauthorized
	}
	var projectID string
	if projectID, err = newProjectID(); err != nil {
		return response, fmt.Errorf("failed to generate project ID: %w", err)
	}
	if err = facade.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return createProjectInTx(ctx, tx, userID, projectID, request)
	}); err != nil {
		return response, err
	}
	response.ID = projectID
	return response, nil
}

// createProjectInTx is the transaction body: insert the datatug_projects/{id}
// record, then add the project brief to the user's DataTug index — inserting
// the index document when the user has none yet, and otherwise updating just
// the new project's field path.
func createProjectInTx(ctx context.Context, tx dal.ReadwriteTransaction, userID, projectID string, request CreateProjectRequest) error {
	project := &dbo4datatug.Project{
		Title:   request.Title,
		Access:  dbo4datatug.AccessPrivate,
		UserIDs: []string{userID},
		Created: &dbo4datatug.Created{At: time.Now().UTC()},
	}
	if err := project.Validate(); err != nil {
		return fmt.Errorf("invalid project record: %w", err)
	}
	projectKey := record.NewKeyWithID(dbo4datatug.ProjectsCollection, projectID)
	if err := tx.Insert(ctx, record.NewRecordWithData(projectKey, project)); err != nil {
		return fmt.Errorf("failed to insert project record: %w", err)
	}

	brief := &dbo4datatug.ProjectBrief{
		Title:  project.Title,
		Access: project.Access,
	}

	userExtRecord := record.NewRecordWithData(
		dal4userus.NewUserExtKey(userID, const4datatug.ExtensionID),
		new(dbo4datatug.UserExt),
	)
	err := tx.Get(ctx, userExtRecord)
	switch {
	case err == nil:
		// The user already has a DataTug index: add just this project's field.
		return tx.Update(ctx, userExtRecord.Key(), []update.Update{
			update.ByFieldPath([]string{
				"stores", dbo4datatug.FirestoreStoreType, "projects", projectID,
			}, brief),
		})
	case record.IsNotFound(err):
		userExt := userExtRecord.Data().(*dbo4datatug.UserExt)
		userExt.Stores = map[string]*dbo4datatug.StoreBrief{
			dbo4datatug.FirestoreStoreType: {
				Title: dbo4datatug.FirestoreStoreTitle,
				Type:  dbo4datatug.FirestoreStoreType,
				Projects: map[string]*dbo4datatug.ProjectBrief{
					projectID: brief,
				},
			},
		}
		if err := userExt.Validate(); err != nil {
			return fmt.Errorf("invalid user ext record: %w", err)
		}
		return tx.Insert(ctx, userExtRecord)
	default:
		return fmt.Errorf("failed to read user's DataTug index: %w", err)
	}
}

// newProjectID returns a short, URL-safe random project ID.
func newProjectID() (string, error) {
	buf := make([]byte, projectIDLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		buf[i] = projectIDAlphabet[int(b)%len(projectIDAlphabet)]
	}
	return string(buf), nil
}

// ErrNotFound is re-exported for callers that need to distinguish a missing
// project record from a storage failure without importing dal-go errors.
var ErrNotFound = errors.New("datatug record not found")
