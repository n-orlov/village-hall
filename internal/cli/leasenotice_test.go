package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNoticeFlagSendsNoticeTrueOnBothStarts: --notice is sent as `notice: true`
// by `vh lease start` and by `vh start`, and left out of the body without it.
func TestNoticeFlagSendsNoticeTrueOnBothStarts(t *testing.T) {
	atTwelve(t)
	for _, c := range []struct {
		name   string
		args   []string
		answer string
		path   string
	}{
		{"lease start", []string{"lease", "start", "aa-1", "heads up"}, leaseAnswer, "/api/v1/leases"},
		{"start", []string{"start", "aa-1", "heads up"}, startAnswer(false, ""), "/api/v1/start"},
	} {
		f := answering(201, c.answer)
		code, _, body := sentBody(t, f, append(append([]string{}, c.args...), "--notice")...)
		if r := f.requests[0]; code != ExitOK || r.URL.Path != c.path || body["notice"] != true {
			t.Errorf("%s --notice: %d %s body %v, want notice true", c.name, code, r.URL.Path, body)
		}
		f = answering(201, c.answer)
		if code, _, body := sentBody(t, f, c.args...); code != ExitOK || body["notice"] != nil {
			t.Errorf("%s without --notice: %d body %v, want no notice field", c.name, code, body)
		}
	}
}

// TestOtherKindRefusalExitsSixWithTheEndToRunFirst: the server's 409 for a live
// lease of the other kind exits 6 on both starts, printing its detail and next.
func TestOtherKindRefusalExitsSixWithTheEndToRunFirst(t *testing.T) {
	const detail = "you already hold L5 on task aa-1, an ordinary lease, and cannot start a notice on it as well"
	const next = "Run vh lease end L5 first, then start the new lease."
	want := detail + " → " + next + "\n"
	for _, args := range [][]string{
		{"lease", "start", "aa-1", "x", "--notice"},
		{"start", "aa-1", "x", "--notice"},
	} {
		f := answering(http.StatusConflict, `{"detail":"`+detail+`","next":"`+next+`"}`)
		srv := httptest.NewServer(f)
		code, out, errs := runEnv(f.env(srv), args...)
		srv.Close()
		if code != ExitConflict || ExitConflict != 6 || out != "" || errs != want {
			t.Errorf("vh %v: got %d %q %q, want 6 %q", args, code, out, errs, want)
		}
	}
}
