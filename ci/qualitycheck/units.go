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
		label, failed, ended, cut := nextCase(dec)
		switch {
		case cut:
			return cases, true, nil
		case ended:
			return cases, false, nil
		case failed:
			cases = append(cases, label)
		}
	}
}

// nextCase reads the decoder up to the end of the next <testcase>: its label and
// whether it failed. ended is set at the end of the file, cut when the file stops
// mid-element.
func nextCase(dec *xml.Decoder) (label string, failed, ended, cut bool) {
	for {
		start, ended, cut := nextTestcase(dec)
		if ended || cut {
			return "", false, ended, cut
		}
		var tc struct {
			Classname string    `xml:"classname,attr"`
			Name      string    `xml:"name,attr"`
			Failure   *struct{} `xml:"failure"`
			Error     *struct{} `xml:"error"`
		}
		if derr := dec.DecodeElement(&tc, &start); derr != nil {
			return "", false, false, true
		}
		if tc.Failure != nil || tc.Error != nil {
			return caseLabel(tc.Classname, tc.Name), true, false, false
		}
	}
}

// nextTestcase reads the decoder up to the start of the next <testcase>.
func nextTestcase(dec *xml.Decoder) (start xml.StartElement, ended, cut bool) {
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return start, true, false
		}
		if err != nil {
			return start, false, true
		}
		if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "testcase" {
			return el, false, false
		}
	}
}

// caseLabel names a failing case: "classname :: name", the name alone, or a placeholder.
func caseLabel(classname, name string) string {
	switch {
	case classname != "" && name != "":
		return classname + " :: " + name
	case name != "":
		return name
	}
	return "(unnamed test case)"
}

// runUnitsGate reads dir for unit failure summaries and failing JUnit cases. found is
// false when there is nothing to report.
func runUnitsGate(dir string) (res gateResult, found bool) {
	var b strings.Builder
	summarised := readSummaries(dir, &b)

	// A unit with no summary (its run was killed outside ci/unitrun.sh, or its attempt
	// hit the Jenkins limit and the Jenkinsfile left unit-cut-<unit>.txt instead): the
	// failing cases its JUnit files hold, by unit.
	heads, slugs := readCuts(dir, summarised)
	cases := readJUnitCases(dir, summarised, heads, &slugs)
	sort.Strings(slugs)
	for _, slug := range slugs {
		b.WriteString(heads[slug] + "\n")
		b.WriteString(casesSection(slug, cases[slug]))
	}

	if b.Len() == 0 {
		return gateResult{}, false
	}
	b.WriteString("Quality judged the results these units left, which are not a whole run: fix the unit above first.\n")
	return gateResult{name: "units", ok: false, output: b.String()}, true
}

// readSummaries writes every non-empty unit-failure-<slug>.txt of dir to b and
// returns the slugs it wrote.
func readSummaries(dir string, b *strings.Builder) map[string]bool {
	summarised := map[string]bool{}
	summaries, _ := filepath.Glob(filepath.Join(dir, "unit-failure-*.txt"))
	sort.Strings(summaries)
	for _, path := range summaries {
		text := readTrimmed(path)
		if text == "" {
			continue
		}
		summarised[slugOf(path, "unit-failure-")] = true
		b.WriteString(text + "\n")
	}
	return summarised
}

// readCuts reads the unit-cut-<slug>.txt notes of the units no summary covers: the
// line that opens each unit's block, by slug, and the slugs in file order.
func readCuts(dir string, summarised map[string]bool) (heads map[string]string, slugs []string) {
	heads = map[string]string{}
	cuts, _ := filepath.Glob(filepath.Join(dir, "unit-cut-*.txt"))
	sort.Strings(cuts)
	for _, path := range cuts {
		text := readTrimmed(path)
		slug := slugOf(path, "unit-cut-")
		if text == "" || summarised[slug] {
			continue
		}
		heads[slug] = text
		slugs = append(slugs, slug)
	}
	return heads, slugs
}

// readJUnitCases reads the failing cases of every JUnit file whose unit has no
// summary, by unit slug. A unit with failing cases and no head yet gets one, and its
// slug joins slugs.
func readJUnitCases(dir string, summarised map[string]bool, heads map[string]string, slugs *[]string) map[string][]string {
	junits, _ := filepath.Glob(filepath.Join(dir, "junit-*.xml"))
	sort.Strings(junits)
	cases := map[string][]string{} // slug -> report lines
	for _, path := range junits {
		unit := unitOfJUnit(filepath.Base(path))
		slug := unitSlug(unit)
		failed, truncated, ok := unsummarisedFailures(path, unit, summarised[slug])
		if !ok {
			continue
		}
		if _, has := heads[slug]; !has {
			heads[slug] = fmt.Sprintf("unit %s: no failure summary was written (it was cut off or killed outside ci/unitrun.sh)", unit)
			*slugs = append(*slugs, slug)
		}
		cases[slug] = append(cases[slug], caseLines(filepath.Base(path), failed, truncated)...)
	}
	return cases
}

// unsummarisedFailures is the failing cases of a JUnit file of a unit no summary
// covers; ok is false when the file is not a unit's, the unit has a summary, the
// file cannot be read, or it holds nothing to report.
func unsummarisedFailures(path, unit string, summarised bool) (failed []string, truncated, ok bool) {
	if unit == "" || summarised {
		return nil, false, false
	}
	failed, truncated, err := failingCases(path)
	if err != nil {
		return nil, false, false
	}
	return failed, truncated, len(failed) > 0 || truncated
}

// caseLines is one JUnit file's block of the report: its name, then its failing cases.
func caseLines(name string, failed []string, truncated bool) []string {
	note := ""
	if truncated {
		note = " (file cut off: the cases before the cut)"
	}
	lines := []string{fmt.Sprintf("  %s%s:", name, note)}
	for _, c := range failed {
		lines = append(lines, "    "+c)
	}
	if len(failed) == 0 {
		lines = append(lines, "    (no failed test before the cut)")
	}
	return lines
}

// casesSection is the failing-tests part of one unit's block.
func casesSection(slug string, lines []string) string {
	switch {
	case len(lines) > 0:
		return "failing tests read from its JUnit files:\n" + strings.Join(lines, "\n") + "\n"
	case unitOfSlug(slug) != "":
		return "failing tests: none named (no failing case in its JUnit files)\n"
	}
	return ""
}

// readTrimmed is the trimmed text of a file in the results directory, empty when it
// cannot be read.
func readTrimmed(path string) string {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: a unit note in the results directory
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// slugOf is a unit note's slug: its file name without the prefix and the .txt suffix.
func slugOf(path, prefix string) string {
	return strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), prefix), ".txt")
}
