package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/n-orlov/village-hall/internal/idempotency"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
)

// TestAnIdempotentAnswerIsEncodedBeforeTheStatementThatSealsItsKey pins the order
// of a keyed write's writer statements: the handler's write, then the answer
// fully encoded, then the single statement that seals the key (the UPDATE of its
// row), then the commit. The seal statement runs with the finished body in hand;
// it does no encoding of its own.
func TestAnIdempotentAnswerIsEncodedBeforeTheStatementThatSealsItsKey(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	idem := &idempotency.Store{Store: st}
	encodedAt := -1
	h := idempotent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A write inside the unit, so the writer is held while the answer is built.
		if _, err := st.WriterFor(r.Context()).ExecContext(r.Context(), `UPDATE idempotency_keys SET state = state WHERE 1 = 0`); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(map[string]string{"made": "it"}); err != nil {
			t.Error(err)
		}
		encodedAt = len(rec.All())
	}), idem)

	rec.Reset()
	out := httptest.NewRecorder()
	h.ServeHTTP(out, directRequest(strings.NewReader("{}"), "order"))
	if out.Code != http.StatusCreated || strings.TrimSpace(out.Body.String()) != `{"made":"it"}` {
		t.Fatalf("answer = %d %q", out.Code, out.Body)
	}

	log := rec.All()
	seal, commit := -1, -1
	for i, s := range log {
		sql := strings.Join(strings.Fields(s.SQL), " ")
		switch {
		case s.Kind == storetest.Exec && strings.HasPrefix(sql, "UPDATE idempotency_keys SET state = 'done'"):
			seal = i
		case s.Kind == storetest.Commit && seal >= 0:
			commit = i
		}
	}
	if seal < 0 || commit < seal {
		t.Fatalf("no seal statement followed by a commit in the writer log: %v", log)
	}
	if encodedAt < 0 || encodedAt > seal {
		t.Errorf("the answer was finished after %d statements, but the seal statement is #%d: encoding must come first", encodedAt, seal)
	}
	if log[seal].Pool != store.PoolWriter && log[seal].Pool != store.PoolShared {
		t.Errorf("the seal statement ran on pool %q, want the writer", log[seal].Pool)
	}

	// The sealed body is the whole answer: the replay returns it unchanged.
	again := httptest.NewRecorder()
	h.ServeHTTP(again, directRequest(strings.NewReader("{}"), "order"))
	if again.Code != out.Code || again.Body.String() != out.Body.String() || again.Header().Get(ReplayedHeader) != "true" {
		t.Errorf("replay = %d %q replayed=%q, want the stored answer", again.Code, again.Body, again.Header().Get(ReplayedHeader))
	}
}
