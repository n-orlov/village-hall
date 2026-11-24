package features

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cucumber/godog"
)

// askWait is one GET /api/v1/asks/{ref}?wait=N left running while the scenario
// goes on, with the moment it started and, once it ends, how.
type askWait struct {
	started time.Time
	// wait is the length the request asked the server to hold it for.
	wait time.Duration
	// seenAtEnd is poll_seen_at as the end of the wait left it, in stored
	// milliseconds, once a step has read it.
	seenAtEnd int64
	done      chan struct{}
	status    int
	body      []byte
	took      time.Duration
	err       error
}

// startAskWait sends the wait request in the background. The seconds go into
// ?wait= exactly as the scenario wrote them, a fraction included, so the server
// and not the step decides what a wait of that length means.
func (h *harness) startAskWait(parent context.Context, seconds, ref, actor string) error {
	if err := h.requireKey(); err != nil {
		return err
	}
	n, err := strconv.ParseFloat(seconds, 64)
	if err != nil {
		return fmt.Errorf("wait of %q seconds: %w", seconds, err)
	}
	w := &askWait{started: time.Now(), wait: wholeAndFractionalSeconds(n), done: make(chan struct{})}
	h.api.wait = w
	target := fmt.Sprintf("http://%s%s?wait=%s", h.addr, h.askPath(ref, ""), seconds)
	key := h.key
	go func() {
		defer close(w.done)
		ctx, cancel := context.WithTimeout(parent, w.wait+30*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			w.err = err
			return
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Board-Actor", actor)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			w.err = err
			return
		}
		defer resp.Body.Close()
		w.status = resp.StatusCode
		w.body, w.err = io.ReadAll(resp.Body)
		w.took = time.Since(w.started)
	}()
	return nil
}

// wholeAndFractionalSeconds is the duration a step's "N seconds" names: N may be
// written with a fraction, so a step that bounds a wait can be as short as the
// scenario's own clock needs.
func wholeAndFractionalSeconds(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}

// waitEndsWithin makes the background wait's response the scenario's last
// response, failing when it has not ended within the seconds.
func (h *harness) waitEndsWithin(_ context.Context, seconds float64) error {
	w := h.api.wait
	if w == nil {
		return errors.New("no wait was started")
	}
	select {
	case <-w.done:
	case <-time.After(wholeAndFractionalSeconds(seconds)):
		return fmt.Errorf("the wait was still held after %g seconds", seconds)
	}
	if w.err != nil {
		return w.err
	}
	h.status, h.api.body = w.status, w.body
	return nil
}

// waitStillHeld fails when the background wait ended already.
func (h *harness) waitStillHeld(_ context.Context) error {
	w := h.api.wait
	if w == nil {
		return errors.New("no wait was started")
	}
	select {
	case <-w.done:
		return fmt.Errorf("the wait ended after %s with status %d, want it held", w.took, w.status)
	default:
		return nil
	}
}

// requestAskWaiting asks for the ask with ?wait=<wait>; wait is sent as written
// so that 0 and 301 are tried too.
func (h *harness) requestAskWaiting(ctx context.Context, ref, wait, actor string) error {
	path := h.askPath(ref, "") + "?wait=" + url.QueryEscape(wait)
	return h.sendTask(ctx, http.MethodGet, path, "", actor)
}

// responseWithin checks that the last request took less than the seconds.
func (h *harness) responseWithin(_ context.Context, seconds float64) error {
	if took := h.api.answeredAt.Sub(h.api.sentAt); took >= wholeAndFractionalSeconds(seconds) {
		return fmt.Errorf("the request took %s, want under %g seconds", took, seconds)
	}
	return nil
}

func (h *harness) pollSeen(ctx context.Context, ref string) (bool, error) {
	n, err := askNumber(ref)
	if err != nil {
		return false, err
	}
	db, err := h.openDB()
	if err != nil {
		return false, err
	}
	defer db.Close()
	q := `SELECT count(*) FROM asks WHERE n = ` + h.placeholder(1) + ` AND poll_seen_at IS NOT NULL`
	var c int
	if err := db.QueryRowContext(ctx, q, n).Scan(&c); err != nil {
		return false, err
	}
	return c == 1, nil
}

func (h *harness) placeholder(i int) string {
	if h.leg == "postgres" {
		return fmt.Sprintf("$%d", i)
	}
	return "?"
}

