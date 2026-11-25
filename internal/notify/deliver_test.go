package notify_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/notify"
	"github.com/n-orlov/village-hall/internal/store"
)

// receiver is an httptest server that answers with the next status of its script
// (the last one repeats) and records what it was sent.
type receiver struct {
	*httptest.Server
	mu      sync.Mutex
	script  []int
	bodies  []string
	headers []http.Header
}

func newReceiver(t *testing.T, script ...int) *receiver {
	t.Helper()
	r := &receiver{script: script}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		n := len(r.bodies)
		r.bodies = append(r.bodies, string(b))
		r.headers = append(r.headers, req.Header.Clone())
		status := r.script[min(n, len(r.script)-1)]
		r.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte("secret receiver body"))
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) posts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

// outbox is a migrated database with one pending row per call of add, and a worker reading a clock the test moves.
type outbox struct {
	env   env
	now   time.Time
	w     *notify.Worker
	n     int
	event []string
}

func newOutbox(t *testing.T, url string) *outbox {
	t.Helper()
	e := newEnv(t, nil)
	o := &outbox{env: e, now: t0.Add(time.Hour)}
	o.w = &notify.Worker{Store: e.st, URL: url, Timeout: 5 * time.Second, Now: func() time.Time { return o.now }}
	for i := 0; i < 6; i++ {
		o.event = append(o.event, e.event(t, "task_created"))
	}
	return o
}

