package datatugfacade

import (
	"context"
	"errors"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"github.com/sneat-co/sneat-go-core/facade"

	"github.com/datatug/backend/dbo4datatug"
)

const testUserID = "user1"
const testProjectID = "proj1234"

// fakeTx implements only the dal.ReadwriteTransaction methods the facade uses.
// The embedded interface satisfies the rest of the (large) interface, and any
// method the facade starts calling without the test modelling it panics with a
// nil-pointer dereference rather than silently passing.
type fakeTx struct {
	dal.ReadwriteTransaction
	userExt      *dbo4datatug.UserExt
	inserts      []record.Record
	updatedKey   *record.Key
	userExtFails error
	insertFails  error
}

func (t *fakeTx) Get(_ context.Context, rec record.Record) error {
	if t.userExtFails != nil {
		return t.userExtFails
	}
	if t.userExt == nil {
		return record.ErrRecordNotFound
	}
	*(rec.Data().(*dbo4datatug.UserExt)) = *t.userExt
	return nil
}

func (t *fakeTx) Insert(_ context.Context, rec record.Record, _ ...dal.InsertOption) error {
	if t.insertFails != nil {
		return t.insertFails
	}
	t.inserts = append(t.inserts, rec)
	return nil
}

func (t *fakeTx) Update(_ context.Context, key *record.Key, _ []update.Update, _ ...dal.Precondition) error {
	t.updatedKey = key
	return nil
}

// fakeDB serves the fakeTx for the facade's transaction helper.
type fakeDB struct {
	dal.DB
	tx *fakeTx
}

func (db fakeDB) RunReadwriteTransaction(ctx context.Context, f func(context.Context, dal.ReadwriteTransaction) error, _ ...dal.TransactionOption) error {
	return f(ctx, db.tx)
}

func firestoreRequest() CreateProjectRequest {
	return CreateProjectRequest{StoreID: dbo4datatug.FirestoreStoreType, Title: "My project"}
}

func TestCreateProjectRequestValidate(t *testing.T) {
	for name, request := range map[string]CreateProjectRequest{
		"missing store": {Title: "T"},
		"missing title": {StoreID: dbo4datatug.FirestoreStoreType},
		"unknown store": {StoreID: "github.com", Title: "T"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := request.Validate(); err == nil {
				t.Fatalf("Validate() must reject %+v", request)
			}
		})
	}
	if err := firestoreRequest().Validate(); err != nil {
		t.Fatalf("Validate() rejected a valid request: %v", err)
	}
}

// A user with no DataTug index yet gets the index document inserted, holding
// the cloud store brief with the new project in it.
func TestCreateProjectInTx_insertsIndexWhenUserHasNone(t *testing.T) {
	tx := &fakeTx{}
	request := firestoreRequest()

	if err := createProjectInTx(context.Background(), tx, testUserID, testProjectID, request); err != nil {
		t.Fatalf("createProjectInTx(): %v", err)
	}
	if len(tx.inserts) != 2 {
		t.Fatalf("expected 2 inserts (project + user index), got %d", len(tx.inserts))
	}
	project, ok := tx.inserts[0].Data().(*dbo4datatug.Project)
	if !ok {
		t.Fatalf("first insert is %T, want *dbo4datatug.Project", tx.inserts[0].Data())
	}
	if project.Title != request.Title {
		t.Errorf("project title = %q, want %q", project.Title, request.Title)
	}
	if project.Access != dbo4datatug.AccessPrivate {
		t.Errorf("project access = %q, want %q", project.Access, dbo4datatug.AccessPrivate)
	}
	if len(project.UserIDs) != 1 || project.UserIDs[0] != testUserID {
		t.Errorf("project userIDs = %v, want [%s]", project.UserIDs, testUserID)
	}
	if project.Created == nil || project.Created.At.IsZero() {
		t.Error("project created.at must be set")
	}

	ext, ok := tx.inserts[1].Data().(*dbo4datatug.UserExt)
	if !ok {
		t.Fatalf("second insert is %T, want *dbo4datatug.UserExt", tx.inserts[1].Data())
	}
	store, ok := ext.Stores[dbo4datatug.FirestoreStoreType]
	if !ok {
		t.Fatalf("user index is missing the %q store: %+v", dbo4datatug.FirestoreStoreType, ext.Stores)
	}
	if store.Type != dbo4datatug.FirestoreStoreType || store.Title != dbo4datatug.FirestoreStoreTitle {
		t.Errorf("store brief = %+v, want type/title of the cloud store", store)
	}
	brief, ok := store.Projects[testProjectID]
	if !ok {
		t.Fatalf("user index is missing project %q: %+v", testProjectID, store.Projects)
	}
	if brief.Title != request.Title || brief.Access != dbo4datatug.AccessPrivate {
		t.Errorf("project brief = %+v, want title %q access %q", brief, request.Title, dbo4datatug.AccessPrivate)
	}
	// The user-ext document must be keyed as users/{uid}/ext/datatug.
	if key := tx.inserts[1].Key().String(); key != "users/"+testUserID+"/ext/datatug" {
		t.Errorf("user index key = %q, want users/%s/ext/datatug", key, testUserID)
	}
}

