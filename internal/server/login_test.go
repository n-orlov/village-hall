package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/auth"
	"github.com/n-orlov/village-hall/internal/oidc"
	"github.com/n-orlov/village-hall/internal/store"
)

// fakeIdentity is an identity provider whose every answer a test sets.
type fakeIdentity struct {
	authErr, exchangeErr, verifyErr error
	claims                          oidc.Claims
	gotVerifier, gotNonce, gotCode  string
}

func (f *fakeIdentity) AuthURL(_ context.Context, state, nonce, verifier string) (string, error) {
	if f.authErr != nil {
		return "", f.authErr
	}
	f.gotVerifier, f.gotNonce = verifier, nonce
	return "https://idp.test/authorize?state=" + url.QueryEscape(state) + "&nonce=" + url.QueryEscape(nonce), nil
}

func (f *fakeIdentity) Exchange(_ context.Context, code, verifier string) (string, error) {
	f.gotCode = code
	if verifier != f.gotVerifier {
		return "", errors.New("verifier differs from the login's")
	}
	return "raw-token", f.exchangeErr
}

func (f *fakeIdentity) Verify(_ context.Context, _, nonce string) (oidc.Claims, error) {
	if f.verifyErr == nil && nonce != f.gotNonce {
		return oidc.Claims{}, oidc.ErrNonce
	}
	return f.claims, f.verifyErr
}

const publicURL = "https://board.example.test"

