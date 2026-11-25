package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func startAnswer(created bool, conflicts string) string {
	made := "false"
	if created {
		made = "true"
	}
	now := strings.Replace(nowAnswer, "%s", "", 1)
	return `{"lease":{"ref":"L1","task":"aa-1","actor":"me","intent":"i","state":"live","resources":[],"renewed_at":"2026-03-04T12:00:00Z","expires_at":"2026-03-04T14:00:00Z"},"conflicts":[` +
		conflicts + `],"task_created":` + made + `,"now":` + now + `,"protocol":6,"protocol_seen":6}`
}

const startNowText = "you: me\nnotices: none\nmy leases:\n  L1  aa-1  Wire it  \"wiring\"  res repo:x,host:y  exp 1h52m\nothers:\n" +
	"  L2  aa-2  you  \"testing\"  exp 41m\nconflicts: none\n" +
	"my asks:\n  A1  open  aa-1  me  \"which one?\"  asked 3m ago\n" +
	"  ANSWERED A2  answered  aa-1  me  \"ready?\"  answered by you 1m ago: \"yes now\"\n" +
	"notes:\n  N4  10m ago  you  aa-2  [host:gw1]  gw1 reboots  +\n  +3 more → vh notes --since last\n" +
	"attention: asks 2 · done 3 · expired 1 · overdue 0 · conflicts 1\n"

func atTwelve(t *testing.T) {
	t.Helper()
	old := nowFn
	nowFn = func() time.Time { return time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { nowFn = old })
}

func TestStartSendsOneRequestAndPrintsTheLeaseLineThenTheNowSummary(t *testing.T) {
	atTwelve(t)
	f := answering(201, startAnswer(false, ""))
	code, out, body := sentBody(t, f, "start", "AA-1", "doing it", "--ttl", "4h", "--res", "repo:x", "--res=host:y")
	r := f.requests[0]
	if code != ExitOK || r.Method != "POST" || r.URL.Path != "/api/v1/start" {
		t.Fatalf("got %d %s %s", code, r.Method, r.URL.Path)
	}
	if want := "ok lease L1 aa-1 exp 2h\n" + startNowText; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	res, _ := body["resources"].([]any)
	if body["task"] != "AA-1" || body["intent"] != "doing it" || body["ttl_seconds"] != float64(14400) || len(res) != 2 || body["new_task"] != nil || len(body) != 4 {
		t.Errorf("body %v", body)
	}
}

func TestStartWithAConflictPrintsItBetweenTheLeaseAndTheSummaryAndExitsSeven(t *testing.T) {
	atTwelve(t)
	conflict := `{"lease":"L5","holder":"h","task":"bb-1","intent":"go","expires_at":"2026-03-04T12:41:00Z","resources":["a:b"]}`
	f := answering(201, startAnswer(false, conflict))
	srv := httptest.NewServer(f)
	defer srv.Close()
	code, out, errs := runEnv(f.env(srv), "start", "aa-1", "x")
	want := "ok lease L1 aa-1 exp 2h\nCONFLICT a:b: L5 h (bb-1) \"go\" exp 41m\n" + startNowText
	if code != ExitWarning || out != want || errs != "" {
		t.Errorf("got %d %q %q, want 7 %q", code, out, errs, want)
	}
}

func TestStartJSONPrintsTheAnswerAsItCame(t *testing.T) {
	answer := startAnswer(false, "")
	f := answering(200, answer)
	srv := httptest.NewServer(f)
	defer srv.Close()
	if code, out, errs := runEnv(f.env(srv), "start", "aa-1", "x", "--json"); code != ExitOK || out != answer+"\n" || errs != "" {
		t.Errorf("got %d %q %q", code, out, errs)
	}
}

func TestNewSendsTheNewTaskAndPrintsTheTaskLineTheLeaseLineAndTheSummary(t *testing.T) {
	atTwelve(t)
	f := answering(201, startAnswer(true, ""))
	code, out, body := sentBody(t, f, "new", "aa", "Fresh work", "--start", "doing it", "--ttl=30m", "--res", "repo:z")
	r := f.requests[0]
	if code != ExitOK || r.Method != "POST" || r.URL.Path != "/api/v1/start" {
		t.Fatalf("got %d %s %s", code, r.Method, r.URL.Path)
	}
	if want := "ok task aa-1 created\nok lease L1 aa-1 exp 2h\n" + startNowText; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	nt, _ := body["new_task"].(map[string]any)
	res, _ := body["resources"].([]any)
	if nt["initiative"] != "aa" || nt["title"] != "Fresh work" || len(nt) != 2 || body["intent"] != "doing it" ||
		body["ttl_seconds"] != float64(1800) || len(res) != 1 || body["task"] != nil || len(body) != 4 {
		t.Errorf("body %v", body)
	}
}

