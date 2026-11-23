package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/auth"
	"github.com/n-orlov/village-hall/internal/events"
	"github.com/n-orlov/village-hall/internal/feed"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/store"
)

// streamSettings tune one server of a rig.
type streamSettings struct {
	poll, heartbeat, writeTimeout time.Duration
	// extra adds options to the server, given its store.
	extra func(*store.Store) []Option
}

// rig is one running board server: the real handler on a real listener over a
// real database, with the change feed the stream is served from.
type rig struct {
	t        *testing.T
	base     string
	token    string
	cancel   context.CancelFunc
	done     chan error
	feedDone chan struct{}
	feed     *feed.Feed
}

// openStore opens and migrates the database at url, closed when t ends.
func openStore(t *testing.T, url string) *store.Store {
	t.Helper()
	st, err := store.Open(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// startRig serves st on a free port. The server stops when t ends or when
// stop is called, whichever comes first.
func startRig(t *testing.T, st *store.Store, token string, cfg streamSettings) *rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f, err := feed.New(ctx, feed.StoreSource{Store: st}, feed.Options{Poll: cfg.poll})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, feed: f, token: token, cancel: cancel, done: make(chan error, 1), feedDone: make(chan struct{})}
	go func() { defer close(r.feedDone); f.Run(ctx) }()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.base = "http://" + ln.Addr().String()
	keys := &auth.Keys{Store: st}
	opts := []Option{WithInitiatives(&initiatives.Store{Store: st}), WithEvents(&events.Store{Store: st}), WithStream(f, cfg.heartbeat)}
	if cfg.writeTimeout > 0 {
		opts = append(opts, WithWriteTimeout(cfg.writeTimeout))
	}
	if cfg.extra != nil {
		opts = append(opts, cfg.extra(st)...)
	}
	go func() { r.done <- serveOn(ctx, ln, ok, (&auth.Verifier{Store: st}).Verify, nil, keys, opts...) }()
	t.Cleanup(func() { _ = r.stop() })
	return r
}

// stop shuts the server down and waits for it and its feed; it returns what
// the server returned.
func (r *rig) stop() error {
	r.cancel()
	select {
	case err := <-r.done:
		<-r.feedDone
		r.done <- err
		return err
	case <-time.After(15 * time.Second):
		r.t.Error("the server did not stop within 15 s")
		return context.DeadlineExceeded
	}
}

var noKeepAlive = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

// do sends a request as the key holder; token "" sends no credential.
func (r *rig) do(method, path, body string, token string) *http.Response {
	r.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, r.base+path, strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(ActorHeader, "tester@host")
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := noKeepAlive.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	return resp
}

// createInitiative writes one event through the server's own API.
func (r *rig) createInitiative(slug string) {
	r.t.Helper()
	resp := r.do(http.MethodPost, "/api/v1/initiatives", fmt.Sprintf(`{"slug":%q,"title":"Title of %s"}`, slug, slug), r.token)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		r.t.Fatalf("create %s = %d %s", slug, resp.StatusCode, b)
	}
}

// sse is an open stream whose messages are read block by block.
type sse struct {
	t      *testing.T
	resp   *http.Response
	blocks chan string
}

// openStream connects to path and returns once the server has sent the headers.
func (r *rig) openStream(path string) *sse { return r.openStreamWith(path, nil) }

// openStreamWith is openStream with extra request headers, such as Last-Event-ID.
func (r *rig) openStreamWith(path string, header map[string]string) *sse {
	r.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, r.base+path, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set(ActorHeader, "tester@host")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := noKeepAlive.Do(req) //nolint:bodyclose // the body is closed by sse.close, registered with t.Cleanup below
	if err != nil {
		r.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		r.t.Fatalf("GET %s = %d %s", path, resp.StatusCode, b)
	}
	s := &sse{t: r.t, resp: resp, blocks: make(chan string, 64)}
	go func() {
		defer close(s.blocks)
		sc := bufio.NewScanner(resp.Body)
		var block []string
		for sc.Scan() {
			if line := sc.Text(); line != "" {
				block = append(block, line)
				continue
			}
			s.blocks <- strings.Join(block, "\n")
			block = nil
		}
	}()
	r.t.Cleanup(s.close)
	return s
}

