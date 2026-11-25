package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/auth"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
)

// recordedBoard is a keyServer over a recording store seeded with two
// initiatives (aa, bb), three tasks each, one done task per initiative (which
// needs attention) and one live lease.
func recordedBoard(t *testing.T) (*keyServer, *storetest.Recorder) {
	t.Helper()
	st, rec := storetest.OpenMigrated(t)
	s := newKeyServerOver(t, st, keyServerBuild)
	for _, slug := range []string{"aa", "bb"} {
		s.create(slug, "")
		for _, title := range []string{"one", "two", "three"} {
			s.task(`{"initiative":"` + slug + `","title":"` + title + `"}`)
		}
		if rec := s.send(http.MethodPost, "/api/v1/tasks/"+slug+"-3/done", "", "claude@host"); rec.Code != http.StatusOK {
			t.Fatalf("done %s-3 = %d %s", slug, rec.Code, rec.Body)
		}
	}
	ls := &leases.Store{Store: st}
	if _, err := ls.Start(context.Background(), audit.Actor{Handle: "codex@host"}, "key:caller", leases.Start{Task: "aa-1", Intent: "wire it"}); err != nil {
		t.Fatal(err)
	}
	return s, rec
}

// readStatements is how many statements a GET of path costs on each pool.
func readStatements(t *testing.T, s *keyServer, rec *storetest.Recorder, path string) (reader, writer int) {
	t.Helper()
	rec.Reset()
	if got := s.send(http.MethodGet, path, "", "claude@host"); got.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, got.Code, got.Body)
	}
	return rec.Statements(store.PoolReader), rec.Statements(store.PoolWriter)
}

// duePoll is the statement that looks for a lease that ran out.
const duePoll = "FROM leases WHERE state = 'live' AND expires_at <"

// dueLookups is how many statements since the last Reset look for a due lease.
func dueLookups(rec *storetest.Recorder) int {
	n := 0
	for _, st := range rec.All() {
		if strings.Contains(st.SQL, duePoll) {
			n++
		}
	}
	return n
}

// The pins are the statement counts measured on recordedBoard; a count above
// one fails. An initiative filter adds the lookups that scope the derivation to
// the initiative, and still looks for a due lease once.
func TestGetTasksStatementCountsArePinned(t *testing.T) {
	s, rec := recordedBoard(t)
	for _, c := range []struct {
		path   string
		reader int
	}{{"/api/v1/tasks", 11}, {"/api/v1/tasks?initiative=aa", 15}, {"/api/v1/timeline", 14}} {
		reader, writer := readStatements(t, s, rec, c.path)
		if reader > c.reader || writer != 0 {
			t.Errorf("GET %s = %d reader and %d writer statements, want at most %d and 0", c.path, reader, writer, c.reader)
		}
		if n := dueLookups(rec); n != 1 {
			t.Errorf("GET %s looked for a due lease %d times, want once", c.path, n)
		}
	}
}

// dueLease starts a lease on task that ran out long ago.
func dueLease(t *testing.T, s *keyServer, task string) {
	t.Helper()
	past := time.Now().Add(-48 * time.Hour)
	ttl := int64(60)
	ls := &leases.Store{Store: s.st, Now: func() time.Time { return past }}
	if _, err := ls.Start(context.Background(), audit.Actor{Handle: "codex@host"}, "key:caller", leases.Start{Task: task, Intent: "gone", TTL: &ttl}); err != nil {
		t.Fatal(err)
	}
}

func TestOneListReadExpiresADueLeaseAndTheNextWritesNothing(t *testing.T) {
	for _, path := range []string{"/api/v1/tasks", "/api/v1/tasks?initiative=aa", "/api/v1/tasks?initiative=bb", "/api/v1/timeline"} {
		t.Run(path, func(t *testing.T) {
			s, rec := recordedBoard(t)
			dueLease(t, s, "aa-2")
			rec.Reset()
			if got := s.send(http.MethodGet, path, "", "claude@host"); got.Code != http.StatusOK {
				t.Fatalf("GET %s = %d %s", path, got.Code, got.Body)
			}
			if w := rec.Count(store.PoolWriter, storetest.Query) + rec.Count(store.PoolWriter, storetest.Exec); w == 0 {
				t.Errorf("GET %s wrote nothing: the due lease was not expired", path)
			}
			var state string
			if err := s.st.Reader().QueryRow(`SELECT state FROM leases WHERE task_id = (SELECT id FROM tasks WHERE seq = 2 AND initiative_id = (SELECT id FROM initiatives WHERE slug = 'aa'))`).Scan(&state); err != nil || state != "expired" {
				t.Errorf("the due lease is %q (%v), want expired", state, err)
			}
			if _, writer := readStatements(t, s, rec, path); writer != 0 {
				t.Errorf("a second GET %s wrote %d statements", path, writer)
			}
		})
	}
}

