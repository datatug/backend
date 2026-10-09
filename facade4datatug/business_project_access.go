package facade4datatug

import (
	"context"
	"errors"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
)

var (
	ErrBusinessServiceUnproved = errors.New("business space service access is unproved")
	ErrBusinessServiceEnded    = errors.New("business space service has ended")
)

// SpaceServiceAccess is the product-neutral, current paid-service projection
// supplied by a trusted Paymentus adapter. It is not a checkout session or a
// purchase claim. Historical project admissions never substitute for it.
type SpaceServiceAccess struct {
	Mode, ServiceID, PayerSpaceID, OwnerFamily, AccountKind, ProductID, PlanID string
	OwnerSubscriptionID, PaidServiceProofID, GrantVersion                      string
	OwnerGeneration, OwnerRevision                                             int64
	PaidUntilUTC                                                               time.Time
	State                                                                      string // active or ended, after complete source reconciliation
	UnlimitedProjects, UnlimitedContacts                                       bool
}

// CurrentSpaceServiceAccessReader must use only the supplied transaction. A
// production implementation must prove the current owner/money fence, signed
// provider ingress, paid service, refunds and period from durable source data.
// Missing, partial, pending and unknown source data must return an error.
type CurrentSpaceServiceAccessReader interface {
	ReadCurrentSpaceServiceAccess(context.Context, dal.ReadTransaction, string, string, string) (SpaceServiceAccess, error)
}

// BusinessProjectAccessVerifier contains no provider, DB or mutation handle.
// It is deliberately unmounted until the Paymentus reader and every hosted
// create/write/linkage gate use the same current authority.
type BusinessProjectAccessVerifier struct {
	reader CurrentSpaceServiceAccessReader
	now    func() time.Time
}

func NewBusinessProjectAccessVerifier(reader CurrentSpaceServiceAccessReader, now func() time.Time) (*BusinessProjectAccessVerifier, error) {
	if sharedProjectPortAbsent(reader) || now == nil {
		return nil, ErrBusinessServiceUnproved
	}
	return &BusinessProjectAccessVerifier{reader: reader, now: now}, nil
}

// ReadCurrent verifies the selected Space's LIVE DataTug Business base. Caller
// authority, current Contactus membership and action-specific role remain
// separate required checks. A replacement owner for the same Space/service can
// qualify without rewriting immutable project admission provenance.
func (v *BusinessProjectAccessVerifier) ReadCurrent(ctx context.Context, tx dal.ReadTransaction, spaceID string) (SpaceServiceAccess, error) {
	var zero SpaceServiceAccess
	if v == nil || sharedProjectPortAbsent(v.reader) || v.now == nil || ctx == nil || sharedProjectPortAbsent(tx) || models4datatug.ValidateSharedProjectIdentifier(spaceID) != nil {
		return zero, ErrBusinessServiceUnproved
	}
	at := v.now().UTC()
	if at.IsZero() {
		return zero, ErrBusinessServiceUnproved
	}
	got, err := v.reader.ReadCurrentSpaceServiceAccess(ctx, sharedProjectReadTransaction{tx}, "live", "datatug", spaceID)
	if err != nil {
		return zero, err
	}
	if got.Mode != "live" || got.ServiceID != "datatug" || got.PayerSpaceID != spaceID || got.OwnerFamily != "datatug" || got.AccountKind != "organisation" || got.ProductID != "datatug-business-usage" ||
		(got.PlanID != "datatug-business-usage-monthly" && got.PlanID != "datatug-business-usage-annual") ||
		got.OwnerSubscriptionID == "" || got.OwnerGeneration < 1 || got.OwnerRevision < 1 || got.PaidServiceProofID == "" || got.GrantVersion == "" ||
		got.PaidUntilUTC.IsZero() || got.PaidUntilUTC.Location() != time.UTC || !got.UnlimitedProjects || !got.UnlimitedContacts {
		return zero, ErrBusinessServiceUnproved
	}
	// The source read may cross the paid-period boundary. Check a fresh server
	// clock after it returns so an expired grant cannot be admitted using the
	// time sampled before the read.
	current := v.now().UTC()
	if current.IsZero() || current.Before(at) {
		return zero, ErrBusinessServiceUnproved
	}
	switch got.State {
	case "ended":
		return zero, ErrBusinessServiceEnded
	case "active":
		if !current.Before(got.PaidUntilUTC) {
			return zero, ErrBusinessServiceEnded
		}
		return got, nil
	default:
		return zero, ErrBusinessServiceUnproved
	}
}
