// Copyright 2026 Sneat.co
package facade4datatug

import (
	"reflect"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

// ProBusinessSharedProjectOptions configures both plans on one route service.
// Billing intent selects only which authority to prove; it never grants one.
type ProBusinessSharedProjectOptions struct {
	Pro      PaidSharedProjectOptions
	Business BusinessSharedProjectOptions
}

func sharedProBusinessOwnerLinks(options ProBusinessSharedProjectOptions) (*PaidProjectOwnerLinksOptions, error) {
	proLinks, businessLinks := options.Pro.ContactLinks, options.Business.ContactLinks
	if proLinks != nil && businessLinks != nil && !reflect.DeepEqual(proLinks, businessLinks) {
		return nil, ErrSharedProjectUnavailable
	}
	if proLinks == nil {
		proLinks = businessLinks
	}
	if proLinks == nil {
		return nil, ErrSharedProjectUnavailable
	}
	return proLinks, nil
}

func newProBusinessSharedProjectService(db dal.DB, ids IDGenerator, authority SharedProjectCreateAuthority, now func() time.Time, options ProBusinessSharedProjectOptions, activation *sharedProjectActivation) (*SharedProjectService, error) {
	if options.Pro.validate() != nil || options.Business.AccessPolicy.validate() != nil || sharedProjectPortAbsent(options.Business.ServiceReader) ||
		sharedProjectPortAbsent(db) || sharedProjectPortAbsent(ids) || now == nil {
		return nil, ErrSharedProjectUnavailable
	}
	if activation == nil && sharedProjectPortAbsent(authority) {
		return nil, ErrSharedProjectUnavailable
	}
	ownerOptions, err := sharedProBusinessOwnerLinks(options)
	if err != nil {
		return nil, err
	}
	ownerLinks, err := snapshotProjectOwnerLinks(ownerOptions)
	if err != nil {
		return nil, err
	}
	business, err := NewBusinessProjectAccessVerifier(options.Business.ServiceReader, options.Business.AccessPolicy, now)
	if err != nil {
		return nil, err
	}
	pro := snapshotPaidSharedProjectOptions(options.Pro)
	return &SharedProjectService{
		db: db, ids: ids, authority: authority, now: now, paid: &pro,
		business: business, ownerLinks: ownerLinks, activation: activation,
		recordQueryEditCandidates: options.Pro.EnableQueryEditCandidates,
	}, nil
}

// NewProBusinessSharedProjectService enables both finite personal Pro and
// explicit Space Business access on a single shared-project route service.
func NewProBusinessSharedProjectService(db dal.DB, ids IDGenerator, authority SharedProjectCreateAuthority, now func() time.Time, options ProBusinessSharedProjectOptions) (*SharedProjectService, error) {
	return newProBusinessSharedProjectService(db, ids, authority, now, options, nil)
}

// NewActivatingProBusinessSharedProjectService adds explicit-submit Core
// activation for first creation in a Space. Pro quota inventory remains
// mandatory, while Business creation does not initialize or consume it.
func NewActivatingProBusinessSharedProjectService(db dal.DB, ids IDGenerator, now func() time.Time, options ProBusinessSharedProjectOptions, activationOptions SharedProjectActivationOptions) (*SharedProjectService, error) {
	activation, err := snapshotSharedProjectActivation(activationOptions, true)
	if err != nil {
		return nil, err
	}
	return newProBusinessSharedProjectService(db, ids, nil, now, options, activation)
}

// BusinessSharedProjectOptions are trusted host composition. The caller's
// existing Space authority remains required for each create command; current
// paid access is separately read from Paymentus in that same transaction.
type BusinessSharedProjectOptions struct {
	AccessPolicy  BusinessProjectAccessPolicy
	ServiceReader contract4paymentus.CurrentSpaceServiceReader
	ContactLinks  *PaidProjectOwnerLinksOptions
}

// NewBusinessSharedProjectService enables only the DataTug Business shared
// project path. It has no Pro directory, finite quota, or AI entitlement.
func NewBusinessSharedProjectService(
	db dal.DB,
	ids IDGenerator,
	authority SharedProjectCreateAuthority,
	now func() time.Time,
	options BusinessSharedProjectOptions,
) (*SharedProjectService, error) {
	if options.AccessPolicy.validate() != nil || sharedProjectPortAbsent(options.ServiceReader) {
		return nil, ErrSharedProjectUnavailable
	}
	s, err := NewSharedProjectService(db, ids, authority, now)
	if err != nil {
		return nil, err
	}
	ownerLinks, err := snapshotProjectOwnerLinks(options.ContactLinks)
	if err != nil {
		return nil, err
	}
	access, err := NewBusinessProjectAccessVerifier(options.ServiceReader, options.AccessPolicy, now)
	if err != nil {
		return nil, err
	}
	s.business = access
	s.ownerLinks = ownerLinks
	return s, nil
}

// NewActivatingBusinessSharedProjectService composes Business access with the
// same explicit-submit Core capability activation used for Pro. A Space that
// has not enabled DataTug can therefore create its first shared project; the
// capability marker is still planned and applied only in the create
// transaction after current paid, membership, and owner-contact checks.
func NewActivatingBusinessSharedProjectService(
	db dal.DB,
	ids IDGenerator,
	now func() time.Time,
	options BusinessSharedProjectOptions,
	activation SharedProjectActivationOptions,
) (*SharedProjectService, error) {
	if sharedProjectPortAbsent(db) || sharedProjectPortAbsent(ids) || now == nil || options.AccessPolicy.validate() != nil || sharedProjectPortAbsent(options.ServiceReader) {
		return nil, ErrSharedProjectActivationUnavailable
	}
	activationSnapshot, err := snapshotSharedProjectActivation(activation, false)
	if err != nil {
		return nil, err
	}
	ownerLinks, err := snapshotProjectOwnerLinks(options.ContactLinks)
	if err != nil {
		return nil, ErrSharedProjectActivationUnavailable
	}
	access, err := NewBusinessProjectAccessVerifier(options.ServiceReader, options.AccessPolicy, now)
	if err != nil {
		return nil, ErrSharedProjectActivationUnavailable
	}
	return &SharedProjectService{
		db: db, ids: ids, now: now, business: access, ownerLinks: ownerLinks,
		activation: activationSnapshot,
	}, nil
}
