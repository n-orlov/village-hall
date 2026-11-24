package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/actors"
	"github.com/n-orlov/village-hall/internal/auth"
	"github.com/n-orlov/village-hall/internal/events"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
)

// sessionServer is a board with OIDC login whose session clock a test moves.
type sessionServer struct {
	t    *testing.T
	h    http.Handler
	st   *store.Store
	now  time.Time
	id   string
	csrf string
}

func newSessionServer(t *testing.T) *sessionServer {
	t.Helper()
	st, err := store.Open(migratedSQLiteURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := &sessionServer{t: t, st: st, now: time.Now().UTC().Truncate(time.Millisecond)}
	sessions := auth.Sessions{Store: st, Idle: 100 * time.Second, Now: func() time.Time { return s.now }}
	login := &Login{Identity: &fakeIdentity{}, Sessions: sessions, Allowed: map[string]bool{"alice@example.com": true}, Operator: "operator", PublicURL: publicURL}
	s.h = NewHandler(ok, (&auth.Verifier{Store: st}).Verify, login, nil, WithInitiatives(&initiatives.Store{Store: st}),
		WithTasks(&tasks.Store{Store: st}, &leases.Store{Store: st}), WithEvents(&events.Store{Store: st}), WithLeases(&leases.Store{Store: st}),
		WithMeta(Build{Version: "v9.9.9", Protocol: 7}, &actors.Store{Store: st}))
	id, sess, err := sessions.Create(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	s.id, s.csrf = id, sess.CSRFToken
	return s
}

// send makes a cookie-authenticated API request, with the CSRF token and the
// board's Origin when withCSRF.
func (s *sessionServer) send(method, path, body string, withCSRF bool) *httptest.ResponseRecorder {
	s.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Accept", "application/json")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if withCSRF {
		r.Header.Set("X-CSRF-Token", s.csrf)
		r.Header.Set("Origin", publicURL)
	}
	r.AddCookie(s.cookie())
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	return rec
}

func (s *sessionServer) cookie() *http.Cookie {
	return &http.Cookie{Name: SessionCookie, Value: s.id, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

// lastSeen is the session's stored last_seen_at, or -1 when it has no row.
func (s *sessionServer) lastSeen() int64 {
	s.t.Helper()
	var at int64 = -1
	rows, err := s.st.Reader().QueryContext(context.Background(), `SELECT last_seen_at FROM sessions WHERE id_hash = ?`, auth.HashKey(s.id))
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&at); err != nil {
			s.t.Fatal(err)
		}
	}
	return at
}

func TestASessionIsMarkedSeenOnlyByARequestThatSucceeds(t *testing.T) {
	t.Parallel()
	s := newSessionServer(t)
	created := s.lastSeen()
	s.now = s.now.Add(11 * time.Second) // past a tenth of the idle timeout: a use would write
	refused := []struct {
		method, path, body string
		csrf               bool
		status             int
	}{
		{http.MethodPost, "/api/v1/leases", `{"task":"aa-9","intent":"x"}`, true, http.StatusNotFound},
		{http.MethodPost, "/api/v1/leases/L9/renew", "", true, http.StatusNotFound},
		{http.MethodPost, "/api/v1/leases/L999/end", "", true, http.StatusNotFound},
		{http.MethodPost, "/api/v1/leases/L999/end", "", false, http.StatusForbidden},
		{http.MethodPost, "/api/v1/leases/L9/ack", "", true, http.StatusNotFound},
		{http.MethodPost, "/api/v1/leases", `{`, true, http.StatusBadRequest},
		{http.MethodGet, "/api/v1/tasks/aa-9", "", false, http.StatusNotFound},
	}
	for _, tc := range refused {
		if rec := s.send(tc.method, tc.path, tc.body, tc.csrf); rec.Code != tc.status {
			t.Fatalf("%s %s = %d, want %d: %s", tc.method, tc.path, rec.Code, tc.status, rec.Body)
		}
		if got := s.lastSeen(); got != created {
			t.Fatalf("%s %s, answered %d, moved last_seen_at from %d to %d", tc.method, tc.path, tc.status, created, got)
		}
	}
	if rec := s.send(http.MethodGet, "/api/v1/leases", "", false); rec.Code != http.StatusOK {
		t.Fatalf("a successful read = %d: %s", rec.Code, rec.Body)
	}
	if got, want := s.lastSeen(), store.EncodeTime(s.now); got != want {
		t.Fatalf("after a successful read last_seen_at = %d, want %d", got, want)
	}
}

func TestADeadSessionIsRemovedOnlyByARequestThatIsNotRefused(t *testing.T) {
	t.Parallel()
	s := newSessionServer(t)
	s.now = s.now.Add(101 * time.Second) // idle past the timeout
	if rec := s.send(http.MethodPost, "/api/v1/leases/L9/end", "", true); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a write with a dead session = %d, want 401", rec.Code)
	}
	if s.lastSeen() < 0 {
		t.Fatal("a refused request deleted the dead session's row")
	}
	if rec := browse(s.h, http.MethodGet, "/", s.cookie()); rec.Code != http.StatusFound {
		t.Fatalf("a navigation with a dead session = %d, want 302", rec.Code)
	}
	if s.lastSeen() >= 0 {
		t.Fatal("the redirect to login left the dead session's row")
	}
}
