package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"
)

// BlockUsage is the single stderr line for a malformed `vh block`.
const BlockUsage = "usage: vh block <task> \"<question>\" [--poll D | --stop] [--json]"

var blockFlags = []flagSpec{{"--poll", true}, {"--stop", false}, {"--json", false}}

// blockView is what POST /api/v1/asks answers: the ask, with the caller's lease
// on its task when a lease_action found one.
type blockView struct {
	askView
	Lease *leaseView `json:"lease"`
}

// blockCmd runs `vh block <task> "<question>" [--poll D | --stop]`: an ask on
// the task with its lease action, in one request. The default (--stop) ends my
// lease on the task and prints the `ask` line and the `lease end` line. --poll
// keeps the lease, renews it while it waits for the answer and ends as
// `ask --poll` does.
func blockCmd(args []string, stdout, stderr io.Writer, env Env) int {
	p, wait, ok := parseBlock(args)
	if !ok {
		return usageLine(stderr, BlockUsage)
	}
	c, err := newClient(env)
	if err != nil {
		return report(stderr, err)
	}
	ctx, stop := clientContext()
	defer stop()
	v, raw, err := raiseBlock(ctx, c, p, wait)
	if err != nil {
		return report(stderr, raiseFailure(p.pos[0])(err))
	}
	if wait == 0 {
		return printBlocked(p, v, raw, stdout)
	}
	if !p.has("--json") {
		_, _ = fmt.Fprintf(stdout, "ok ask %s raised\n", v.Ref)
	}
	return blockWait(ctx, c, p, v, raw, pollDeadline(nowFn(), wait, pollSecond(env)), stdout, stderr)
}

// parseBlock reads the command line and the seconds of --poll (0 without it).
// It is not ok for a missing or extra argument, a bad duration or --poll
// together with --stop.
func parseBlock(args []string) (parsedArgs, int64, bool) {
	p, ok := parseArgs(args, blockFlags)
	if !ok || len(p.pos) != 2 {
		return p, 0, false
	}
	wait, ok := pollSeconds(p)
	return p, wait, ok
}

// raiseBlock sends the ask: a stop ask that ends the lease, or with a wait a
// poll ask that keeps it. It returns the answer and its raw body.
func raiseBlock(ctx context.Context, c *client, p parsedArgs, wait int64) (blockView, json.RawMessage, error) {
	body := map[string]any{"task": p.pos[0], "question": p.pos[1], "mode": "stop", "lease_action": "stop"}
	if wait > 0 {
		body["mode"], body["lease_action"] = "poll", "keep"
	}
	var raw json.RawMessage
	var v blockView
	if err := c.do(ctx, "POST", "/api/v1/asks", nil, body, &raw); err != nil {
		return v, raw, err
	}
	return v, raw, json.Unmarshal(raw, &v)
}

// printBlocked prints the answer of a --stop block: the `ask` line and, when a
// lease ended, the `lease end` line; with --json the response as it came.
func printBlocked(p parsedArgs, v blockView, raw json.RawMessage, stdout io.Writer) int {
	if p.has("--json") {
		_, _ = fmt.Fprintln(stdout, string(raw))
		return ExitOK
	}
	_, _ = fmt.Fprintf(stdout, "ok ask %s raised\n", v.Ref)
	if v.Lease != nil {
		_, _ = fmt.Fprintf(stdout, "ok lease %s ended\n", v.Lease.Ref)
	}
	return ExitOK
}

// blockWait waits up to deadline for the answer of the raised poll ask v,
// renewing the lease the block kept before each wait, and prints how it ended.
func blockWait(ctx context.Context, c *client, p parsedArgs, v blockView, raw json.RawMessage, deadline time.Time, stdout, stderr io.Writer) int {
	fetch := waitFetch(ctx, c, v.Ref)
	if v.Lease != nil {
		fetch = renewing(ctx, c, v.Lease.Ref, fetch)
	}
	r := pollAsk(read{a: v.askView, raw: raw}, deadline, nowFn, fetch)
	if r.err != nil {
		return report(stderr, askRefFailure(v.Ref)(r.err))
	}
	return printPollEnd(r.a, r.raw, p.has("--json"), stdout)
}

// renewing renews the lease ref before each read of fetch, so it stays live
// however long the wait is. A renewal that fails is not the wait's failure: the
// answer still matters, and the lease is the server's to expire.
func renewing(ctx context.Context, c *client, ref string, fetch askFetch) askFetch {
	return func(wait int, until time.Time) read {
		var raw json.RawMessage
		_ = c.do(ctx, "POST", "/api/v1/leases/"+url.PathEscape(ref)+"/renew", nil, nil, &raw)
		return fetch(wait, until)
	}
}
