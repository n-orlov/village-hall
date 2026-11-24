package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// postgresImage is the same pinned image the features' Postgres leg runs.
const postgresImage = "postgres:16-alpine@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab"

// pgServer is the Postgres container of one test, started through the docker
// socket the way features/postgres_test.go does.
type pgServer struct {
	host, port string
	count      atomic.Int64
	admin      *sql.DB // one pool on the admin database, shared by every create

	templateOnce sync.Once
	templateErr  error
}

// pgTemplate is the database the container's migrations are applied to once, and
// newPostgresDatabase copies: CREATE DATABASE ... TEMPLATE takes a few
// milliseconds where migrating an empty database takes about 0.2 s.
const pgTemplate = "board_template"

var testPostgres atomic.Pointer[pgServer]

func pgDSN(host, port, db string) string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword("board", "board-test"), Host: host + ":" + port, Path: "/" + db,
		RawQuery: "sslmode=disable&connect_timeout=5"}
	return u.String()
}

// withPostgres starts the container, runs fn with it reachable through
// newPostgresDatabase, and removes it. It fails a test that asks for it when
// legsOf(t) has no Postgres leg, so a run on BOARD_DB_LEG=sqlite (mutation's
// covered and mutant runs) never starts a container.
func withPostgres(t *testing.T, fn func()) {
	t.Helper()
	if !slices.Contains(legsOf(t), "postgres") {
		t.Fatalf("withPostgres on BOARD_DB_LEG=%q: run the test through eachLeg", os.Getenv("BOARD_DB_LEG"))
	}
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
		Env:          map[string]string{"POSTGRES_USER": "board", "POSTGRES_PASSWORD": "board-test", "POSTGRES_DB": "postgres"},
		Tmpfs:        map[string]string{"/var/lib/postgresql/data": "rw"},
		Labels:       labels,
		WaitingFor: wait.ForSQL("5432/tcp", "pgx", func(host string, port network.Port) string { return pgDSN(host, port.Port(), "postgres") }).
			WithStartupTimeout(90 * time.Second),
	}})
	if c != nil {
		defer func() { _ = c.Terminate(context.Background(), testcontainers.StopTimeout(0)) }()
	}
	if err != nil {
		t.Fatalf("starting Postgres through the docker socket: %v", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", pgDSN(host, port.Port(), "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	testPostgres.Store(&pgServer{host: host, port: port.Port(), admin: admin})
	defer testPostgres.Store(nil)
	fn()
}

// prepareTemplate creates pgTemplate and migrates it once per container, then
// closes its store: a database with a session on it cannot be copied.
func (p *pgServer) prepareTemplate(ctx context.Context) error {
	p.templateOnce.Do(func() {
		p.templateErr = func() error {
			if _, err := p.admin.ExecContext(ctx, "CREATE DATABASE "+pgTemplate); err != nil {
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
		}()
	})
	return p.templateErr
}

// newPostgresDatabase creates a database on the container withPostgres started,
// as a copy of the template the container's migrations were applied to once, and
// returns its URL. The tests of the migrations themselves (MigrateTo) run on
// SQLite and open an empty database of their own.
func newPostgresDatabase(t *testing.T) string {
	t.Helper()
	p := testPostgres.Load()
	if p == nil {
		t.Fatal("no Postgres container: call withPostgres first")
	}
	if err := p.prepareTemplate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return p.create(t, " TEMPLATE "+pgTemplate)
}

func (p *pgServer) create(t *testing.T, clause string) string {
	t.Helper()
	name := fmt.Sprintf("board_%d_%d", os.Getpid(), p.count.Add(1))
	if _, err := p.admin.ExecContext(context.Background(), "CREATE DATABASE "+name+clause); err != nil {
		t.Fatalf("creating database %s: %v", name, err)
	}
	return pgDSN(p.host, p.port, name)
}

// legsOf is the databases a test runs against: BOARD_DB_LEG names one, and an
// unset variable means both, as the whole-suite Go pass runs it.
func legsOf(t *testing.T) []string {
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

// eachLeg runs fn as a subtest on every leg, with Postgres started when it is one.
func eachLeg(t *testing.T, fn func(t *testing.T, leg string)) {
	t.Helper()
	run := func() {
		for _, leg := range legsOf(t) {
			t.Run(leg, func(t *testing.T) { fn(t, leg) })
		}
	}
	for _, leg := range legsOf(t) {
		if leg == "postgres" {
			withPostgres(t, run)
			return
		}
	}
	run()
}
