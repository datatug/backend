package facade4datatug

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/coretypes"
)

var ErrBusinessActivityBindingUnavailable = errors.New("business activity binding is unavailable")

// BusinessActivityActorVerifier must bind actorID to the authenticated server
// identity for this request. Implementations must use only the supplied
// transaction and must not trust an actor ID copied from a request body.
type BusinessActivityActorVerifier interface {
	VerifyBusinessActivityActor(context.Context, dal.ReadTransaction, string) error
}

type BusinessActivityBindingReaderOptions struct {
	Access        *BusinessProjectAccessVerifier
	InitialStarts contract4paymentus.InitialServiceStartReader
	Periods       contract4paymentus.UsagePeriodReader
	Contacts      CurrentProjectContactPort
	Actors        BusinessActivityActorVerifier
}

// NativeBusinessActivityBindingReader composes DataTug project authorization
// with Paymentus' current paid-service and immutable original-start readers.
// Every authority read is confined to the caller's transaction.
type NativeBusinessActivityBindingReader struct {
	access        *BusinessProjectAccessVerifier
	initialStarts contract4paymentus.InitialServiceStartReader
	periods       contract4paymentus.UsagePeriodReader
	contacts      CurrentProjectContactPort
	actors        BusinessActivityActorVerifier
}

func NewNativeBusinessActivityBindingReader(options BusinessActivityBindingReaderOptions) (*NativeBusinessActivityBindingReader, error) {
	if sharedProjectPortAbsent(options.Access) || sharedProjectPortAbsent(options.InitialStarts) || sharedProjectPortAbsent(options.Periods) ||
		sharedProjectPortAbsent(options.Contacts) || sharedProjectPortAbsent(options.Actors) {
		return nil, ErrBusinessActivityBindingUnavailable
	}
	return &NativeBusinessActivityBindingReader{
		access: options.Access, initialStarts: options.InitialStarts, periods: options.Periods, contacts: options.Contacts, actors: options.Actors,
	}, nil
}

