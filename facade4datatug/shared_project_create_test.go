package facade4datatug

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/backend/models4datatug"
	"github.com/sneat-co/sneat-go-core/sneatcoretesting"
)

var sharedTestTime = time.Date(2026, 10, 6, 13, 0, 0, 0, time.UTC)

func sharedCommand() SharedProjectCreateCommand {
	return SharedProjectCreateCommand{ActorID: "actor", SpaceID: "space", CommandID: "command", Title: "Shared"}
}

type sharedAuthority struct {
	prepare func(SharedProjectCreateBinding) (PreparedSharedProjectCreate, error)
	check   func(context.Context, dal.ReadwriteTransaction, SharedProjectCreateBinding, time.Time) error
}

func (a *sharedAuthority) PrepareSharedProjectCreate(_ context.Context, b SharedProjectCreateBinding) (PreparedSharedProjectCreate, error) {
	if a.prepare != nil {
		return a.prepare(b)
	}
	return PreparedSharedProjectCreate{Binding: b, IssuedAt: sharedTestTime, Validator: a}, nil
}

func (a *sharedAuthority) ValidateSharedProjectCreateInTransaction(ctx context.Context, tx dal.ReadwriteTransaction, b SharedProjectCreateBinding, at time.Time) error {
	if a.check != nil {
		return a.check(ctx, tx, b, at)
	}
	return nil
}