// add inserts a pending row of the kind, due at the clock's now, and returns its id.
func (o *outbox) add(t *testing.T, kind, payload string) string {
	t.Helper()
	o.n++
	id := fmt.Sprintf("N%02d", o.n)
	at := store.EncodeTime(o.now)
	_, err := o.env.st.Writer().ExecContext(ctx, o.env.st.Dialect().Rebind(`INSERT INTO notifications (id, kind, event_id, payload, next_attempt_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`),
		id, kind, o.event[(o.n-1)%6], payload, at, at)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type rowState struct {
	state, lastErr string
	attempts       int
	next           time.Time
	claimed, sent  sql.NullInt64
	hasErr         bool
}

func (o *outbox) row(t *testing.T, id string) rowState {
	t.Helper()
	var r rowState
	var next int64
	var le sql.NullString
	err := o.env.st.Reader().QueryRowContext(ctx, o.env.st.Dialect().Rebind(`SELECT state, attempts, next_attempt_at, claimed_until, last_error, sent_at FROM notifications WHERE id = ?`), id).
		Scan(&r.state, &r.attempts, &next, &r.claimed, &le, &r.sent)
	if err != nil {
		t.Fatal(err)
	}
	r.next, r.lastErr, r.hasErr = store.DecodeTime(next), le.String, le.Valid
	return r
}

func (o *outbox) tick(t *testing.T) {
	t.Helper()
	if err := o.w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestADueRowIsPostedAsJSONAndMarkedSent(t *testing.T) {
	rc := newReceiver(t, 204)
	o := newOutbox(t, rc.URL)
	o.w.Header = notify.Header{Name: "X-Receiver-Token", Value: "s3cret"}
	id := o.add(t, "asking", `{"kind":"asking"}`)
	o.tick(t)
	if rc.posts() != 1 || rc.bodies[0] != `{"kind":"asking"}` {
		t.Fatalf("posts = %q", rc.bodies)
	}
	h := rc.headers[0]
	if h.Get("Content-Type") != "application/json" || h.Get("X-Receiver-Token") != "s3cret" {
		t.Fatalf("headers = %v", h)
	}
	r := o.row(t, id)
	if r.state != "sent" || r.attempts != 1 || !r.sent.Valid || r.sent.Int64 != store.EncodeTime(o.now) || r.claimed.Valid || r.hasErr {
		t.Fatalf("row = %+v", r)
	}
	o.tick(t)
	if rc.posts() != 1 {
		t.Fatalf("a sent row was posted again: %d posts", rc.posts())
	}
}

func TestNoHeaderIsSentWhenNoneIsSet(t *testing.T) {
	rc := newReceiver(t, 200)
	o := newOutbox(t, rc.URL)
	o.add(t, "done", `{}`)
	o.tick(t)
	if len(rc.headers[0]) != 4 { // Accept-Encoding, Content-Length, Content-Type, User-Agent
		t.Fatalf("headers = %v", rc.headers[0])
	}
}

func TestAFailedRowRetriesOnTheBackoffSchedule(t *testing.T) {
	rc := newReceiver(t, 500, 500, 500, 500, 500, 500, 200)
	o := newOutbox(t, rc.URL)
	id := o.add(t, "done", `{}`)
	wait := []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour}
	for i, d := range wait {
		o.tick(t)
		r := o.row(t, id)
		if r.state != "pending" || r.attempts != i+1 || !r.next.Equal(o.now.Add(d)) || r.lastErr != "http 500" || r.claimed.Valid || r.sent.Valid {
			t.Fatalf("after failure %d: %+v, want next %s", i+1, r, o.now.Add(d))
		}
		before := rc.posts()
		o.now = r.next.Add(-time.Millisecond)
		o.tick(t)
		if rc.posts() != before {
			t.Fatalf("failure %d: posted before the backoff of %s lapsed", i+1, d)
		}
		o.now = r.next
	}
	o.tick(t)
	r := o.row(t, id)
	if r.state != "failed" || r.attempts != 6 || r.lastErr != "http 500" || rc.posts() != 6 {
		t.Fatalf("after six failures: %+v, %d posts", r, rc.posts())
	}
	o.now = o.now.Add(24 * time.Hour)
	o.tick(t)
	if rc.posts() != 6 {
		t.Fatalf("a failed row was posted again")
	}
}

func TestA500ThenA200EndsSentAndKeepsTheLastError(t *testing.T) {
	rc := newReceiver(t, 500, 200)
	o := newOutbox(t, rc.URL)
	id := o.add(t, "done", `{}`)
	o.tick(t)
	o.now = o.now.Add(30 * time.Second)
	o.tick(t)
	r := o.row(t, id)
	if r.state != "sent" || r.attempts != 2 || r.lastErr != "http 500" || !r.sent.Valid || rc.posts() != 2 {
		t.Fatalf("row = %+v, %d posts", r, rc.posts())
	}
}

func TestTheLastErrorNeverCarriesTheBodyTheURLOrTheHeader(t *testing.T) {
	rc := newReceiver(t, 503)
	o := newOutbox(t, rc.URL)
	o.w.Header = notify.Header{Name: "X-Receiver-Token", Value: "s3cret"}
	id := o.add(t, "done", `{}`)
	o.tick(t)
	if r := o.row(t, id); r.lastErr != "http 503" {
		t.Fatalf("last_error = %q", r.lastErr)
	}
	rc.Close()
	o.now = o.now.Add(time.Minute)
	o.tick(t)
	e := o.row(t, id).lastErr
	if e != "network error" || strings.Contains(e, "127.0.0.1") || strings.Contains(e, "s3cret") {
		t.Fatalf("last_error = %q", e)
	}
}

func TestAHungReceiverTimesOutAndIsRetried(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(hung.Close)
	t.Cleanup(func() { close(release) })
	o := newOutbox(t, hung.URL)
	o.w.Timeout = 200 * time.Millisecond
	id := o.add(t, "done", `{}`)
	start := time.Now()
	o.tick(t)
	if time.Since(start) > 3*time.Second {
		t.Fatalf("the hung receiver held the tick for %s", time.Since(start))
	}
	r := o.row(t, id)
	if r.state != "pending" || r.lastErr != "timeout" || r.attempts != 1 || !r.next.Equal(o.now.Add(30*time.Second)) {
		t.Fatalf("row = %+v", r)
	}
}

// stalledBody answers 200 with its headers flushed, then holds the body until
// the client gives up or release closes.
func stalledBody(release <-chan struct{}) http.HandlerFunc {
	return stalledBodyWith(http.StatusOK, release)
}

// stalledBodyWith is stalledBody under another status.
func stalledBodyWith(status int, release <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":`))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
}

// A received 2xx is a success whatever its body does: the PRD marks a row sent
// on any 2xx, so a body that stalls, breaks off or never ends cannot fail it.
func TestA200WhoseBodyStallsPastTheTimeoutIsSentAtOnce(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(stalledBody(release))
	t.Cleanup(hung.Close)
	t.Cleanup(func() { close(release) })
	o := newOutbox(t, hung.URL)
	o.w.Timeout = 2 * time.Second
	id := o.add(t, "done", `{}`)
	start := time.Now()
	o.tick(t)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the stalled body held the tick for %s, the status line had already said 2xx", took)
	}
	r := o.row(t, id)
	if r.state != "sent" || r.lastErr != "" || r.attempts != 1 || !r.sent.Valid || r.claimed.Valid {
		t.Fatalf("row = %+v", r)
	}
	o.tick(t)
	if r := o.row(t, id); r.attempts != 1 {
		t.Fatalf("a sent row was posted again: %+v", r)
	}
}

