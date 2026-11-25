package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/actors"
	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/attention"
	"github.com/n-orlov/village-hall/internal/auth"
	"github.com/n-orlov/village-hall/internal/cursors"
	"github.com/n-orlov/village-hall/internal/events"
	"github.com/n-orlov/village-hall/internal/idempotency"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/notes"
	"github.com/n-orlov/village-hall/internal/notify"
	"github.com/n-orlov/village-hall/internal/roster"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
	"github.com/n-orlov/village-hall/internal/timeline"
)

// keyServer is a handler over a real migrated store, with a real verifier and
// one key, "caller", that a test authenticates with.
type keyServer struct {
	t     *testing.T
	h     http.Handler
	keys  *auth.Keys
	token string
	st    *store.Store
	// clock is the time the idempotency store reads; tests move it.
	clock *testClock
}

// keyServerBuild is the build the key server reports, unless a test asks
// newKeyServerOfBuild for another.
var keyServerBuild = Build{Version: "v9.9.9", Revision: "abc123def456", Protocol: 7}

func newKeyServer(t *testing.T) *keyServer {
	t.Helper()
	return newKeyServerOfBuild(t, keyServerBuild)
}

// newKeyServerOfBuild is newKeyServer reporting build b.
func newKeyServerOfBuild(t *testing.T, b Build) *keyServer {
	t.Helper()
	return newKeyServerOn(t, migratedSQLiteURL(t), b)
}

// newKeyServerOn is newKeyServer over the database at url, empty or already
// migrated (databases.New and migratedSQLiteURL hand out migrated copies).
func newKeyServerOn(t *testing.T, url string, b Build) *keyServer {
	t.Helper()
	st := openStore(t, url)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return newKeyServerOver(t, st, b)
}

// newKeyServerOver is newKeyServer over the migrated store st.
func newKeyServerOver(t *testing.T, st *store.Store, b Build) *keyServer {
	t.Helper()
	keys, clock := &auth.Keys{Store: st}, newTestClock()
	token, _, err := keys.Create(context.Background(), "caller", "admin-cli", 0)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(ok, (&auth.Verifier{Store: st}).Verify, nil, keys, WithInitiatives(&initiatives.Store{Store: st}),
		WithTasks(&tasks.Store{Store: st}, &leases.Store{Store: st}), WithEvents(&events.Store{Store: st}), WithLeases(&leases.Store{Store: st}),
		WithAsks(&asks.Store{Store: st}), WithNotes(&notes.Store{Store: st}), WithNotifications(&notify.Store{Store: st}), WithAttention(&attention.Store{Store: st}), WithTimeline(&timeline.Store{Store: st}, &leases.Store{Store: st}, &attention.Store{Store: st}), WithActors(&roster.Store{Store: st, Leases: &leases.Store{Store: st}, Asks: &asks.Store{Store: st}}), WithCursors(&cursors.Store{Store: st}),
		WithMeta(b, &actors.Store{Store: st}), WithIdempotency(&idempotency.Store{Store: st, Now: clock.Now}))
	return &keyServer{t: t, h: h, keys: keys, token: token, st: st, clock: clock}
}

func (s *keyServer) send(method, path, body, actor string) *httptest.ResponseRecorder {
	s.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+s.token)
	r.Header.Set(ActorHeader, actor)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return doc
}

type eventRow struct{ principal, actor, typ, ref, summary, data string }