func newSharedService(t *testing.T, db dal.DB, ids IDGenerator, authority SharedProjectCreateAuthority) *SharedProjectService {
	t.Helper()
	s, err := NewSharedProjectService(db, ids, authority, func() time.Time { return sharedTestTime })
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustReadSharedReceipt(t *testing.T, db dal.DB, c SharedProjectCreateCommand) models4datatug.SharedProjectCreateReceipt {
	t.Helper()
	r, data := models4datatug.NewSharedProjectCreateReceiptRecord(c.SpaceID, c.CommandID)
	if err := db.Get(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return *data
}

func assertSharedAbsent(t *testing.T, db dal.DB, space, project, command string) {
	t.Helper()
	p, _ := models4datatug.NewSharedProjectRecord(space, project)
	r, _ := models4datatug.NewSharedProjectCreateReceiptRecord(space, command)
	for _, item := range []record.Record{p, r} {
		if err := db.Get(context.Background(), item); !record.IsNotFound(err) {
			t.Fatalf("unexpected record %s: %v", item.Key(), err)
		}
	}
}

func TestSharedProjectCreateReplayAndScopeIsolation(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	ctx := context.Background()
	if _, err := NewFacade(db, fakeIDGenerator{next: "same"}).CreateProject(ctx, "actor", "firestore", "Private"); err != nil {
		t.Fatal(err)
	}
	var validations atomic.Int32
	a := &sharedAuthority{check: func(_ context.Context, _ dal.ReadwriteTransaction, _ SharedProjectCreateBinding, _ time.Time) error {
		validations.Add(1)
		return nil
	}}
	s := newSharedService(t, db, fakeIDGenerator{next: "same"}, a)
	c := sharedCommand()
	first, err := s.Create(ctx, c)
	if err != nil || first.ProjectID != "same" || first.SpaceID != c.SpaceID {
		t.Fatalf("first %+v: %v", first, err)
	}
	receipt := mustReadSharedReceipt(t, db, c)
	s.ids = fakeIDGenerator{err: errors.New("entropy unavailable after commit")}
	s.now = func() time.Time { return sharedTestTime.Add(time.Hour) }
	second, err := s.Create(ctx, c)
	if err != nil || first != second || receipt != mustReadSharedReceipt(t, db, c) || validations.Load() != 2 {
		t.Fatalf("replay %+v: %v, validations %d", second, err, validations.Load())
	}
	s.ids = fakeIDGenerator{next: "same"}
	c.SpaceID = "other-space"
	if _, err = s.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, space := range []string{"space", "other-space"} {
		r, p := models4datatug.NewSharedProjectRecord(space, "same")
		if err = db.Get(ctx, r); err != nil || p.Title != "Shared" || p.Access != models4datatug.AccessProtected || len(p.UserIDs) != 0 {
			t.Fatalf("shared payload %s %+v: %v", space, p, err)
		}
	}
	r, private := models4datatug.NewProjectRecord("same")
	if err = db.Get(ctx, r); err != nil || private.Title != "Private" || len(private.UserIDs) != 1 {
		t.Fatalf("private project changed %+v: %v", private, err)
	}
	indexRecord, index := models4datatug.NewUserExtRecord("actor", "datatug")
	if err = db.Get(ctx, indexRecord); err != nil || len(index.Stores["firestore"].Projects) != 1 || index.Stores["firestore"].Projects["same"].Title != "Private" {
		t.Fatalf("private index changed %+v: %v", index, err)
	}
}

func TestSharedProjectCreateConflictsAndReauthorization(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	a := &sharedAuthority{}
	s := newSharedService(t, db, fakeIDGenerator{next: "project"}, a)
	c := sharedCommand()
	if _, err := s.Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SharedProjectCreateCommand){func(c *SharedProjectCreateCommand) { c.Title = "Changed" }, func(c *SharedProjectCreateCommand) { c.ActorID = "other" }} {
		changed := c
		change(&changed)
		if _, err := s.Create(context.Background(), changed); !errors.Is(err, ErrSharedProjectConflict) {
			t.Fatalf("changed command: %v", err)
		}
	}
	a.check = func(context.Context, dal.ReadwriteTransaction, SharedProjectCreateBinding, time.Time) error {
		return errors.New("role revoked")
	}
	if ref, err := s.Create(context.Background(), c); !errors.Is(err, ErrSharedProjectUnauthorized) || ref != (models4datatug.SharedProjectRef{}) {
		t.Fatalf("unauthorized replay %+v: %v", ref, err)
	}
}

func TestSharedProjectCreatePreparedBindingAndClock(t *testing.T) {
	for _, field := range []string{"actor", "space", "command", "digest", "nil-validator", "future", "zero-issued"} {
		t.Run(field, func(t *testing.T) {
			db := sneatcoretesting.NewMemoryDB()
			a := &sharedAuthority{}
			a.prepare = func(b SharedProjectCreateBinding) (PreparedSharedProjectCreate, error) {
				p := PreparedSharedProjectCreate{Binding: b, IssuedAt: sharedTestTime, Validator: a}
				switch field {
				case "actor":
					p.Binding.ActorID = "foreign"
				case "space":
					p.Binding.SpaceID = "foreign"
				case "command":
					p.Binding.CommandID = "foreign"
				case "digest":
					p.Binding.RequestDigest = "foreign"
				case "nil-validator":
					var nilValidator *sharedAuthority
					p.Validator = nilValidator
				case "future":
					p.IssuedAt = sharedTestTime.Add(time.Second)
				case "zero-issued":
					p.IssuedAt = time.Time{}
				}
				return p, nil
			}
			s := newSharedService(t, db, fakeIDGenerator{next: "project"}, a)
			if _, err := s.Create(context.Background(), sharedCommand()); !errors.Is(err, ErrSharedProjectUnauthorized) {
				t.Fatal(err)
			}
			assertSharedAbsent(t, db, "space", "project", "command")
		})
	}
	prepared := false
	a := &sharedAuthority{}
	a.prepare = func(b SharedProjectCreateBinding) (PreparedSharedProjectCreate, error) {
		prepared = true
		return PreparedSharedProjectCreate{Binding: b, IssuedAt: sharedTestTime, Validator: a}, nil
	}
	a.check = func(_ context.Context, _ dal.ReadwriteTransaction, _ SharedProjectCreateBinding, at time.Time) error {
		if at != sharedTestTime.Add(time.Second) {
			return errors.New("wrong validation time")
		}
		return nil
	}
	s := newSharedService(t, sneatcoretesting.NewMemoryDB(), fakeIDGenerator{next: "project"}, a)
	s.now = func() time.Time {
		if !prepared {
			t.Fatal("clock captured before preparation")
		}
		return sharedTestTime.Add(time.Second)
	}
	if _, err := s.Create(context.Background(), sharedCommand()); err != nil {
		t.Fatal(err)
	}
}

// These decorators reuse the real transactional memory fixture, injecting
// failures without replacing its atomicity, snapshot or retry semantics.
type sharedFaultDB struct {
	dal.DB
	wrap        func(dal.ReadwriteTransaction) dal.ReadwriteTransaction
	afterCommit error
}

func (db sharedFaultDB) RunReadwriteTransaction(ctx context.Context, f dal.RWTxWorker, opts ...dal.TransactionOption) error {
	err := db.DB.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		if db.wrap != nil {
			tx = db.wrap(tx)
		}
		return f(ctx, tx)
	}, opts...)
	if err == nil {
		return db.afterCommit
	}
	return err
}

