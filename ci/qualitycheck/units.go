package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// unitResultsDir (-unit-results) is the directory the Jenkinsfile's units left their
// results in (ci-results after Quality unstashed Suite, E2E and Image). Empty turns
// the units gate off.
var unitResultsDir string

// The units gate (task 006, R3). Quality runs after Suite and E2E whatever their
// outcome, and a unit that failed or was cut by its bound leaves partial results
// (ci/unitrun.sh keeps them and writes unit-failure-<unit>.txt). The gate reads those
// results and says which unit failed, how, and which tests had failed, so a Quality
// section about a missing coverage profile or JUnit file is never the only account of
// a unit's failure. It appears only when there is something to report, and a section
// that appears fails: Quality judged results that are not a whole run.

// unitOfJUnit names the unit that writes a JUnit file, by the file's name.
func unitOfJUnit(name string) string {
	switch {
	case strings.HasPrefix(name, "junit-playwright"):
		return "E2E"
	case strings.HasPrefix(name, "junit-vitest"), strings.HasPrefix(name, "junit-go"), strings.HasPrefix(name, "junit-features"):
		return "Suite"
	}
	return ""
}

// unitOfSlug is unitSlug's inverse for the units that write JUnit files.
func unitOfSlug(slug string) string {
	for _, u := range []string{"E2E", "Suite"} {
		if unitSlug(u) == slug {
			return u
		}
	}
	return ""
}

// unitSlug is the unit's name as ci/unitrun.sh slugs it for unit-failure-<slug>.txt.
func unitSlug(unit string) string { return strings.ToLower(unit) }

// failingCases lists every failing <testcase> of one JUnit file as "classname :: name".
// A case with a <failure> or an <error> fails. A file cut off mid-write (the unit was
// killed) yields the cases read before the cut, with truncated set.
func failingCases(path string) (cases []string, truncated bool, err error) {
	f, err := os.Open(path) //nolint:gosec // G304: the path is a JUnit file in the results directory the caller named
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	dec := xml.NewDecoder(f)
	dec.Strict = false
	for {
		tok, terr := dec.Token()
		if errors.Is(terr, io.EOF) {
			return cases, false, nil
		}
		if terr != nil {
			return cases, true, nil
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "testcase" {
			continue
		}
		var tc struct {
			Classname string    `xml:"classname,attr"`
			Name      string    `xml:"name,attr"`
			Failure   *struct{} `xml:"failure"`
			Error     *struct{} `xml:"error"`
		}
		if derr := dec.DecodeElement(&tc, &start); derr != nil {
			return cases, true, nil
		}
		if tc.Failure == nil && tc.Error == nil {
			continue
		}
		switch {
		case tc.Classname != "" && tc.Name != "":
			cases = append(cases, tc.Classname+" :: "+tc.Name)
		case tc.Name != "":
			cases = append(cases, tc.Name)
		default:
			cases = append(cases, "(unnamed test case)")
		}
	}
}

// runUnitsGate reads dir for unit failure summaries and failing JUnit cases. found is
// false when there is nothing to report.
func runUnitsGate(dir string) (res gateResult, found bool) {
	var b strings.Builder
	summarised := map[string]bool{}

	summaries, _ := filepath.Glob(filepath.Join(dir, "unit-failure-*.txt"))
	sort.Strings(summaries)
	for _, path := range summaries {
		raw, err := os.ReadFile(path) //nolint:gosec // G304: a unit summary in the results directory
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(raw))
		if text == "" {
			continue
		}
		slug := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "unit-failure-"), ".txt")
		summarised[slug] = true
		b.WriteString(text + "\n")
		found = true
	}

	// A unit with no summary (its run was killed outside ci/unitrun.sh, or its attempt
	// hit the Jenkins limit and the Jenkinsfile left unit-cut-<unit>.txt instead): the
	// failing cases its JUnit files hold, by unit.
	cuts, _ := filepath.Glob(filepath.Join(dir, "unit-cut-*.txt"))
	sort.Strings(cuts)
	heads := map[string]string{} // slug -> the line that opens the unit's block
	var slugs []string
	for _, path := range cuts {
		raw, err := os.ReadFile(path) //nolint:gosec // G304: a unit note in the results directory
		if err != nil || strings.TrimSpace(string(raw)) == "" {
			continue
		}
		slug := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "unit-cut-"), ".txt")
		if !summarised[slug] {
			heads[slug] = strings.TrimSpace(string(raw))
			slugs = append(slugs, slug)
		}
	}
	junits, _ := filepath.Glob(filepath.Join(dir, "junit-*.xml"))
	sort.Strings(junits)
	cases := map[string][]string{} // slug -> report lines
	for _, path := range junits {
		unit := unitOfJUnit(filepath.Base(path))
		slug := unitSlug(unit)
		if unit == "" || summarised[slug] {
			continue
		}
		failed, truncated, err := failingCases(path)
		if err != nil || (len(failed) == 0 && !truncated) {
			continue
		}
		if _, ok := heads[slug]; !ok {
			heads[slug] = fmt.Sprintf("unit %s: no failure summary was written (it was cut off or killed outside ci/unitrun.sh)", unit)
			slugs = append(slugs, slug)
		}
		note := ""
		if truncated {
			note = " (file cut off: the cases before the cut)"
		}
		cases[slug] = append(cases[slug], fmt.Sprintf("  %s%s:", filepath.Base(path), note))
		for _, c := range failed {
			cases[slug] = append(cases[slug], "    "+c)
		}
		if len(failed) == 0 {
			cases[slug] = append(cases[slug], "    (no failed test before the cut)")
		}
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		found = true
		b.WriteString(heads[slug] + "\n")
		if lines := cases[slug]; len(lines) > 0 {
			b.WriteString("failing tests read from its JUnit files:\n" + strings.Join(lines, "\n") + "\n")
		} else if unitOfSlug(slug) != "" {
			b.WriteString("failing tests: none named (no failing case in its JUnit files)\n")
		}
	}

	if !found {
		return gateResult{}, false
	}
	b.WriteString("Quality judged the results these units left, which are not a whole run: fix the unit above first.\n")
	return gateResult{name: "units", ok: false, output: b.String()}, true
}
