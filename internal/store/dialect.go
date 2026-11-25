package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
	"modernc.org/sqlite"
)

// Dialect is the small set of differences between the supported databases
// that hand-written portable SQL still has to care about (SPEC 12.2). Queries
// are written once with `?` placeholders and passed through Rebind.
type Dialect interface {
	// Name is "sqlite" or "postgres".
	Name() string
	// GooseDialect is the goose dialect that applies this database's migrations.
	GooseDialect() goose.Dialect
	// Placeholder is the 1-based positional parameter marker.
	Placeholder(i int) string
	// Rebind rewrites `?` markers outside string literals into Placeholder form.
	Rebind(query string) string
	// Insert builds INSERT INTO table (cols) VALUES (?, ...) with `?` markers.
	Insert(table string, cols []string) string
	// InsertReturning is Insert followed by RETURNING returning.
	InsertReturning(table string, cols []string, returning ...string) string
	// InsertSet builds INSERT INTO table (cols) from a single `?` parameter:
	// a JSON array of one object per row, keyed by column name, whose rows are
	// inserted in array order. It is one statement and one parameter however
	// many rows the array holds.
	InsertSet(table string, cols []string) string
	// InSet is a subquery of the strings of a single `?` parameter, a JSON
	// array of strings (TextSet), for `col IN (InSet())`: one parameter however
	// many values the array holds.
	InSet() string
	// RefSet is a subquery of the (slug, seq) columns of a single `?`
	// parameter, a JSON array of [slug, seq] pairs (RefSetParam): one parameter
	// however many refs the array holds.
	RefSet() string
	// Upsert builds an insert that, on a conflict over the conflict columns,
	// updates the named columns from the proposed row.
	Upsert(table string, cols, conflict, update []string) string
	// PartialIndex builds CREATE [UNIQUE] INDEX ... WHERE where.
	PartialIndex(name, table string, cols []string, where string, unique bool) string
	// EncodeBool is the bound value for a boolean column.
	EncodeBool(v bool) any
	// SupportsListenNotify reports whether LISTEN/NOTIFY exists.
	SupportsListenNotify() bool
	// LockEventWrite is the statement a transaction runs right before it
	// inserts into events, or "" when the database needs none (SPEC 7.3). On
	// Postgres it takes a shared transaction lock, so EventWriters can name every
	// transaction that may hold an uncommitted event number.
	LockEventWrite() string
	// LockRow is the clause a SELECT ends with to lock the rows of the table
	// alias names until the transaction ends, or "" when the database needs none.
	// On Postgres it is FOR UPDATE, so a concurrent transaction that locks the
	// same row waits, and then reads what the winner committed. SQLite has one
	// writer at a time, so its writes are already serial.
	LockRow(alias string) string
	// EventWriters is a query whose rows name, one text column each, the
	// transactions that currently hold the LockEventWrite lock, or "" when the
	// database has no such writers.
	EventWriters() string
	// IsMissingTable reports whether err says a table does not exist.
	IsMissingTable(err error) bool
	// IsUniqueViolation reports whether err is a unique or primary key violation.
	IsUniqueViolation(err error) bool
}

// EncodeTime is the stored form of a timestamp in every dialect: UTC
// milliseconds since the Unix epoch.
func EncodeTime(t time.Time) int64 { return t.UTC().UnixMilli() }

// EncodeBound is the stored form of an instant that a stored timestamp is
// compared with: the smallest whole millisecond not before t. A stored value s
// is strictly earlier than t exactly when s < EncodeBound(t), and not earlier
// exactly when s >= EncodeBound(t). EncodeTime floors, so using it for that
// comparison would call a value stored one millisecond before t "not earlier"
// whenever t holds a sub-millisecond part. The stored precision stays one
// millisecond.
func EncodeBound(t time.Time) int64 {
	t = t.UTC()
	ms := t.UnixMilli()
	if time.UnixMilli(ms).Before(t) {
		ms++
	}
	return ms
}

