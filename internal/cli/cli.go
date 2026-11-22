package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/n-orlov/village-hall/internal/actors"
	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/attention"
	"github.com/n-orlov/village-hall/internal/auth"
	"github.com/n-orlov/village-hall/internal/cursors"
	"github.com/n-orlov/village-hall/internal/envvar"
	"github.com/n-orlov/village-hall/internal/events"
	"github.com/n-orlov/village-hall/internal/feed"
	"github.com/n-orlov/village-hall/internal/idempotency"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/notes"
	"github.com/n-orlov/village-hall/internal/notify"
	"github.com/n-orlov/village-hall/internal/roster"
	"github.com/n-orlov/village-hall/internal/server"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/sweep"
	"github.com/n-orlov/village-hall/internal/tasks"
	"github.com/n-orlov/village-hall/internal/timeline"
	"github.com/n-orlov/village-hall/internal/version"
)

// Exit codes (SPEC §8.1).
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// DefaultDBURL is the database when VH_DB_URL is unset.
const DefaultDBURL = "sqlite:///data/board.db"

// Usage is the single line printed to stderr for a usage error.
const Usage = "usage: vh <serve|version|healthcheck|update|help|whoami|init|task|lease|start|new|finish|block|ping|catchup|now|milestone|check|events|ask|answer|note|notes|attention|admin>"

// Env looks up an environment variable.
type Env func(string) string

// Run executes the board command line and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer, env Env) int {
	if len(args) == 0 {
		return usage(stderr)
	}
	switch args[0] {
	case "version":
		return versionCmd(args[1:], stdout, stderr)
	case "serve":
		return serveCmd(args[1:], stderr, env)
	case "healthcheck":
		return healthcheckCmd(args[1:], stdout, stderr, env)
	case "update":
		return updateCmd(args[1:], stdout, stderr, env)
	case "help":
		return helpCmd(args[1:], stdout, stderr, env)
	case "admin":
		return admin(args[1:], stdout, stderr, env)
	default:
		return runClient(args, stdout, stderr, env)
	}
}

// versionCmd runs `vh version`, which takes no argument.
func versionCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		return usage(stderr)
	}
	_, _ = fmt.Fprintln(stdout, version.Line())
	return ExitOK
}

// serveCmd runs `vh serve`, which takes no argument.
func serveCmd(args []string, stderr io.Writer, env Env) int {
	if len(args) != 0 {
		return usage(stderr)
	}
	return serve(stderr, env)
}

// clientCommands are the subcommands that call the server at VH_URL.
var clientCommands = map[string]func([]string, io.Writer, io.Writer, Env) int{
	"whoami":    whoami,
	"init":      initCmd,
	"task":      taskCmd,
	"lease":     leaseCmd,
	"start":     startCmd,
	"new":       newCmd,
	"finish":    finishCmd,
	"block":     blockCmd,
	"ping":      pingCmd,
	"catchup":   catchupCmd,
	"now":       nowCmd,
	"milestone": milestoneCmd,
	"check":     checkCmd,
	"events":    eventsCmd,
	"ask":       askCmd,
	"answer":    answerCmd,
	"note":      noteCmd,
	"notes":     notesCmd,
	"attention": attentionCmd,
}

// runClient runs a client subcommand, or reports the usage error.
func runClient(args []string, stdout, stderr io.Writer, env Env) int {
	if cmd, ok := clientCommands[args[0]]; ok {
		return cmd(args[1:], stdout, stderr, env)
	}
	return usage(stderr)
}

// admin runs `vh admin ...`, which talks to the database directly.
func admin(args []string, stdout, stderr io.Writer, env Env) int {
	if len(args) > 0 && args[0] == "keys" {
		return adminKeys(args[1:], stdout, stderr, env)
	}
	if len(args) != 1 || args[0] != "migrate" {
		return usage(stderr)
	}
	return adminMigrate(stdout, stderr, env)
}

// adminMigrate runs `vh admin migrate`.
func adminMigrate(stdout, stderr io.Writer, env Env) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(dbURL(env))
	if err != nil {
		return fail(stderr, err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(ctx); err != nil {
		return fail(stderr, err)
	}
	v, err := st.SchemaVersion(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "ok migrated, schema version %d\n", v)
	return ExitOK
}

// fail prints one error line and returns the error exit code.
func fail(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "vh: %v\n", err)
	return ExitError
}

