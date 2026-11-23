// Command pkgtime fails when a Go package's test time in the Suite unit's JUnit
// is over its committed budget (R2, R9 e: the slowest packages stay fast).
//
//	go run ./ci/pkgtime -junit ci-results/junit-go.xml -budgets ci/pkgtime/budgets.json
//
// The budgets file maps a package path relative to the module (internal/server)
// to the most seconds its tests may take. Each budgeted package is read from the
// JUnit file as the <testsuite> gotestsum writes for it, whose time attribute is
// the package's test time: a testsuite named after the package, with or without
// the module prefix. The exit status is 0 when every budgeted package is at or
// under its budget, 1 when one is over or has no testsuite in the file (a
// package that did not run cannot be shown fast), and 2 on a usage or input
// error. A budget is a bound, never a target: it is only ever lowered.
package main

import (
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type suite struct {
	Name string  `xml:"name,attr"`
	Time float64 `xml:"time,attr"`
}

type suites struct {
	Suites []suite `xml:"testsuite"`
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pkgtime", flag.ContinueOnError)
	fs.SetOutput(stderr)
	junit := fs.String("junit", "", "the Suite unit's Go JUnit file")
	budgets := fs.String("budgets", "", "the JSON file of budgets in seconds, by package path")
	if err := fs.Parse(args); err != nil || *junit == "" || *budgets == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: pkgtime -junit <junit-go.xml> -budgets <budgets.json>")
		return 2
	}
	limits, err := readBudgets(*budgets)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "pkgtime:", err)
		return 2
	}
	times, err := readTimes(*junit)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "pkgtime:", err)
		return 2
	}
	if !report(stdout, *junit, limits, times) {
		return 1
	}
	return 0
}

// report prints one line per budgeted package, in path order, and says whether
// every one is at or under its budget.
func report(stdout io.Writer, junit string, limits map[string]float64, times []suite) bool {
	pkgs := make([]string, 0, len(limits))
	for p := range limits {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	allOK := true
	for _, p := range pkgs {
		took, ok := lookup(times, p)
		switch {
		case !ok:
			allOK = false
			_, _ = fmt.Fprintf(stdout, "FAIL %s: no testsuite in %s, so its test time is unknown (budget %g s)\n", p, junit, limits[p])
		case took > limits[p]:
			allOK = false
			_, _ = fmt.Fprintf(stdout, "FAIL %s: %g s of test time, over its budget of %g s\n", p, took, limits[p])
		default:
			_, _ = fmt.Fprintf(stdout, "ok   %s: %g s of test time, within its budget of %g s\n", p, took, limits[p])
		}
	}
	return allOK
}

func readBudgets(path string) (map[string]float64, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the budgets path is the tool's own command-line argument
	if err != nil {
		return nil, err
	}
	var limits map[string]float64
	if err := json.Unmarshal(b, &limits); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(limits) == 0 {
		return nil, fmt.Errorf("%s: no budget", path)
	}
	for p, v := range limits {
		if v <= 0 {
			return nil, fmt.Errorf("%s: the budget of %s is %g, want above zero", path, p, v)
		}
	}
	return limits, nil
}

func readTimes(path string) ([]suite, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the JUnit path is the tool's own command-line argument
	if err != nil {
		return nil, err
	}
	var doc suites
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return doc.Suites, nil
}

// lookup is the test time of pkg: the testsuite named pkg or ending in /pkg.
func lookup(all []suite, pkg string) (float64, bool) {
	for _, s := range all {
		if s.Name == pkg || strings.HasSuffix(s.Name, "/"+pkg) {
			return s.Time, true
		}
	}
	return 0, false
}
