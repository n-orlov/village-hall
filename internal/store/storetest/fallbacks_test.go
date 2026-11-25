package storetest_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"

	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
)

// bareDriver is the least a database/sql driver offers: no context methods, no
// session reset, no validity check. The recorder must still see every statement
// run through it and pass the results and the errors on.
type bareDriver struct {
	failConnect, failPrepare, failBegin bool
	execs, queries                      []string
	committed, rolledBack               int
}

type bareConn struct{ d *bareDriver }

type bareStmt struct {
	d *bareDriver
	q string
}

type bareTx struct{ d *bareDriver }

type bareRows struct{ done bool }

func (d *bareDriver) Connect(context.Context) (driver.Conn, error) {
	if d.failConnect {
		return nil, errors.New("connect refused")
	}
	return bareConn{d}, nil
}
func (d *bareDriver) Driver() driver.Driver { return nil }

func (c bareConn) Prepare(q string) (driver.Stmt, error) {
	if c.d.failPrepare {
		return nil, errors.New("prepare refused")
	}
	return bareStmt{c.d, q}, nil
}
func (c bareConn) Close() error { return nil }
func (c bareConn) Begin() (driver.Tx, error) {
	if c.d.failBegin {
		return nil, errors.New("begin refused")
	}
	return bareTx(c), nil
}

func (s bareStmt) Close() error  { return nil }
func (s bareStmt) NumInput() int { return -1 }
func (s bareStmt) Exec([]driver.Value) (driver.Result, error) {
	s.d.execs = append(s.d.execs, s.q)
	return driver.RowsAffected(1), nil
}
func (s bareStmt) Query([]driver.Value) (driver.Rows, error) {
	s.d.queries = append(s.d.queries, s.q)
	return &bareRows{}, nil
}

func (t bareTx) Commit() error   { t.d.committed++; return nil }
func (t bareTx) Rollback() error { t.d.rolledBack++; return nil }

func (r *bareRows) Columns() []string { return []string{"x"} }
func (r *bareRows) Close() error      { return nil }
func (r *bareRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done, dest[0] = true, int64(7)
	return nil
}

func openBare(d *bareDriver) (*sql.DB, *storetest.Recorder) {
	rec := &storetest.Recorder{}
	return sql.OpenDB(rec.Wrapper()(store.PoolWriter, d)), rec
}

func TestAStatementThroughADriverWithOnlyTheBareInterfacesIsRecordedAndAnswered(t *testing.T) {
	d := &bareDriver{}
	db, rec := openBare(d)
	defer func() { _ = db.Close() }()
	if res, err := db.ExecContext(ctx, `UPDATE a SET x = 1`); err != nil {
		t.Fatal(err)
	} else if n, _ := res.RowsAffected(); n != 1 {
		t.Errorf("rows affected = %d, want the driver's 1", n)
	}
	var x int
	if err := db.QueryRowContext(ctx, `SELECT x FROM a`).Scan(&x); err != nil || x != 7 {
		t.Fatalf("query = %d, %v, want the driver's 7", x, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM a`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if d.committed != 1 || d.rolledBack != 1 {
		t.Errorf("the driver saw %d commits and %d rollbacks, want 1 and 1", d.committed, d.rolledBack)
	}
	var kinds []storetest.Kind
	for _, s := range rec.All() {
		if s.Pool != store.PoolWriter {
			t.Errorf("%+v is not on the pool the wrapper was given", s)
		}
		kinds = append(kinds, s.Kind)
	}
	want := []storetest.Kind{storetest.Exec, storetest.Query, storetest.Begin, storetest.Exec, storetest.Commit, storetest.Begin, storetest.Rollback}
	if len(kinds) != len(want) {
		t.Fatalf("recorded %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("recorded %v, want %v", kinds, want)
		}
	}
	if len(d.execs) != 2 || len(d.queries) != 1 {
		t.Errorf("the driver ran %v and %v", d.execs, d.queries)
	}
}

func TestAFailureOfTheDriverIsPassedOnAndRecordsNothingItDidNotRun(t *testing.T) {
	for name, d := range map[string]*bareDriver{
		"connect": {failConnect: true},
		"prepare": {failPrepare: true},
	} {
		db, rec := openBare(d)
		if _, err := db.ExecContext(ctx, `UPDATE a SET x = 1`); err == nil {
			t.Errorf("%s: the driver's failure was swallowed", name)
		}
		if n := rec.Statements(""); n != 0 {
			t.Errorf("%s: %d statements recorded for one that never ran", name, n)
		}
		_ = db.Close()
	}
	d := &bareDriver{failBegin: true}
	db, rec := openBare(d)
	defer func() { _ = db.Close() }()
	if _, err := db.BeginTx(ctx, nil); err == nil {
		t.Error("begin: the driver's failure was swallowed")
	}
	if n := rec.Count("", storetest.Begin); n != 0 {
		t.Errorf("a begin that failed was recorded %d times", n)
	}
}

func TestAPreparedStatementRecordsEachExecutionOnItsOwn(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	w := st.Writer()
	if _, err := w.ExecContext(ctx, `CREATE TABLE probe (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	rec.Reset()
	p, err := w.PrepareContext(ctx, `INSERT INTO probe (x) VALUES (?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := p.ExecContext(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	_ = p.Close()
	if n := rec.Count(store.PoolWriter, storetest.Exec); n != 3 {
		t.Errorf("3 executions of one prepared INSERT recorded as %d: %+v", n, rec.All())
	}
	if _, err := w.PrepareContext(ctx, `INSERT INTO no_such_table (x) VALUES (?)`); err == nil {
		t.Error("preparing a bad statement succeeded")
	}
	if n := rec.Statements(""); n != 3 {
		t.Errorf("a statement that failed to prepare was recorded: %d records", n)
	}
}