func (s *keyServer) events() []eventRow {
	s.t.Helper()
	rows, err := s.keys.Store.Reader().QueryContext(context.Background(),
		`SELECT e.principal, a.handle, e.type, e.ref_id, e.summary, e.data FROM events e JOIN actors a ON a.id = e.actor_id ORDER BY e.n`)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []eventRow
	for rows.Next() {
		var e eventRow
		if err := rows.Scan(&e.principal, &e.actor, &e.typ, &e.ref, &e.summary, &e.data); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestCreateKeyReturnsTheKeyOnceAndRecordsPrincipalAndActorSeparately(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	rec := s.send(http.MethodPost, "/api/v1/keys", `{"name":"laptop","expires_in":"12h"}`, "claude@host")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	created := decode(t, rec)
	key, _ := created["key"].(string)
	if !auth.WellFormed(key) || created["name"] != "laptop" || created["status"] != "active" || created["created_by"] != "key:caller" {
		t.Fatalf("created = %v", created)
	}
	if created["display_prefix"] != auth.DisplayPrefix(key) {
		t.Errorf("display_prefix %v for key %q", created["display_prefix"], key)
	}
	exp, err := time.Parse(time.RFC3339, created["expires_at"].(string))
	if err != nil || time.Until(exp) < 11*time.Hour || time.Until(exp) > 13*time.Hour {
		t.Errorf("expires_at = %v (%v), want about 12h away", created["expires_at"], err)
	}
	list := s.send(http.MethodGet, "/api/v1/keys", "", "claude@host")
	if strings.Contains(list.Body.String(), key[len(auth.KeyPrefix)+auth.DisplayPrefixLength:]) {
		t.Fatalf("the list holds the key: %s", list.Body)
	}
	items := decode(t, list)["items"].([]any)
	if len(items) != 2 || items[1].(map[string]any)["name"] != "laptop" {
		t.Fatalf("items = %v, want caller then laptop", items)
	}
	if _, has := items[1].(map[string]any)["key"]; has {
		t.Errorf("a listed key has a key field: %v", items[1])
	}
	want := eventRow{principal: "key:caller", actor: "claude@host", typ: "key_created", ref: created["id"].(string), summary: "key laptop created"}
	got := s.events()
	if len(got) != 1 || got[0].principal != want.principal || got[0].actor != want.actor || got[0].typ != want.typ || got[0].ref != want.ref || got[0].summary != want.summary {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
	if strings.Contains(got[0].data, key) || !strings.Contains(got[0].data, `"name":"laptop"`) || !strings.Contains(got[0].data, `"expires_at":"`) {
		t.Errorf("event data %q", got[0].data)
	}
}

func TestACreatedKeyWithoutExpiryHasNoExpiresAt(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	created := decode(t, s.send(http.MethodPost, "/api/v1/keys", `{"name":"laptop"}`, "a"))
	if _, has := created["expires_at"]; has {
		t.Errorf("expires_at = %v, want it absent", created["expires_at"])
	}
	if ev := s.events(); len(ev) != 1 || !strings.Contains(ev[0].data, `"expires_at":null`) {
		t.Errorf("events = %+v", ev)
	}
}

func TestCreateKeyTakesTheLongestExpiryInDaysOrHours(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	for name, in := range map[string]string{"days": "36500d", "hours": "876000h", "six-digit hours": "100000h"} {
		rec := s.send(http.MethodPost, "/api/v1/keys", `{"name":"`+strings.ReplaceAll(name, " ", "-")+`","expires_in":"`+in+`"}`, "a")
		if rec.Code != http.StatusCreated {
			t.Errorf("%s (%s): %d %s, want 201", name, in, rec.Code, rec.Body)
		}
	}
}

func TestCreateKeyRefusesWhatItCannotMake(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"bad name":      {`{"name":"bad name"}`, 400},
		"bad expiry":    {`{"name":"x","expires_in":"soon"}`, 400},
		"long expiry":   {`{"name":"x","expires_in":"99999d"}`, 400},
		"long hours":    {`{"name":"x","expires_in":"876001h"}`, 400},
		"taken name":    {`{"name":"caller"}`, 409},
		"unreadable":    {`{`, 400},
		"missing name":  {`{}`, 400},
		"wrong type":    {`{"name":7}`, 400},
		"empty body":    {``, 400},
		"expiry number": {`{"name":"x","expires_in":7}`, 400},
	} {
		rec := s.send(http.MethodPost, "/api/v1/keys", tc.body, "a")
		if rec.Code != tc.want || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s: %d %q, want %d problem", name, rec.Code, rec.Header().Get("Content-Type"), tc.want)
		}
	}
	if ev := s.events(); len(ev) != 0 {
		t.Errorf("a refused create left events %+v", ev)
	}
}

func TestRevokeKeyRevokesOnceAndRecordsIt(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	created := decode(t, s.send(http.MethodPost, "/api/v1/keys", `{"name":"laptop"}`, "a"))
	first := s.send(http.MethodPost, "/api/v1/keys/laptop/revoke", "", "b")
	if first.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", first.Code, first.Body)
	}
	doc := decode(t, first)
	if doc["status"] != "revoked" || doc["revoked_at"] == nil || doc["name"] != "laptop" {
		t.Fatalf("revoked = %v", doc)
	}
	second := decode(t, s.send(http.MethodPost, "/api/v1/keys/laptop/revoke", "", "c"))
	if second["revoked_at"] != doc["revoked_at"] {
		t.Errorf("second revoke moved revoked_at: %v then %v", doc["revoked_at"], second["revoked_at"])
	}
	ev := s.events()
	if len(ev) != 2 || ev[1].typ != "key_revoked" || ev[1].principal != "key:caller" || ev[1].actor != "b" || ev[1].ref != created["id"] {
		t.Fatalf("events = %+v, want create then exactly one revoke by b", ev)
	}
	rec := s.send(http.MethodPost, "/api/v1/keys/nobody/revoke", "", "b")
	if rec.Code != http.StatusNotFound {
		t.Errorf("revoke of an unknown key = %d, want 404", rec.Code)
	}
}

