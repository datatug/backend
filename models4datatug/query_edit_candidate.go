package models4datatug

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/dal-go/record"
)

const GitHubQueryEditSourceID = "github-query-draft/1"
const QueryEditCandidateCollection = "queryEditCandidates"
const QueryEditUnclassified = "unclassified"

var ErrInvalidQueryEditCandidate = errors.New("invalid query edit candidate")

// QueryEditCandidate is private, server-accepted evidence of a changed query
// body submitted by an authorized actor. It is unclassified for billing: a
// provider failure, stale later commit, or abandoned save does not promote it.
// Query text and text-derived digests are deliberately absent.
type QueryEditCandidate struct {
	Version        int       `firestore:"v"`
	SourceID       string    `firestore:"sourceID"`
	EventID        string    `firestore:"eventID"`
	ActorID        string    `firestore:"actorID"`
	OperationID    string    `firestore:"operationID"`
	SpaceID        string    `firestore:"spaceID"`
	ProjectID      string    `firestore:"projectID"`
	RepositoryID   int64     `firestore:"repositoryID"`
	Folder         string    `firestore:"folder"`
	ExpectedHead   string    `firestore:"expectedHead"`
	QueryPath      string    `firestore:"queryPath"`
	BodyChanged    bool      `firestore:"bodyChanged"`
	Classification string    `firestore:"classification"`
	AcceptedAtUTC  time.Time `firestore:"acceptedAtUTC"`
}

func (c QueryEditCandidate) Validate() error {
	if c.Version != 1 || c.SourceID != GitHubQueryEditSourceID || c.EventID != NewQueryEditCandidateID(c.ActorID, c.OperationID) || c.ActorID == "" || c.OperationID == "" || ValidateSharedProjectIdentifier(c.SpaceID) != nil || ValidateSharedProjectIdentifier(c.ProjectID) != nil || c.RepositoryID < 1 || c.Folder == "" || c.ExpectedHead == "" || c.QueryPath == "" || !c.BodyChanged || c.Classification != QueryEditUnclassified || c.AcceptedAtUTC.IsZero() || c.AcceptedAtUTC.Location() != time.UTC {
		return ErrInvalidQueryEditCandidate
	}
	return nil
}

func NewQueryEditCandidateID(actorID, operationID string) string {
	encoded, _ := json.Marshal([3]string{GitHubQueryEditSourceID, actorID, operationID})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func NewQueryEditCandidateRecord(spaceID, actorID, operationID string) (record.Record, *QueryEditCandidate) {
	key := record.NewKeyWithParentAndID(sharedProjectExtensionKey(spaceID), QueryEditCandidateCollection, NewQueryEditCandidateID(actorID, operationID))
	candidate := new(QueryEditCandidate)
	return record.NewRecordWithData(key, candidate), candidate
}
