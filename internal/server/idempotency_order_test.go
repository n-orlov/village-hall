package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/n-orlov/village-hall/internal/idempotency"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
)

// orderProbe is the generated operations with POST /notes replaced by one that
// writes a row inside the request's unit of work and returns a note.
type orderProbe struct {
	operations
	st *store.Store
}

func (p orderProbe) PostNote(ctx context.Context, _ PostNoteRequestObject) (PostNoteResponseObject, error) {
	if _, err := p.st.WriterFor(ctx).ExecContext(ctx, `INSERT INTO idem_probe (v) VALUES ('x')`); err != nil {
		return nil, err
	}
	return PostNote201JSONResponse{Id: "01PROBE", Author: "claude@host", Summary: "made"}, nil
}

// touchedWriter calls touched once, on the first use of the response writer: the
// generated code encodes the whole answer into a buffer before it sets a header,
// so by then the answer is encoded.
type touchedWriter struct {
	http.ResponseWriter
	once    sync.Once
	touched func()
}

func (w *touchedWriter) Header() http.Header {
	w.once.Do(w.touched)
	return w.ResponseWriter.Header()
}

func (w *touchedWriter) WriteHeader(status int) {
	w.once.Do(w.touched)
	w.ResponseWriter.WriteHeader(status)
}

func (w *touchedWriter) Write(b []byte) (int, error) {
	w.once.Do(w.touched)
	return w.ResponseWriter.Write(b)
}

// TestAnIdempotentAnswerIsEncodedBeforeTheStatementThatSealsItsKey drives a keyed
// POST through the generated operations and pins where the answer is encoded in
// the log of writer statements: the request's write, then the encoding of the
// answer, then the one statement that seals the key with the encoded answer, and
// only then the commit of the unit, so the write and its answer stand together.
func TestAnIdempotentAnswerIsEncodedBeforeTheStatementThatSealsItsKey(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	if _, err := st.Writer().ExecContext(context.Background(), `CREATE TABLE idem_probe (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	encodedAt := -1
	mux := http.NewServeMux()
	HandlerWithOptions(strictOperations(orderProbe{st: st}), StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: parameterUnreadable})
	h := idempotent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(&touchedWriter{ResponseWriter: w, touched: func() { encodedAt = len(rec.All()) }}, r)
	}), &idempotency.Store{Store: st})

	rec.Reset()
	out := httptest.NewRecorder()
	h.ServeHTTP(out, directRequest(strings.NewReader(`{"summary":"made"}`), "order"))
	if out.Code != http.StatusCreated || !strings.Contains(out.Body.String(), `"id":"01PROBE"`) {
		t.Fatalf("answer = %d %s", out.Code, out.Body)
	}

	log := rec.All()
	wrote, commit, seal := -1, -1, -1
	for i, s := range log {
		sql := strings.Join(strings.Fields(s.SQL), " ")
		switch {
		case s.Kind == storetest.Exec && strings.HasPrefix(sql, "INSERT INTO idem_probe"):
			wrote = i
		case s.Kind == storetest.Commit && wrote >= 0 && commit < 0:
			commit = i
		case s.Kind == storetest.Exec && strings.HasPrefix(sql, "UPDATE idempotency_keys SET state = 'done', status = ?, content_type = ?, body = ?"):
			seal = i
		}
	}
	if wrote < 0 || commit < 0 || seal < 0 || encodedAt < 0 {
		t.Fatalf("write #%d, commit #%d, seal #%d, encoded at #%d in the writer log: %v", wrote, commit, seal, encodedAt, log)
	}
	if encodedAt <= wrote {
		t.Errorf("the answer was encoded after #%d statements, before the request's write #%d: it cannot describe it", encodedAt, wrote)
	}
	if seal < encodedAt {
		t.Errorf("the seal statement is #%d, but the answer was encoded only after #%d statements: encoding must come first", seal, encodedAt)
	}
	if commit <= seal || !log[seal].InTx {
		t.Errorf("the seal statement #%d (in a transaction: %v) and the commit #%d: the answer must be stored in the unit that holds the write", seal, log[seal].InTx, commit)
	}

	// The sealed body is the whole answer: the replay returns it unchanged and the
	// write happened once.
	again := httptest.NewRecorder()
	h.ServeHTTP(again, directRequest(strings.NewReader(`{"summary":"made"}`), "order"))
	if again.Code != out.Code || again.Body.String() != out.Body.String() || again.Header().Get(ReplayedHeader) != "true" {
		t.Errorf("replay = %d %q replayed=%q, want the stored answer", again.Code, again.Body, again.Header().Get(ReplayedHeader))
	}
	if rows := probeRows(t, st); rows != 1 {
		t.Errorf("probe rows = %d, want the one write", rows)
	}
}

// probeRows is the number of rows the probe handlers committed.
func probeRows(t *testing.T, st *store.Store) int {
	t.Helper()
	var rows int
	if err := st.Reader().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM idem_probe`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// failOnlyTheAnswer makes the database refuse the update that stores an encoded
// answer (the only one that sets a status), and nothing else: the claim of the
// key and every write of the request go through.
func failOnlyTheAnswer(t *testing.T, st *store.Store) (lift func()) {
	t.Helper()
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS idem_probe (v TEXT)`,
		`CREATE TRIGGER seal_fail BEFORE UPDATE ON idempotency_keys WHEN NEW.status IS NOT NULL BEGIN SELECT RAISE(ABORT, 'injected seal failure'); END`,
	} {
		if _, err := st.Writer().ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		if _, err := st.Writer().ExecContext(context.Background(), `DROP TRIGGER seal_fail`); err != nil {
			t.Fatal(err)
		}
	}
}

// writingHandler is a handler that writes writes rows into the request's unit
// of work and answers status with a JSON body; calls counts its runs.
func writingHandler(t *testing.T, st *store.Store, writes, status int, calls *int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		for i := 0; i < writes; i++ {
			if _, err := st.WriterFor(r.Context()).ExecContext(r.Context(), `INSERT INTO idem_probe (v) VALUES ('x')`); err != nil {
				t.Error(err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"made":"it"}`))
	})
}

