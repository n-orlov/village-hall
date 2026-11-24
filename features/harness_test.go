package features

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"github.com/n-orlov/village-hall/internal/oidc/oidctest"
	_ "modernc.org/sqlite" // the driver the schema-version step writes through
)

const usageLine = "usage: vh <serve|version|healthcheck|update|help|whoami|init|task|lease|start|new|finish|block|ping|catchup|now|milestone|check|events|ask|answer|note|notes|attention|admin>"

// dbLeg returns the database leg named by BOARD_DB_LEG.
func dbLeg() (string, error) {
	switch leg := strings.TrimSpace(os.Getenv("BOARD_DB_LEG")); leg {
	case "", "sqlite":
		return "sqlite", nil
	case "postgres":
		return "postgres", nil
	default:
		return "", fmt.Errorf("BOARD_DB_LEG=%q: want sqlite or postgres", leg)
	}
}

// boardPackage is the import path of the binary every scenario drives.
const boardPackage = "github.com/n-orlov/village-hall/cmd/vh"

// boardBuildArgs is the `go build` argument list for the black-box board
// binary. With a coverDir (the suite's GOCOVERDIR) the binary is built with
// -cover, and with a coverMode (the suite's BOARD_FEATURES_COVERMODE, the
// unit pass's mode) -covermode=<mode> too: `go tool covdata` cannot merge
// atomic counters (a -race unit pass) with a set-mode binary's. Neither set:
// the plain build. A mode without a coverDir is ignored.
func boardBuildArgs(binary, coverDir, coverMode string) []string {
	args := []string{"build", "-o", binary}
	if coverDir != "" {
		args = append(args, "-cover")
		if coverMode != "" {
			args = append(args, "-covermode="+coverMode)
		}
	}
	return append(args, boardPackage)
}

// compileBoard builds the real board binary at bin, instrumented for coverage
// whenever GOCOVERDIR is supplied, the way the suite supplies it.
func compileBoard(bin string) ([]byte, error) {
	cmd := exec.Command("go", boardBuildArgs(bin, os.Getenv("GOCOVERDIR"), os.Getenv("BOARD_FEATURES_COVERMODE"))...)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	return cmd.CombinedOutput()
}

// buildBoard compiles the real binary once for the whole run.
func buildBoard(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "board")
	if out, err := compileBoard(bin); err != nil {
		t.Fatalf("building the board binary: %v\n%s", err, out)
	}
	return bin
}

// harness is one scenario's state.
type harness struct {
	binary string
	leg    string

	exit   int
	stdout string
	stderr string

	status int

	// operations is the operation list read from api/openapi.yaml and the
	// status each answered to a request with no credential.
	operations []operation
	answers    map[string]int

	// web is the browser of the OIDC scenarios, idp the mock provider they
	// start (nil until a scenario asks for one) and idpSecret its client secret.
	web       browser
	idp       *oidctest.Provider
	idpSecret string

	// key is the API key a scenario remembered from stdout.
	key string
	api apiState

	// eventRef is the event ref a scenario remembered; <event> in a client
	// command line stands for it.
	eventRef string

	// stdin is the text the next command reads from standard input, and
	// textFile a file of text a command line names as <file>; both are the
	// scenario's own (the file is removed when the scenario ends).
	stdin    string
	textFile string

	// background is the client command a scenario left running.
	background *backgroundClient

	server *exec.Cmd
	addr   string
	dir    string
	dbURL  string

	// shared is the Postgres every scenario of the run gets a database on
	// (nil on the SQLite leg). own is a container private to the running
	// scenario, started for one tagged @dedicated-database so that it may stop
	// the database without disturbing the others.
	shared    *postgresServer
	dedicated bool
	own       *postgresServer

	// empty is set for a scenario tagged @empty-database: its Postgres
	// database holds no table, where any other scenario's is a copy of the
	// migrated template. templated records that the scenario's database is
	// such a copy.
	empty     bool
	templated bool
}