func newLogin(t *testing.T, id *fakeIdentity) *Login {
	t.Helper()
	st, err := store.Open(migratedSQLiteURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &Login{Identity: id, Sessions: auth.Sessions{Store: st}, Allowed: map[string]bool{"alice@example.com": true}, Operator: "operator", PublicURL: publicURL}
}

func browse(h http.Handler, method, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Accept", "text/html")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func setCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// startLogin runs GET /auth/login and returns the callback URL a provider
// would send the browser to, with the pre-login cookie.
func startLogin(t *testing.T, h http.Handler) (string, *http.Cookie) {
	t.Helper()
	rec := browse(h, http.MethodGet, "/auth/login")
	if rec.Code != http.StatusFound {
		t.Fatalf("/auth/login answered %d: %s", rec.Code, rec.Body)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	cookie := setCookie(rec, loginCookie)
	if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("pre-login cookie %+v", cookie)
	}
	return callbackPath + "?code=the-code&state=" + url.QueryEscape(loc.Query().Get("state")), cookie
}

func handler(l *Login) http.Handler {
	return NewHandler(ok, func(context.Context, string) (string, error) { return "k", nil }, l, nil)
}

var alice = oidc.Claims{Email: "Alice@Example.com", EmailVerified: true}

func TestAnonymousBrowserNavigationGoesToLoginAndAPICallsGet401(t *testing.T) {
	t.Parallel()
	h := handler(newLogin(t, &fakeIdentity{}))
	rec := browse(h, http.MethodGet, "/")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/auth/login" {
		t.Fatalf("navigation to /: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, path := range []string{"/", "/auth/login", "/auth/oidc/callback", "/auth/session", "/readyz"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("API call to %s: %d, want 401", path, rec.Code)
		}
	}
	if rec := browse(h, http.MethodPost, "/auth/logout"); rec.Code != http.StatusUnauthorized {
		t.Errorf("a POST with only Accept: text/html is not a navigation: %d, want 401", rec.Code)
	}
}

func TestLoginRedirectsToTheProviderWithPKCEStateAndNonce(t *testing.T) {
	t.Parallel()
	id := &fakeIdentity{}
	rec := browse(handler(newLogin(t, id)), http.MethodGet, "/auth/login")
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "https://idp.test/authorize?state=") {
		t.Fatalf("login: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(id.gotVerifier) != 43 || id.gotNonce == "" {
		t.Fatalf("verifier %q, nonce %q", id.gotVerifier, id.gotNonce)
	}
}

func TestLoginReportsAnUnreachableProviderAndATooBusyServer(t *testing.T) {
	t.Parallel()
	down := handler(newLogin(t, &fakeIdentity{authErr: oidc.ErrProvider}))
	if rec := browse(down, http.MethodGet, "/auth/login"); rec.Code != http.StatusBadGateway {
		t.Errorf("provider down: %d, want 502", rec.Code)
	}
	l := newLogin(t, &fakeIdentity{})
	l.pending = map[string]pendingLogin{}
	for i := 0; i < maxPending; i++ {
		l.pending[string(rune('a'+i%26))+time.Duration(i).String()] = pendingLogin{expires: time.Now().Add(time.Hour)}
	}
	if rec := browse(handler(l), http.MethodGet, "/auth/login"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("full table: %d, want 503", rec.Code)
	}
}

func TestExpiredPendingLoginsAreSweptWhenTheTableIsFull(t *testing.T) {
	t.Parallel()
	l := newLogin(t, &fakeIdentity{})
	l.pending = map[string]pendingLogin{}
	for i := 0; i < maxPending; i++ {
		l.pending[time.Duration(i).String()] = pendingLogin{expires: time.Now().Add(-time.Minute)}
	}
	if _, err := l.begin(pendingLogin{}); err != nil || len(l.pending) != 1 {
		t.Fatalf("begin: %v, %d pending", err, len(l.pending))
	}
	id, _ := l.begin(pendingLogin{})
	l.Now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, ok := l.take(id); ok {
		t.Fatal("a pending login was taken after loginTTL")
	}
}

func TestCallbackStartsASessionForAnAllowedVerifiedEmail(t *testing.T) {
	t.Parallel()
	l := newLogin(t, &fakeIdentity{claims: alice})
	h := handler(l)
	callback, cookie := startLogin(t, h)
	rec := browse(h, http.MethodGet, callback, cookie)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("callback: %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	session := setCookie(rec, SessionCookie)
	if session == nil || !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteLaxMode || session.MaxAge != int(auth.DefaultSessionTTL/time.Second) {
		t.Fatalf("session cookie %+v", session)
	}
	// The pre-login cookie is single-use: the same callback again is refused.
	if again := browse(h, http.MethodGet, callback, cookie); again.Code != http.StatusBadRequest {
		t.Fatalf("replayed callback: %d, want 400", again.Code)
	}
	body := browse(h, http.MethodGet, "/auth/session", session)
	if body.Code != http.StatusOK || !strings.Contains(body.Body.String(), `"principal":"oidc:Alice@Example.com"`) || !strings.Contains(body.Body.String(), `"actor":"operator"`) {
		t.Fatalf("session: %d %s", body.Code, body.Body)
	}
	if browse(h, http.MethodGet, "/readyz", session).Code != http.StatusOK {
		t.Fatal("a session cookie did not authenticate /readyz")
	}
	out := write(h, "/auth/logout", session, csrfOf(t, body), publicURL)
	if out.Code != http.StatusNoContent || setCookie(out, SessionCookie).MaxAge >= 0 {
		t.Fatalf("logout: %d", out.Code)
	}
	if after := browse(h, http.MethodGet, "/readyz", session); after.Code != http.StatusFound {
		t.Fatalf("after logout a navigation: %d, want a redirect to login", after.Code)
	}
}

func TestCallbackRefusesWhatDoesNotBelongToALoginInProgress(t *testing.T) {
	t.Parallel()
	h := handler(newLogin(t, &fakeIdentity{claims: alice}))
	cases := map[string]struct {
		path     func(callback string) string
		noCookie bool
		want     int
	}{
		"no cookie":    {func(c string) string { return c }, true, http.StatusBadRequest},
		"wrong state":  {func(string) string { return callbackPath + "?code=c&state=forged" }, false, http.StatusBadRequest},
		"no state":     {func(string) string { return callbackPath + "?code=c" }, false, http.StatusBadRequest},
		"provider err": {func(c string) string { return strings.Replace(c, "code=the-code", "error=access_denied", 1) }, false, http.StatusForbidden},
		"no code":      {func(c string) string { return strings.Replace(c, "code=the-code&", "", 1) }, false, http.StatusBadRequest},
	}
	for name, tc := range cases {
		callback, cookie := startLogin(t, h)
		cs := []*http.Cookie{cookie}
		if tc.noCookie {
			cs = nil
		}
		rec := browse(h, http.MethodGet, tc.path(callback), cs...)
		if rec.Code != tc.want || setCookie(rec, SessionCookie) != nil {
			t.Errorf("%s: %d, want %d and no session", name, rec.Code, tc.want)
		}
	}
	unknown := &http.Cookie{Name: loginCookie, Value: "nope", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	if got := browse(h, http.MethodGet, callbackPath+"?code=c&state=s", unknown).Code; got != http.StatusBadRequest {
		t.Errorf("unknown pre-login id: %d, want 400", got)
	}
}

func TestCallbackMapsEachFailureToItsStatus(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		id   *fakeIdentity
		want int
	}{
		"code refused":   {&fakeIdentity{exchangeErr: oidc.ErrProvider, claims: alice}, http.StatusBadGateway},
		"bad signature":  {&fakeIdentity{verifyErr: oidc.ErrInvalidToken}, http.StatusUnauthorized},
		"foreign nonce":  {&fakeIdentity{verifyErr: oidc.ErrNonce}, http.StatusBadRequest},
		"jwks down":      {&fakeIdentity{verifyErr: oidc.ErrProvider}, http.StatusBadGateway},
		"not verified":   {&fakeIdentity{claims: oidc.Claims{Email: "alice@example.com"}}, http.StatusForbidden},
		"not allowed":    {&fakeIdentity{claims: oidc.Claims{Email: "bob@example.com", EmailVerified: true}}, http.StatusForbidden},
		"no email claim": {&fakeIdentity{claims: oidc.Claims{EmailVerified: true}}, http.StatusForbidden},
	}
	for name, tc := range cases {
		h := handler(newLogin(t, tc.id))
		callback, cookie := startLogin(t, h)
		rec := browse(h, http.MethodGet, callback, cookie)
		if rec.Code != tc.want || setCookie(rec, SessionCookie) != nil {
			t.Errorf("%s: %d (session %v), want %d and no session", name, rec.Code, setCookie(rec, SessionCookie), tc.want)
		}
	}
}

func TestCallbackFailsWhenTheSessionCannotBeStored(t *testing.T) {
	t.Parallel()
	l := newLogin(t, &fakeIdentity{claims: alice})
	h := handler(l)
	callback, cookie := startLogin(t, h)
	_ = l.Sessions.Store.Close()
	if rec := browse(h, http.MethodGet, callback, cookie); rec.Code != http.StatusInternalServerError {
		t.Fatalf("closed store: %d, want 500", rec.Code)
	}
}

func TestAnInvalidSessionCookieIsAnonymousAndAStoreFailureIs503(t *testing.T) {
	t.Parallel()
	l := newLogin(t, &fakeIdentity{})
	h := handler(l)
	stale := &http.Cookie{Name: SessionCookie, Value: "stale", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	if rec := browse(h, http.MethodGet, "/", stale); rec.Code != http.StatusFound {
		t.Errorf("stale cookie navigation: %d, want a redirect", rec.Code)
	}
	_ = l.Sessions.Store.Close()
	if rec := browse(h, http.MethodGet, "/", stale); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unusable store: %d, want 503", rec.Code)
	}
}

func TestSessionAndLogoutNeedABrowserSessionAndLoginOffAnswers404(t *testing.T) {
	t.Parallel()
	h := handler(newLogin(t, &fakeIdentity{}))
	for _, c := range []struct{ method, path string }{{http.MethodGet, "/auth/session"}, {http.MethodPost, "/auth/logout"}} {
		r := httptest.NewRequest(c.method, c.path, nil)
		r.Header.Set("Authorization", "Bearer brd_x")
		r.Header.Set(ActorHeader, "a")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a key: %d, want 401", c.method, c.path, rec.Code)
		}
	}
	off := handler(nil)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/auth/login"}, {http.MethodGet, "/auth/oidc/callback"}, {http.MethodGet, "/auth/session"}, {http.MethodPost, "/auth/logout"},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		r.Header.Set("Authorization", "Bearer brd_x")
		r.Header.Set(ActorHeader, "a")
		rec := httptest.NewRecorder()
		off.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s with login off: %d, want 404", c.method, c.path, rec.Code)
		}
	}
}

