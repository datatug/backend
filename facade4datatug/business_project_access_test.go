package facade4datatug

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/coretypes"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

type businessServiceAccessReader func(context.Context, dal.ReadTransaction, contract4paymentus.ServicePurchaseScope) (contract4paymentus.CurrentSpaceServiceAccess, error)

func (businessServiceAccessReader) Mode() contract4paymentus.Mode { return contract4paymentus.ModeLive }

func (r businessServiceAccessReader) ReadCurrentSpaceServiceAccess(ctx context.Context, tx dal.ReadTransaction, scope contract4paymentus.ServicePurchaseScope) (contract4paymentus.CurrentSpaceServiceAccess, error) {
	return r(ctx, tx, scope)
}

type businessTestModeServiceReader struct{}

func (businessTestModeServiceReader) Mode() contract4paymentus.Mode {
	return contract4paymentus.ModeTest
}
func (businessTestModeServiceReader) ReadCurrentSpaceServiceAccess(context.Context, dal.ReadTransaction, contract4paymentus.ServicePurchaseScope) (contract4paymentus.CurrentSpaceServiceAccess, error) {
	return contract4paymentus.CurrentSpaceServiceAccess{}, nil
}

func validPaymentusBusinessAccess(at time.Time) contract4paymentus.CurrentSpaceServiceAccess {
	return contract4paymentus.CurrentSpaceServiceAccess{
		Scope:        contract4paymentus.ServicePurchaseScope{Mode: contract4paymentus.ModeLive, SpaceID: "business-space", ServiceID: BusinessProjectServiceID},
		PayerSpaceID: "business-space", OwnerFamily: BusinessProjectOwnerFamily, AccountKind: BusinessProjectAccountKind,
		ProductID: BusinessProjectProductID, PlanID: BusinessMonthlyPlanID,
		ProviderAccountID: "provider-account", CustomerID: "original-customer", LineageID: "initial-lineage",
		OwnerSubscriptionID: "subscription", PaidServiceProofID: "reconciled-service", OwnerGeneration: 2, OwnerRevision: 3,
		State: contract4paymentus.ServiceCurrentFinancialPaid, PaidFromUTC: at.Add(-time.Hour), PaidThroughUTC: at.Add(time.Hour),
		EffectiveEndUTC: at.Add(time.Hour), ObservedAtUTC: at.Add(-time.Minute), EvidenceValidUntilUTC: at.Add(30 * time.Minute),
	}
}

func newBusinessVerifier(t *testing.T, reader contract4paymentus.CurrentSpaceServiceReader, now func() time.Time) *BusinessProjectAccessVerifier {
	t.Helper()
	v, err := NewBusinessProjectAccessVerifier(reader, BusinessProjectAccessPolicy{GrantVersion: "business-project-v1", Mode: contract4paymentus.ModeLive}, now)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestBusinessAccessUsesCallerTransactionAndVerifiedSpace(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	called := 0
	reader := businessServiceAccessReader(func(_ context.Context, tx dal.ReadTransaction, scope contract4paymentus.ServicePurchaseScope) (contract4paymentus.CurrentSpaceServiceAccess, error) {
		called++
		if tx == nil || scope != (contract4paymentus.ServicePurchaseScope{Mode: contract4paymentus.ModeLive, SpaceID: "business-space", ServiceID: "datatug"}) {
			t.Fatalf("wrong current service scope %+v, tx %T", scope, tx)
		}
		if _, canWrite := tx.(dal.ReadwriteTransaction); canWrite {
			t.Fatal("Paymentus reader acquired transaction writes")
		}
		return validPaymentusBusinessAccess(at), nil
	})
	v := newBusinessVerifier(t, reader, func() time.Time { return at })
	db := sneatcoretesting.NewMemoryDB()
	err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		grant, err := v.ReadCurrent(ctx, tx, "business-space")
		if err != nil || !grant.UnlimitedProjects || !grant.UnlimitedContacts || grant.OwnerGeneration != 2 || grant.PaidUntilUTC != at.Add(30*time.Minute) {
			t.Fatalf("current Business grant %+v, %v", grant, err)
		}
		return nil
	})
	if err != nil || called != 1 {
		t.Fatalf("same transaction read: %v, calls %d", err, called)
	}
}

func TestBusinessAccessVerifierRejectsTestModeReader(t *testing.T) {
	if _, err := NewBusinessProjectAccessVerifier(businessTestModeServiceReader{}, BusinessProjectAccessPolicy{GrantVersion: "business-project-v1", Mode: contract4paymentus.ModeLive}, func() time.Time { return sharedTestTime }); !errors.Is(err, ErrBusinessServiceUnproved) {
		t.Fatalf("TEST current-service reader error = %v, want LIVE-only refusal", err)
	}
}

