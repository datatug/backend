package facade4datatug

import (
	"context"

	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

// BusinessUsageTestClockRuntime is a Space-scoped TEST harness surface. It
// deliberately omits the global lifecycle worker and refreshes its provider
// capability before every exposed operation.
type BusinessUsageTestClockRuntime struct {
	runtime *BusinessUsageRuntime
	clock   *contract4paymentus.ServiceTestClockCapability
	spaceID string
}

// NewBusinessUsageTestClockRuntime composes the normal native runtime with a
// verified, exact-Space TEST clock. Caller-provided clocks are never accepted.
func NewBusinessUsageTestClockRuntime(options BusinessUsageRuntimeOptions, clock *contract4paymentus.ServiceTestClockCapability, spaceID string) (*BusinessUsageTestClockRuntime, error) {
	if options.Mode != contract4paymentus.ModeTest || clock == nil || clock.Mode() != contract4paymentus.ModeTest ||
		!validBusinessUsageRuntimePolicyScope(clock.Scope()) || clock.Scope().SpaceID != spaceID ||
		models4datatug.ValidateSharedProjectIdentifier(spaceID) != nil || options.CurrentService == nil || options.CurrentService.Mode() != contract4paymentus.ModeTest {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	bound, ok := options.CurrentService.(interface {
		TestClockCapability() *contract4paymentus.ServiceTestClockCapability
	})
	if !ok || bound.TestClockCapability() != clock {
		return nil, ErrBusinessUsageRuntimeUnavailable
	}
	options.Now = clock.LogicalTime
	options.testClock = clock
	runtime, err := newBusinessUsageRuntime(options)
	if err != nil {
		return nil, err
	}
	return &BusinessUsageTestClockRuntime{runtime: runtime, clock: clock, spaceID: spaceID}, nil
}

// IssueContext and Report are the only activity operations exposed to the
// harness; the server refreshes the clock before entering existing authority.
func (r *BusinessUsageTestClockRuntime) IssueContext(ctx context.Context, actorID, spaceID, projectID string) (QueryActivityContextResponse, error) {
	if err := r.refresh(ctx, spaceID); err != nil {
		return QueryActivityContextResponse{}, err
	}
	return r.runtime.Activity.IssueContext(ctx, actorID, spaceID, projectID)
}

func (r *BusinessUsageTestClockRuntime) Report(ctx context.Context, actorID, spaceID string, request QueryActivityReport) (QueryActivityReportResult, error) {
	if err := r.refresh(ctx, spaceID); err != nil {
		return QueryActivityReportResult{}, err
	}
	return r.runtime.Activity.Report(ctx, actorID, spaceID, request)
}

func (r *BusinessUsageTestClockRuntime) OpenCurrent(ctx context.Context, spaceID string) (contract4paymentus.UsagePeriodSnapshot, error) {
	if err := r.refresh(ctx, spaceID); err != nil {
		return contract4paymentus.UsagePeriodSnapshot{}, err
	}
	return r.runtime.Periods.OpenCurrent(ctx, spaceID)
}

func (r *BusinessUsageTestClockRuntime) CloseDue(ctx context.Context, ref contract4paymentus.UsagePeriodRef) (contract4paymentus.UsagePeriodClose, error) {
	if err := r.refreshRef(ctx, ref); err != nil {
		return contract4paymentus.UsagePeriodClose{}, err
	}
	return r.runtime.Periods.CloseDue(ctx, ref)
}

func (r *BusinessUsageTestClockRuntime) PrepareInvoice(ctx context.Context, ref contract4paymentus.UsagePeriodRef) (contract4paymentus.UsageInvoiceResult, error) {
	if err := r.refreshRef(ctx, ref); err != nil {
		return contract4paymentus.UsageInvoiceResult{}, err
	}
	return r.runtime.Invoices.Prepare(ctx, ref)
}

func (r *BusinessUsageTestClockRuntime) ProcessInvoice(ctx context.Context, ref contract4paymentus.UsagePeriodRef) (contract4paymentus.UsageInvoiceResult, error) {
	if err := r.refreshRef(ctx, ref); err != nil {
		return contract4paymentus.UsageInvoiceResult{}, err
	}
	return r.runtime.Invoices.Process(ctx, ref)
}

func (r *BusinessUsageTestClockRuntime) ReadInvoice(ctx context.Context, ref contract4paymentus.UsagePeriodRef) (contract4paymentus.UsageInvoiceResult, error) {
	if err := r.refreshRef(ctx, ref); err != nil {
		return contract4paymentus.UsageInvoiceResult{}, err
	}
	return r.runtime.Invoices.Read(ctx, ref)
}

func (r *BusinessUsageTestClockRuntime) refreshRef(ctx context.Context, ref contract4paymentus.UsagePeriodRef) error {
	if r == nil || ref.Scope.Mode != contract4paymentus.ModeTest || ref.Scope.SpaceID != r.spaceID || ref.Scope.ProductID != BusinessProjectProductID ||
		ref.Scope.PayerID != r.spaceID || ref.Scope.ServiceID != BusinessProjectServiceID {
		return ErrBusinessUsageRuntimeUnavailable
	}
	return r.refresh(ctx, ref.Scope.SpaceID)
}

func (r *BusinessUsageTestClockRuntime) refresh(ctx context.Context, spaceID string) error {
	if r == nil || r.runtime == nil || r.clock == nil || ctx == nil || spaceID != r.spaceID ||
		r.clock.Mode() != contract4paymentus.ModeTest || r.clock.Scope().Mode != contract4paymentus.ModeTest ||
		r.clock.Scope().SpaceID != r.spaceID || r.clock.Scope().ServiceID != BusinessProjectServiceID {
		return ErrBusinessUsageRuntimeUnavailable
	}
	if err := r.clock.Refresh(ctx); err != nil || !validBusinessServiceTime(r.clock.LogicalTime()) {
		return ErrBusinessUsageRuntimeUnavailable
	}
	return nil
}

var _ interface {
	IssueContext(context.Context, string, string, string) (QueryActivityContextResponse, error)
	Report(context.Context, string, string, QueryActivityReport) (QueryActivityReportResult, error)
	OpenCurrent(context.Context, string) (contract4paymentus.UsagePeriodSnapshot, error)
	CloseDue(context.Context, contract4paymentus.UsagePeriodRef) (contract4paymentus.UsagePeriodClose, error)
	PrepareInvoice(context.Context, contract4paymentus.UsagePeriodRef) (contract4paymentus.UsageInvoiceResult, error)
	ProcessInvoice(context.Context, contract4paymentus.UsagePeriodRef) (contract4paymentus.UsageInvoiceResult, error)
	ReadInvoice(context.Context, contract4paymentus.UsagePeriodRef) (contract4paymentus.UsageInvoiceResult, error)
} = (*BusinessUsageTestClockRuntime)(nil)
