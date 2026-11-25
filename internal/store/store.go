package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the Postgres database/sql driver
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // the pure-Go SQLite driver
)

// The `common` directory holds DDL that is identical in both dialects. A
// dialect directory holds only the migrations whose DDL must differ. Versions
// are unique across the directories a dialect applies.
//
//go:embed migrations/common/*.sql migrations/sqlite/*.sql migrations/postgres/*.sql
var migrationFiles embed.FS

// ErrNotMigrated is returned by Ready when the database does not carry every
// embedded migration.
var ErrNotMigrated = errors.New("database is not migrated")

// ErrSchemaNewer is wrapped by the error Migrate, CheckSchema and Ready return
// when the database records a goose version this binary does not embed.
var ErrSchemaNewer = errors.New("database schema is newer than this binary")

// SQLite settings (SPEC 12.2). Each is applied to every connection the driver
// opens, and a unit test reads all five back.
//
// wal_autocheckpoint is 10000 pages, ten times SQLite's default: the default
// checkpoint ran inside a commit on the request path every few hundred writes and
// put a 15-25 ms tail on write latency (SPEC 14). The checkpointer of
// checkpoint.go truncates the log on its own timer instead.
var sqlitePragmas = []string{
	"busy_timeout(5000)",
	"journal_mode(WAL)",
	"foreign_keys(1)",
	"synchronous(NORMAL)",
	"wal_autocheckpoint(10000)",
}

// Store is one open database. On SQLite it holds one writer connection and a
// read pool, and on Postgres both are the same pool.
type Store struct {
	dialect    Dialect
	writer     *sql.DB
	reader     *sql.DB
	migrations fs.FS
	// notify holds the kinds of alert a write enqueues; nil means alerts are off.
	notify atomic.Pointer[map[string]bool]
	// publicURL is VH_PUBLIC_URL as alerts link to it; empty when unset.
	publicURL atomic.Pointer[string]
	// notifyWake wakes the notification worker; nil while none runs.
	notifyWake atomic.Pointer[func()]
	// txAfter holds, per open writer transaction, what runs once it commits.
	txAfter sync.Map
}

// Open opens the database named by a VH_DB_URL value. It does not migrate.
// Supported: sqlite:///data/board.db and postgres://... (or postgresql://...).
func Open(rawURL string) (*Store, error) { return OpenWrapped(rawURL, nil) }

// The pools a ConnectorWrapper is asked to wrap. On SQLite the writer and the
// reader are two pools; on Postgres they are one, named PoolShared.
const (
	PoolWriter = "writer"
	PoolReader = "reader"
	PoolShared = "shared"
)

// ConnectorWrapper is the test-only hook of OpenWrapped: it receives the
// connector of one pool, named by a Pool* constant, and returns the connector
// that pool is opened on instead.
type ConnectorWrapper func(pool string, inner driver.Connector) driver.Connector

// OpenWrapped is Open with every pool opened on the connector wrap returns for
// it. A nil wrap is Open itself. It exists so a test can observe the statements
// a store issues (internal/store/storetest); production code calls Open.
func OpenWrapped(rawURL string, wrap ConnectorWrapper) (*Store, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse VH_DB_URL: %w", err)
	}
	switch u.Scheme {
	case "sqlite":
		return openSQLite(u, wrap)
	case "postgres", "postgresql":
		return openPostgres(rawURL, wrap)
	default:
		return nil, fmt.Errorf("VH_DB_URL scheme %q is not supported (want sqlite://<path> or postgres://...)", u.Scheme)
	}
}

func dialectMigrations(name string) (fs.FS, error) {
	common, err := fs.Sub(migrationFiles, "migrations/common")
	if err != nil {
		return nil, err
	}
	own, err := fs.Sub(migrationFiles, "migrations/"+name)
	if err != nil {
		return nil, err
	}
	return layered{common, own}, nil
}