func dbURL(env Env) string {
	if v := envvar.DBURL.Get(env); v != "" {
		return v
	}
	return DefaultDBURL
}

func usage(stderr io.Writer) int {
	_, _ = fmt.Fprintln(stderr, Usage)
	return ExitUsage
}

func serve(stderr io.Writer, env Env) int {
	addr := envvar.Listen.Get(env)
	if addr == "" {
		addr = server.DefaultListen
	}
	migrate := envvar.Migrate.Get(env)
	if migrate != "" && migrate != "auto" && migrate != "off" {
		_, _ = fmt.Fprintf(stderr, "vh: %s=%q: want auto or off\n", envvar.Migrate.Name(), migrate)
		return ExitError
	}
	if err := validateSettings(env); err != nil {
		return fail(stderr, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveUntil(ctx, stderr, env, dbURL(env), addr, migrate != "off")
}

// validateSettings checks the settings serve reads at start-up parse, so a bad one
// exits 1 before anything is opened.
func validateSettings(env Env) error {
	if _, err := sweep.ParseInterval(envvar.SweepInterval.Get(env)); err != nil {
		return err
	}
	if _, _, err := streamSettings(env); err != nil {
		return err
	}
	if _, err := store.ParseCheckpointInterval(envvar.SQLiteCheckpoint.Get(env)); err != nil {
		return err
	}
	return validateNotifySettings(env)
}

func validateNotifySettings(env Env) error {
	if _, err := store.ParseNotifyOn(envvar.NotifyOn.Get(env)); err != nil {
		return err
	}
	if _, err := notify.ParseTimeout(envvar.NotifyTimeout.Get(env)); err != nil {
		return err
	}
	_, err := notify.ParseHeader(envvar.NotifyHeader.Get(env))
	return err
}

// serveUntil opens the database, migrates it when migrate is set, and serves
// on addr until ctx ends. It logs to stderr as JSON lines (SPEC §8): each
// migration it applies, a start line, and the stop.
func serveUntil(ctx context.Context, stderr io.Writer, env Env, url, addr string, migrate bool) int {
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	st, err := store.Open(url)
	if err != nil {
		return fail(stderr, err)
	}
	defer func() { _ = st.Close() }()
	if err := startupSteps(ctx, st, env, migrate, logger); err != nil {
		return fail(stderr, err)
	}
	announce(ctx, logger, st, addr)
	login, err := loginFromEnv(env, st)
	if err != nil {
		return fail(stderr, err)
	}
	// Registered before the sweeper and the feed so that, deferred last-in
	// first-out, its final checkpoint runs after both stopped and before the
	// store closes.
	defer startCheckpointer(ctx, stderr, env, st)()
	stopSweeper := startSweeper(ctx, stderr, env, st)
	defer stopSweeper()
	defer startNotifier(ctx, stderr, env, st)()
	stream, stopFeed, err := startFeed(ctx, stderr, env, st)
	if err != nil {
		return fail(stderr, err)
	}
	defer stopFeed()
	if err := server.Serve(ctx, addr, st.Ready, (&auth.Verifier{Store: st}).Verify, login, &auth.Keys{Store: st}, server.WithInitiatives(&initiatives.Store{Store: st}), server.WithTasks(&tasks.Store{Store: st}, &leases.Store{Store: st}), server.WithEvents(&events.Store{Store: st}), server.WithLeases(&leases.Store{Store: st}), server.WithAsks(&asks.Store{Store: st}), server.WithNotes(&notes.Store{Store: st}), server.WithNotifications(&notify.Store{Store: st}), server.WithAttention(&attention.Store{Store: st}), server.WithTimeline(&timeline.Store{Store: st}, &leases.Store{Store: st}, &attention.Store{Store: st}), server.WithActors(&roster.Store{Store: st, Leases: &leases.Store{Store: st}, Asks: &asks.Store{Store: st}}), server.WithCursors(&cursors.Store{Store: st}), server.WithIdempotency(&idempotency.Store{Store: st}), server.WithMeta(server.Build{Version: version.Current(), Revision: version.Revision(), Protocol: ProtocolVersion()}, &actors.Store{Store: st}), server.WithLogger(logger), stream); err != nil {
		return fail(stderr, err)
	}
	logger.Info("stopped")
	return ExitOK
}

// startupSteps readies the store before serve listens: the schema, the alert
// settings, the leases a crash left behind and the inbox, in that order, stopping
// at the first error.
func startupSteps(ctx context.Context, st *store.Store, env Env, migrate bool, logger *slog.Logger) error {
	if err := prepareSchema(ctx, st, migrate, logger); err != nil {
		return err
	}
	if err := configureNotify(st, env); err != nil {
		return err
	}
	if err := reopenLeased(ctx, st); err != nil {
		return err
	}
	return bootstrapInbox(ctx, st, env)
}

// configureNotify turns on the alerts VH_NOTIFY_URL and VH_NOTIFY_ON ask for,
// linking them to VH_PUBLIC_URL, which must then be an absolute http(s) URL when set
// (SPEC 13): with no URL no write enqueues a notification.
func configureNotify(st *store.Store, env Env) error {
	kinds, err := store.ParseNotifyOn(envvar.NotifyOn.Get(env))
	if err != nil {
		return err
	}
	if envvar.NotifyURL.Get(env) != "" {
		if err := validPublicURL(envvar.PublicURL.Get(env)); err != nil {
			return err
		}
		st.SetPublicURL(envvar.PublicURL.Get(env))
		st.SetNotify(kinds)
	}
	return nil
}

// streamSettings reads VH_STREAM_POLL and VH_STREAM_HEARTBEAT.
func streamSettings(env Env) (poll, heartbeat time.Duration, err error) {
	if poll, err = feed.ParseInterval(envvar.StreamPoll.Get(env)); err != nil {
		return 0, 0, err
	}
	if heartbeat, err = server.ParseHeartbeat(envvar.StreamHeartbeat.Get(env)); err != nil {
		return 0, 0, err
	}
	return poll, heartbeat, nil
}

// startFeed starts the change feed that tails the events table and returns the
// option that serves the stream from it, and the function that stops the feed
// and waits for it. serve has already checked the settings parse.
func startFeed(ctx context.Context, stderr io.Writer, env Env, st *store.Store) (server.Option, func(), error) {
	poll, heartbeat, err := streamSettings(env)
	if err != nil {
		return nil, nil, err
	}
	report := func(err error) { _, _ = fmt.Fprintf(stderr, "vh: change feed: %v\n", err) }
	f := feed.NewLazy(feed.StoreSource{Store: st}, feed.Options{Poll: poll, Report: report})
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); f.Run(ctx) }()
	return server.WithStream(f, heartbeat), func() { cancel(); <-done }, nil
}