// TestAKeyedWriteWhoseEncodedAnswerCannotBeStoredCommitsNothingAndItsRetryRunsOnceThenReplays
// fails only the statement that stores the encoded answer, for successes of
// different statuses and requests of one and of two writes: nothing the request
// wrote stays, the key is released, the answer is 503 with a next, the same
// request then runs once and every send after it is its replay, and a different
// request under the key is 422.
func TestAKeyedWriteWhoseEncodedAnswerCannotBeStoredCommitsNothingAndItsRetryRunsOnceThenReplays(t *testing.T) {
	for _, c := range []struct {
		name           string
		writes, status int
	}{
		{"one write answered 201", 1, http.StatusCreated},
		{"two writes answered 201", 2, http.StatusCreated},
		{"one write answered 200", 1, http.StatusOK},
		{"two writes answered 202", 2, http.StatusAccepted},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, _ := storetest.OpenMigrated(t)
			lift := failOnlyTheAnswer(t, st)
			calls := 0
			h := idempotent(writingHandler(t, st, c.writes, c.status, &calls), &idempotency.Store{Store: st})
			send := func(body string) *httptest.ResponseRecorder {
				out := httptest.NewRecorder()
				h.ServeHTTP(out, directRequest(strings.NewReader(body), "unstored"))
				return out
			}

			first := send("{}")
			var problem struct{ Next string }
			if err := json.Unmarshal(first.Body.Bytes(), &problem); err != nil || first.Code != http.StatusServiceUnavailable || problem.Next == "" || first.Header().Get(ReplayedHeader) != "" {
				t.Fatalf("send whose answer cannot be stored = %d %s (%v), want a 503 with a next", first.Code, first.Body, err)
			}
			var keys int
			if err := st.Reader().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM idempotency_keys`).Scan(&keys); err != nil || keys != 0 {
				t.Errorf("%d key rows (%v), want the key released", keys, err)
			}
			if rows := probeRows(t, st); rows != 0 {
				t.Fatalf("%d rows committed by a request whose answer was not stored, want none", rows)
			}

			lift()
			run, replay := send("{}"), send("{}")
			if run.Code != c.status || run.Header().Get(ReplayedHeader) != "" || run.Body.String() != `{"made":"it"}` {
				t.Errorf("retry = %d %s (%q), want the request run: %d", run.Code, run.Body, run.Header().Get(ReplayedHeader), c.status)
			}
			if replay.Code != c.status || replay.Header().Get(ReplayedHeader) != "true" || replay.Body.String() != run.Body.String() {
				t.Errorf("second retry = %d %s (%q), want the stored replay", replay.Code, replay.Body, replay.Header().Get(ReplayedHeader))
			}
			if other := send(`{"other":1}`); other.Code != http.StatusUnprocessableEntity {
				t.Errorf("a different request under the key = %d %s, want 422", other.Code, other.Body)
			}
			if rows := probeRows(t, st); rows != c.writes || calls != 2 {
				t.Errorf("%d rows and %d runs after the retry, want %d rows and 2 runs (the failed one and the retry)", rows, calls, c.writes)
			}
		})
	}
}

// TestAKeyedRefusalWhoseAnswerCannotBeStoredIsStillAnswered shows the rule at
// its other edge: a refusal wrote nothing, so an answer that cannot be stored
// releases the key and the refusal itself is still what the client reads.
func TestAKeyedRefusalWhoseAnswerCannotBeStoredIsStillAnswered(t *testing.T) {
	st, _ := storetest.OpenMigrated(t)
	failOnlyTheAnswer(t, st)
	calls := 0
	h := idempotent(writingHandler(t, st, 1, http.StatusConflict, &calls), &idempotency.Store{Store: st})
	out := httptest.NewRecorder()
	h.ServeHTTP(out, directRequest(strings.NewReader("{}"), "refused"))
	if out.Code != http.StatusConflict || out.Body.String() != `{"made":"it"}` {
		t.Errorf("refusal = %d %s, want it answered as it was", out.Code, out.Body)
	}
	if rows := probeRows(t, st); rows != 0 {
		t.Errorf("%d rows committed with a refusal whose answer was not stored, want none", rows)
	}
}
