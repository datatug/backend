package facade4datatug

import (
	"context"
	"errors"
	"io"
	"reflect"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/paymentus/backend/contract4paymentus"
)

const BusinessUsageLifecycleMaxPageSize = 100

var ErrBusinessUsageLifecycleUnavailable = errors.New("business usage lifecycle worker is unavailable")

// BusinessUsageLifecycleResult reports bounded progress. Cursors are durable
// scan positions and carry no authority; candidates are re-read from their
// Space-scoped records before any lifecycle operation.
type BusinessUsageLifecycleResult struct {
	CheckpointsScanned int
	PeriodsOpened      int
	PeriodsClosed      int
	InvoicesProcessed  int
	ActivitySpaces     int
	ActivityScanned    int
	ActivityDelivered  int
	Failures           int
	CheckpointAfter    string
	PendingAfter       string
	CheckpointHasMore  bool
	PendingHasMore     bool
}

// BusinessUsageLifecycleWorker is an owning-module scheduler port. It uses
// collection-group queries only to discover bounded pages of Space-owned
// records; all financial authority and writes remain in the native services.
type BusinessUsageLifecycleWorker struct {
	mode     contract4paymentus.Mode
	db       dal.DB
	query    dal.QueryExecutor
	periods  *BusinessUsagePeriodService
	activity *QueryActivityService
	invoices contract4paymentus.UsageInvoiceService
	now      func() time.Time
}

func NewBusinessUsageLifecycleWorker(db dal.DB, query dal.QueryExecutor, periods *BusinessUsagePeriodService, activity *QueryActivityService, now func() time.Time) (*BusinessUsageLifecycleWorker, error) {
	return newBusinessUsageLifecycleWorker(activityMode(activity), db, query, periods, activity, nil, now)
}

func NewBusinessUsageLifecycleWorkerWithInvoices(mode contract4paymentus.Mode, db dal.DB, query dal.QueryExecutor, periods *BusinessUsagePeriodService, activity *QueryActivityService, invoices contract4paymentus.UsageInvoiceService, now func() time.Time) (*BusinessUsageLifecycleWorker, error) {
	if sharedProjectPortAbsent(invoices) {
		return nil, ErrBusinessUsageLifecycleUnavailable
	}
	return newBusinessUsageLifecycleWorker(mode, db, query, periods, activity, invoices, now)
}

func newBusinessUsageLifecycleWorker(mode contract4paymentus.Mode, db dal.DB, query dal.QueryExecutor, periods *BusinessUsagePeriodService, activity *QueryActivityService, invoices contract4paymentus.UsageInvoiceService, now func() time.Time) (*BusinessUsageLifecycleWorker, error) {
	if !validBusinessUsageMode(mode) || sharedProjectPortAbsent(db) || sharedProjectPortAbsent(query) || sharedProjectPortAbsent(periods) ||
		sharedProjectPortAbsent(activity) || activity.mode != mode || now == nil {
		return nil, ErrBusinessUsageLifecycleUnavailable
	}
	return &BusinessUsageLifecycleWorker{mode: mode, db: db, query: query, periods: periods, activity: activity, invoices: invoices, now: now}, nil
}

func activityMode(activity *QueryActivityService) contract4paymentus.Mode {
	if activity == nil {
		return ""
	}
	return activity.mode
}

