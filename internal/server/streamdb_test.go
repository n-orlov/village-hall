package server

import (
	"context"
	"database/sql"
	goflag "flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the driver the admin connection uses
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/n-orlov/village-hall/internal/store"
)

// postgresImage is the same pinned image the features' Postgres leg runs.
const postgresImage = "postgres:16-alpine@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab"

// legs are the databases a test runs against: BOARD_DB_LEG names one, and an
// unset variable means both, as the whole-suite Go pass runs it.
func legs(t *testing.T) []string {
	t.Helper()
	switch leg := strings.TrimSpace(os.Getenv("BOARD_DB_LEG")); leg {
	case "":
		return []string{"sqlite", "postgres"}
	case "sqlite", "postgres":
		return []string{leg}
	default:
		t.Fatalf("BOARD_DB_LEG=%q: want sqlite or postgres", leg)
		return nil
	}
}

// hasLeg reports whether leg is one of legs(t).
func hasLeg(t *testing.T, leg string) bool {
	t.Helper()
	for _, l := range legs(t) {
		if l == leg {
			return true
		}
	}
	return false
}

// onPostgres runs fn as the test's "postgres" subtest when the Postgres leg is
// one of legs(t). A test of two servers on one Postgres database has no SQLite
// case, so on BOARD_DB_LEG=sqlite it runs no subtest and starts no container;
// the Suite's Go pass, with the variable unset, runs it.
func onPostgres(t *testing.T, fn func(t *testing.T, dbs *postgresDatabases)) {
	t.Helper()
	if hasLeg(t, "postgres") {
		t.Run("postgres", func(t *testing.T) { fn(t, startPostgres(t)) })
	}
}

// databases hands out databases of one leg.
type databases interface {
	// New returns the URL of a database a test may Migrate: empty on SQLite,
	// and on Postgres a copy of a database the binary's migrations are already
	// applied to, which Migrate leaves as it is.
	New(t *testing.T) string
}

type sqliteDatabases struct{}

func (sqliteDatabases) New(t *testing.T) string {
	t.Helper()
	return "sqlite://" + filepath.Join(t.TempDir(), "b.db")
}

// postgresDatabases is one Postgres container started through the docker socket
// the way features/postgres_test.go does, with one database per call.
type postgresDatabases struct {
	host, port string
	count      atomic.Int64
	admin      *sql.DB // one pool on the admin database, shared by every New and drop
}

// pgTemplate is the database the container's migrations are applied to once, and
// every test's database is created from: CREATE DATABASE ... TEMPLATE copies its
// files in a few milliseconds where migrating an empty database takes ~0.2 s.
const pgTemplate = "board_template"

const (
	pgUser     = "board"
	pgPassword = "board-test"
	pgAdminDB  = "postgres"
)

func pgDSN(host, port, database string) string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword(pgUser, pgPassword), Host: host + ":" + port, Path: "/" + database,
		RawQuery: "sslmode=disable&connect_timeout=5"}
	return u.String()
}

var (
	sharedPostgres     *postgresDatabases
	sharedPostgresDone func()
	sharedPostgresOnce sync.Once
	sharedPostgresErr  error
)

// TestMain registers the SQLite function and connection hook the hold tests
// use, before any (parallel) test opens a database, and removes the Postgres
// container the tests of this package share.
func TestMain(m *testing.M) {
	raiseParallelWhenUnset()
	installSQLiteHoldHook()
	if err := installMarkFunc(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	if sharedPostgresDone != nil {
		sharedPostgresDone()
	}
	os.Exit(code)
}

// sleepBoundParallelFactor is how many parallel tests this package lets run per
// CPU. Most of its time is real-clock windows (the ask waits, the feed's quiet
// periods, the hold-and-release races), during which a test holds a slot of
// go test's -parallel and no CPU: at the default of one slot per CPU a unit
// capped at 4 CPUs ran 97 s of test time in 4 slots (29 s in build 305).
const sleepBoundParallelFactor = 2

// raiseParallelWhenUnset gives the package sleepBoundParallelFactor slots per
// CPU unless a caller named -parallel itself (ci/stability.sh's run at
// -parallel=1 keeps meaning one test at a time). It changes how many tests
// overlap, never a window, a bound or an assertion.
func raiseParallelWhenUnset() {
	// the testing flags are parsed by m.Run, after TestMain starts: an explicit
	// -parallel is read from the arguments go test passed
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-test.parallel") || strings.HasPrefix(a, "--test.parallel") {
			return
		}
	}
	if f := goflag.Lookup("test.parallel"); f != nil {
		_ = f.Value.Set(strconv.Itoa(sleepBoundParallelFactor * runtime.GOMAXPROCS(0)))
	}
}

// startPostgres returns the Postgres container the tests of this package share,
// starting it on first use. It fails a test that asks for it when legs(t) has no
// Postgres leg, so a run on BOARD_DB_LEG=sqlite (mutation's covered and mutant
// runs) never starts a container.
func startPostgres(t *testing.T) *postgresDatabases {
	t.Helper()
	if !hasLeg(t, "postgres") {
		t.Fatalf("startPostgres on BOARD_DB_LEG=%q: run the test through onPostgres or forEachLeg", os.Getenv("BOARD_DB_LEG"))
	}
	sharedPostgresOnce.Do(func() { sharedPostgres, sharedPostgresDone, sharedPostgresErr = runPostgres() })
	if sharedPostgresErr != nil {
		t.Fatal(sharedPostgresErr)
	}
	return sharedPostgres
}