func TestBusinessAccessRejectsIncompleteAndCrossScopeProof(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	base := validPaymentusBusinessAccess(at)
	for name, change := range map[string]func(*contract4paymentus.CurrentSpaceServiceAccess){
		"TEST":                  func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.Scope.Mode = contract4paymentus.ModeTest },
		"wrong service":         func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.Scope.ServiceID = "sso" },
		"foreign Space":         func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.PayerSpaceID = "other-space" },
		"wrong account kind":    func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.AccountKind = "organisation" },
		"wrong owner family":    func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.OwnerFamily = "other" },
		"old product":           func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.ProductID = "datatug-business" },
		"Pro plan":              func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.PlanID = "datatug-pro-monthly" },
		"missing owner":         func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.OwnerSubscriptionID = "" },
		"missing generation":    func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.OwnerGeneration = 0 },
		"missing revision":      func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.OwnerRevision = 0 },
		"missing payment":       func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.PaidServiceProofID = "" },
		"pending":               func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.State = "pending" },
		"missing paid end":      func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.PaidThroughUTC = time.Time{} },
		"missing effective end": func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.EffectiveEndUTC = time.Time{} },
		"paid period starts in future": func(a *contract4paymentus.CurrentSpaceServiceAccess) {
			a.PaidFromUTC = a.EffectiveEndUTC.Add(time.Hour)
		},
		"missing evidence fence": func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.EvidenceValidUntilUTC = time.Time{} },
		"missing observed at":    func(a *contract4paymentus.CurrentSpaceServiceAccess) { a.ObservedAtUTC = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			fact := base
			change(&fact)
			v := newBusinessVerifier(t, businessServiceAccessReader(func(context.Context, dal.ReadTransaction, contract4paymentus.ServicePurchaseScope) (contract4paymentus.CurrentSpaceServiceAccess, error) {
				return fact, nil
			}), func() time.Time { return at })
			err := sneatcoretesting.NewMemoryDB().RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
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
	fact := validPaymentusBusinessAccess(at)
	v := newBusinessVerifier(t, businessServiceAccessReader(func(context.Context, dal.ReadTransaction, contract4paymentus.ServicePurchaseScope) (contract4paymentus.CurrentSpaceServiceAccess, error) {
		return fact, nil
	}), func() time.Time { return at })
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
	fact.PaidThroughUTC = at
	check(ErrBusinessServiceEnded)
	fact = validPaymentusBusinessAccess(at)
	fact.EffectiveEndUTC = at
	check(ErrBusinessServiceEnded)
	fact = validPaymentusBusinessAccess(at)
	fact.EvidenceValidUntilUTC = at
	check(ErrBusinessServiceEnded)
	fact = validPaymentusBusinessAccess(at)
	fact.State = contract4paymentus.ServiceCurrentFinancialEnded
	check(ErrBusinessServiceEnded)
	fact.State = contract4paymentus.ServiceCurrentFinancialRefunded
	check(ErrBusinessServiceEnded)
	fact.State = "unknown"
	check(ErrBusinessServiceUnproved)
	readerError := errors.New("money ingress unavailable")
	v.reader = businessServiceAccessReader(func(context.Context, dal.ReadTransaction, contract4paymentus.ServicePurchaseScope) (contract4paymentus.CurrentSpaceServiceAccess, error) {
		return contract4paymentus.CurrentSpaceServiceAccess{}, readerError
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
	for _, tc := range []struct {
		name string
		now  time.Time
		want error
	}{
		{name: "before expiry", now: started.Add(20 * time.Minute)},
		{name: "at evidence expiry", now: started.Add(30 * time.Minute), want: ErrBusinessServiceEnded},
		{name: "after paid expiry", now: started.Add(time.Hour), want: ErrBusinessServiceEnded},
		{name: "clock moved backwards", now: started.Add(-time.Second), want: ErrBusinessServiceUnproved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := started
			fact := validPaymentusBusinessAccess(started)
			reads := 0
			v := newBusinessVerifier(t, businessServiceAccessReader(func(context.Context, dal.ReadTransaction, contract4paymentus.ServicePurchaseScope) (contract4paymentus.CurrentSpaceServiceAccess, error) {
				reads++
				current = tc.now
				return fact, nil
			}), func() time.Time { return current })
			if err := sneatcoretesting.NewMemoryDB().RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
				got, err := v.ReadCurrent(ctx, tx, "business-space")
				if !errors.Is(err, tc.want) || (tc.want == nil && (got.State != "active" || got.PaidUntilUTC != started.Add(30*time.Minute))) || (tc.want != nil && got != (SpaceServiceAccess{})) {
					t.Fatalf("grant %+v, error %v, want %v", got, err, tc.want)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if reads != 1 {
				t.Fatalf("reader calls = %d", reads)
			}
		})
	}
}

func TestBusinessAccessRequiresReaderPolicyAndTransaction(t *testing.T) {
	if _, err := NewBusinessProjectAccessVerifier(nil, BusinessProjectAccessPolicy{GrantVersion: "v1", Mode: contract4paymentus.ModeLive}, time.Now); !errors.Is(err, ErrBusinessServiceUnproved) {
		t.Fatal(err)
	}
	if _, err := NewBusinessProjectAccessVerifier(businessServiceAccessReader(func(context.Context, dal.ReadTransaction, contract4paymentus.ServicePurchaseScope) (contract4paymentus.CurrentSpaceServiceAccess, error) {
		return contract4paymentus.CurrentSpaceServiceAccess{}, nil
	}), BusinessProjectAccessPolicy{}, time.Now); !errors.Is(err, ErrBusinessServiceUnproved) {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	called := 0
	v := newBusinessVerifier(t, businessServiceAccessReader(func(context.Context, dal.ReadTransaction, contract4paymentus.ServicePurchaseScope) (contract4paymentus.CurrentSpaceServiceAccess, error) {
		called++
		return validPaymentusBusinessAccess(at), nil
	}), func() time.Time { return at })
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
		Version: 2, Mode: "live", Product: BusinessProjectProductID, PayerID: "business-space",
		ActorID: "creator", SpaceID: "business-space", ProjectID: "project", CommandID: "operation",
		RequestDigest: "digest", LimitsVersion: "business-v1", ProfileVersion: "business-v1",
		SubscriptionID: "subscription", OwnerGeneration: 1, OwnerRevision: 1,
		ServiceID: BusinessProjectServiceID, PlanID: BusinessMonthlyPlanID, PaidServiceProofID: "reconciled-service",
		UnlimitedProjects: true, UnlimitedContacts: true, CreatedAt: at, OwnerContact: owner,
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
}
