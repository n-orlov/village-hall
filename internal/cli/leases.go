package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ExitWarning is the exit code of a lease write that succeeded while another
// live lease holds one of its resources (SPEC §8.1).
const ExitWarning = 7

// LeaseUsage is the single stderr line for a malformed `vh lease` command line.
const LeaseUsage = "usage: vh lease start <task> \"<intent>\" [--ttl D] [--res r]... [--notice] [--json] | renew [<L…>|<task>] [--ttl D] [--intent text] [--json] | end [<L…>|<task>] [\"<outcome>\"] [--json] | ack <L…> [--json] | ls [--state live|ended|expired|all] [--mine] [--task ref] [--res r] [--limit N|--all] [--json]"

// leaseSubcommands are the words after `vh lease` and what runs each.
var leaseSubcommands = map[string]func([]string, io.Writer, io.Writer, Env) int{
	"start": leaseStart,
	"renew": leaseRenew,
	"end":   leaseEnd,
	"ack":   leaseAck,
	"ls":    leaseLs,
}

// leaseCmd runs `vh lease ...` against the server named by VH_URL.
func leaseCmd(args []string, stdout, stderr io.Writer, env Env) int {
	if len(args) == 0 {
		return usageLine(stderr, LeaseUsage)
	}
	run, ok := leaseSubcommands[args[0]]
	if !ok {
		return usageLine(stderr, LeaseUsage)
	}
	return run(args[1:], stdout, stderr, env)
}

// leaseResult is what a start or a renewal answers: the lease and the live
// leases of others that hold one of its resources.
type leaseResult struct {
	Lease     leaseView      `json:"lease"`
	Conflicts []conflictView `json:"conflicts"`
}

type conflictView struct {
	Lease     string   `json:"lease"`
	Holder    string   `json:"holder"`
	Task      string   `json:"task"`
	Intent    string   `json:"intent"`
	ExpiresAt string   `json:"expires_at"`
	Resources []string `json:"resources"`
}