// runPostgres starts a container through the docker socket and returns it with
// the function that removes it.
func runPostgres() (*postgresDatabases, func(), error) {
	ctx := context.Background()
	_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	if host := strings.TrimSpace(os.Getenv("BOARD_CI_DOCKER_HOST")); host != "" {
		_ = os.Setenv("TESTCONTAINERS_HOST_OVERRIDE", host)
	}
	labels := map[string]string{"ralphd.role": "sibling", "board.ci": "postgres"}
	if id := strings.TrimSpace(os.Getenv("BOARD_CI_RUN_ID")); id != "" {
		labels["ralphd.run"] = id
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{Started: true, ContainerRequest: testcontainers.ContainerRequest{
		Image:        postgresImage,
		ExposedPorts: []string{"5432/tcp"},
		Env:          map[string]string{"POSTGRES_USER": pgUser, "POSTGRES_PASSWORD": pgPassword, "POSTGRES_DB": pgAdminDB},
		// the package's tests run in parallel, each with its own database and
		// pools, which can outgrow the default of 100 connections
		Cmd: []string{"postgres", "-c", "max_connections=500"},
		// the data directory in memory: CREATE DATABASE (once per test and
		// leg) is several times faster, and the container is gone at once
		// when killed; New drops each database when its test ends
		Tmpfs:  map[string]string{"/var/lib/postgresql/data": "rw"},
		Labels: labels,
		WaitingFor: wait.ForSQL("5432/tcp", "pgx", func(host string, port network.Port) string { return pgDSN(host, port.Port(), pgAdminDB) }).
			WithStartupTimeout(90 * time.Second),
	}})
	// The container is thrown away with every database in it, so it is killed
	// rather than stopped: a graceful stop took over 10 s, the longest step of
	// the package's whole run.
	terminate := func() {
		if c != nil {
			_ = c.Terminate(context.Background(), testcontainers.StopTimeout(0))
		}
	}
	if err != nil {
		terminate()
		return nil, nil, fmt.Errorf("starting Postgres through the docker socket: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		terminate()
		return nil, nil, err
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		terminate()
		return nil, nil, err
	}
	p := &postgresDatabases{host: host, port: port.Port()}
	if err := p.prepare(ctx); err != nil {
		terminate()
		return nil, nil, err
	}
	return p, func() { _ = p.admin.Close(); terminate() }, nil
}

// prepare opens the shared admin pool and builds the template database: created,
// migrated and closed again, since a database with a session on it cannot be
// copied.
func (p *postgresDatabases) prepare(ctx context.Context) error {
	admin, err := sql.Open("pgx", pgDSN(p.host, p.port, pgAdminDB))
	if err != nil {
		return err
	}
	p.admin = admin
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+pgTemplate); err != nil {
		return fmt.Errorf("creating database %s: %w", pgTemplate, err)
	}
	st, err := store.Open(pgDSN(p.host, p.port, pgTemplate))
	if err != nil {
		return fmt.Errorf("opening database %s: %w", pgTemplate, err)
	}
	if err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		return fmt.Errorf("migrating database %s: %w", pgTemplate, err)
	}
	return st.Close()
}

func (p *postgresDatabases) New(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("board_%d_%d", os.Getpid(), p.count.Add(1))
	if _, err := p.admin.ExecContext(context.Background(), "CREATE DATABASE "+name+" TEMPLATE "+pgTemplate); err != nil {
		t.Fatalf("creating database %s: %v", name, err)
	}
	// registered before the test opens its store, so it runs after the store
	// is closed; FORCE ends any connection the test left behind
	t.Cleanup(func() { p.drop(t, name) })
	return pgDSN(p.host, p.port, name)
}

// drop removes the database name, so the in-memory data directory holds only
// the databases of the tests running now.
func (p *postgresDatabases) drop(t *testing.T, name string) {
	if _, err := p.admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
		t.Errorf("dropping database %s: %v", name, err)
	}
}

// forEachLeg runs fn as a parallel subtest on every leg legs names, with the
// leg's databases. The Postgres container is shared by the package's tests and
// removed when they have all run.
func forEachLeg(t *testing.T, fn func(t *testing.T, leg string, dbs databases)) {
	t.Helper()
	runLegs(t, true, fn)
}

// forEachLegInOrder is forEachLeg with the legs run one after the other, for a
// test that measures the whole process (its goroutines) and so must run alone.
func forEachLegInOrder(t *testing.T, fn func(t *testing.T, leg string, dbs databases)) {
	t.Helper()
	runLegs(t, false, fn)
}

func runLegs(t *testing.T, parallel bool, fn func(t *testing.T, leg string, dbs databases)) {
	t.Helper()
	for _, leg := range legs(t) {
		var dbs databases = sqliteDatabases{}
		if leg == "postgres" {
			dbs = startPostgres(t)
		}
		t.Run(leg, func(t *testing.T) {
			if parallel {
				t.Parallel()
			}
			fn(t, leg, dbs)
		})
	}
}
