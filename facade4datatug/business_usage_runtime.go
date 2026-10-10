package facade4datatug

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

var ErrBusinessUsageRuntimeUnavailable = errors.New("business usage runtime is unavailable")

// BusinessUsageTaxPolicyRegistration is immutable server configuration. One
// entry is selected for new periods; non-current entries remain available to
// validate historical closed periods after a tax policy revision.
type BusinessUsageTaxPolicyRegistration struct {
	Policy  contract4paymentus.UsageInvoiceStripeTaxPolicy
	Current bool
}

// BusinessUsageRuntimeOptions binds a complete usage runtime to one fixed
// provider mode. Every authority and worker created here uses that same mode.
// The invoice provider is supplied through the published Paymentus contract;
// callers do not pass provider IDs or tax choices in activity requests.
type BusinessUsageRuntimeOptions struct {
	DB              dal.DB
	Query           dal.QueryExecutor
	Mode            contract4paymentus.Mode
	CurrentService  contract4paymentus.CurrentSpaceServiceReader
	InitialStarts   contract4paymentus.InitialServiceStartReader
	Contacts        CurrentProjectContactPort
	Actors          BusinessActivityActorVerifier
	AccessPolicy    BusinessProjectAccessPolicy
	TaxPolicies     []BusinessUsageTaxPolicyRegistration
	InvoiceProvider contract4paymentus.UsageInvoiceProvider
	Pricing         func() contract4paymentus.UsagePricingConfig
	Now             func() time.Time
}

// BusinessUsageRuntime exposes the server-owned composition used by project
// activity intake and the bounded lifecycle scheduler.
type BusinessUsageRuntime struct {
	Access         *BusinessProjectAccessVerifier
	Activity       *BusinessQueryActivityService
	Periods        *BusinessUsagePeriodService
	Invoices       contract4paymentus.UsageInvoiceService
	Worker         *BusinessUsageLifecycleWorker
	Ledger         contract4paymentus.UsageLedger
	PeriodReader   contract4paymentus.UsagePeriodReader
	PayerAuthority contract4paymentus.UsagePeriodInvoicePayerAuthority
}