func (s *sse) close() { _ = s.resp.Body.Close() }

// next returns the next block (one message or one comment), failing when none
// arrives within d or the stream ends.
func (s *sse) next(d time.Duration) string {
	s.t.Helper()
	select {
	case b, ok := <-s.blocks:
		if !ok {
			s.t.Fatal("the stream ended")
		}
		return b
	case <-time.After(d):
		s.t.Fatalf("no message within %v", d)
		return ""
	}
}

// nextMessage returns the next block that is not a comment, failing after two seconds.
func (s *sse) nextMessage() string {
	s.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b := s.next(time.Until(deadline))
		if !strings.HasPrefix(b, ":") {
			return b
		}
	}
}

// ended reports whether the stream has been closed by the server within d.
func (s *sse) ended(d time.Duration) bool {
	select {
	case _, ok := <-s.blocks:
		return !ok
	case <-time.After(d):
		return false
	}
}

func newToken(t *testing.T, st *store.Store) string {
	t.Helper()
	token, _, err := (&auth.Keys{Store: st}).Create(context.Background(), "caller", "admin-cli", 0)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// newRig opens and migrates a database of the leg and serves it.
func newRig(t *testing.T, dbs databases, cfg streamSettings) *rig {
	t.Helper()
	st := openStore(t, dbs.New(t))
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return startRig(t, st, newToken(t, st), cfg)
}

func TestAnEventWrittenAfterConnectArrivesWithinTwoSeconds(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		// A poll of an hour: only the wake a local write gives can deliver it in time.
		r := newRig(t, dbs, streamSettings{poll: time.Hour, heartbeat: time.Minute})
		s := r.openStream("/api/v1/stream?topics=events")
		h := s.resp.Header
		for name, want := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no"} {
			if got := h.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		r.createInitiative("aa")
		msg := s.nextMessage()
		lines := strings.Split(msg, "\n")
		if len(lines) != 3 || lines[0] != "event: board" || !strings.HasPrefix(lines[1], "id: ") || !strings.HasPrefix(lines[2], "data: ") {
			t.Fatalf("message = %q, want event/id/data lines", msg)
		}
		var streamed map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[2], "data: ")), &streamed); err != nil {
			t.Fatalf("data is not one JSON line: %v", err)
		}
		if streamed["id"] != strings.TrimPrefix(lines[1], "id: ") || streamed["type"] != "initiative_created" {
			t.Errorf("streamed event = %v", streamed)
		}
		listed := r.do(http.MethodGet, "/api/v1/events?limit=1", "", r.token)
		defer func() { _ = listed.Body.Close() }()
		var page struct{ Items []map[string]any }
		if err := json.NewDecoder(listed.Body).Decode(&page); err != nil || len(page.Items) != 1 {
			t.Fatalf("GET /events: %v %v", err, page)
		}
		if !reflect.DeepEqual(page.Items[0], streamed) {
			t.Errorf("streamed %v differs from GET /events %v", streamed, page.Items[0])
		}
	})
}

func TestEveryEventIsOneMessageInOrder(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		r := newRig(t, dbs, streamSettings{poll: time.Hour, heartbeat: time.Minute})
		s := r.openStream("/api/v1/stream")
		for _, slug := range []string{"aa", "bb", "cc"} {
			r.createInitiative(slug)
		}
		for i, slug := range []string{"aa", "bb", "cc"} {
			msg := s.nextMessage()
			var e map[string]any
			data := msg[strings.Index(msg, "data: ")+len("data: "):]
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				t.Fatalf("message %d = %q: %v", i, msg, err)
			}
			if e["ref"] != fmt.Sprintf("E%d", i+1) || e["initiative"] != slug {
				t.Errorf("message %d is %v, want E%d on %s", i, e, i+1, slug)
			}
		}
	})
}

