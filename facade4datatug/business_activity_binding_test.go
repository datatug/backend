package facade4datatug

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

type activityActorVerifierFunc func(context.Context, dal.ReadTransaction, string) error

func (f activityActorVerifierFunc) VerifyBusinessActivityActor(ctx context.Context, tx dal.ReadTransaction, actor string) error {
	return f(ctx, tx, actor)
}

type initialServiceStartReaderFunc func(context.Context, dal.ReadTransaction, contract4paymentus.ServicePurchaseScope) (contract4paymentus.ServiceInitialServiceStart, error)

func (f initialServiceStartReaderFunc) ReadInitialServiceStart(ctx context.Context, tx dal.ReadTransaction, scope contract4paymentus.ServicePurchaseScope) (contract4paymentus.ServiceInitialServiceStart, error) {
	return f(ctx, tx, scope)
}

func newBusinessActivityFixture(t *testing.T, actorID string) (dal.DB, contract4linkage.RelationshipEntityRef, contract4linkage.RelationshipEntityRef, *NativeBusinessActivityBindingReader, *businessCurrentServiceReader, *contract4paymentus.ServiceInitialServiceStart) {
	t.Helper()
	db := sneatcoretesting.NewMemoryDB()
	seedPaidOwnerContact(t, db, "space")
	serviceAccess := validPaymentusBusinessAccess(sharedTestTime)
	serviceAccess.Scope.SpaceID = "space"
	serviceAccess.PayerSpaceID = "space"
	currentReader := &businessCurrentServiceReader{access: serviceAccess}
	contactLinks := &PaidProjectOwnerLinksOptions{
		Roles: ProjectRoleCatalog{Version: "business-query-roles-v1", Roles: []string{"viewer", "contributor", "owner"}, OwnerRole: "owner"},
		Owner: projectContactFixturePort{}, Contacts: projectContactFixturePort{},
	}
	service, err := NewBusinessSharedProjectService(db, &sharedCounterIDs{}, &sharedAuthority{}, func() time.Time { return sharedTestTime }, BusinessSharedProjectOptions{
		AccessPolicy:  BusinessProjectAccessPolicy{GrantVersion: "business-project-v1"},
		ServiceReader: currentReader,
		ContactLinks:  contactLinks,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := sharedCommand()
	command.BillingIntent = BillingIntentSpaceBusiness
	created, err := service.Create(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	projectRef := projectFixtureRef(created.SpaceID, created.ProjectID)
	contactRef := contactFixtureRef(created.SpaceID, "external-contact")
	seedProjectContact(t, db, contactRef, actorID, true)
	linkActivityContact(t, db, projectRef, contactRef, string(models4datatug.ProjectRoleViewer))

	scope := contract4paymentus.ServicePurchaseScope{
		Mode: contract4paymentus.ModeLive, SpaceID: created.SpaceID, ServiceID: BusinessProjectServiceID,
	}
	anchor := &contract4paymentus.ServiceInitialServiceStart{
		Version: 1, Scope: scope, PayerID: created.SpaceID, InitiatingActorID: "original-buyer",
		LineageID: "initial-lineage", QuoteID: "initial-quote", QuoteFingerprint: "initial-fingerprint",
		ProviderAccountID: "provider-account", CustomerID: "original-customer", SessionID: "original-session",
		SubscriptionID: "original-subscription", InvoiceID: "initial-invoice", InvoiceLineID: "initial-line",
		PaymentID: "initial-payment", PriceID: "business-price", Currency: "eur", PaymentType: "new_subscription",
		ExecutionGeneration: 1, CashMinor: 9900, CashConfirmed: true, RefundsKnown: true, NoRefunds: true, DisputeResolved: true,
		AnchorUTC:           time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC),
		InitialPeriodEndUTC: time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC),
		ObservedAtUTC:       time.Date(2026, 10, 1, 9, 31, 0, 0, time.UTC),
	}
	access := newBusinessVerifier(t, currentReader, func() time.Time { return sharedTestTime })
	reader, err := NewNativeBusinessActivityBindingReader(BusinessActivityBindingReaderOptions{
		Access: access,
		InitialStarts: initialServiceStartReaderFunc(func(_ context.Context, tx dal.ReadTransaction, got contract4paymentus.ServicePurchaseScope) (contract4paymentus.ServiceInitialServiceStart, error) {
			if _, writable := tx.(dal.ReadwriteTransaction); writable {
				t.Fatal("initial-service reader received a write transaction")
			}
			if got != scope {
				t.Fatalf("initial-service scope = %+v, want %+v", got, scope)
			}
			return *anchor, nil
		}),
		Contacts: projectContactFixturePort{},
		Actors: activityActorVerifierFunc(func(_ context.Context, tx dal.ReadTransaction, got string) error {
			if _, writable := tx.(dal.ReadwriteTransaction); writable {
				t.Fatal("actor verifier received a write transaction")
			}
			if got != actorID {
				return ErrQueryActivityUnauthorized
			}
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, projectRef, contactRef, reader, currentReader, anchor
}

func linkActivityContact(t *testing.T, db dal.DB, projectRef, contactRef contract4linkage.RelationshipEntityRef, role string) {
	t.Helper()
	projectRecord, project := models4datatug.NewSharedLinkedProjectRecord(string(projectRef.SpaceID), projectRef.ItemRef.ItemID)
	contactKeyRecord, _ := models4datatug.NewProjectContactLinkageRecord(contactRef)
	contactValue := new(projectContactFixture)
	contactRecord := record.NewRecordWithData(contactKeyRecord.Key(), contactValue)
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := tx.Get(ctx, projectRecord); err != nil {
			return err
		}
		if err := tx.Get(ctx, contactRecord); err != nil {
			return err
		}
		projectCommand := contract4linkage.RelationshipItemRolesCommand{
			ItemRef: contactRef.ItemRef, Add: &contract4linkage.RolesCommand{RolesOfItem: []string{role}},
		}
		contactCommand := contract4linkage.RelationshipItemRolesCommand{
			ItemRef: projectRef.ItemRef, Add: &contract4linkage.RolesCommand{RolesToItem: []string{role}},
		}
		if _, err := project.WithRelatedAndIDs.ApplyDirectedRelationshipAndID(sharedTestTime, "linker", projectRef.SpaceID, projectCommand); err != nil {
			return err
		}
		if _, err := contactValue.WithRelatedAndIDs.ApplyDirectedRelationshipAndID(sharedTestTime, "linker", contactRef.SpaceID, contactCommand); err != nil {
			return err
		}
		if err := tx.Set(ctx, projectRecord); err != nil {
			return err
		}
		return tx.Set(ctx, contactRecord)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBusinessActivityBindingUsesExternalProjectContactAndOriginalStart(t *testing.T) {
	db, projectRef, _, reader, current, anchor := newBusinessActivityFixture(t, "external-uid")
	current.access.OwnerSubscriptionID = "replacement-subscription"
	current.access.OwnerGeneration = 5
	current.access.OwnerRevision = 1
	current.access.PaidServiceProofID = "renewal-paid-proof"
	at := sharedTestTime.Add(10 * time.Minute)
	var got BusinessActivityBinding
	if err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		var err error
		got, err = reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, at)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wantScope := contract4paymentus.UsageScope{
		Mode: contract4paymentus.ModeLive, SpaceID: string(projectRef.SpaceID), ProductID: BusinessProjectProductID,
		PayerID: string(projectRef.SpaceID), ServiceID: BusinessProjectServiceID,
	}
	wantPeriod, err := contract4paymentus.UsagePeriodForAnchor(wantScope, contract4paymentus.DataTugBusinessUsagePricing(), anchor.AnchorUTC, at)
	if err != nil {
		t.Fatal(err)
	}
	if !got.QueryUseAllowed || got.QueryUseProofID == "" || got.PaidBindingProofID != "renewal-paid-proof" ||
		got.Period != wantPeriod.Ref || got.PeriodStartUTC != wantPeriod.StartUTC || got.PeriodEndUTC != wantPeriod.EndUTC ||
		got.PaidUntilUTC != current.access.EvidenceValidUntilUTC {
		t.Fatalf("Business activity binding = %+v, expected original anchor %s and current paid proof", got, anchor.AnchorUTC)
	}
}

func TestBusinessActivityBindingRejectsUnverifiedOrBrokenProjectAccess(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, db dal.DB, ref contract4linkage.RelationshipEntityRef, contact contract4linkage.RelationshipEntityRef, current *businessCurrentServiceReader, anchor *contract4paymentus.ServiceInitialServiceStart)
		want  error
	}{
		{
			name: "inactive assigned contact",
			setup: func(t *testing.T, db dal.DB, _ contract4linkage.RelationshipEntityRef, contact contract4linkage.RelationshipEntityRef, _ *businessCurrentServiceReader, _ *contract4paymentus.ServiceInitialServiceStart) {
				r, _ := models4datatug.NewProjectContactLinkageRecord(contact)
				value := &projectContactFixture{WithRelatedAndIDs: emptyProjectLinkage(), UserID: "external-uid", Active: true}
				contactRecord := record.NewRecordWithData(r.Key(), value)
				if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
					if err := tx.Get(ctx, contactRecord); err != nil {
						return err
					}
					value.Active = false
					return tx.Set(ctx, contactRecord)
				}); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrQueryActivityUnauthorized,
		},
		{
			name: "unapproved role",
			setup: func(t *testing.T, db dal.DB, ref contract4linkage.RelationshipEntityRef, contact contract4linkage.RelationshipEntityRef, _ *businessCurrentServiceReader, _ *contract4paymentus.ServiceInitialServiceStart) {
				linkActivityContact(t, db, ref, contact, "unapproved-role")
			},
			want: ErrSharedProjectUnauthorized,
		},
		{
			name: "missing current grant",
			setup: func(_ *testing.T, _ dal.DB, _ contract4linkage.RelationshipEntityRef, _ contract4linkage.RelationshipEntityRef, current *businessCurrentServiceReader, _ *contract4paymentus.ServiceInitialServiceStart) {
				current.err = ErrBusinessServiceUnproved
			},
			want: ErrBusinessServiceUnproved,
		},
		{
			name: "missing original anchor",
			setup: func(_ *testing.T, _ dal.DB, _ contract4linkage.RelationshipEntityRef, _ contract4linkage.RelationshipEntityRef, _ *businessCurrentServiceReader, anchor *contract4paymentus.ServiceInitialServiceStart) {
				*anchor = contract4paymentus.ServiceInitialServiceStart{}
			},
			want: ErrBusinessServiceUnproved,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, projectRef, contactRef, reader, current, anchor := newBusinessActivityFixture(t, "external-uid")
			tc.setup(t, db, projectRef, contactRef, current, anchor)
			err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
				_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, sharedTestTime)
				return err
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("reader error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestBusinessActivityBindingRejectsNonReciprocalProjectGraph(t *testing.T) {
	db, projectRef, contactRef, reader, _, _ := newBusinessActivityFixture(t, "external-uid")
	r, _ := models4datatug.NewProjectContactLinkageRecord(contactRef)
	value := new(projectContactFixture)
	contactRecord := record.NewRecordWithData(r.Key(), value)
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if err := tx.Get(ctx, contactRecord); err != nil {
			return err
		}
		value.Related = nil
		value.RelatedIDs = []string{contract4linkage.NoRelatedID}
		return tx.Set(ctx, contactRecord)
	}); err != nil {
		t.Fatal(err)
	}
	err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, sharedTestTime)
		return err
	})
	if !errors.Is(err, ErrSharedProjectConflict) {
		t.Fatalf("broken reciprocal link error = %v, want conflict", err)
	}
}

func TestBusinessActivityBindingRequiresVerifiedActorIdentity(t *testing.T) {
	db, projectRef, _, reader, _, _ := newBusinessActivityFixture(t, "external-uid")
	err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "different-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, sharedTestTime)
		return err
	})
	if !errors.Is(err, ErrQueryActivityUnauthorized) {
		t.Fatalf("wrong verified actor error = %v, want unauthorized", err)
	}
}

func TestBusinessActivityQueryRolePermissionMustBeExplicit(t *testing.T) {
	if hasQueryUseRole([]string{"reader", "runner"}, map[string]bool{"reader": false, "runner": true}) != true ||
		hasQueryUseRole([]string{"reader"}, map[string]bool{"reader": false}) ||
		hasQueryUseRole([]string{"unknown"}, map[string]bool{"reader": true}) {
		t.Fatal("query-use role resolution did not require an explicit permitted role")
	}
	if !slices.Equal([]string{"runner"}, queryUseRoles([]string{"reader", "runner"}, map[string]bool{"reader": false, "runner": true})) {
		t.Fatal("query-use permission projection returned unexpected roles")
	}
}
