package facade4datatug

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
	"github.com/sneat-co/paymentus/backend/stripeplumbing"
	"github.com/sneat-co/paymentus/backend/subscriptions"
	"github.com/sneat-co/sneat-core-modules/linkage/contract4linkage"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

func TestBusinessUsageTaxPolicyAuthorityBindsCurrentAndHistoricalTerms(t *testing.T) {
	old := runtimeTestTaxPolicy(contract4paymentus.ModeTest, "2026-10-10", "prod_business_test")
	current := runtimeTestTaxPolicy(contract4paymentus.ModeTest, "2026-10-11", "prod_business_test")
	authority, err := newBusinessUsageTaxPolicyAuthority(contract4paymentus.ModeTest, []BusinessUsageTaxPolicyRegistration{
		{Policy: old},
		{Policy: current, Current: true},
	})
	if err != nil {
		t.Fatalf("construct immutable current and historical tax policies: %v", err)
	}

	db := sneatcoretesting.NewMemoryDB()
	quote := runtimeTestServiceQuote(current)
	if quote.Currency != "eur" || current.Currency != "EUR" {
		t.Fatalf("test must exercise native lowercase quote and canonical uppercase policy: quote=%q policy=%q", quote.Currency, current.Currency)
	}
	scope := contract4paymentus.ServicePurchaseScope{Mode: contract4paymentus.ModeTest, SpaceID: "business-space", ServiceID: BusinessProjectServiceID}
	if err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		got, err := authority.ReadCurrentUsagePeriodTaxPolicy(ctx, tx, scope, quote)
		if err != nil {
			return err
		}
		if got.Binding != current.Binding || got.ProductID != quote.ProductID || got.ProviderProductID != quote.ProviderProductID || !strings.EqualFold(got.Currency, quote.Currency) {
			t.Fatalf("current policy was not bound to the exact quote: %#v", got)
		}
		for name, mutate := range map[string]func(*contract4paymentus.ServiceQuoteSnapshot){
			"logical product":  func(q *contract4paymentus.ServiceQuoteSnapshot) { q.ProductID = "datatug-other" },
			"provider product": func(q *contract4paymentus.ServiceQuoteSnapshot) { q.ProviderProductID = "prod_other" },
			"currency":         func(q *contract4paymentus.ServiceQuoteSnapshot) { q.Currency = "USD" },
			"tax code":         func(q *contract4paymentus.ServiceQuoteSnapshot) { q.ProviderProductTaxCode = "txcd_other" },
			"tax behavior":     func(q *contract4paymentus.ServiceQuoteSnapshot) { q.TaxBehavior = "exclusive" },
		} {
			t.Run(name, func(t *testing.T) {
				wrong := quote
				mutate(&wrong)
				if _, err := authority.ReadCurrentUsagePeriodTaxPolicy(ctx, tx, scope, wrong); err == nil {
					t.Fatal("accepted quote whose immutable tax terms differ from the configured policy")
				}
			})
		}
		return nil
	}); err != nil {
		t.Fatalf("read current policy in supplied transaction: %v", err)
	}

	binding := contract4paymentus.UsagePeriodPayerBinding{
		Scope: contract4paymentus.UsageScope{
			Mode: contract4paymentus.ModeTest, SpaceID: "business-space", ProductID: BusinessProjectProductID,
			PayerID: "business-space", ServiceID: BusinessProjectServiceID,
		},
		ProductID: current.ProductID, ProviderProductID: current.Binding.ProviderProductID,
		Currency: current.Currency, Tax: current.Binding, TaxCode: current.TaxCode, TaxBehavior: current.TaxBehavior,
	}
	if err := db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		historical, err := authority.ReadHistoricalUsagePeriodTaxPolicy(ctx, tx, binding)
		if err != nil || historical.Binding != current.Binding || historical.ProductID != current.ProductID {
			t.Fatalf("read exact retained historical policy: result=%#v err=%v", historical, err)
		}
		for name, mutate := range map[string]func(*contract4paymentus.UsagePeriodPayerBinding){
			"tax code":     func(b *contract4paymentus.UsagePeriodPayerBinding) { b.TaxCode = "txcd_other" },
			"tax behavior": func(b *contract4paymentus.UsagePeriodPayerBinding) { b.TaxBehavior = "exclusive" },
		} {
			t.Run(name, func(t *testing.T) {
				wrong := binding
				mutate(&wrong)
				if _, err := authority.ReadHistoricalUsagePeriodTaxPolicy(ctx, tx, wrong); err == nil {
					t.Fatal("accepted historical policy that mismatched the frozen payer binding")
				}
			})
		}
		binding.Tax = old.Binding
		historical, err = authority.ReadHistoricalUsagePeriodTaxPolicy(ctx, tx, binding)
		if err != nil || historical.Binding != old.Binding {
			t.Fatalf("read superseded but retained historical policy: result=%#v err=%v", historical, err)
		}
		binding.Scope.Mode = contract4paymentus.ModeLive
		if _, err := authority.ReadHistoricalUsagePeriodTaxPolicy(ctx, tx, binding); err == nil {
			t.Fatal("accepted historical policy through a different runtime mode")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type businessUsageRuntimeCurrentGateway struct{}

func (businessUsageRuntimeCurrentGateway) Mode() stripeplumbing.ServiceMode {
	return stripeplumbing.ServiceModeTest
}

func (businessUsageRuntimeCurrentGateway) ServiceCurrentSubscriptionEvidence(context.Context, string) (stripeplumbing.ServiceCurrentFinancialEvidence, error) {
	return stripeplumbing.ServiceCurrentFinancialEvidence{}, errors.New("unexpected provider read in runtime construction test")
}

type businessUsageRuntimeClockBoundCurrent struct {
	contract4paymentus.CurrentSpaceServiceReader
	clock *contract4paymentus.ServiceTestClockCapability
}

func (r businessUsageRuntimeClockBoundCurrent) TestClockCapability() *contract4paymentus.ServiceTestClockCapability {
	return r.clock
}

type businessUsageRuntimeInvoiceProvider struct{}

func (businessUsageRuntimeInvoiceProvider) FindByIntent(context.Context, contract4paymentus.UsageInvoiceProviderRequest) (contract4paymentus.UsageInvoiceLookup, error) {
	return contract4paymentus.UsageInvoiceLookup{}, nil
}
func (businessUsageRuntimeInvoiceProvider) CreateDraft(context.Context, contract4paymentus.UsageInvoiceProviderRequest) (contract4paymentus.UsageInvoiceDraft, error) {
	return contract4paymentus.UsageInvoiceDraft{}, nil
}
func (businessUsageRuntimeInvoiceProvider) ReadDraft(context.Context, contract4paymentus.UsageInvoiceProviderRequest, string) (contract4paymentus.UsageInvoiceDraft, error) {
	return contract4paymentus.UsageInvoiceDraft{}, nil
}
func (businessUsageRuntimeInvoiceProvider) AddAssignedLine(context.Context, contract4paymentus.UsageInvoiceProviderRequest, string, string) error {
	return nil
}
func (businessUsageRuntimeInvoiceProvider) Finalize(context.Context, contract4paymentus.UsageInvoiceProviderRequest, string, string) error {
	return nil
}
func (businessUsageRuntimeInvoiceProvider) Read(context.Context, contract4paymentus.UsageInvoiceProviderRequest, string) (contract4paymentus.UsageInvoiceProviderRecord, error) {
	return contract4paymentus.UsageInvoiceProviderRecord{}, nil
}

func TestBusinessUsageRuntimeComposesOneExplicitTestMode(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	current, err := subscriptions.NewCurrentSpaceServiceAuthority(db, businessUsageRuntimeCurrentGateway{}, subscriptions.CurrentSpaceServiceConfig{
		Enabled: true, Mode: subscriptions.ModeTest, MaxEvidenceAge: time.Minute,
		Purchase: subscriptions.ServicePurchaseConfig{
			ServiceID: BusinessProjectServiceID, StorefrontID: "datatug", ProductID: BusinessProjectProductID,
			CatalogueVersion: "business-test-v1", PlanIDs: []string{BusinessMonthlyPlanID}, ProviderAccountID: "acct_test",
			OwnerFamily: BusinessProjectOwnerFamily, Currency: "eur",
			Return: subscriptions.ServiceReturnContract{ID: "business-test-return", URL: "https://checkout-test.example/return"},
		},
	})
	if err != nil {
		t.Fatalf("construct fixed TEST current authority: %v", err)
	}
	policy := runtimeTestTaxPolicy(contract4paymentus.ModeTest, "2026-10-10", "prod_business_test")
	initial := initialServiceStartReaderFunc(func(context.Context, dal.ReadTransaction, contract4paymentus.ServicePurchaseScope) (contract4paymentus.ServiceInitialServiceStart, error) {
		return contract4paymentus.ServiceInitialServiceStart{}, errors.New("unexpected initial-start read in runtime construction test")
	})
	options := BusinessUsageRuntimeOptions{
		DB: db, Query: db, Mode: contract4paymentus.ModeTest, CurrentService: current, InitialStarts: initial,
		Contacts: businessActivityContactReaderFunc(func(context.Context, dal.ReadTransaction, contract4linkage.RelationshipEntityRef) (ProjectContactState, error) {
			return ProjectContactState{}, errors.New("unexpected contact read in runtime construction test")
		}),
		Actors: activityActorVerifierFunc(func(context.Context, dal.ReadTransaction, string) error {
			return errors.New("unexpected actor read in runtime construction test")
		}),
		AccessPolicy:    BusinessProjectAccessPolicy{GrantVersion: "business-test-v1", Mode: contract4paymentus.ModeTest},
		TaxPolicies:     []BusinessUsageTaxPolicyRegistration{{Policy: policy, Current: true}},
		InvoiceProvider: businessUsageRuntimeInvoiceProvider{},
		Pricing:         func() contract4paymentus.UsagePricingConfig { return contract4paymentus.DataTugBusinessUsagePricing() },
		Now:             func() time.Time { return time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC) },
	}
	runtime, err := NewBusinessUsageRuntime(options)
	if err != nil {
		t.Fatalf("construct complete fixed TEST usage runtime: %v", err)
	}
	if runtime.Access == nil || runtime.Access.Mode() != contract4paymentus.ModeTest || runtime.Activity == nil || runtime.Periods == nil ||
		runtime.Invoices == nil || runtime.Worker == nil || runtime.Ledger == nil || runtime.PeriodReader == nil || runtime.PayerAuthority == nil ||
		runtime.Worker.mode != contract4paymentus.ModeTest || runtime.Periods.authority.mode != contract4paymentus.ModeTest {
		t.Fatalf("incomplete or cross-mode runtime composition: %#v", runtime)
	}
	// The public factory must reject a clock-bound reader even when callers
	// cannot set the private testClock option. This keeps a global Worker from
	// escaping a one-Space TEST clock runtime.
	options.CurrentService = businessUsageRuntimeClockBoundCurrent{
		CurrentSpaceServiceReader: options.CurrentService,
		clock:                     new(contract4paymentus.ServiceTestClockCapability),
	}
	if _, err := NewBusinessUsageRuntime(options); !errors.Is(err, ErrBusinessUsageRuntimeUnavailable) {
		t.Fatalf("public factory accepted a clock-bound current reader: %v", err)
	}
	options.CurrentService = current
	options.Mode = contract4paymentus.ModeLive // Request-like config cannot relabel TEST dependencies.
	if _, err := NewBusinessUsageRuntime(options); !errors.Is(err, ErrBusinessUsageRuntimeUnavailable) {
		t.Fatalf("runtime accepted configured LIVE mode with TEST-native authorities: %v", err)
	}
}

