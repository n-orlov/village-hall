package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/n-orlov/village-hall/internal/actors"
	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/attention"
	"github.com/n-orlov/village-hall/internal/auth"
	"github.com/n-orlov/village-hall/internal/cursors"
	"github.com/n-orlov/village-hall/internal/events"
	"github.com/n-orlov/village-hall/internal/feed"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/notes"
	"github.com/n-orlov/village-hall/internal/notify"
	"github.com/n-orlov/village-hall/internal/roster"
	"github.com/n-orlov/village-hall/internal/tasks"
	"github.com/n-orlov/village-hall/internal/timeline"
	"github.com/n-orlov/village-hall/internal/webui"
)

// DefaultListen is the listen address when VH_LISTEN is unset.
const DefaultListen = ":8080"

// Readiness reports nil when the service can take traffic: the database is
// reachable and migrated.
type Readiness func(ctx context.Context) error

// NewHandler returns the HTTP handler the server serves. GET /healthz answers 200
// whenever the process is up and needs no credential. Every other request needs
// a Bearer key that verify accepts and a Board-Actor header (401, 400 or 503
// otherwise). GET /readyz answers 200 when ready returns nil and 503
// otherwise. Every other GET is answered from the web UI embedded in the
// binary, so GET / is its index.html.
//
// login, when not nil, turns on browser login (SPEC 3.2): a request with a valid
// session cookie is authenticated as the operator, and a request with no
// credential that is a browser navigation is sent to /auth/login.
//
// keys is the store behind /api/v1/keys; nil answers those operations with 503.
//
// WithInitiatives gives the /api/v1/initiatives operations their store; without
// it they answer 503.
func NewHandler(ready Readiness, verify KeyVerifier, login *Login, keys *auth.Keys, opts ...Option) http.Handler {
	h, _ := newHandler(ready, verify, login, keys, opts...)
	return h
}

// newHandler is NewHandler and the operations behind it, which Serve reads the
// write timeout and the stream hub from.
func newHandler(ready Readiness, verify KeyVerifier, login *Login, keys *auth.Keys, opts ...Option) (http.Handler, operations) {
	ops := operations{ready: ready, loginOps: loginOps{login: login}, keyOps: keyOps{keys: keys}, logger: discardLogger()}
	for _, opt := range opts {
		opt(&ops)
	}
	if ops.hub != nil {
		ops.hub.events = ops.events
		ops.hub.attention = ops.attention
		ops.waits = ops.hub
	}
	ops.nowOps = newNowOps(ops)
	ops.startOps = newStartOps(ops)
	ops.finishOps = newFinishOps(ops)
	ops.pingOps = newPingOps(ops)
	ops.catchupOps = newCatchupOps(ops)
	ops.block = newStartOps(ops).checkin
	mux := http.NewServeMux()
	HandlerWithOptions(strictOperations(ops), StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: parameterUnreadable})
	ui := ops.ui
	if ui == nil {
		ui = webui.Files()
	}
	mux.Handle("GET /", uiHandler(ui))
	// The feed is woken outside idempotent, once a keyed write has committed.
	h := idempotent(mux, ops.idem)
	if ops.hub != nil {
		h = wakeFeedAfterWrites(h, ops.hub.feed)
	}
	return logged(withProblemNext(authenticated(h, verify, login)), ops.logger), ops
}

// wakeFeedAfterWrites wakes the change feed once a request that may have
// written has been answered, so a local write reaches a stream without
// waiting for the poll interval.
func wakeFeedAfterWrites(next http.Handler, f *feed.Feed) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			f.Notify()
		}
	})
}

// Option adds an optional part to the handler NewHandler returns.
type Option func(*operations)

// WithUI serves the app from ui instead of the embedded build.
func WithUI(ui fs.FS) Option {
	return func(o *operations) { o.ui = ui }
}

// WithInitiatives serves /api/v1/initiatives from st.
func WithInitiatives(st *initiatives.Store) Option {
	return func(o *operations) { o.inits = st }
}

// WithTasks serves /api/v1/tasks from st, reading live leases from ls; without
// both the operations answer 503.
func WithTasks(st *tasks.Store, ls *leases.Store) Option {
	return func(o *operations) {
		o.taskOps = taskOps{tasks: st, leases: ls, asks: o.taskOps.asks, reasons: o.reasons}
		o.initiativeOps.tasks = st
		o.initiativeOps.leases = ls
	}
}

// WithEvents serves GET /api/v1/events and GET /api/v1/tasks/{ref}/events from
// st; without it they answer 503.
func WithEvents(st *events.Store) Option {
	return func(o *operations) { o.events = st }
}

// WithNotifications serves GET /api/v1/notifications from st; without it the operation answers 503.
func WithNotifications(st *notify.Store) Option {
	return func(o *operations) { o.notifyOps = notifyOps{notify: st} }
}