// initiativeOf returns the initiative of the next event message of s.
func initiativeOf(s *sse) any {
	s.t.Helper()
	msg := s.nextMessage()
	var e map[string]any
	if err := json.Unmarshal([]byte(msg[strings.Index(msg, "data: ")+len("data: "):]), &e); err != nil {
		s.t.Fatalf("message %q: %v", msg, err)
	}
	return e["initiative"]
}

// SPEC 7.1: a stream starts at the change feed's position when it connects. An
// event the feed delivered before the connection is not sent; one committed
// before it that the feed had not read yet is sent, so nothing is lost between
// a client's read of the board and its stream.
func TestAStreamStartsAtTheFeedPositionWhenItConnects(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		st := openStore(t, dbs.New(t))
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		// A poll of an hour: the feed reads only when a write through the server wakes it.
		r := startRig(t, st, newToken(t, st), streamSettings{poll: time.Hour, heartbeat: time.Minute})
		first := r.openStream("/api/v1/stream?topics=events")
		r.createInitiative("delivered")
		if got := initiativeOf(first); got != "delivered" {
			t.Fatalf("first stream got %v, want delivered", got)
		}
		// Committed beside the server, so nothing wakes the feed: it stays unread.
		if _, err := (initiatives.Store{Store: st}).Create(context.Background(), audit.Actor{Handle: "beside@host"}, "key:caller",
			initiatives.New{Slug: "unread", Title: "Title of unread"}); err != nil {
			t.Fatal(err)
		}
		second := r.openStream("/api/v1/stream?topics=events")
		r.createInitiative("after")
		for _, want := range []string{"unread", "after"} {
			if got := initiativeOf(second); got != want {
				t.Errorf("second stream got %v, want %s (the delivered event is not sent again)", got, want)
			}
		}
	})
}

func TestTopicsAreCheckedAndAuthenticationIsRequired(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		r := newRig(t, dbs, streamSettings{poll: time.Hour, heartbeat: time.Minute})
		for path, want := range map[string]int{
			"/api/v1/stream?topics=events":           http.StatusOK,
			"/api/v1/stream?topics=attention":        http.StatusOK,
			"/api/v1/stream?topics=events,attention": http.StatusOK,
			"/api/v1/stream":                         http.StatusOK,
			"/api/v1/stream?topics=bogus":            http.StatusBadRequest,
			"/api/v1/stream?topics=events,bogus":     http.StatusBadRequest,
			"/api/v1/stream?topics=":                 http.StatusBadRequest,
		} {
			resp := r.do(http.MethodGet, path, "", r.token)
			if resp.StatusCode != want {
				t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
			}
			if want == http.StatusBadRequest {
				var doc map[string]any
				_ = json.NewDecoder(resp.Body).Decode(&doc)
				if next, _ := doc["next"].(string); !strings.Contains(next, "topics=") {
					t.Errorf("GET %s: problem %v does not say how to fix it", path, doc)
				}
			}
			_ = resp.Body.Close()
		}
		resp := r.do(http.MethodGet, "/api/v1/stream", "", "")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET /api/v1/stream without a credential = %d, want 401", resp.StatusCode)
		}
	})
}

func TestAnIdleStreamSendsAPingAtTheConfiguredHeartbeat(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		r := newRig(t, dbs, streamSettings{poll: time.Hour, heartbeat: 50 * time.Millisecond})
		s := r.openStream("/api/v1/stream?topics=events")
		for range 3 {
			if got := s.next(2 * time.Second); got != ": ping" {
				t.Fatalf("idle block = %q, want \": ping\"", got)
			}
		}
	})
}