func TestNewWithAConflictExitsSevenAfterTheTaskLine(t *testing.T) {
	atTwelve(t)
	conflict := `{"lease":"L5","holder":"h","task":"bb-1","intent":"go","expires_at":"2026-03-04T12:41:00Z","resources":["a:b"]}`
	f := answering(201, startAnswer(true, conflict))
	srv := httptest.NewServer(f)
	defer srv.Close()
	code, out, _ := runEnv(f.env(srv), "new", "aa", "t", "--start", "x")
	if !strings.HasPrefix(out, "ok task aa-1 created\nok lease L1 aa-1 exp 2h\nCONFLICT a:b: L5 h") || code != ExitWarning {
		t.Errorf("got %d %q", code, out)
	}
}

func TestStartAndNewRefusalsWordTheirNext(t *testing.T) {
	for _, c := range []struct {
		args   []string
		status int
		detail string
		code   int
		want   string
	}{
		{[]string{"start", "AA-1", "x"}, http.StatusNotFound, "no task", ExitNotFound, "no task → vh task ls\n"},
		{[]string{"start", "AA-1", "x"}, http.StatusConflict, "cannot start a lease on task aa-1: it is closed", ExitConflict, "cannot start a lease on task aa-1: it is closed → vh task show aa-1\n"},
		{[]string{"start", "AA-1", "x"}, http.StatusConflict, "cannot start a lease on task aa-1: it is done", ExitConflict, "cannot start a lease on task aa-1: it is done → vh task reopen aa-1 first\n"},
		{[]string{"start", "AA-1", "x"}, http.StatusBadRequest, "bad", ExitError, "bad\n"},
		{[]string{"new", "bb", "t", "--start", "x"}, http.StatusNotFound, "there is no initiative bb", ExitNotFound, "there is no initiative bb → vh init ls\n"},
		{[]string{"new", "bb", "t", "--start", "x"}, http.StatusConflict, "the initiative bb is closed", ExitConflict, "the initiative bb is closed → vh init show bb\n"},
		{[]string{"new", "bb", "t", "--start", "x"}, http.StatusBadRequest, "bad", ExitError, "bad\n"},
	} {
		f := answering(c.status, `{"detail":"`+c.detail+`"}`)
		srv := httptest.NewServer(f)
		code, out, errs := runEnv(f.env(srv), c.args...)
		srv.Close()
		if code != c.code || out != "" || errs != c.want {
			t.Errorf("%v %d: got %d %q %q, want %d %q", c.args, c.status, code, out, errs, c.code, c.want)
		}
	}
}

func TestStartAndNewMalformedLinesAreUsageErrorsThatSendNothing(t *testing.T) {
	f := answering(201, startAnswer(false, ""))
	srv := httptest.NewServer(f)
	defer srv.Close()
	for _, c := range []struct {
		args  []string
		usage string
	}{
		{[]string{"start"}, StartUsage}, {[]string{"start", "aa-1"}, StartUsage}, {[]string{"start", "aa-1", "x", "y"}, StartUsage},
		{[]string{"start", "aa-1", "x", "--start", "y"}, StartUsage}, {[]string{"start", "aa-1", "x", "--ttl"}, StartUsage},
		{[]string{"new"}, NewUsage}, {[]string{"new", "aa"}, NewUsage}, {[]string{"new", "aa", "t"}, NewUsage},
		{[]string{"new", "aa", "t", "--start"}, NewUsage}, {[]string{"new", "aa", "t", "--start", ""}, NewUsage},
		{[]string{"new", "aa", "t", "u", "--start", "x"}, NewUsage}, {[]string{"new", "aa", "t", "--start", "x", "--no-such"}, NewUsage},
	} {
		code, out, errs := runEnv(f.env(srv), c.args...)
		if code != ExitUsage || out != "" || errs != c.usage+"\n" {
			t.Errorf("%v: got %d %q %q", c.args, code, out, errs)
		}
	}
	for _, args := range [][]string{{"start", "aa-1", "x", "--ttl", "2"}, {"new", "aa", "t", "--start", "x", "--ttl", "2"}} {
		if code, out, errs := runEnv(f.env(srv), args...); code != ExitUsage || out != "" || !strings.Contains(errs, `invalid duration "2"`) {
			t.Errorf("%v: got %d %q %q", args, code, out, errs)
		}
	}
	if len(f.requests) != 0 {
		t.Errorf("%d requests were sent for malformed command lines", len(f.requests))
	}
}

