package notify

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/n-orlov/village-hall/internal/store"
)

// Backoff is the wait before each retry of a failed delivery: the first retry
// follows the first failure after 30 s, and so on. The sixth failure, the
// failure of the fifth retry, ends the row `failed`.
var Backoff = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour}

// TickInterval is how often Run looks for due rows when nothing woke it: a
// backstop that finds the retries whose backoff has run out. A write that
// enqueues a row wakes the worker at once (Wake), so it does not set the latency.
const TickInterval = 10 * time.Second

// claimBatch is how many due rows one tick looks at.
const claimBatch = 20

// claimSlack is added to the timeout to give how long a server holds the claim
// on a row it is delivering, so a slow receiver is never raced by another server.
const claimSlack = 30 * time.Second

// Worker delivers the outbox of serve: it claims due rows with a conditional
// update and POSTs each payload to URL.
type Worker struct {
	Store   *store.Store
	URL     string
	Header  Header
	Timeout time.Duration
	// Now is the clock of the tick, the claim lapse and the backoff; nil means time.Now.
	Now func() time.Time
	// Client sends the POST; nil means a client that follows no redirect.
	Client *http.Client
	// Interval is the backstop tick of Run; zero means TickInterval.
	Interval time.Duration
	// NewTicker makes the backstop ticker of Run; nil means a time.Ticker. Tests inject it.
	NewTicker func(d time.Duration) (ticks <-chan time.Time, stop func())

	wakeOnce sync.Once
	wake     chan struct{}
}

func (w *Worker) wakeCh() chan struct{} {
	w.wakeOnce.Do(func() { w.wake = make(chan struct{}, 1) })
	return w.wake
}

// Wake asks Run for a pass at once. It never blocks and wakes are coalesced.
func (w *Worker) Wake() {
	select {
	case w.wakeCh() <- struct{}{}:
	default:
	}
}

// Run delivers the outbox until ctx ends: one pass when it starts and after each
// Wake, and one every Interval as the backstop. While it runs, Enqueue wakes it
// once its write has committed. A pass's error goes to report (when set) and never
// stops Run.
func (w *Worker) Run(ctx context.Context, report func(error)) {
	ticks, stop := w.backstop()
	defer stop()
	w.Store.SetNotifyWake(w.Wake)
	defer w.Store.SetNotifyWake(nil)
	for {
		w.pass(ctx, report)
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		case <-w.wakeCh():
		}
	}
}

// pass is one delivery pass; its error goes to report (when set) unless the server is shutting down.
func (w *Worker) pass(ctx context.Context, report func(error)) {
	if err := w.Tick(ctx); err != nil && report != nil && ctx.Err() == nil {
		report(err)
	}
}

// backstop is the clock of Run's periodic pass: NewTicker (a real ticker when unset) every Interval
// (TickInterval when unset).
func (w *Worker) backstop() (<-chan time.Time, func()) {
	interval := w.Interval
	if interval <= 0 {
		interval = TickInterval
	}
	if w.NewTicker != nil {
		return w.NewTicker(interval)
	}
	t := time.NewTicker(interval)
	return t.C, t.Stop
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) client() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type row struct {
	id, payload string
	attempts    int
}

// Tick delivers every row that is due at the clock's now and not claimed.
func (w *Worker) Tick(ctx context.Context) error {
	due, err := w.due(ctx)
	if err != nil {
		return err
	}
	for _, r := range due {
		if more, err := w.deliver(ctx, r); err != nil || !more {
			return err
		}
	}
	return nil
}

// deliver claims and delivers one row. It reports false when the tick should
// stop: the server is shutting down, so the row keeps its claim and is delivered
// again once the claim lapses.
func (w *Worker) deliver(ctx context.Context, r row) (bool, error) {
	if ctx.Err() != nil {
		return false, nil
	}
	won, err := w.claim(ctx, r.id)
	if err != nil || !won {
		return err == nil, err
	}
	failure := w.post(ctx, r.payload)
	if ctx.Err() != nil {
		return false, nil
	}
	return true, w.finish(ctx, r, failure)
}

func (w *Worker) due(ctx context.Context) ([]row, error) {
	now := store.EncodeTime(w.now())
	rows, err := w.Store.ReaderFor(ctx).QueryContext(ctx, w.Store.Dialect().Rebind(`SELECT id, payload, attempts FROM notifications
		WHERE state = 'pending' AND next_attempt_at <= ? AND (claimed_until IS NULL OR claimed_until <= ?)
		ORDER BY next_attempt_at, id LIMIT `+fmt.Sprint(claimBatch)), now, now)
	if err != nil {
		return nil, fmt.Errorf("read due notifications: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.payload, &r.attempts); err != nil {
			return nil, fmt.Errorf("read due notifications: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// claim takes the row for the claim period. The update is conditional on the
// row still being due and unclaimed, so of several servers one wins.
func (w *Worker) claim(ctx context.Context, id string) (bool, error) {
	now := w.now()
	res, err := w.Store.WriterFor(ctx).ExecContext(ctx, w.Store.Dialect().Rebind(`UPDATE notifications SET claimed_until = ?
		WHERE id = ? AND state = 'pending' AND next_attempt_at <= ? AND (claimed_until IS NULL OR claimed_until <= ?)`),
		store.EncodeTime(now.Add(w.Timeout+claimSlack)), id, store.EncodeTime(now), store.EncodeTime(now))
	if err != nil {
		return false, fmt.Errorf("claim notification: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// post sends the payload and returns "" for any received 2xx status, whatever
// its body, else the error: the HTTP status or an error class, never a body, a
// URL or a header value. The timeout covers connect to the status line.
func (w *Worker) post(ctx context.Context, payload string) string {
	ctx, cancel := context.WithTimeout(ctx, w.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader([]byte(payload)))
	if err != nil {
		return "bad request"
	}
	req.Header.Set("Content-Type", "application/json")
	if w.Header.Name != "" {
		req.Header.Set(w.Header.Name, w.Header.Value)
	}
	resp, err := w.client().Do(req)
	if err != nil {
		return errorClass(err)
	}
	defer func() { _ = resp.Body.Close() }()
	// The status line decides: any received 2xx is a success whatever the body
	// does afterwards, so the body is never read and a stalled or truncated one
	// cannot fail the delivery.
	if resp.StatusCode/100 == 2 {
		return ""
	}
	return fmt.Sprintf("http %d", resp.StatusCode)
}

func errorClass(err error) string {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return "timeout"
	}
	return "network error"
}

// finish records the outcome of one delivery and releases the claim.
func (w *Worker) finish(ctx context.Context, r row, failure string) error {
	now := w.now()
	state, next, lastErr, sentAt := "sent", store.EncodeTime(now), sql.NullString{}, sql.NullInt64{Int64: store.EncodeTime(now), Valid: true}
	if failure != "" {
		state, lastErr, sentAt = "pending", sql.NullString{String: failure, Valid: true}, sql.NullInt64{}
		if r.attempts < len(Backoff) {
			next = store.EncodeTime(now.Add(Backoff[r.attempts]))
		} else {
			state = "failed"
		}
	}
	_, err := w.Store.WriterFor(ctx).ExecContext(ctx, w.Store.Dialect().Rebind(`UPDATE notifications
		SET state = ?, attempts = ?, next_attempt_at = ?, claimed_until = NULL, last_error = COALESCE(CAST(? AS TEXT), last_error), sent_at = ?
		WHERE id = ?`), state, r.attempts+1, next, lastErr, sentAt, r.id)
	if err != nil {
		return fmt.Errorf("record notification delivery: %w", err)
	}
	return nil
}
