// Package feed is the change feed of board serve: it tails the events table in
// the order of `n` (SPEC §7) and hands every event to its subscribers once, in
// order. The SSE stream and the ask long-poll are built on it, so that several
// servers on one database each see every event, whichever server took the write.
//
// A local write wakes the feed at once through Notify; the poll every
// VH_STREAM_POLL (10 s by default) is the backstop that catches a write made by
// another server on the same database. On Postgres a lower `n` can commit after a higher one, so a
// gap in `n` is held back until every transaction that could still commit the
// missing number has ended; a number still missing after that rolled back.
package feed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/n-orlov/village-hall/internal/events"
	"github.com/n-orlov/village-hall/internal/store"
)

// DefaultPoll is the poll interval when VH_STREAM_POLL is unset. It is a
// backstop, not the delivery path: a write through this server wakes the feed
// at once (Notify), so the idle tick only has to find the writes of other
// replicas, and an idle board does not read the table every second.
const DefaultPoll = 10 * time.Second

const (
	defaultBuffer = 256
	batchSize     = 500
)

// ErrOverflow is the reason a subscription ends when its subscriber did not
// keep up. The subscriber has missed events and must resynchronise.
var ErrOverflow = errors.New("feed: subscriber too slow")

// ParseInterval reads VH_STREAM_POLL. An empty value is DefaultPoll and a Go
// duration above zero is that interval. Anything else is an error: a poll
// interval of zero would spin.
func ParseInterval(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultPoll, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("VH_STREAM_POLL=%q: want a duration above zero such as 10s", v)
	}
	return d, nil
}

// Source reads the events table.
type Source interface {
	// After returns up to limit events with n above n, in order of n.
	After(ctx context.Context, n int64, limit int) ([]events.Event, error)
	// Last returns the highest n in the table, 0 when it is empty.
	Last(ctx context.Context) (int64, error)
	// Writers names the transactions that are inside an event write right now:
	// each took the event-write lock before its number was drawn and holds it
	// until it commits or rolls back. A database with one writer returns none.
	Writers(ctx context.Context) ([]string, error)
}

// StoreSource reads the events of one database.
type StoreSource struct{ Store *store.Store }

// After implements Source.
func (s StoreSource) After(ctx context.Context, n int64, limit int) ([]events.Event, error) {
	q := events.Query{Limit: limit}
	if n > 0 {
		q.Since = fmt.Sprintf("E%d", n)
	}
	p, err := events.Store{Store: s.Store}.List(ctx, q)
	if err != nil {
		return nil, err
	}
	return p.Items, nil
}

// Last implements Source.
func (s StoreSource) Last(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	if err := s.Store.ReaderFor(ctx).QueryRowContext(ctx, `SELECT MAX(n) FROM events`).Scan(&n); err != nil {
		return 0, fmt.Errorf("read last event: %w", err)
	}
	return n.Int64, nil
}

// Writers implements Source.
func (s StoreSource) Writers(ctx context.Context) ([]string, error) {
	q := s.Store.Dialect().EventWriters()
	if q == "" {
		return nil, nil
	}
	rows, err := s.Store.ReaderFor(ctx).QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("read event writers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, fmt.Errorf("read event writers: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read event writers: %w", err)
	}
	return out, nil
}

// Options tunes a Feed. The zero value is the production setting.
type Options struct {
	// Poll is the poll interval; 0 means DefaultPoll.
	Poll time.Duration
	// Buffer is the size of each subscription's channel; 0 means 256.
	Buffer int
	// Tick makes the poll ticker; nil means a time.Ticker. Tests inject it.
	Tick func(d time.Duration) (ticks <-chan time.Time, stop func())
	// Report receives the errors of a failed poll; the feed retries on the next
	// tick whatever it does.
	Report func(error)
}

// Feed tails the events table and fans events out to subscribers.
type Feed struct {
	src  Source
	opts Options
	wake chan struct{}

	mu   sync.Mutex
	last int64
	subs map[*Subscription]struct{}
	// settled is the number up to which every missing `n` is known to have
	// rolled back: an event at or below it is delivered across a gap.
	settled int64
	// pend is the open gap the feed waits on, nil when there is none.
	pend *pending
	// unresolved is true until a lazy feed has read the newest event number.
	unresolved bool
}

