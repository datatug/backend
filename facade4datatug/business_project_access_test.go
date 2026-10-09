package facade4datatug

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/coretypes"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

type businessServiceAccessReader func(context.Context, dal.ReadTransaction, string, string, string) (SpaceServiceAccess, error)

func (r businessServiceAccessReader) ReadCurrentSpaceServiceAccess(ctx context.Context, tx dal.ReadTransaction, mode, service, space string) (SpaceServiceAccess, error) {
	return r(ctx, tx, mode, service, space)
}

func validBusinessServiceAccess(at time.Time) SpaceServiceAccess {
	return SpaceServiceAccess{
		Mode: "live", ServiceID: "datatug", PayerSpaceID: "business-space", OwnerFamily: "datatug", AccountKind: "organisation",
		ProductID: "datatug-business-usage", PlanID: "datatug-business-usage-monthly",
		OwnerSubscriptionID: "subscription", OwnerGeneration: 2, OwnerRevision: 3,
		PaidServiceProofID: "reconciled-service", GrantVersion: "business-usage-v1", PaidUntilUTC: at.Add(time.Hour), State: "active",
		UnlimitedProjects: true, UnlimitedContacts: true,
	}
}

func TestBusinessAccessUsesCallerTransactionAndVerifiedSpace(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	called := 0
	reader := businessServiceAccessReader(func(_ context.Context, tx dal.ReadTransaction, mode, service, space string) (SpaceServiceAccess, error) {
		called++
		if tx == nil || mode != "live" || service != "datatug" || space != "business-space" {
			t.Fatalf("wrong current service scope %q %q %q, tx %T", mode, service, space, tx)
		}
		if _, canWrite := tx.(dal.ReadwriteTransaction); canWrite {
			t.Fatal("Paymentus reader acquired transaction writes")
		}
		return validBusinessServiceAccess(at), nil
	})
	v, err := NewBusinessProjectAccessVerifier(reader, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	db := sneatcoretesting.NewMemoryDB()
	err = db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		grant, err := v.ReadCurrent(ctx, tx, "business-space")
		if err != nil || !grant.UnlimitedProjects || !grant.UnlimitedContacts || grant.OwnerGeneration != 2 {
			t.Fatalf("current Business grant %+v, %v", grant, err)
		}
		return nil
	})
	if err != nil || called != 1 {
		t.Fatalf("same transaction read: %v, calls %d", err, called)
	}
}