func TestAKeyRevokedThroughTheAPIGets401(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	rec := s.send(http.MethodPost, "/api/v1/keys/caller/revoke", "", "a")
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d", rec.Code)
	}
	rec = s.send(http.MethodGet, "/api/v1/keys", "", "a")
	if rec.Code != http.StatusUnauthorized || problem(t, rec)["detail"] != "key revoked" {
		t.Errorf("revoked key: %d %s", rec.Code, rec.Body)
	}
}

func TestKeyOperationsAnswer503WithoutAStore(t *testing.T) {
	t.Parallel()
	h := NewHandler(ok, acceptKey, nil, nil)
	for method, path := range map[string]string{http.MethodGet: "/api/v1/keys"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authedWith(method, path, ""))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", method, path, rec.Code)
		}
	}
	for path, body := range map[string]string{"/api/v1/keys": `{"name":"x"}`, "/api/v1/keys/x/revoke": ""} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authedWith(http.MethodPost, path, body))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("POST %s = %d, want 503", path, rec.Code)
		}
	}
}

func authedWith(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testKey)
	r.Header.Set(ActorHeader, "tester")
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestKeyOperationsRefuseARequestWithoutAPrincipal(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	o := keyOps{keys: s.keys}
	body := &CreateKeyJSONRequestBody{Name: "x"}
	if r, err := o.CreateKey(context.Background(), CreateKeyRequestObject{Body: body}); err != nil || r.(problemResponse).status != 401 {
		t.Errorf("create without a principal = %v %v", r, err)
	}
	if r, err := o.RevokeKey(context.Background(), RevokeKeyRequestObject{Name: "caller"}); err != nil || r.(problemResponse).status != 401 {
		t.Errorf("revoke without a principal = %v %v", r, err)
	}
	ctx := withIdentity(context.Background(), auth.Identity{Principal: "key:caller", Actor: "a"})
	if r, err := o.CreateKey(ctx, CreateKeyRequestObject{}); err != nil || r.(problemResponse).status != 400 {
		t.Errorf("create without a body = %v %v", r, err)
	}
}

func TestASessionActsAsTheOperatorOnTheKeyOperations(t *testing.T) {
	t.Parallel()
	l := newLogin(t, &fakeIdentity{})
	keys := &auth.Keys{Store: l.Sessions.Store}
	h := NewHandler(ok, acceptKey, l, keys)
	id, sess, err := l.Sessions.Create(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/keys", strings.NewReader(`{"name":"laptop"}`))
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: id, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	r.Header.Set(CSRFHeader, sess.CSRFToken)
	r.Header.Set("Origin", publicURL)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create by session = %d %s", rec.Code, rec.Body)
	}
	if got := decode(t, rec)["created_by"]; got != "oidc:alice@example.com" {
		t.Errorf("created_by = %v", got)
	}
	var principal, handle, kind string
	err = l.Sessions.Store.Reader().QueryRowContext(context.Background(),
		`SELECT e.principal, a.handle, a.kind FROM events e JOIN actors a ON a.id = e.actor_id`).Scan(&principal, &handle, &kind)
	if err != nil || principal != "oidc:alice@example.com" || handle != "operator" || kind != "operator" {
		t.Errorf("event principal %q actor %q kind %q (%v)", principal, handle, kind, err)
	}
}

func TestKeyStatusEnumsAcceptOnlyTheDeclaredStates(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"active", "expired", "revoked"} {
		if !KeyStatus(status).Valid() || !CreatedKeyStatus(status).Valid() {
			t.Errorf("%s is a declared status", status)
		}
	}
	if KeyStatus("gone").Valid() || CreatedKeyStatus("gone").Valid() {
		t.Error("gone is not a declared status")
	}
}
