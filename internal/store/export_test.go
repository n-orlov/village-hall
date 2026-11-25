package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync/atomic"
)

// OpenCounting opens the SQLite database at path like Open does, but every
// statement either pool runs adds one to n. Only the store's own tests can call
// it (an external test package sees this file), so production code carries no
// hook.
func OpenCounting(path string, n *atomic.Int64) (*Store, error) {
	open := func(readOnly bool) (*sql.DB, error) {
		plain, err := sql.Open("sqlite", sqliteDSN(path, readOnly))
		if err != nil {
			return nil, err
		}
		defer func() { _ = plain.Close() }()
		return sql.OpenDB(countingConnector{dsn: sqliteDSN(path, readOnly), inner: plain.Driver(), n: n}), nil
	}
	writer, err := open(false)
	if err != nil {
		return nil, err
	}
	writer.SetMaxOpenConns(1)
	reader, err := open(true)
	if err != nil {
		return nil, err
	}
	mig, err := dialectMigrations("sqlite")
	if err != nil {
		return nil, err
	}
	return &Store{dialect: SQLite{}, writer: writer, reader: reader, migrations: mig}, nil
}

// OpenCountingPostgres opens the Postgres database at rawURL like Open does,
// but every statement any connection of its pool runs adds one to n.
func OpenCountingPostgres(rawURL string, n *atomic.Int64) (*Store, error) {
	plain, err := sql.Open("pgx", rawURL)
	if err != nil {
		return nil, err
	}
	inner := plain.Driver()
	_ = plain.Close()
	db := sql.OpenDB(countingConnector{dsn: rawURL, inner: inner, n: n})
	mig, err := dialectMigrations("postgres")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{dialect: Postgres{}, writer: db, reader: db, migrations: mig}, nil
}

type countingConnector struct {
	dsn   string
	inner driver.Driver
	n     *atomic.Int64
}

func (c countingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.inner.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return countingConn{conn, c.n}, nil
}

func (c countingConnector) Driver() driver.Driver { return c.inner }

// countingConn counts the statements run on it. The SQLite driver runs them
// directly (QueryerContext and ExecerContext), never through Prepare.
type countingConn struct {
	driver.Conn
	n *atomic.Int64
}

func (c countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.n.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.n.Add(1)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.n.Add(1)
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (c countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

// DownTo rolls the embedded migrations back to version, for the tests of a
// migration's Down.
func (s *Store) DownTo(ctx context.Context, version int64) error {
	p, err := s.provider()
	if err != nil {
		return err
	}
	_, err = p.DownTo(ctx, version)
	return err
}
