package cli

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const nowAnswer = `{"actor":"me","my_leases":[{"ref":"L1","task":"aa-1","title":"Wire it","actor":"me","intent":"wiring","state":"live","resources":["repo:x","host:y"],"renewed_at":"2026-03-04T12:00:00Z","expires_at":"2026-03-04T13:52:00Z"}],"others_leases":[{"ref":"L2","task":"aa-2","actor":"you","intent":"testing","state":"live","resources":[],"renewed_at":"2026-03-04T12:00:00Z","expires_at":"2026-03-04T12:41:00Z"}],"conflicts":[%s],"my_asks":[{"ref":"A1","task":"aa-1","asker":"me","question":"which one?","mode":"stop","state":"open","created_at":"2026-03-04T11:57:00Z"},{"ref":"A2","task":"aa-1","asker":"me","question":"ready?","mode":"stop","state":"answered","answer":"yes\nnow","answered_by":"you","answered_at":"2026-03-04T11:59:00Z","created_at":"2026-03-04T11:00:00Z"}],"notes":[{"ref":"N4","author":"you","summary":"gw1 reboots","resource":"host:gw1","has_body":true,"broadcast":false,"task":"aa-2","created_at":"2026-03-04T11:50:00Z"}],"notes_more":3,"attention":{"open_asks":2,"done_not_closed":3,"expired_leases":1,"overdue_watches":0,"conflicts":1,"total":7}}`

const nowConflict = `{"lease":"L2","holder":"you","task":"aa-2","intent":"testing","expires_at":"2026-03-04T12:41:00Z","resources":["repo:x"]}`

func TestNowTextListsTheThreeSections(t *testing.T) {
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	f := answering(200, strings.Replace(nowAnswer, "%s", nowConflict, 1))
	srv := httptest.NewServer(f)
	defer srv.Close()
	code, out, errs := runEnv(f.env(srv), "now", "--resources", "repo:x", "--resources=host:z")
	want := "you: me\nnotices: none\nmy leases:\n  L1  aa-1  Wire it  \"wiring\"  res repo:x,host:y  exp 1h52m\nothers:\n" +
		"  L2  aa-2  you  \"testing\"  exp 41m\nconflicts:\n  CONFLICT repo:x: L2 you (aa-2) \"testing\" exp 41m\n" +
		"my asks:\n  A1  open  aa-1  me  \"which one?\"  asked 3m ago\n" +
		"  ANSWERED A2  answered  aa-1  me  \"ready?\"  answered by you 1m ago: \"yes now\"\n" +
		"notes:\n  N4  10m ago  you  aa-2  [host:gw1]  gw1 reboots  +\n  +3 more → vh notes --since last\n" +
		"attention: asks 2 · done 3 · expired 1 · overdue 0 · conflicts 1\n"
	if code != ExitWarning || errs != "" {
		t.Fatalf("got %d %q", code, errs)
	}
	if got := f.requests[0].URL.RawQuery; got != "resources=repo%3Ax%2Chost%3Az" {
		t.Errorf("query %q", got)
	}
	// The times shown depend on the clock the command ran with, so compare the fixed parts.
	if !strings.HasPrefix(out, "you: me\nnotices: none\nmy leases:\n  L1  aa-1  Wire it  \"wiring\"  res repo:x,host:y  exp ") {
		t.Errorf("got %q", out)
	}
	var v nowView
	mustDecodeNow(t, strings.Replace(nowAnswer, "%s", nowConflict, 1), &v)
	if got := nowText(v, now); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNowWithNoConflictExitsZeroAndJSONIsTheAnswer(t *testing.T) {
	body := strings.Replace(nowAnswer, "%s", "", 1)
	f := answering(200, body)
	srv := httptest.NewServer(f)
	defer srv.Close()
	code, out, errs := runEnv(f.env(srv), "now", "--json")
	if code != ExitOK || errs != "" || out != body+"\n" {
		t.Errorf("got %d %q %q", code, out, errs)
	}
	if got := f.requests[0].URL.RawQuery; got != "" {
		t.Errorf("query %q", got)
	}
	f = answering(200, strings.Replace(nowAnswer, "%s", nowConflict, 1))
	srv2 := httptest.NewServer(f)
	defer srv2.Close()
	if code, _, _ := runEnv(f.env(srv2), "now", "--json"); code != ExitWarning {
		t.Errorf("--json with a conflict exits %d", code)
	}
}

func TestNowRefusalsAndUsage(t *testing.T) {
	f := answering(400, `{"title":"x","detail":"bad resource","next":"fix it"}`)
	srv := httptest.NewServer(f)
	defer srv.Close()
	if code, out, errs := runEnv(f.env(srv), "now", "--resources", "a"); code != ExitError || out != "" || errs != "bad resource → fix it\n" {
		t.Errorf("got %d %q %q", code, out, errs)
	}
	for _, args := range [][]string{{"now", "extra"}, {"now", "--bogus"}, {"now", "--resources"}} {
		if code, _, errs := runEnv(f.env(srv), args...); code != ExitUsage || errs != NowUsage+"\n" {
			t.Errorf("%v: got %d %q", args, code, errs)
		}
	}
	bad := answering(200, `[1]`)
	srv2 := httptest.NewServer(bad)
	defer srv2.Close()
	if code, _, _ := runEnv(bad.env(srv2), "now"); code != ExitError {
		t.Errorf("an unreadable answer exits %d", code)
	}
	if code, _, _ := runEnv(envWith(nil), "now"); code == ExitOK {
		t.Error("no key must not exit 0")
	}
}

func TestNowSectionCapsAtFive(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e", "f", "g"}
	want := "others:\n  a\n  b\n  c\n  d\n  e\n  +2 more → vh lease ls\n"
	if got := nowSection("others", lines, 0, "vh lease ls"); got != want {
		t.Errorf("got %q", got)
	}
	if got := nowSection("others", lines[:5], 0, "x"); strings.Contains(got, "more") {
		t.Errorf("five lines need no more line: %q", got)
	}
	if got := nowSection("conflicts", nil, 0, "x"); got != "conflicts: none\n" {
		t.Errorf("got %q", got)
	}
}

func mustDecodeNow(t *testing.T, s string, v *nowView) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatal(err)
	}
}

func TestNowTextWithNothingNewStillPrintsTheAttentionLine(t *testing.T) {
	var v nowView
	mustDecodeNow(t, `{"actor":"me","my_leases":[],"others_leases":[],"conflicts":[],"my_asks":[],"notes":[],"notes_more":0,"attention":{}}`, &v)
	want := "you: me\nnotices: none\nmy leases: none\nothers: none\nconflicts: none\nmy asks: none\nnotes: none\n" +
		"attention: asks 0 · done 0 · expired 0 · overdue 0 · conflicts 0\n"
	if got := nowText(v, time.Now()); got != want {
		t.Errorf("got %q", got)
	}
}

func TestNowSectionCountsTheItemsTheServerLeftOut(t *testing.T) {
	want := "notes:\n  a\n  +4 more → vh notes --since last\n"
	if got := nowSection("notes", []string{"a"}, 4, "vh notes --since last"); got != want {
		t.Errorf("got %q", got)
	}
	over := []string{"a", "b", "c", "d", "e", "f"}
	if got := nowSection("notes", over, 2, "x"); !strings.HasSuffix(got, "  +3 more → x\n") {
		t.Errorf("got %q", got)
	}
}
