package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/notify"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
)

// hook is an httptest receiver that records each POST body and answers with the
// next status of its script (the last repeats).
type hook struct {
	*httptest.Server
	mu     sync.Mutex
	status []int
	bodies []string
	header http.Header
}

func newHook(t *testing.T, status ...int) *hook {
	t.Helper()
	h := &hook{status: status}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		n := len(h.bodies)
		h.bodies, h.header = append(h.bodies, string(b)), r.Header.Clone()
		code := h.status[min(n, len(h.status)-1)]
		h.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *hook) got() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.bodies...)
}

// worker delivers st's outbox to url on the world's clock.
func (w *outboxWorld) worker(st *store.Store, url string) *notify.Worker {
	return &notify.Worker{Store: st, URL: url, Timeout: 5 * time.Second, Now: func() time.Time { return w.now }}
}

func (w *outboxWorld) states() map[string]int {
	w.t.Helper()
	rs, err := w.st.Reader().QueryContext(context.Background(), `SELECT state, COUNT(*) FROM notifications GROUP BY state`)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = rs.Close() }()
	out := map[string]int{}
	for rs.Next() {
		var s string
		var n int
		if err := rs.Scan(&s, &n); err != nil {
			w.t.Fatal(err)
		}
		out[s] = n
	}
	return out
}

func (w *outboxWorld) tickWorker(wk *notify.Worker) {
	w.t.Helper()
	if err := wk.Tick(context.Background()); err != nil {
		w.t.Fatal(err)
	}
}

func decodeBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	return m
}

func TestEachTriggerDeliversOnePOSTWithTheDocumentedFields(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		ctx := context.Background()
		w := newOutboxWorld(t, dbs, allKinds())
		st := w.st
		st.SetPublicURL("https://board.example")
		h := newHook(t, 200)
		wk := w.worker(st, h.URL)

		w.lease(outboxAgent, "aa-1", 60)
		w.tick(2 * time.Minute)
		if _, err := w.leases.ExpireDue(ctx); err != nil {
			t.Fatal(err)
		}
		w.tickWorker(wk)
		w.ask(asks.New{Task: "aa-2", Question: "which parser?"})
		w.tickWorker(wk)
		w.tick(time.Second)
		if _, err := w.tasks.Done(ctx, outboxAgent, "key:x", "aa-3", tasks.DoneOptions{}); err != nil {
			t.Fatal(err)
		}
		w.tickWorker(wk)
		w.tickWorker(wk)

		got := h.got()
		if len(got) != 3 {
			t.Fatalf("%d POSTs, want one per trigger: %q", len(got), got)
		}
		for i, kind := range []string{"stranded", "asking", "done"} {
			m := decodeBody(t, got[i])
			if m["kind"] != kind || m["text"] == "" || !strings.HasPrefix(fmt.Sprint(m["text"]), kind) || m["board_url"] != "https://board.example/" {
				t.Fatalf("%s POST = %v", kind, m)
			}
			if a, _ := m["actor"].(map[string]any); a["handle"] != "claude@host" {
				t.Fatalf("%s actor = %v", kind, m["actor"])
			}
			if tk, _ := m["task"].(map[string]any); tk["ref"] != fmt.Sprintf("aa-%d", []int{1, 2, 3}[i]) || tk["url"] != "https://board.example/#/t/"+fmt.Sprint(tk["ref"]) {
				t.Fatalf("%s task = %v", kind, m["task"])
			}
		}
		if l, _ := decodeBody(t, got[0])["lease"].(map[string]any); l["ref"] == nil || l["intent"] != "work on aa-1" || l["expired_at"] == nil {
			t.Fatalf("stranded lease = %v", l)
		}
		if a, _ := decodeBody(t, got[1])["ask"].(map[string]any); a["question"] != "which parser?" || a["ref"] == nil {
			t.Fatalf("asking ask = %v", a)
		}
		if h.header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q", h.header.Get("Content-Type"))
		}
		if s := w.states(); s["sent"] != 3 || len(s) != 1 {
			t.Fatalf("states = %v", s)
		}
	})
}

func TestA500ThenA200EndsSentOnTheInjectedClock(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		w := newOutboxWorld(t, dbs, allKinds())
		h := newHook(t, 500, 200)
		wk := w.worker(w.st, h.URL)
		w.ask(asks.New{Question: "q"})
		w.tickWorker(wk)
		if s := w.states(); s["pending"] != 1 || len(h.got()) != 1 {
			t.Fatalf("after the 500: states %v, %d POSTs", s, len(h.got()))
		}
		w.tick(29 * time.Second)
		w.tickWorker(wk)
		if len(h.got()) != 1 {
			t.Fatalf("retried after 29 s")
		}
		w.tick(time.Second)
		w.tickWorker(wk)
		if s := w.states(); s["sent"] != 1 || len(h.got()) != 2 {
			t.Fatalf("after the retry: states %v, %d POSTs", s, len(h.got()))
		}
	})
}