func NewBusinessUsageRuntime(options BusinessUsageRuntimeOptions) (*BusinessUsageRuntime, error) {
	if !validBusinessUsageMode(options.Mode) || sharedProjectPortAbsent(options.DB) || sharedProjectPortAbsent(options.Query) ||
		sharedProjectPortAbsent(options.CurrentService) || options.CurrentService.Mode() != options.Mode ||
		sharedProjectPortAbsent(options.InitialStarts) || sharedProjectPortAbsent(options.Contacts) || sharedProjectPortAbsent(options.Actors) ||
		options.AccessPolicy.Mode != options.Mode ||
		sharedProjectPortAbsent(options.InvoiceProvider) || options.Now == nil {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	access, err := NewBusinessProjectAccessVerifier(options.CurrentService, options.AccessPolicy, options.Now)
	if err != nil {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	policies, err := newBusinessUsageTaxPolicyAuthority(options.Mode, options.TaxPolicies)
	if err != nil {
		return nil, err
	}
	payerAuthority, err := contract4paymentus.NewSpaceServiceUsagePayerAuthority(options.CurrentService, policies)
	if err != nil {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	periodReader := contract4paymentus.NewDalgoUsagePeriodReaderWithPayerBinding()
	closeTerms, err := NewNativeBusinessUsageCloseTermsReader(options.Mode, options.DB, options.InitialStarts, periodReader)
	if err != nil {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	periodAuthority, err := NewNativeBusinessUsagePeriodAuthority(BusinessUsagePeriodAuthorityOptions{
		Mode: options.Mode, Access: access, InitialStarts: options.InitialStarts, Periods: periodReader,
		CloseTerms: closeTerms, Pricing: options.Pricing, Now: options.Now,
	})
	if err != nil {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	ledger, err := contract4paymentus.NewDalgoUsageLedgerWithPayerBinding(options.DB, periodAuthority, payerAuthority)
	if err != nil {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	bindingReader, err := NewNativeBusinessActivityBindingReader(BusinessActivityBindingReaderOptions{
		Access: access, InitialStarts: options.InitialStarts, Periods: periodReader, Contacts: options.Contacts, Actors: options.Actors,
	})
	if err != nil {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	corrections, err := contract4paymentus.NewDalgoUsageCorrectionInbox(options.DB, NewQueryActivityUsageAuthority())
	if err != nil {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	activity, err := NewQueryActivityService(options.Mode, options.DB, bindingReader, ledger, corrections, options.Now)
	if err != nil {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	periods, err := NewBusinessUsagePeriodService(options.DB, periodAuthority, ledger, options.Now)
	if err != nil {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	activityRoutes, err := NewBusinessQueryActivityService(periods, activity)
	if err != nil {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	invoices, err := contract4paymentus.NewDalgoUsageInvoiceService(options.DB, periodReader, payerAuthority, options.InvoiceProvider)
	if err != nil {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	worker, err := NewBusinessUsageLifecycleWorkerWithInvoices(options.Mode, options.DB, options.Query, periods, activity, invoices, options.Now)
	if err != nil {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	return &BusinessUsageRuntime{
		Access: access, Activity: activityRoutes, Periods: periods, Invoices: invoices,
		Worker: worker, Ledger: ledger, PeriodReader: periodReader, PayerAuthority: payerAuthority,
	}, nil
}

type businessUsageTaxPolicyAuthority struct {
	mode    contract4paymentus.Mode
	entries []BusinessUsageTaxPolicyRegistration
	current contract4paymentus.UsageInvoiceStripeTaxPolicy
}

func newBusinessUsageTaxPolicyAuthority(mode contract4paymentus.Mode, entries []BusinessUsageTaxPolicyRegistration) (*businessUsageTaxPolicyAuthority, error) {
	if !validBusinessUsageMode(mode) || len(entries) == 0 {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	result := &businessUsageTaxPolicyAuthority{mode: mode, entries: append([]BusinessUsageTaxPolicyRegistration(nil), entries...)}
	seen := make(map[contract4paymentus.UsageInvoiceTaxBinding]struct{}, len(entries))
	currentCount := 0
	for _, entry := range result.entries {
		policy := entry.Policy
		canonical, err := policy.CanonicalBinding()
		if err != nil || canonical != policy.Binding || policy.Mode != mode || policy.ProductID != BusinessProjectProductID {
			return nil, ErrBusinessUsageRuntimeUnavailable
		}
		if _, exists := seen[canonical]; exists {
			return nil, ErrBusinessUsageRuntimeUnavailable
		}
		seen[canonical] = struct{}{}
		if entry.Current {
			currentCount++
			result.current = policy
		}
	}
	if currentCount != 1 {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	return result, nil
}

func (a *businessUsageTaxPolicyAuthority) ReadCurrentUsagePeriodTaxPolicy(ctx context.Context, tx dal.ReadTransaction, scope contract4paymentus.ServicePurchaseScope, quote contract4paymentus.ServiceQuoteSnapshot) (contract4paymentus.UsagePeriodTaxPolicy, error) {
	var zero contract4paymentus.UsagePeriodTaxPolicy
	if a == nil || ctx == nil || sharedProjectPortAbsent(tx) || !validBusinessUsageRuntimePolicyScope(scope) || scope.Mode != a.mode || quote.ProductID != BusinessProjectProductID {
		return zero, ErrBusinessUsageRuntimeUnavailable
	}
	policy := a.current
	if policy.ProductID != quote.ProductID || policy.Binding.ProviderProductID != quote.ProviderProductID ||
		!strings.EqualFold(policy.Currency, quote.Currency) || policy.TaxCode != quote.ProviderProductTaxCode || policy.TaxBehavior != quote.TaxBehavior {
		return zero, ErrBusinessUsageRuntimeUnavailable
	}
	return businessUsagePeriodTaxPolicy(policy, scope), nil
}

func (a *businessUsageTaxPolicyAuthority) ReadHistoricalUsagePeriodTaxPolicy(ctx context.Context, tx dal.ReadTransaction, binding contract4paymentus.UsagePeriodPayerBinding) (contract4paymentus.UsagePeriodTaxPolicy, error) {
	var zero contract4paymentus.UsagePeriodTaxPolicy
	if a == nil || ctx == nil || sharedProjectPortAbsent(tx) || !validBusinessUsageRuntimePolicyScope(contract4paymentus.ServicePurchaseScope{Mode: binding.Scope.Mode, SpaceID: binding.Scope.SpaceID, ServiceID: binding.Scope.ServiceID}) ||
		binding.Scope.Mode != a.mode || binding.Scope.ProductID != BusinessProjectProductID {
		return zero, ErrBusinessUsageRuntimeUnavailable
	}
	scope := contract4paymentus.ServicePurchaseScope{Mode: binding.Scope.Mode, SpaceID: binding.Scope.SpaceID, ServiceID: binding.Scope.ServiceID}
	for _, entry := range a.entries {
		policy := entry.Policy
		if policy.Binding == binding.Tax && policy.ProductID == binding.ProductID && policy.Binding.ProviderProductID == binding.ProviderProductID &&
			policy.Currency == binding.Currency && policy.TaxCode == binding.TaxCode && policy.TaxBehavior == binding.TaxBehavior {
			return businessUsagePeriodTaxPolicy(policy, scope), nil
		}
	}
	return zero, ErrBusinessUsageRuntimeUnavailable
}

func businessUsagePeriodTaxPolicy(policy contract4paymentus.UsageInvoiceStripeTaxPolicy, scope contract4paymentus.ServicePurchaseScope) contract4paymentus.UsagePeriodTaxPolicy {
	return contract4paymentus.UsagePeriodTaxPolicy{
		Mode: policy.Mode, Scope: scope, ProductID: policy.ProductID, ProviderProductID: policy.Binding.ProviderProductID,
		Currency: policy.Currency, TaxCode: policy.TaxCode, TaxBehavior: policy.TaxBehavior, Binding: policy.Binding,
	}
}

var _ contract4paymentus.UsagePeriodTaxPolicyAuthority = (*businessUsageTaxPolicyAuthority)(nil)

func validBusinessUsageRuntimePolicyScope(scope contract4paymentus.ServicePurchaseScope) bool {
	return validBusinessUsageMode(scope.Mode) && scope.ServiceID == BusinessProjectServiceID && models4datatug.ValidateSharedProjectIdentifier(scope.SpaceID) == nil
}