func sqliteDSN(path string, readOnly bool) string {
	q := url.Values{}
	for _, p := range sqlitePragmas {
		q.Add("_pragma", p)
	}
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Add("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

// sqliteFilePath is the database file a sqlite:// URL names, with its
// directory created.
func sqliteFilePath(u *url.URL) (string, error) {
	path := filepath.Clean(u.Host + u.Path)
	if path == "." {
		return "", errors.New("VH_DB_URL names no database file")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { //nolint:gosec // the path is the operator's own VH_DB_URL, not request input
		return "", fmt.Errorf("create database directory: %w", err)
	}
	return path, nil
}

// openSQLiteWriter opens the single writer connection. Opening the writer
// first creates the file and switches it to WAL before any reader
// connection looks at it.
func openSQLiteWriter(path string, wrap ConnectorWrapper) (*sql.DB, error) {
	writer, err := openPool("sqlite", sqliteDSN(path, false), PoolWriter, wrap)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	writer.SetMaxOpenConns(1)
	if err := writer.Ping(); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	return writer, nil
}

// openPool is sql.Open of the named driver, on the connector wrap returns for
// pool when wrap is not nil.
func openPool(driverName, dsn, pool string, wrap ConnectorWrapper) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil || wrap == nil {
		return db, err
	}
	drv := db.Driver()
	_ = db.Close()
	return sql.OpenDB(wrap(pool, dsnConnector{dsn: dsn, drv: drv})), nil
}

// dsnConnector opens connections of a driver on one fixed DSN, as sql.Open does.
type dsnConnector struct {
	dsn string
	drv driver.Driver
}

func (c dsnConnector) Connect(context.Context) (driver.Conn, error) { return c.drv.Open(c.dsn) }

func (c dsnConnector) Driver() driver.Driver { return c.drv }

func openSQLite(u *url.URL, wrap ConnectorWrapper) (*Store, error) {
	path, err := sqliteFilePath(u)
	if err != nil {
		return nil, err
	}
	writer, err := openSQLiteWriter(path, wrap)
	if err != nil {
		return nil, err
	}
	reader, err := openPool("sqlite", sqliteDSN(path, true), PoolReader, wrap)
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	reader.SetMaxOpenConns(max(4, runtime.NumCPU()))
	mig, err := dialectMigrations("sqlite")
	if err != nil {
		_ = writer.Close()
		_ = reader.Close()
		return nil, err
	}
	return &Store{dialect: SQLite{}, writer: writer, reader: reader, migrations: mig}, nil
}

func openPostgres(rawURL string, wrap ConnectorWrapper) (*Store, error) {
	db, err := openPool("pgx", rawURL, PoolShared, wrap)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	mig, err := dialectMigrations("postgres")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{dialect: Postgres{}, writer: db, reader: db, migrations: mig}, nil
}

// Dialect returns the database's SQL dialect.
func (s *Store) Dialect() Dialect { return s.dialect }

// Writer is the pool for statements that write: on SQLite one connection.
func (s *Store) Writer() *sql.DB { return s.writer }

// Reader is the pool for statements that only read.
func (s *Store) Reader() *sql.DB { return s.reader }

// Close releases the database.
func (s *Store) Close() error {
	err := s.writer.Close()
	if s.reader != s.writer {
		err = errors.Join(err, s.reader.Close())
	}
	return err
}

func (s *Store) provider() (*goose.Provider, error) {
	return goose.NewProvider(s.dialect.GooseDialect(), s.writer, s.migrations)
}

// latest is the highest migration version this binary embeds.
func (s *Store) latest() (int64, error) {
	p, err := s.provider()
	if err != nil {
		return 0, fmt.Errorf("prepare migrations: %w", err)
	}
	sources := p.ListSources()
	if len(sources) == 0 {
		return 0, errors.New("no embedded migrations")
	}
	return sources[len(sources)-1].Version, nil
}

// applied is the highest applied goose version, 0 when the database has none.
// It never writes.
func (s *Store) applied(ctx context.Context) (int64, error) {
	var v sql.NullInt64
	err := s.reader.QueryRowContext(ctx, s.dialect.Rebind(
		`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = ?`), s.dialect.EncodeBool(true)).Scan(&v)
	if err != nil {
		if s.dialect.IsMissingTable(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read migration version: %w", err)
	}
	return v.Int64, nil
}

// SchemaVersion is the highest applied goose version, 0 for a database that
// has never been migrated. It never writes.
func (s *Store) SchemaVersion(ctx context.Context) (int64, error) { return s.applied(ctx) }

// CheckSchema returns an error wrapping ErrSchemaNewer when the database
// records a goose version above the highest one this binary embeds. It never
// writes, and a database with no migrations at all passes.
func (s *Store) CheckSchema(ctx context.Context) error {
	latest, err := s.latest()
	if err != nil {
		return err
	}
	applied, err := s.applied(ctx)
	if err != nil {
		return err
	}
	if applied > latest {
		return fmt.Errorf("%w: database is at version %d, this binary embeds up to %d: upgrade board",
			ErrSchemaNewer, applied, latest)
	}
	return nil
}

// Migrate applies every embedded migration that is not applied yet. It refuses
// a database whose schema is newer than the binary.
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.MigrateApplied(ctx)
	return err
}

// AppliedMigration is one migration MigrateApplied applied: its goose version,
// its file name and how long it took.
type AppliedMigration struct {
	Version  int64
	Source   string
	Duration time.Duration
}

// MigrateApplied is Migrate, and returns the migrations it applied, in the
// order it applied them (none when the database was already current).
func (s *Store) MigrateApplied(ctx context.Context) ([]AppliedMigration, error) {
	if err := s.CheckSchema(ctx); err != nil {
		return nil, err
	}
	p, err := s.provider()
	if err != nil {
		return nil, fmt.Errorf("prepare migrations: %w", err)
	}
	results, err := p.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	applied := make([]AppliedMigration, 0, len(results))
	for _, r := range results {
		applied = append(applied, AppliedMigration{Version: r.Source.Version, Source: path.Base(r.Source.Path), Duration: r.Duration})
	}
	return applied, nil
}

// MigrateTo applies the embedded migrations up to and including version, and
// no further. It lets a test build the schema of an earlier release; it
// refuses a database whose schema is newer than the binary, like Migrate.
func (s *Store) MigrateTo(ctx context.Context, version int64) error {
	if err := s.CheckSchema(ctx); err != nil {
		return err
	}
	p, err := s.provider()
	if err != nil {
		return fmt.Errorf("prepare migrations: %w", err)
	}
	if _, err := p.UpTo(ctx, version); err != nil {
		return fmt.Errorf("apply migrations up to %d: %w", version, err)
	}
	return nil
}

// Ready reports nil when the database is reachable and carries every embedded
// migration, and an error otherwise. It never writes.
func (s *Store) Ready(ctx context.Context) error {
	if err := s.reader.PingContext(ctx); err != nil {
		return fmt.Errorf("database unreachable: %w", err)
	}
	latest, err := s.latest()
	if err != nil {
		return err
	}
	applied, err := s.applied(ctx)
	if err != nil {
		return err
	}
	switch {
	case applied > latest:
		return fmt.Errorf("%w: database is at version %d, this binary embeds up to %d", ErrSchemaNewer, applied, latest)
	case applied < latest:
		return ErrNotMigrated
	}
	return nil
}
