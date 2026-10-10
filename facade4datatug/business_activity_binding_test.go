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

type businessActivityContactReaderFunc func(context.Context, dal.ReadTransaction, contract4linkage.RelationshipEntityRef) (ProjectContactState, error)

func (f businessActivityContactReaderFunc) ReadCurrentProjectContact(ctx context.Context, tx dal.ReadTransaction, ref contract4linkage.RelationshipEntityRef) (ProjectContactState, error) {
	return f(ctx, tx, ref)
}

type malformedBusinessActivityGraphTx struct{ dal.ReadTransaction }

func (tx malformedBusinessActivityGraphTx) Get(ctx context.Context, r record.Record) error {
	if err := tx.ReadTransaction.Get(ctx, r); err != nil {
		return err
	}
	if graph, ok := r.Data().(*contract4linkage.WithRelatedAndIDs); ok {
		graph.RelatedIDs = []string{""}
	}
	return nil
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
	periods := &businessUsagePeriodStateReader{states: make(map[contract4paymentus.UsagePeriodRef]contract4paymentus.UsagePeriodState)}
	reader, err := NewNativeBusinessActivityBindingReader(BusinessActivityBindingReaderOptions{
		Access:  access,
		Periods: periods,
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
		if _, err := project.ApplyDirectedRelationshipAndID(sharedTestTime, "linker", projectRef.SpaceID, projectCommand); err != nil {
			return err
		}
		if _, err := contactValue.ApplyDirectedRelationshipAndID(sharedTestTime, "linker", contactRef.SpaceID, contactCommand); err != nil {
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

func TestBusinessActivityBindingKeepsNativeFrozenPeriodAcrossPricingRevision(t *testing.T) {
	db, projectRef, _, reader, _, anchor := newBusinessActivityFixture(t, "external-uid")
	at := sharedTestTime.Add(10 * time.Minute)
	usageScope := contract4paymentus.UsageScope{
		Mode: contract4paymentus.ModeLive, SpaceID: string(projectRef.SpaceID), ProductID: BusinessProjectProductID,
		PayerID: string(projectRef.SpaceID), ServiceID: BusinessProjectServiceID,
	}
	current := contract4paymentus.DataTugBusinessUsagePricing()
	currentPeriod, err := contract4paymentus.UsagePeriodForAnchor(usageScope, current, anchor.AnchorUTC, at)
	if err != nil {
		t.Fatal(err)
	}
	frozen := current
	frozen.Version = "2026-11-10"
	frozen.ConfigID = "datatug-business-usage-next"
	frozen.MonthlyOverageUnitMinor += 100
	frozenPeriod, err := contract4paymentus.UsagePeriodForAnchor(usageScope, frozen, anchor.AnchorUTC, at)
	if err != nil {
		t.Fatal(err)
	}
	if !sameBusinessUsageWindow(currentPeriod, frozenPeriod) {
		t.Fatal("test snapshots changed the monthly identity instead of only pricing")
	}
	periods := reader.periods.(*businessUsagePeriodStateReader)
	periods.mu.Lock()
	periods.states[frozenPeriod.Ref] = contract4paymentus.UsagePeriodState{Snapshot: frozenPeriod, DistinctMAU: 1}
	periods.mu.Unlock()
	var got BusinessActivityBinding
	if err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		var err error
		got, err = reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, at)
		return err
	}); err != nil {
		t.Fatalf("binding rejected the native period's frozen config after revision: %v", err)
	}
	if got.Period != frozenPeriod.Ref || got.PeriodStartUTC != frozenPeriod.StartUTC || got.PeriodEndUTC != frozenPeriod.EndUTC {
		t.Fatalf("binding diverged from the native frozen period: %+v / %+v", got, frozenPeriod)
	}
	periods.mu.Lock()
	state := periods.states[frozenPeriod.Ref]
	state.Closed = true
	periods.states[frozenPeriod.Ref] = state
	periods.mu.Unlock()
	if err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, at)
		return err
	}); err == nil {
		t.Fatal("binding admitted new activity to a natively closed period")
	}
	periods.mu.Lock()
	periods.err = errors.New("native period read failed")
	periods.mu.Unlock()
	if err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, at)
		return err
	}); err == nil {
		t.Fatal("binding treated an unreadable native period as an unopened period")
	}
}

