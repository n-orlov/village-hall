package feed

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the driver the admin connection uses
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/events"
	"github.com/n-orlov/village-hall/internal/store"
)

// postgresImage is the same pinned image the features' Postgres leg runs.
const postgresImage = "postgres:16-alpine@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab"

// onPostgres runs fn as the test's "postgres" subtest unless BOARD_DB_LEG names
// another leg. A test of two feeds on one Postgres database has no SQLite case,
// so on BOARD_DB_LEG=sqlite (mutation's covered and mutant runs) it runs no
// subtest and starts no container; the Suite's Go pass, with the variable
// unset, runs it.
func onPostgres(t *testing.T, fn func(t *testing.T)) {
	t.Helper()
	switch leg := strings.TrimSpace(os.Getenv("BOARD_DB_LEG")); leg {
	case "", "postgres":
		t.Run("postgres", fn)
	case "sqlite":
	default:
		t.Fatalf("BOARD_DB_LEG=%q: want sqlite or postgres", leg)
	}
}

var (
	pgOnce      sync.Once
	pgContainer testcontainers.Container
	pgHost      string
	pgPort      string
	pgErr       error
	pgDatabases atomic.Int64
)

func pgDSN(host, port, db string) string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword("board", "board-test"), Host: host + ":" + port, Path: "/" + db, RawQuery: "sslmode=disable&connect_timeout=5"}
	return u.String()
}

// startContainer starts the package's one Postgres container through the docker
// socket the way features/postgres_test.go does, with its data directory in
// memory. It runs once per test process; closePostgres removes the container.
func startContainer() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
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
		pgContainer = c
	}
	if err != nil {
		return fmt.Errorf("starting Postgres through the docker socket: %w", err)
	}
	if pgHost, err = c.Host(ctx); err != nil {
		return err
	}
	mapped, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return err
	}
	pgPort = mapped.Port()
	return nil
}

// closePostgres removes the container, if one was started; TestMain calls it
// once the package's tests have ended.
func closePostgres() {
	if pgContainer != nil {
		_ = pgContainer.Terminate(context.Background(), testcontainers.StopTimeout(0))
	}
}

// startPostgres returns the URL of a new empty database on the package's one
// Postgres container, starting the container on first use, and drops the
// database when the test ends. It fails on BOARD_DB_LEG=sqlite, so no
// SQLite-leg run starts a container.
func startPostgres(t *testing.T) string {
	t.Helper()
	if leg := strings.TrimSpace(os.Getenv("BOARD_DB_LEG")); leg == "sqlite" {
		t.Fatal("startPostgres on BOARD_DB_LEG=sqlite: run the test through onPostgres")
	}
	pgOnce.Do(func() { pgErr = startContainer() })
	if pgErr != nil {
		t.Fatal(pgErr)
	}
	admin, err := sql.Open("pgx", pgDSN(pgHost, pgPort, "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	name := fmt.Sprintf("feed_%d_%d", os.Getpid(), pgDatabases.Add(1))
	if _, err := admin.ExecContext(context.Background(), "CREATE DATABASE "+name); err != nil {
		t.Fatalf("creating database %s: %v", name, err)
	}
	t.Cleanup(func() {
		if db, err := sql.Open("pgx", pgDSN(pgHost, pgPort, "postgres")); err == nil {
			_, _ = db.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			_ = db.Close()
		}
	})
	return pgDSN(pgHost, pgPort, name)
}

func openPostgres(t *testing.T, url string) *store.Store {
	t.Helper()
	st, err := store.Open(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// startFeed runs a feed on st with the production options (Options{}, so the
// default BOARD_STREAM_POLL of one second) and subscribes to it.
func startFeed(t *testing.T, st *store.Store) *Subscription {
	t.Helper()
	f, err := New(context.Background(), StoreSource{Store: st}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := f.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); waitDone(t, done) })
	return s
}

// collect reads want events from s, failing on a timeout.
func collect(t *testing.T, name string, s *Subscription, want int) []events.Event {
	t.Helper()
	var got []events.Event
	for len(got) < want {
		select {
		case e, ok := <-s.Events():
			if !ok {
				t.Fatalf("%s: subscription closed after %d events: %v", name, len(got), s.Err())
			}
			got = append(got, e)
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: %d of %d events after 10 s: %v", name, len(got), want, summaries(got))
		}
	}
	return got
}

func summaries(es []events.Event) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = fmt.Sprintf("%d:%s", e.N, e.Summary)
	}
	return out
}

