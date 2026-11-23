package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // the driver the store registers, for the schema-version fixture

	"github.com/n-orlov/village-hall/internal/auth"
	"github.com/n-orlov/village-hall/internal/idempotency"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/version"
)

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb, func(string) string { return "" })
	return code, out.String(), errb.String()
}

func TestVersionPrintsCurrentVersion(t *testing.T) {
	code, out, errs := run("version")
	if code != ExitOK || errs != "" || out != version.Line()+"\n" {
		t.Fatalf("got code=%d out=%q err=%q", code, out, errs)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"bogus"}, {"version", "extra"}, {"serve", "extra"}} {
		code, out, errs := run(args...)
		if code != 2 || out != "" || errs != Usage+"\n" || strings.Count(errs, "\n") != 1 {
			t.Fatalf("%v: got code=%d out=%q err=%q", args, code, out, errs)
		}
	}
}

func TestServeRejectsUnknownMigrateMode(t *testing.T) {
	env := func(k string) string {
		if k == "BOARD_MIGRATE" {
			return "sometimes"
		}
		return ""
	}
	if code, errs := runServe(t, env); code != ExitError || !strings.Contains(errs, "VH_MIGRATE") {
		t.Fatalf("got code=%d err=%q", code, errs)
	}
}

func envWith(kv map[string]string) Env { return func(k string) string { return kv[k] } }

// serveRefusalBound is how long runServe waits for `vh serve` to return.
// A refusal returns in milliseconds; the bound only turns a serve that started
// serving instead into a failure.
const serveRefusalBound = 10 * time.Second

// serveReadyBound is how long serveReady waits for the listener to answer.
const serveReadyBound = 30 * time.Second

// runServe runs `vh serve` with env and returns its exit code and stderr.
// Each caller expects serve to refuse and return at once. A serve that starts
// serving instead stops only on SIGINT or SIGTERM, so without a bound the test
// would block until the package's own timeout (gremlins sets that to 60 times
// the covered run, far past the Mutation stage's 30 minutes): it fails after
// serveRefusalBound instead.
func runServe(t *testing.T, env Env) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- Run([]string{"serve"}, &out, &errb, env) }()
	select {
	case code := <-done:
		return code, errb.String()
	case <-time.After(serveRefusalBound):
		t.Fatalf("vh serve did not return within %v: it is serving instead of refusing", serveRefusalBound)
		return 0, ""
	}
}

func TestAdminUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"admin"}, {"admin", "bogus"}, {"admin", "migrate", "extra"}} {
		code, out, errs := run(args...)
		if code != ExitUsage || out != "" || errs != Usage+"\n" {
			t.Fatalf("%v: got code=%d out=%q err=%q", args, code, out, errs)
		}
	}
}

func TestAdminMigrateAppliesAndReportsTheVersion(t *testing.T) {
	env := envWith(map[string]string{"BOARD_DB_URL": "sqlite://" + filepath.Join(t.TempDir(), "b.db")})
	for i := 0; i < 2; i++ { // the second run is a no-op
		var out, errb bytes.Buffer
		if code := Run([]string{"admin", "migrate"}, &out, &errb, env); code != ExitOK ||
			out.String() != "ok migrated, schema version 15\n" || errb.Len() != 0 {
			t.Fatalf("run %d: code=%d out=%q err=%q", i, code, out.String(), errb.String())
		}
	}
}

func TestAdminMigrateFailsOnAnUnsupportedURL(t *testing.T) {
	var out, errb bytes.Buffer
	env := envWith(map[string]string{"BOARD_DB_URL": "mysql://x/y"})
	if code := Run([]string{"admin", "migrate"}, &out, &errb, env); code != ExitError ||
		out.Len() != 0 || strings.Count(errb.String(), "\n") != 1 {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errb.String())
	}
}

func sqliteURL(t *testing.T) string {
	t.Helper()
	return "sqlite://" + filepath.Join(t.TempDir(), "b.db")
}

// serveRefused runs serveUntil against url and addr for a start that must
// refuse and return on its own. The context is only a hang guard, generous
// because a loaded machine can spend seconds opening and migrating the store.
func serveRefused(t *testing.T, url, addr string, migrate bool) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), serveRefusalBound)
	defer cancel()
	var errb bytes.Buffer
	code := serveUntil(ctx, &errb, func(string) string { return "" }, url, addr, migrate)
	return code, withoutLogLines(t, errb.String())
}

