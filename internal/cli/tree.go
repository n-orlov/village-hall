package cli

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// commandTree is every command the client binary has, as the words that name
// it, with the flags it accepts. The flags are the tables the parsers read, so
// a flag in the tree is a flag the command takes.
var commandTree = map[string][]flagSpec{
	"version":           nil,
	"serve":             nil,
	"healthcheck":       healthcheckFlags,
	"update":            updateFlags,
	"whoami":            whoamiFlags,
	"init add":          initAddFlags,
	"init ls":           initLsFlags,
	"init show":         initShowFlags,
	"init edit":         initEditFlags,
	"init close":        initCloseFlags,
	"init reopen":       initReopenFlags,
	"task add":          taskAddFlags,
	"task ls":           taskLsFlags,
	"task show":         taskShowFlags,
	"task edit":         taskEditFlags,
	"task done":         taskDoneFlags,
	"task reopen":       taskReopenFlags,
	"task close":        taskCloseFlags,
	"task tag":          taskTagFlags,
	"task untag":        taskTagFlags,
	"task dep add":      taskDepFlags,
	"task dep rm":       taskDepFlags,
	"task events":       taskEventsFlags,
	"lease start":       leaseStartFlags,
	"lease renew":       leaseRenewFlags,
	"lease end":         leaseEndFlags,
	"lease ack":         leaseEndFlags,
	"lease ls":          leaseLsFlags,
	"start":             startFlags,
	"new":               newFlags,
	"finish":            finishFlags,
	"block":             blockFlags,
	"ping":              pingFlags,
	"catchup":           catchupFlags,
	"now":               nowFlags,
	"milestone":         milestoneFlags,
	"check":             checkFlags,
	"events":            eventsFlags,
	"ask":               askFlags,
	"ask show":          askShowFlags,
	"ask ls":            askLsFlags,
	"ask withdraw":      askDropFlags,
	"answer":            answerFlags,
	"note":              noteFlags,
	"note show":         noteShowFlags,
	"notes":             notesFlags,
	"attention":         attentionFlags,
	"help agent":        helpAgentFlags,
	"admin migrate":     nil,
	"admin keys create": {{"--expires", true}},
	"admin keys list":   nil,
	"admin keys revoke": nil,
}

// Commands returns the command tree: each command, as its words joined by a
// space, with the names of its flags.
func Commands() map[string][]string {
	tree := make(map[string][]string, len(commandTree))
	for cmd, specs := range commandTree {
		names := make([]string, len(specs))
		for i, s := range specs {
			names[i] = s.Name
		}
		sort.Strings(names)
		tree[cmd] = names
	}
	return tree
}

var (
	commandSpan = regexp.MustCompile("`(vh [^`]*)`")
	flagToken   = regexp.MustCompile(`--[a-z][a-z-]*`)
	wordToken   = regexp.MustCompile(`^[a-z]+$`)
)

// UnknownReferences reads text, which names commands as `vh <words> …` in
// backticks, and returns one message for each command the tree does not have
// and each flag the named command does not take. No message means the text
// mentions only what this build has.
func UnknownReferences(text string, tree map[string][]string) []string {
	var problems []string
	for _, m := range commandSpan.FindAllStringSubmatch(text, -1) {
		span := m[1]
		var words []string
		for _, tok := range strings.Fields(span)[1:] {
			if !wordToken.MatchString(tok) {
				break
			}
			words = append(words, tok)
		}
		cmd := strings.Join(words, " ")
		flags, ok := tree[cmd]
		if !ok {
			problems = append(problems, fmt.Sprintf("unknown command %q in `%s`", "vh "+cmd, span))
			continue
		}
		for _, f := range flagToken.FindAllString(span, -1) {
			if !slices.Contains(flags, f) {
				problems = append(problems, fmt.Sprintf("unknown flag %s for %q in `%s`", f, "vh "+cmd, span))
			}
		}
	}
	return problems
}

// ProtocolText is the agent protocol cheat sheet the binary embeds.
func ProtocolText() string { return protocolText }
