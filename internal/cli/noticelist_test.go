package cli

import (
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// noticeItems is n notices N1..Nn, held by alternating actors, the second with resources.
func noticeItems(n int) string {
	items := make([]string, n)
	for i := range items {
		res := `[]`
		if i%2 == 1 {
			res = `["host:gw1","repo:x"]`
		}
		items[i] = fmt.Sprintf(`{"ref":"L%d","task":"aa-%d","title":"T%d","actor":"h%d","intent":"heads up %d","state":"live","resources":%s,"notice":true,"renewed_at":"2026-03-04T12:00:00Z","expires_at":"2026-03-04T12:41:00Z"}`,
			i+10, i+1, i+1, i%2, i+1, res)
	}
	return strings.Join(items, ",")
}

// nowWithNotices is the now answer of nowAnswer with the given notices array.
func nowWithNotices(notices string) string {
	body := strings.Replace(nowAnswer, "%s", "", 1)
	return strings.Replace(body, `"my_leases"`, `"notices":[`+notices+`],"my_leases"`, 1)
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := "testdata/" + name + ".golden"
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s (go test -update rewrites it)\n--- got\n%s--- want\n%s", path, got, want)
	}
}

// TestNowPrintsTheNoticesSectionFirstAsTheGolden: `vh now` and `vh start` print
// `notices:` before `my leases:`; with 0 notices `none`, with 7 five lines and
// `+2 more → vh lease ls --notice`.
func TestNowPrintsTheNoticesSectionFirstAsTheGolden(t *testing.T) {
	atTwelve(t)
	for _, c := range []struct {
		name    string
		notices int
	}{{"now-notices-0", 0}, {"now-notices-2", 2}, {"now-notices-7", 7}} {
		f := answering(200, nowWithNotices(noticeItems(c.notices)))
		srv := httptest.NewServer(f)
		code, out, errs := runEnv(f.env(srv), "now")
		srv.Close()
		if code != ExitOK || errs != "" {
			t.Fatalf("%s: got %d %q", c.name, code, errs)
		}
		checkGolden(t, c.name, out)
		if i, j := strings.Index(out, "notices:"), strings.Index(out, "my leases:"); i < 0 || i > j {
			t.Errorf("%s: notices: at %d, my leases: at %d", c.name, i, j)
		}
	}
	startBody := strings.Replace(startAnswer(false, ""), strings.Replace(nowAnswer, "%s", "", 1), nowWithNotices(noticeItems(7)), 1)
	f := answering(201, startBody)
	srv := httptest.NewServer(f)
	defer srv.Close()
	code, out, errs := runEnv(f.env(srv), "start", "aa-1", "doing it")
	if code != ExitOK || errs != "" {
		t.Fatalf("start: got %d %q", code, errs)
	}
	checkGolden(t, "start-notices-7", out)
}

// TestOldServerWithoutNoticesPrintsNoticesNone: an answer with no `notices` key
// reads as no notices.
func TestOldServerWithoutNoticesPrintsNoticesNone(t *testing.T) {
	atTwelve(t)
	f := answering(200, strings.Replace(nowAnswer, "%s", "", 1))
	srv := httptest.NewServer(f)
	defer srv.Close()
	_, out, _ := runEnv(f.env(srv), "now")
	if !strings.HasPrefix(out, "you: me\nnotices: none\nmy leases:\n") {
		t.Errorf("got %q", out)
	}
}

// TestLeaseLsNoticeAsksForLiveNotices: --notice sends notice=true with state
// live and prints the lease lines; without it no notice parameter is sent, and
// the more-line keeps --notice.
func TestLeaseLsNoticeAsksForLiveNotices(t *testing.T) {
	atTwelve(t)
	f := answering(200, `{"items":[`+noticeItems(2)+`],"next_cursor":""}`)
	srv := httptest.NewServer(f)
	defer srv.Close()
	code, out, errs := runEnv(f.env(srv), "lease", "ls", "--notice")
	if code != ExitOK || errs != "" {
		t.Fatalf("got %d %q", code, errs)
	}
	q := f.requests[0].URL.Query()
	if q.Get("notice") != "true" || q.Get("state") != "live" {
		t.Errorf("query %v", q)
	}
	want := "L10  live  aa-1  h0  \"heads up 1\"  exp 41m\nL11  live  aa-2  h1  \"heads up 2\"  res host:gw1,repo:x  exp 41m\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
	f = answering(200, `{"items":[`+noticeItems(2)+`],"next_cursor":""}`)
	srv2 := httptest.NewServer(f)
	defer srv2.Close()
	_, out, _ = runEnv(f.env(srv2), "lease", "ls", "--notice", "--limit", "1")
	if f.requests[0].URL.Query().Get("notice") != "true" || !strings.HasSuffix(out, "+1 more → vh lease ls --notice --all\n") {
		t.Errorf("got %q", out)
	}
	f = answering(200, `{"items":[],"next_cursor":""}`)
	srv3 := httptest.NewServer(f)
	defer srv3.Close()
	runEnv(f.env(srv3), "lease", "ls")
	if _, ok := f.requests[0].URL.Query()["notice"]; ok {
		t.Errorf("notice sent without --notice: %v", f.requests[0].URL.Query())
	}
}