// pending is a gap in `n` below bound, the highest number a read saw, and the
// transactions that held the event-write lock right after that read. Every
// missing number below bound was drawn by one of them or by a transaction that
// had already ended, so once all of them have ended a fresh read shows every
// missing number that committed, and the rest rolled back.
type pending struct {
	bound   int64
	holders []string
}

// New returns a feed positioned at the newest event: it delivers the events
// written from now on. Run starts it.
func New(ctx context.Context, src Source, opts Options) (*Feed, error) {
	last, err := src.Last(ctx)
	if err != nil {
		return nil, err
	}
	return NewAt(src, last, opts), nil
}

// NewLazy returns a feed that is positioned at the newest event as of its first
// successful read of the table, which Poll tries until it works. A server
// started with VH_MIGRATE=off on a database that is not migrated yet, or
// that cannot be reached yet, serves and starts delivering once the table is
// readable (GET /readyz says why it is not ready until then). A subscription
// made before that has After 0 and carries the events from that read on.
func NewLazy(src Source, opts Options) *Feed {
	f := NewAt(src, 0, opts)
	f.unresolved = true
	return f
}

// NewAt returns a feed that delivers the events after n.
func NewAt(src Source, n int64, opts Options) *Feed {
	if opts.Poll <= 0 {
		opts.Poll = DefaultPoll
	}
	if opts.Buffer <= 0 {
		opts.Buffer = defaultBuffer
	}
	if opts.Tick == nil {
		opts.Tick = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	return &Feed{src: src, opts: opts, wake: make(chan struct{}, 1), last: n, subs: map[*Subscription]struct{}{}}
}

// Notify wakes the feed at once; a write calls it after it commits. It never
// blocks and wakes are coalesced.
func (f *Feed) Notify() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// Run tails the table until ctx ends, then ends every subscription.
func (f *Feed) Run(ctx context.Context) {
	defer f.closeAll(context.Canceled)
	ticks, stop := f.opts.Tick(f.opts.Poll)
	defer stop()
	for {
		f.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		case <-f.wake:
		}
	}
}

// Poll reads and delivers every event after the last delivered one.
func (f *Feed) Poll(ctx context.Context) {
	for {
		full, err := f.pollOnce(ctx)
		if err != nil {
			if ctx.Err() == nil && f.opts.Report != nil {
				f.opts.Report(err)
			}
			return
		}
		if !full {
			return
		}
	}
}

// pollOnce reads one batch and delivers it. It reports whether to read again at
// once: the batch was full, or it opened a gap whose writers may already be gone.
func (f *Feed) pollOnce(ctx context.Context) (bool, error) {
	from, pend, ok := f.position(ctx)
	if !ok {
		return false, nil
	}
	settle, err := f.settles(ctx, pend)
	if err != nil {
		return false, err
	}
	items, err := f.src.After(ctx, from, batchSize)
	if err != nil {
		return false, err
	}
	open, last := f.advance(items, pend, settle)
	if open {
		if err := f.openGap(ctx, items[len(items)-1].N); err != nil {
			return false, err
		}
	}
	return open || readAgain(len(items), last, from), nil
}

// readAgain reports whether a full batch moved the feed forward, so the next one
// may be waiting.
func readAgain(n int, last, from int64) bool { return n == batchSize && last > from }

