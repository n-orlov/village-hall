package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// leaseStates are the values of --state.
var leaseStates = []string{"live", "ended", "expired", "all"}

// leaseQuery is what `vh lease ls` asks the server for.
type leaseQuery struct {
	state, task, resource string
	mine, notice          bool
}

// leaseFilter reads the filters of lease ls. A state outside the four is not ok.
func leaseFilter(p parsedArgs) (leaseQuery, bool) {
	q := leaseQuery{
		state: last(p.values["--state"]), task: last(p.values["--task"]),
		resource: last(p.values["--res"]), mine: p.has("--mine"), notice: p.has("--notice"),
	}
	return q, q.state == "" || slices.Contains(leaseStates, q.state)
}

// command is the `vh lease ls` line that shows the same leases, with --all.
func (q leaseQuery) command() string {
	cmd := "vh lease ls"
	if q.state != "" {
		cmd += " --state " + q.state
	}
	if q.mine {
		cmd += " --mine"
	}
	if q.notice {
		cmd += " --notice"
	}
	if q.task != "" {
		cmd += " --task " + quoteArg(q.task)
	}
	if q.resource != "" {
		cmd += " --res " + quoteArg(q.resource)
	}
	return cmd
}

// leaseListArgs reads the filters and the cap of lease ls; parsed says whether the flags parsed.
func leaseListArgs(p parsedArgs, parsed bool) (leaseQuery, int, bool) {
	limit, okCap := listCap(p)
	filter, okFilter := leaseFilter(p)
	return filter, limit, parsed && okCap && okFilter && len(p.pos) == 0
}

// leaseLs runs `vh lease ls`.
func leaseLs(args []string, stdout, stderr io.Writer, env Env) int {
	p, ok := parseArgs(args, leaseLsFlags)
	filter, limit, ok := leaseListArgs(p, ok)
	if !ok {
		return usageLine(stderr, LeaseUsage)
	}
	c, err := newClient(env)
	if err != nil {
		return report(stderr, err)
	}
	ctx, stop := clientContext()
	defer stop()
	items, err := c.allPages(ctx, "/api/v1/leases", filter.values(c.id.Actor))
	if err != nil {
		return report(stderr, withNext(err, 404, "vh task ls"))
	}
	items, more := capItems(items, limit)
	if p.has("--json") {
		return printJSON(stdout, stderr, items)
	}
	return printLeaseLines(stdout, stderr, items, more, filter.command())
}

// values are the query parameters of the list; actor is who --mine means.
func (q leaseQuery) values(actor string) url.Values {
	v := url.Values{"state": {"live"}}
	if q.state != "" {
		v.Set("state", q.state)
	}
	for k, val := range map[string]string{"task": q.task, "resource": q.resource} {
		if val != "" {
			v.Set(k, val)
		}
	}
	if q.notice {
		v.Set("notice", "true")
	}
	if q.mine {
		v.Set("actor", actor)
	}
	return v
}

func printLeaseLines(stdout, stderr io.Writer, items []json.RawMessage, more int, cmd string) int {
	now := nowFn()
	for _, raw := range items {
		var l leaseView
		if err := json.Unmarshal(raw, &l); err != nil {
			return report(stderr, err)
		}
		_, _ = fmt.Fprintln(stdout, leaseLine(l, now))
	}
	if more > 0 {
		_, _ = fmt.Fprintf(stdout, "+%d more → %s --all\n", more, cmd)
	}
	return ExitOK
}

// leaseLine is `L7  live  <task>  <holder>  "<intent>"  res a,b  exp 41m`; an
// expired lease ends `expired 14m ago`, an ended one `ended 3m ago`.
func leaseLine(l leaseView, now time.Time) string {
	parts := []string{l.Ref, l.State, l.Task, l.Actor, strconv.Quote(l.Intent)}
	if len(l.Resources) > 0 {
		parts = append(parts, "res "+strings.Join(l.Resources, ","))
	}
	return strings.Join(parts, "  ") + "  " + leaseEnding(l, now)
}

// leaseEnding says when the lease runs out or ran out, or when it ended.
func leaseEnding(l leaseView, now time.Time) string {
	switch l.State {
	case "ended":
		return "ended " + ago(now, l.EndedAt)
	case "expired":
		return "expired " + ago(now, l.ExpiresAt)
	}
	t, err := time.Parse(time.RFC3339, l.ExpiresAt)
	if err != nil {
		return "exp " + l.ExpiresAt
	}
	return "exp " + span(t.Sub(now))
}
