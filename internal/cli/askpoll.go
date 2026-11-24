package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"
)

// ExitTimeout is the exit code of `vh ask --poll` when the deadline passes
// with the ask still open (SPEC §8.1).
const ExitTimeout = 3

// maxAskWait is the longest single ?wait= the server accepts, in seconds.
const maxAskWait = 300

// pollSeconds reads --poll D as whole seconds. 0 means no --poll. It is not ok
// for a bad duration and for --poll given together with --stop.
func pollSeconds(p parsedArgs) (int64, bool) {
	if !p.has("--poll") {
		return 0, true
	}
	seconds, err := parseDuration(last(p.values["--poll"]))
	return seconds, err == nil && !p.has("--stop")
}

// nextWait is the length in whole seconds of the next ?wait= call when left
// is the time to the deadline. Within the server's cap it is left rounded UP to
// a whole second, since the server takes no shorter wait than one second: that
// call also carries the deadline as until, so the server ends it at the
// deadline itself, sub-second remainder included. Past the cap the call takes
// what is beyond one capped call (at most the cap, at least a second), so it
// ends a whole cap before the deadline and the last call is always one that
// reaches it. There is no next call (false) once nothing is left.
func nextWait(left time.Duration) (int, bool) {
	if left <= 0 {
		return 0, false
	}
	const limit = maxAskWait * time.Second
	if left > limit {
		left = min(left-limit, limit)
	}
	return int((left + time.Second - 1) / time.Second), true
}

// read is the result of one request: the ask and its raw body, or the error.
type read struct {
	a   askView
	raw json.RawMessage
	err error
}

// ended says the read found an answer or a withdrawal.
func (r read) ended() bool { return r.err == nil && r.a.State != "open" }

// askFetch reads the ask once with ?wait=wait&until=until: the server answers
// when the ask ends, or with the ask as it stood when until passed, whenever
// that answer reaches the client.
type askFetch func(wait int, until time.Time) read

// pollAsk waits for the ask to leave `open` by repeating fetch until the
// deadline. now is the clock. It returns the last read: still open when the
// deadline came first, as the ask stood when it passed, whatever a later read
// would find: the server counts an end at until only on proof its COMMIT had
// returned by then (a read that came back by until and saw it, or a clock
// read after the COMMIT that is at or before until), never on a stamp or mark
// taken before the COMMIT. An answer or withdrawal whose stamp places it after
// the deadline (endedAfter) did not take effect by it, so the ask is then
// still open too.
func pollAsk(first read, deadline time.Time, now func() time.Time, fetch askFetch) read {
	r := first
	for r.err == nil && !r.ended() {
		wait, ok := nextWait(deadline.Sub(now()))
		if !ok {
			break
		}
		r = fetch(wait, deadline)
	}
	if r.ended() && endedAfter(r.a, deadline) {
		r.a.State = "open"
	}
	return r
}

// stampPrecision is the precision of the server's answered_at and withdrawn_at:
// it stores an instant floored to a whole millisecond (SPEC §2), so a stamp s
// says only that the end happened somewhere in [s, s+1ms).
const stampPrecision = time.Millisecond

// endedAfter says a's stamp places its answer or withdrawal after deadline. A
// whole-millisecond stamp s does so when s is at or after the deadline: the end
// then happened at s or up to a millisecond later. A stamp carrying a
// sub-millisecond part is taken as exact. A stamp in the millisecond the
// deadline falls in, but before it, is no proof either way (the end happened
// before or after the deadline), so the server's answer, which counts an end
// only on proof of its COMMIT, decides: it counts. An ask that is open, or
// whose stamp is missing or unreadable, did not end after it.
func endedAfter(a askView, deadline time.Time) bool {
	stamp := ""
	switch a.State {
	case "answered":
		stamp = a.AnsweredAt
	case "withdrawn":
		stamp = a.WithdrawnAt
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return false
	}
	if at.Truncate(stampPrecision).Equal(at) {
		return !at.Before(deadline)
	}
	return at.After(deadline)
}

