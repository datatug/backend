package facade4datatug

import (
	"context"

	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

type BusinessUsagePeriodOpener interface {
	OpenCurrent(context.Context, string) (contract4paymentus.UsagePeriodSnapshot, error)
}

type QueryActivityContextService interface {
	IssueContext(context.Context, string, string, string) (QueryActivityContextResponse, error)
	Report(context.Context, string, string, QueryActivityReport) (QueryActivityReportResult, error)
}

// BusinessQueryActivityService is the thin route composition used by the
// host. It opens and verifies the current server-owned period before issuing
// a query activity context; Report continues through the existing authority
// and checkpoint fences.
type BusinessQueryActivityService struct {
	periods  BusinessUsagePeriodOpener
	activity QueryActivityContextService
}

func NewBusinessQueryActivityService(periods BusinessUsagePeriodOpener, activity QueryActivityContextService) (*BusinessQueryActivityService, error) {
	if sharedProjectPortAbsent(periods) || sharedProjectPortAbsent(activity) {
		return nil, ErrBusinessUsagePeriodUnavailable
	}
	return &BusinessQueryActivityService{periods: periods, activity: activity}, nil
}

func (s *BusinessQueryActivityService) IssueContext(ctx context.Context, actorID, spaceID, projectID string) (QueryActivityContextResponse, error) {
	var zero QueryActivityContextResponse
	if s == nil || sharedProjectPortAbsent(s.periods) || sharedProjectPortAbsent(s.activity) || ctx == nil {
		return zero, ErrQueryActivityUnavailable
	}
	if _, err := s.periods.OpenCurrent(ctx, spaceID); err != nil {
		return zero, err
	}
	return s.activity.IssueContext(ctx, actorID, spaceID, projectID)
}

func (s *BusinessQueryActivityService) Report(ctx context.Context, actorID, spaceID string, request QueryActivityReport) (QueryActivityReportResult, error) {
	if s == nil || sharedProjectPortAbsent(s.activity) || ctx == nil {
		return QueryActivityReportResult{}, ErrQueryActivityUnavailable
	}
	return s.activity.Report(ctx, actorID, spaceID, request)
}

var _ interface {
	IssueContext(context.Context, string, string, string) (QueryActivityContextResponse, error)
	Report(context.Context, string, string, QueryActivityReport) (QueryActivityReportResult, error)
} = (*BusinessQueryActivityService)(nil)
