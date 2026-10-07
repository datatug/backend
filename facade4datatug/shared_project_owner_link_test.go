// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"errors"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"reflect"
	"testing"
	"time"
)

type ownerContactProbe struct {
	t   *testing.T
	tx  dal.ReadTransaction
	ref contract4linkage.RelationshipEntityRef
	err error
}

func (p *ownerContactProbe) ResolveProjectOwnerContact(_ context.Context, tx dal.ReadTransaction, b SharedProjectCreateBinding) (contract4linkage.RelationshipEntityRef, error) {
	p.t.Helper()
	if _, ok := tx.(dal.WriteSession); ok {
		p.t.Fatal("owner binder can write")
	}
	p.tx = tx
	if b.ActorID != "actor" || b.PayerID != "personal-1" || b.Mode != "live" {
		p.t.Fatal("owner binding lost paid provenance", b)
	}
	return p.ref, p.err
}
func TestPaidSharedCreateOwnerContactAtomicGraphAndImmutableProof(t *testing.T) {
	db, s, o := paidCreateFixture(t)
	probe := &ownerContactProbe{t: t, ref: contactFixtureRef("space", "owner-contact")}
	s.ownerLinks.owner = probe
	// A browser/config mutation cannot replace the snapshotted owner catalog.
	o.ContactLinks.Roles.Roles[0] = "changed"
	o.ContactLinks.Roles.OwnerRole = "changed"
	ref, err := s.Create(context.Background(), sharedCommand())
	if err != nil {
		t.Fatal(err)
	}
	pr, p := models4datatug.NewSharedLinkedProjectRecord(ref.SpaceID, ref.ProjectID)
	if err := db.Get(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	ar, a := models4datatug.NewProjectAdmissionRecord(ref.SpaceID, ref.ProjectID)
	if err := db.Get(context.Background(), ar); err != nil {
		t.Fatal(err)
	}
	receipt := mustReadSharedReceipt(t, db, sharedCommand())
	if a.OwnerContact.Validate() != nil || a.OwnerContact != receipt.OwnerContact || a.OwnerContact.Role != "role-a" || len(p.UserIDs) != 0 {
		t.Fatal("owner provenance", a, receipt, p)
	}
	roles, err := readProjectContactRoles(probe.ref.SpaceID, p.WithRelatedAndIDs, s.ownerLinks.catalog)
	if err != nil || assignedProjectContacts(roles) != 1 || !reflect.DeepEqual(roles[probe.ref], []string{"role-a"}) {
		t.Fatal(roles, err)
	}
	cr, _ := models4datatug.NewProjectContactLinkageRecord(probe.ref)
	contact := new(projectContactFixture)
	if err := db.Get(context.Background(), record.NewRecordWithData(cr.Key(), contact)); err != nil {
		t.Fatal(err)
	}
	edge, err := graphItem(contact.WithRelatedAndIDs, probe.ref.SpaceID, projectFixtureRef(ref.SpaceID, ref.ProjectID))
	if err != nil || !reflect.DeepEqual(rolesOf(edge, false), []string{"role-a"}) || len(rolesOf(edge, true)) != 0 || contact.UserID != "actor" || !contact.Active {
		t.Fatal("wrong reciprocal/identity", contact, err)
	}
	// Replay cannot require fresh owner resolution or rewrite the existing graph.
	probe.err = errors.New("binding directory temporarily unavailable")
	before := cloneFixtureGraph(t, contact.WithRelatedAndIDs)
	if replay, err := s.Create(context.Background(), sharedCommand()); err != nil || replay != ref {
		t.Fatal(replay, err)
	}
	if err := db.Get(context.Background(), record.NewRecordWithData(cr.Key(), contact)); err != nil || !reflect.DeepEqual(before, contact.WithRelatedAndIDs) {
		t.Fatal("replay mutated contact", err)
	}
}
func TestPaidSharedCreateOwnerBindingFailureLeavesNoAllocation(t *testing.T) {
	for _, reason := range []string{"missing-port", "typed-nil", "resolver-error", "foreign-contact", "bad-ref", "missing-contact", "inactive", "unregistered", "foreign-uid", "bad-graph"} {
		t.Run(reason, func(t *testing.T) {
			db, s, _ := paidCreateFixture(t)
			ref := contactFixtureRef("space", "owner-contact")
			r, _ := models4datatug.NewProjectContactLinkageRecord(ref)
			probe := &ownerContactProbe{t: t, ref: ref}
			s.ownerLinks.owner = probe
			switch reason {
			case "missing-port":
				s.ownerLinks = nil
			case "typed-nil":
				var absent *ownerContactProbe
				o := paidFixtureOwnerLinks()
				o.Owner = absent
				if _, err := snapshotProjectOwnerLinks(o); !errors.Is(err, ErrSharedProjectUnavailable) {
					t.Fatal(err)
				}
				return
			case "resolver-error":
				probe.err = ErrSharedProjectUnauthorized
			case "foreign-contact":
				probe.ref.SpaceID = "other-space"
			case "bad-ref":
				probe.ref.ItemRef.Collection = "wrong"
			case "missing-contact":
				if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error { return tx.Delete(ctx, r.Key()) }); err != nil {
					t.Fatal(err)
				}
			case "inactive":
				paidUpdate(t, db, r.Key(), "active", false)
			case "unregistered":
				paidUpdate(t, db, r.Key(), "userID", "")
			case "foreign-uid":
				paidUpdate(t, db, r.Key(), "userID", "other")
			case "bad-graph":
				paidUpdate(t, db, r.Key(), "relatedIDs", nil)
			}
			if got, err := s.Create(context.Background(), sharedCommand()); err == nil || got != (models4datatug.SharedProjectRef{}) {
				t.Fatal(got, err)
			}
			assertSharedAbsent(t, db, "space", "project-1", "command")
			if paidQuota(t, db).Allocated != 0 {
				t.Fatal("binding failed after allocation")
			}
		})
	}
}