// A user that already has a DataTug index gets only the new project's field
// touched, so concurrent creates for different projects do not clobber each
// other's briefs.
func TestCreateProjectInTx_updatesExistingIndex(t *testing.T) {
	tx := &fakeTx{userExt: &dbo4datatug.UserExt{Stores: map[string]*dbo4datatug.StoreBrief{
		dbo4datatug.FirestoreStoreType: {
			Title: dbo4datatug.FirestoreStoreTitle,
			Type:  dbo4datatug.FirestoreStoreType,
			Projects: map[string]*dbo4datatug.ProjectBrief{
				"existing": {Title: "Existing project", Access: dbo4datatug.AccessPrivate},
			},
		},
	}}}

	if err := createProjectInTx(context.Background(), tx, testUserID, testProjectID, firestoreRequest()); err != nil {
		t.Fatalf("createProjectInTx(): %v", err)
	}
	if len(tx.inserts) != 1 {
		t.Fatalf("expected only the project record to be inserted, got %d inserts", len(tx.inserts))
	}
	if tx.updatedKey == nil {
		t.Fatal("expected the existing user index to be updated")
	}
	if key := tx.updatedKey.String(); key != "users/"+testUserID+"/ext/datatug" {
		t.Errorf("updated key = %q, want users/%s/ext/datatug", key, testUserID)
	}
}

func TestCreateProjectInTx_propagatesReadFailure(t *testing.T) {
	readErr := errors.New("boom")
	tx := &fakeTx{userExtFails: readErr}

	err := createProjectInTx(context.Background(), tx, testUserID, testProjectID, firestoreRequest())
	if !errors.Is(err, readErr) {
		t.Fatalf("createProjectInTx() error = %v, want it to wrap %v", err, readErr)
	}
}

// A failing project insert must abort before the user's index is touched.
func TestCreateProjectInTx_propagatesInsertFailure(t *testing.T) {
	insertErr := errors.New("insert boom")
	tx := &fakeTx{insertFails: insertErr}

	err := createProjectInTx(context.Background(), tx, testUserID, testProjectID, firestoreRequest())
	if !errors.Is(err, insertErr) {
		t.Fatalf("createProjectInTx() error = %v, want it to wrap %v", err, insertErr)
	}
	if tx.updatedKey != nil {
		t.Error("the user index must not be updated when the project insert fails")
	}
	if tx.inserts != nil {
		t.Errorf("no record should be recorded as inserted, got %d", len(tx.inserts))
	}
}

// CreateProject wires the facade to the platform's transaction helper and
// returns the generated project id.
func TestCreateProject_returnsGeneratedID(t *testing.T) {
	tx := &fakeTx{}
	// WithSneatDB wraps the context (context.WithValue), which drops the
	// ContextWithUser interface, so the user context is layered back on top.
	ctx := facade.NewContextWithUser(
		facade.WithSneatDB(context.Background(), fakeDB{tx: tx}),
		facade.NewUserContext(testUserID),
	)

	response, err := CreateProject(ctx, firestoreRequest())
	if err != nil {
		t.Fatalf("CreateProject(): %v", err)
	}
	if len(response.ID) != projectIDLength {
		t.Errorf("response.ID = %q, want %d characters", response.ID, projectIDLength)
	}
	if len(tx.inserts) != 2 {
		t.Errorf("expected the project and the user index to be written, got %d inserts", len(tx.inserts))
	}
}

// userContextWithoutID models a principal that reached the facade without a
// user id. facade.NewUserContext panics on an empty id by design, so the stub
// is the only way to exercise CreateProject's own guard.
type userContextWithoutID struct{}

func (userContextWithoutID) GetUserID() string { return "" }

func TestCreateProject_rejectsUnauthenticatedUser(t *testing.T) {
	ctx := facade.NewContextWithUser(
		facade.WithSneatDB(context.Background(), fakeDB{tx: &fakeTx{}}),
		userContextWithoutID{},
	)

	if _, err := CreateProject(ctx, firestoreRequest()); !errors.Is(err, facade.ErrUnauthorized) {
		t.Fatalf("CreateProject() error = %v, want %v", err, facade.ErrUnauthorized)
	}
}

// newProjectID must produce distinct, alphabet-only ids.
func TestNewProjectID(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id, err := newProjectID()
		if err != nil {
			t.Fatalf("newProjectID(): %v", err)
		}
		if len(id) != projectIDLength {
			t.Fatalf("id %q is %d characters, want %d", id, len(id), projectIDLength)
		}
		for _, r := range id {
			if !containsRune(projectIDAlphabet, r) {
				t.Fatalf("id %q contains %q, which is outside the alphabet", id, r)
			}
		}
		seen[id] = true
	}
	if len(seen) < 90 {
		t.Errorf("100 generated ids produced only %d distinct values", len(seen))
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