func TestAnInitiativeFilterDerivesAttentionForThatInitiativeOnly(t *testing.T) {
	s, rec := recordedBoard(t)
	for slug, other := range map[string]string{"aa": "bb", "bb": "aa"} {
		doc := decode(t, s.send(http.MethodGet, "/api/v1/tasks?initiative="+slug, "", "claude@host"))
		items, _ := doc["items"].([]any)
		if len(items) != 3 {
			t.Fatalf("initiative %s lists %d tasks, want 3", slug, len(items))
		}
		for _, it := range items {
			m := it.(map[string]any)
			att, _ := m["attention"].([]any)
			wantDone := m["ref"] == slug+"-3"
			if (len(att) == 1) != wantDone || len(att) > 1 {
				t.Errorf("%v attention = %v, want done only on %s-3 (not %s)", m["ref"], att, slug, other)
			}
		}
	}
	// The derivation itself is scoped: each of its three section reads filters on the initiative.
	for path, scoped := range map[string]bool{"/api/v1/tasks": false, "/api/v1/tasks?initiative=aa": true} {
		rec.Reset()
		s.send(http.MethodGet, path, "", "claude@host")
		sections, filtered := 0, 0
		for _, st := range rec.All() {
			sql := strings.Join(strings.Fields(st.SQL), " ")
			if strings.Contains(sql, "FROM asks k JOIN actors a") || strings.Contains(sql, "t.status = 'done'") && strings.Contains(sql, "ll.n") ||
				strings.Contains(sql, "l.state = 'expired'") {
				sections++
				if strings.Contains(sql, " IN (?) ORDER BY") {
					filtered++
				}
			}
		}
		if sections != 3 || scoped != (filtered == 3) || (!scoped && filtered != 0) {
			t.Errorf("GET %s ran %d attention section reads, %d of them filtered on the initiative", path, sections, filtered)
		}
	}
	both := decode(t, s.send(http.MethodGet, "/api/v1/tasks?attention=true", "", "claude@host"))
	if items, _ := both["items"].([]any); len(items) != 2 {
		t.Errorf("attention=true lists %d tasks, want 2", len(items))
	}
	one := decode(t, s.send(http.MethodGet, "/api/v1/tasks?attention=true&initiative=bb", "", "claude@host"))
	if items, _ := one["items"].([]any); len(items) != 1 || items[0].(map[string]any)["ref"] != "bb-3" {
		t.Errorf("attention=true&initiative=bb = %v", one)
	}
	if rec := s.send(http.MethodGet, "/api/v1/tasks?initiative=zz", "", "claude@host"); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown initiative = %d", rec.Code)
	}
}

// GET /timeline resolves the kept task ids once (the tasks statement; the nine timeline statements after it are
// keyed by them) and each named initiative once (the lookup of its row and its tags, two statements), where Check
// and Read each looked it up. The pins are exact, measured on recordedBoard, and cover the attention derivation
// and the due-lease lookup around the read.
func TestGetTimelineStatementCountIsPinned(t *testing.T) {
	s, rec := recordedBoard(t)
	for _, c := range []struct {
		path   string
		reader int
	}{
		{"/api/v1/timeline", 14},
		{"/api/v1/timeline?initiative=aa", 16},
		{"/api/v1/timeline?initiative=aa&initiative=bb", 18},
	} {
		reader, writer := readStatements(t, s, rec, c.path)
		if reader != c.reader || writer != 0 {
			t.Errorf("GET %s = %d reader and %d writer statements, want exactly %d and 0", c.path, reader, writer, c.reader)
		}
		lookups := 0
		for _, st := range rec.All() {
			if strings.Contains(st.SQL, "FROM initiatives i JOIN actors c") {
				lookups++
			}
		}
		if want := strings.Count(c.path, "initiative="); lookups != want {
			t.Errorf("GET %s looked up %d initiative rows, want %d", c.path, lookups, want)
		}
	}
}

// TestGetReadyzIsTwoReaderStatements pins the statements of one GET /readyz over
// a migrated store: the key lookup and the applied version, nothing else.
func TestGetReadyzIsTwoReaderStatements(t *testing.T) {
	t.Parallel()
	st, rec := storetest.OpenMigrated(t)
	keys := &auth.Keys{Store: st}
	token, _, err := keys.Create(context.Background(), "caller", "admin-cli", 0)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(st.Ready, (&auth.Verifier{Store: st}).Verify, nil, keys)
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set(ActorHeader, "claude@host")
	h.ServeHTTP(httptest.NewRecorder(), r) // the key's first use stamps last_used_at
	rec.Reset()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /readyz = %d %s", w.Code, w.Body)
	}
	// The key lookup and the applied version, both reads; nothing else.
	if reads := rec.Statements(store.PoolReader); reads != 2 || rec.Statements(store.PoolWriter) != 0 {
		t.Errorf("GET /readyz: %+v", rec.All())
	}
	versions := 0
	for _, s := range rec.All() {
		if strings.Contains(s.SQL, "goose_db_version") {
			versions++
		}
	}
	if versions != 1 {
		t.Errorf("GET /readyz read the applied version %d times, want 1", versions)
	}
}
