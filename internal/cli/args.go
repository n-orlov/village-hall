package cli

import "strings"

// flagSpec names one flag a client command accepts and whether it takes a value.
// The tables below are the only flags the client commands register: the
// parser accepts nothing else, and a test reads them to keep a key flag out.
type flagSpec struct {
	Name  string
	Value bool
}

var (
	initAddFlags  = []flagSpec{{"--tag", true}, {"--json", false}}
	initLsFlags   = []flagSpec{{"--tag", true}, {"--status", true}, {"--closed-within", true}, {"--limit", true}, {"--all", false}, {"--json", false}}
	initShowFlags = []flagSpec{{"--json", false}}
	initEditFlags = []flagSpec{
		{"--title", true}, {"--description", true}, {"--tag", true}, {"--no-tags", false},
		{"--start", true}, {"--end", true}, {"--clear-start", false}, {"--clear-end", false}, {"--json", false},
	}
	initCloseFlags  = []flagSpec{{"--force", false}, {"--json", false}}
	initReopenFlags = []flagSpec{{"--json", false}}
	taskAddFlags    = []flagSpec{
		{"--description", true}, {"--kind", true}, {"--cadence", true}, {"--after", true},
		{"--start", true}, {"--end", true}, {"--link", true}, {"--tag", true}, {"--json", false},
	}
	taskLsFlags = []flagSpec{
		{"--status", true}, {"--kind", true}, {"--active", false}, {"--blocked", false}, {"--overdue", false},
		{"--q", true}, {"--limit", true}, {"--all", false}, {"--json", false}, {"--attention", false},
	}
	taskShowFlags = []flagSpec{{"--json", false}}
	taskEditFlags = []flagSpec{
		{"--title", true}, {"--description", true}, {"--start", true}, {"--end", true}, {"--cadence", true},
		{"--link", true}, {"--no-links", false}, {"--json", false},
	}
	taskDepFlags    = []flagSpec{{"--json", false}}
	taskTagFlags    = []flagSpec{{"--json", false}}
	taskDoneFlags   = []flagSpec{{"--force", false}, {"--json", false}}
	taskReopenFlags = []flagSpec{{"--json", false}}
	taskCloseFlags  = []flagSpec{{"--resolution", true}, {"--for", true}, {"--json", false}}
	taskEventsFlags = []flagSpec{{"--limit", true}, {"--all", false}, {"--json", false}}
	milestoneFlags  = []flagSpec{{"--json", false}}
	checkFlags      = []flagSpec{{"--next", true}, {"--json", false}}
	eventsFlags     = []flagSpec{
		{"--since", true}, {"--task", true}, {"--initiative", true}, {"--type", true}, {"--actor", true},
		{"--limit", true}, {"--all", false}, {"--json", false},
	}
	leaseStartFlags = []flagSpec{{"--ttl", true}, {"--res", true}, {"--notice", false}, {"--json", false}}
	leaseRenewFlags = []flagSpec{{"--ttl", true}, {"--intent", true}, {"--json", false}}
	leaseEndFlags   = []flagSpec{{"--json", false}}
	leaseLsFlags    = []flagSpec{
		{"--state", true}, {"--mine", false}, {"--task", true}, {"--res", true},
		{"--notice", false}, {"--limit", true}, {"--all", false}, {"--json", false},
	}
	askFlags      = []flagSpec{{"--details", true}, {"--option", true}, {"--poll", true}, {"--stop", false}, {"--blocking", false}, {"--json", false}}
	askShowFlags  = []flagSpec{{"--json", false}}
	askLsFlags    = []flagSpec{{"--state", true}, {"--mine", false}, {"--task", true}, {"--limit", true}, {"--all", false}, {"--json", false}}
	askDropFlags  = []flagSpec{{"--json", false}}
	answerFlags   = []flagSpec{{"--relay", false}, {"--for", true}, {"--json", false}}
	noteFlags     = []flagSpec{{"--body", true}, {"--task", true}, {"--init", true}, {"--res", true}, {"--mention", true}, {"--json", false}}
	noteShowFlags = []flagSpec{{"--json", false}}
	notesFlags    = []flagSpec{
		{"--since", true}, {"--task", true}, {"--init", true}, {"--broadcast", false}, {"--res", true}, {"--mention", true},
		{"--ack", false}, {"--limit", true}, {"--all", false}, {"--json", false},
	}
	attentionFlags = []flagSpec{{"--tag", true}, {"--init", true}, {"--limit", true}, {"--all", false}, {"--json", false}}
	nowFlags       = []flagSpec{{"--resources", true}, {"--json", false}}
	whoamiFlags    = []flagSpec{}
	helpAgentFlags = []flagSpec{{"--version", false}}
)

// parsedArgs is a command line split into positional arguments and flags.
type parsedArgs struct {
	pos    []string
	values map[string][]string
	set    map[string]bool
}

func (p parsedArgs) has(name string) bool { return p.set[name] }

// parseArgs reads args against specs, flags and positionals in any order. A
// flag the table does not name, or a value flag with no value, is not ok. After
// a bare `--` everything is positional.
func parseArgs(args []string, specs []flagSpec) (parsedArgs, bool) {
	p := parsedArgs{values: map[string][]string{}, set: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			p.pos = append(p.pos, args[i+1:]...)
			return p, true
		}
		if !strings.HasPrefix(a, "-") {
			p.pos = append(p.pos, a)
			continue
		}
		var ok bool
		if i, ok = p.flag(args, i, specs); !ok {
			return p, false
		}
	}
	return p, true
}

// flag reads the flag at args[i] and returns the index of its last argument.
func (p parsedArgs) flag(args []string, i int, specs []flagSpec) (int, bool) {
	name, val, hasVal := strings.Cut(args[i], "=")
	spec, found := lookupFlag(specs, name)
	if !found || (hasVal && !spec.Value) {
		return i, false
	}
	p.set[name] = true
	if !spec.Value {
		return i, true
	}
	if !hasVal {
		if i+1 >= len(args) {
			return i, false
		}
		i++
		val = args[i]
	}
	p.values[name] = append(p.values[name], val)
	return i, true
}

func lookupFlag(specs []flagSpec, name string) (flagSpec, bool) {
	for _, s := range specs {
		if s.Name == name {
			return s, true
		}
	}
	return flagSpec{}, false
}