func TestBusinessUsageTaxPolicyAuthorityRejectsAmbiguousOrUnapprovedConfig(t *testing.T) {
	policy := runtimeTestTaxPolicy(contract4paymentus.ModeTest, "2026-10-10", "prod_business_test")
	for name, entries := range map[string][]BusinessUsageTaxPolicyRegistration{
		"no current":        {{Policy: policy}},
		"multiple current":  {{Policy: policy, Current: true}, {Policy: runtimeTestTaxPolicy(contract4paymentus.ModeTest, "2026-10-11", "prod_business_test"), Current: true}},
		"duplicate binding": {{Policy: policy, Current: true}, {Policy: policy}},
		"wrong mode":        {{Policy: runtimeTestTaxPolicy(contract4paymentus.ModeLive, "2026-10-10", "prod_business_live"), Current: true}},
		"wrong product":     {{Policy: contract4paymentus.UsageInvoiceStripeTaxPolicy{ProductID: "other", Mode: policy.Mode, Currency: policy.Currency, TaxCode: policy.TaxCode, TaxBehavior: policy.TaxBehavior, Binding: policy.Binding}, Current: true}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newBusinessUsageTaxPolicyAuthority(contract4paymentus.ModeTest, entries); err == nil {
				t.Fatal("accepted ambiguous or unapproved tax policy configuration")
			}
		})
	}
}

func runtimeTestTaxPolicy(mode contract4paymentus.Mode, revision, providerProductID string) contract4paymentus.UsageInvoiceStripeTaxPolicy {
	policy := contract4paymentus.UsageInvoiceStripeTaxPolicy{
		ProductID: BusinessProjectProductID, Mode: mode, Currency: "EUR",
		TaxCode: "txcd_10103001", TaxBehavior: "inclusive",
		Binding: contract4paymentus.UsageInvoiceTaxBinding{
			ID: "datatug-business-usage-tax-" + string(mode), Revision: revision, ProviderProductID: providerProductID,
		},
	}
	policy.Binding, _ = policy.CanonicalBinding()
	return policy
}

func runtimeTestServiceQuote(policy contract4paymentus.UsageInvoiceStripeTaxPolicy) contract4paymentus.ServiceQuoteSnapshot {
	return contract4paymentus.ServiceQuoteSnapshot{
		ProductID: policy.ProductID, ProviderProductID: policy.Binding.ProviderProductID,
		Currency: strings.ToLower(policy.Currency), ProviderProductTaxCode: policy.TaxCode, TaxBehavior: policy.TaxBehavior,
	}
}