func TestStartWithAnUnreadableAnswerOrNoServerFails(t *testing.T) {
	f := answering(201, `{"lease": "not an object"}`)
	srv := httptest.NewServer(f)
	defer srv.Close()
	if code, out, _ := runEnv(f.env(srv), "start", "aa-1", "x"); code == ExitOK || out != "" {
		t.Errorf("unreadable answer: got %d %q", code, out)
	}
	none := func(string) string { return "" }
	if code, _, _ := runEnv(none, "start", "aa-1", "x"); code == ExitOK {
		t.Error("start with no server configured succeeded")
	}
}

func TestStartCommandsAreInTheTreeWithTheirFlags(t *testing.T) {
	tree := Commands()
	for cmd, want := range map[string]string{"start": "--json,--notice,--res,--ttl", "new": "--json,--res,--start,--tag,--ttl"} {
		got, ok := tree[cmd]
		if !ok || strings.Join(got, ",") != want {
			t.Errorf("Commands()[%q] = %v, want %s", cmd, got, want)
		}
	}
}

func TestProtocolHintPicksTheServerHintBeforeTheStaleReadingHint(t *testing.T) {
	const (
		serverNewer = "hint: vh protocol v7 on the server, you have v6 → vh update, then vh help agent\n"
		seenOlder   = "hint: vh protocol v6 changed since you last read it → vh help agent\n"
	)
	for name, c := range map[string]struct {
		server, seen, client int
		want                 string
	}{
		"server newer":                {7, 6, 6, serverNewer},
		"server newer, never read":    {7, 0, 6, serverNewer},
		"read an older text":          {6, 5, 6, seenOlder},
		"never read it":               {6, 0, 6, seenOlder},
		"server older, read an older": {5, 4, 6, seenOlder},
		"all current":                 {6, 6, 6, ""},
		"server older, all read":      {5, 6, 6, ""},
		"read a newer text":           {6, 7, 6, ""},
	} {
		if got := protocolHint(c.server, c.seen, c.client); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestStartAndNewPrintTheHintAfterTheSummaryAndKeepTheExitCode(t *testing.T) {
	atTwelve(t)
	conflict := `{"lease":"L5","holder":"h","task":"bb-1","intent":"go","expires_at":"2026-03-04T12:41:00Z","resources":["a:b"]}`
	for name, args := range map[string][]string{"start": {"start", "aa-1", "x"}, "new": {"new", "aa", "T", "--start", "x"}} {
		answer := strings.Replace(startAnswer(name == "new", conflict), `"protocol_seen":6`, `"protocol_seen":0`, 1)
		f := answering(201, answer)
		srv := httptest.NewServer(f)
		code, out, errs := runEnv(f.env(srv), args...)
		srv.Close()
		hint := "hint: vh protocol v6 changed since you last read it → vh help agent\n"
		if code != ExitWarning || !strings.HasSuffix(out, startNowText+hint) || errs != "" {
			t.Errorf("%s: got %d %q %q", name, code, out, errs)
		}
	}
}

func TestStartJSONCarriesNoHintLine(t *testing.T) {
	answer := strings.Replace(startAnswer(false, ""), `"protocol_seen":6`, `"protocol_seen":0`, 1)
	f := answering(200, answer)
	srv := httptest.NewServer(f)
	defer srv.Close()
	if code, out, _ := runEnv(f.env(srv), "start", "aa-1", "x", "--json"); code != ExitOK || out != answer+"\n" {
		t.Errorf("got %d %q", code, out)
	}
}

func TestTheHintWorksAcrossTheV5ToV6Bump(t *testing.T) {
	if ProtocolVersion() != 6 {
		t.Fatalf("this test is about the bump to v6, the binary embeds v%d", ProtocolVersion())
	}
	if got := protocolHint(6, 5, ProtocolVersion()); got != "hint: vh protocol v6 changed since you last read it \u2192 vh help agent\n" {
		t.Errorf("an actor whose protocol-seen is 5 got %q, want the read-again hint", got)
	}
	if got := protocolHint(6, 6, ProtocolVersion()); got != "" {
		t.Errorf("an actor whose protocol-seen is 6 got %q, want no hint", got)
	}
}
