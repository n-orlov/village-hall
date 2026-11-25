package cli

import (
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/help-agent.golden from the embedded protocol text")

const goldenPath = "testdata/help-agent.golden"

func TestHelpAgentPrintsTheEmbeddedProtocolAndMatchesTheGolden(t *testing.T) {
	code, out, errs := runEnv(envWith(nil), "help", "agent")
	if code != ExitOK || errs != "" {
		t.Fatalf("got %d %q", code, errs)
	}
	if !strings.HasPrefix(out, "vh protocol v6\n") {
		t.Fatalf("the text is not headed vh protocol v6: %q", firstLine(out))
	}
	if *update {
		if err := os.WriteFile(goldenPath, []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if out != string(want) {
		t.Fatalf("the text differs from %s: review the change, bump the version in the header when an agent would have to behave differently, then run go test -update\n--- got\n%s", goldenPath, out)
	}
}

func firstLine(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }

func TestHelpAgentTextFitsTheLineBudget(t *testing.T) {
	lines := strings.Split(strings.TrimSuffix(ProtocolText(), "\n"), "\n")
	if len(lines) > 45 {
		t.Errorf("the text has %d lines, the budget is 45", len(lines))
	}
	for i, l := range lines {
		if n := len([]rune(l)); n > 100 {
			t.Errorf("line %d has %d columns, the budget is 100: %q", i+1, n, l)
		}
	}
}

func TestHelpAgentTextCoversIdentityWhoamiAndInitiatives(t *testing.T) {
	text := ProtocolText()
	for _, want := range []string{"VH_URL", "VH_API_KEY", "VH_ACTOR", "VH_WAY_BACK", "`vh whoami`", "`vh init ls", "`vh task ls "} {
		if !strings.Contains(text, want) {
			t.Errorf("the text does not mention %s", want)
		}
	}
}

func TestHelpAgentTextCoversTasksLeasesConflictsAndClosing(t *testing.T) {
	text := ProtocolText()
	for _, want := range []string{"`vh task done <ref>`", "`vh task reopen <ref>`", "`vh lease ack L5`", "`vh now --resources ", "CONFLICT", "exit 7", "`vh task close <ref> --for operator`", "never close"} {
		if !strings.Contains(text, want) {
			t.Errorf("the text does not mention %s", want)
		}
	}
}

func TestHelpAgentTextDescribesNotices(t *testing.T) {
	text := ProtocolText()
	for _, want := range []string{
		"NOTICES", "outage, freeze or reboot that affects others", "`vh start <task> \"<what, until>\" --notice`",
		"`vh ping` renews it while the situation holds", "`vh finish <task> \"<outcome>\"` ends it when over", "Read `notices` at check-in",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the text does not mention %s", want)
		}
	}
}

func TestHelpAgentTextNamesOnlyCommandsAndFlagsThisBuildHas(t *testing.T) {
	if p := UnknownReferences(ProtocolText(), Commands()); len(p) != 0 {
		t.Fatalf("the text names what does not exist: %v", p)
	}
	if len(commandSpan.FindAllString(ProtocolText(), -1)) < 5 {
		t.Fatal("the text names almost no command, so the check proves nothing")
	}
}

func TestUnknownReferencesFindsAnUnknownCommandAndAnUnknownFlag(t *testing.T) {
	text := "run `vh frobnicate <task>` or `vh init ls --nope` or `vh whoami` or `vh init ls --all`"
	got := UnknownReferences(text, Commands())
	if len(got) != 2 || !strings.Contains(got[0], `unknown command "vh frobnicate"`) || !strings.Contains(got[1], "unknown flag --nope") {
		t.Fatalf("got %v", got)
	}
}

func TestCommandsListsTheFlagsOfEachCommand(t *testing.T) {
	tree := Commands()
	if got := strings.Join(tree["help agent"], " "); got != "--version" {
		t.Errorf("help agent flags %q", got)
	}
	if got := strings.Join(tree["init ls"], " "); got != "--all --closed-within --json --limit --status --tag" {
		t.Errorf("init ls flags %q", got)
	}
	if _, ok := tree["whoami"]; !ok {
		t.Error("whoami is not in the tree")
	}
}

func TestProtocolVersionIsTheNumberInTheHeader(t *testing.T) {
	if v := ProtocolVersion(); v != 6 {
		t.Fatalf("version %d", v)
	}
	defer func(old string) { protocolText = old }(protocolText)
	for text, want := range map[string]int{"vh protocol v12\nx": 12, "protocol v1\n": -1, "board protocol v3\n": -1, "vh protocol v\n": -1, "": -1, "vh protocol v99999999999999999999\n": -1} {
		protocolText = text
		if got := ProtocolVersion(); got != want {
			t.Errorf("%q gave %d, want %d", text, got, want)
		}
	}
}

func TestHelpAgentVersionPrintsJustTheNumberAndRecordsNothing(t *testing.T) {
	f := &fakeBoard{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	code, out, errs := runEnv(f.env(srv), "help", "agent", "--version")
	if code != ExitOK || out != "6\n" || errs != "" || len(f.requests) != 0 {
		t.Fatalf("got %d %q %q with %d requests", code, out, errs, len(f.requests))
	}
}

func TestHelpAgentRecordsTheVersionAsRead(t *testing.T) {
	f := &fakeBoard{handler: func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"actor":"claude:test","protocol_seen":0}`))
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	code, out, errs := runEnv(f.env(srv), "help", "agent")
	if code != ExitOK || out != ProtocolText() || errs != "" {
		t.Fatalf("got %d %q", code, errs)
	}
	if len(f.requests) != 1 {
		t.Fatalf("%d requests, want 1", len(f.requests))
	}
	r := f.requests[0]
	if r.Method != http.MethodPut || r.URL.Path != "/api/v1/actors/me/protocol-seen" || f.bodies[0] != `{"version":6}` {
		t.Fatalf("request %s %s %q", r.Method, r.URL.Path, f.bodies[0])
	}
	if r.Header.Get("Board-Actor") != "claude:test" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer brd_") {
		t.Fatalf("headers %v", r.Header)
	}
}

func TestHelpAgentStillPrintsWhenTheServerCannotBeUsed(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer refused.Close()
	for name, env := range map[string]Env{
		"unreachable": envWith(map[string]string{"BOARD_URL": url, "BOARD_API_KEY": "brd_x", "BOARD_ACTOR": "a"}),
		"no key":      envWith(map[string]string{"BOARD_URL": url}),
		"no url":      envWith(map[string]string{"BOARD_API_KEY": "brd_x"}),
		"key refused": envWith(map[string]string{"BOARD_URL": refused.URL, "BOARD_API_KEY": "brd_x", "BOARD_ACTOR": "a"}),
	} {
		code, out, errs := runEnv(env, "help", "agent")
		if code != ExitOK || out != ProtocolText() || errs != "" {
			t.Errorf("%s: got %d %q %q", name, code, out, errs)
		}
	}
}

func TestHelpUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"help", "other"}, {"help", "agent", "--bogus"}, {"help", "agent", "extra"}, {"help", "agent", "--version=1"}} {
		code, out, errs := runEnv(envWith(nil), args...)
		if code != ExitUsage || out != "" || errs != HelpAgentUsage+"\n" {
			t.Errorf("%v: got %d %q %q", args, code, out, errs)
		}
	}
}

func TestHelpAgentTextCoversAsksNotesAndAttention(t *testing.T) {
	text := ProtocolText()
	for _, want := range []string{"`vh block <task> ", "(--stop, the default)", "--poll 10m", "exit 0 answered", "3 still", "`vh answer ", "--relay", "`vh note ", "`vh catchup`", "`vh attention`", "`vh note show "} {
		if !strings.Contains(text, want) {
			t.Errorf("the text does not mention %s", want)
		}
	}
}

func TestHelpAgentTextIsWrittenAroundTheShortcutCommands(t *testing.T) {
	text := ProtocolText()
	for _, want := range []string{
		"`vh start <task> \"<intent>\" --res host-a --ttl 2h`", "`vh new <slug> \"<title>\" --start \"<intent>\"`",
		"`vh ping \"<progress>\"`", "`vh finish \"<outcome>\" --done`", "`vh block <task> \"<question>\"`", "`vh catchup`",
		"`vh now --resources host-a`", "`vh task reopen <ref>` first", "`vh task done --force` only when the operator says so",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the text does not mention %s", want)
		}
	}
	for _, gone := range []string{"BOARD_", "board protocol", "`vh lease start", "`vh notes "} {
		if strings.Contains(text, gone) {
			t.Errorf("the text still mentions %s", gone)
		}
	}
}
