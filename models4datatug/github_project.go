package models4datatug

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dal-go/record"
)

const (
	GitHubProjectInitializing = "initializing"
	GitHubProjectReady        = "ready"
)

var (
	ErrInvalidGitHubBinding = errors.New("invalid GitHub project binding")
	gitHubOwnerPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	gitHubRepoPattern       = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	gitHubFolderSegment     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// GitHubProjectBinding is the immutable repository identity and project path.
// Owner/name are checked against GitHub on every request; they are never
// authority by themselves. Branch is the initially selected working branch.
type GitHubProjectBinding struct {
	RepositoryID int64  `json:"repositoryID" firestore:"repositoryID"`
	Owner        string `json:"owner" firestore:"owner"`
	Name         string `json:"name" firestore:"name"`
	Folder       string `json:"folder" firestore:"folder"`
	Branch       string `json:"branch" firestore:"branch"`
}

func (b GitHubProjectBinding) Validate() error {
	if b.RepositoryID < 1 || !gitHubOwnerPattern.MatchString(b.Owner) || !gitHubRepoPattern.MatchString(b.Name) || len(b.Folder) == 0 || len(b.Folder) > 256 || b.Branch == "" || len(b.Branch) > 255 || strings.ContainsAny(b.Branch, "\r\n\x00") {
		return ErrInvalidGitHubBinding
	}
	for _, segment := range strings.Split(b.Folder, "/") {
		if segment == "." || segment == ".." || !gitHubFolderSegment.MatchString(segment) {
			return ErrInvalidGitHubBinding
		}
	}
	return nil
}

// GitHubProjectLocator is a private uniqueness pointer, not a grant or a
// project. Its key is derived from immutable repository ID plus folder, so a
// repository rename cannot create a second shared project in another Space.
// The referenced shared project and its admission remain Space bounded.
type GitHubProjectLocator struct {
	Version      int       `firestore:"v"`
	RepositoryID int64     `firestore:"repositoryID"`
	Folder       string    `firestore:"folder"`
	SpaceID      string    `firestore:"spaceID"`
	ProjectID    string    `firestore:"projectID"`
	Status       string    `firestore:"status"`
	CreatedAt    time.Time `firestore:"createdAt"`
}

func (l GitHubProjectLocator) Validate() error {
	if l.Version != 1 || l.RepositoryID < 1 || ValidateSharedProjectIdentifier(l.SpaceID) != nil || ValidateSharedProjectIdentifier(l.ProjectID) != nil || (l.Status != GitHubProjectInitializing && l.Status != GitHubProjectReady) || l.CreatedAt.IsZero() || l.CreatedAt.Location() != time.UTC {
		return ErrInvalidGitHubBinding
	}
	b := GitHubProjectBinding{RepositoryID: l.RepositoryID, Owner: "owner", Name: "repo", Folder: l.Folder, Branch: "branch"}
	return b.Validate()
}

func NewGitHubProjectLocatorRecord(repositoryID int64, folder string) (record.Record, *GitHubProjectLocator) {
	key := record.NewKeyWithID("datatugGithubProjectLocators", githubProjectLocatorID(repositoryID, folder))
	value := new(GitHubProjectLocator)
	return record.NewRecordWithData(key, value), value
}

func githubProjectLocatorID(repositoryID int64, folder string) string {
	data, _ := json.Marshal([3]any{"github-project-locator/1", repositoryID, folder})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// GitHubProjectCreateOperation is server-only saga evidence. It binds a client
// operation ID to one actor, Space, payload and expected Git head before the
// provider write. A committed-but-not-finalized result can be reconciled;
// an uncertain outcome never releases quota or starts another commit blindly.
type GitHubProjectCreateOperation struct {
	Version        int                  `firestore:"v"`
	ActorID        string               `firestore:"actorID"`
	OperationID    string               `firestore:"operationID"`
	SpaceID        string               `firestore:"spaceID"`
	ProjectID      string               `firestore:"projectID"`
	RequestDigest  string               `firestore:"requestDigest"`
	Binding        GitHubProjectBinding `firestore:"binding"`
	ExpectedHead   string               `firestore:"expectedHead"`
	CommittedHead  string               `firestore:"committedHead,omitempty"`
	TemplateID     string               `firestore:"templateID"`
	TemplateCommit string               `firestore:"templateCommit"`
	Status         string               `firestore:"status"`
	CreatedAt      time.Time            `firestore:"createdAt"`
}

func (o GitHubProjectCreateOperation) Validate() error {
	if o.Version != 1 || o.ActorID == "" || len(o.ActorID) > 128 || o.OperationID == "" || len(o.OperationID) > 128 || ValidateSharedProjectIdentifier(o.SpaceID) != nil || ValidateSharedProjectIdentifier(o.ProjectID) != nil || o.RequestDigest == "" || o.ExpectedHead == "" || o.TemplateID == "" || o.TemplateCommit == "" || o.CreatedAt.IsZero() || o.CreatedAt.Location() != time.UTC || o.Binding.Validate() != nil {
		return ErrInvalidGitHubBinding
	}
	if o.Status != GitHubProjectInitializing && o.Status != GitHubProjectReady {
		return ErrInvalidGitHubBinding
	}
	if o.Status == GitHubProjectReady && o.CommittedHead == "" {
		return ErrInvalidGitHubBinding
	}
	return nil
}

// The operation key is actor-wide, rather than Space/project scoped. Reusing
// one ID for a different Space, repository, branch or payload is detectable.
func NewGitHubProjectCreateOperationRecord(actorID, operationID string) (record.Record, *GitHubProjectCreateOperation) {
	data, _ := json.Marshal([3]string{"github-project-create-operation/1", actorID, operationID})
	sum := sha256.Sum256(data)
	key := record.NewKeyWithID("datatugGithubProjectCreateOperations", hex.EncodeToString(sum[:]))
	value := new(GitHubProjectCreateOperation)
	return record.NewRecordWithData(key, value), value
}

func (o GitHubProjectCreateOperation) Match(actorID, operationID, spaceID, digest string) error {
	if o.Validate() != nil || o.ActorID != actorID || o.OperationID != operationID || o.SpaceID != spaceID || o.RequestDigest != digest {
		return fmt.Errorf("GitHub project create operation conflicts")
	}
	return nil
}