// ciInternalPrefixes are the BOARD_ names that belong to CI, not to the
// product: the suite and its scripts read them, and the binary under test
// ignores them. Every other BOARD_ name and every VH_ name is a product
// variable (or a lookalike) and never reaches a process the suite starts.
var ciInternalPrefixes = []string{
	"BOARD_CI_", "BOARD_GODOG_", "BOARD_DB_LEG", "BOARD_MUTATION_",
	"BOARD_IMAGE_", "BOARD_ALLURE_", "BOARD_FEATURES_",
}

// scrubProductEnv drops the BOARD_* and VH_* entries of environ, except the
// CI-internal names.
func scrubProductEnv(environ []string) []string {
	var kept []string
	for _, kv := range environ {
		if (strings.HasPrefix(kv, "BOARD_") || strings.HasPrefix(kv, "VH_")) && !ciInternal(kv) {
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}

func ciInternal(kv string) bool {
	for _, p := range ciInternalPrefixes {
		if strings.HasPrefix(kv, p) {
			return true
		}
	}
	return false
}

// env is the environment of a process the suite starts: the test process's,
// scrubbed of product variables, plus extra.
func (h *harness) env(extra ...string) []string {
	return append(scrubProductEnv(os.Environ()), extra...)
}

func (h *harness) run(ctx context.Context, line string) error {
	return h.runWith(ctx, line)
}

func (h *harness) runWithVar(ctx context.Context, line, name, value string) error {
	return h.runWith(ctx, line, name+"="+value)
}

func (h *harness) runWith(ctx context.Context, line string, extra ...string) error {
	fields := strings.Fields(line)
	if len(fields) == 0 || fields[0] != "vh" {
		return fmt.Errorf("command %q must start with board", line)
	}
	return h.runArgs(ctx, fields[1:], extra...)
}

// runArgs runs the board binary with args and records its exit code and output.
func (h *harness) runArgs(ctx context.Context, args []string, extra ...string) error {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, h.binary, args...)
	if h.dbURL != "" {
		extra = append([]string{"BOARD_DB_URL=" + h.dbURL}, extra...)
	}
	cmd.Env = h.env(extra...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if h.stdin != "" {
		cmd.Stdin, h.stdin = strings.NewReader(h.stdin), ""
	}
	err := cmd.Run()
	h.stdout, h.stderr = out.String(), errb.String()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		h.exit = 0
	case errors.As(err, &exitErr):
		h.exit = exitErr.ExitCode()
	default:
		return err
	}
	return nil
}

// startServer starts `vh serve` on a free port and waits until it accepts.
func (h *harness) startServer(ctx context.Context) (context.Context, error) {
	return h.startServerWith(ctx)
}

// startServerWithVar starts the server with one extra BOARD_* variable set.
func (h *harness) startServerWithVar(ctx context.Context, name, value string) (context.Context, error) {
	return h.startServerWith(ctx, name+"="+value)
}

func (h *harness) startServerWith(ctx context.Context, extra ...string) (context.Context, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return ctx, err
	}
	h.addr = "127.0.0.1:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	if err := h.freshDatabase(); err != nil {
		return ctx, err
	}
	for i, kv := range extra {
		extra[i] = strings.ReplaceAll(kv, "{addr}", h.addr)
	}
	h.server = exec.Command(h.binary, "serve")
	h.server.Env = h.env(append([]string{"BOARD_LISTEN=" + h.addr, "BOARD_DB_URL=" + h.dbURL}, extra...)...)
	var logs bytes.Buffer
	h.server.Stdout, h.server.Stderr = &logs, &logs
	if err := h.server.Start(); err != nil {
		return ctx, err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.Dial("tcp", h.addr); err == nil {
			_ = conn.Close()
			return ctx, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return ctx, fmt.Errorf("server never listened on %s: %s", h.addr, logs.String())
}

// freshDatabase names a database file in the scenario's temp directory, once.
// A scenario that already prepared one (a migrated or too-new store) keeps it.
func (h *harness) freshDatabase() error {
	if h.dbURL != "" {
		return nil
	}
	if h.leg == "postgres" {
		return h.freshPostgres(false)
	}
	dir, err := os.MkdirTemp("", "board-godog-")
	if err != nil {
		return err
	}
	h.dir = dir
	h.dbURL = "sqlite://" + filepath.Join(dir, "board.db")
	return nil
}

// emptyDatabase is freshDatabase for a step that exercises the migrations
// themselves: the database it names has no table. A scenario whose database was
// already copied from the migrated template must be tagged @empty-database.
func (h *harness) emptyDatabase() error {
	if h.templated {
		return errors.New("the scenario's database is already migrated: tag the scenario @empty-database")
	}
	if h.dbURL != "" {
		return nil
	}
	if h.leg == "postgres" {
		return h.freshPostgres(true)
	}
	return h.freshDatabase()
}

// freshPostgres names a database on the run's Postgres, or on a private
// container for a scenario tagged @dedicated-database: empty when asked, or
// when the scenario is tagged @empty-database or @dedicated-database, else a
// copy of the migrated template.
func (h *harness) freshPostgres(empty bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	server := h.shared
	if h.dedicated {
		own, err := startPostgres(ctx)
		if err != nil {
			return err
		}
		h.own, server = own, own
	}
	empty = empty || h.empty || h.dedicated
	create := server.newDatabase
	if empty {
		create = server.newEmptyDatabase
	}
	url, err := create(ctx)
	if err != nil {
		return err
	}
	h.dbURL, h.templated = url, !empty
	return nil
}

// openDB opens the scenario's database with the driver of the leg.
func (h *harness) openDB() (*sql.DB, error) {
	if h.leg == "postgres" {
		return sql.Open("pgx", h.dbURL)
	}
	return sql.Open("sqlite", strings.TrimPrefix(h.dbURL, "sqlite://"))
}

// recordSchemaVersion makes the scenario's database claim goose version v as
// applied, the way a newer binary's migration would have.
func (h *harness) recordSchemaVersion(ctx context.Context, v int) error {
	if err := h.emptyDatabase(); err != nil {
		return err
	}
	db, err := h.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	insert := "INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)"
	if h.leg == "postgres" {
		insert = "INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)"
	}
	_, err = db.ExecContext(ctx, insert, v)
	return err
}

// phase0Tables are the tables the embedded migrations create: the Phase 0 ones
// and, since Phase 1, tasks and task_deps.
var phase0Tables = []string{"actors", "initiatives", "events", "api_keys", "initiative_tags", "sessions", "tasks", "task_deps"}

// hasPhase0Tables reads every table the migrations create, which fails when one
// is missing.
func (h *harness) hasPhase0Tables(ctx context.Context) error {
	db, err := h.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	for _, table := range phase0Tables {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			return fmt.Errorf("table %s: %w", table, err)
		}
	}
	return nil
}

