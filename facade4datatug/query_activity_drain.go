package facade4datatug

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const QueryActivityMaxDrainBatchSize = 100

type QueryActivityDrainRequest struct {
	Limit   int
	AfterID string
}

type QueryActivityDrainResult struct {
	Scanned     int
	Delivered   int
	Failed      int
	NextAfterID string
	HasMore     bool
}

// Drain discovers pending work from the server-owned Space subtree, then
// delivers one bounded page. Failures remain pending and do not block later
// records in this page; the returned cursor advances so a worker can finish a
// full pass before resetting to the beginning and retrying earlier failures.
// Cancellation returns the partial result and the last completed scan position,
// leaving the current and remaining rows eligible for continuation. The cursor
// is only a scan position, never an identity or authorization fact.
func (s *QueryActivityService) Drain(ctx context.Context, spaceID string, request QueryActivityDrainRequest) (QueryActivityDrainResult, error) {
	var result QueryActivityDrainResult
	if s == nil || !validBusinessUsageMode(s.mode) || sharedProjectPortAbsent(s.db) || sharedProjectPortAbsent(s.ledger) || sharedProjectPortAbsent(s.corrections) || s.now == nil ||
		models4datatug.ValidateSharedProjectIdentifier(spaceID) != nil || request.Limit < 1 || request.Limit > QueryActivityMaxDrainBatchSize || !validQueryActivityDrainCursor(request.AfterID) {
		return result, ErrQueryActivityInvalid
	}
	anchor, _ := models4datatug.NewQueryActivityPendingRecord(spaceID, "drain-anchor")
	collection := dal.NewCollectionRef(models4datatug.QueryActivityPendingCollection, "", anchor.Key().Parent())
	type item struct {
		id      string
		pending models4datatug.QueryActivityPending
		valid   bool
	}
	var page []item
	result.NextAfterID = request.AfterID
	err := s.db.RunReadonlyTransaction(ctx, func(txCtx context.Context, tx dal.ReadTransaction) error {
		page = nil
		result = QueryActivityDrainResult{NextAfterID: request.AfterID}
		queryBuilder := dal.From(collection).NewQuery().OrderBy(dal.Ascending(dal.DocumentID())).Limit(request.Limit + 1)
		if request.AfterID != "" {
			queryBuilder.StartAfter(dal.Cursor(request.AfterID))
		}
		query := queryBuilder.SelectIntoRecord(func() record.Record {
			return record.NewRecordWithIncompleteKey(models4datatug.QueryActivityPendingCollection, reflect.String, new(models4datatug.QueryActivityPending))
		})
		records, err := dal.ExecuteQueryAndReadAllToRecords(txCtx, query, tx)
		if err != nil {
			return err
		}
		pageCount := len(records)
		if pageCount > request.Limit {
			result.HasMore = true
			pageCount = request.Limit
		}
		for i := 0; i < pageCount; i++ {
			pendingRecord := records[i]
			pending, ok := pendingRecord.Data().(*models4datatug.QueryActivityPending)
			id, idOK := pendingRecord.Key().ID.(string)
			if !idOK || !validQueryActivityDrainCursor(id) {
				return ErrQueryActivityConflict
			}
			var value models4datatug.QueryActivityPending
			if ok && pending != nil {
				value = *pending
			}
			page = append(page, item{id: id, pending: value, valid: ok && pending != nil})
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	for _, candidate := range page {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Scanned++
		pending := candidate.pending
		if !candidate.valid || pending.Validate() != nil || pending.SpaceID != spaceID || pending.ReceiptID != candidate.id {
			result.Failed++
			result.NextAfterID = candidate.id
			continue
		}
		if pending.Period.Scope.Mode != s.mode {
			// The subtree may contain periods from other fixed-mode runtimes. The
			// bounded scan cursor advances, but this service never reads or writes
			// their delivery watermark or receipt state.
			result.NextAfterID = candidate.id
			continue
		}
		if pending.DeliveryState == models4datatug.QueryActivityPendingStateDelivered {
			if err := s.advanceDeliveryWatermark(ctx, pending.Period); err != nil {
				result.Failed++
			}
			result.NextAfterID = candidate.id
			continue
		}
		if pending.DeliveryState != models4datatug.QueryActivityPendingStatePending {
			result.Failed++
			result.NextAfterID = candidate.id
			continue
		}
		if err := s.Deliver(ctx, spaceID, candidate.id); err != nil {
			if cancellation := activityDrainCancellation(ctx, err); cancellation != nil {
				return result, cancellation
			}
			result.Failed++
			result.NextAfterID = candidate.id
			continue
		}
		result.Delivered++
		result.NextAfterID = candidate.id
	}
	if !result.HasMore {
		result.NextAfterID = ""
	}
	return result, nil
}

func activityDrainCancellation(ctx context.Context, deliveryErr error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(deliveryErr, context.Canceled) || errors.Is(deliveryErr, context.DeadlineExceeded) {
		return deliveryErr
	}
	if code := status.Code(deliveryErr); code == codes.Canceled || code == codes.DeadlineExceeded {
		return deliveryErr
	}
	return nil
}

func validQueryActivityDrainCursor(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
