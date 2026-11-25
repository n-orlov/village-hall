// Package storetest is a test helper: it opens a store whose every statement is
// recorded, so a test can pin how many statements a store call issues, on which
// pool, and inside which transaction. Only tests import it.
package storetest

import (
	"context"
	"database/sql/driver"
	"path/filepath"
	"sync"
	"testing"

	"github.com/n-orlov/village-hall/internal/store"
)

// Kind is what a recorded Statement is.
type Kind string

// The kinds of statement a Recorder records. Begin, Commit and Rollback are the
// transaction boundaries of a writer (or reader) transaction; their SQL is the
// word itself, the driver issues the real BEGIN (IMMEDIATE on the SQLite
// writer) inside the call.
const (
	Query    Kind = "query"
	Exec     Kind = "exec"
	Begin    Kind = "begin"
	Commit   Kind = "commit"
	Rollback Kind = "rollback"
)

// Statement is one statement or transaction boundary a store issued.
type Statement struct {
	SQL string
	// Pool is store.PoolWriter, store.PoolReader or, on Postgres where both
	// are one pool, store.PoolShared.
	Pool string
	Kind Kind
	// InTx is true for a statement run, or a Commit or Rollback issued, on a
	// connection with a transaction open.
	InTx bool
}

// Recorder collects the statements of one store. It is safe for concurrent use.
type Recorder struct {
	mu   sync.Mutex
	list []Statement
}

func (r *Recorder) add(s Statement) {
	r.mu.Lock()
	r.list = append(r.list, s)
	r.mu.Unlock()
}

// Reset forgets everything recorded so far, so a test measures one call.
func (r *Recorder) Reset() {
	r.mu.Lock()
	r.list = nil
	r.mu.Unlock()
}

// All is every record since the last Reset, in the order issued.
func (r *Recorder) All() []Statement {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Statement(nil), r.list...)
}

// Count is the number of records of kind on pool since the last Reset. An empty
// pool matches every pool.
func (r *Recorder) Count(pool string, kind Kind) int {
	n := 0
	for _, s := range r.All() {
		if (pool == "" || s.Pool == pool) && s.Kind == kind {
			n++
		}
	}
	return n
}

// Statements is the number of Query and Exec records on pool since the last
// Reset (transaction boundaries excluded). An empty pool matches every pool.
func (r *Recorder) Statements(pool string) int {
	return r.Count(pool, Query) + r.Count(pool, Exec)
}

// Wrapper is the store.ConnectorWrapper that records into r.
func (r *Recorder) Wrapper() store.ConnectorWrapper {
	return func(pool string, inner driver.Connector) driver.Connector {
		return connector{Connector: inner, pool: pool, rec: r}
	}
}

// Open opens the database at rawURL (sqlite://... or postgres://...) with every
// statement of both pools recorded. It does not migrate.
func Open(rawURL string) (*store.Store, *Recorder, error) {
	rec := &Recorder{}
	st, err := store.OpenWrapped(rawURL, rec.Wrapper())
	if err != nil {
		return nil, nil, err
	}
	return st, rec, nil
}

// OpenMigrated opens a fresh SQLite store in the test's temp directory, applies
// every embedded migration, and returns it with a Recorder that holds nothing
// yet: the migration's own statements are not recorded.
func OpenMigrated(t testing.TB) (*store.Store, *Recorder) {
	t.Helper()
	st, rec, err := Open("sqlite://" + filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec.Reset()
	return st, rec
}

type connector struct {
	driver.Connector
	pool string
	rec  *Recorder
}

func (c connector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &recConn{Conn: conn, pool: c.pool, rec: c.rec}, nil
}

// recConn records what runs on one connection. database/sql sees only the
// interfaces it implements, so each one the drivers offer is forwarded.
type recConn struct {
	driver.Conn
	pool string
	rec  *Recorder
	inTx bool
}

func (c *recConn) note(sql string, kind Kind) {
	c.rec.add(Statement{SQL: sql, Pool: c.pool, Kind: kind, InTx: c.inTx})
}

func (c *recConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	c.note(query, Query)
	return q.QueryContext(ctx, query, args)
}

func (c *recConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	c.note(query, Exec)
	return e.ExecContext(ctx, query, args)
}

func (c *recConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var st driver.Stmt
	var err error
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		st, err = p.PrepareContext(ctx, query)
	} else {
		st, err = c.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &stmt{Stmt: st, query: query, c: c}, nil
}

func (c *recConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var tx driver.Tx
	var err error
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = b.BeginTx(ctx, opts)
	} else {
		tx, err = c.Conn.Begin() //nolint:staticcheck // the fallback for a driver without ConnBeginTx
	}
	if err != nil {
		return nil, err
	}
	c.note("BEGIN", Begin)
	c.inTx = true
	return &txn{Tx: tx, c: c}, nil
}

func (c *recConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *recConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

type txn struct {
	driver.Tx
	c *recConn
}

func (t *txn) Commit() error {
	t.c.note("COMMIT", Commit)
	t.c.inTx = false
	return t.Tx.Commit()
}

func (t *txn) Rollback() error {
	t.c.note("ROLLBACK", Rollback)
	t.c.inTx = false
	return t.Tx.Rollback()
}

// stmt records each execution of a prepared statement, not its preparation.
type stmt struct {
	driver.Stmt
	query string
	c     *recConn
}

func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	s.c.note(s.query, Exec)
	if e, ok := s.Stmt.(driver.StmtExecContext); ok {
		return e.ExecContext(ctx, args)
	}
	return s.Stmt.Exec(values(args)) //nolint:staticcheck // the fallback for a driver without StmtExecContext
}

func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	s.c.note(s.query, Query)
	if q, ok := s.Stmt.(driver.StmtQueryContext); ok {
		return q.QueryContext(ctx, args)
	}
	return s.Stmt.Query(values(args)) //nolint:staticcheck // the fallback for a driver without StmtQueryContext
}

func values(args []driver.NamedValue) []driver.Value {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		out[i] = a.Value
	}
	return out
}