// Run performs at most one page from each collection group. Failed candidates
// are counted and skipped for this pass; cursors wrap on a later pass so a
// single held Space cannot starve other Spaces. The underlying operations are
// idempotent and safe to retry after interruption.
func (w *BusinessUsageLifecycleWorker) Run(ctx context.Context, pageSize int) (BusinessUsageLifecycleResult, error) {
	var result BusinessUsageLifecycleResult
	if w == nil || !validBusinessUsageMode(w.mode) || sharedProjectPortAbsent(w.db) || sharedProjectPortAbsent(w.query) || sharedProjectPortAbsent(w.periods) ||
		sharedProjectPortAbsent(w.activity) || ctx == nil || pageSize < 1 || pageSize > BusinessUsageLifecycleMaxPageSize {
		return result, ErrBusinessUsageLifecycleUnavailable
	}
	state, err := w.readWorkerState(ctx)
	if err != nil {
		return result, err
	}
	result.CheckpointAfter = state.CheckpointAfterPath
	checkpoints, checkpointMore, err := w.readGroupPage(ctx, models4datatug.QueryActivityPeriodCheckpointsCollection, state.CheckpointAfterPath, pageSize, false)
	if err != nil {
		return result, err
	}
	result.CheckpointHasMore = checkpointMore
	spaceDrained := make(map[string]bool)
	for i, candidate := range checkpoints {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.CheckpointsScanned++
		spaceID, checkpoint, valid := businessUsageCheckpointCandidate(candidate)
		if !valid {
			result.Failures++
		} else if checkpoint.Period.Scope.Mode != w.mode {
			// Collection-group scans are shared by TEST and LIVE. Ignore foreign
			// mode records without touching them, then advance only this mode cursor.
		} else {
			if err := w.processCheckpoint(ctx, spaceID, checkpoint, pageSize, &result, spaceDrained); err != nil {
				result.Failures++
			}
		}
		cursor := candidate.Key().String()
		if i == len(checkpoints)-1 && !checkpointMore {
			cursor = ""
		}
		if err := w.saveWorkerCursor(ctx, true, cursor); err != nil {
			return result, err
		}
		result.CheckpointAfter = cursor
	}
	if len(checkpoints) == 0 && state.CheckpointAfterPath != "" {
		if err := w.saveWorkerCursor(ctx, true, ""); err != nil {
			return result, err
		}
		result.CheckpointAfter = ""
	}

	state, err = w.readWorkerState(ctx)
	if err != nil {
		return result, err
	}
	result.PendingAfter = state.PendingAfterPath
	pending, pendingMore, err := w.readGroupPage(ctx, models4datatug.QueryActivityPendingCollection, state.PendingAfterPath, pageSize, true)
	if err != nil {
		return result, err
	}
	result.PendingHasMore = pendingMore
	spaces := make([]string, 0, len(pending))
	seenSpaces := make(map[string]struct{}, len(pending))
	for _, candidate := range pending {
		spaceID, mode, ok := businessUsagePendingSpace(candidate)
		if !ok {
			result.Failures++
			continue
		}
		if mode != w.mode {
			continue
		}
		if _, exists := seenSpaces[spaceID]; !exists {
			seenSpaces[spaceID] = struct{}{}
			spaces = append(spaces, spaceID)
		}
	}
	for _, spaceID := range spaces {
		if spaceDrained[spaceID] || len(spaceDrained) >= pageSize {
			continue
		}
		result.ActivitySpaces++
		if err := w.drainSpace(ctx, spaceID, pageSize, &result); err != nil {
			result.Failures++
		}
		spaceDrained[spaceID] = true
	}
	if len(pending) > 0 {
		cursor := pending[len(pending)-1].Key().String()
		if !pendingMore {
			cursor = ""
		}
		if err := w.saveWorkerCursor(ctx, false, cursor); err != nil {
			return result, err
		}
		result.PendingAfter = cursor
	} else if state.PendingAfterPath != "" {
		if err := w.saveWorkerCursor(ctx, false, ""); err != nil {
			return result, err
		}
		result.PendingAfter = ""
	}
	return result, nil
}