func TestA200WhoseBodyIsCutShortIsSent(t *testing.T) {
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":`))
	}))
	t.Cleanup(cut.Close)
	o := newOutbox(t, cut.URL)
	id := o.add(t, "done", `{}`)
	o.tick(t)
	r := o.row(t, id)
	if r.state != "sent" || r.lastErr != "" || r.attempts != 1 || !r.sent.Valid {
		t.Fatalf("row = %+v", r)
	}
}

func TestA200WithALongBodyIsSent(t *testing.T) {
	long := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(strings.Repeat("x", 1<<20)))
	}))
	t.Cleanup(long.Close)
	o := newOutbox(t, long.URL)
	id := o.add(t, "done", `{}`)
	o.tick(t)
	if r := o.row(t, id); r.state != "sent" || r.lastErr != "" || r.attempts != 1 {
		t.Fatalf("row = %+v", r)
	}
}

// Every 2xx status the receiver may answer is a success, with a stalled body
// behind the status line or none at all.
func TestEveryReceived2xxStatusIsSentWhateverItsBody(t *testing.T) {
	for _, status := range []int{200, 201, 202, 203, 204, 206, 207, 226, 299} {
		for name, serve := range map[string]func(w http.ResponseWriter, release <-chan struct{}, r *http.Request){
			"no body": func(_ http.ResponseWriter, _ <-chan struct{}, _ *http.Request) {},
			"stalled body": func(w http.ResponseWriter, release <-chan struct{}, r *http.Request) {
				_, _ = w.Write([]byte("partial"))
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
				}
			},
		} {
			t.Run(fmt.Sprintf("%d %s", status, name), func(t *testing.T) {
				release := make(chan struct{})
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.WriteHeader(status)
					serve(w, release, r)
				}))
				t.Cleanup(srv.Close)
				t.Cleanup(func() { close(release) })
				o := newOutbox(t, srv.URL)
				o.w.Timeout = 2 * time.Second
				id := o.add(t, "done", `{}`)
				start := time.Now()
				o.tick(t)
				if took := time.Since(start); took > time.Second {
					t.Fatalf("the tick took %s", took)
				}
				if r := o.row(t, id); r.state != "sent" || r.lastErr != "" || r.attempts != 1 {
					t.Fatalf("row = %+v", r)
				}
			})
		}
	}
}

// The body is never read for a verdict: a 2xx whose body says it failed is
// still a received 2xx, and so is one whose headers a body decoder would choke on.
func TestA200WhoseBodyReportsAFailureIsStillSent(t *testing.T) {
	for name, body := range map[string]string{
		"json error": `{"ok":false,"error":"rejected"}`,
		"plain text": "ERROR: could not process this notification",
		"html page":  "<html><body>500 Internal Server Error</body></html>",
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(srv.Close)
			o := newOutbox(t, srv.URL)
			id := o.add(t, "done", `{}`)
			o.tick(t)
			if r := o.row(t, id); r.state != "sent" || r.lastErr != "" || r.attempts != 1 || !r.sent.Valid {
				t.Fatalf("row = %+v", r)
			}
			o.tick(t)
			if r := o.row(t, id); r.attempts != 1 {
				t.Fatalf("a sent row was posted again: %+v", r)
			}
		})
	}
}

