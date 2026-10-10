package facade4datatug

import (
	"context"
	"errors"
	"testing"

	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

func TestBusinessUsageTestClockRuntimeConstructorRejectsUnboundClock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options BusinessUsageRuntimeOptions
		spaceID string
	}{
		{name: "live mode", options: BusinessUsageRuntimeOptions{Mode: contract4paymentus.ModeLive}, spaceID: "business-space"},
		{name: "empty space", options: BusinessUsageRuntimeOptions{Mode: contract4paymentus.ModeTest}},
		{name: "invalid space", options: BusinessUsageRuntimeOptions{Mode: contract4paymentus.ModeTest}, spaceID: "bad space"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewBusinessUsageTestClockRuntime(tc.options, nil, tc.spaceID); !errors.Is(err, ErrBusinessUsageRuntimeUnavailable) {
				t.Fatalf("constructor error = %v, want unavailable", err)
			}
		})
	}
}

func TestBusinessUsageTestClockScopedPortsRejectNilReceiver(t *testing.T) {
	var runtime *BusinessUsageTestClockRuntime
	ctx := context.Background()
	ref := contract4paymentus.UsagePeriodRef{}
	checks := []struct {
		name string
		call func() error
	}{
		{"issue context", func() error { _, err := runtime.IssueContext(ctx, "actor", "space", "project"); return err }},
		{"report", func() error { _, err := runtime.Report(ctx, "actor", "space", QueryActivityReport{}); return err }},
		{"open current", func() error { _, err := runtime.OpenCurrent(ctx, "space"); return err }},
		{"close due", func() error { _, err := runtime.CloseDue(ctx, ref); return err }},
		{"prepare invoice", func() error { _, err := runtime.PrepareInvoice(ctx, ref); return err }},
		{"process invoice", func() error { _, err := runtime.ProcessInvoice(ctx, ref); return err }},
		{"read invoice", func() error { _, err := runtime.ReadInvoice(ctx, ref); return err }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, ErrBusinessUsageRuntimeUnavailable) {
				t.Fatalf("operation error = %v, want unavailable", err)
			}
		})
	}
}

func TestBusinessUsageTestClockScopedPortsRejectForeignSpaceBeforeRuntime(t *testing.T) {
	// This deliberately unbound wrapper has a zero-value capability and an
	// empty runtime. The foreign-space guard must refuse before either can be
	// consulted, so no scoped operation can reach a transaction or provider.
	runtime := &BusinessUsageTestClockRuntime{
		runtime: &BusinessUsageRuntime{}, clock: &contract4paymentus.ServiceTestClockCapability{}, spaceID: "business-space",
	}
	ctx := context.Background()
	ref := testClockHarnessPeriodRef()
	ref.Scope.SpaceID = "foreign-space"
	checks := []struct {
		name string
		call func() error
	}{
		{"issue context", func() error { _, err := runtime.IssueContext(ctx, "actor", "foreign-space", "project"); return err }},
		{"report", func() error {
			_, err := runtime.Report(ctx, "actor", "foreign-space", QueryActivityReport{})
			return err
		}},
		{"open current", func() error { _, err := runtime.OpenCurrent(ctx, "foreign-space"); return err }},
		{"close due", func() error { _, err := runtime.CloseDue(ctx, ref); return err }},
		{"prepare invoice", func() error { _, err := runtime.PrepareInvoice(ctx, ref); return err }},
		{"process invoice", func() error { _, err := runtime.ProcessInvoice(ctx, ref); return err }},
		{"read invoice", func() error { _, err := runtime.ReadInvoice(ctx, ref); return err }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, ErrBusinessUsageRuntimeUnavailable) {
				t.Fatalf("operation error = %v, want unavailable", err)
			}
		})
	}
}

