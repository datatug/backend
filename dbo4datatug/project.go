// Package dbo4datatug defines the DataTug database objects (the shapes stored
// in the shared Sneat/Firestore database). It deliberately has no knowledge of
// any particular database: the structs are plain data with json/firestore tags,
// and everything is read/written through dal-go by the facade packages.
package dbo4datatug

import (
	"time"

	"github.com/strongo/validation"
)

const (
	// ProjectsCollection is the dal-go collection holding DataTug projects.
	ProjectsCollection = "datatug_projects"

	// FirestoreStoreType is the store type of the DataTug cloud store. It is
	// the value the web client sends as `?store=` and uses as the key of the
	// store brief in the user's index.
	FirestoreStoreType = "firestore"

	// FirestoreStoreTitle is the human-readable title of the cloud store.
	FirestoreStoreTitle = "DataTug cloud"

	// AccessPrivate means only the userIDs listed on the project can read it.
	AccessPrivate = "private"
)

// Project is the data stored in a datatug_projects/{projectID} document.
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

// Validate validates the project record.
func (v Project) Validate() error {
	if v.Title == "" {
		return validation.NewErrRecordIsMissingRequiredField("title")
	}
	return nil
}

// UserExt is the data stored in the users/{userID}/ext/datatug document: the
// per-user index of the DataTug stores the user can see and the project briefs
// inside each of them. This is the platform's extension-owned user record —
// extension data never lives inline in the core users/{userID} document.
type UserExt struct {
	Stores map[string]*StoreBrief `json:"stores,omitempty" firestore:"stores,omitempty"`
}

// StoreBrief is a brief of one store in the user's DataTug index.
type StoreBrief struct {
	Title    string                   `json:"title" firestore:"title"`
	Type     string                   `json:"type" firestore:"type"`
	URL      string                   `json:"url,omitempty" firestore:"url,omitempty"`
	Projects map[string]*ProjectBrief `json:"projects,omitempty" firestore:"projects,omitempty"`
}

// ProjectBrief is a brief of one project in the user's DataTug index.
type ProjectBrief struct {
	Title  string `json:"title" firestore:"title"`
	Access string `json:"access,omitempty" firestore:"access,omitempty"`
}

// Validate validates the user's DataTug extension record.
func (v *UserExt) Validate() error {
	if v == nil {
		return validation.NewErrRecordIsMissingRequiredField("userExt")
	}
	for storeID, store := range v.Stores {
		if store == nil {
			return validation.NewErrBadRecordFieldValue("stores."+storeID, "is null")
		}
	}
	return nil
}
