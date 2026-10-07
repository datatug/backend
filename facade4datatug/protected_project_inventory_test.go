// Copyright 2026 Sneat.co
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

func TestDALProtectedProjectInventoryCompleteAndDeterministic(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	seedInventorySpace(t, db, "space-one")
	seedInventoryProject(t, db, "space-one", "project-one", true, models4datatug.ProjectAdmission{
		Version: 1, Mode: "live", Product: "datatug", PayerID: "payer-one", ActorID: "actor-one",
		SpaceID: "space-one", ProjectID: "project-one", CommandID: "command-one", RequestDigest: "request-one",
		LimitsVersion: "limits-one", ProfileVersion: "profile-one", QuotaBasisDigest: "basis-one",
		ProtectedProjectsLimit: 5, ProtectedUsersLimit: 5, QuotaRevision: 2, SubscriptionID: "subscription-one",
		OwnerGeneration: 1, CreatedAt: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC),
	})
	seedInventorySpace(t, db, "space-two")
	seedInventoryProject(t, db, "space-two", "project-two", true, models4datatug.ProjectAdmission{
		Version: 1, Mode: "live", Product: "datatug", PayerID: "payer-two", ActorID: "actor-two",
		SpaceID: "space-two", ProjectID: "project-two", CommandID: "command-two", RequestDigest: "request-two",
		LimitsVersion: "limits-two", ProfileVersion: "profile-two", QuotaBasisDigest: "basis-two",
		ProtectedProjectsLimit: 5, ProtectedUsersLimit: 5, QuotaRevision: 1, SubscriptionID: "subscription-two",
		OwnerGeneration: 1, CreatedAt: time.Date(2026, 10, 7, 12, 1, 0, 0, time.UTC),
	})
	inventory, err := NewDALProtectedProjectInventory(db)
	if err != nil {
		t.Fatal(err)
	}
	one, err := inventory.CompleteProtectedProjectBasis(context.Background(), "live", "datatug", "payer-one")
	if err != nil || one.Allocated != 1 || one.Digest == "" {
		t.Fatalf("first basis = %+v, %v", one, err)
	}
	two, err := inventory.CompleteProtectedProjectBasis(context.Background(), "live", "datatug", "payer-one")
	if err != nil || two != one {
		t.Fatalf("repeated basis = %+v, %v; want %+v", two, err, one)
	}
	otherPayer, err := inventory.CompleteProtectedProjectBasis(context.Background(), "live", "datatug", "payer-two")
	if err != nil || otherPayer.Allocated != 1 || otherPayer.Digest == one.Digest {
		t.Fatalf("other payer basis = %+v, %v", otherPayer, err)
	}
}

func TestDALProtectedProjectInventoryRejectsProtectedOrphanAndAdmissionOrphan(t *testing.T) {
	for _, test := range []struct {
		name      string
		project   bool
		admission bool
	}{
		{name: "protected project without admission", project: true},
		{name: "admission without project", admission: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := sneatcoretesting.NewMemoryDB()
			seedInventorySpace(t, db, "space-one")
			if test.project {
				projectRecord, project := models4datatug.NewSharedProjectRecord("space-one", "project-one")
				project.Access = models4datatug.AccessProtected
				if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
					return tx.Insert(ctx, projectRecord)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if test.admission {
				admissionRecord, admission := models4datatug.NewProjectAdmissionRecord("space-one", "project-one")
				*admission = models4datatug.ProjectAdmission{Version: 1, Mode: "live", Product: "datatug", PayerID: "payer-one", ActorID: "actor-one", SpaceID: "space-one", ProjectID: "project-one", CommandID: "command-one", RequestDigest: "request-one", LimitsVersion: "limits-one", ProfileVersion: "profile-one", QuotaBasisDigest: "basis-one", ProtectedProjectsLimit: 5, ProtectedUsersLimit: 5, QuotaRevision: 1, SubscriptionID: "subscription-one", OwnerGeneration: 1, CreatedAt: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
				if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
					return tx.Insert(ctx, admissionRecord)
				}); err != nil {
					t.Fatal(err)
				}
			}
			inventory, err := NewDALProtectedProjectInventory(db)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inventory.CompleteProtectedProjectBasis(context.Background(), "live", "datatug", "payer-one"); !errors.Is(err, ErrProtectedProjectInventory) {
				t.Fatalf("inventory error = %v", err)
			}
		})
	}
}

func TestDALProtectedProjectInventoryRejectsMissingSpaceAncestor(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	seedInventoryProject(t, db, "deleted-space", "project-one", true, models4datatug.ProjectAdmission{
		Version: 1, Mode: "live", Product: "datatug", PayerID: "payer-one", ActorID: "actor-one",
		SpaceID: "deleted-space", ProjectID: "project-one", CommandID: "command-one", RequestDigest: "request-one",
		LimitsVersion: "limits-one", ProfileVersion: "profile-one", QuotaBasisDigest: "basis-one",
		ProtectedProjectsLimit: 5, ProtectedUsersLimit: 5, QuotaRevision: 1, SubscriptionID: "subscription-one",
		OwnerGeneration: 1, CreatedAt: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC),
	})
	inventory, err := NewDALProtectedProjectInventory(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inventory.CompleteProtectedProjectBasis(context.Background(), "live", "datatug", "payer-one"); !errors.Is(err, ErrProtectedProjectInventory) {
		t.Fatalf("inventory error = %v", err)
	}
}