func TestBusinessActivityBindingReaderRequiresEveryAuthority(t *testing.T) {
	_, _, _, reader, _, _ := newBusinessActivityFixture(t, "external-uid")
	base := BusinessActivityBindingReaderOptions{
		Access: reader.access, InitialStarts: reader.initialStarts, Periods: reader.periods, Contacts: reader.contacts, Actors: reader.actors,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*BusinessActivityBindingReaderOptions)
	}{
		{"current service reader", func(o *BusinessActivityBindingReaderOptions) { o.Access = nil }},
		{"initial service start reader", func(o *BusinessActivityBindingReaderOptions) { o.InitialStarts = nil }},
		{"native usage period reader", func(o *BusinessActivityBindingReaderOptions) { o.Periods = nil }},
		{"current contact reader", func(o *BusinessActivityBindingReaderOptions) { o.Contacts = nil }},
		{"authenticated actor verifier", func(o *BusinessActivityBindingReaderOptions) { o.Actors = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := base
			tc.mutate(&options)
			if reader, err := NewNativeBusinessActivityBindingReader(options); reader != nil || !errors.Is(err, ErrBusinessActivityBindingUnavailable) {
				t.Fatalf("reader = %v, error = %v; want unavailable", reader, err)
			}
		})
	}
}

func TestBusinessActivityBindingRequiresCurrentProjectAndPaidInterval(t *testing.T) {
	t.Run("missing project", func(t *testing.T) {
		_, projectRef, _, reader, _, _ := newBusinessActivityFixture(t, "external-uid")
		emptyDB := sneatcoretesting.NewMemoryDB()
		err := emptyDB.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), "missing-project", sharedTestTime)
			return err
		})
		if err == nil {
			t.Fatal("missing project was accepted")
		}
	})

	t.Run("non-protected project", func(t *testing.T) {
		db, projectRef, _, reader, _, _ := newBusinessActivityFixture(t, "external-uid")
		projectRecord, project := models4datatug.NewSharedLinkedProjectRecord(string(projectRef.SpaceID), projectRef.ItemRef.ItemID)
		if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			if err := tx.Get(ctx, projectRecord); err != nil {
				return err
			}
			project.Access = "public"
			return tx.Set(ctx, projectRecord)
		}); err != nil {
			t.Fatal(err)
		}
		err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, sharedTestTime)
			return err
		})
		if !errors.Is(err, ErrQueryActivityUnauthorized) {
			t.Fatalf("non-protected project error = %v, want unauthorized", err)
		}
	})

	t.Run("missing immutable admission", func(t *testing.T) {
		db, projectRef, _, reader, _, _ := newBusinessActivityFixture(t, "external-uid")
		admissionRecord, _ := models4datatug.NewProjectAdmissionRecord(string(projectRef.SpaceID), projectRef.ItemRef.ItemID)
		if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			return tx.Delete(ctx, admissionRecord.Key())
		}); err != nil {
			t.Fatal(err)
		}
		err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, sharedTestTime)
			return err
		})
		if err == nil {
			t.Fatal("missing immutable admission was accepted")
		}
	})

	t.Run("paid evidence expired at occurrence time", func(t *testing.T) {
		db, projectRef, _, reader, _, _ := newBusinessActivityFixture(t, "external-uid")
		at := sharedTestTime.Add(40 * time.Minute)
		err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, at)
			return err
		})
		if !errors.Is(err, ErrBusinessServiceUnproved) {
			t.Fatalf("binding after paid evidence expiry = %v, want unproved", err)
		}
	})
}

