package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Querier is what a statement runs on: a pool, or the transaction of a unit of
// work.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Unit is a unit of work: one writer transaction that every statement made
// under its context joins, so that what a request writes, and the answer the
// request stores for its Idempotency-Key, commit together or not at all (SPEC
// 6.1). A transaction begun under it is a savepoint inside it, and a read under
// it sees what the unit has written so far.
type Unit struct {
	owner *Store
	tx    *sql.Tx
	// closed is set once the unit commits or rolls back; a context that still
	// carries it then runs on the pools again.
	closed atomic.Bool
	mu     sync.Mutex
	// savepoints numbers the savepoints taken in the unit.
	savepoints int
	// after holds what runs once the unit has committed.
	after []func(context.Context)
	ctx   context.Context
}

type unitKey struct{ owner *Store }

// BeginUnit opens a unit of work on the writer and returns a context that
// carries it. The unit is not cancelled with ctx: it ends only by Commit or
// Rollback.
func (s *Store) BeginUnit(ctx context.Context) (context.Context, *Unit, error) {
	base := context.WithoutCancel(ctx)
	tx, err := s.writer.BeginTx(base, nil)
	if err != nil {
		return ctx, nil, err
	}
	u := &Unit{owner: s, tx: tx, ctx: base}
	return context.WithValue(ctx, unitKey{s}, u), u, nil
}

// unitOf is the open unit of work ctx carries for s, or nil.
func (s *Store) unitOf(ctx context.Context) *Unit {
	u, _ := ctx.Value(unitKey{s}).(*Unit)
	if u == nil || u.closed.Load() {
		return nil
	}
	return u
}

// Commit commits everything the unit wrote, then runs what AfterCommit
// deferred.
func (u *Unit) Commit() error {
	if u.closed.Swap(true) {
		return sql.ErrTxDone
	}
	if err := u.tx.Commit(); err != nil {
		return err
	}
	for _, f := range u.after {
		f(u.ctx)
	}
	return nil
}

// Rollback undoes everything the unit wrote and drops what AfterCommit
// deferred. It returns sql.ErrTxDone once the unit has ended.
func (u *Unit) Rollback() error {
	if u.closed.Swap(true) {
		return sql.ErrTxDone
	}
	return u.tx.Rollback()
}

// AfterCommit runs f once what ctx's unit of work wrote has committed, or at
// once when ctx carries none. f gets a context without the unit, and never
// runs when the unit rolls back.
func (s *Store) AfterCommit(ctx context.Context, f func(context.Context)) {
	if u := s.unitOf(ctx); u != nil {
		u.after = append(u.after, f)
		return
	}
	f(ctx)
}

// WriterFor is where a writing statement made under ctx runs: the transaction
// of ctx's unit of work, or the writer pool.
func (s *Store) WriterFor(ctx context.Context) Querier {
	if u := s.unitOf(ctx); u != nil {
		return unitQuerier{u}
	}
	return s.writer
}

// ReaderFor is where a reading statement made under ctx runs: the transaction
// of ctx's unit of work, so the read sees what the unit wrote, or the reader
// pool.
func (s *Store) ReaderFor(ctx context.Context) Querier {
	if u := s.unitOf(ctx); u != nil {
		return unitQuerier{u}
	}
	return s.reader
}

// Begin begins a write transaction: on the writer, or, under a unit of work, as
// a savepoint inside the unit's transaction.
func (s *Store) Begin(ctx context.Context) (*Tx, error) {
	u := s.unitOf(ctx)
	if u == nil {
		tx, err := s.writer.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		return &Tx{Tx: tx, owner: s}, nil
	}
	name, err := u.savepoint(ctx)
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: u.tx, savepoint: name}, nil
}

// savepoint takes a new savepoint in the unit and returns its name.
func (u *Unit) savepoint(ctx context.Context) (string, error) {
	u.mu.Lock()
	u.savepoints++
	name := fmt.Sprintf("vh_unit_%d", u.savepoints)
	u.mu.Unlock()
	if _, err := u.tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return "", err
	}
	return name, nil
}

// unitQuerier runs statements in a unit's transaction. On Postgres a failed
// statement aborts the whole transaction, so each statement that writes runs
// under a savepoint of its own and a failure undoes only that statement, as it
// would outside a unit; SQLite is treated the same way.
type unitQuerier struct{ u *Unit }

func (q unitQuerier) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	name, err := q.u.savepoint(ctx)
	if err != nil {
		return nil, err
	}
	res, err := q.u.tx.ExecContext(ctx, query, args...)
	if err != nil {
		if _, undo := q.u.tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+name); undo != nil {
			return nil, errors.Join(err, undo)
		}
		return nil, err
	}
	if _, err := q.u.tx.ExecContext(ctx, "RELEASE SAVEPOINT "+name); err != nil {
		return nil, err
	}
	return res, nil
}

func (q unitQuerier) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return q.u.tx.QueryContext(ctx, query, args...)
}

func (q unitQuerier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return q.u.tx.QueryRowContext(ctx, query, args...)
}

// Tx is a write transaction from Begin. Inside a unit of work, Commit releases
// its savepoint, leaving the commit to the unit, and Rollback undoes back to
// it. Once either has run, both return sql.ErrTxDone.
type Tx struct {
	*sql.Tx
	owner     *Store
	savepoint string
	done      bool
}

// Commit commits the transaction, or releases its savepoint inside a unit.
func (t *Tx) Commit() error {
	if t.savepoint == "" {
		if err := t.Tx.Commit(); err != nil {
			t.owner.dropTxHooks(t.Tx)
			return err
		}
		t.owner.runTxHooks(t.Tx)
		return nil
	}
	if t.done {
		return sql.ErrTxDone
	}
	t.done = true
	_, err := t.Exec("RELEASE SAVEPOINT " + t.savepoint)
	return err
}

// Rollback rolls the transaction back, or undoes back to its savepoint inside a
// unit.
func (t *Tx) Rollback() error {
	if t.savepoint == "" {
		t.owner.dropTxHooks(t.Tx)
		return t.Tx.Rollback()
	}
	if t.done {
		return sql.ErrTxDone
	}
	t.done = true
	if _, err := t.Exec("ROLLBACK TO SAVEPOINT " + t.savepoint); err != nil {
		return err
	}
	_, err := t.Exec("RELEASE SAVEPOINT " + t.savepoint)
	return err
}

// AfterTxCommit runs f once what tx wrote has committed. Under ctx's unit of work
// that is the unit's commit (AfterCommit); otherwise tx must come from Begin,
// and f runs when its Commit succeeds and never when it rolls back. It is how a
// write that enqueues work wakes the worker only after the row is visible.
func (s *Store) AfterTxCommit(ctx context.Context, tx *sql.Tx, f func()) {
	if s.unitOf(ctx) != nil {
		s.AfterCommit(ctx, func(context.Context) { f() })
		return
	}
	hooks, _ := s.txAfter.LoadOrStore(tx, new([]func()))
	*hooks.(*[]func()) = append(*hooks.(*[]func()), f)
}

func (s *Store) runTxHooks(tx *sql.Tx) {
	if hooks, ok := s.txAfter.LoadAndDelete(tx); ok {
		for _, f := range *hooks.(*[]func()) {
			f()
		}
	}
}

func (s *Store) dropTxHooks(tx *sql.Tx) { s.txAfter.Delete(tx) }
