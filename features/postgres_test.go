package features

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the driver the Postgres leg's admin connection uses
	"github.com/moby/moby/api/types/network"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// postgresImage is the Postgres the leg runs against, pinned by digest (16 is
// the oldest version SPEC 12.2 supports).
const postgresImage = "postgres:16-alpine@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab"

const (
	pgUser     = "board"
	pgPassword = "board-test"
	pgAdminDB  = "postgres"
)

// siblingLabels are put on every container the suite starts, so the job that
// owns this run can reap them and nothing else can mistake them for its own.
// ci/run.sh forwards the run id as BOARD_CI_RUN_ID.
func siblingLabels() map[string]string {
	labels := map[string]string{"ralphd.role": "sibling", "board.ci": "postgres"}
	if id := strings.TrimSpace(os.Getenv("BOARD_CI_RUN_ID")); id != "" {
		labels["ralphd.run"] = id
	}
	return labels
}

// configureTestcontainers points testcontainers at the docker host the way
// ci/run.sh set the sibling up. The reaper container is off because the harness
// removes each container it starts itself and every container carries the
// sibling labels, so the job that owns the run can reap a leaked one.
func configureTestcontainers() {
	_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	if host := strings.TrimSpace(os.Getenv("BOARD_CI_DOCKER_HOST")); host != "" {
		_ = os.Setenv("TESTCONTAINERS_HOST_OVERRIDE", host)
	}
}

// pgTemplate is the database a server's migrations are applied to once, and
// newDatabase copies: CREATE DATABASE ... TEMPLATE takes a few milliseconds
// where migrating an empty database takes about 0.2 s.
const pgTemplate = "board_template"

// postgresServer is one Postgres container started through the docker socket.
type postgresServer struct {
	container testcontainers.Container
	host      string
	port      string

	adminOnce sync.Once
	admin     *sql.DB // one pool on the admin database, shared by every create and drop
	adminErr  error

	templateOnce sync.Once
	templateErr  error
}

// adminDB is the one admin pool of the server, opened on first use.
func (p *postgresServer) adminDB() (*sql.DB, error) {
	p.adminOnce.Do(func() { p.admin, p.adminErr = sql.Open("pgx", adminDSN(p.host, p.port)) })
	return p.admin, p.adminErr
}

// prepareTemplate creates pgTemplate and migrates it once, then closes its store:
// a database with a session on it cannot be copied.
func (p *postgresServer) prepareTemplate(ctx context.Context) error {
	p.templateOnce.Do(func() {
		p.templateErr = func() error {
			admin, err := p.adminDB()
			if err != nil {
				return err
			}
			if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+pgTemplate); err != nil {
				return fmt.Errorf("creating database %s: %w", pgTemplate, err)
			}
			st, err := store.Open(dsn(p.host, p.port, pgTemplate))
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

// startPostgres starts a Postgres container and waits until it accepts SQL.
// The container is reached on the docker host's published port, which
// ci/run.sh makes resolvable from the sibling.
func startPostgres(ctx context.Context) (*postgresServer, error) {
	configureTestcontainers()
	req := testcontainers.ContainerRequest{
		Image:        postgresImage,
		ExposedPorts: []string{"5432/tcp"},
		Env:          map[string]string{"POSTGRES_USER": pgUser, "POSTGRES_PASSWORD": pgPassword, "POSTGRES_DB": pgAdminDB},
		// The data directory in memory, as internal/server's test Postgres
		// has it: each scenario's CREATE DATABASE and migrations no longer
		// wait on fsyncs of the host's disk, which on a CI host busy with
		// other builds made a scenario five times slower than on SQLite and
		// ran the leg into its timeout. The harness drops each scenario's
		// database when the scenario ends, so the tmpfs stays small.
		Tmpfs:      map[string]string{"/var/lib/postgresql/data": "rw"},
		Labels:     siblingLabels(),
		WaitingFor: wait.ForSQL("5432/tcp", "pgx", func(host string, port network.Port) string { return adminDSN(host, port.Port()) }).WithStartupTimeout(90 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		if c != nil {
			_ = c.Terminate(ctx)
		}
		return nil, fmt.Errorf("starting Postgres through the docker socket: %w", err)
	}
	host, err := c.Host(ctx)
	if err == nil {
		var port string
		if p, perr := c.MappedPort(ctx, "5432/tcp"); perr != nil {
			err = perr
		} else {
			port = p.Port()
		}
		if err == nil {
			return &postgresServer{container: c, host: host, port: port}, nil
		}
	}
	_ = c.Terminate(ctx)
	return nil, err
}

func adminDSN(host, port string) string {
	return dsn(host, port, pgAdminDB)
}

func dsn(host, port, database string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(pgUser, pgPassword),
		Host:     host + ":" + port,
		Path:     "/" + database,
		RawQuery: "sslmode=disable&connect_timeout=5",
	}
	return u.String()
}

var databaseCounter atomic.Int64

// newDatabase creates a database on the server and returns its URL: a copy of
// the migrated template, so a scenario that only needs the schema skips the
// migrations. A scenario that exercises migration itself calls newEmptyDatabase.
func (p *postgresServer) newDatabase(ctx context.Context) (string, error) {
	if err := p.prepareTemplate(ctx); err != nil {
		return "", err
	}
	return p.createDatabase(ctx, " TEMPLATE "+pgTemplate)
}

// newEmptyDatabase creates a database with no table in it and returns its URL.
func (p *postgresServer) newEmptyDatabase(ctx context.Context) (string, error) {
	return p.createDatabase(ctx, "")
}

func (p *postgresServer) createDatabase(ctx context.Context, clause string) (string, error) {
	name := fmt.Sprintf("board_%d_%d", os.Getpid(), databaseCounter.Add(1))
	admin, err := p.adminDB()
	if err != nil {
		return "", err
	}
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name+clause); err != nil {
		return "", fmt.Errorf("creating database %s: %w", name, err)
	}
	return dsn(p.host, p.port, name), nil
}

// dropDatabase removes a database newDatabase created; FORCE ends any
// connection still open on it.
func (p *postgresServer) dropDatabase(ctx context.Context, databaseURL string) error {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return err
	}
	admin, err := p.adminDB()
	if err != nil {
		return err
	}
	_, err = admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+strings.TrimPrefix(u.Path, "/")+" WITH (FORCE)")
	return err
}

// stop stops the container, the way an outage of the database would.
func (p *postgresServer) stop(ctx context.Context) error {
	timeout := 10 * time.Second
	return p.container.Stop(ctx, &timeout)
}

// terminate removes the container.
func (p *postgresServer) terminate(ctx context.Context) {
	if p.admin != nil {
		_ = p.admin.Close()
	}
	_ = p.container.Terminate(ctx, testcontainers.StopTimeout(0)) // the data is in memory and thrown away
}