// The event the stream is to carry past the write timeout is written to the
// store and the feed woken, as the server's own write path does, rather than
// through a request to this server: a request is itself cut by the 300 ms
// write timeout when it takes longer than that to serve (a slow commit on a
// loaded host), and the client then reads EOF. The stream, not that request,
// is under test.
func TestAStreamOutlivesTheServersWriteTimeout(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		st := openStore(t, dbs.New(t))
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		r := startRig(t, st, newToken(t, st), streamSettings{poll: time.Hour, heartbeat: 100 * time.Millisecond, writeTimeout: 300 * time.Millisecond})
		s := r.openStream("/api/v1/stream?topics=events")
		start := time.Now()
		for time.Since(start) < 1500*time.Millisecond {
			if got := s.next(time.Second); got != ": ping" {
				t.Fatalf("block = %q, want \": ping\"", got)
			}
		}
		if _, err := (initiatives.Store{Store: st}).Create(context.Background(), audit.Actor{Handle: "tester@host"}, "key:x", initiatives.New{Slug: "aa", Title: "Title of aa"}); err != nil {
			t.Fatal(err)
		}
		r.feed.Notify()
		if msg := s.nextMessage(); !strings.HasPrefix(msg, "event: board") {
			t.Errorf("message after the write timeout = %q", msg)
		}
	})
}

// stableGoroutines returns the goroutine count once two reads 50 ms apart agree.
func stableGoroutines() int {
	n := runtime.NumGoroutine()
	for range 100 {
		time.Sleep(50 * time.Millisecond)
		m := runtime.NumGoroutine()
		if m == n {
			return n
		}
		n = m
	}
	return n
}

// settledGoroutines polls until the goroutine count is at most want, and returns
// the last count read.
func settledGoroutines(want int) int {
	n := runtime.NumGoroutine()
	for deadline := time.Now().Add(5 * time.Second); n > want && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}

// It counts the goroutines of the whole process, so it is the one test of the
// package that is not parallel: it runs alone, before the parallel ones start.
func TestNoGoroutineOutlivesADisconnectedClientOrTheServer(t *testing.T) {
	forEachLegInOrder(t, func(t *testing.T, _ string, dbs databases) {
		st := openStore(t, dbs.New(t))
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		token := newToken(t, st)
		noKeepAlive.CloseIdleConnections()
		beforeServer := stableGoroutines()
		r := startRig(t, st, token, streamSettings{poll: time.Hour, heartbeat: 50 * time.Millisecond})
		r.createInitiative("warm") // a warm database pool, so only streams are measured below
		idle := stableGoroutines()
		var streams []*sse
		for range 5 {
			streams = append(streams, r.openStream("/api/v1/stream"))
		}
		for _, s := range streams {
			s.next(2 * time.Second)
		}
		if during := runtime.NumGoroutine(); during <= idle {
			t.Fatalf("5 open streams cost no goroutine (%d, idle %d): the measure is blind", during, idle)
		}
		for _, s := range streams {
			s.close()
		}
		if n := settledGoroutines(idle); n > idle {
			t.Errorf("after the clients disconnected %d goroutines run, want at most %d", n, idle)
		}
		open := r.openStream("/api/v1/stream")
		open.next(2 * time.Second)
		if err := r.stop(); err != nil {
			t.Errorf("the server stopped with %v", err)
		}
		if !open.ended(2 * time.Second) {
			t.Error("the open stream was not ended by the shutdown")
		}
		open.close()
		noKeepAlive.CloseIdleConnections()
		if n := settledGoroutines(beforeServer); n > beforeServer {
			t.Errorf("after the server stopped %d goroutines run, want at most %d", n, beforeServer)
		}
	})
}