func (r *NativeBusinessActivityBindingReader) ReadCurrentBusinessActivityBinding(
	ctx context.Context,
	tx dal.ReadTransaction,
	actorID, spaceID, projectID string,
	at time.Time,
) (BusinessActivityBinding, error) {
	var zero BusinessActivityBinding
	if r == nil || sharedProjectPortAbsent(r.access) || sharedProjectPortAbsent(r.initialStarts) || sharedProjectPortAbsent(r.periods) ||
		sharedProjectPortAbsent(r.contacts) || sharedProjectPortAbsent(r.actors) || ctx == nil ||
		sharedProjectPortAbsent(tx) || !validQueryActivityActor(actorID) ||
		models4datatug.ValidateSharedProjectIdentifier(spaceID) != nil ||
		models4datatug.ValidateSharedProjectIdentifier(projectID) != nil || !validQueryActivityTime(at) ||
		at.Location() != time.UTC {
		return zero, ErrQueryActivityInvalid
	}
	if err := r.actors.VerifyBusinessActivityActor(ctx, sharedProjectReadTransaction{tx}, actorID); err != nil {
		return zero, ErrQueryActivityUnauthorized
	}

	projectRecord, project := models4datatug.NewSharedLinkedProjectRecord(spaceID, projectID)
	if err := tx.Get(ctx, projectRecord); err != nil {
		return zero, err
	}
	if project.Access != models4datatug.AccessProtected || len(project.UserIDs) != 0 {
		return zero, ErrQueryActivityUnauthorized
	}
	projectRef := businessActivityProjectRef(spaceID, projectID)
	admission, err := readLinkedProjectAdmission(ctx, sharedProjectReadTransaction{tx}, projectRef, project)
	if err != nil {
		return zero, err
	}
	if admission.Version != 2 || admission.Mode != "live" || admission.Product != BusinessProjectProductID ||
		admission.SpaceID != spaceID || admission.ProjectID != projectID || admission.PayerID != spaceID ||
		admission.ServiceID != BusinessProjectServiceID {
		return zero, ErrQueryActivityUnauthorized
	}

	queryProof, err := r.readCurrentProjectQueryUse(ctx, tx, projectRef, project, admission, actorID)
	if err != nil {
		return zero, err
	}
	access, err := r.access.ReadCurrent(ctx, sharedProjectReadTransaction{tx}, spaceID)
	if err != nil {
		return zero, err
	}
	if access.Mode != string(contract4paymentus.ModeLive) || access.ServiceID != BusinessProjectServiceID ||
		access.PayerSpaceID != spaceID || access.OwnerFamily != BusinessProjectOwnerFamily ||
		access.AccountKind != BusinessProjectAccountKind || access.ProductID != BusinessProjectProductID ||
		(access.PlanID != BusinessMonthlyPlanID && access.PlanID != BusinessAnnualPlanID) ||
		access.PaidServiceProofID == "" || access.State != "active" || access.PaidUntilUTC.IsZero() || !at.Before(access.PaidUntilUTC) {
		return zero, ErrBusinessServiceUnproved
	}

	scope := contract4paymentus.ServicePurchaseScope{
		Mode: contract4paymentus.ModeLive, SpaceID: spaceID, ServiceID: BusinessProjectServiceID,
	}
	initial, err := r.initialStarts.ReadInitialServiceStart(ctx, sharedProjectReadTransaction{tx}, scope)
	if err != nil {
		return zero, err
	}
	if !validBusinessInitialServiceStart(initial, scope) {
		return zero, ErrBusinessServiceUnproved
	}
	usageScope := contract4paymentus.UsageScope{
		Mode: scope.Mode, SpaceID: spaceID, ProductID: BusinessProjectProductID,
		PayerID: spaceID, ServiceID: BusinessProjectServiceID,
	}
	config := contract4paymentus.DataTugBusinessUsagePricing()
	period, err := contract4paymentus.UsagePeriodForAnchor(usageScope, config, initial.AnchorUTC, at)
	if err != nil {
		return zero, err
	}
	// A period's pricing is frozen by the native UsageLedger at Open. Reuse
	// that exact snapshot after a config revision; fall back to current pricing
	// only before the native period exists. Unknown or malformed native state
	// must never be treated as absence.
	state, periodErr := r.periods.ReadPeriod(ctx, sharedProjectReadTransaction{tx}, period.Ref)
	if periodErr == nil {
		if !validBusinessUsageSnapshot(state.Snapshot) || !sameBusinessUsageWindow(period, state.Snapshot) ||
			!usagePeriodMatchesOriginalStart(state.Snapshot, initial) || state.Closed {
			return zero, ErrBusinessServiceUnproved
		}
		period = state.Snapshot
	} else if !errors.Is(periodErr, contract4paymentus.ErrUsagePeriodMissing) {
		return zero, ErrBusinessServiceUnproved
	}
	if access.PaidServiceProofID == "" || queryProof == "" {
		return zero, ErrBusinessServiceUnproved
	}
	return BusinessActivityBinding{
		Period: period.Ref, PeriodStartUTC: period.StartUTC, PeriodEndUTC: period.EndUTC,
		PaidUntilUTC: access.PaidUntilUTC, PaidBindingProofID: access.PaidServiceProofID,
		QueryUseProofID: queryProof, QueryUseAllowed: true,
	}, nil
}

