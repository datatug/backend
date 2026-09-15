// Package models4datatug holds this extension's database objects and the
// dalgo keys they are stored under. Like the rest of the module it depends on
// dal-go only — no database client, no platform package.
package models4datatug

import (
	"time"

	"github.com/dal-go/record"
)

const (
	// ProjectsCollection stores DataTug projects.
	ProjectsCollection = "datatug_projects"

	// UsersCollection is the platform's users collection.
	UsersCollection = "users"

	// UserExtCollection is the platform's collection of per-extension user
	// documents: users/{userID}/ext/{extID}. The DataTug extension's user data
	// (which stores and projects the user can see) lives there, never inline in
	// the core users/{userID} document. This mirrors the key that
	// sneat-core-modules' dal4userus.NewUserExtKey builds; the module builds it
	// itself so that it depends on dal-go only.
	UserExtCollection = "ext"

	// FirestoreStoreID is the store id of the DataTug cloud store. It is what
	// the web client sends as `?store=` and the key of the store brief in the
	// user's index.
	FirestoreStoreID = "firestore"

	// FirestoreStoreTitle is the human-readable title of the cloud store.
	FirestoreStoreTitle = "DataTug cloud"

	// GithubStoreID is the store id of a project hosted in a GitHub repository —
	// the id the web client's GitHub reader and navigation use.
	GithubStoreID = "github.com"

	// GithubStoreType is the store type reported for GitHub-hosted projects.
	GithubStoreType = "github"

	// GithubStoreTitle is the human-readable title of the GitHub store.
	GithubStoreTitle = "GitHub"

	// DefaultGithubProjectFolder is the repo folder a project lives in when the
	// user does not name one — the same default the web client's
	// parseGithubProjectId() applies.
	DefaultGithubProjectFolder = "datatug"

	// AccessPrivate means only the userIDs listed on the project can read it.
	AccessPrivate = "private"
)

// Project is the data stored in a datatug_projects/{projectID} record.
type Project struct {
	Title   string   `json:"title" firestore:"title"`
	Access  string   `json:"access,omitempty" firestore:"access,omitempty"`
	UserIDs []string `json:"userIDs,omitempty" firestore:"userIDs,omitempty"`
	Created *Created `json:"created,omitempty" firestore:"created,omitempty"`
}

// Created holds the creation timestamp of a DataTug record.
type Created struct {
	At time.Time `json:"at" firestore:"at"`
}

// NewProjectKey builds the dalgo key of a project record.
func NewProjectKey(projectID string) *record.Key {
	return record.NewKeyWithID(ProjectsCollection, projectID)
}

// NewProjectRecord wraps a fresh Project in its dalgo record, returning both
// the record (for database calls) and the DBO (to populate fields on).
func NewProjectRecord(projectID string) (record.Record, *Project) {
	dbo := new(Project)
	return record.NewRecordWithData(NewProjectKey(projectID), dbo), dbo
}

// UserExt is the DataTug extension data of one user: the index of the DataTug
// stores the user can see, and the project briefs inside each of them. It is
// the data of the users/{userID}/ext/datatug record.
type UserExt struct {
	Stores map[string]*StoreBrief `json:"stores,omitempty" firestore:"stores,omitempty"`
}

// StoreBrief is a brief of one store in a user's DataTug index.
type StoreBrief struct {
	Title    string                   `json:"title" firestore:"title"`
	Type     string                   `json:"type" firestore:"type"`
	URL      string                   `json:"url,omitempty" firestore:"url,omitempty"`
	Projects map[string]*ProjectBrief `json:"projects,omitempty" firestore:"projects,omitempty"`
}

// ProjectBrief is a brief of one project in a user's DataTug index.
type ProjectBrief struct {
	Title  string `json:"title" firestore:"title"`
	Access string `json:"access,omitempty" firestore:"access,omitempty"`
}

// NewUserExtKey builds the dalgo key of a user's DataTug index record:
// users/{userID}/ext/{extID}.
func NewUserExtKey(userID, extID string) *record.Key {
	userKey := record.NewKeyWithID(UsersCollection, userID)
	return record.NewKeyWithParentAndID(userKey, UserExtCollection, extID)
}

// NewUserExtRecord wraps a fresh UserExt in its dalgo record, returning both
// the record and the DBO.
func NewUserExtRecord(userID, extID string) (record.Record, *UserExt) {
	dbo := new(UserExt)
	return record.NewRecordWithData(NewUserExtKey(userID, extID), dbo), dbo
}

// NewGithubProjectID builds the id a GitHub-hosted project is addressed by:
// `repo@org@folder` — the format the web client's parseGithubProjectId()
// expects.
func NewGithubProjectID(org, repo, folder string) string {
	return repo + "@" + org + "@" + folder
}
