// overflowviewports_test.go keeps e2e/overflow.spec.ts's main viewport loop to one viewport per distinct layout, and
// holds that list against the CSS (task 030, R9 j). The layouts change at the px breakpoints the web UI's media queries
// use (768 and 1024 today), so the spec runs one viewport below the smallest breakpoint, one in every band between
// breakpoints (for every state), and one exactly on each breakpoint. The 1024 px viewport carries `nowOnly: true`: only
// the states whose hash is #/now run there, because only the Now view has a layout of its own from 1024 px up.
//
// The check reads every min-width/max-width px value of a media query under web/src (not a test file, not
// node_modules) and the spec's `const viewports = [...]` list. A new breakpoint without a viewport in its band, or a
// spec list that lost a layout, fails it with the missing viewport named.
//
// SUITECHECK_E2E_DIR points the check at another e2e directory; it defaults to the repository's own e2e/. The web
// sources are always the repository's own web/src.
package suitecheck

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var (
	// A min-width or max-width px value inside a media query's parentheses, `(min-width: 768px)`. A `max-width: 75ch`
	// property or a `max-width: min(10rem, 28vw)` is not a breakpoint: only the parenthesised form with px is read.
	mediaWidth     = regexp.MustCompile(`\(\s*(min|max)-width\s*:\s*([0-9]+(?:\.[0-9]+)?)px\s*\)`)
	viewportsBlock = regexp.MustCompile(`(?s)const viewports = \[(.*?)\] as const;`)
	viewportWidth  = regexp.MustCompile(`size:\s*\{\s*width:\s*(\d+)`)
	viewportNow    = regexp.MustCompile(`nowOnly:\s*(true|false)`)
	webSourceFile  = regexp.MustCompile(`\.(css|svelte|ts|js)$`)
	webTestFile    = regexp.MustCompile(`\.(test|spec)\.(ts|js)$`)
)

type overflowViewport struct {
	width   int
	nowOnly bool
}

// breakpointsIn returns the sorted distinct widths, in px, at which a media query under dir changes the layout: a
// min-width N is the edge N, a max-width N the first width past it (floor(N)+1).
func breakpointsIn(t *testing.T, dir string) []int {
	t.Helper()
	edges := map[int]bool{}
	files := 0
	err := fs.WalkDir(os.DirFS(dir), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if !webSourceFile.MatchString(path) || webTestFile.MatchString(path) {
			return nil
		}
		raw, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			return err
		}
		files++
		for _, m := range mediaWidth.FindAllStringSubmatch(string(raw), -1) {
			px, err := strconv.ParseFloat(m[2], 64)
			if err != nil {
				return err
			}
			if m[1] == "min" {
				edges[int(math.Ceil(px))] = true
			} else {
				edges[int(math.Floor(px))+1] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatalf("no web source file under %s", dir)
	}
	var out []int
	for e := range edges {
		out = append(out, e)
	}
	sort.Ints(out)
	return out
}

// overflowViewports parses the spec's viewport list; every entry names its width and says whether it is nowOnly.
func overflowViewports(spec string) ([]overflowViewport, error) {
	block := viewportsBlock.FindStringSubmatch(spec)
	if block == nil {
		return nil, fmt.Errorf("no `const viewports = [...] as const;` list in the spec")
	}
	var out []overflowViewport
	for _, line := range strings.Split(block[1], "\n") {
		w := viewportWidth.FindStringSubmatch(line)
		if w == nil {
			continue
		}
		n := viewportNow.FindStringSubmatch(line)
		if n == nil {
			return nil, fmt.Errorf("viewport %q does not say nowOnly: true or false", strings.TrimSpace(line))
		}
		width, _ := strconv.Atoi(w[1])
		out = append(out, overflowViewport{width: width, nowOnly: n[1] == "true"})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the spec's viewport list holds no viewport")
	}
	return out, nil
}

// viewportGaps returns one line per layout the viewports do not cover: nothing below the smallest breakpoint, a band
// between breakpoints (the last one open-ended) with no viewport that runs every state, a breakpoint with no viewport
// exactly on it, and a spec that does not gate its nowOnly viewport to the #/now states.
func viewportGaps(breakpoints []int, viewports []overflowViewport, spec string) []string {
	var gaps []string
	if len(breakpoints) == 0 {
		return []string{"web/src has no min-width/max-width px breakpoint: the check reads nothing"}
	}
	lower := 0
	for i := -1; i < len(breakpoints); i++ {
		upper := math.MaxInt
		if i+1 < len(breakpoints) {
			upper = breakpoints[i+1]
		}
		if i >= 0 {
			lower = breakpoints[i]
		}
		covered := false
		for _, v := range viewports {
			if !v.nowOnly && v.width >= lower && v.width < upper {
				covered = true
			}
		}
		if !covered {
			band := fmt.Sprintf("from %d px up", lower)
			if upper != math.MaxInt {
				band = fmt.Sprintf("from %d px up to %d px", lower, upper-1)
			}
			if i < 0 {
				band = fmt.Sprintf("below %d px", upper)
			}
			gaps = append(gaps, "no viewport that runs every state "+band)
		}
	}
	for _, b := range breakpoints {
		on := false
		for _, v := range viewports {
			if v.width == b {
				on = true
			}
		}
		if !on {
			gaps = append(gaps, fmt.Sprintf("no viewport exactly on the %d px breakpoint", b))
		}
	}
	for _, want := range []string{"vp.nowOnly", "s.hash === '#/now'"} {
		if !strings.Contains(spec, want) {
			gaps = append(gaps, "the spec does not gate its nowOnly viewport: "+want+" not found")
		}
	}
	return gaps
}

func overflowSpecText(t *testing.T) (string, string) {
	t.Helper()
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("SUITECHECK_E2E_DIR")
	if dir == "" {
		dir = filepath.Join(root, "e2e")
	}
	raw, err := fs.ReadFile(os.DirFS(dir), "overflow.spec.ts")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), filepath.Join(root, "web", "src")
}