func TestA200WithAnUndecodableContentEncodingIsStillSent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("this is not gzip"))
	}))
	t.Cleanup(srv.Close)
	o := newOutbox(t, srv.URL)
	id := o.add(t, "done", `{}`)
	o.tick(t)
	if r := o.row(t, id); r.state != "sent" || r.lastErr != "" || r.attempts != 1 || !r.sent.Valid {
		t.Fatalf("row = %+v", r)
	}
}

// A non-2xx status is a failure whatever the body does, and is retried on the schedule.
func TestANon2xxWithAStalledBodyFailsWithItsStatusAndIsRetried(t *testing.T) {
	for _, status := range []int{300, 404, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			release := make(chan struct{})
			srv := httptest.NewServer(stalledBodyWith(status, release))
			t.Cleanup(srv.Close)
			t.Cleanup(func() { close(release) })
			o := newOutbox(t, srv.URL)
			o.w.Timeout = 2 * time.Second
			id := o.add(t, "done", `{}`)
			o.tick(t)
			r := o.row(t, id)
			if r.state != "pending" || r.lastErr != fmt.Sprintf("http %d", status) || r.attempts != 1 || r.sent.Valid || !r.next.Equal(o.now.Add(30*time.Second)) {
				t.Fatalf("row = %+v", r)
			}
		})
	}
}

func TestARedirectIsAFailureAndIsNotFollowed(t *testing.T) {
	elsewhere := newReceiver(t, 200)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	o := newOutbox(t, redirect.URL)
	id := o.add(t, "done", `{}`)
	o.tick(t)
	if r := o.row(t, id); r.state != "pending" || r.lastErr != "http 302" || elsewhere.posts() != 0 {
		t.Fatalf("row = %+v, %d posts to the redirect target", r, elsewhere.posts())
	}
}

func TestAClaimedRowIsLeftAloneUntilItsClaimLapses(t *testing.T) {
	rc := newReceiver(t, 200)
	o := newOutbox(t, rc.URL)
	id := o.add(t, "done", `{}`)
	claimed := o.now.Add(time.Minute)
	if _, err := o.env.st.Writer().ExecContext(ctx, o.env.st.Dialect().Rebind(`UPDATE notifications SET claimed_until = ? WHERE id = ?`), store.EncodeTime(claimed), id); err != nil {
		t.Fatal(err)
	}
	o.tick(t)
	o.now = claimed.Add(-time.Millisecond)
	o.tick(t)
	if rc.posts() != 0 {
		t.Fatalf("a claimed row was posted")
	}
	o.now = claimed
	o.tick(t)
	if rc.posts() != 1 || o.row(t, id).state != "sent" {
		t.Fatalf("the lapsed claim was not delivered: %d posts, %+v", rc.posts(), o.row(t, id))
	}
}

func TestADeliveryHoldsItsClaimForTheTimeoutPlusSlack(t *testing.T) {
	o := newOutbox(t, "")
	var claimedUntil sql.NullInt64
	id := o.add(t, "done", `{}`)
	held := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		claimedUntil = o.row(t, id).claimed
	}))
	t.Cleanup(held.Close)
	o.w.URL = held.URL
	o.tick(t)
	if want := store.EncodeTime(o.now.Add(o.w.Timeout + 30*time.Second)); !claimedUntil.Valid || claimedUntil.Int64 != want {
		t.Fatalf("claimed_until during the POST = %v, want %d", claimedUntil, want)
	}
}

func TestOnlyOneOfTwoWorkersClaimsARow(t *testing.T) {
	rc := newReceiver(t, 200)
	o := newOutbox(t, rc.URL)
	id := o.add(t, "done", `{}`)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = o.w.Tick(ctx) }()
	}
	wg.Wait()
	if rc.posts() != 1 || o.row(t, id).state != "sent" {
		t.Fatalf("%d posts", rc.posts())
	}
}