type sharedFaultTx struct {
	dal.ReadwriteTransaction
	failRead   bool
	failInsert int
	inserts    int
}

func (tx *sharedFaultTx) Get(ctx context.Context, r record.Record) error {
	if tx.failRead {
		return errors.New("receipt read failed")
	}
	return tx.ReadwriteTransaction.Get(ctx, r)
}

func (tx *sharedFaultTx) Insert(ctx context.Context, r record.Record, opts ...dal.InsertOption) error {
	tx.inserts++
	if tx.inserts == tx.failInsert {
		return errors.New("insert failed")
	}
	return tx.ReadwriteTransaction.Insert(ctx, r, opts...)
}

func TestSharedProjectCreateTransactionFaultsAndResponseLoss(t *testing.T) {
	for _, fault := range []string{"read", "project", "receipt"} {
		t.Run(fault, func(t *testing.T) {
			base := sneatcoretesting.NewMemoryDB()
			db := sharedFaultDB{DB: base, wrap: func(tx dal.ReadwriteTransaction) dal.ReadwriteTransaction {
				f := &sharedFaultTx{ReadwriteTransaction: tx}
				switch fault {
				case "read":
					f.failRead = true
				case "project":
					f.failInsert = 1
				case "receipt":
					f.failInsert = 2
				}
				return f
			}}
			if ref, err := newSharedService(t, db, fakeIDGenerator{next: "project"}, &sharedAuthority{}).Create(context.Background(), sharedCommand()); err == nil || ref != (models4datatug.SharedProjectRef{}) {
				t.Fatalf("fault returned %+v: %v", ref, err)
			}
			assertSharedAbsent(t, base, "space", "project", "command")
		})
	}
	base := sneatcoretesting.NewMemoryDB()
	s := newSharedService(t, sharedFaultDB{DB: base, afterCommit: errors.New("response lost after commit")}, fakeIDGenerator{next: "project"}, &sharedAuthority{})
	if ref, err := s.Create(context.Background(), sharedCommand()); err == nil || ref != (models4datatug.SharedProjectRef{}) {
		t.Fatalf("response loss %+v: %v", ref, err)
	}
	s.db = base
	s.ids = fakeIDGenerator{err: errors.New("no new entropy")}
	if ref, err := s.Create(context.Background(), sharedCommand()); err != nil || ref.ProjectID != "project" {
		t.Fatalf("recovery %+v: %v", ref, err)
	}
}

type sharedCounterIDs struct{ n atomic.Int32 }

func (ids *sharedCounterIDs) NewID(context.Context) (string, error) {
	return fmt.Sprintf("project-%d", ids.n.Add(1)), nil
}

type sharedGateTx struct {
	dal.ReadwriteTransaction
	n     *atomic.Int32
	gate  chan struct{}
	count int32
}