// TestOverflowViewportsCoverEveryCSSBreakpoint holds the real spec against the real web/src.
func TestOverflowViewportsCoverEveryCSSBreakpoint(t *testing.T) {
	spec, webSrc := overflowSpecText(t)
	breakpoints := breakpointsIn(t, webSrc)
	viewports, err := overflowViewports(spec)
	if err != nil {
		t.Fatal(err)
	}
	if gaps := viewportGaps(breakpoints, viewports, spec); len(gaps) > 0 {
		t.Errorf("e2e/overflow.spec.ts's viewports %v do not cover the breakpoints %v of web/src: %s",
			viewports, breakpoints, strings.Join(gaps, "; "))
	}
}

// TestOverflowViewportsAreTheThreeLayoutsPlusDock pins the width list the requirement names: 390, 768 and 1440 for every
// state, 1024 for the Now states only (the wide-layout block of 2560/3440 is a separate list).
func TestOverflowViewportsAreTheThreeLayoutsPlusDock(t *testing.T) {
	spec, _ := overflowSpecText(t)
	viewports, err := overflowViewports(spec)
	if err != nil {
		t.Fatal(err)
	}
	all, now := map[int]bool{}, map[int]bool{}
	for _, v := range viewports {
		if v.nowOnly {
			now[v.width] = true
		} else {
			all[v.width] = true
		}
	}
	if len(all) != 3 || !all[390] || !all[768] || !all[1440] || len(now) != 1 || !now[1024] {
		t.Errorf("every-state widths %v and Now-only widths %v, want 390, 768, 1440 and 1024", all, now)
	}
}