func TestLogoutFailsWhenTheStoreCannotDeleteTheSession(t *testing.T) {
	t.Parallel()
	l := newLogin(t, &fakeIdentity{claims: alice})
	h := handler(l)
	callback, cookie := startLogin(t, h)
	session := setCookie(browse(h, http.MethodGet, callback, cookie), SessionCookie)
	ctx := withSession(context.Background(), sessionIdentity{id: session.Value})
	_ = l.Sessions.Store.Close()
	if _, err := (loginOps{login: l}).PostAuthLogout(ctx, PostAuthLogoutRequestObject{}); err == nil {
		t.Fatal("logout on a closed store succeeded")
	}
}

// write sends a POST the way a page script would, with the given CSRF token and
// Origin (a header left empty is not sent).
func write(h http.Handler, path string, session *http.Cookie, token, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, nil)
	r.Header.Set("Accept", "application/json")
	r.AddCookie(session)
	if token != "" {
		r.Header.Set(CSRFHeader, token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func csrfOf(t *testing.T, sessionResponse *httptest.ResponseRecorder) string {
	t.Helper()
	var doc struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(sessionResponse.Body.Bytes(), &doc); err != nil || doc.CSRFToken == "" {
		t.Fatalf("session body %q holds no csrf_token: %v", sessionResponse.Body, err)
	}
	return doc.CSRFToken
}

func TestACookieAuthenticatedWriteNeedsTheCSRFTokenAndTheSameOrigin(t *testing.T) {
	t.Parallel()
	l := newLogin(t, &fakeIdentity{claims: alice})
	h := handler(l)
	callback, cookie := startLogin(t, h)
	session := setCookie(browse(h, http.MethodGet, callback, cookie), SessionCookie)
	token := csrfOf(t, browse(h, http.MethodGet, "/auth/session", session))
	cases := map[string]struct{ token, origin string }{
		"no token":          {"", publicURL},
		"a wrong token":     {token + "x", publicURL},
		"no origin":         {token, ""},
		"a foreign origin":  {token, "https://evil.example"},
		"another scheme":    {token, "http://board.example.test"},
		"another port":      {token, "https://board.example.test:8443"},
		"the null origin":   {token, "null"},
		"neither header":    {"", ""},
		"a look-alike host": {token, "https://board.example.test.evil.example"},
	}
	for name, c := range cases {
		if rec := write(h, "/auth/logout", session, c.token, c.origin); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, rec.Code)
		}
	}
	if rec := browse(h, http.MethodGet, "/auth/session", session); rec.Code != http.StatusOK {
		t.Fatalf("a refused write ended the session: %d", rec.Code)
	}
	// A Referer never stands in for a missing Origin: a same-origin Referer
	// without an Origin is refused like no origin at all.
	r := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	r.AddCookie(session)
	r.Header.Set(CSRFHeader, token)
	r.Header.Set("Referer", publicURL+"/some/page")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a write with the token and a same-origin Referer but no Origin: %d, want 403", rec.Code)
	}
	if rec := write(h, "/auth/logout", session, token, publicURL); rec.Code != http.StatusNoContent {
		t.Fatalf("a write with the token and the same Origin: %d, want 204", rec.Code)
	}
}

func TestABearerWriteNeedsNoCSRFToken(t *testing.T) {
	t.Parallel()
	h := handler(newLogin(t, &fakeIdentity{}))
	r := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	r.Header.Set("Authorization", "Bearer brd_x")
	r.Header.Set(ActorHeader, "a")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("a key-authenticated write was held to the CSRF rule: %d", rec.Code)
	}
}
