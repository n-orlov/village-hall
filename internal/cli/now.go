package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// NowUsage is the single stderr line for a malformed `vh now`.
const NowUsage = "usage: vh now [--resources r1,r2] [--json]"

// nowCap is how many lines each section of `vh now` shows (SPEC §8.1).
const nowCap = 5

// nowView is what GET /api/v1/now answers.
type nowView struct {
	Actor        string         `json:"actor"`
	Notices      []leaseView    `json:"notices"`
	MyLeases     []nowLease     `json:"my_leases"`
	OthersLeases []leaseView    `json:"others_leases"`
	Conflicts    []conflictView `json:"conflicts"`
	MyAsks       []askView      `json:"my_asks"`
	Notes        []noteView     `json:"notes"`
	NotesMore    int            `json:"notes_more"`
	Attention    nowAttention   `json:"attention"`
}

// nowAttention is the size of each attention section.
type nowAttention struct {
	OpenAsks      int `json:"open_asks"`
	DoneNotClosed int `json:"done_not_closed"`
	ExpiredLeases int `json:"expired_leases"`
	OverdueWatch  int `json:"overdue_watches"`
	Conflicts     int `json:"conflicts"`
}

type nowLease struct {
	leaseView
	Title string `json:"title"`
}

// nowCmd runs `vh now [--resources r1,r2]`: the check-in. It exits 7 when a
// conflict touches one of my live leases or a resource given.
func nowCmd(args []string, stdout, stderr io.Writer, env Env) int {
	p, ok := parseArgs(args, nowFlags)
	if !ok || len(p.pos) != 0 {
		return usageLine(stderr, NowUsage)
	}
	raw, v, err := fetchNow(p, env)
	if err != nil {
		return report(stderr, err)
	}
	if p.has("--json") {
		_, _ = fmt.Fprintln(stdout, string(raw))
	} else {
		_, _ = fmt.Fprint(stdout, nowText(v, nowFn()))
	}
	if len(v.Conflicts) > 0 {
		return ExitWarning
	}
	return ExitOK
}

// fetchNow asks the server for the check-in, with the resources given, and
// returns the answer as it came and as read.
func fetchNow(p parsedArgs, env Env) (json.RawMessage, nowView, error) {
	var v nowView
	c, err := newClient(env)
	if err != nil {
		return nil, v, err
	}
	ctx, stop := clientContext()
	defer stop()
	q := url.Values{}
	if res := strings.Join(p.values["--resources"], ","); res != "" {
		q.Set("resources", res)
	}
	var raw json.RawMessage
	if err := c.do(ctx, "GET", "/api/v1/now", q, nil, &raw); err != nil {
		return nil, v, err
	}
	return raw, v, json.Unmarshal(raw, &v)
}

// nowText is the check-in: who I am, then the sections `notices` (every live
// notice, `none` from an old server that omits them), `my leases`, `others`,
// `conflicts`, `my asks` and `notes`, each one line per item, `none` for an
// empty one, and the `attention` line with the counts.
func nowText(v nowView, now time.Time) string {
	mine := make([]string, len(v.MyLeases))
	for i, l := range v.MyLeases {
		mine[i] = myLeaseLine(l, now)
	}
	others := make([]string, len(v.OthersLeases))
	for i, l := range v.OthersLeases {
		others[i] = otherLeaseLine(l, now)
	}
	conflicts := make([]string, len(v.Conflicts))
	for i, c := range v.Conflicts {
		conflicts[i] = conflictLine(c, now)
	}
	notices := make([]string, len(v.Notices))
	for i, l := range v.Notices {
		notices[i] = otherLeaseLine(l, now)
	}
	return "you: " + v.Actor + "\n" +
		nowSection("notices", notices, 0, "vh lease ls --notice") +
		nowSection("my leases", mine, 0, "vh lease ls --mine") +
		nowSection("others", others, 0, "vh lease ls") +
		nowSection("conflicts", conflicts, 0, "vh lease ls") +
		nowSection("my asks", myAskLines(v.MyAsks, now), 0, "vh ask ls --mine") +
		nowSection("notes", noteLines(v.Notes, now), v.NotesMore, "vh notes --since last") +
		attentionLine(v.Attention) + "\n"
}

// myAskLines is one line per ask of mine: the ask line of `ask ls`, and for an
// answered ask `ANSWERED ` in front and the answer, on one line, at the end.
func myAskLines(asks []askView, now time.Time) []string {
	lines := make([]string, len(asks))
	for i, a := range asks {
		lines[i] = askLine(a, now)
		if a.State == "answered" {
			lines[i] = "ANSWERED " + lines[i] + ": " + strconv.Quote(strings.Join(strings.Fields(a.Answer), " "))
		}
	}
	return lines
}

// noteLines is one line per note, as `vh notes` prints it.
func noteLines(notes []noteView, now time.Time) []string {
	lines := make([]string, len(notes))
	for i, n := range notes {
		lines[i] = noteLine(n, now)
	}
	return lines
}

// attentionLine is `attention: asks 2 · done 3 · expired 1 · overdue 0 · conflicts 1`.
func attentionLine(a nowAttention) string {
	return fmt.Sprintf("attention: asks %d · done %d · expired %d · overdue %d · conflicts %d",
		a.OpenAsks, a.DoneNotClosed, a.ExpiredLeases, a.OverdueWatch, a.Conflicts)
}

// nowSection is a heading and up to nowCap lines under it, then the
// `+K more → <command>` line, K counting the lines past the cap and the
// `extra` items the server left out; an empty section is the heading and `none`.
func nowSection(name string, lines []string, extra int, more string) string {
	if len(lines) == 0 {
		return name + ": none\n"
	}
	var b strings.Builder
	b.WriteString(name + ":\n")
	for i, line := range lines {
		if i == nowCap {
			break
		}
		b.WriteString("  " + line + "\n")
	}
	if left := max(len(lines)-nowCap, 0) + extra; left > 0 {
		b.WriteString("  +" + strconv.Itoa(left) + " more → " + more + "\n")
	}
	return b.String()
}

// myLeaseLine is `L7  <task>  <title>  "<intent>"  res a,b  exp 1h52m`.
func myLeaseLine(l nowLease, now time.Time) string {
	return joinLease([]string{l.Ref, l.Task, l.Title, strconv.Quote(l.Intent)}, l.leaseView, now)
}

// otherLeaseLine is `L5  <task>  <holder>  "<intent>"  res a,b  exp 41m`.
func otherLeaseLine(l leaseView, now time.Time) string {
	return joinLease([]string{l.Ref, l.Task, l.Actor, strconv.Quote(l.Intent)}, l, now)
}

// joinLease ends the parts with the resources and the time left.
func joinLease(parts []string, l leaseView, now time.Time) string {
	if len(l.Resources) > 0 {
		parts = append(parts, "res "+strings.Join(l.Resources, ","))
	}
	return strings.Join(append(parts, leaseEnding(l, now)), "  ")
}