// serveReady is serveReadyLog with the JSON log lines left out of stderr.
func serveReady(t *testing.T, url string, migrate bool) (int, string) {
	t.Helper()
	code, errs := serveReadyLog(t, url, migrate)
	return code, withoutLogLines(t, errs)
}

// serveReadyLog runs serveUntil against url on a free port, waits until /healthz
// answers 200 (it does even on an unmigrated store), then cancels it and
// returns its exit code and stderr. Startup time does not count against the
// server's lifetime, so a loaded machine cannot turn a slow start into a
// deadline error.
func serveReadyLog(t *testing.T, url string, migrate bool) (int, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var errb bytes.Buffer
	exit := make(chan int, 1)
	go func() { exit <- serveUntil(ctx, &errb, func(string) string { return "" }, url, addr, migrate) }()
	ready := false
	for deadline := time.Now().Add(serveReadyBound); !ready && time.Now().Before(deadline); {
		select {
		case code := <-exit:
			return code, errb.String()
		case <-time.After(20 * time.Millisecond):
		}
		if resp, err := http.Get("http://" + addr + "/healthz"); err == nil {
			ready = resp.StatusCode == http.StatusOK
			_ = resp.Body.Close()
		}
	}
	if !ready {
		cancel()
		<-exit
		t.Fatalf("serve was not ready within %v; stderr %q", serveReadyBound, errb.String())
	}
	cancel()
	select {
	case code := <-exit:
		return code, errb.String()
	case <-time.After(serveRefusalBound):
		t.Fatal("serve did not stop after its context ended")
		return 0, ""
	}
}

func TestServeUntilServesAndStopsCleanlyWhenTheContextEnds(t *testing.T) {
	for _, migrate := range []bool{true, false} {
		if code, errs := serveReady(t, sqliteURL(t), migrate); code != ExitOK || errs != "" {
			t.Errorf("migrate=%v: code=%d err=%q", migrate, code, errs)
		}
	}
}

func TestServeUntilFailsWithOneLineOnABadDatabaseOrAddress(t *testing.T) {
	for name, tc := range map[string]struct{ url, addr string }{
		"unsupported database": {"mysql://x/y", "127.0.0.1:0"},
		"unusable address":     {sqliteURL(t), "not-an-address"},
	} {
		code, errs := serveRefused(t, tc.url, tc.addr, true)
		if code != ExitError || !strings.HasPrefix(errs, "vh: ") || strings.Count(errs, "\n") != 1 {
			t.Errorf("%s: code=%d err=%q", name, code, errs)
		}
	}
}