func (w *BusinessUsageLifecycleWorker) processCheckpoint(ctx context.Context, spaceID string, checkpoint models4datatug.QueryActivityPeriodCheckpoint, pageSize int, result *BusinessUsageLifecycleResult, spaceDrained map[string]bool) error {
	if checkpoint.Validate() != nil || checkpoint.Period.Scope.Mode != w.mode || checkpoint.Period.Scope.SpaceID != spaceID {
		return ErrBusinessUsageLifecycleUnavailable
	}
	switch checkpoint.State {
	case models4datatug.QueryActivityCheckpointOpening:
		snapshot, err := w.periods.ResumeOpening(ctx, checkpoint.Period)
		if err != nil {
			return err
		}
		if snapshot.Ref != checkpoint.Period {
			return ErrBusinessUsageLifecycleUnavailable
		}
		result.PeriodsOpened++
		return nil
	case models4datatug.QueryActivityCheckpointReady, models4datatug.QueryActivityCheckpointClosing:
		if checkpoint.DeliveredThrough < checkpoint.AcceptedCount && !spaceDrained[spaceID] && len(spaceDrained) < pageSize {
			// Count the attempt before running it so another period or pending
			// receipt for this Space cannot trigger a second drain in this pass,
			// even when the first attempt fails and will need retry next pass.
			spaceDrained[spaceID] = true
			if err := w.drainSpace(ctx, spaceID, pageSize, result); err != nil {
				return err
			}
		}
		now := w.now().UTC()
		if !businessUsageCloseTimeAllowed(checkpoint.Snapshot.EndUTC, now, now) {
			return nil
		}
		closed, err := w.periods.CloseDue(ctx, checkpoint.Period)
		if err != nil {
			return err
		}
		if closed.Period.Ref != checkpoint.Period {
			return ErrBusinessUsageLifecycleUnavailable
		}
		result.PeriodsClosed++
		return w.processClosedInvoice(ctx, checkpoint.Period, result)
	case models4datatug.QueryActivityCheckpointClosed:
		return w.processClosedInvoice(ctx, checkpoint.Period, result)
	default:
		return ErrBusinessUsageLifecycleUnavailable
	}
}

func (w *BusinessUsageLifecycleWorker) processClosedInvoice(ctx context.Context, ref contract4paymentus.UsagePeriodRef, result *BusinessUsageLifecycleResult) error {
	if w.invoices == nil {
		return nil // Legacy non-invoice worker composition; runtime uses the strict WithInvoices constructor.
	}
	if ref.Scope.Mode != w.mode {
		return ErrBusinessUsageLifecycleUnavailable
	}
	if _, err := w.invoices.Prepare(ctx, ref); err != nil {
		return err
	}
	if _, err := w.invoices.Process(ctx, ref); err != nil {
		return err
	}
	result.InvoicesProcessed++
	return nil
}

func (w *BusinessUsageLifecycleWorker) drainSpace(ctx context.Context, spaceID string, pageSize int, result *BusinessUsageLifecycleResult) error {
	cursorRecord, cursor := models4datatug.NewQueryActivityDrainCursorRecord(w.mode, spaceID)
	err := w.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		getErr := tx.Get(txCtx, cursorRecord)
		if getErr != nil && !record.IsNotFound(getErr) {
			return getErr
		}
		if getErr == nil && cursorRecord.Exists() {
			if cursor.Validate() != nil || cursor.Mode != w.mode || cursor.SpaceID != spaceID {
				return ErrBusinessUsageLifecycleUnavailable
			}
			return nil
		}
		// DAL adapters may signal absence as either ErrNotFound or
		// (nil, Exists=false); both are safe for this non-authoritative cursor.
		*cursor = models4datatug.QueryActivityDrainCursor{Version: 1, Mode: w.mode, SpaceID: spaceID, UpdatedAtUTC: models4datatug.CanonicalQueryActivityTime(w.now())}
		return tx.Insert(txCtx, cursorRecord)
	})
	if err != nil {
		return err
	}
	if cursor.Validate() != nil {
		return ErrBusinessUsageLifecycleUnavailable
	}
	drain, err := w.activity.Drain(ctx, spaceID, QueryActivityDrainRequest{Limit: pageSize, AfterID: cursor.AfterID})
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		// Drain preserves the last completed scan position in its result. Save
		// that position before returning so cancellation remains resumable.
		if saveErr := w.saveSpaceDrainCursor(ctx, spaceID, drain.NextAfterID); saveErr != nil {
			return saveErr
		}
		result.ActivityScanned += drain.Scanned
		result.ActivityDelivered += drain.Delivered
		result.Failures += drain.Failed
		return err
	}
	result.ActivityScanned += drain.Scanned
	result.ActivityDelivered += drain.Delivered
	result.Failures += drain.Failed
	if saveErr := w.saveSpaceDrainCursor(ctx, spaceID, drain.NextAfterID); saveErr != nil {
		return saveErr
	}
	return err
}