func appendIn(t *testing.T, tx *sql.Tx, st *store.Store, actorID, summary string) {
	t.Helper()
	err := audit.AppendAs(context.Background(), tx, st.Dialect(), actorID, t0, audit.Event{Principal: "tester", Type: "note_posted", Summary: summary})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTwoFeedsOnOnePostgresDatabaseDeliverTheWholeSequenceInOrder(t *testing.T) {
	onPostgres(t, func(t *testing.T) {
		ctx := context.Background()
		url := startPostgres(t)
		a, b := openPostgres(t, url), openPostgres(t, url)
		if err := a.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		// The actor row first, in its own transaction, so the writers below do not
		// wait on each other for it.
		tx, err := a.Writer().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		actorID, err := audit.UpsertActor(ctx, tx, a.Dialect(), audit.Actor{Handle: "tester"}, t0)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		subA, subB := startFeed(t, a), startFeed(t, b)

		appendEvent2 := func(summary string) {
			tx, err := a.Writer().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			appendIn(t, tx, a, actorID, summary)
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		appendEvent2("first")
		// "slow" takes n=2 and stays open; "fast" takes n=3 and commits before it.
		slow, err := a.Writer().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		appendIn(t, slow, a, actorID, "slow")
		fast, err := b.Writer().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		appendIn(t, fast, b, actorID, "fast")
		if err := fast.Commit(); err != nil {
			t.Fatal(err)
		}

		// Both feeds see "first" but must hold "fast" back for as long as n=2 is
		// uncommitted: here 2.5 s, more than two poll intervals.
		for name, s := range map[string]*Subscription{"a": subA, "b": subB} {
			if got := collect(t, name, s, 1); got[0].Summary != "first" {
				t.Fatalf("%s: first event %v", name, summaries(got))
			}
		}
		hold := time.After(2500 * time.Millisecond)
		for waiting := true; waiting; {
			select {
			case e := <-subA.Events():
				t.Fatalf("a: delivered n=%d %q ahead of the uncommitted n=2", e.N, e.Summary)
			case e := <-subB.Events():
				t.Fatalf("b: delivered n=%d %q ahead of the uncommitted n=2", e.N, e.Summary)
			case <-hold:
				waiting = false
			}
		}
		if w, err := (StoreSource{Store: a}).Writers(ctx); err != nil || len(w) != 1 {
			t.Fatalf("Writers with n=2 open = %v, %v; want the one open writer", w, err)
		}
		if err := slow.Commit(); err != nil {
			t.Fatal(err)
		}
		if w, err := (StoreSource{Store: b}).Writers(ctx); err != nil || len(w) != 0 {
			t.Fatalf("Writers after the commit = %v, %v; want none", w, err)
		}
		appendEvent2("last")

		// A writer that draws n=5 and rolls back leaves a gap for good: the feeds
		// hold n=6 while it is open and deliver it once it has rolled back.
		doomed, err := a.Writer().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		appendIn(t, doomed, a, actorID, "doomed")
		appendEvent2("after")
		time.Sleep(1500 * time.Millisecond)
		if err := doomed.Rollback(); err != nil {
			t.Fatal(err)
		}
		appendEvent2("final")

		want := []string{"slow", "fast", "last", "after", "final"}
		wantN := []int64{2, 3, 4, 6, 7}
		for name, s := range map[string]*Subscription{"a": subA, "b": subB} {
			got := collect(t, name, s, len(want))
			for i, e := range got {
				if e.Summary != want[i] || e.N != wantN[i] {
					t.Errorf("%s: event %d is %d:%s, want %d:%s (all: %v)", name, i, e.N, e.Summary, wantN[i], want[i], summaries(got))
				}
			}
			select {
			case e := <-s.Events():
				t.Errorf("%s: duplicate or extra event n=%d %q", name, e.N, e.Summary)
			case <-time.After(300 * time.Millisecond):
			}
		}
	})
}