func TestServeUntilRefusesADatabaseNewerThanTheBinary(t *testing.T) {
	url := sqliteURL(t)
	if code := Run([]string{"admin", "migrate"}, io.Discard, io.Discard, envWith(map[string]string{"BOARD_DB_URL": url})); code != ExitOK {
		t.Fatalf("migrate exited %d", code)
	}
	db, err := sql.Open("sqlite", strings.TrimPrefix(url, "sqlite://"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO goose_db_version (version_id, is_applied) VALUES (9999, 1)"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	for _, migrate := range []bool{true, false} {
		code, errs := serveRefused(t, url, "127.0.0.1:0", migrate)
		if code != ExitError || !strings.Contains(errs, "9999") {
			t.Errorf("migrate=%v: code=%d err=%q, want a refusal naming version 9999", migrate, code, errs)
		}
	}
}

func TestServeReportsAListenFailureFromTheEnvironment(t *testing.T) {
	env := envWith(map[string]string{"BOARD_DB_URL": sqliteURL(t), "BOARD_LISTEN": "not-an-address"})
	if code, errs := runServe(t, env); code != ExitError || !strings.Contains(errs, "listen") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestServeRejectsAMalformedSweepInterval(t *testing.T) {
	if code, errs := runServe(t, envWith(map[string]string{"BOARD_SWEEP_INTERVAL": "soon"})); code != ExitError ||
		!strings.Contains(errs, "VH_SWEEP_INTERVAL") {
		t.Fatalf("got code=%d err=%q", code, errs)
	}
}

func TestServeRejectsAnUnreadableStreamPoll(t *testing.T) {
	for _, v := range []string{"soon", "0", "0s", "-2s", "off"} {
		if code, errs := runServe(t, envWith(map[string]string{"BOARD_STREAM_POLL": v})); code != ExitError ||
			!strings.Contains(errs, "VH_STREAM_POLL") || strings.Count(errs, "\n") != 1 {
			t.Errorf("BOARD_STREAM_POLL=%q: code=%d err=%q", v, code, errs)
		}
	}
}

// sessionsLeft counts the rows of the sessions table.
func sessionsLeft(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.Reader().QueryRow(`SELECT count(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSweeperDeletesExpiredSessionsAtTheConfiguredIntervalAndNotWhenOff(t *testing.T) {
	for _, tc := range []struct {
		interval string
		want     int
	}{{"5ms", 0}, {"off", 1}} {
		st, err := store.Open(sqliteURL(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Hour)
		sessions := auth.Sessions{Store: st, TTL: time.Minute, Now: func() time.Time { return old }}
		if _, _, err := sessions.Create(context.Background(), "a@example.com"); err != nil {
			t.Fatal(err)
		}
		var errb bytes.Buffer
		stop := startSweeper(context.Background(), &errb, envWith(map[string]string{"BOARD_SWEEP_INTERVAL": tc.interval}), st)
		deadline := time.Now().Add(3 * time.Second)
		for sessionsLeft(t, st) != tc.want && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if tc.want == 1 {
			time.Sleep(50 * time.Millisecond)
		}
		stop()
		if got := sessionsLeft(t, st); got != tc.want || errb.Len() != 0 {
			t.Errorf("interval %s: %d sessions left, want %d (stderr %q)", tc.interval, got, tc.want, errb.String())
		}
		_ = st.Close()
	}
}

func TestSweeperDeletesIdempotencyRowsOlderThanADayAndKeepsTheRest(t *testing.T) {
	st, err := store.Open(sqliteURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for key, age := range map[string]time.Duration{"stale": 25 * time.Hour, "fresh": time.Hour} {
		then := time.Now().Add(-age)
		keys := idempotency.Store{Store: st, Now: func() time.Time { return then }}
		if o, _, err := keys.Claim(context.Background(), idempotency.Request{Principal: "key:a", Key: key, Fingerprint: "f"}); err != nil || o != idempotency.Claimed {
			t.Fatalf("claim %s = %v, %v", key, o, err)
		}
	}
	left := func() (keys []string) {
		rows, err := st.Reader().Query(`SELECT key_hash FROM idempotency_keys`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				t.Fatal(err)
			}
			keys = append(keys, k)
		}
		return keys
	}
	var errb bytes.Buffer
	stop := startSweeper(context.Background(), &errb, envWith(map[string]string{"BOARD_SWEEP_INTERVAL": "5ms"}), st)
	deadline := time.Now().Add(3 * time.Second)
	for len(left()) != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	stop()
	if got := left(); len(got) != 1 || errb.Len() != 0 {
		t.Errorf("%d idempotency rows left, want the one fresh row (stderr %q)", len(got), errb.String())
	}
}

func TestSweeperReportsAFailingJobOnStderr(t *testing.T) {
	st, err := store.Open(sqliteURL(t))
	if err != nil {
		t.Fatal(err)
	}
	var errb syncBuffer
	_ = st.Close()
	stop := startSweeper(context.Background(), &errb, envWith(map[string]string{"BOARD_SWEEP_INTERVAL": "5ms"}), st)
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(errb.String(), "vh: sweep:") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if !strings.Contains(errb.String(), "vh: sweep:") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestSweeperWithAMalformedIntervalRunsNothing(_ *testing.T) {
	stop := startSweeper(context.Background(), io.Discard, envWith(map[string]string{"BOARD_SWEEP_INTERVAL": "soon"}), nil)
	stop()
}

// syncBuffer is a bytes.Buffer the sweeper goroutine and the test may share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestReopenLeasedLeavesADatabaseWithoutTheSchemaAlone(t *testing.T) {
	st, err := store.Open(sqliteURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	if err := reopenLeased(ctx, st); err != nil {
		t.Errorf("unmigrated database: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := reopenLeased(ctx, st); err != nil {
		t.Errorf("migrated database: %v", err)
	}
}

// withoutLogLines drops serve's JSON log lines from stderr, failing the test on
// one that is not a JSON object with a time, a level and a message.
func withoutLogLines(t *testing.T, stderr string) string {
	t.Helper()
	var rest strings.Builder
	for _, line := range strings.SplitAfter(stderr, "\n") {
		if !strings.HasPrefix(line, "{") {
			rest.WriteString(line)
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil || rec["time"] == nil || rec["level"] == nil || rec["msg"] == nil {
			t.Errorf("bad log line %q", line)
		}
	}
	return rest.String()
}
