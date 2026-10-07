// Copyright 2026 Sneat.co
package facade4datatug

import (
	"context"
	"errors"
	"fmt"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type projectRoleFixtureAuthority struct {
	managerCalls, targetCalls int
	managerErr, targetErr     error
	requests                  []ProjectRoleMutationAuthorization
	expectedTX                dal.ReadTransaction
	t                         *testing.T
}

func (p *projectRoleFixtureAuthority) check(tx dal.ReadTransaction) {
	p.t.Helper()
	if tx != p.expectedTX {
		p.t.Fatal("role authority escaped caller transaction")
	}
	if _, ok := tx.(dal.WriteSession); ok {
		p.t.Fatal("role port can write")
	}
}
func (p *projectRoleFixtureAuthority) AuthorizeProjectRoleMutation(_ context.Context, tx dal.ReadTransaction, r ProjectRoleMutationAuthorization) error {
	p.check(tx)
	p.managerCalls++
	p.requests = append(p.requests, cloneProjectRoleAuthorization(r))
	// Attempting to mutate port evidence must not change Core snapshots/target evidence.
	if len(r.ActorContacts) > 0 {
		r.ActorContacts[0].Roles[0] = "mutated-port-evidence"
	}
	if len(r.Changes) > 0 && len(r.Changes[0].Proposed) > 0 {
		r.Changes[0].Proposed[0] = "mutated-port-change"
	}
	return p.managerErr
}
func (p *projectRoleFixtureAuthority) AuthorizeProjectContactRoleChange(_ context.Context, tx dal.ReadTransaction, r ProjectRoleMutationAuthorization, c ProjectContactRoleChange) error {
	p.check(tx)
	p.targetCalls++
	for _, role := range c.Proposed {
		if role == "mutated-port-change" {
			p.t.Fatal("manager alias reached target")
		}
	}
	return p.targetErr
}

type linkagePolicyFixture struct {
	t         *testing.T
	db        dal.DB
	service   *SharedProjectService
	policy    *PaidProjectLinkagePolicy
	authority *projectRoleFixtureAuthority
	project   contract4linkage.RelationshipEntityRef
}

func newLinkagePolicyFixture(t *testing.T) *linkagePolicyFixture {
	t.Helper()
	db, s, o := paidCreateFixture(t)
	ref, err := s.Create(context.Background(), sharedCommand())
	if err != nil {
		t.Fatal(err)
	}
	a := &projectRoleFixtureAuthority{t: t}
	p, err := NewPaidProjectLinkagePolicy(PaidProjectLinkageOptions{Paid: o, Roles: paidFixtureOwnerLinks().Roles, Contacts: projectContactFixturePort{}, Manager: a, Targets: a, Now: func() time.Time { return sharedTestTime }})
	if err != nil {
		t.Fatal(err)
	}
	return &linkagePolicyFixture{t: t, db: db, service: s, policy: p, authority: a, project: projectFixtureRef(ref.SpaceID, ref.ProjectID)}
}
func cloneFixtureGraph(t *testing.T, g contract4linkage.WithRelatedAndIDs) contract4linkage.WithRelatedAndIDs {
	t.Helper()
	c, err := cloneProjectLinkage(g)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func fixtureGraphRecord(ref contract4linkage.RelationshipEntityRef) (record.Record, *contract4linkage.WithRelatedAndIDs) {
	if isProjectRelationshipRef(ref) {
		r, p := models4datatug.NewSharedLinkedProjectRecord(string(ref.SpaceID), ref.ItemRef.ItemID)
		return r, &p.WithRelatedAndIDs
	}
	return models4datatug.NewProjectContactLinkageRecord(ref)
}

// Build the same complete cumulative reciprocal graph Core exposes, using its
// published directed DTO method. Direction changes only command/source framing.
func (f *linkagePolicyFixture) batch(ctx context.Context, tx dal.ReadTransaction, id string, add, remove []string, contactSource bool) (contract4linkage.RelationshipMutationBatch, error) {
	contact := contactFixtureRef(string(f.project.SpaceID), id)
	pr, pg := fixtureGraphRecord(f.project)
	cr, cg := fixtureGraphRecord(contact)
	if err := tx.Get(ctx, pr); err != nil {
		return contract4linkage.RelationshipMutationBatch{}, err
	}
	if err := tx.Get(ctx, cr); err != nil {
		return contract4linkage.RelationshipMutationBatch{}, err
	}
	pn, cn := cloneFixtureGraph(f.t, *pg), cloneFixtureGraph(f.t, *cg)
	pc := contract4linkage.RelationshipItemRolesCommand{ItemRef: contact.ItemRef}
	cc := contract4linkage.RelationshipItemRolesCommand{ItemRef: f.project.ItemRef}
	if add != nil {
		pc.Add = &contract4linkage.RolesCommand{RolesOfItem: add}
		cc.Add = &contract4linkage.RolesCommand{RolesToItem: add}
	}
	if remove != nil {
		pc.Remove = &contract4linkage.RolesCommand{RolesOfItem: remove}
		cc.Remove = &contract4linkage.RolesCommand{RolesToItem: remove}
	}
	if _, err := pn.ApplyDirectedRelationshipAndID(sharedTestTime, "actor", f.project.SpaceID, pc); err != nil {
		return contract4linkage.RelationshipMutationBatch{}, err
	}
	if _, err := cn.ApplyDirectedRelationshipAndID(sharedTestTime, "actor", contact.SpaceID, cc); err != nil {
		return contract4linkage.RelationshipMutationBatch{}, err
	}
	b := contract4linkage.RelationshipMutationBatch{ActorUserID: "actor", ObservedAt: sharedTestTime, Source: f.project, Commands: []contract4linkage.RelationshipItemRolesCommand{pc}, Entities: []contract4linkage.RelationshipEntityMutation{{Ref: f.project, Before: *pg, Proposed: pn}, {Ref: contact, Before: *cg, Proposed: cn}}}
	if contactSource {
		b.Source = contact
		b.Commands = []contract4linkage.RelationshipItemRolesCommand{cc}
	}
	return b, nil
}
func writeFixtureBatch(ctx context.Context, tx dal.ReadwriteTransaction, b contract4linkage.RelationshipMutationBatch) error {
	for _, e := range b.Entities {
		r, _ := fixtureGraphRecord(e.Ref)
		if err := tx.Update(ctx, r.Key(), []update.Update{update.ByFieldPath([]string{"related"}, e.Proposed.Related), update.ByFieldPath([]string{"relatedIDs"}, e.Proposed.RelatedIDs)}); err != nil {
			return err
		}
	}
	return nil
}
func (f *linkagePolicyFixture) change(id string, add, remove []string, contactSource, write bool, alter func(*contract4linkage.RelationshipMutationBatch)) error {
	return f.db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		read := sharedProjectReadTransaction{tx}
		f.authority.expectedTX = sharedProjectReadTransaction{read}
		b, err := f.batch(ctx, read, id, add, remove, contactSource)
		if err != nil {
			return err
		}
		if alter != nil {
			alter(&b)
		}
		if err = f.policy.AuthorizeRelationshipMutation(ctx, read, b); err != nil {
			return err
		}
		if write {
			return writeFixtureBatch(ctx, tx, b)
		}
		return nil
	})
}
func (f *linkagePolicyFixture) addContact(id, uid string, active bool) {
	f.t.Helper()
	seedProjectContact(f.t, f.db, contactFixtureRef(string(f.project.SpaceID), id), uid, active)
}
func TestPaidProjectLinkageBothDirectionsDistinctContactsAndFinalGraph(t *testing.T) {
	for _, direction := range []bool{false, true} {
		t.Run(fmt.Sprint(direction), func(t *testing.T) {
			f := newLinkagePolicyFixture(t)
			for i := range 4 {
				id := fmt.Sprintf("contact-%d", i)
				f.addContact(id, "", i != 0)
				if err := f.change(id, []string{"role-a", "role-b"}, nil, direction, true, nil); err != nil {
					t.Fatal(err)
				}
			}
			// Unregistered and inactive assignments count, but a second role or duplicate
			// add on the same contact at the limit does not consume another slot.
			if err := f.change("contact-0", []string{"role-b"}, nil, direction, true, nil); err != nil {
				t.Fatal(err)
			}
			f.addContact("sixth", "", true)
			if err := f.change("sixth", []string{"role-b"}, nil, direction, true, nil); !errors.Is(err, ErrProjectContactLimit) {
				t.Fatalf("sixth: %v", err)
			}
			if err := f.change("contact-0", nil, []string{"role-a", "role-b"}, direction, true, nil); err != nil {
				t.Fatal(err)
			}
			if err := f.change("sixth", []string{"role-b"}, nil, direction, true, nil); err != nil {
				t.Fatal(err)
			}
			if f.authority.managerCalls != 7 {
				t.Fatal(f.authority.managerCalls)
			}
		})
	}
}
func TestPaidProjectLinkageDirectionsHaveIdenticalProjectGraph(t *testing.T) {
	f := newLinkagePolicyFixture(t)
	f.addContact("target", "", true)
	if err := f.db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		a, err := f.batch(ctx, tx, "target", []string{"role-b"}, nil, false)
		if err != nil {
			return err
		}
		b, err := f.batch(ctx, tx, "target", []string{"role-b"}, nil, true)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(a.Entities, b.Entities) {
			t.Fatal("direction changed final graph")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestPaidProjectLinkageNoopFreshAuthorityAndRevocation(t *testing.T) {
	for _, reason := range []string{"manager", "refunded", "expiry", "owner-inactive", "owner-rebound", "missing-grant", "test", "historical-ownerless", "missing-receipt", "bad-owner-edge"} {
		t.Run(reason, func(t *testing.T) {
			f := newLinkagePolicyFixture(t)
			switch reason {
			case "manager":
				f.authority.managerErr = ErrSharedProjectUnauthorized
			case "refunded":
				paidUpdate(t, f.db, models4datatug.NewCurrentPlanKey("personal-1"), "status", "ended")
			case "expiry":
				f.policy.now = func() time.Time { return sharedTestTime.Add(24 * time.Hour) }
			case "owner-inactive":
				r, _ := models4datatug.NewProjectContactLinkageRecord(contactFixtureRef("space", "owner-contact"))
				paidUpdate(t, f.db, r.Key(), "active", false)
			case "owner-rebound":
				r, _ := models4datatug.NewProjectContactLinkageRecord(contactFixtureRef("space", "owner-contact"))
				paidUpdate(t, f.db, r.Key(), "userID", "foreign")
			case "missing-grant":
				paidUpdate(t, f.db, models4datatug.NewPlanApplicationKey("live", "datatug", "personal-1"), "lastProPaidServiceProofId", "")
			case "test":
				f.policy.paid.Mode = "test"
			case "historical-ownerless":
				r, _ := models4datatug.NewProjectAdmissionRecord("space", f.project.ItemRef.ItemID)
				paidUpdate(t, f.db, r.Key(), "ownerContact", nil)
			case "missing-receipt":
				r, _ := models4datatug.NewSharedProjectCreateReceiptRecord("space", "command")
				if err := f.db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error { return tx.Delete(ctx, r.Key()) }); err != nil {
					t.Fatal(err)
				}
			case "bad-owner-edge":
				r, _ := models4datatug.NewProjectContactLinkageRecord(contactFixtureRef("space", "owner-contact"))
				paidUpdate(t, f.db, r.Key(), "related", nil)
				paidUpdate(t, f.db, r.Key(), "relatedIDs", []string{"-"})
			}
			if err := f.change("owner-contact", []string{"role-a"}, nil, false, false, nil); err == nil {
				t.Fatal("stale no-op admitted")
			}
		})
	}
}
func TestPaidProjectLinkageOwnerAndReciprocalMutationFences(t *testing.T) {
	for _, reason := range []string{"remove-owner", "wrong-orientation", "missing-counterpart", "foreign-project", "duplicate-entity", "unknown-role", "subpath", "empty-space", "project-to-project", "owner-provenance"} {
		t.Run(reason, func(t *testing.T) {
			f := newLinkagePolicyFixture(t)
			f.addContact("target", "", true)
			id, add, remove := "target", []string{"role-b"}, []string(nil)
			if reason == "remove-owner" {
				id, add, remove = "owner-contact", nil, []string{"role-a"}
			}
			if reason == "unknown-role" {
				add = []string{"not-configured"}
			}
			alter := func(b *contract4linkage.RelationshipMutationBatch) {
				switch reason {
				case "wrong-orientation":
					edge, _ := graphItem(b.Entities[1].Proposed, b.Entities[1].Ref.SpaceID, f.project)
					edge.RolesOfItem = edge.RolesToItem
					edge.RolesToItem = nil
				case "missing-counterpart":
					b.Entities = b.Entities[:1]
				case "foreign-project":
					b.Entities[0].Ref.SpaceID = "foreign"
				case "duplicate-entity":
					b.Entities = append(b.Entities, b.Entities[0])
				case "subpath":
					b.Entities[1].Ref.ItemRef.SubPath = "embedded"
				case "empty-space":
					b.Entities[1].Ref.SpaceID = ""
				case "project-to-project":
					b.Source = projectFixtureRef("space", "other-project")
				case "owner-provenance":
					b.Entities[0].Before.RelatedIDs = []string{"bad"}
				}
			}
			if err := f.change(id, add, remove, false, false, alter); err == nil {
				t.Fatal("invalid batch admitted")
			}
		})
	}
}
func TestPaidProjectLinkageManagerDistinctSponsorAndAllUIDEvidence(t *testing.T) {
	f := newLinkagePolicyFixture(t)
	f.addContact("manager-1", "guest", true)
	f.addContact("manager-2", "guest", true)
	for _, id := range []string{"manager-1", "manager-2"} {
		if err := f.change(id, []string{"role-b"}, nil, false, true, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.change("owner-contact", []string{"role-a"}, nil, false, false, func(b *contract4linkage.RelationshipMutationBatch) { b.ActorUserID = "guest" }); err != nil {
		t.Fatal(err)
	}
	got := f.authority.requests[len(f.authority.requests)-1]
	if got.ActorID != "guest" || len(got.ActorContacts) != 2 {
		t.Fatalf("manager evidence %+v", got)
	}
	// Guest was never used as the personal payer actor. Its assigned role evidence
	// is independent of the immutable original paid sponsor's current proof.
	r, _ := models4datatug.NewProjectContactLinkageRecord(contactFixtureRef("space", "manager-1"))
	paidUpdate(t, f.db, r.Key(), "active", false)
	r, _ = models4datatug.NewProjectContactLinkageRecord(contactFixtureRef("space", "manager-2"))
	paidUpdate(t, f.db, r.Key(), "active", false)
	if err := f.change("owner-contact", []string{"role-a"}, nil, false, false, func(b *contract4linkage.RelationshipMutationBatch) { b.ActorUserID = "guest" }); err == nil {
		t.Fatal("inactive contact is manager")
	}
}
func TestPaidProjectLinkageTargetsRollbackAndRetryExpiry(t *testing.T) {
	f := newLinkagePolicyFixture(t)
	f.addContact("target", "", true)
	f.authority.targetErr = ErrSharedProjectUnauthorized
	if err := f.change("target", []string{"role-b"}, nil, false, true, nil); !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatal(err)
	}
	f.authority.targetErr = nil
	// Complete retry reruns current payment time; first staged changes are rolled back.
	current := sharedTestTime
	f.policy.now = func() time.Time { return current }
	f.db = retryPlanDB{DB: f.db, between: func() { current = sharedTestTime.Add(24 * time.Hour) }}
	if err := f.change("target", []string{"role-b"}, nil, false, true, nil); !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatal(err)
	}
	r, p := models4datatug.NewSharedLinkedProjectRecord("space", f.project.ItemRef.ItemID)
	if err := f.db.Get(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	roles, err := readProjectContactRoles(f.project.SpaceID, p.WithRelatedAndIDs, f.policy.catalog)
	if err != nil || len(roles[contactFixtureRef("space", "target")]) != 0 {
		t.Fatalf("partial link %+v %v", roles, err)
	}
}
func TestPaidProjectLinkageConcurrentLastSlot(t *testing.T) {
	f := newLinkagePolicyFixture(t)
	for i := range 3 {
		id := fmt.Sprintf("before-%d", i)
		f.addContact(id, "", true)
		if err := f.change(id, []string{"role-b"}, nil, false, true, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"last-a", "last-b"} {
		f.addContact(id, "", true)
	}
	// Independent policy ports avoid sharing mutable fixture counters across workers.
	gate := make(chan struct{})
	var reads atomic.Int32
	db := sharedFaultDB{DB: f.db, wrap: func(tx dal.ReadwriteTransaction) dal.ReadwriteTransaction {
		return sharedGateTx{ReadwriteTransaction: tx, n: &reads, gate: gate, count: 2}
	}}
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for _, id := range []string{"last-a", "last-b"} {
		wg.Go(func() {
			local := *f
			a := &projectRoleFixtureAuthority{t: t}
			p := *f.policy
			p.manager, p.targets = a, a
			local.authority, local.policy, local.db = a, &p, db
			out <- local.change(id, []string{"role-b"}, nil, false, true, nil)
		})
	}
	wg.Wait()
	close(out)
	success, refusal := 0, 0
	for err := range out {
		if err == nil {
			success++
		} else if errors.Is(err, ErrProjectContactLimit) {
			refusal++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || refusal != 1 {
		t.Fatalf("success=%d refusal=%d", success, refusal)
	}
}

func TestPaidProjectLinkageDeletedContactCountsUntilExplicitCleanup(t *testing.T) {
	f := newLinkagePolicyFixture(t)
	for i := range 4 {
		id := fmt.Sprintf("contact-%d", i)
		f.addContact(id, "", true)
		if err := f.change(id, []string{"role-b"}, nil, false, true, nil); err != nil {
			t.Fatal(err)
		}
	}
	deleted := contactFixtureRef("space", "contact-0")
	r, _ := models4datatug.NewProjectContactLinkageRecord(deleted)
	paidUpdate(t, f.db, r.Key(), "status", "deleted")
	f.addContact("replacement", "", true)
	if err := f.change("replacement", []string{"role-b"}, nil, false, true, nil); !errors.Is(err, ErrProjectContactLimit) {
		t.Fatal("deleted contact silently freed slot", err)
	}
	f.authority.targetErr = ErrSharedProjectUnauthorized
	if err := f.change("contact-0", nil, []string{"role-b"}, true, true, nil); !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatal("cleanup needs target authority", err)
	}
	f.authority.targetErr = nil
	if err := f.change("contact-0", nil, []string{"role-b"}, true, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.change("replacement", []string{"role-b"}, nil, false, true, nil); err != nil {
		t.Fatal(err)
	}
	// The paid service is untouched by removing the last contact role.
	plan := new(models4datatug.PlanRecord)
	if err := f.db.Get(context.Background(), record.NewRecordWithData(models4datatug.NewCurrentPlanKey("personal-1"), plan)); err != nil || plan.Status != "active" || plan.PaidUntil == nil || !plan.PaidUntil.Equal(sharedTestTime.Add(24*time.Hour)) {
		t.Fatalf("cleanup changed subscription %+v %v", plan, err)
	}
}
func TestPaidProjectLinkageMissingReciprocalCannotReclaimSlot(t *testing.T) {
	f := newLinkagePolicyFixture(t)
	f.addContact("missing", "", true)
	if err := f.change("missing", []string{"role-b"}, nil, false, true, nil); err != nil {
		t.Fatal(err)
	}
	r, _ := models4datatug.NewProjectContactLinkageRecord(contactFixtureRef("space", "missing"))
	if err := f.db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error { return tx.Delete(ctx, r.Key()) }); err != nil {
		t.Fatal(err)
	}
	if err := f.change("missing", nil, []string{"role-b"}, true, true, nil); !record.IsNotFound(err) {
		t.Fatalf("missing reciprocal cleanup %v", err)
	}
	pr, p := models4datatug.NewSharedLinkedProjectRecord("space", f.project.ItemRef.ItemID)
	if err := f.db.Get(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	roles, err := readProjectContactRoles(f.project.SpaceID, p.WithRelatedAndIDs, f.policy.catalog)
	if err != nil || assignedProjectContacts(roles) != 2 {
		t.Fatal("missing contact reclaimed slot", roles, err)
	}
}
func TestPaidProjectLinkagePublicPrivateExcludedAndProtectedMalformedDenied(t *testing.T) {
	for _, access := range []string{models4datatug.AccessPrivate, "public", "unknown"} {
		t.Run(access, func(t *testing.T) {
			f := newLinkagePolicyFixture(t)
			r, _ := models4datatug.NewSharedLinkedProjectRecord("space", f.project.ItemRef.ItemID)
			paidUpdate(t, f.db, r.Key(), "access", access)
			f.authority.managerErr = ErrSharedProjectUnauthorized
			err := f.change("owner-contact", []string{"role-a"}, nil, false, false, nil)
			if access == "unknown" {
				if err == nil {
					t.Fatal("unknown access admitted")
				}
			} else if err != nil || f.authority.managerCalls != 0 {
				t.Fatal("nonprotected product policy changed", err)
			}
		})
	}
}
func TestPaidProjectLinkageSecondProtectedProjectFailureDeniesBatch(t *testing.T) {
	f := newLinkagePolicyFixture(t)
	f.addContact("target", "", true)
	other := sharedCommand()
	other.CommandID = "another-create"
	ref, err := f.service.Create(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	second := projectFixtureRef(ref.SpaceID, ref.ProjectID)
	ar, _ := models4datatug.NewProjectAdmissionRecord(ref.SpaceID, ref.ProjectID)
	paidUpdate(t, f.db, ar.Key(), "ownerContact", nil)
	if err := f.change("target", []string{"role-b"}, nil, false, false, func(b *contract4linkage.RelationshipMutationBatch) {
		r, g := fixtureGraphRecord(second)
		if err := f.db.Get(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		b.Entities = append(b.Entities, contract4linkage.RelationshipEntityMutation{Ref: second, Before: *g, Proposed: cloneFixtureGraph(t, *g)})
		b.Source = contactFixtureRef("space", "target")
	}); err == nil {
		t.Fatal("second protected project skipped")
	}
}

func TestPaidProjectLinkageConfigurationAndBatchValidation(t *testing.T) {
	f := newLinkagePolicyFixture(t)
	o := PaidProjectLinkageOptions{Paid: f.policy.paid, Roles: paidFixtureOwnerLinks().Roles, Contacts: projectContactFixturePort{}, Manager: f.authority, Targets: f.authority, Now: func() time.Time { return sharedTestTime }}
	for _, mutate := range []func(*PaidProjectLinkageOptions){func(v *PaidProjectLinkageOptions) { v.Roles = ProjectRoleCatalog{} }, func(v *PaidProjectLinkageOptions) { v.Contacts = nil }, func(v *PaidProjectLinkageOptions) { v.Manager = (*projectRoleFixtureAuthority)(nil) }, func(v *PaidProjectLinkageOptions) { v.Targets = nil }, func(v *PaidProjectLinkageOptions) { v.Now = nil }, func(v *PaidProjectLinkageOptions) { v.Paid.Mode = "test" }} {
		v := o
		mutate(&v)
		if _, err := NewPaidProjectLinkagePolicy(v); !errors.Is(err, ErrSharedProjectUnavailable) {
			t.Fatal("invalid policy configuration", err)
		}
	}
	for _, reason := range []string{"no-actor", "no-time", "no-entities", "no-project", "no-source", "bad-command", "clock-zero", "clock-backwards"} {
		t.Run(reason, func(t *testing.T) {
			local := newLinkagePolicyFixture(t)
			err := local.change("owner-contact", []string{"role-a"}, nil, false, false, func(b *contract4linkage.RelationshipMutationBatch) {
				switch reason {
				case "no-actor":
					b.ActorUserID = ""
				case "no-time":
					b.ObservedAt = time.Time{}
				case "no-entities":
					b.Entities = nil
				case "no-project":
					b.Entities = b.Entities[1:]
					b.Source = b.Entities[0].Ref
				case "no-source":
					b.Source = contactFixtureRef("space", "absent")
				case "bad-command":
					b.Commands[0].ItemRef.ItemID = "bad@"
				case "clock-zero":
					local.policy.now = func() time.Time { return time.Time{} }
				case "clock-backwards":
					local.policy.now = func() time.Time { return sharedTestTime.Add(-time.Nanosecond) }
				}
			})
			if err == nil {
				t.Fatal("invalid policy batch admitted")
			}
		})
	}
}
func TestPaidProjectLinkageSourceGrantLimitIsAuthoritative(t *testing.T) {
	f := newLinkagePolicyFixture(t)
	limits := clonePlanLimits(f.policy.paid.Config.ProLimits)
	n := int64(2)
	limits.ProtectedProjectUsers = &n
	paidUpdate(t, f.db, models4datatug.NewCurrentPlanKey("personal-1"), "limits", limits)
	paidUpdate(t, f.db, models4datatug.NewPlanApplicationKey("live", "datatug", "personal-1"), "lastProProtectedProjectUsers", n)
	for _, id := range []string{"second", "third"} {
		f.addContact(id, "", true)
	}
	if err := f.change("second", []string{"role-b"}, nil, true, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.change("third", []string{"role-b"}, nil, false, true, nil); !errors.Is(err, ErrProjectContactLimit) {
		t.Fatal("configuration replaced source limit", err)
	}
}
func TestPaidProjectLinkageFinalBatchSwapAtCap(t *testing.T) {
	f := newLinkagePolicyFixture(t)
	for i := range 4 {
		id := fmt.Sprintf("contact-%d", i)
		f.addContact(id, "", true)
		if err := f.change(id, []string{"role-b"}, nil, false, true, nil); err != nil {
			t.Fatal(err)
		}
	}
	f.addContact("new-contact", "", true)
	if err := f.db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		read := sharedProjectReadTransaction{tx}
		f.authority.expectedTX = sharedProjectReadTransaction{read}
		add, err := f.batch(ctx, read, "new-contact", []string{"role-b"}, nil, false)
		if err != nil {
			return err
		}
		remove, err := f.batch(ctx, read, "contact-0", nil, []string{"role-b"}, false)
		if err != nil {
			return err
		}
		// Commands are cumulatively projected before the policy call; the temporary
		// sixth contact in the first command is not mistaken for the final count.
		final := cloneFixtureGraph(t, add.Entities[0].Proposed)
		if _, err := final.ApplyDirectedRelationshipAndID(sharedTestTime, "actor", f.project.SpaceID, remove.Commands[0]); err != nil {
			return err
		}
		add.Commands = append(add.Commands, remove.Commands...)
		add.Entities[0].Proposed = final
		add.Entities = append(add.Entities, remove.Entities[1])
		if err := f.policy.AuthorizeRelationshipMutation(ctx, read, add); err != nil {
			return err
		}
		return writeFixtureBatch(ctx, tx, add)
	}); err != nil {
		t.Fatal(err)
	}
}
