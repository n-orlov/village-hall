// a11ypass_test.go pins the two axe passes of e2e/a11y.spec.ts (task 027, R9 g): the light pass runs axe's full rule
// set on every state, the dark pass runs the colour rules only (color-contrast, and at most axe's other colour rule,
// link-in-text-block), since the theme changes colours and not the document. It reads the spec and fails on a spec
// whose dark pass runs the full set, whose light pass is restricted, that disables or excludes anything, or that
// goes back to a `beforeEach` that loads a page before every test.
package suitecheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	a11yLineComment  = regexp.MustCompile(`(?m)//[^\n]*`)
	a11yBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	a11yPasses       = regexp.MustCompile(`(?s)===\s*'dark'\s*\?\s*await\s+(new AxeBuilder\(\{\s*page\s*\}\)[^:;]*?)\.analyze\(\)\s*:\s*await\s+(new AxeBuilder\(\{\s*page\s*\}\)[^;]*?)\.analyze\(\)\s*;`)
	a11yDarkRules    = regexp.MustCompile(`(?s)const\s+DARK_PASS_RULES\s*=\s*\[([^\]]*)\]`)
	a11yQuoted       = regexp.MustCompile(`'([^']*)'|"([^"]*)"`)
)

// a11yRestrictions are the AxeBuilder calls that narrow a scan; the spec makes none of them except the dark
// pass's one withRules.
var a11yRestrictions = []string{".disableRules(", ".withTags(", ".exclude(", ".include(", ".options("}

func squash(s string) string { return strings.Join(strings.Fields(s), "") }

// a11yPassProblem returns "" when spec (the text of e2e/a11y.spec.ts) runs the full rule set in the light pass and
// only colour rules in the dark pass, otherwise what is wrong.
func a11yPassProblem(spec string) string {
	code := a11yBlockComment.ReplaceAllString(spec, "")
	code = a11yLineComment.ReplaceAllString(code, "")
	for _, call := range a11yRestrictions {
		if strings.Contains(code, call) {
			return "a11y.spec.ts calls " + call + ") on an AxeBuilder: no rule is disabled and no element or tag is excluded"
		}
	}
	if n := strings.Count(code, "new AxeBuilder("); n != 2 {
		return "a11y.spec.ts must build exactly two AxeBuilders (the light and the dark pass), found " + strconv.Itoa(n)
	}
	if n := strings.Count(code, ".withRules("); n != 1 {
		return "a11y.spec.ts must restrict rules in exactly one place, the dark pass; found " + strconv.Itoa(n) + " withRules calls"
	}
	m := a11yPasses.FindStringSubmatch(code)
	if m == nil {
		return "a11y.spec.ts has no `theme === 'dark' ? <dark AxeBuilder> : <light AxeBuilder>` choice that ends in analyze()"
	}
	if dark := squash(m[1]); dark != "newAxeBuilder({page}).withRules(DARK_PASS_RULES)" {
		return "the dark pass must be `new AxeBuilder({ page }).withRules(DARK_PASS_RULES)`, it is `" + dark + "`"
	}
	if light := squash(m[2]); light != "newAxeBuilder({page})" {
		return "the light pass must be the unrestricted `new AxeBuilder({ page })`, it is `" + light + "`"
	}
	list := a11yDarkRules.FindStringSubmatch(code)
	if list == nil {
		return "a11y.spec.ts has no `const DARK_PASS_RULES = [...]` list"
	}
	var rules []string
	for _, q := range a11yQuoted.FindAllStringSubmatch(list[1], -1) {
		rules = append(rules, q[1]+q[2])
	}
	hasContrast := false
	for _, r := range rules {
		switch r {
		case "color-contrast":
			hasContrast = true
		case "link-in-text-block":
		default:
			return "the dark pass may run only the colour rules (color-contrast, link-in-text-block); DARK_PASS_RULES also names '" + r + "'"
		}
	}
	if !hasContrast {
		return "DARK_PASS_RULES must name 'color-contrast'"
	}
	if strings.Contains(code, "beforeEach") {
		return "a11y.spec.ts must seed in a beforeAll and load pages in the tests, not in a beforeEach"
	}
	if !strings.Contains(code, "beforeAll") {
		return "a11y.spec.ts must seed its board once per worker, in a beforeAll"
	}
	return ""
}

func TestA11ySpecRunsFullSetLightAndColourRulesDark(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "e2e", "a11y.spec.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if p := a11yPassProblem(string(raw)); p != "" {
		t.Error(p)
	}
}

// TestA11yPassCheckFailsOnADoctoredSpec doctors the real spec each way the rule can break and requires a named
// problem for every one.
func TestA11yPassCheckFailsOnADoctoredSpec(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "e2e", "a11y.spec.ts"))
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	const (
		darkScan  = "await new AxeBuilder({ page }).withRules(DARK_PASS_RULES).analyze()"
		lightScan = ": await new AxeBuilder({ page }).analyze();"
		rules     = "['color-contrast', 'link-in-text-block']"
	)
	for _, part := range []string{darkScan, lightScan, rules, "test.beforeAll("} {
		if strings.Count(spec, part) != 1 {
			t.Fatalf("e2e/a11y.spec.ts no longer holds %q exactly once: update this test with it", part)
		}
	}
	cases := []struct{ name, from, to, want string }{
		{"dark pass runs the full set", darkScan, "await new AxeBuilder({ page }).analyze()", "exactly one place"},
		{"dark pass restricted by tags only", darkScan, "await new AxeBuilder({ page }).withTags(['wcag2aa']).analyze()", ".withTags("},
		{"light pass restricted to colour contrast", lightScan, ": await new AxeBuilder({ page }).withRules(DARK_PASS_RULES).analyze();", "exactly one place"},
		{"light pass restricted by a rule list", lightScan, ": await new AxeBuilder({ page }).withRules(['region']).analyze();", "exactly one place"},
		{"light pass disables a rule", lightScan, ": await new AxeBuilder({ page }).disableRules(['region']).analyze();", ".disableRules("},
		{"light pass excludes an element", lightScan, ": await new AxeBuilder({ page }).exclude('.vh-tag').analyze();", ".exclude("},
		{"dark pass names a DOM rule", rules, "['color-contrast', 'region']", "also names 'region'"},
		{"dark pass names no colour contrast", rules, "['link-in-text-block']", "must name 'color-contrast'"},
		{"seeding moves back to a beforeEach", "test.beforeAll(", "test.beforeEach(", "beforeEach"},
		{"no seeding hook at all", "test.beforeAll(", "test.skip(", "beforeAll"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doctored := strings.Replace(spec, c.from, c.to, 1)
			p := a11yPassProblem(doctored)
			if p == "" {
				t.Fatalf("a doctored a11y.spec.ts (%s) passed the check", c.name)
			}
			if !strings.Contains(p, c.want) {
				t.Errorf("problem %q does not name %q", p, c.want)
			}
		})
	}
}