func TestATickLooksOnlyAtPendingRowsInDueOrder(t *testing.T) {
	rc := newReceiver(t, 200)
	o := newOutbox(t, rc.URL)
	for i := 0; i < 3; i++ {
		o.add(t, "done", fmt.Sprintf(`{"n":%d}`, i))
		o.now = o.now.Add(time.Second)
	}
	o.add(t, "done", `{"n":"future"}`)
	if _, err := o.env.st.Writer().ExecContext(ctx, `UPDATE notifications SET next_attempt_at = next_attempt_at + 3600000 WHERE payload LIKE '%future%'`); err != nil {
		t.Fatal(err)
	}
	o.tick(t)
	want := []string{`{"n":0}`, `{"n":1}`, `{"n":2}`}
	if strings.Join(rc.bodies, ",") != strings.Join(want, ",") {
		t.Fatalf("bodies = %q, want %q", rc.bodies, want)
	}
}

func TestATickEndedByShutdownLeavesTheRowToItsClaim(t *testing.T) {
	cctx, cancel := context.WithCancel(ctx)
	block := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		cancel()
		<-r.Context().Done()
	}))
	t.Cleanup(block.Close)
	o := newOutbox(t, block.URL)
	id := o.add(t, "done", `{}`)
	if err := o.w.Tick(cctx); err != nil {
		t.Fatal(err)
	}
	if r := o.row(t, id); r.state != "pending" || r.attempts != 0 || !r.claimed.Valid {
		t.Fatalf("row = %+v: a shutdown must not count as a failed attempt", r)
	}
}

func TestADatabaseErrorEndsTheTick(t *testing.T) {
	rc := newReceiver(t, 200)
	o := newOutbox(t, rc.URL)
	o.add(t, "done", `{}`)
	if _, err := o.env.st.Writer().ExecContext(ctx, `DROP INDEX notifications_state_next_attempt_at`); err != nil {
		t.Fatal(err)
	}
	if _, err := o.env.st.Writer().ExecContext(ctx, `ALTER TABLE notifications RENAME TO gone`); err != nil {
		t.Fatal(err)
	}
	if err := o.w.Tick(ctx); err == nil || !strings.Contains(err.Error(), "read due notifications") {
		t.Fatalf("Tick = %v", err)
	}
}

func TestParseTimeout(t *testing.T) {
	for in, want := range map[string]time.Duration{"": notify.DefaultTimeout, "200ms": 200 * time.Millisecond, "3s": 3 * time.Second} {
		if got, err := notify.ParseTimeout(in); err != nil || got != want {
			t.Errorf("ParseTimeout(%q) = %v, %v, want %v", in, got, err, want)
		}
	}
	if notify.DefaultTimeout != 10*time.Second {
		t.Errorf("DefaultTimeout = %v", notify.DefaultTimeout)
	}
	for _, in := range []string{"0", "0s", "-1s", "soon", "10"} {
		if _, err := notify.ParseTimeout(in); err == nil || !strings.Contains(err.Error(), "VH_NOTIFY_TIMEOUT") {
			t.Errorf("ParseTimeout(%q) = %v, want an error", in, err)
		}
	}
}