func TestSixFailuresEndFailed(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		w := newOutboxWorld(t, dbs, allKinds())
		h := newHook(t, 500)
		wk := w.worker(w.st, h.URL)
		w.ask(asks.New{Question: "q"})
		for i := 0; i < 6; i++ {
			w.tickWorker(wk)
			w.tick(time.Hour + time.Second)
		}
		w.tickWorker(wk)
		var attempts int
		var lastErr string
		if err := w.st.Reader().QueryRowContext(context.Background(), `SELECT attempts, last_error FROM notifications`).Scan(&attempts, &lastErr); err != nil {
			t.Fatal(err)
		}
		if s := w.states(); s["failed"] != 1 || len(s) != 1 || len(h.got()) != 6 || attempts != 6 || lastErr != "http 500" {
			t.Fatalf("states %v, %d POSTs, %d attempts, last_error %q", s, len(h.got()), attempts, lastErr)
		}
	})
}

// hangs are the ways a receiver outlasts BOARD_NOTIFY_TIMEOUT before a status
// line: it never answers. A 2xx status line ends the wait, whatever its body does.
var hangs = map[string]func(w http.ResponseWriter, r *http.Request, release <-chan struct{}){
	"before the status": func(_ http.ResponseWriter, r *http.Request, release <-chan struct{}) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	},
}

func TestAHungReceiverTimesOutUnderBoardNotifyTimeoutAndIsRetried(t *testing.T) {
	t.Parallel()
	for name, hang := range hangs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			hungReceiverIsRetried(t, hang)
		})
	}
}

func hungReceiverIsRetried(t *testing.T, hang func(http.ResponseWriter, *http.Request, <-chan struct{})) {
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		w := newOutboxWorld(t, dbs, allKinds())
		release := make(chan struct{})
		hung := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { hang(rw, r, release) }))
		defer hung.Close()
		defer close(release)
		timeout, err := notify.ParseTimeout("200ms")
		if err != nil {
			t.Fatal(err)
		}
		wk := w.worker(w.st, hung.URL)
		wk.Timeout = timeout
		w.ask(asks.New{Question: "q"})
		start := time.Now()
		w.tickWorker(wk)
		if time.Since(start) > 3*time.Second {
			t.Fatalf("the tick took %s under a 200ms timeout", time.Since(start))
		}
		var lastErr string
		var attempts int
		if err := w.st.Reader().QueryRowContext(context.Background(), `SELECT attempts, last_error FROM notifications WHERE state = 'pending'`).Scan(&attempts, &lastErr); err != nil || lastErr != "timeout" || attempts != 1 {
			t.Fatalf("row: attempts %d last_error %q, %v", attempts, lastErr, err)
		}
		good := newHook(t, 200)
		wk.URL = good.URL
		w.tick(30 * time.Second)
		w.tickWorker(wk)
		if s := w.states(); s["sent"] != 1 || len(good.got()) != 1 {
			t.Fatalf("the retry: states %v, %d POSTs", s, len(good.got()))
		}
	})
}

// seedAsks raises n asks, one outbox row each.
func (w *outboxWorld) seedAsks(n int) {
	for i := 0; i < n; i++ {
		w.ask(asks.New{Question: fmt.Sprintf("question-%02d", i)})
	}
}

func TestTwoServersDeliverASeededBatchExactlyOnce(t *testing.T) {
	t.Parallel()
	onPostgres(t, func(t *testing.T, dbs *postgresDatabases) {
		w := newOutboxWorld(t, dbs, allKinds())
		const rows = 30
		w.seedAsks(rows)
		st2 := openStore(t, w.url)
		h := newHook(t, 200)
		a, b := w.worker(w.st, h.URL), w.worker(st2, h.URL)
		var wg sync.WaitGroup
		for _, wk := range []*notify.Worker{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 3; i++ {
					if err := wk.Tick(context.Background()); err != nil {
						t.Error(err)
					}
				}
			}()
		}
		wg.Wait()
		seen := map[string]int{}
		for _, body := range h.got() {
			seen[fmt.Sprint(decodeBody(t, body)["ask"].(map[string]any)["question"])]++
		}
		if len(seen) != rows {
			t.Fatalf("%d distinct rows delivered, want %d", len(seen), rows)
		}
		for q, n := range seen {
			if n != 1 {
				t.Errorf("%s delivered %d times", q, n)
			}
		}
		if s := w.states(); s["sent"] != rows || len(s) != 1 {
			t.Fatalf("states = %v", s)
		}
	})
}