func TestAnEventWrittenThroughOneServerReachesAStreamOnAnotherOnOnePostgres(t *testing.T) {
	t.Parallel()
	onPostgres(t, func(t *testing.T, dbs *postgresDatabases) {
		url := dbs.New(t)
		a := openStore(t, url)
		if err := a.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		token := newToken(t, a)
		cfg := streamSettings{poll: 100 * time.Millisecond, heartbeat: time.Minute}
		writer := startRig(t, a, token, cfg)
		reader := startRig(t, openStore(t, url), token, cfg)
		s := reader.openStream("/api/v1/stream?topics=events")
		writer.createInitiative("aa")
		msg := s.nextMessage()
		if !strings.HasPrefix(msg, "event: board") || !strings.Contains(msg, `"initiative":"aa"`) {
			t.Errorf("message on the other server = %q", msg)
		}
	})
}

func TestAStreamAnswers503WithoutAFeedAndEndsAtOnceOnAClosedHub(t *testing.T) {
	t.Parallel()
	got, err := streamOps{}.GetStream(context.Background(), GetStreamRequestObject{})
	if p, ok := got.(problemResponse); err != nil || !ok || p.status != http.StatusServiceUnavailable {
		t.Errorf("stream without a feed = %#v, %v, want a 503 problem", got, err)
	}
	hub := &streamHub{feed: feed.NewAt(nil, 0, feed.Options{}), heartbeat: time.Hour, stop: make(chan struct{})}
	hub.Close()
	hub.Close()
	rec := httptest.NewRecorder()
	resp, err := streamOps{hub: hub}.GetStream(context.Background(), GetStreamRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.VisitGetStreamResponse(rec); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("a stream on a closed hub = %d %v, want 200 text/event-stream that ends", rec.Code, rec.Header())
	}
}

func TestAStreamEndsWhenTheClientCanNoLongerBeWrittenTo(t *testing.T) {
	t.Parallel()
	f := feed.NewAt(nil, 0, feed.Options{})
	hub := &streamHub{feed: f, heartbeat: 5 * time.Millisecond, stop: make(chan struct{})}
	for name, w := range map[string]http.ResponseWriter{"flush fails": &brokenWriter{failFlush: true}, "write fails": &brokenWriter{failWrite: true}} {
		resp, _ := streamOps{hub: hub}.GetStream(context.Background(), GetStreamRequestObject{})
		done := make(chan error, 1)
		go func() { done <- resp.VisitGetStreamResponse(w) }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s: the stream returned %v, want a clean end", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s: the stream did not end", name)
		}
	}
}

// brokenWriter is a response writer whose client has gone away.
type brokenWriter struct {
	header               http.Header
	failFlush, failWrite bool
}

func (b *brokenWriter) Header() http.Header {
	if b.header == nil {
		b.header = http.Header{}
	}
	return b.header
}
func (b *brokenWriter) WriteHeader(int) {}
func (b *brokenWriter) Write(p []byte) (int, error) {
	if b.failWrite {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}
func (b *brokenWriter) FlushError() error {
	if b.failFlush {
		return io.ErrClosedPipe
	}
	return nil
}

func TestParseHeartbeatReadsADurationAboveZero(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]time.Duration{"": DefaultHeartbeat, " ": DefaultHeartbeat, "25s": 25 * time.Second, "150ms": 150 * time.Millisecond} {
		if got, err := ParseHeartbeat(in); err != nil || got != want {
			t.Errorf("ParseHeartbeat(%q) = %v, %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "0s", "-1s", "soon", "25"} {
		if _, err := ParseHeartbeat(in); err == nil || !strings.Contains(err.Error(), "VH_STREAM_HEARTBEAT") {
			t.Errorf("ParseHeartbeat(%q) error = %v, want one naming VH_STREAM_HEARTBEAT", in, err)
		}
	}
}

func TestStreamTopicEnumAcceptsOnlyTheDeclaredValues(t *testing.T) {
	t.Parallel()
	if !GetStreamParamsTopicsEvents.Valid() || !GetStreamParamsTopicsAttention.Valid() || GetStreamParamsTopics("x").Valid() {
		t.Error("the topics enum does not accept exactly events and attention")
	}
}
