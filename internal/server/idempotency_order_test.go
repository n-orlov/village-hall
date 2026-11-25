package server

import (
	"context"
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
// the log of writer statements: the request's write and the binding of its key
// commit first, the answer is then encoded with no writer transaction open, and
// only after that does the one statement that seals the key with the answer
// (Complete) run, on its own outside any transaction.
func TestAnIdempotentAnswerIsEncodedBeforeTheStatementThatSealsItsKey(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	if _, err := st.Writer().ExecContext(context.Background(), `CREATE TABLE idem_probe (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	encodeFrom, encodedAt := -1, -1
	// The outermost operation middleware: its code after next runs once the
	// operation has its result, just before the generated code encodes it.
	mark := func(next StrictHandlerFunc, _ string) StrictHandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			response, err := next(ctx, w, r, request)
			encodeFrom = len(rec.All())
			return response, err
		}
	}
	mux := http.NewServeMux()
	HandlerWithOptions(strictOperations(orderProbe{st: st}, mark), StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: parameterUnreadable})
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
	if wrote < 0 || commit < 0 || seal < 0 || encodeFrom < 0 || encodedAt < encodeFrom {
		t.Fatalf("write #%d, commit #%d, seal #%d, encoding from #%d to #%d in the writer log: %v", wrote, commit, seal, encodeFrom, encodedAt, log)
	}
	if commit >= encodeFrom {
		t.Errorf("the unit holding the write committed at #%d, but encoding began after #%d statements: the writer was held while the answer was encoded", commit, encodeFrom)
	}
	for i := commit + 1; i < encodedAt; i++ {
		if log[i].Kind == storetest.Begin || log[i].InTx {
			t.Errorf("statement #%d %q ran in a transaction between the unit's commit and the end of encoding", i, log[i].SQL)
		}
	}
	if seal < encodedAt {
		t.Errorf("the seal statement is #%d, but the answer was encoded only after #%d statements: encoding must come first", seal, encodedAt)
	}
	if log[seal].InTx || (log[seal].Pool != store.PoolWriter && log[seal].Pool != store.PoolShared) {
		t.Errorf("the seal statement ran on pool %q in a transaction=%v, want the writer on its own", log[seal].Pool, log[seal].InTx)
	}

	// The sealed body is the whole answer: the replay returns it unchanged and the
	// write happened once.
	again := httptest.NewRecorder()
	h.ServeHTTP(again, directRequest(strings.NewReader(`{"summary":"made"}`), "order"))
	if again.Code != out.Code || again.Body.String() != out.Body.String() || again.Header().Get(ReplayedHeader) != "true" {
		t.Errorf("replay = %d %q replayed=%q, want the stored answer", again.Code, again.Body, again.Header().Get(ReplayedHeader))
	}
	var rows int
	if err := st.Reader().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM idem_probe`).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("probe rows = %d (%v), want the one write", rows, err)
	}
}

// TestAKeyedWriteWhoseAnswerCannotBeStoredOnceItCommittedIsAnsweredAndNeverRunsAgain
// fails only the statement that seals the key, after the unit with the write and
// the binding of the key has committed: the request is answered, its key is not
// released, and a retry is 409 instead of a second run.
func TestAKeyedWriteWhoseAnswerCannotBeStoredOnceItCommittedIsAnsweredAndNeverRunsAgain(t *testing.T) {
	st, _ := storetest.OpenMigrated(t)
	for _, q := range []string{
		`CREATE TABLE idem_probe (v TEXT)`,
		`CREATE TRIGGER seal_fail BEFORE UPDATE ON idempotency_keys WHEN NEW.status IS NOT NULL BEGIN SELECT RAISE(ABORT, 'injected seal failure'); END`,
	} {
		if _, err := st.Writer().ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	h := idempotent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if _, err := st.WriterFor(r.Context()).ExecContext(r.Context(), `INSERT INTO idem_probe (v) VALUES ('x')`); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"made":"it"}`))
	}), &idempotency.Store{Store: st})

	first := httptest.NewRecorder()
	h.ServeHTTP(first, directRequest(strings.NewReader("{}"), "unsealed"))
	if first.Code != http.StatusCreated || first.Body.String() != `{"made":"it"}` || first.Header().Get(ReplayedHeader) != "" {
		t.Errorf("first = %d %s (%q), want the answer of the committed write", first.Code, first.Body, first.Header().Get(ReplayedHeader))
	}
	retry := httptest.NewRecorder()
	h.ServeHTTP(retry, directRequest(strings.NewReader("{}"), "unsealed"))
	if retry.Code != http.StatusConflict {
		t.Errorf("retry = %d %s, want 409: the key stays in flight", retry.Code, retry.Body)
	}
	var rows int
	if err := st.Reader().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM idem_probe`).Scan(&rows); err != nil || rows != 1 || calls != 1 {
		t.Errorf("%d probe rows (%v), %d runs, want one of each", rows, err, calls)
	}
}