// reopenLeased reopens the done tasks that hold a live lease (SPEC 4.3). A database that
// does not carry every migration (VH_MIGRATE=off on a fresh or older one) has nothing to
// reopen: serve reports it as not ready and this step waits for the next start.
func reopenLeased(ctx context.Context, st *store.Store) error {
	if st.Ready(ctx) != nil {
		return nil
	}
	_, err := (&tasks.Store{Store: st}).ReopenLeased(ctx)
	return err
}

// bootstrapInbox creates the built-in inbox initiative when none exists (SPEC 4), made by
// the operator actor. Like reopenLeased it waits for the next start on a database that is
// not migrated.
func bootstrapInbox(ctx context.Context, st *store.Store, env Env) error {
	if st.Ready(ctx) != nil {
		return nil
	}
	operator := envvar.OperatorHandle.Get(env)
	if operator == "" {
		operator = DefaultOperatorHandle
	}
	_, err := (&initiatives.Store{Store: st}).EnsureInbox(ctx, operator)
	return err
}

// prepareSchema refuses a database ahead of this binary whatever
// VH_MIGRATE says, then migrates when asked to, logging each migration applied.
func prepareSchema(ctx context.Context, st *store.Store, migrate bool, logger *slog.Logger) error {
	if err := st.CheckSchema(ctx); err != nil {
		return err
	}
	if !migrate {
		return nil
	}
	applied, err := st.MigrateApplied(ctx)
	for _, m := range applied {
		logger.Info("migration applied", "version", m.Version, "source", m.Source, "duration_ms", m.Duration.Milliseconds())
	}
	return err
}