func (r *NativeBusinessActivityBindingReader) readCurrentProjectQueryUse(
	ctx context.Context,
	tx dal.ReadTransaction,
	projectRef contract4linkage.RelationshipEntityRef,
	project *models4datatug.SharedLinkedProject,
	admission *models4datatug.ProjectAdmission,
	actorID string,
) (string, error) {
	catalog := models4datatug.ProjectRoleCatalog()
	approved := projectRoleCatalog{version: fmt.Sprint(catalog.Version), roles: make(map[string]struct{}, len(catalog.Roles))}
	canRunQueries := make(map[string]bool, len(catalog.Roles))
	for _, role := range catalog.Roles {
		id := string(role.ID)
		approved.roles[id] = struct{}{}
		canRunQueries[id] = role.CanRunQueries
	}
	rolesByContact, err := readProjectContactRoles(projectRef.SpaceID, project.WithRelatedAndIDs, approved)
	if err != nil {
		return "", err
	}
	var evidence *businessActivityQueryUseEvidence
	for contactRef, roles := range rolesByContact {
		if len(roles) == 0 || contactRef.SpaceID != projectRef.SpaceID {
			continue
		}
		state, err := r.contacts.ReadCurrentProjectContact(ctx, sharedProjectReadTransaction{tx}, contactRef)
		if err != nil {
			return "", ErrQueryActivityUnavailable
		}
		if state.Ref != contactRef {
			return "", ErrSharedProjectConflict
		}
		if !state.Exists || !state.Active || state.UserID != actorID {
			continue
		}
		contactRecord, contactGraph := models4datatug.NewProjectContactLinkageRecord(contactRef)
		if err := tx.Get(ctx, contactRecord); err != nil || contactGraph.Validate() != nil {
			return "", ErrQueryActivityUnavailable
		}
		edge, err := graphItem(*contactGraph, contactRef.SpaceID, projectRef)
		if err != nil || !slices.Equal(rolesOf(edge, false), roles) {
			return "", ErrSharedProjectConflict
		}
		queryRoles := queryUseRoles(roles, canRunQueries)
		if len(queryRoles) == 0 {
			return "", ErrQueryActivityUnauthorized
		}
		if evidence != nil {
			// Multiple project contacts for one UID are ambiguous. Do not union
			// their roles or silently choose whichever graph entry was visited first.
			return "", ErrQueryActivityUnauthorized
		}
		evidence = &businessActivityQueryUseEvidence{
			Version: 1, ActorID: actorID, SpaceID: string(projectRef.SpaceID), ProjectID: projectRef.ItemRef.ItemID,
			Contact: contactRef, ContactUserID: state.UserID, ContactActive: state.Active,
			Roles: queryRoles, RoleCatalogVersion: catalog.Version,
			AdmissionDigest: admission.RequestDigest, AdmissionCommandID: admission.CommandID,
		}
	}
	if evidence == nil {
		return "", ErrQueryActivityUnauthorized
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return "", ErrQueryActivityUnavailable
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func queryUseRoles(roles []string, canRunQueries map[string]bool) []string {
	permitted := make([]string, 0, len(roles))
	for _, role := range roles {
		if canRunQueries[role] {
			permitted = append(permitted, role)
		}
	}
	return permitted
}

func hasQueryUseRole(roles []string, canRunQueries map[string]bool) bool {
	return len(queryUseRoles(roles, canRunQueries)) != 0
}

type businessActivityQueryUseEvidence struct {
	Version            int
	ActorID            string
	SpaceID            string
	ProjectID          string
	Contact            contract4linkage.RelationshipEntityRef
	ContactUserID      string
	ContactActive      bool
	Roles              []string
	RoleCatalogVersion uint16
	AdmissionDigest    string
	AdmissionCommandID string
}

func businessActivityProjectRef(spaceID, projectID string) contract4linkage.RelationshipEntityRef {
	return contract4linkage.RelationshipEntityRef{
		SpaceID: coretypes.SpaceID(spaceID),
		ItemRef: coretypes.ItemRef{ExtID: "datatug", Collection: "projects", ItemID: projectID},
	}
}

func validBusinessInitialServiceStart(start contract4paymentus.ServiceInitialServiceStart, scope contract4paymentus.ServicePurchaseScope) bool {
	return start.Version == 1 && start.Scope == scope && start.PayerID == scope.SpaceID &&
		start.LineageID != "" && start.QuoteID != "" && start.QuoteFingerprint != "" &&
		start.ProviderAccountID != "" && start.CustomerID != "" && start.SessionID != "" &&
		start.SubscriptionID != "" && start.InvoiceID != "" && start.InvoiceLineID != "" &&
		start.PaymentID != "" && start.PriceID != "" && start.Currency == "eur" &&
		start.PaymentType == "new_subscription" && start.ExecutionGeneration > 0 && start.CashMinor > 0 &&
		start.CashConfirmed && start.RefundsKnown && start.NoRefunds && start.DisputeResolved &&
		validBusinessServiceTime(start.AnchorUTC) && validBusinessServiceTime(start.InitialPeriodEndUTC) &&
		validBusinessServiceTime(start.ObservedAtUTC) && start.AnchorUTC.Before(start.InitialPeriodEndUTC) &&
		!start.ObservedAtUTC.Before(start.AnchorUTC)
}