func TestBusinessAccessRejectsIncompleteAndCrossScopeProof(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	base := validBusinessServiceAccess(at)
	for name, change := range map[string]func(*SpaceServiceAccess){
		"TEST":               func(a *SpaceServiceAccess) { a.Mode = "test" },
		"wrong service":      func(a *SpaceServiceAccess) { a.ServiceID = "sso" },
		"foreign Space":      func(a *SpaceServiceAccess) { a.PayerSpaceID = "other-space" },
		"personal account":   func(a *SpaceServiceAccess) { a.AccountKind = "personal" },
		"old product":        func(a *SpaceServiceAccess) { a.ProductID = "datatug-business" },
		"Pro plan":           func(a *SpaceServiceAccess) { a.PlanID = "datatug-pro-monthly" },
		"missing owner":      func(a *SpaceServiceAccess) { a.OwnerSubscriptionID = "" },
		"missing generation": func(a *SpaceServiceAccess) { a.OwnerGeneration = 0 },
		"missing revision":   func(a *SpaceServiceAccess) { a.OwnerRevision = 0 },
		"missing payment":    func(a *SpaceServiceAccess) { a.PaidServiceProofID = "" },
		"missing grant":      func(a *SpaceServiceAccess) { a.GrantVersion = "" },
		"missing projects":   func(a *SpaceServiceAccess) { a.UnlimitedProjects = false },
		"missing contacts":   func(a *SpaceServiceAccess) { a.UnlimitedContacts = false },
		"pending":            func(a *SpaceServiceAccess) { a.State = "pending" },
		"missing end":        func(a *SpaceServiceAccess) { a.PaidUntilUTC = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			fact := base
			change(&fact)
			v, err := NewBusinessProjectAccessVerifier(businessServiceAccessReader(func(context.Context, dal.ReadTransaction, string, string, string) (SpaceServiceAccess, error) {
				return fact, nil
			}), func() time.Time { return at })
			if err != nil {
				t.Fatal(err)
			}
			err = sneatcoretesting.NewMemoryDB().RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
				got, err := v.ReadCurrent(ctx, tx, "business-space")
				if !errors.Is(err, ErrBusinessServiceUnproved) || got != (SpaceServiceAccess{}) {
					t.Fatalf("bad proof accepted: %+v, %v", got, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBusinessAccessSeparatesConfirmedEndFromUnknown(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	fact := validBusinessServiceAccess(at)
	v, err := NewBusinessProjectAccessVerifier(businessServiceAccessReader(func(context.Context, dal.ReadTransaction, string, string, string) (SpaceServiceAccess, error) {
		return fact, nil
	}), func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	db := sneatcoretesting.NewMemoryDB()
	check := func(want error) {
		t.Helper()
		if err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			got, err := v.ReadCurrent(ctx, tx, "business-space")
			if !errors.Is(err, want) || got != (SpaceServiceAccess{}) {
				t.Fatalf("grant %+v, error %v, want %v", got, err, want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	fact.PaidUntilUTC = at
	check(ErrBusinessServiceEnded)
	fact.PaidUntilUTC = at.Add(time.Hour)
	fact.State = "ended"
	check(ErrBusinessServiceEnded)
	fact.State = "unknown"
	check(ErrBusinessServiceUnproved)
	readerError := errors.New("money ingress unavailable")
	v.reader = businessServiceAccessReader(func(context.Context, dal.ReadTransaction, string, string, string) (SpaceServiceAccess, error) {
		return SpaceServiceAccess{}, readerError
	})
	if err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		_, err := v.ReadCurrent(ctx, tx, "business-space")
		if !errors.Is(err, readerError) {
			t.Fatalf("source error was translated into denial: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBusinessAccessRechecksServerTimeAfterSourceRead(t *testing.T) {
	started := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	paidUntil := started.Add(time.Hour)
	for _, tc := range []struct {
		name string
		end  time.Time
		want error
	}{
		{name: "before expiry", end: paidUntil.Add(-time.Nanosecond)},
		{name: "at expiry", end: paidUntil, want: ErrBusinessServiceEnded},
		{name: "after expiry", end: paidUntil.Add(time.Nanosecond), want: ErrBusinessServiceEnded},
		{name: "missing final time", end: time.Time{}, want: ErrBusinessServiceUnproved},
		{name: "clock moved backwards", end: started.Add(-time.Second), want: ErrBusinessServiceUnproved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := started
			fact := validBusinessServiceAccess(started)
			v, err := NewBusinessProjectAccessVerifier(businessServiceAccessReader(func(context.Context, dal.ReadTransaction, string, string, string) (SpaceServiceAccess, error) {
				current = tc.end
				return fact, nil
			}), func() time.Time { return current })
			if err != nil {
				t.Fatal(err)
			}
			if err := sneatcoretesting.NewMemoryDB().RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
				got, err := v.ReadCurrent(ctx, tx, "business-space")
				if !errors.Is(err, tc.want) {
					t.Fatalf("error = %v, want %v", err, tc.want)
				}
				if tc.want == nil {
					if got != fact {
						t.Fatalf("active grant = %+v, want %+v", got, fact)
					}
				} else if got != (SpaceServiceAccess{}) {
					t.Fatalf("rejected grant leaked: %+v", got)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBusinessAccessRequiresNonNilReaderAndServerTime(t *testing.T) {
	if _, err := NewBusinessProjectAccessVerifier(nil, time.Now); !errors.Is(err, ErrBusinessServiceUnproved) {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	called := 0
	v, err := NewBusinessProjectAccessVerifier(businessServiceAccessReader(func(context.Context, dal.ReadTransaction, string, string, string) (SpaceServiceAccess, error) {
		called++
		return validBusinessServiceAccess(at), nil
	}), func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	for _, space := range []string{"", "../foreign"} {
		if _, err := v.ReadCurrent(context.Background(), nil, space); !errors.Is(err, ErrBusinessServiceUnproved) {
			t.Fatal(err)
		}
	}
	if _, err := v.ReadCurrent(context.Background(), nil, "business-space"); !errors.Is(err, ErrBusinessServiceUnproved) || called != 0 {
		t.Fatalf("missing transaction used source: %v, calls %d", err, called)
	}
	v.now = func() time.Time { return time.Time{} }
	if err := sneatcoretesting.NewMemoryDB().RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		if _, err := v.ReadCurrent(ctx, tx, "business-space"); !errors.Is(err, ErrBusinessServiceUnproved) || called != 0 {
			t.Fatalf("zero server time used source: %v, calls %d", err, called)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProLinkedProjectReaderCannotTreatBusinessAdmissionAsPro(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	owner := models4datatug.ProjectOwnerContactProof{
		Version: 1, Role: "owner", CatalogVersion: "1",
		Contact: contract4linkage.RelationshipEntityRef{
			SpaceID: coretypes.SpaceID("business-space"),
			ItemRef: contract4linkage.ItemRef{ExtID: "contactus", Collection: "contacts", ItemID: "owner-contact"},
		},
	}
	a := models4datatug.ProjectAdmission{
		Version: 2, Mode: "live", Product: "datatug-business-usage", PayerID: "business-space",
		ActorID: "creator", SpaceID: "business-space", ProjectID: "project", CommandID: "operation",
		RequestDigest: "digest", LimitsVersion: "business-v1", ProfileVersion: "business-v1",
		SubscriptionID: "subscription", OwnerGeneration: 1, OwnerRevision: 1,
		QuotaBasisDigest: "complete-inventory", QuotaRevision: 1, ServiceID: "datatug",
		PlanID: "datatug-business-usage-monthly", PaidServiceProofID: "reconciled-service",
		UnlimitedProjects: true, UnlimitedContacts: true, CreatedAt: at, OwnerContact: owner,
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	db := sneatcoretesting.NewMemoryDB()
	r, stored := models4datatug.NewProjectAdmissionRecord(a.SpaceID, a.ProjectID)
	*stored = a
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Insert(ctx, r)
	}); err != nil {
		t.Fatal(err)
	}
	project := &models4datatug.SharedLinkedProject{Project: models4datatug.Project{Created: &models4datatug.Created{At: at}}}
	ref := contract4linkage.RelationshipEntityRef{SpaceID: coretypes.SpaceID(a.SpaceID), ItemRef: contract4linkage.ItemRef{ExtID: "datatug", Collection: "projects", ItemID: a.ProjectID}}
	if err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		_, err := readLinkedProjectAdmission(ctx, tx, ref, project)
		if !errors.Is(err, ErrSharedProjectConflict) {
			t.Fatalf("Pro reader accepted Business admission: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
