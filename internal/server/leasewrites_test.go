package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
)

// leaseWrites is the writer statements one request costs, as the recording
// helper saw them, in order: the kind and the statement text on one line.
func leaseWrites(t *testing.T, s *keyServer, rec *storetest.Recorder, path, body string, want int) []string {
	t.Helper()
	rec.Reset()
	if got := s.sendWith(http.MethodPost, path, body, "claude@host", map[string]string{WayBackHeader: "tmux 3"}); got.Code != want {
		t.Fatalf("POST %s = %d %s, want %d", path, got.Code, got.Body, want)
	}
	var out []string
	for _, st := range rec.All() {
		if st.Pool == store.PoolWriter {
			out = append(out, string(st.Kind)+" "+strings.Join(strings.Fields(st.SQL), " "))
		}
	}
	return out
}

// countWith is how many of the statements contain every one of the fragments.
func countWith(got []string, fragments ...string) int {
	n := 0
	for _, q := range got {
		all := true
		for _, f := range fragments {
			all = all && strings.Contains(q, f)
		}
		if all {
			n++
		}
	}
	return n
}

// TestLeaseWritesStatementsArePinned pins what a lease write costs the writer
// (once the actors exist): the contested leases of the answer load in one
// statement (the leases and the shared resources together), the lease the write
// already found is the one the answer carries (no read follows the renewal's
// update), and the resources of a lease go in with one InsertMany statement.
func TestLeaseWritesStatementsArePinned(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	s := newKeyServerOver(t, st, keyServerBuild)
	s.create("aa", "")
	s.task(`{"initiative":"aa","title":"one"}`)
	s.task(`{"initiative":"aa","title":"two"}`)
	// the first request also stamps the key's last_used_at; the other holder of
	// the contested resources makes the answer carry a conflict
	leaseWrites(t, s, rec, "/api/v1/notes", `{"summary":"hello"}`, http.StatusCreated)
	if got := s.send(http.MethodPost, "/api/v1/leases", `{"task":"aa-2","intent":"theirs","resources":["repo:b","repo:c","repo:z"]}`, "codex@host"); got.Code != http.StatusCreated {
		t.Fatalf("POST /leases = %d %s", got.Code, got.Body)
	}
	const (
		body       = `{"task":"aa-1","intent":"mine","resources":["repo:b","repo:a","repo:c"]}`
		conflictOn = "JOIN lease_resources r ON r.lease_id = l.id"
	)

	t.Run("POST /leases new", func(t *testing.T) {
		got := leaseWrites(t, s, rec, "/api/v1/leases", body, http.StatusCreated)
		if len(got) != 12 {
			t.Errorf("a new lease ran %d writer statements, want 12: %q", len(got), got)
		}
		if n := countWith(got, "INSERT INTO lease_resources"); n != 1 {
			t.Errorf("lease resources went in with %d statements, want 1 InsertMany: %q", n, got)
		}
		if n := countWith(got, "DELETE FROM lease_resources"); n != 0 {
			t.Errorf("a new lease deleted its (no) resources %d times: %q", n, got)
		}
		if n := countWith(got, conflictOn); n != 1 {
			t.Errorf("the contested leases loaded with %d statements, want 1: %q", n, got)
		}
	})
	t.Run("POST /leases renewing", func(t *testing.T) {
		got := leaseWrites(t, s, rec, "/api/v1/leases", body, http.StatusOK)
		if len(got) != 13 {
			t.Errorf("a renewing start ran %d writer statements, want 13: %q", len(got), got)
		}
		if n := countWith(got, "INSERT INTO lease_resources"); n != 1 {
			t.Errorf("lease resources went in with %d statements, want 1 InsertMany: %q", n, got)
		}
		if n := countWith(got, conflictOn); n != 1 {
			t.Errorf("the contested leases loaded with %d statements, want 1: %q", n, got)
		}
	})
	t.Run("POST /leases/{id}/renew with a way_back", func(t *testing.T) {
		got := leaseWrites(t, s, rec, "/api/v1/leases/L2/renew", `{"resources":["repo:b","repo:q"]}`, http.StatusOK)
		if len(got) != 14 {
			t.Errorf("a renewal ran %d writer statements, want 14: %q", len(got), got)
		}
		if n := countWith(got, "INSERT INTO actors", "way_back = excluded.way_back"); n != 1 {
			t.Errorf("the way_back took %d actor upserts, want 1: %q", n, got)
		}
		if n := countWith(got, "UPDATE actors"); n != 0 {
			t.Errorf("the actor took %d UPDATE statements, want 0: %q", n, got)
		}
		if n := countWith(got, conflictOn); n != 1 {
			t.Errorf("the contested leases loaded with %d statements, want 1: %q", n, got)
		}
	})
}