// announce logs the start line: the build (version, revision, protocol), the
// listen address, the database dialect and the schema version the database is
// at once migrations ran (-1 with the error when it cannot be read). It never
// names the database URL, which may carry a password.
func announce(ctx context.Context, logger *slog.Logger, st *store.Store, addr string) {
	attrs := []any{"version", version.Current(), "revision", version.Revision(), "protocol", ProtocolVersion(),
		"listen", addr, "db", st.Dialect().Name()}
	schema, err := st.SchemaVersion(ctx)
	if err != nil {
		schema = -1
		attrs = append(attrs, "schema_error", err.Error())
	}
	logger.Info("starting", append(attrs, "schema", schema)...)
}

// startCheckpointer checkpoints a SQLite write-ahead log every
// VH_SQLITE_CHECKPOINT until ctx ends (`0` runs no timer), and returns the
// function that stops the timer, waits for it and checkpoints once more. The
// caller runs it on clean shutdown, after every other writer stopped. serve has
// already checked the setting parses; Postgres is unaffected.
func startCheckpointer(ctx context.Context, stderr io.Writer, env Env, st *store.Store) func() {
	interval, err := store.ParseCheckpointInterval(envvar.SQLiteCheckpoint.Get(env))
	if err != nil {
		return func() {}
	}
	report := func(err error) { _, _ = fmt.Fprintf(stderr, "vh: checkpoint: %v\n", err) }
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sweep.Run(ctx, interval, report, st.Checkpoint)
	}()
	return func() {
		cancel()
		<-done
		if err := st.Checkpoint(context.Background()); err != nil {
			report(err)
		}
	}
}

// startSweeper runs runSweeper in the background and returns the function that
// stops it and waits for it, so it never touches the store after it is closed.
func startSweeper(ctx context.Context, stderr io.Writer, env Env, st *store.Store) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSweeper(ctx, stderr, env, st)
	}()
	return func() { cancel(); <-done }
}

// runSweeper expires due leases and deletes expired sessions and stale idempotency rows every
// VH_SWEEP_INTERVAL until ctx ends; "off" runs nothing. serve has already
// checked the setting parses.
func runSweeper(ctx context.Context, stderr io.Writer, env Env, st *store.Store) {
	interval, err := sweep.ParseInterval(envvar.SweepInterval.Get(env))
	if err != nil {
		return
	}
	ls, sessions, idem := &leases.Store{Store: st}, &auth.Sessions{Store: st}, &idempotency.Store{Store: st}
	sweep.Run(ctx, interval, func(err error) { _, _ = fmt.Fprintf(stderr, "vh: sweep: %v\n", err) },
		func(ctx context.Context) error { _, err := ls.ExpireDue(ctx); return err },
		func(ctx context.Context) error { _, err := sessions.DeleteExpired(ctx); return err },
		func(ctx context.Context) error { _, err := idem.DeleteExpired(ctx); return err })
}

// startNotifier delivers the notifications outbox to VH_NOTIFY_URL every
// notify.TickInterval until ctx ends, and returns the function that stops it and
// waits for it. With the URL unset it runs nothing. serve has already checked
// the timeout and the header parse.
func startNotifier(ctx context.Context, stderr io.Writer, env Env, st *store.Store) func() {
	url := envvar.NotifyURL.Get(env)
	timeout, err := notify.ParseTimeout(envvar.NotifyTimeout.Get(env))
	header, herr := notify.ParseHeader(envvar.NotifyHeader.Get(env))
	if url == "" || err != nil || herr != nil {
		return func() {}
	}
	w := &notify.Worker{Store: st, URL: url, Header: header, Timeout: timeout}
	report := func(err error) { _, _ = fmt.Fprintf(stderr, "vh: notify: %v\n", err) }
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); sweep.Run(ctx, notify.TickInterval, report, w.Tick) }()
	return func() { cancel(); <-done }
}