type failOwnerContactUpdate struct{ dal.ReadwriteTransaction }

func (t failOwnerContactUpdate) Update(ctx context.Context, key *record.Key, u []update.Update, opts ...dal.Precondition) error {
	if key.Collection() == "contacts" {
		return errors.New("injected reciprocal write failure")
	}
	return t.ReadwriteTransaction.Update(ctx, key, u, opts...)
}
func TestPaidSharedCreateOwnerReciprocalWriteRollback(t *testing.T) {
	db, s, _ := paidCreateFixture(t)
	s.db = sharedFaultDB{DB: db, wrap: func(tx dal.ReadwriteTransaction) dal.ReadwriteTransaction { return failOwnerContactUpdate{tx} }}
	if _, err := s.Create(context.Background(), sharedCommand()); err == nil {
		t.Fatal("contact fault committed")
	}
	assertSharedAbsent(t, db, "space", "project-1", "command")
	if paidQuota(t, db).Allocated != 0 {
		t.Fatal("quota leaked")
	}
	r, g := models4datatug.NewProjectContactLinkageRecord(contactFixtureRef("space", "owner-contact"))
	if err := db.Get(context.Background(), r); err != nil || !reflect.DeepEqual(*g, emptyProjectLinkage()) {
		t.Fatal("contact partial edge", g, err)
	}
	s.db = db
	if _, err := s.Create(context.Background(), sharedCommand()); err != nil {
		t.Fatal(err)
	}
}
func TestPaidSharedCreateReplayOwnerlessOrReboundRefuses(t *testing.T) {
	for _, reason := range []string{"ownerless", "receipt-mismatch", "inactive", "uid", "missing-reciprocal", "missing-project", "wrong-created"} {
		t.Run(reason, func(t *testing.T) {
			db, s, _ := paidCreateFixture(t)
			ref, err := s.Create(context.Background(), sharedCommand())
			if err != nil {
				t.Fatal(err)
			}
			ar, _ := models4datatug.NewProjectAdmissionRecord(ref.SpaceID, ref.ProjectID)
			rr, _ := models4datatug.NewSharedProjectCreateReceiptRecord("space", "command")
			cr, _ := models4datatug.NewProjectContactLinkageRecord(contactFixtureRef("space", "owner-contact"))
			pr, _ := models4datatug.NewSharedLinkedProjectRecord(ref.SpaceID, ref.ProjectID)
			switch reason {
			case "ownerless":
				paidUpdate(t, db, ar.Key(), "ownerContact", nil)
			case "receipt-mismatch":
				p := mustReadSharedReceipt(t, db, sharedCommand()).OwnerContact
				p.Role = "role-b"
				paidUpdate(t, db, rr.Key(), "ownerContact", p)
			case "inactive":
				paidUpdate(t, db, cr.Key(), "active", false)
			case "uid":
				paidUpdate(t, db, cr.Key(), "userID", "foreign")
			case "missing-reciprocal":
				paidUpdate(t, db, cr.Key(), "related", nil)
				paidUpdate(t, db, cr.Key(), "relatedIDs", []string{"-"})
			case "missing-project":
				if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error { return tx.Delete(ctx, pr.Key()) }); err != nil {
					t.Fatal(err)
				}
			case "wrong-created":
				paidUpdate(t, db, pr.Key(), "created", models4datatug.Created{At: sharedTestTime.Add(time.Hour)})
			}
			if _, err := s.Create(context.Background(), sharedCommand()); err == nil {
				t.Fatal("unsafe replay")
			}
			if paidQuota(t, db).Allocated != 1 {
				t.Fatal("replay changed allocation")
			}
		})
	}
}
