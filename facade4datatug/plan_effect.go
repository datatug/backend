package facade4datatug

import (
	"context"
	"errors"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
)

var ErrPlanEffectUnproved = errors.New("plan effect is not proved")

// PlanOwnerFence is copied from the private payment owner tuple by the host.
type PlanOwnerFence struct {
	Mode, Family, AccountID, OwnerSubscriptionID string
	OwnerGeneration, SubscriptionRevision        int64
}

type PlanMoneyFence struct {
	IngestEpoch, ReconciledEpoch int64
	Unresolved                   bool
}

type PlanEffectAuthority string

const (
	PlanEffectCurrent  PlanEffectAuthority = "current"
	PlanEffectObsolete PlanEffectAuthority = "obsolete"
	PlanEffectUnproved PlanEffectAuthority = "unproved"
)

// PlanEffectGrants mirrors the values frozen by the payment effect. The
// protected pair is optional only for accepted legacy effects; complete Pro
// limits still require a separately versioned server configuration.
type PlanEffectGrants struct {
	Contributors, ProjectContributors, AIQuestions int64
	AIPaysFor                                      string
	ProtectedProjects                              *int64 `json:"protectedProjects,omitempty"`
	ProtectedProjectUsers                          *int64 `json:"protectedProjectUsers,omitempty"`
}

type PlanPayment struct {
	Mode, Provider, InvoiceID, PaymentID, SubscriptionID, AccountID string
	Currency                                                        string
	ReceivedCents                                                   int64
	CashConfirmed, RefundsKnown, DisputeResolved                    bool
	Lines                                                           []PlanPaymentLine
	Refunds                                                         []PlanRefund
}

type PlanPaymentLine struct {
	ID, ProductFamily           string
	StartUTC, EndUTC            time.Time
	CashCents, RetainedTaxCents int64
	RetainedTaxKnown            bool
}

type PlanRefund struct {
	ID               string
	GrossCents       int64
	LineGrossCents   map[string]int64
	AttributionKnown bool
}

// ServiceRefundProof describes the current owner's service plan, even when a
// historical subscription caused this revision. It is never inferred from
// the account-wide A+C cash snapshot. Unknown and absence are distinct.
type ServiceRefundProof struct {
	Known                 bool
	HasPaidServiceInvoice bool
	InvoiceID             string
	ServiceStartUTC       time.Time
	ServiceEndUTC         time.Time
	RefundedInFull        bool
}

// AccountPlanEffect is a provider-neutral copy of the accepted payment fact.
// The host passes the *complete* source fact to VerifyCurrentEffect and
// ReconcileMoney; this type never substitutes for paymentus's accepted digest.
type AccountPlanEffect struct {
	Fence                    PlanOwnerFence
	BuyerID                  string
	AccountKind              string
	PlanID                   string
	PlaceID                  string
	Tier                     string
	Period                   string
	Status                   string
	LastPaidEnd              time.Time
	ScheduledEnd             time.Time
	EffectiveEnd             time.Time
	PaidService              bool
	AppliedPercent           int
	LaunchGrant              bool
	Grants                   PlanEffectGrants
	Payments                 []PlanPayment
	CashBasisKnown           bool
	MoneyIngestEpoch         int64
	ObservedAt               time.Time
	CheckoutStartedAt        time.Time
	QuoteKey                 string
	PaidServiceProofID       string
	BasisOnly                bool
	SourceSubscriptionID     string
	SourceRevision           int64
	SourcePaidService        bool
	SourceQuoteKey           string
	SourcePaidServiceProofID string
	LastServiceRefund        *ServiceRefundProof
	TerminalFullRefund       bool `json:"terminalFullRefund,omitempty"`
}

// PlanEffectOwnerPort is bound to paymentus's transaction-aware owner reader.
// Every method receives the same DAL transaction as the product writer.
type PlanEffectOwnerPort interface {
	ReadOwner(context.Context, dal.ReadTransaction, string, string, string) (PlanOwnerFence, error)
	ReadMoneyFence(context.Context, dal.ReadTransaction, string, string, string) (PlanMoneyFence, error)
	ClassifyEffect(context.Context, dal.ReadTransaction, PlanOwnerFence) (PlanEffectAuthority, error)
	VerifyCurrentEffect(context.Context, dal.ReadTransaction, AccountPlanEffect) error
	ReconcileMoney(context.Context, dal.ReadwriteTransaction, AccountPlanEffect) error
}

type PersonalPlanOwnerPort interface {
	VerifyPersonalOwner(context.Context, dal.ReadTransaction, string, string) error
}

// PaidMonthAllocator is a narrow host translation to core's existing
// AllocatePaidMonths. The writer owns deduplication and aggregation.
type PaidMonthAllocator interface {
	AllocatePaidMonths(PlanPayment, string) (map[string]int64, error)
}

type ProLimitsSnapshot struct {
	Version            string
	ProjectGuestsKnown bool
	Limits             models4datatug.PlanLimits
}

type ProLimitsResolver interface {
	ResolveProLimits(string, PlanEffectGrants) (ProLimitsSnapshot, error)
}

// ConfiguredProLimits is a pure, versioned source of complete Pro grants.
// Production values are supplied by the host's reviewed configuration.
type ConfiguredProLimits struct {
	ByPlanID map[string]ProLimitsSnapshot
}

func (c ConfiguredProLimits) ResolveProLimits(planID string, grants PlanEffectGrants) (ProLimitsSnapshot, error) {
	snapshot, ok := c.ByPlanID[planID]
	if !ok {
		return ProLimitsSnapshot{}, ErrPlanEffectUnproved
	}
	return checkedProLimits(snapshot, grants)
}

// Check at the writer boundary too: an injected resolver is not trusted to
// validate its own result or to retain ownership of mutable configuration.
func checkedProLimits(snapshot ProLimitsSnapshot, grants PlanEffectGrants) (ProLimitsSnapshot, error) {
	l := snapshot.Limits
	if snapshot.Version == "" || !snapshot.ProjectGuestsKnown || l.ProjectContributors == nil ||
		l.ProtectedProjects == nil || l.ProtectedProjectUsers == nil ||
		*l.ProtectedProjects <= 0 || *l.ProtectedProjectUsers <= 0 ||
		l.Contributors != grants.Contributors || *l.ProjectContributors != grants.ProjectContributors ||
		l.AIQuestions != grants.AIQuestions || l.AIPaysFor != grants.AIPaysFor ||
		l.ProjectGuests < 0 || len(l.AIModelClasses) == 0 || validateLimits(l) != nil {
		return ProLimitsSnapshot{}, ErrPlanEffectUnproved
	}
	if (grants.ProtectedProjects == nil) != (grants.ProtectedProjectUsers == nil) {
		return ProLimitsSnapshot{}, ErrPlanEffectUnproved
	}
	if grants.ProtectedProjects != nil && (*grants.ProtectedProjects != *l.ProtectedProjects || *grants.ProtectedProjectUsers != *l.ProtectedProjectUsers) {
		return ProLimitsSnapshot{}, ErrPlanEffectUnproved
	}
	snapshot.Limits = clonePlanLimits(l)
	return snapshot, nil
}

func clonePlanEffectGrants(in PlanEffectGrants) PlanEffectGrants {
	out := in
	if in.ProtectedProjects != nil {
		value := *in.ProtectedProjects
		out.ProtectedProjects = &value
	}
	if in.ProtectedProjectUsers != nil {
		value := *in.ProtectedProjectUsers
		out.ProtectedProjectUsers = &value
	}
	return out
}