func (tx sharedGateTx) Get(ctx context.Context, r record.Record) error {
	err := tx.ReadwriteTransaction.Get(ctx, r)
	if strings.Contains(r.Key().String(), "/projectCreates/") {
		if n := tx.n.Add(1); n <= tx.count {
			if n == tx.count {
				close(tx.gate)
			}
			select {
			case <-tx.gate:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return err
}

func TestSharedProjectCreateConcurrentSameCommand(t *testing.T) {
	const count = 8
	base := sneatcoretesting.NewMemoryDB()
	gate := make(chan struct{})
	var reads, validations atomic.Int32
	db := sharedFaultDB{DB: base, wrap: func(tx dal.ReadwriteTransaction) dal.ReadwriteTransaction {
		return sharedGateTx{ReadwriteTransaction: tx, n: &reads, gate: gate, count: count}
	}}
	a := &sharedAuthority{check: func(context.Context, dal.ReadwriteTransaction, SharedProjectCreateBinding, time.Time) error {
		validations.Add(1)
		return nil
	}}
	ids := &sharedCounterIDs{}
	s := newSharedService(t, db, ids, a)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type outcome struct {
		ref models4datatug.SharedProjectRef
		err error
	}
	out := make(chan outcome, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() { ref, err := s.Create(ctx, sharedCommand()); out <- outcome{ref, err} })
	}
	wg.Wait()
	close(out)
	var expected models4datatug.SharedProjectRef
	for result := range out {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if expected == (models4datatug.SharedProjectRef{}) {
			expected = result.ref
		}
		if result.ref != expected {
			t.Fatalf("concurrent result %+v != %+v", result.ref, expected)
		}
	}
	if validations.Load() <= count || reads.Load() <= count || ids.n.Load() != count {
		t.Fatalf("retry proof validation=%d reads=%d ids=%d", validations.Load(), reads.Load(), ids.n.Load())
	}
	var committed int
	for i := 1; i <= count; i++ {
		r, _ := models4datatug.NewSharedProjectRecord("space", fmt.Sprintf("project-%d", i))
		if err := base.Get(ctx, r); err == nil {
			committed++
		} else if !record.IsNotFound(err) {
			t.Fatal(err)
		}
	}
	if committed != 1 {
		t.Fatalf("committed %d projects", committed)
	}
}

type sharedCurrentAuthority struct {
	Allowed bool `json:"allowed"`
}

func sharedAuthorityRecord() (record.Record, *sharedCurrentAuthority) {
	data := new(sharedCurrentAuthority)
	return record.NewRecordWithData(record.NewKeyWithID("sharedTestAuthority", "current"), data), data
}
func setSharedAuthority(t *testing.T, db dal.DB, allowed bool) {
	t.Helper()
	if err := db.RunReadwriteTransaction(context.Background(), func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		r, data := sharedAuthorityRecord()
		data.Allowed = allowed
		return tx.Set(ctx, r)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSharedProjectCreateRevocationFencesConcurrentCommit(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	setSharedAuthority(t, db, true)
	read, release := make(chan struct{}), make(chan struct{})
	var attempts atomic.Int32
	a := &sharedAuthority{check: func(ctx context.Context, tx dal.ReadwriteTransaction, _ SharedProjectCreateBinding, _ time.Time) error {
		r, data := sharedAuthorityRecord()
		if err := tx.Get(ctx, r); err != nil {
			return err
		}
		if !data.Allowed {
			return errors.New("role revoked")
		}
		if attempts.Add(1) == 1 {
			close(read)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}}
	s := newSharedService(t, db, fakeIDGenerator{next: "project"}, a)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Create(ctx, sharedCommand()); done <- err }()
	select {
	case <-read:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	setSharedAuthority(t, db, false)
	close(release)
	if err := <-done; !errors.Is(err, ErrSharedProjectUnauthorized) {
		t.Fatal(err)
	}
	assertSharedAbsent(t, db, "space", "project", "command")
}

func TestSharedProjectCreateMissingPortsAndInvalidCommands(t *testing.T) {
	db := sneatcoretesting.NewMemoryDB()
	var nilAuthority *sharedAuthority
	var nilIDs *sharedCounterIDs
	for _, tc := range []struct {
		db        dal.DB
		ids       IDGenerator
		authority SharedProjectCreateAuthority
		now       func() time.Time
	}{
		{nil, fakeIDGenerator{}, &sharedAuthority{}, time.Now}, {db, nilIDs, &sharedAuthority{}, time.Now}, {db, fakeIDGenerator{}, nilAuthority, time.Now}, {db, fakeIDGenerator{}, &sharedAuthority{}, nil},
	} {
		if _, err := NewSharedProjectService(tc.db, tc.ids, tc.authority, tc.now); !errors.Is(err, ErrSharedProjectUnavailable) {
			t.Fatal(err)
		}
	}
	for _, mutate := range []func(*SharedProjectCreateCommand){func(c *SharedProjectCreateCommand) { c.ActorID = "" }, func(c *SharedProjectCreateCommand) { c.SpaceID = "../foreign" }, func(c *SharedProjectCreateCommand) { c.CommandID = "a/b" }, func(c *SharedProjectCreateCommand) { c.Title = " " }} {
		c := sharedCommand()
		mutate(&c)
		a := &sharedAuthority{prepare: func(SharedProjectCreateBinding) (PreparedSharedProjectCreate, error) {
			t.Fatal("invalid input reached authority")
			return PreparedSharedProjectCreate{}, nil
		}}
		if _, err := newSharedService(t, db, fakeIDGenerator{next: "project"}, a).Create(context.Background(), c); !errors.Is(err, ErrSharedProjectInvalid) {
			t.Fatal(err)
		}
	}
}