// databaseStopped stops the database the way an outage would. Only a scenario
// with its own container may do it.
func (h *harness) databaseStopped(ctx context.Context) error {
	if h.own == nil {
		return errors.New("only a scenario tagged @dedicated-database on the Postgres leg may stop its database")
	}
	return h.own.stop(ctx)
}

// get sends a GET to the running server and records the status. headers are
// added to the request; none means a credential-less request.
func (h *harness) get(ctx context.Context, path string, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+h.addr+path, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	h.status = resp.StatusCode
	h.api.body, err = io.ReadAll(resp.Body)
	return err
}

func (h *harness) getWithoutCredentials(ctx context.Context, path string) error {
	return h.get(ctx, path, nil)
}

// getWithKey sends the remembered key and an actor.
func (h *harness) getWithKey(ctx context.Context, path string) error {
	if h.key == "" {
		return errors.New("no key was remembered")
	}
	return h.get(ctx, path, map[string]string{"Authorization": "Bearer " + h.key, "Board-Actor": "godog"})
}

// getWithKeyAndNoActor sends the remembered key and no Board-Actor header.
func (h *harness) getWithKeyAndNoActor(ctx context.Context, path string) error {
	if h.key == "" {
		return errors.New("no key was remembered")
	}
	return h.get(ctx, path, map[string]string{"Authorization": "Bearer " + h.key})
}