func (w *BusinessUsageLifecycleWorker) saveSpaceDrainCursor(ctx context.Context, spaceID, afterID string) error {
	recordValue, cursor := models4datatug.NewQueryActivityDrainCursorRecord(w.mode, spaceID)
	return w.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		err := tx.Get(txCtx, recordValue)
		if err != nil && !record.IsNotFound(err) {
			return err
		}
		if afterID != "" && !validQueryActivityDrainCursor(afterID) {
			return ErrBusinessUsageLifecycleUnavailable
		}
		absent := record.IsNotFound(err) || err == nil && !recordValue.Exists()
		if absent {
			*cursor = models4datatug.QueryActivityDrainCursor{Version: 1, Mode: w.mode, SpaceID: spaceID,
				AfterID: afterID, UpdatedAtUTC: models4datatug.CanonicalQueryActivityTime(w.now())}
		} else if cursor.Validate() != nil || cursor.Mode != w.mode || cursor.SpaceID != spaceID {
			return ErrBusinessUsageLifecycleUnavailable
		}
		cursor.AfterID = afterID
		cursor.UpdatedAtUTC = models4datatug.CanonicalQueryActivityTime(w.now())
		if cursor.Validate() != nil {
			return ErrBusinessUsageLifecycleUnavailable
		}
		if absent {
			return tx.Insert(txCtx, recordValue)
		}
		return tx.Set(txCtx, recordValue)
	})
}

func (w *BusinessUsageLifecycleWorker) readWorkerState(ctx context.Context) (models4datatug.BusinessUsageWorkerState, error) {
	var state models4datatug.BusinessUsageWorkerState
	recordValue, statePointer := models4datatug.NewBusinessUsageWorkerStateRecord(w.mode)
	err := w.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		err := tx.Get(txCtx, recordValue)
		if err == nil && recordValue.Exists() {
			if statePointer.Validate() != nil || statePointer.Mode != w.mode {
				return ErrBusinessUsageLifecycleUnavailable
			}
			state = *statePointer
			return nil
		}
		if err != nil && !record.IsNotFound(err) {
			return err
		}
		*statePointer = models4datatug.BusinessUsageWorkerState{Version: 1, Mode: w.mode, UpdatedAtUTC: models4datatug.CanonicalQueryActivityTime(w.now())}
		if statePointer.Validate() != nil {
			return ErrBusinessUsageLifecycleUnavailable
		}
		if err := tx.Insert(txCtx, recordValue); err != nil {
			return err
		}
		state = *statePointer
		return nil
	})
	return state, err
}

func (w *BusinessUsageLifecycleWorker) saveWorkerCursor(ctx context.Context, checkpoints bool, cursor string) error {
	recordValue, state := models4datatug.NewBusinessUsageWorkerStateRecord(w.mode)
	return w.db.RunReadwriteTransaction(ctx, func(txCtx context.Context, tx dal.ReadwriteTransaction) error {
		err := tx.Get(txCtx, recordValue)
		if err != nil && !record.IsNotFound(err) {
			return err
		}
		absent := record.IsNotFound(err) || err == nil && !recordValue.Exists()
		if absent {
			*state = models4datatug.BusinessUsageWorkerState{Version: 1, Mode: w.mode, UpdatedAtUTC: models4datatug.CanonicalQueryActivityTime(w.now())}
		}
		if state.Validate() != nil || state.Mode != w.mode {
			return ErrBusinessUsageLifecycleUnavailable
		}
		if checkpoints {
			state.CheckpointAfterPath = cursor
		} else {
			state.PendingAfterPath = cursor
		}
		state.UpdatedAtUTC = models4datatug.CanonicalQueryActivityTime(w.now())
		if state.Validate() != nil {
			return ErrBusinessUsageLifecycleUnavailable
		}
		if absent {
			return tx.Insert(txCtx, recordValue)
		}
		return tx.Set(txCtx, recordValue)
	})
}