func TestBusinessActivityBindingFailsClosedOnAuthorityReadErrors(t *testing.T) {
	t.Run("contact lookup error", func(t *testing.T) {
		db, projectRef, _, reader, _, _ := newBusinessActivityFixture(t, "external-uid")
		reader.contacts = businessActivityContactReaderFunc(func(context.Context, dal.ReadTransaction, contract4linkage.RelationshipEntityRef) (ProjectContactState, error) {
			return ProjectContactState{}, errors.New("contact lookup failed")
		})
		err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, sharedTestTime)
			return err
		})
		if !errors.Is(err, ErrQueryActivityUnavailable) {
			t.Fatalf("contact lookup error = %v, want unavailable", err)
		}
	})

	t.Run("mismatched contact reference", func(t *testing.T) {
		db, projectRef, _, reader, _, _ := newBusinessActivityFixture(t, "external-uid")
		reader.contacts = businessActivityContactReaderFunc(func(_ context.Context, _ dal.ReadTransaction, ref contract4linkage.RelationshipEntityRef) (ProjectContactState, error) {
			ref.ItemRef.ItemID = "different-contact"
			return ProjectContactState{Ref: ref, Exists: true, Active: true, UserID: "external-uid"}, nil
		})
		err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, sharedTestTime)
			return err
		})
		if !errors.Is(err, ErrSharedProjectConflict) {
			t.Fatalf("mismatched contact reference error = %v, want conflict", err)
		}
	})

	t.Run("initial start read error", func(t *testing.T) {
		db, projectRef, _, reader, _, _ := newBusinessActivityFixture(t, "external-uid")
		want := errors.New("initial service proof unavailable")
		reader.initialStarts = initialServiceStartReaderFunc(func(context.Context, dal.ReadTransaction, contract4paymentus.ServicePurchaseScope) (contract4paymentus.ServiceInitialServiceStart, error) {
			return contract4paymentus.ServiceInitialServiceStart{}, want
		})
		err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, sharedTestTime)
			return err
		})
		if !errors.Is(err, want) {
			t.Fatalf("initial start read error = %v, want original failure", err)
		}
	})
}

func TestBusinessActivityBindingRejectsInvalidOccurrenceAndPreAnchorTime(t *testing.T) {
	t.Run("non-UTC occurrence time", func(t *testing.T) {
		db, projectRef, _, reader, _, _ := newBusinessActivityFixture(t, "external-uid")
		localTime := time.Date(2026, 10, 10, 12, 0, 0, 0, time.FixedZone("UTC", 0))
		err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, localTime)
			return err
		})
		if !errors.Is(err, ErrQueryActivityInvalid) {
			t.Fatalf("non-UTC occurrence error = %v, want invalid", err)
		}
	})

	t.Run("occurrence before original service start", func(t *testing.T) {
		db, projectRef, _, reader, _, anchor := newBusinessActivityFixture(t, "external-uid")
		at := anchor.AnchorUTC.Add(-time.Minute)
		err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, at)
			return err
		})
		if err == nil {
			t.Fatal("activity before immutable service start was accepted")
		}
	})
}

func TestBusinessActivityBindingRejectsAmbiguousAndMalformedContactEvidence(t *testing.T) {
	t.Run("two contacts for the authenticated UID", func(t *testing.T) {
		db, projectRef, _, reader, _, _ := newBusinessActivityFixture(t, "external-uid")
		second := contactFixtureRef(string(projectRef.SpaceID), "second-external-contact")
		seedProjectContact(t, db, second, "external-uid", true)
		linkActivityContact(t, db, projectRef, second, string(models4datatug.ProjectRoleViewer))
		err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, sharedTestTime)
			return err
		})
		if !errors.Is(err, ErrQueryActivityUnauthorized) {
			t.Fatalf("ambiguous contact evidence error = %v, want unauthorized", err)
		}
	})

	t.Run("empty assignment is not query permission", func(t *testing.T) {
		db, projectRef, _, reader, _, _ := newBusinessActivityFixture(t, "external-uid")
		projectRecord, project := models4datatug.NewSharedLinkedProjectRecord(string(projectRef.SpaceID), projectRef.ItemRef.ItemID)
		if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
			if err := tx.Get(ctx, projectRecord); err != nil {
				return err
			}
			for _, collections := range project.Related {
				for _, items := range collections {
					for _, item := range items {
						if item != nil {
							item.RolesOfItem = nil
						}
					}
				}
			}
			return tx.Set(ctx, projectRecord)
		}); err != nil {
			t.Fatal(err)
		}
		err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			_, err := reader.ReadCurrentBusinessActivityBinding(ctx, tx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, sharedTestTime)
			return err
		})
		if !errors.Is(err, ErrQueryActivityUnauthorized) {
			t.Fatalf("empty role assignment error = %v, want unauthorized", err)
		}
	})

	t.Run("corrupt linkage index", func(t *testing.T) {
		db, projectRef, _, reader, _, _ := newBusinessActivityFixture(t, "external-uid")
		err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
			corruptingTx := malformedBusinessActivityGraphTx{ReadTransaction: tx}
			_, err := reader.ReadCurrentBusinessActivityBinding(ctx, corruptingTx, "external-uid", string(projectRef.SpaceID), projectRef.ItemRef.ItemID, sharedTestTime)
			return err
		})
		if !errors.Is(err, ErrQueryActivityUnavailable) {
			t.Fatalf("corrupt linkage index error = %v, want unavailable", err)
		}
	})
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