// keyFromAnotherDatabase migrates a throwaway SQLite database, mints a key on
// it and remembers the key, so that a scenario holds a genuine key the
// scenario's own (unmigrated) database has never seen.
func (h *harness) keyFromAnotherDatabase(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "board-godog-other-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	url := "BOARD_DB_URL=sqlite://" + filepath.Join(dir, "other.db")
	for _, args := range [][]string{{"admin", "migrate"}, {"admin", "keys", "create", "elsewhere"}} {
		cmd := exec.CommandContext(ctx, h.binary, args...)
		cmd.Env = h.env(url)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("board %s: %w: %s", strings.Join(args, " "), err, errb.String())
		}
		h.stdout = out.String()
	}
	return h.rememberKey()
}

func (h *harness) stopServer() {
	if h.api.releases != nil {
		h.api.releases.Close()
		h.api.releases = nil
	}
	if h.api.installedVH != "" {
		_ = os.RemoveAll(filepath.Dir(h.api.installedVH))
		h.api.installedVH = ""
	}
	if h.api.stubBoard != nil {
		h.api.stubBoard.Close()
		h.api.stubBoard = nil
	}
	if h.server != nil && h.server.Process != nil {
		endServer(h.server)
	}
	if h.dir != "" {
		_ = os.RemoveAll(h.dir)
	}
	if h.textFile != "" {
		_ = os.Remove(h.textFile)
		h.textFile = ""
	}
	h.stdin = ""
	if h.own != nil {
		h.own.terminate(context.Background())
	} else if h.shared != nil && strings.HasPrefix(h.dbURL, "postgres://") {
		// the shared Postgres keeps its data in memory: give the scenario's
		// database back (the server above has ended)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		_ = h.shared.dropDatabase(ctx, h.dbURL)
		cancel()
	}
	if h.idp != nil {
		h.idp.Close()
	}
	h.web, h.idp, h.idpSecret = browser{}, nil, ""
	h.dir, h.dbURL, h.own, h.dedicated, h.key, h.api = "", "", nil, false, "", apiState{}
	h.templated = false
}

var versionPattern = regexp.MustCompile(`^(v[0-9][^\s]*|devel(\+[0-9a-f]+)?)(-dirty)?$`)