func TestSharedProjectInventoryPathRejectsMalformedDataTugAncestry(t *testing.T) {
	rootSpace := record.NewKeyWithID("spaces", "space-one")
	nestedSpace := record.NewKeyWithParentAndID(
		record.NewKeyWithID("organizations", "org-one"),
		"spaces",
		"space-one",
	)
	dataTugExtension := record.NewKeyWithParentAndID(rootSpace, "ext", "datatug")
	nestedDataTugExtension := record.NewKeyWithParentAndID(nestedSpace, "ext", "datatug")
	invalidProjectID := record.NewKeyWithParentAndID(dataTugExtension, "projects", "../project")
	nestedRoot := record.NewKeyWithParentAndID(nestedDataTugExtension, "projects", "project-one")
	unrelated := record.NewKeyWithParentAndID(
		record.NewKeyWithParentAndID(rootSpace, "ext", "another-extension"),
		"projects",
		"project-one",
	)

	for _, test := range []struct {
		name      string
		key       *record.Key
		wantOwned bool
		wantExact bool
	}{
		{name: "invalid project identifier under DataTug", key: invalidProjectID, wantOwned: true},
		{name: "nested Space ancestry under DataTug", key: nestedRoot, wantOwned: true},
		{name: "unrelated extension", key: unrelated},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, owned, exact := sharedProjectInventoryPath(test.key, sharedProjectCollection)
			if owned != test.wantOwned || exact != test.wantExact {
				t.Fatalf("ownership = %v, exact = %v; want %v, %v", owned, exact, test.wantOwned, test.wantExact)
			}
		})
	}
}

func TestDALProtectedProjectInventoryRejectsMalformedOwnedRecordsAndSkipsOthers(t *testing.T) {
	for _, test := range []struct {
		name string
		key  *record.Key
	}{
		{
			name: "invalid project identifier",
			key: record.NewKeyWithParentAndID(
				record.NewKeyWithParentAndID(record.NewKeyWithID("spaces", "space-one"), "ext", "datatug"),
				"projects",
				"../project",
			),
		},
		{
			name: "nested Space ancestry",
			key: record.NewKeyWithParentAndID(
				record.NewKeyWithParentAndID(
					record.NewKeyWithParentAndID(record.NewKeyWithID("organizations", "org-one"), "spaces", "space-one"),
					"ext",
					"datatug",
				),
				"projects",
				"project-one",
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := sneatcoretesting.NewMemoryDB()
			seedInventorySpace(t, db, "space-one")
			if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
				project := &models4datatug.Project{Access: models4datatug.AccessProtected}
				return tx.Insert(ctx, record.NewRecordWithData(test.key, project))
			}); err != nil {
				t.Fatal(err)
			}
			inventory, err := NewDALProtectedProjectInventory(db)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inventory.CompleteProtectedProjectBasis(context.Background(), "live", "datatug", "payer-one"); !errors.Is(err, ErrProtectedProjectInventory) {
				t.Fatalf("inventory error = %v", err)
			}
		})
	}

	db := sneatcoretesting.NewMemoryDB()
	seedInventorySpace(t, db, "space-one")
	unrelatedKey := record.NewKeyWithParentAndID(
		record.NewKeyWithParentAndID(record.NewKeyWithID("spaces", "space-one"), "ext", "another-extension"),
		"projects",
		"project-one",
	)
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		project := &models4datatug.Project{Access: models4datatug.AccessProtected}
		return tx.Insert(ctx, record.NewRecordWithData(unrelatedKey, project))
	}); err != nil {
		t.Fatal(err)
	}
	inventory, err := NewDALProtectedProjectInventory(db)
	if err != nil {
		t.Fatal(err)
	}
	if basis, err := inventory.CompleteProtectedProjectBasis(context.Background(), "live", "datatug", "payer-one"); err != nil || basis.Allocated != 0 {
		t.Fatalf("basis = %+v, error = %v; want empty complete inventory", basis, err)
	}
}

func seedInventorySpace(t *testing.T, db dal.DB, id string) {
	t.Helper()
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Insert(ctx, record.NewRecordWithData(record.NewKeyWithID("spaces", id), new(struct{})))
	}); err != nil {
		t.Fatal(err)
	}
}

func seedInventoryProject(t *testing.T, db dal.DB, space, projectID string, protected bool, admission models4datatug.ProjectAdmission) {
	t.Helper()
	projectRecord, project := models4datatug.NewSharedProjectRecord(space, projectID)
	if protected {
		project.Access = models4datatug.AccessProtected
	}
	admissionRecord, admissionValue := models4datatug.NewProjectAdmissionRecord(space, projectID)
	*admissionValue = admission
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := tx.Insert(ctx, projectRecord); err != nil {
			return err
		}
		return tx.Insert(ctx, admissionRecord)
	}); err != nil {
		t.Fatal(err)
	}
}
