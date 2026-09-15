package facade4datatug

import (
	"context"
	"fmt"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"

	"github.com/datatug/backend/const4datatug"
	"github.com/datatug/backend/models4datatug"
)

// storeIndex identifies the store entry a project brief is registered under.
type storeIndex struct {
	ID    string
	Type  string
	Title string
}

// readProjectIndex reads the user's DataTug index. Every command must call this
// BEFORE its first write: Firestore requires all reads in a transaction to
// precede any write. indexExists reports whether the user already has an index
// record.
func readProjectIndex(
	ctx context.Context, tx dal.ReadwriteTransaction, userID string,
) (userExtRecord record.Record, userExt *models4datatug.UserExt, indexExists bool, err error) {
	userExtRecord, userExt = models4datatug.NewUserExtRecord(userID, const4datatug.ExtensionID)
	readErr := tx.Get(ctx, userExtRecord)
	if readErr == nil {
		return userExtRecord, userExt, true, nil
	}
	if !record.IsNotFound(readErr) {
		return nil, nil, false, fmt.Errorf("failed to read user's DataTug index: %w", readErr)
	}
	return userExtRecord, userExt, false, nil
}

// writeProjectBrief registers a project brief under index, creating the index
// record when the user has none and the store entry when that store is new.
// When the index and store already exist only the project's field path is
// written, so concurrent registrations cannot clobber each other's briefs.
func writeProjectBrief(
	ctx context.Context, tx dal.ReadwriteTransaction,
	userExtRecord record.Record, userExt *models4datatug.UserExt, indexExists bool,
	index storeIndex, projectID string, brief *models4datatug.ProjectBrief,
) error {
	if indexExists {
		if _, storeExists := userExt.Stores[index.ID]; storeExists {
			return tx.Update(ctx, userExtRecord.Key(), []update.Update{
				update.ByFieldPath([]string{"stores", index.ID, "projects", projectID}, brief),
			})
		}
		return tx.Update(ctx, userExtRecord.Key(), []update.Update{
			update.ByFieldPath([]string{"stores", index.ID}, &models4datatug.StoreBrief{
				Title: index.Title,
				Type:  index.Type,
				Projects: map[string]*models4datatug.ProjectBrief{
					projectID: brief,
				},
			}),
		})
	}
	userExt.Stores = map[string]*models4datatug.StoreBrief{
		index.ID: {
			Title: index.Title,
			Type:  index.Type,
			Projects: map[string]*models4datatug.ProjectBrief{
				projectID: brief,
			},
		},
	}
	return tx.Insert(ctx, userExtRecord)
}