func (w *BusinessUsageLifecycleWorker) readGroupPage(ctx context.Context, collection, afterPath string, limit int, pending bool) (rows []record.Record, hasMore bool, err error) {
	queryBuilder := dal.From(dal.NewCollectionGroupRef(collection, "")).NewQuery().OrderBy(dal.Ascending(dal.DocumentID())).Limit(limit + 1)
	if afterPath != "" {
		queryBuilder.StartAfter(dal.Cursor(afterPath))
	}
	if pending {
		queryBuilder.WhereField("deliveryState", dal.Equal, models4datatug.QueryActivityPendingStatePending)
	}
	var prototype record.Record
	if pending {
		prototype = record.NewRecordWithIncompleteKey(collection, reflect.String, new(models4datatug.QueryActivityPending))
	} else {
		prototype = record.NewRecordWithIncompleteKey(collection, reflect.String, new(models4datatug.QueryActivityPeriodCheckpoint))
	}
	query := queryBuilder.SelectIntoRecord(func() record.Record { return prototype })
	reader, err := w.query.ExecuteQueryToRecordsReader(ctx, query)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if closeErr := reader.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	rows = make([]record.Record, 0, limit+1)
	for len(rows) <= limit {
		row, err := reader.Next()
		if err != nil {
			if errors.Is(err, dal.ErrNoMoreRecords) || errors.Is(err, io.EOF) {
				return rows, false, nil
			}
			return nil, false, err
		}
		if sharedProjectPortAbsent(row) || sharedProjectPortAbsent(row.Key()) {
			return nil, false, ErrBusinessUsageLifecycleUnavailable
		}
		rows = append(rows, row)
	}
	return rows[:limit], true, nil
}

func businessUsageCheckpointCandidate(row record.Record) (string, models4datatug.QueryActivityPeriodCheckpoint, bool) {
	var empty models4datatug.QueryActivityPeriodCheckpoint
	spaceID, ok := businessCollectionGroupSpace(row, models4datatug.QueryActivityPeriodCheckpointsCollection)
	if !ok {
		return "", empty, false
	}
	checkpoint, ok := row.Data().(*models4datatug.QueryActivityPeriodCheckpoint)
	if !ok || checkpoint == nil {
		return spaceID, empty, false
	}
	return spaceID, *checkpoint, true
}

func businessUsagePendingSpace(row record.Record) (string, contract4paymentus.Mode, bool) {
	spaceID, ok := businessCollectionGroupSpace(row, models4datatug.QueryActivityPendingCollection)
	if !ok {
		return "", "", false
	}
	pending, ok := row.Data().(*models4datatug.QueryActivityPending)
	if !ok || pending == nil || pending.Validate() != nil || pending.SpaceID != spaceID {
		return spaceID, "", false
	}
	return spaceID, pending.Period.Scope.Mode, true
}

func businessCollectionGroupSpace(row record.Record, collection string) (string, bool) {
	if sharedProjectPortAbsent(row) || sharedProjectPortAbsent(row.Key()) || row.Key().Collection() != collection {
		return "", false
	}
	extension := row.Key().Parent()
	if extension == nil || extension.Collection() != "ext" || extension.ID != "datatug" {
		return "", false
	}
	space := extension.Parent()
	if space == nil || space.Collection() != "spaces" || space.Parent() != nil {
		return "", false
	}
	spaceID, ok := space.ID.(string)
	return spaceID, ok && models4datatug.ValidateSharedProjectIdentifier(spaceID) == nil
}