// pollSeenWithin waits for poll_seen_at of the ask to be set.
func (h *harness) pollSeenWithin(ctx context.Context, seconds float64, ref string) error {
	deadline := time.Now().Add(wholeAndFractionalSeconds(seconds))
	for {
		seen, err := h.pollSeen(ctx, ref)
		if err != nil || seen {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("poll_seen_at of %s was not set within %g seconds", ref, seconds)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *harness) pollNotSeen(ctx context.Context, ref string) error {
	seen, err := h.pollSeen(ctx, ref)
	if err == nil && seen {
		err = fmt.Errorf("poll_seen_at of %s is set, want null", ref)
	}
	return err
}

// backdatePoll makes the ask's poll_seen_at the given seconds ago: no sleep, the
// row says the time has passed.
func (h *harness) backdatePoll(ctx context.Context, ref string, seconds int) error {
	n, err := askNumber(ref)
	if err != nil {
		return err
	}
	at := time.Now().Add(-time.Duration(seconds) * time.Second).UnixMilli()
	rows, err := h.exec(ctx, `UPDATE asks SET poll_seen_at = ? WHERE n = ?`, at, n)
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("%d asks backdated for %s, want 1", rows, ref)
	}
	return nil
}

// pollSeenAt reads the ask's stored poll_seen_at in milliseconds.
func (h *harness) pollSeenAt(ctx context.Context, ref string) (int64, error) {
	n, err := askNumber(ref)
	if err != nil {
		return 0, err
	}
	db, err := h.openDB()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	q := `SELECT poll_seen_at FROM asks WHERE n = ` + h.placeholder(1) + ` AND poll_seen_at IS NOT NULL`
	var at int64
	if err := db.QueryRowContext(ctx, q, n).Scan(&at); err != nil {
		return 0, fmt.Errorf("poll_seen_at of %s: %w", ref, err)
	}
	return at, nil
}

// pollSeenWhenWaitEnded checks that the wait, which ended, wrote poll_seen_at at
// its end: no earlier than one second before the wait ran out (the start of the
// wait wrote it at least that much earlier, and no renewal falls inside a wait
// this short) and no later than the response arrived. The value is kept as the
// moment the wait ended.
func (h *harness) pollSeenWhenWaitEnded(ctx context.Context, ref string) error {
	w := h.api.wait
	if w == nil {
		return errors.New("no wait was started")
	}
	select {
	case <-w.done:
	default:
		return errors.New("the wait is still held")
	}
	at, err := h.pollSeenAt(ctx, ref)
	if err != nil {
		return err
	}
	low := w.started.Add(w.wait - time.Second).UnixMilli()
	high := w.started.Add(w.took).UnixMilli()
	if at < low || at > high {
		return fmt.Errorf("poll_seen_at of %s is %d, want the end of the wait (%d to %d)", ref, at, low, high)
	}
	w.seenAtEnd = at
	return nil
}

// clockPastWaitEnd moves the simulated clock to the seconds after the end of
// the wait: the server's clock is the real one, so poll_seen_at is set that
// much before the value the end of the wait wrote, which is the same distance.
func (h *harness) clockPastWaitEnd(ctx context.Context, seconds int, ref string) error {
	w := h.api.wait
	if w == nil || w.seenAtEnd == 0 {
		return errors.New("the end of the wait was not read yet")
	}
	n, err := askNumber(ref)
	if err != nil {
		return err
	}
	at := w.seenAtEnd - int64(seconds)*1000
	rows, err := h.exec(ctx, `UPDATE asks SET poll_seen_at = ? WHERE n = ?`, at, n)
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("%d asks moved for %s, want 1", rows, ref)
	}
	return nil
}

func registerAskWaitSteps(sc *godog.ScenarioContext, h *harness) {
	sc.Step(`^I start a wait of (\d+(?:\.\d+)?) seconds on the ask "([^"]*)" as actor "([^"]*)"$`, h.startAskWait)
	sc.Step(`^the wait ends within (\d+(?:\.\d+)?) seconds$`, h.waitEndsWithin)
	sc.Step(`^the wait is still held$`, h.waitStillHeld)
	sc.Step(`^I request the ask "([^"]*)" waiting "([^"]*)" seconds as actor "([^"]*)"$`, h.requestAskWaiting)
	sc.Step(`^the response arrived in under (\d+(?:\.\d+)?) seconds$`, h.responseWithin)
	sc.Step(`^within (\d+(?:\.\d+)?) seconds the poll of the ask "([^"]*)" is seen in the database$`, h.pollSeenWithin)
	sc.Step(`^the poll of the ask "([^"]*)" is not seen in the database$`, h.pollNotSeen)
	sc.Step(`^the poll of the ask "([^"]*)" was last seen (\d+) seconds ago$`, h.backdatePoll)
	sc.Step(`^the poll of the ask "([^"]*)" was last seen when the wait ended$`, h.pollSeenWhenWaitEnded)
	sc.Step(`^the clock is (\d+) seconds past the end of the wait on the ask "([^"]*)"$`, h.clockPastWaitEnd)
}