func TestARowClaimedByAStoppedServerIsDeliveredByTheOtherAfterItsClaimLapses(t *testing.T) {
	t.Parallel()
	onPostgres(t, func(t *testing.T, dbs *postgresDatabases) {
		w := newOutboxWorld(t, dbs, allKinds())
		w.ask(asks.New{Question: "stuck"})
		st2 := openStore(t, w.url)

		// Server one claims the row and is stopped mid-POST.
		stopped, cancel := context.WithCancel(context.Background())
		hung := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			cancel()
			<-r.Context().Done()
		}))
		defer hung.Close()
		one := w.worker(w.st, hung.URL)
		if err := one.Tick(stopped); err != nil {
			t.Fatal(err)
		}
		if s := w.states(); s["pending"] != 1 {
			t.Fatalf("states after the stop = %v", s)
		}

		h := newHook(t, 200)
		two := w.worker(st2, h.URL)
		w.tickWorker(two)
		w.tick(5*time.Second + 30*time.Second - time.Millisecond)
		w.tickWorker(two)
		if len(h.got()) != 0 {
			t.Fatalf("the other server delivered a row still claimed")
		}
		w.tick(time.Millisecond)
		w.tickWorker(two)
		if s := w.states(); s["sent"] != 1 || len(h.got()) != 1 {
			t.Fatalf("after the claim lapsed: states %v, %d POSTs", s, len(h.got()))
		}
	})
}

// A 2xx status line marks the row sent whatever the body does after it: it
// stalls past BOARD_NOTIFY_TIMEOUT, breaks off, or the answer has none.
func TestA2xxIsSentWhateverItsBody(t *testing.T) {
	t.Parallel()
	bodies := map[string]func(w http.ResponseWriter, r *http.Request, release <-chan struct{}){
		"a stalled body": func(w http.ResponseWriter, r *http.Request, release <-chan struct{}) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":`))
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
		},
		"a truncated body": func(w http.ResponseWriter, _ *http.Request, _ <-chan struct{}) {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"ok":`))
		},
		"no body": func(w http.ResponseWriter, _ *http.Request, _ <-chan struct{}) {
			w.WriteHeader(http.StatusNoContent)
		},
		"a body that reports a failure": func(w http.ResponseWriter, _ *http.Request, _ <-chan struct{}) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":false,"error":"rejected"}`))
		},
		"an undecodable content encoding": func(w http.ResponseWriter, _ *http.Request, _ <-chan struct{}) {
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("not gzip at all"))
		},
		"the last 2xx status": func(w http.ResponseWriter, _ *http.Request, _ <-chan struct{}) {
			w.WriteHeader(299)
		},
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
				w := newOutboxWorld(t, dbs, allKinds())
				release := make(chan struct{})
				srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					body(rw, r, release)
				}))
				defer srv.Close()
				defer close(release)
				timeout, err := notify.ParseTimeout("200ms")
				if err != nil {
					t.Fatal(err)
				}
				wk := w.worker(w.st, srv.URL)
				wk.Timeout = timeout
				w.ask(asks.New{Question: "q"})
				w.tickWorker(wk)
				var attempts int
				var lastErr *string
				if err := w.st.Reader().QueryRowContext(context.Background(), `SELECT attempts, last_error FROM notifications`).Scan(&attempts, &lastErr); err != nil {
					t.Fatal(err)
				}
				if s := w.states(); s["sent"] != 1 || len(s) != 1 || attempts != 1 || lastErr != nil {
					t.Fatalf("states %v, %d attempts, last_error %v", s, attempts, lastErr)
				}
			})
		})
	}
}

// Only a 2xx status sends: a non-2xx answer stays pending and is retried
// whatever its body says, so a body claiming success cannot send a row.
func TestANon2xxIsRetriedWhateverItsBody(t *testing.T) {
	t.Parallel()
	for name, code := range map[string]int{"a 500": http.StatusInternalServerError, "a 404": http.StatusNotFound, "a 429": http.StatusTooManyRequests} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
				w := newOutboxWorld(t, dbs, allKinds())
				srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					rw.WriteHeader(code)
					_, _ = rw.Write([]byte(`{"ok":true}`))
				}))
				defer srv.Close()
				w.ask(asks.New{Question: "q"})
				w.tickWorker(w.worker(w.st, srv.URL))
				var attempts int
				var lastErr *string
				if err := w.st.Reader().QueryRowContext(context.Background(), `SELECT attempts, last_error FROM notifications`).Scan(&attempts, &lastErr); err != nil {
					t.Fatal(err)
				}
				if s := w.states(); s["pending"] != 1 || len(s) != 1 || attempts != 1 || lastErr == nil {
					t.Fatalf("states %v, %d attempts, last_error %v", s, attempts, lastErr)
				}
			})
		})
	}
}