func TestBusinessUsageTestClockPeriodPortsRejectMalformedOrForeignRefs(t *testing.T) {
	base := testClockHarnessPeriodRef()
	foreign := []struct {
		name string
		edit func(*contract4paymentus.UsagePeriodRef)
	}{
		{"wrong mode", func(ref *contract4paymentus.UsagePeriodRef) { ref.Scope.Mode = contract4paymentus.ModeLive }},
		{"wrong space", func(ref *contract4paymentus.UsagePeriodRef) { ref.Scope.SpaceID = "foreign-space" }},
		{"wrong product", func(ref *contract4paymentus.UsagePeriodRef) { ref.Scope.ProductID = "foreign-product" }},
		{"wrong payer", func(ref *contract4paymentus.UsagePeriodRef) { ref.Scope.PayerID = "foreign-payer" }},
		{"wrong service", func(ref *contract4paymentus.UsagePeriodRef) { ref.Scope.ServiceID = "foreign-service" }},
	}
	operations := []struct {
		name string
		call func(*BusinessUsageTestClockRuntime, contract4paymentus.UsagePeriodRef) error
	}{
		{"close due", func(runtime *BusinessUsageTestClockRuntime, ref contract4paymentus.UsagePeriodRef) error {
			_, err := runtime.CloseDue(context.Background(), ref)
			return err
		}},
		{"prepare invoice", func(runtime *BusinessUsageTestClockRuntime, ref contract4paymentus.UsagePeriodRef) error {
			_, err := runtime.PrepareInvoice(context.Background(), ref)
			return err
		}},
		{"process invoice", func(runtime *BusinessUsageTestClockRuntime, ref contract4paymentus.UsagePeriodRef) error {
			_, err := runtime.ProcessInvoice(context.Background(), ref)
			return err
		}},
		{"read invoice", func(runtime *BusinessUsageTestClockRuntime, ref contract4paymentus.UsagePeriodRef) error {
			_, err := runtime.ReadInvoice(context.Background(), ref)
			return err
		}},
	}
	runtime := &BusinessUsageTestClockRuntime{spaceID: "business-space"}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			for _, malformed := range foreign {
				t.Run(malformed.name, func(t *testing.T) {
					ref := base
					malformed.edit(&ref)
					if err := operation.call(runtime, ref); !errors.Is(err, ErrBusinessUsageRuntimeUnavailable) {
						t.Fatalf("operation error = %v, want unavailable", err)
					}
				})
			}
		})
	}
}

func TestBusinessUsageTestClockRefreshRejectsMissingScopeAndCapability(t *testing.T) {
	ctx := context.Background()
	checks := []struct {
		name    string
		runtime *BusinessUsageTestClockRuntime
		ctx     context.Context
		spaceID string
	}{
		{name: "nil receiver", spaceID: "business-space"},
		{name: "missing composed runtime", runtime: &BusinessUsageTestClockRuntime{clock: &contract4paymentus.ServiceTestClockCapability{}, spaceID: "business-space"}, ctx: ctx, spaceID: "business-space"},
		{name: "missing capability", runtime: &BusinessUsageTestClockRuntime{runtime: &BusinessUsageRuntime{}, spaceID: "business-space"}, ctx: ctx, spaceID: "business-space"},
		{name: "nil context", runtime: &BusinessUsageTestClockRuntime{runtime: &BusinessUsageRuntime{}, clock: &contract4paymentus.ServiceTestClockCapability{}, spaceID: "business-space"}, spaceID: "business-space"},
		{name: "foreign space", runtime: &BusinessUsageTestClockRuntime{runtime: &BusinessUsageRuntime{}, clock: &contract4paymentus.ServiceTestClockCapability{}, spaceID: "business-space"}, ctx: ctx, spaceID: "foreign-space"},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.runtime.refresh(check.ctx, check.spaceID); !errors.Is(err, ErrBusinessUsageRuntimeUnavailable) {
				t.Fatalf("refresh error = %v, want unavailable", err)
			}
		})
	}
}

func testClockHarnessPeriodRef() contract4paymentus.UsagePeriodRef {
	return contract4paymentus.UsagePeriodRef{Scope: contract4paymentus.UsageScope{
		Mode: contract4paymentus.ModeTest, SpaceID: "business-space", ProductID: BusinessProjectProductID,
		PayerID: "business-space", ServiceID: BusinessProjectServiceID,
	}}
}
