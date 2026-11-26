package server

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/n-orlov/village-hall/internal/attention"
)

// AttentionRecheck is the longest a stream with the attention topic goes
// without recomputing the counts: overdue watches and expiring leases change
// with time, not with an event (SPEC 7.1).
const AttentionRecheck = 30 * time.Second

// recheckEvery is the interval of the time-driven recompute: AttentionRecheck,
// or the heartbeat when that is shorter, so a test that shortens the heartbeat
// shortens the recheck with it.
func recheckEvery(heartbeat time.Duration) time.Duration {
	return min(heartbeat, AttentionRecheck)
}

// countsWatch is one stream's view of the attention counts: it recomputes them
// and tells whether they differ from the ones the stream sent last. A watch with
// no filter takes its recomputations from the hub, which shares one among every
// such stream; a watch with a filter reads its own.
type countsWatch struct {
	hub   *streamHub
	query attention.Query
	last  *attention.Counts
	gen   int64 // the generation of the newest shared computation this watch has taken counts from
}

// unscoped reports whether the watch counts the whole board, so its counts are
// the ones every other such stream sends.
func (w *countsWatch) unscoped() bool {
	return len(w.query.Initiatives) == 0 && len(w.query.Tags) == 0
}

// poll reads the counts afresh, for the watch's own query. changed is true the
// first time and whenever they differ from the previous call's.
func (w *countsWatch) poll(ctx context.Context) (counts attention.Counts, changed bool, err error) {
	if w.unscoped() {
		var gen int64
		counts, gen, err = w.hub.shared.fresh(ctx, w.hub)
		if err != nil {
			return counts, false, err
		}
		w.gen = max(w.gen, gen)
		return counts, w.note(counts), nil
	}
	counts, err = w.hub.countsFor(ctx, w.query)
	if err != nil {
		return counts, false, err
	}
	return counts, w.note(counts), nil
}

// pollAfter is poll for the counts an event moved: an unscoped watch takes the
// computation the hub made after that event (or makes it for every stream).
func (w *countsWatch) pollAfter(ctx context.Context, n int64) (counts attention.Counts, changed bool, err error) {
	if !w.unscoped() {
		return w.poll(ctx)
	}
	counts, gen, err := w.hub.shared.afterEvent(ctx, w.hub, n)
	if err != nil {
		return counts, false, err
	}
	w.gen = max(w.gen, gen)
	return counts, w.note(counts), nil
}

// noteShared records counts the hub's recheck computed at generation gen, and
// tells whether they differ from the ones sent last. A computation older than one
// the watch already took counts from is stale (an event moved the counts while it
// ran), so it is dropped rather than sent as a step back.
func (w *countsWatch) noteShared(counts attention.Counts, gen int64) bool {
	if gen < w.gen {
		return false
	}
	w.gen = gen
	return w.note(counts)
}

// note records counts as the ones sent last and tells whether they differ from
// the ones before.
func (w *countsWatch) note(counts attention.Counts) bool {
	changed := w.last == nil || *w.last != counts
	w.last = &counts
	return changed
}

// countsFor computes the counts of query, through the hook a test sets or the
// attention store.
func (h *streamHub) countsFor(ctx context.Context, query attention.Query) (attention.Counts, error) {
	if h.countsOf != nil {
		return h.countsOf(ctx, query)
	}
	return h.attention.Counts(ctx, query)
}

// countsShare is the unscoped counts the streams of one hub share: the last
// computation, and the streams waiting for the next recheck.
type countsShare struct {
	mu     sync.Mutex // held while computing, so the streams behind the first reuse its result
	have   bool
	gen    int64 // counts the computations made, in the order they ran (under mu)
	after  int64 // the newest event the feed had delivered when counts were computed
	counts attention.Counts

	tickMu  sync.Mutex
	tickers map[chan sharedCounts]struct{}
	running bool
}

// sharedCounts is one computation of the unscoped counts and its generation.
type sharedCounts struct {
	counts attention.Counts
	gen    int64
}

// afterEvent returns counts computed after the feed delivered event n: the last
// computation when it already was, else a new one made after the feed's whole
// batch (its position when the computation starts), which every other stream
// handling an event of that batch then reuses.
func (c *countsShare) afterEvent(ctx context.Context, h *streamHub, n int64) (attention.Counts, int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.have && c.after >= n {
		return c.counts, c.gen, nil
	}
	return c.computeLocked(ctx, h, n)
}

// fresh computes the unscoped counts now, whatever the last computation holds.
func (c *countsShare) fresh(ctx context.Context, h *streamHub) (attention.Counts, int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.computeLocked(ctx, h, 0)
}

