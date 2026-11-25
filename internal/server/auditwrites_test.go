package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
)

// postNotesWrites is the writer statements one POST /notes costs once the actor
// row exists, as the recording helper saw them, in order.
func postNotesWrites(t *testing.T, s *keyServer, rec *storetest.Recorder, actor string) []string {
	t.Helper()
	rec.Reset()
	if got := s.send(http.MethodPost, "/api/v1/notes", `{"summary":"hello"}`, actor); got.Code != http.StatusCreated {
		t.Fatalf("POST /notes = %d %s", got.Code, got.Body)
	}
	var out []string
	for _, st := range rec.All() {
		if st.Pool == store.PoolWriter {
			out = append(out, string(st.Kind)+" "+strings.Join(strings.Fields(st.SQL), " "))
		}
	}
	return out
}

// TestPostNotesWriterStatementsArePinned pins the audit part of a note: the
// actor is recorded in one upsert statement (INSERT ... ON CONFLICT DO UPDATE
// ... RETURNING) and the origin the request carries is resolved once, so a
// first and a repeat POST cost the same writer statements: begin, the actor
// upsert, the note insert, the note's read-back, the event insert, commit.
func TestPostNotesWriterStatementsArePinned(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	s := newKeyServerOver(t, st, keyServerBuild)
	// The first request also stamps the key's last_used_at; it is not an audit write.
	postNotesWrites(t, s, rec, "warmup@host")
	for _, round := range []string{"first", "repeat"} {
		got := postNotesWrites(t, s, rec, "claude@host")
		actorStatements := 0
		for _, q := range got {
			if strings.Contains(q, "INTO actors") || strings.Contains(q, "UPDATE actors") || strings.Contains(q, "FROM actors") {
				actorStatements++
				if !strings.Contains(q, "INSERT INTO actors") || !strings.Contains(q, "ON CONFLICT") || !strings.Contains(q, "DO UPDATE") || !strings.Contains(q, "RETURNING") {
					t.Errorf("%s POST: an actors statement that is not the one upsert: %s", round, q)
				}
			}
		}
		if actorStatements != 1 {
			t.Errorf("%s POST /notes ran %d actors statements on the writer, want exactly 1: %q", round, actorStatements, got)
		}
		if len(got) != 6 {
			t.Errorf("%s POST /notes ran %d writer statements, want 6: %q", round, len(got), got)
		}
	}
}
