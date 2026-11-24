package features

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// maxWait bounds what a scenario may wait for: the expiry scenarios configure
// short lifetimes and sleep for them, and no wait is longer than this.
const maxWait = 5 * time.Second

// readsCSRFToken fetches the session's CSRF token the way the UI does.
func (h *harness) readsCSRFToken(ctx context.Context, path string) error {
	if err := h.send(ctx, http.MethodGet, path, "application/json", nil); err != nil {
		return err
	}
	var doc struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal([]byte(h.web.body), &doc); err != nil || doc.CSRFToken == "" {
		return fmt.Errorf("GET %s answered %d %q: no csrf_token", path, h.status, h.web.body)
	}
	h.web.csrf = doc.CSRFToken
	return nil
}

// browserPosts sends a write with or without the CSRF token and with the
// board's own Origin, a named one, or none.
func (h *harness) browserPosts(ctx context.Context, path, tokenClause, origin, named string) error {
	headers := map[string]string{}
	if strings.HasPrefix(tokenClause, "the session") {
		if h.web.csrf == "" {
			return errors.New("the browser has not read a CSRF token")
		}
		headers["X-CSRF-Token"] = h.web.csrf
	}
	switch {
	case origin == "the board's own Origin":
		headers["Origin"] = "http://" + h.addr
	case named != "":
		headers["Origin"] = named
	}
	return h.send(ctx, http.MethodPost, path, "application/json", headers)
}

// browserPostsBody sends a write with a JSON body, the session's CSRF token and
// the board's own Origin. Single quotes in body stand for double quotes.
func (h *harness) browserPostsBody(ctx context.Context, path, body string) error {
	if h.web.csrf == "" {
		return errors.New("the browser has not read a CSRF token")
	}
	headers := map[string]string{"X-CSRF-Token": h.web.csrf, "Origin": "http://" + h.addr}
	return h.sendBody(ctx, http.MethodPost, path, "application/json", headers, strings.ReplaceAll(body, "'", `"`))
}

// sessionLastSeen backdates the last-seen time of every session row, so the
// next use of it is due to write a new one (or, past the idle timeout, finds
// it dead) without a wait.
func (h *harness) sessionLastSeen(ctx context.Context, hours int) error {
	at := time.Now().Add(-time.Duration(hours) * time.Hour).UnixMilli()
	n, err := h.exec(ctx, `UPDATE sessions SET last_seen_at = ?`, at)
	if err == nil && n != 1 {
		err = fmt.Errorf("%d sessions backdated, want 1", n)
	}
	return err
}

// sessionSeenRecently requires the session row's last-seen time to be within
// the last minute: a successful request wrote it.
func (h *harness) sessionSeenRecently(ctx context.Context) error {
	since := time.Now().Add(-time.Minute).UnixMilli()
	got, err := h.scalar(ctx, `SELECT count(*) FROM sessions WHERE last_seen_at >= ?`, since)
	if err == nil && got != "1" {
		err = fmt.Errorf("%s sessions were seen in the last minute, want 1", got)
	}
	return err
}

func (h *harness) browserCallsAPI(ctx context.Context, path string) error {
	return h.send(ctx, http.MethodGet, path, "application/json", nil)
}

func (h *harness) browserRemembersSession() error {
	h.web.remembered = h.web.jar(h.addr)[sessionCookie]
	if h.web.remembered == "" {
		return errors.New("the browser holds no session cookie to remember")
	}
	return nil
}

func (h *harness) browserPutsSessionBack() error {
	if h.web.remembered == "" {
		return errors.New("the browser remembered no session cookie")
	}
	h.web.jar(h.addr)[sessionCookie] = h.web.remembered
	return nil
}

// browserWaits really waits: the expiry scenarios run against short configured
// lifetimes, so the clock they drive is the server's own.
func (h *harness) browserWaits(ctx context.Context, seconds float64) error {
	d := wholeAndFractionalSeconds(seconds)
	if d > maxWait {
		return fmt.Errorf("a wait of %s is longer than the %s a step may sleep", d, maxWait)
	}
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func registerSessionSteps(sc *godog.ScenarioContext, h *harness) {
	sc.Step(`^the browser reads its CSRF token from "([^"]*)"$`, h.readsCSRFToken)
	sc.Step(`^the browser sends POST "([^"]*)" with (the session's CSRF token|no CSRF token) and (the board's own Origin|no Origin|the Origin "([^"]*)")$`, h.browserPosts)
	sc.Step(`^the browser calls GET "([^"]*)" as an API client$`, h.browserCallsAPI)
	sc.Step(`^the browser sends POST "([^"]*)" with body "([^"]*)", the session's CSRF token and the board's own Origin$`, h.browserPostsBody)
	sc.Step(`^the session was last seen (\d+) hours ago$`, h.sessionLastSeen)
	sc.Step(`^the session was seen within the last minute$`, h.sessionSeenRecently)
	sc.Step(`^the browser remembers its session cookie$`, h.browserRemembersSession)
	sc.Step(`^the browser puts the remembered session cookie back$`, h.browserPutsSessionBack)
	sc.Step(`^the browser waits (\d+(?:\.\d+)?) seconds?$`, h.browserWaits)
}