// computeLocked computes the counts after the feed's position (or event n, when
// that is further) and keeps them as the newest computation. The caller holds mu,
// so computations run, and are numbered, one at a time: a larger generation always
// read the database later.
func (c *countsShare) computeLocked(ctx context.Context, h *streamHub, n int64) (attention.Counts, int64, error) {
	n = max(n, h.feed.Position())
	counts, err := h.countsFor(ctx, attention.Query{})
	if err != nil {
		return counts, 0, err
	}
	c.gen++
	c.have, c.after, c.counts = true, n, counts
	return counts, c.gen, nil
}

// recheck joins the hub's recheck: the returned channel receives the unscoped
// counts the hub computes once for every stream, every recheckEvery. stop leaves.
func (h *streamHub) recheck() (counts <-chan sharedCounts, stop func()) {
	c := &h.shared
	ch := make(chan sharedCounts, 1)
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	if c.tickers == nil {
		c.tickers = map[chan sharedCounts]struct{}{}
	}
	c.tickers[ch] = struct{}{}
	if !c.running {
		c.running = true
		go h.recheckLoop()
	}
	return ch, func() {
		c.tickMu.Lock()
		defer c.tickMu.Unlock()
		delete(c.tickers, ch)
	}
}

// recheckTicks is the clock of the recheck: a ticker of recheckEvery, or the
// one a test sets.
func (h *streamHub) recheckTicks() (<-chan time.Time, func()) {
	if h.ticks != nil {
		return h.ticks()
	}
	t := time.NewTicker(recheckEvery(h.heartbeat))
	return t.C, t.Stop
}

// recheckLoop computes the unscoped counts once per tick and hands them to every
// stream that joined. It ends with the hub, or when the last stream left.
func (h *streamHub) recheckLoop() {
	c := &h.shared
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.cancelOnClose(ctx, cancel)
	ticks, stopTicks := h.recheckTicks()
	defer stopTicks()
	for {
		select {
		case <-ctx.Done():
			c.stopRunning()
			return
		case <-ticks:
		}
		if !c.anyTicker() {
			c.stopRunning()
			return
		}
		c.mu.Lock()
		counts, gen, err := c.computeLocked(ctx, h, 0)
		c.mu.Unlock()
		if err != nil { // the next tick tries again
			continue
		}
		c.deliver(sharedCounts{counts: counts, gen: gen})
	}
}

// cancelOnClose calls cancel when the hub closes, or returns when ctx ends first.
func (h *streamHub) cancelOnClose(ctx context.Context, cancel context.CancelFunc) {
	select {
	case <-h.stop:
		cancel()
	case <-ctx.Done():
	}
}

// stopRunning records that the recheck loop has ended, so the next stream to join starts one.
func (c *countsShare) stopRunning() {
	c.tickMu.Lock()
	c.running = false
	c.tickMu.Unlock()
}

// anyTicker reports whether a stream is waiting for the recheck.
func (c *countsShare) anyTicker() bool {
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	return len(c.tickers) > 0
}

// deliver hands counts to every stream waiting for the recheck, replacing a pair it has not read yet.
func (c *countsShare) deliver(counts sharedCounts) {
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	for ch := range c.tickers {
		select {
		case <-ch:
		default:
		}
		ch <- counts
	}
}

// attentionWatch is the watch of a stream that asked for the attention topic,
// nil when it did not or the server has no attention store.
func (r streamResponse) attentionWatch() *countsWatch {
	if !r.topics.attention || r.hub.attention == nil {
		return nil
	}
	return &countsWatch{hub: r.hub, query: r.query}
}

// attentionMessage sends `event: attention` with the counts as one JSON line.
func (s *streamWriter) attention(c attention.Counts) bool {
	data, err := json.Marshal(apiCounts(c))
	if err != nil {
		return false
	}
	return s.write("event: attention\ndata: " + string(data) + "\n\n")
}

// sendCounts recomputes the counts and sends them when they changed. A failed
// read is not the end of the stream: the next event or recheck tries again.
func (s *liveStream) sendCounts() bool {
	if s.watch == nil {
		return true
	}
	counts, changed, err := s.watch.poll(s.r.ctx)
	return err != nil || !changed || s.out.attention(counts)
}

// sendCountsAfter is sendCounts for the counts event n moved, which an unscoped
// stream shares with every other.
func (s *liveStream) sendCountsAfter(n int64) bool {
	if s.watch == nil {
		return true
	}
	counts, changed, err := s.watch.pollAfter(s.r.ctx, n)
	return err != nil || !changed || s.out.attention(counts)
}

// sendRechecked sends the counts of the hub's recheck when they changed.
func (s *liveStream) sendRechecked(shared sharedCounts) bool {
	return !s.watch.noteShared(shared.counts, shared.gen) || s.out.attention(shared.counts)
}