// position returns the number the next read starts after and the gap the feed
// waits on, resolving a lazy feed first. It reports false while the table cannot
// be read.
func (f *Feed) position(ctx context.Context) (int64, *pending, bool) {
	if !f.resolve(ctx) {
		return 0, nil, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last, f.pend, true
}

// resolve positions a lazy feed at the newest event. It reports whether the
// feed has a position; a table that cannot be read yet is not an error, the
// next poll tries again.
func (f *Feed) resolve(ctx context.Context) bool {
	f.mu.Lock()
	unresolved := f.unresolved
	f.mu.Unlock()
	if !unresolved {
		return true
	}
	last, err := f.src.Last(ctx)
	if err != nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unresolved {
		f.last, f.unresolved = last, false
	}
	return true
}

// settles reads the writers BEFORE the table, while a gap is open, and returns
// the gap's bound once none of its holders is left (0 until then). A writer that
// ends after this check ended before the read that follows, so that read shows
// its event if it committed one.
func (f *Feed) settles(ctx context.Context, pend *pending) (int64, error) {
	if pend == nil {
		return 0, nil
	}
	w, err := f.src.Writers(ctx)
	if err != nil {
		return 0, err
	}
	left := still(pend.holders, w)
	f.mu.Lock()
	defer f.mu.Unlock()
	pend.holders = left
	if len(left) > 0 {
		return 0, nil
	}
	return pend.bound, nil
}

// advance delivers the events of one read up to the first gap that is not
// settled. It reports whether it stopped at a gap no pending entry covers yet,
// and the number of the last event delivered.
func (f *Feed) advance(items []events.Event, pend *pending, settle int64) (bool, int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if settle > 0 && f.pend == pend {
		f.settled, f.pend = max(f.settled, settle), nil
	}
	held := f.deliverUpToGap(items)
	if f.pend != nil && f.last >= f.pend.bound {
		f.pend = nil // the gap closed by itself
	}
	return held && f.pend == nil, f.last
}

// deliverUpToGap delivers items in order until one skips past a number that is
// neither delivered nor settled. It reports whether it stopped there. The
// caller holds f.mu.
func (f *Feed) deliverUpToGap(items []events.Event) bool {
	for _, e := range items {
		if e.N <= f.last {
			continue
		}
		if e.N != f.last+1 && e.N > f.settled {
			return true
		}
		f.last = e.N
		f.deliver(e)
	}
	return false
}

// openGap reads the writers AFTER the table that showed a gap below bound: every
// missing number below it was drawn by a writer that holds the lock now or that
// has already ended.
func (f *Feed) openGap(ctx context.Context, bound int64) error {
	w, err := f.src.Writers(ctx)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pend == nil {
		f.pend = &pending{bound: bound, holders: w}
	}
	return nil
}

// still returns the holders that are still among the current writers.
func still(holders, writers []string) []string {
	var out []string
	for _, h := range holders {
		if slices.Contains(writers, h) {
			out = append(out, h)
		}
	}
	return out
}

// deliver sends e to every subscriber, ending those whose buffer is full.
func (f *Feed) deliver(e events.Event) {
	for s := range f.subs {
		select {
		case s.ch <- e:
		default:
			f.end(s, ErrOverflow)
		}
	}
}

// end removes s and closes its channel. The caller holds f.mu.
func (f *Feed) end(s *Subscription, err error) {
	if _, ok := f.subs[s]; !ok {
		return
	}
	delete(f.subs, s)
	s.err = err
	close(s.ch)
}

func (f *Feed) closeAll(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for s := range f.subs {
		f.end(s, err)
	}
}

// Subscription is one subscriber's view of the feed.
type Subscription struct {
	f  *Feed
	ch chan events.Event
	// After is the number of the last event the feed had delivered when the
	// subscription began: every event it carries has a higher n.
	After int64
	err   error
}

// Subscribe starts a subscription. The events it carries are exactly those with
// n above Subscription.After, so a subscriber that replays from the table up to
// After and then reads the channel misses nothing and sees nothing twice. It
// costs no goroutine. Close it when done.
func (f *Feed) Subscribe() *Subscription {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &Subscription{f: f, ch: make(chan events.Event, f.opts.Buffer), After: f.last}
	f.subs[s] = struct{}{}
	return s
}

// Position is the number of the last event the feed has delivered: every event
// up to it is already in the subscribers' channels.
func (f *Feed) Position() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// Events is the channel of events, in order. It is closed when the subscription
// ends: Err then says why.
func (s *Subscription) Events() <-chan events.Event { return s.ch }

// Err is why the channel closed: nil after Close, ErrOverflow when the
// subscriber was too slow and context.Canceled when the feed stopped. Read it
// only after the channel has closed.
func (s *Subscription) Err() error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	return s.err
}

// Close ends the subscription. It is safe to call more than once.
func (s *Subscription) Close() {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.end(s, nil)
}