// DecodeTime is the inverse of EncodeTime.
func DecodeTime(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// SQLite is the Dialect for modernc.org/sqlite.
type SQLite struct{}

// Postgres is the Dialect for pgx.
type Postgres struct{}

// Name implements Dialect.
func (SQLite) Name() string { return "sqlite" }

// Name implements Dialect.
func (Postgres) Name() string { return "postgres" }

// GooseDialect implements Dialect.
func (SQLite) GooseDialect() goose.Dialect { return goose.DialectSQLite3 }

// GooseDialect implements Dialect.
func (Postgres) GooseDialect() goose.Dialect { return goose.DialectPostgres }

// Placeholder implements Dialect.
func (SQLite) Placeholder(int) string { return "?" }

// Placeholder implements Dialect.
func (Postgres) Placeholder(i int) string { return "$" + strconv.Itoa(i) }

// Rebind implements Dialect.
func (SQLite) Rebind(q string) string { return q }

// Rebind implements Dialect.
func (p Postgres) Rebind(q string) string {
	var b strings.Builder
	n, quoted := 0, false
	for i := 0; i < len(q); i++ {
		switch c := q[i]; {
		case c == '\'':
			quoted = !quoted
			b.WriteByte(c)
		case c == '?' && !quoted:
			n++
			b.WriteString(p.Placeholder(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// InSet implements Dialect.
func (SQLite) InSet() string { return "SELECT value FROM json_each(?)" }

// InSet implements Dialect.
func (Postgres) InSet() string { return "SELECT jsonb_array_elements_text(?::jsonb)" }

// RefSet implements Dialect.
func (SQLite) RefSet() string {
	return "SELECT json_extract(value, '$[0]') AS slug, json_extract(value, '$[1]') AS seq FROM json_each(?)"
}

// RefSet implements Dialect.
func (Postgres) RefSet() string {
	return "SELECT e->>0 AS slug, (e->>1)::integer AS seq FROM jsonb_array_elements(?::jsonb) AS e"
}

// TextSet is the parameter of Dialect.InSet: values as a JSON array, [] for none.
func TextSet(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(values) // a []string always marshals
	return string(b)
}

// InsertSet implements Dialect: json_each walks the array, key is the index.
func (SQLite) InsertSet(table string, cols []string) string {
	vals := make([]string, len(cols))
	for i, c := range cols {
		vals[i] = fmt.Sprintf(`json_extract(value, '$."%s"')`, c)
	}
	return fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM json_each(?) ORDER BY key", table, strings.Join(cols, ", "), strings.Join(vals, ", "))
}

// InsertSet implements Dialect: jsonb_populate_recordset types each value by
// the table's own column.
func (Postgres) InsertSet(table string, cols []string) string {
	vals := make([]string, len(cols))
	for i, c := range cols {
		vals[i] = "r." + c
	}
	return fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM jsonb_populate_recordset(NULL::%s, ?::jsonb) WITH ORDINALITY AS r ORDER BY r.ordinality",
		table, strings.Join(cols, ", "), strings.Join(vals, ", "), table)
}

func insert(table string, cols []string) string {
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(cols, ", "), marks)
}

func returningClause(returning []string) string {
	if len(returning) == 0 {
		return ""
	}
	return " RETURNING " + strings.Join(returning, ", ")
}

func upsert(table string, cols, conflict, update []string) string {
	sets := make([]string, len(update))
	for i, c := range update {
		sets[i] = c + " = excluded." + c
	}
	action := "DO NOTHING"
	if len(sets) > 0 {
		action = "DO UPDATE SET " + strings.Join(sets, ", ")
	}
	return insert(table, cols) + " ON CONFLICT (" + strings.Join(conflict, ", ") + ") " + action
}

func partialIndex(name, table string, cols []string, where string, unique bool) string {
	kind := "INDEX"
	if unique {
		kind = "UNIQUE INDEX"
	}
	return fmt.Sprintf("CREATE %s %s ON %s (%s) WHERE %s", kind, name, table, strings.Join(cols, ", "), where)
}

// Insert implements Dialect.
func (SQLite) Insert(table string, cols []string) string { return insert(table, cols) }

// Insert implements Dialect.
func (Postgres) Insert(table string, cols []string) string { return insert(table, cols) }

// InsertReturning implements Dialect (SQLite 3.35+ supports RETURNING).
func (SQLite) InsertReturning(table string, cols []string, returning ...string) string {
	return insert(table, cols) + returningClause(returning)
}

// InsertReturning implements Dialect.
func (Postgres) InsertReturning(table string, cols []string, returning ...string) string {
	return insert(table, cols) + returningClause(returning)
}

// Upsert implements Dialect.
func (SQLite) Upsert(table string, cols, conflict, update []string) string {
	return upsert(table, cols, conflict, update)
}

// Upsert implements Dialect.
func (Postgres) Upsert(table string, cols, conflict, update []string) string {
	return upsert(table, cols, conflict, update)
}

// PartialIndex implements Dialect.
func (SQLite) PartialIndex(name, table string, cols []string, where string, unique bool) string {
	return partialIndex(name, table, cols, where, unique)
}

// PartialIndex implements Dialect.
func (Postgres) PartialIndex(name, table string, cols []string, where string, unique bool) string {
	return partialIndex(name, table, cols, where, unique)
}

// EncodeBool implements Dialect: SQLite has no boolean type and stores 0 or 1.
func (SQLite) EncodeBool(v bool) any {
	if v {
		return 1
	}
	return 0
}

// EncodeBool implements Dialect.
func (Postgres) EncodeBool(v bool) any { return v }

// SupportsListenNotify implements Dialect.
func (SQLite) SupportsListenNotify() bool { return false }

// SupportsListenNotify implements Dialect.
func (Postgres) SupportsListenNotify() bool { return true }

// eventLockKey is the Postgres advisory lock key of event writes ("bdv1"). It is
// below 2^32, so pg_locks shows it as classid 0, objid eventLockKey, objsubid 1.
const eventLockKey = 0x62647631

// LockEventWrite implements Dialect: SQLite has one writer at a time, so an
// event number is never pending behind a committed higher one.
func (SQLite) LockEventWrite() string { return "" }

// LockEventWrite implements Dialect.
func (Postgres) LockEventWrite() string {
	return fmt.Sprintf("SELECT pg_advisory_xact_lock_shared(%d)", eventLockKey)
}

// EventWriters implements Dialect.
func (SQLite) EventWriters() string { return "" }

// EventWriters implements Dialect.
func (Postgres) EventWriters() string {
	return fmt.Sprintf(`SELECT virtualtransaction FROM pg_locks
WHERE locktype = 'advisory' AND granted AND classid = 0 AND objid = %d AND objsubid = 1
AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, eventLockKey)
}

// IsMissingTable implements Dialect.
func (SQLite) IsMissingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// IsMissingTable implements Dialect (SQLSTATE 42P01, undefined_table).
func (Postgres) IsMissingTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

// SQLite result codes for a UNIQUE and a PRIMARY KEY constraint failure.
const (
	sqliteConstraintPrimaryKey = 1555
	sqliteConstraintUnique     = 2067
)

// IsUniqueViolation implements Dialect.
func (SQLite) IsUniqueViolation(err error) bool {
	var sErr *sqlite.Error
	if !errors.As(err, &sErr) {
		return false
	}
	return sErr.Code() == sqliteConstraintUnique || sErr.Code() == sqliteConstraintPrimaryKey
}

// IsUniqueViolation implements Dialect (SQLSTATE 23505, unique_violation).
func (Postgres) IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// LockRow implements Dialect: SQLite has one writer at a time.
func (SQLite) LockRow(string) string { return "" }

// LockRow implements Dialect.
func (Postgres) LockRow(alias string) string { return " FOR UPDATE OF " + alias }
