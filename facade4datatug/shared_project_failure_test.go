package facade4datatug

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

type sharedUnsupportedTxDB struct{ dal.DB }

func (sharedUnsupportedTxDB) RunReadwriteTransaction(context.Context, dal.RWTxWorker, ...dal.TransactionOption) error {
	return dal.ErrNotSupported
}

func TestSharedProjectCreateNoTransactionFallback(t *testing.T) {
	base := sneatcoretesting.NewMemoryDB()
	s := newSharedService(t, sharedUnsupportedTxDB{DB: base}, fakeIDGenerator{next: "project"}, &sharedAuthority{})
	if ref, err := s.Create(context.Background(), sharedCommand()); !errors.Is(err, dal.ErrNotSupported) || ref != (models4datatug.SharedProjectRef{}) {
		t.Fatalf("unsupported transaction %+v: %v", ref, err)
	}
	assertSharedAbsent(t, base, "space", "project", "command")
}

func TestSharedProjectCreateAuthorityDenialIsNotMembershipInference(t *testing.T) {
	// The domain must honor both authority stages. Concrete ordinary-Space,
	// role, module and expiry policy belongs to the released host adapter.
	for _, stage := range []string{"prepare", "transaction"} {
		for _, reason := range []string{"foreign-actor", "foreign-space", "role-revoked", "module-disabled", "inactive-space", "nonordinary-space", "expired-reservation"} {
			t.Run(stage+"/"+reason, func(t *testing.T) {
				db := sneatcoretesting.NewMemoryDB()
				a := &sharedAuthority{}
				if stage == "prepare" {
					a.prepare = func(SharedProjectCreateBinding) (PreparedSharedProjectCreate, error) {
						return PreparedSharedProjectCreate{}, errors.New(reason)
					}
				} else {
					a.check = func(context.Context, dal.ReadwriteTransaction, SharedProjectCreateBinding, time.Time) error {
						return errors.New(reason)
					}
				}
				if ref, err := newSharedService(t, db, fakeIDGenerator{next: "project"}, a).Create(context.Background(), sharedCommand()); !errors.Is(err, ErrSharedProjectUnauthorized) || ref != (models4datatug.SharedProjectRef{}) {
					t.Fatalf("ref %+v err %v", ref, err)
				}
				assertSharedAbsent(t, db, "space", "project", "command")
			})
		}
	}
}

func TestSharedProjectCreateIDCollisionDoesNotOverwrite(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	s := newSharedService(t, db, fakeIDGenerator{next: "project"}, &sharedAuthority{})
	c := sharedCommand()
	if _, err := s.Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	before := mustReadSharedReceipt(t, db, c)
	c.CommandID, c.Title = "second-command", "Different"
	if _, err := s.Create(context.Background(), c); err == nil {
		t.Fatal("ID collision overwrote project")
	}
	r, project := models4datatug.NewSharedProjectRecord("space", "project")
	if err := db.Get(context.Background(), r); err != nil || project.Title != "Shared" {
		t.Fatalf("project %+v: %v", project, err)
	}
	receipt, _ := models4datatug.NewSharedProjectCreateReceiptRecord(c.SpaceID, c.CommandID)
	if err := db.Get(context.Background(), receipt); err == nil {
		t.Fatal("failed command committed receipt")
	}
	if before != mustReadSharedReceipt(t, db, sharedCommand()) {
		t.Fatal("original receipt changed")
	}
}

func TestSharedProjectCreateRejectsForeignOrCorruptReceipt(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "corrupt", true: "foreign"}[foreign], func(t *testing.T) {
			db := sneatcoretesting.NewMemoryDB()
			s := newSharedService(t, db, fakeIDGenerator{next: "project"}, &sharedAuthority{})
			c := sharedCommand()
			if _, err := s.Create(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			original := mustReadSharedReceipt(t, db, c)
			if foreign {
				c.SpaceID = "other-space"
			}
			if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
				r, data := models4datatug.NewSharedProjectCreateReceiptRecord(c.SpaceID, c.CommandID)
				*data = original
				if !foreign {
					data.RequestDigest = "corrupt"
				}
				// Seed an invalid persisted row with a non-Validatable DTO to test
				// the read boundary; production writes keep DAL validation enabled.
				return tx.Set(ctx, record.NewRecordWithData(r.Key(), map[string]any{"v": data.Version, "actorID": data.ActorID, "spaceID": data.SpaceID, "commandID": data.CommandID, "title": data.Title, "requestDigest": data.RequestDigest, "projectID": data.ProjectID, "createdAt": data.CreatedAt}))
			}); err != nil {
				t.Fatal(err)
			}
			if ref, err := s.Create(context.Background(), c); !errors.Is(err, ErrSharedProjectConflict) || ref != (models4datatug.SharedProjectRef{}) {
				t.Fatalf("bad receipt %+v: %v", ref, err)
			}
		})
	}
}