// WithTimeline serves GET /api/v1/timeline from st, expiring leases through ls
// and reading each task's attention from at (nil: none needs a person); without
// st and ls the operation answers 503.
func WithTimeline(st *timeline.Store, ls *leases.Store, at *attention.Store) Option {
	return func(o *operations) { o.timelineOps = timelineOps{timeline: st, leases: ls, derived: at} }
}

// WithActors serves GET /api/v1/actors and GET /api/v1/actors/{handle} from st; without it the operations answer 503.
func WithActors(st *roster.Store) Option {
	return func(o *operations) { o.actorOps = actorOps{roster: st} }
}

// WithLeases serves /api/v1/leases from st; without it the operations answer 503.
func WithLeases(st *leases.Store) Option {
	return func(o *operations) { o.leaseOps = leaseOps{leases: st} }
}

// WithAsks serves /api/v1/asks from st; without it the operations answer 503.
func WithAsks(st *asks.Store) Option {
	return func(o *operations) {
		o.askOps = askOps{asks: st, touch: o.touch, clock: o.clock, unit: o.unit}
		o.taskOps.asks = st
	}
}

// WithAskTouch sets how often a held ask long-poll renews the ask's poll_seen_at
// (DefaultAskTouch, 30 s, when unset or not above zero). Tests shorten it.
func WithAskTouch(d time.Duration) Option {
	return func(o *operations) { o.touch = d }
}

// WithAskWaitUnit sets how long one second of an ask long-poll's ?wait= lasts
// (a real second when unset or not above zero). It is a test hook: the wait is
// still asked for, and bounded to 1 to 300, in whole seconds, so a test rig that
// sets it makes a wait of 1 last d instead of a second, and no deployment sets it.
func WithAskWaitUnit(d time.Duration) Option {
	return func(o *operations) { o.unit = d }
}

// WithAskClock makes the ask waits read the time from c (the real clock when
// unset). Tests use it to order an end and a wait's until without sleeping.
func WithAskClock(c AskClock) Option {
	return func(o *operations) { o.clock = c }
}

// WithNotes serves /api/v1/notes from st; without it the operations answer 503.
func WithNotes(st *notes.Store) Option {
	return func(o *operations) { o.notes = st }
}

// WithAttention serves GET /api/v1/attention from st; without it the operation answers 503.
func WithAttention(st *attention.Store) Option {
	return func(o *operations) {
		o.attentionOps = attentionOps{attention: st}
		o.reasons = st
		o.needs = st
	}
}

// WithCursors serves PUT /api/v1/cursors/{stream} from st and lets `since=cursor`
// on the note and event lists (and `ack=true` on the note list) use it; without
// it they answer 503.
func WithCursors(st *cursors.Store) Option {
	return func(o *operations) {
		o.cursorOps = cursorOps{cursors: st}
		o.noteOps.cursors = o.cursorOps
		o.eventOps.cursors = o.cursorOps
	}
}

// WithMeta sets what GET /api/v1/meta reports about the server, and gives
// PUT /api/v1/actors/me/protocol-seen its store; without the store that
// operation answers 503.
func WithMeta(b Build, st *actors.Store) Option {
	return func(o *operations) { o.metaOps = metaOps{build: b, actors: st} }
}

// WithStream serves GET /api/v1/stream from the change feed f, with a `: ping`
// comment every heartbeat while a stream is idle, and wakes f after every
// request that may have written. Without it the operation answers 503. The
// streams end when the server shuts down.
func WithStream(f *feed.Feed, heartbeat time.Duration) Option {
	return func(o *operations) {
		o.hub = &streamHub{feed: f, heartbeat: heartbeat, stop: make(chan struct{})}
	}
}

// WithWriteTimeout sets the write timeout of the http.Server Serve runs. The
// stream moves its own write deadline before every write, so it outlives it.
// The default is none.
func WithWriteTimeout(d time.Duration) Option {
	return func(o *operations) { o.writeTimeout = d }
}

// Serve listens on addr and serves NewHandler(ready, verify, login, keys) until ctx is cancelled, then shuts
// down gracefully. It returns nil on a clean shutdown.
func Serve(ctx context.Context, addr string, ready Readiness, verify KeyVerifier, login *Login, keys *auth.Keys, opts ...Option) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	return serveOn(ctx, ln, ready, verify, login, keys, opts...)
}

func serveOn(ctx context.Context, ln net.Listener, ready Readiness, verify KeyVerifier, login *Login, keys *auth.Keys, opts ...Option) error {
	h, ops := newHandler(ready, verify, login, keys, opts...)
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, WriteTimeout: ops.writeTimeout,
		ErrorLog: slog.NewLogLogger(ops.logger.Handler(), slog.LevelError)}
	if ops.hub != nil {
		srv.RegisterOnShutdown(ops.hub.Close)
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	ops.logger.Info("listening", "addr", ln.Addr().String())
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	ops.logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