// TestOverflowViewportCheckFailsOnDoctoredInputs: a doctored web/src that adds a 1280 px breakpoint, a doctored spec
// without the 768 px viewport and one that does not gate its Now-only viewport each fail, and the unmodified inputs
// pass, so the check is not vacuously green.
func TestOverflowViewportCheckFailsOnDoctoredInputs(t *testing.T) {
	spec, webSrc := overflowSpecText(t)
	actual := breakpointsIn(t, webSrc)
	viewports, err := overflowViewports(spec)
	if err != nil {
		t.Fatal(err)
	}
	if gaps := viewportGaps(actual, viewports, spec); len(gaps) > 0 {
		t.Fatalf("the real inputs have gaps: %v", gaps)
	}

	// A doctored copy of web/src: a new component with a 1280 px media query.
	doctored := t.TempDir()
	if err := os.WriteFile(filepath.Join(doctored, "styles.css"), []byte("@media (min-width: 768px) { a { b: c } }\n@media (min-width: 1024px) { a { b: c } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := viewportGaps(breakpointsIn(t, doctored), viewports, spec); len(got) != 0 {
		t.Fatalf("a copy with 768 and 1024 only has gaps: %v", got)
	}
	if err := os.WriteFile(filepath.Join(doctored, "wide.css"), []byte("@media (min-width: 1280px) { a { b: c } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withNew := breakpointsIn(t, doctored)
	if len(withNew) != 3 || withNew[2] != 1280 {
		t.Fatalf("doctored breakpoints = %v, want 768, 1024, 1280", withNew)
	}
	gaps := strings.Join(viewportGaps(withNew, viewports, spec), "; ")
	for _, want := range []string{"from 1024 px up to 1279 px", "exactly on the 1280 px breakpoint"} {
		if !strings.Contains(gaps, want) {
			t.Errorf("a new 1280 px breakpoint: gaps %q lack %q", gaps, want)
		}
	}

	// A doctored spec without 768, and one that never gates the Now-only viewport.
	var without []overflowViewport
	for _, v := range viewports {
		if v.width != 768 {
			without = append(without, v)
		}
	}
	gaps = strings.Join(viewportGaps(actual, without, spec), "; ")
	for _, want := range []string{"from 768 px up to 1023 px", "exactly on the 768 px breakpoint"} {
		if !strings.Contains(gaps, want) {
			t.Errorf("a spec without 768: gaps %q lack %q", gaps, want)
		}
	}
	if got := viewportGaps(actual, viewports, strings.ReplaceAll(spec, "s.hash === '#/now'", "true")); len(got) != 1 {
		t.Errorf("a spec that does not pick the Now states: gaps = %v, want exactly one", got)
	}
	// A 1024 px viewport that is nowOnly does not stand in for the every-state viewport of a one-band layout.
	onlyNow := []overflowViewport{{390, false}, {768, true}, {1024, true}, {1440, false}}
	if got := strings.Join(viewportGaps(actual, onlyNow, spec), "; "); !strings.Contains(got, "from 768 px up to 1023 px") {
		t.Errorf("a nowOnly 768 viewport covered the every-state band: %q", got)
	}
	// Below the smallest breakpoint.
	noPhone := []overflowViewport{{768, false}, {1024, true}, {1440, false}}
	if got := strings.Join(viewportGaps(actual, noPhone, spec), "; "); !strings.Contains(got, "below 768 px") {
		t.Errorf("a spec without a phone viewport: gaps %q lack the band below 768 px", got)
	}
}

// TestOverflowViewportListParsing: an entry that does not say whether it is nowOnly, and a missing list, are errors.
func TestOverflowViewportListParsing(t *testing.T) {
	if _, err := overflowViewports("const viewports = [\n  { name: 'a', size: { width: 390, height: 1 }, css: '' },\n] as const;"); err == nil {
		t.Error("an entry without nowOnly was accepted")
	}
	if _, err := overflowViewports("const other = 1;"); err == nil {
		t.Error("a spec without a viewport list was accepted")
	}
	got, err := overflowViewports("const viewports = [\n  { name: 'a', size: { width: 390, height: 1 }, css: '', nowOnly: false },\n  { name: 'b', size: { width: 1024, height: 1 }, css: '', nowOnly: true },\n] as const;")
	if err != nil || len(got) != 2 || got[0] != (overflowViewport{390, false}) || got[1] != (overflowViewport{1024, true}) {
		t.Errorf("parsed %v, %v", got, err)
	}
}
