package facade4datatug

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dal-go/dalgo/dal"

	"github.com/datatug/backend/models4datatug"
)

var (
	// ErrUserIDRequired is returned when a command is called without a user.
	ErrUserIDRequired = errors.New("user id is required")

	// ErrTitleRequired is returned when a project is created without a title.
	ErrTitleRequired = errors.New("project title is required")

	// ErrUnsupportedStore is returned for a store this API does not serve. A
	// project in a GitHub repo is created by the client holding the user's
	// GitHub credential, and a local filesystem project by the datatug CLI
	// agent — only the DataTug cloud store is created here.
	ErrUnsupportedStore = errors.New("store is not served by the DataTug cloud API")
)

// CreateProject creates a DataTug project in the cloud store and registers its
// brief in the user's DataTug index, both in one transaction. It returns the
// id of the created project.
func (f Facade) CreateProject(
	ctx context.Context, userID, storeID, title string,
) (projectID string, err error) {
	if userID == "" {
		return "", ErrUserIDRequired
	}
	if title == "" {
		return "", ErrTitleRequired
	}
	if storeID != models4datatug.FirestoreStoreID {
		return "", fmt.Errorf("%w: %s", ErrUnsupportedStore, storeID)
	}
	if projectID, err = f.ids.NewID(ctx); err != nil {
		return "", fmt.Errorf("failed to generate project id: %w", err)
	}
	err = f.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return createProjectInTx(ctx, tx, userID, projectID, storeID, title)
	})
	if err != nil {
		return "", err
	}
	return projectID, nil
}

// createProjectInTx inserts the datatug_projects/{projectID} record and then
// adds the project brief to the user's DataTug index: inserting the index
// record when the user has none yet, and otherwise updating only the new
// project's field path, so two concurrent creates cannot clobber each other's
// briefs.
func createProjectInTx(
	ctx context.Context, tx dal.ReadwriteTransaction,
	userID, projectID, storeID, title string,
) error {
	// The user's index is read BEFORE the first write: Firestore (and the
	// in-memory database the tests run against) requires every read in a
	// transaction to precede any write.
	userExtRecord, userExt, indexExists, err := readProjectIndex(ctx, tx, userID)
	if err != nil {
		return err
	}

	projectRecord, project := models4datatug.NewProjectRecord(projectID)
	project.Title = title
	project.Access = models4datatug.AccessPrivate
	project.UserIDs = []string{userID}
	project.Created = &models4datatug.Created{At: time.Now().UTC()}
	if err := tx.Insert(ctx, projectRecord); err != nil {
		return fmt.Errorf("failed to insert project record: %w", err)
	}

	return writeProjectBrief(ctx, tx, userExtRecord, userExt, indexExists,
		storeIndex{ID: storeID, Type: storeID, Title: models4datatug.FirestoreStoreTitle},
		projectID, &models4datatug.ProjectBrief{Title: title, Access: project.Access})
}
