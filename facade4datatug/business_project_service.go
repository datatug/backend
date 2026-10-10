// Copyright 2026 Sneat.co
package facade4datatug

import (
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

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
