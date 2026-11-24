package features

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// textFileHolds writes text to a temp file a client command line names as
// <file>; a `\n` in text is a line break.
func (h *harness) textFileHolds(text string) error {
	f, err := os.CreateTemp("", "board-godog-text-")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(strings.ReplaceAll(text, `\n`, "\n")); err != nil {
		return err
	}
	h.textFile = f.Name()
	return nil
}

// standardInputHolds sets what the next client command reads from standard input.
func (h *harness) standardInputHolds(text string) error {
	if text == "" {
		return fmt.Errorf("the standard input must hold some text")
	}
	h.stdin = strings.ReplaceAll(text, `\n`, "\n")
	return nil
}

// stderrHasLines checks stderr is exactly n lines.
func (h *harness) stderrHasLines(n int) error {
	if got := strings.Count(h.stderr, "\n"); got != n || !strings.HasSuffix(h.stderr, "\n") {
		return fmt.Errorf("stderr %q has %d lines, want %d", h.stderr, got, n)
	}
	return nil
}

// backgroundClient is one board client command left running while the
// scenario goes on, and how it ended.
type backgroundClient struct {
	done     chan struct{}
	exit     int
	out, err bytes.Buffer
	failure  error
}

// startClientInBackground starts the client command without waiting for it.
func (h *harness) startClientInBackground(ctx context.Context, line, actor string) error {
	if err := h.requireKey(); err != nil {
		return err
	}
	words := splitQuoted(line)
	if len(words) == 0 || words[0] != "vh" {
		return fmt.Errorf("command %q must start with board", line)
	}
	bg := &backgroundClient{done: make(chan struct{})}
	cmd := exec.CommandContext(ctx, h.binary, words[1:]...)
	cmd.Env = h.env("BOARD_URL=http://"+h.addr, "BOARD_API_KEY="+h.key, "BOARD_ACTOR="+actor)
	cmd.Stdout, cmd.Stderr = &bg.out, &bg.err
	if err := cmd.Start(); err != nil {
		return err
	}
	h.background = bg
	go func() {
		defer close(bg.done)
		var exitErr *exec.ExitError
		switch err := cmd.Wait(); {
		case err == nil:
		case errors.As(err, &exitErr):
			bg.exit = exitErr.ExitCode()
		default:
			bg.failure = err
		}
	}()
	return nil
}

// backgroundClientEnds waits for the background client and makes its exit code
// and output the scenario's last command result.
func (h *harness) backgroundClientEnds(seconds float64) error {
	bg := h.background
	if bg == nil {
		return errors.New("no background client was started")
	}
	select {
	case <-bg.done:
	case <-time.After(wholeAndFractionalSeconds(seconds)):
		return fmt.Errorf("the background client was still running after %g seconds", seconds)
	}
	if bg.failure != nil {
		return bg.failure
	}
	h.exit, h.stdout, h.stderr = bg.exit, bg.out.String(), bg.err.String()
	return nil
}

// backgroundClientWaits fails when the background client has ended already.
func (h *harness) backgroundClientWaits() error {
	if h.background == nil {
		return errors.New("no background client was started")
	}
	select {
	case <-h.background.done:
		return fmt.Errorf("the background client ended with exit code %d, want it waiting", h.background.exit)
	default:
		return nil
	}
}

func registerCLIAskSteps(sc *godog.ScenarioContext, h *harness) {
	sc.Step(`^a text file holds "([^"]*)"$`, h.textFileHolds)
	sc.Step(`^I start the client '([^']*)' as actor "([^"]*)" in the background$`, h.startClientInBackground)
	sc.Step(`^the background client ends within (\d+(?:\.\d+)?) seconds$`, h.backgroundClientEnds)
	sc.Step(`^the background client is still waiting$`, h.backgroundClientWaits)
	sc.Step(`^stderr has (\d+) lines?$`, h.stderrHasLines)
	sc.Step(`^the standard input holds "([^"]*)"$`, h.standardInputHolds)
}
