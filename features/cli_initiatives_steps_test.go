package features

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/cucumber/godog"
)

// splitQuoted splits a command line into words; a word may be wrapped in
// double quotes to hold spaces.
func splitQuoted(line string) []string {
	var words []string
	var cur strings.Builder
	inQuote, started := false, false
	for _, r := range line {
		switch {
		case r == '"':
			inQuote, started = !inQuote, true
		case r == ' ' && !inQuote:
			if started {
				words, cur, started = append(words, cur.String()), strings.Builder{}, false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if started {
		words = append(words, cur.String())
	}
	return words
}

// runClient runs a client command against the running server with the
// remembered key. A nil actor leaves BOARD_ACTOR unset; a nil wayBack leaves
// BOARD_WAY_BACK unset.
func (h *harness) runClient(ctx context.Context, line string, actor, wayBack *string) error {
	return h.runClientNamed(ctx, line, actor, wayBack, "BOARD_", "")
}

// runClientNamed is runClient with the variables spelled under prefix (BOARD_
// or VH_), so a scenario can run the same command with only one family set.
// A non-empty task also sets <prefix>TASK, a variable the binary does not
// read, to show that an unrelated one in the same family changes nothing.
func (h *harness) runClientNamed(ctx context.Context, line string, actor, wayBack *string, prefix, task string) error {
	if err := h.requireKey(); err != nil {
		return err
	}
	words := splitQuoted(strings.ReplaceAll(strings.ReplaceAll(line, "<event>", h.eventRef), "<file>", h.textFile))
	if len(words) == 0 || words[0] != "vh" {
		return fmt.Errorf("command %q must start with board", line)
	}
	extra := []string{prefix + "URL=http://" + h.addr, prefix + "API_KEY=" + h.key}
	if actor != nil {
		extra = append(extra, prefix+"ACTOR="+*actor)
	}
	if wayBack != nil {
		extra = append(extra, prefix+"WAY_BACK="+*wayBack)
	}
	if task != "" {
		extra = append(extra, prefix+"TASK="+task)
	}
	extra = append(extra, h.pollSecondEnv()...)
	return h.runArgs(ctx, words[1:], extra...)
}

// runClientOnlyFamily runs the command with BOARD_URL, BOARD_API_KEY,
// BOARD_ACTOR, BOARD_WAY_BACK and BOARD_TASK, or with their VH_ equivalents,
// and with no variable of the other family.
func (h *harness) runClientOnlyFamily(ctx context.Context, line, actor, wayBack, family, task string) error {
	return h.runClientNamed(ctx, line, &actor, &wayBack, family, task)
}

func (h *harness) runClientAsActor(ctx context.Context, line, actor string) error {
	return h.runClient(ctx, line, &actor, nil)
}

func (h *harness) runClientWithWayBack(ctx context.Context, line, actor, wayBack string) error {
	return h.runClient(ctx, line, &actor, &wayBack)
}

func (h *harness) runClientWithNoActor(ctx context.Context, line string) error {
	return h.runClient(ctx, line, nil, nil)
}

func (h *harness) stdoutIs(doc *godog.DocString) error {
	if want := strings.TrimSpace(doc.Content) + "\n"; h.stdout != want {
		return fmt.Errorf("stdout %q, want %q", h.stdout, want)
	}
	return nil
}

func (h *harness) stdoutLineMatches(n int, pattern string) error {
	lines := strings.Split(strings.TrimSuffix(h.stdout, "\n"), "\n")
	if n < 1 || n > len(lines) {
		return fmt.Errorf("stdout %q has %d lines, want a line %d", h.stdout, len(lines), n)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	if !re.MatchString(lines[n-1]) {
		return fmt.Errorf("stdout line %d %q does not match %q", n, lines[n-1], pattern)
	}
	return nil
}

// stdoutJSONField checks stdout is exactly one JSON object whose string member name is value.
func (h *harness) stdoutJSONField(name, value string) error {
	var obj map[string]any
	if err := json.Unmarshal([]byte(h.stdout), &obj); err != nil || strings.Count(h.stdout, "\n") != 1 {
		return fmt.Errorf("stdout %q is not exactly one JSON object line", h.stdout)
	}
	if got, _ := obj[name].(string); got != value {
		return fmt.Errorf("stdout member %q is %v, want %q", name, obj[name], value)
	}
	return nil
}

func (h *harness) stderrIs(want string) error {
	if h.stderr != want+"\n" {
		return fmt.Errorf("stderr %q, want %q", h.stderr, want+"\n")
	}
	return nil
}

func (h *harness) outputLacksKey() error {
	if h.key == "" {
		return fmt.Errorf("no key was remembered")
	}
	if strings.Contains(h.stdout+h.stderr, h.key) {
		return fmt.Errorf("the output holds the key: stdout %q stderr %q", h.stdout, h.stderr)
	}
	return nil
}

func registerCLIInitiativeSteps(sc *godog.ScenarioContext, h *harness) {
	sc.Step(`^I run the client '([^']*)' as actor "([^"]*)"$`, h.runClientAsActor)
	sc.Step(`^I run the client '([^']*)' as actor "([^"]*)" with way back "([^"]*)"$`, h.runClientWithWayBack)
	sc.Step(`^I run the client '([^']*)' with no actor$`, h.runClientWithNoActor)
	sc.Step(`^I run the client '([^']*)' as actor "([^"]*)" with way back "([^"]*)" setting only (BOARD_|VH_) variables including a task "([^"]*)"$`, h.runClientOnlyFamily)
	sc.Step(`^stdout is:$`, h.stdoutIs)
	sc.Step(`^stdout is one JSON object whose "([^"]*)" is "([^"]*)"$`, h.stdoutJSONField)
	sc.Step(`^stdout line (\d+) matches "([^"]*)"$`, h.stdoutLineMatches)
	sc.Step(`^stderr is exactly "([^"]*)"$`, h.stderrIs)
	sc.Step(`^neither stdout nor stderr contains the remembered key$`, h.outputLacksKey)
}