func registerSteps(sc *godog.ScenarioContext, h *harness) {
	sc.Before(func(ctx context.Context, scenario *godog.Scenario) (context.Context, error) {
		h.dedicated, h.empty = false, false
		for _, tag := range scenario.Tags {
			switch tag.Name {
			case "@dedicated-database":
				h.dedicated = true
			case "@empty-database":
				h.empty = true
			}
		}
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		h.stopServer()
		return ctx, nil
	})
	registerKeySteps(sc, h)
	registerAPIKeySteps(sc, h)
	registerOpenAPISteps(sc, h)
	registerOIDCSteps(sc, h)
	registerUIKeySteps(sc, h)
	registerInitiativeAPISteps(sc, h)
	registerTaskAPISteps(sc, h)
	registerInitiativeEditSteps(sc, h)
	registerInitiativeCloseSteps(sc, h)
	registerCLIInitiativeSteps(sc, h)
	registerCLIOutputSteps(sc, h)
	registerCLITaskSteps(sc, h)
	registerCLILeaseSteps(sc, h)
	registerCLIAskSteps(sc, h)
	registerPhase2UpgradeSteps(sc, h)
	registerServeUpgradeSteps(sc, h)
	registerInitiativeInboxSteps(sc, h)
	registerNotificationSteps(sc, h)
	registerHelpAgentSteps(sc, h)
	registerStartSteps(sc, h)
	registerUpdateSteps(sc, h)
	registerFinishSteps(sc, h)
	registerPingSteps(sc, h)
	registerBlockSteps(sc, h)
	registerCatchupSteps(sc, h)
	registerIdempotencySteps(sc, h)
	sc.Step(`^I run "([^"]*)"$`, h.run)
	sc.Step(`^I run "([^"]*)" with (BOARD_[A-Z_]+)=(\w+)$`, h.runWithVar)
	sc.Step(`^I run "([^"]*)" against the server's listen address$`, func(ctx context.Context, line string) error {
		return h.runWith(ctx, line, "BOARD_LISTEN="+h.addr)
	})
	sc.Step(`^the exit code is (\d+)$`, func(want int) error {
		if h.exit != want {
			return fmt.Errorf("exit code %d, want %d (stderr %q)", h.exit, want, h.stderr)
		}
		return nil
	})
	sc.Step(`^stdout is empty$`, func() error {
		if h.stdout != "" {
			return fmt.Errorf("stdout %q, want empty", h.stdout)
		}
		return nil
	})
	sc.Step(`^stderr is empty$`, func() error {
		if h.stderr != "" {
			return fmt.Errorf("stderr %q, want empty", h.stderr)
		}
		return nil
	})
	sc.Step(`^stdout is a single line holding a version$`, func() error {
		line := strings.TrimSuffix(h.stdout, "\n")
		if strings.Contains(line, "\n") || !versionPattern.MatchString(line) {
			return fmt.Errorf("stdout %q is not a single version line", h.stdout)
		}
		return nil
	})
	sc.Step(`^stderr is exactly the usage line$`, func() error {
		if h.stderr != usageLine+"\n" {
			return fmt.Errorf("stderr %q, want exactly %q", h.stderr, usageLine+"\n")
		}
		return nil
	})
	sc.Step(`^a new database for the board commands$`, h.freshDatabase)
	sc.Step(`^the database holds every Phase 0 table$`, h.hasPhase0Tables)
	sc.Step(`^the database is stopped$`, h.databaseStopped)
	sc.Step(`^the database records schema version (\d+) as applied$`, h.recordSchemaVersion)
	sc.Step(`^stderr is exactly one line containing "([^"]*)"$`, func(want string) error {
		plain := withoutLogLines(h.stderr)
		if strings.Count(plain, "\n") != 1 || !strings.HasSuffix(plain, "\n") || !strings.Contains(plain, want) {
			return fmt.Errorf("stderr %q is not exactly one line containing %q (JSON log lines aside)", h.stderr, want)
		}
		return nil
	})
	sc.Step(`^stdout is exactly "([^"]*)"$`, func(want string) error {
		if h.stdout != want+"\n" {
			return fmt.Errorf("stdout %q, want %q", h.stdout, want+"\n")
		}
		return nil
	})
	sc.Step(`^the board server is running on a free port$`, h.startServer)
	sc.Step(`^the board server is running on a free port with (BOARD_[A-Z_]+)=(\w+)$`, h.startServerWithVar)
	sc.Step(`^I request GET "([^"]*)" without credentials$`, h.getWithoutCredentials)
	sc.Step(`^I request GET "([^"]*)" with the remembered key$`, h.getWithKey)
	sc.Step(`^I request GET "([^"]*)" with the remembered key and no Board-Actor header$`, h.getWithKeyAndNoActor)
	sc.Step(`^I hold a key made on another database$`, h.keyFromAnotherDatabase)
	sc.Step(`^the response status is (\d+)$`, func(want int) error {
		if h.status != want {
			return fmt.Errorf("response status %d, want %d", h.status, want)
		}
		return nil
	})
	sc.Step(`^the server accepts connections on its listen address$`, func() error {
		conn, err := net.DialTimeout("tcp", h.addr, 2*time.Second)
		if err != nil {
			return err
		}
		return conn.Close()
	})
}

// endServer stops a started `vh serve` so that it can flush the coverage
// counters an instrumented build holds: SIGTERM first, which the server
// handles by shutting down and exiting, and a kill only when it does not
// exit in time.
func endServer(cmd *exec.Cmd) {
	endServerWithin(cmd, endServerGrace)
}

// endServerGrace is how long a server gets to exit on SIGTERM.
var endServerGrace = 10 * time.Second

func endServerWithin(cmd *exec.Cmd, grace time.Duration) {
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(grace):
		_ = cmd.Process.Kill()
		<-done
	}
}

// withoutLogLines drops the JSON log lines `vh serve` writes (SPEC §8: objects
// with time, level and msg) from stderr, leaving the plain lines a failed start
// or a client command prints.
func withoutLogLines(stderr string) string {
	var rest strings.Builder
	for _, line := range strings.SplitAfter(stderr, "\n") {
		var rec struct {
			Time, Level, Msg *string
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Time != nil && rec.Level != nil && rec.Msg != nil {
			continue
		}
		rest.WriteString(line)
	}
	return rest.String()
}