var (
	leaseRefPattern = regexp.MustCompile(`^[lL][0-9]+$`)
	taskRefPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*-[0-9]+$`)
)

// leaseStart runs `vh lease start <task> "<intent>"`.
func leaseStart(args []string, stdout, stderr io.Writer, env Env) int {
	p, ok := parseArgs(args, leaseStartFlags)
	if !ok || len(p.pos) != 2 {
		return usageLine(stderr, LeaseUsage)
	}
	body, err := leaseBody(p)
	if err != nil {
		return badFlagValue(stderr, err)
	}
	body["task"], body["intent"] = p.pos[0], p.pos[1]
	if p.has("--notice") {
		body["notice"] = true
	}
	if res := p.values["--res"]; len(res) > 0 {
		body["resources"] = res
	}
	c, err := newClient(env)
	if err != nil {
		return report(stderr, err)
	}
	ctx, stop := clientContext()
	defer stop()
	var raw json.RawMessage
	if err := c.do(ctx, "POST", "/api/v1/leases", nil, body, &raw); err != nil {
		return report(stderr, startFailure(err, p.pos[0]))
	}
	return printLeaseResults(p, []json.RawMessage{raw}, false, stdout, stderr)
}

// leaseBody reads --ttl into ttl_seconds.
func leaseBody(p parsedArgs) (map[string]any, error) {
	body := map[string]any{}
	if ttl := last(p.values["--ttl"]); ttl != "" {
		seconds, err := parseDuration(ttl)
		if err != nil {
			return nil, fmt.Errorf("--ttl: %w", err)
		}
		body["ttl_seconds"] = seconds
	}
	return body, nil
}

// startFailure words the next action of a refused start: an unknown task
// exits 4 with task ls, a closed task exits 6 with task show.
func startFailure(err error, task string) error {
	var ae *apiError
	if !errors.As(err, &ae) {
		return err
	}
	switch ae.Status {
	case http.StatusNotFound:
		return withNext(err, ae.Status, "vh task ls")
	case http.StatusConflict:
		if strings.HasPrefix(ae.Detail, "you already hold ") {
			// the other kind of lease is held: the server's next is the end to run first
			return conflictError(ae, ae.Next)
		}
		if strings.HasSuffix(ae.Detail, ": it is done") {
			return conflictError(ae, "vh task reopen "+strings.ToLower(task)+" first")
		}
		return conflictError(ae, "vh task show "+strings.ToLower(task))
	}
	return err
}

// leaseRenew runs `vh lease renew [<L…>|<task>]`.
func leaseRenew(args []string, stdout, stderr io.Writer, env Env) int {
	p, ok := parseArgs(args, leaseRenewFlags)
	if !ok || len(p.pos) > 1 {
		return usageLine(stderr, LeaseUsage)
	}
	body, err := renewBody(p)
	if err != nil {
		return badFlagValue(stderr, err)
	}
	c, err := newClient(env)
	if err != nil {
		return report(stderr, err)
	}
	ctx, stop := clientContext()
	defer stop()
	refs, err := c.renewTargets(ctx, p.pos)
	if err != nil {
		return report(stderr, targetFailure(err))
	}
	done, failure := c.renewAll(ctx, refs, body)
	code := printLeaseResults(p, done, len(p.pos) == 0, stdout, stderr)
	if failure != nil {
		return report(stderr, failure)
	}
	return code
}

// renewBody reads --ttl and --intent.
func renewBody(p parsedArgs) (map[string]any, error) {
	body, err := leaseBody(p)
	if err != nil {
		return nil, err
	}
	if in := last(p.values["--intent"]); in != "" {
		body["intent"] = in
	}
	return body, nil
}

// renewAll renews the leases in order and returns the answers of the ones it
// renewed, and the failure that stopped it.
func (c *client) renewAll(ctx context.Context, refs []string, body map[string]any) ([]json.RawMessage, error) {
	var done []json.RawMessage
	for _, ref := range refs {
		var raw json.RawMessage
		if err := c.do(ctx, "POST", "/api/v1/leases/"+url.PathEscape(ref)+"/renew", nil, orNil(body), &raw); err != nil {
			return done, c.renewFailure(ctx, err, ref)
		}
		done = append(done, raw)
	}
	return done, nil
}

// orNil is nil for an empty body, so a renewal with nothing to change sends none.
func orNil(body map[string]any) any {
	if len(body) == 0 {
		return nil
	}
	return body
}

// renewTargets are the leases a renewal names: all my live leases with no
// argument, else the one lease the argument names.
func (c *client) renewTargets(ctx context.Context, pos []string) ([]string, error) {
	if len(pos) == 1 {
		ref, err := c.leaseOf(ctx, pos[0])
		return []string{ref}, err
	}
	return c.myLive(ctx)
}

// leaseOf is the ref of the lease an argument names: an L<n> ref as it is, a
// task ref as my latest lease on that task (the live one when I hold one).
func (c *client) leaseOf(ctx context.Context, arg string) (string, error) {
	if leaseRefPattern.MatchString(arg) {
		return strings.ToUpper(arg), nil
	}
	q := url.Values{"state": {"all"}, "actor": {c.id.Actor}, "task": {arg}}
	items, err := c.allPages(ctx, "/api/v1/leases", q)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", &apiError{Status: http.StatusNotFound, Detail: "you hold no lease on task " + strings.ToLower(arg), Next: "vh lease ls --task " + strings.ToLower(arg)}
	}
	var l leaseView
	if err := json.Unmarshal(items[len(items)-1], &l); err != nil {
		return "", err
	}
	return l.Ref, nil
}

// myLive lists the refs of my live leases, an error when I hold none.
func (c *client) myLive(ctx context.Context) ([]string, error) {
	items, err := c.allPages(ctx, "/api/v1/leases", url.Values{"state": {"live"}, "actor": {c.id.Actor}})
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(items))
	for _, raw := range items {
		var l leaseView
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, err
		}
		refs = append(refs, l.Ref)
	}
	if len(refs) == 0 {
		return nil, &apiError{Status: http.StatusNotFound, Detail: "you hold no live lease", Next: "vh lease ls"}
	}
	return refs, nil
}

// targetFailure sends an unknown task to task ls.
func targetFailure(err error) error {
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound && strings.Contains(ae.Detail, "no task") {
		return withNext(err, ae.Status, "vh task ls")
	}
	return err
}

// renewFailure words the next action of a refused renewal. A lease that ran
// out (410) or ended (409) exits 6 with the start that replaces it; an unknown
// lease exits 4 with lease ls.
func (c *client) renewFailure(ctx context.Context, err error, ref string) error {
	var ae *apiError
	if !errors.As(err, &ae) {
		return err
	}
	switch ae.Status {
	case http.StatusNotFound:
		return withNext(err, ae.Status, "vh lease ls")
	case http.StatusGone:
		return &apiError{Status: ae.Status, Detail: expiredDetail(ae, ref, nowFn()), Next: c.restartNext(ctx, ref), Exit: ExitConflict}
	case http.StatusConflict:
		return &apiError{Status: ae.Status, Detail: ae.Detail, Next: c.restartNext(ctx, ref), Exit: ExitConflict}
	}
	return err
}

// expiredDetail is `lease L7 expired 14m ago`, from the expiry the 410 carries.
func expiredDetail(ae *apiError, ref string, now time.Time) string {
	if _, err := time.Parse(time.RFC3339, ae.ExpiredAt); err != nil {
		return ae.Detail
	}
	return "lease " + ref + " expired " + ago(now, ae.ExpiredAt)
}

// restartNext is the start that replaces the lease ref, with its task when the
// lease can still be read.
func (c *client) restartNext(ctx context.Context, ref string) string {
	task := "<task>"
	var l leaseView
	if err := c.do(ctx, "GET", "/api/v1/leases/"+url.PathEscape(ref), nil, nil, &l); err == nil && l.Task != "" {
		task = l.Task
	}
	return "vh lease start " + task + ` "<intent>"`
}

// leaseEnd runs `vh lease end [<L…>|<task>] ["<outcome>"]`.
func leaseEnd(args []string, stdout, stderr io.Writer, env Env) int {
	p, ok := parseArgs(args, leaseEndFlags)
	target, outcome, okArgs := endArgs(p.pos)
	if !ok || !okArgs {
		return usageLine(stderr, LeaseUsage)
	}
	c, err := newClient(env)
	if err != nil {
		return report(stderr, err)
	}
	ctx, stop := clientContext()
	defer stop()
	ref, err := c.endTarget(ctx, target)
	if err != nil {
		return report(stderr, targetFailure(err))
	}
	var raw json.RawMessage
	if err := c.do(ctx, "POST", "/api/v1/leases/"+url.PathEscape(ref)+"/end", nil, endBody(outcome), &raw); err != nil {
		return report(stderr, endFailure(err))
	}
	return printEnded(p, raw, stdout, stderr)
}

// endBody is the outcome, or no body without one.
func endBody(outcome string) any {
	if outcome == "" {
		return nil
	}
	return map[string]any{"outcome": outcome}
}

// printEnded prints `ok lease L7 ended`, or the lease with --json.
func printEnded(p parsedArgs, raw json.RawMessage, stdout, stderr io.Writer) int {
	if p.has("--json") {
		_, _ = fmt.Fprintln(stdout, string(raw))
		return ExitOK
	}
	var l leaseView
	if err := json.Unmarshal(raw, &l); err != nil {
		return report(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "ok lease %s ended\n", l.Ref)
	return ExitOK
}

// endArgs splits the positionals of lease end into the lease it names and the
// outcome. A lone word that is a lease ref (L7) or a task ref (aa-1) names the
// lease; any other lone word is the outcome of my only live lease.
func endArgs(pos []string) (target, outcome string, ok bool) {
	switch len(pos) {
	case 0:
		return "", "", true
	case 1:
		if leaseRefPattern.MatchString(pos[0]) || taskRefPattern.MatchString(pos[0]) {
			return pos[0], "", true
		}
		return "", pos[0], true
	case 2:
		return pos[0], pos[1], true
	}
	return "", "", false
}

// endTarget is the lease to end: the one named, else my only live lease.
func (c *client) endTarget(ctx context.Context, target string) (string, error) {
	if target != "" {
		return c.leaseOf(ctx, target)
	}
	refs, err := c.myLive(ctx)
	if err != nil {
		return "", err
	}
	if len(refs) > 1 {
		return "", &apiError{Status: http.StatusBadRequest, Detail: "you hold " + fmt.Sprint(len(refs)) + " live leases: " + strings.Join(refs, ", "), Next: "vh lease end <L…>", Exit: ExitUsage}
	}
	return refs[0], nil
}

// endFailure words the next action of a refused end: an unknown lease exits 4,
// a lease that is not live exits 6, each with lease ls.
func endFailure(err error) error {
	var ae *apiError
	if !errors.As(err, &ae) {
		return err
	}
	switch ae.Status {
	case http.StatusNotFound:
		return withNext(err, ae.Status, "vh lease ls")
	case http.StatusConflict:
		return conflictError(ae, "vh lease ls --mine --state all")
	}
	return err
}

// leaseAck runs `vh lease ack <L…>`.
func leaseAck(args []string, stdout, stderr io.Writer, env Env) int {
	p, ok := parseArgs(args, leaseEndFlags)
	if !ok || len(p.pos) != 1 || !leaseRefPattern.MatchString(p.pos[0]) {
		return usageLine(stderr, LeaseUsage)
	}
	c, err := newClient(env)
	if err != nil {
		return report(stderr, err)
	}
	ctx, stop := clientContext()
	defer stop()
	var raw json.RawMessage
	if err := c.do(ctx, "POST", "/api/v1/leases/"+url.PathEscape(strings.ToUpper(p.pos[0]))+"/ack", nil, nil, &raw); err != nil {
		return report(stderr, endFailure(err))
	}
	return printAcked(p, raw, stdout, stderr)
}

// printAcked prints `ok lease L7 acknowledged`, or the lease with --json.
func printAcked(p parsedArgs, raw json.RawMessage, stdout, stderr io.Writer) int {
	if p.has("--json") {
		_, _ = fmt.Fprintln(stdout, string(raw))
		return ExitOK
	}
	var l leaseView
	if err := json.Unmarshal(raw, &l); err != nil {
		return report(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "ok lease %s acknowledged\n", l.Ref)
	return ExitOK
}

// printLeaseResults prints, for each start or renewal answer, the `ok` line
// and one CONFLICT line per conflict, or the answers as JSON (one object, or
// an array when many). It returns 7 when any answer carries a conflict.
func printLeaseResults(p parsedArgs, raws []json.RawMessage, many bool, stdout, stderr io.Writer) int {
	results, conflicts, err := decodeResults(raws)
	if err != nil {
		return report(stderr, err)
	}
	if p.has("--json") {
		printJSONResults(raws, many, stdout, stderr)
	} else {
		now := nowFn()
		for _, r := range results {
			_, _ = fmt.Fprint(stdout, leaseResultText(r, now))
		}
	}
	if conflicts > 0 {
		return ExitWarning
	}
	return ExitOK
}

// decodeResults reads the answers and counts their conflicts.
func decodeResults(raws []json.RawMessage) ([]leaseResult, int, error) {
	results := make([]leaseResult, len(raws))
	conflicts := 0
	for i, raw := range raws {
		if err := json.Unmarshal(raw, &results[i]); err != nil {
			return nil, 0, err
		}
		conflicts += len(results[i].Conflicts)
	}
	return results, conflicts, nil
}

// printJSONResults prints the answers as they came: one line each, or one array.
func printJSONResults(raws []json.RawMessage, many bool, stdout, stderr io.Writer) {
	if many {
		printJSON(stdout, stderr, raws)
		return
	}
	for _, raw := range raws {
		_, _ = fmt.Fprintln(stdout, string(raw))
	}
}

// leaseResultText is the `ok` line of a lease and its CONFLICT lines.
func leaseResultText(r leaseResult, now time.Time) string {
	var b strings.Builder
	b.WriteString("ok lease " + r.Lease.Ref + " " + r.Lease.Task + " exp " + leaseTTL(r.Lease) + "\n")
	for _, c := range r.Conflicts {
		b.WriteString(conflictLine(c, now) + "\n")
	}
	return b.String()
}

// leaseTTL is the time from the lease's last renewal to its expiry: 2h, 1h52m.
func leaseTTL(l leaseView) string {
	renewed, err1 := time.Parse(time.RFC3339, l.RenewedAt)
	expires, err2 := time.Parse(time.RFC3339, l.ExpiresAt)
	if err1 != nil || err2 != nil {
		return l.ExpiresAt
	}
	return span(expires.Sub(renewed))
}

// conflictLine is `CONFLICT <resources>: L5 <holder> (<task>) "<intent>" exp 41m`.
func conflictLine(c conflictView, now time.Time) string {
	exp := "exp " + c.ExpiresAt
	if t, err := time.Parse(time.RFC3339, c.ExpiresAt); err == nil {
		exp = "exp " + span(t.Sub(now))
	}
	return "CONFLICT " + strings.Join(c.Resources, ",") + ": " + c.Lease + " " + c.Holder + " (" + c.Task + ") " + strconv.Quote(c.Intent) + " " + exp
}

// span writes a length of time as 3h10m, 2h, 41m or 20s; a negative one is 0s.
func span(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", d/time.Hour, d%time.Hour/time.Minute)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d > 0:
		return fmt.Sprintf("%ds", d/time.Second)
	}
	return "0s"
}
