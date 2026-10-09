package models4datatug

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/dal-go/record"
	"github.com/datatug/datatug-core/pkg/dto"
)

var ErrInvalidGitHubQueryOperation = errors.New("invalid GitHub query operation")

// GitHubQueryOperation records a server-only, actor-wide intent before any
// provider write. A retry can prove its exact commit and recover its result.
type GitHubQueryOperation struct {
	Version       int                   `firestore:"v"`
	ActorID       string                `firestore:"actorID"`
	OperationID   string                `firestore:"operationID"`
	RequestDigest string                `firestore:"requestDigest"`
	SpaceID       string                `firestore:"spaceID,omitempty"`
	ProjectID     string                `firestore:"projectID,omitempty"`
	BodyChanged   bool                  `firestore:"bodyChanged,omitempty"`
	RepositoryID  int64                 `firestore:"repositoryID"`
	Folder        string                `firestore:"folder"`
	Branch        string                `firestore:"branch"`
	ExpectedHead  string                `firestore:"expectedHead"`
	QueryPath     string                `firestore:"queryPath"`
	CommittedHead string                `firestore:"committedHead,omitempty"`
	Result        dto.SaveQueryResponse `firestore:"result"`
	CreatedAt     time.Time             `firestore:"createdAt"`
}

func (o GitHubQueryOperation) Validate() error {
	if (o.Version != 1 && o.Version != 2) || o.ActorID == "" || o.OperationID == "" || o.RequestDigest == "" || o.RepositoryID < 1 || o.Folder == "" || o.Branch == "" || o.ExpectedHead == "" || o.QueryPath == "" || o.CreatedAt.IsZero() || o.CreatedAt.Location() != time.UTC {
		return ErrInvalidGitHubQueryOperation
	}
	if o.Version == 2 && (ValidateSharedProjectIdentifier(o.SpaceID) != nil || ValidateSharedProjectIdentifier(o.ProjectID) != nil) {
		return ErrInvalidGitHubQueryOperation
	}
	return nil
}

func NewGitHubQueryOperationRecord(actorID, operationID string) (record.Record, *GitHubQueryOperation) {
	data, _ := json.Marshal([3]string{"github-query-operation/1", actorID, operationID})
	sum := sha256.Sum256(data)
	value := new(GitHubQueryOperation)
	return record.NewRecordWithData(record.NewKeyWithID("datatugGithubQueryOperations", hex.EncodeToString(sum[:])), value), value
}