// pollDeadline is the deadline of a --poll of seconds raised at now, each of
// them second long: exactly now plus seconds, to the nanosecond, never rounded
// or otherwise moved. second is a real second (pollSecond) in every build but
// the scenario harness's.
func pollDeadline(now time.Time, seconds int64, second time.Duration) time.Time {
	return now.Add(time.Duration(seconds) * second).Round(0)
}

// askPoll raises the ask as a poll ask, then waits for its end up to seconds.
func askPoll(p parsedArgs, body map[string]any, seconds int64, stdout, stderr io.Writer, env Env) int {
	c, err := newClient(env)
	if err != nil {
		return report(stderr, err)
	}
	ctx, stop := clientContext()
	defer stop()
	var raw json.RawMessage
	if err := c.do(ctx, "POST", "/api/v1/asks", nil, body, &raw); err != nil {
		return report(stderr, raiseFailure(p.pos[0])(err))
	}
	var first askView
	if err := json.Unmarshal(raw, &first); err != nil {
		return report(stderr, err)
	}
	deadline := pollDeadline(nowFn(), seconds, pollSecond(env))
	r := pollAsk(read{a: first, raw: raw}, deadline, nowFn, waitFetch(ctx, c, first.Ref))
	if r.err != nil {
		return report(stderr, askRefFailure(first.Ref)(r.err))
	}
	return printPollEnd(r.a, r.raw, p.has("--json"), stdout)
}

// getAsk reads the ask once with ?wait=wait and until. The wait may hold the
// request longer than a plain read, so the client's timeout is raised to the
// wait plus the usual one: the answer to a wait that ran to until may reach the
// client after until, and is still the one that counts.
func getAsk(ctx context.Context, c *client, ref string, wait int, until time.Time) read {
	h := *c.http
	h.Timeout = time.Duration(wait)*time.Second + requestTimeout
	long := *c
	long.http = &h
	query := url.Values{"wait": {strconv.Itoa(wait)}, "until": {until.UTC().Format(time.RFC3339Nano)}}
	var r read
	if r.err = long.do(ctx, "GET", askPath(ref, ""), query, nil, &r.raw); r.err == nil {
		r.err = json.Unmarshal(r.raw, &r.a)
	}
	return r
}

// waitFetch reads the ask with ?wait= and until. No read is cut off at the
// deadline: the server's answer is the ask as it stood then, however late it
// comes back.
func waitFetch(ctx context.Context, c *client, ref string) askFetch {
	return func(wait int, until time.Time) read { return getAsk(ctx, c, ref, wait, until) }
}

// printPollEnd prints how the wait ended and returns the exit code: an answer
// is `ok A<n> answered by <h>: <answer>` (0), a withdrawal `A<n> withdrawn`
// with its reason (6), the deadline `A<n> still open → vh ask show A<n>` (3).
// With --json the last ask is printed instead, with the same exit code.
func printPollEnd(a askView, raw json.RawMessage, asJSON bool, stdout io.Writer) int {
	code := pollExit(a.State)
	if asJSON {
		_, _ = fmt.Fprintln(stdout, string(raw))
		return code
	}
	_, _ = fmt.Fprintln(stdout, pollLine(a))
	return code
}

// pollExit is the exit code of an ask that ended the wait in state.
func pollExit(state string) int {
	switch state {
	case "answered":
		return ExitOK
	case "withdrawn":
		return ExitConflict
	}
	return ExitTimeout
}

// pollLine is the one line the end of a wait prints.
func pollLine(a askView) string {
	switch a.State {
	case "answered":
		return fmt.Sprintf("ok %s answered by %s: %s", a.Ref, a.AnsweredBy, a.Answer)
	case "withdrawn":
		if a.WithdrawReason != "" {
			return fmt.Sprintf("%s withdrawn: %s", a.Ref, a.WithdrawReason)
		}
		return a.Ref + " withdrawn"
	}
	return fmt.Sprintf("%s still open → vh ask show %s", a.Ref, a.Ref)
}