func TestParseHeader(t *testing.T) {
	for in, want := range map[string]notify.Header{"": {}, "  ": {}, "X-Token: abc": {Name: "X-Token", Value: "abc"}, "Authorization:Bearer a:b": {Name: "Authorization", Value: "Bearer a:b"}} {
		if got, err := notify.ParseHeader(in); err != nil || got != want {
			t.Errorf("ParseHeader(%q) = %+v, %v, want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"novalue", ": v", "X-Token:", "X Token: v", "X-Token: a\nb", "X-Token: a\x7fb", "Bad(name): v", "Xé: v"} {
		_, err := notify.ParseHeader(in)
		if err == nil || !strings.Contains(err.Error(), "VH_NOTIFY_HEADER") || strings.Contains(err.Error(), "a\n") {
			t.Errorf("ParseHeader(%q) = %v, want an error", in, err)
		}
	}
	if h, err := notify.ParseHeader("X-A: a\tb"); err != nil || h.Value != "a\tb" {
		t.Errorf("a tab inside a value = %+v, %v", h, err)
	}
}

// fakeTicks is the backstop ticker of a Run under test: nothing fires until the test sends, and an
// unbuffered send returns only once Run is back in its select, that is after the pass the last event started.
type fakeTicks struct {
	c chan time.Time
	d chan time.Duration
}

func newFakeTicks() *fakeTicks {
	return &fakeTicks{c: make(chan time.Time), d: make(chan time.Duration, 1)}
}

func (f *fakeTicks) New(d time.Duration) (<-chan time.Time, func()) {
	f.d <- d
	return f.c, func() {}
}

func (f *fakeTicks) fire() { f.c <- time.Time{} }

// postsOn is a receiver that reports each POST on a channel, with the next status of its script.
func postsOn(t *testing.T, script ...int) (string, chan struct{}) {
	t.Helper()
	got := make(chan struct{}, 8)
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		status := script[min(n, len(script)-1)]
		n++
		mu.Unlock()
		w.WriteHeader(status)
		got <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, got
}

func awaitPost(t *testing.T, got chan struct{}, what string) {
	t.Helper()
	select {
	case <-got:
	case <-time.After(30 * time.Second):
		t.Fatalf("no POST: %s", what)
	}
}

func TestTheBackstopTickIsAtLeastTenSeconds(t *testing.T) {
	if notify.TickInterval < 10*time.Second {
		t.Fatalf("TickInterval = %s, want at least 10s", notify.TickInterval)
	}
	e := newEnv(t, nil)
	ft := newFakeTicks()
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	w := &notify.Worker{Store: e.st, URL: "http://127.0.0.1:1", Timeout: time.Second, NewTicker: ft.New}
	go func() { defer close(done); w.Run(rctx, nil) }()
	if d := <-ft.d; d != notify.TickInterval {
		t.Errorf("Run asked its ticker for %s, want TickInterval %s", d, notify.TickInterval)
	}
	cancel()
	<-done
}

func TestAnEnqueueIsDeliveredWithoutTheClockAdvancing(t *testing.T) {
	url, got := postsOn(t, 204)
	e := newEnv(t, []string{store.NotifyDone})
	ft := newFakeTicks()
	w := &notify.Worker{Store: e.st, URL: url, Timeout: 5 * time.Second, Now: func() time.Time { return t0.Add(time.Hour) }, NewTicker: ft.New}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); w.Run(rctx, nil) }()
	t.Cleanup(func() { cancel(); <-done })
	<-ft.d
	// The first pass, at start, finds nothing; the next send returns once it has ended.
	ft.fire()
	id := e.event(t, "task_done")
	tx, err := e.st.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := notify.Enqueue(ctx, tx.Tx, e.st, id, store.NotifyDone, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
		t.Fatal("the row was delivered before its transaction committed")
	default:
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	awaitPost(t, got, "the enqueue did not wake the worker and the backstop never fired")
}

func TestARolledBackEnqueueWakesNothingAndAFailedDeliveryIsRetriedOnTheBackstop(t *testing.T) {
	url, got := postsOn(t, 500, 204)
	e := newEnv(t, []string{store.NotifyDone})
	ft := newFakeTicks()
	now := t0.Add(time.Hour)
	var mu sync.Mutex
	w := &notify.Worker{Store: e.st, URL: url, Timeout: 5 * time.Second, NewTicker: ft.New,
		Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now }}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); w.Run(rctx, nil) }()
	t.Cleanup(func() { cancel(); <-done })
	<-ft.d
	ft.fire()
	id := e.event(t, "task_done")
	enqueue := func(commit bool) {
		tx, err := e.st.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := notify.Enqueue(ctx, tx.Tx, e.st, id, store.NotifyDone, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if !commit {
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	enqueue(false)
	ft.fire() // returns once the pass the rollback might have woken has ended
	select {
	case <-got:
		t.Fatal("a rolled back enqueue was delivered")
	default:
	}
	enqueue(true)
	awaitPost(t, got, "the committed enqueue was not delivered")
	ft.fire() // the failed delivery's pass has ended; the retry is 30 s away
	select {
	case <-got:
		t.Fatal("the failed delivery was retried before its backoff")
	default:
	}
	mu.Lock()
	now = now.Add(notify.Backoff[0])
	mu.Unlock()
	ft.fire()
	awaitPost(t, got, "the backstop tick did not retry the failed delivery")
}
