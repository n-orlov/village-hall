// e2eunit_test.go probes the Jenkinsfile's E2E unit and its timeout policy
// (operator ruling 001-WAVE, task 035): Playwright runs in a unit of its own,
// beside Suite and not inside it, with its own bound; a unit that runs out its
// own bound on a node fails and is not retried while a wait with no node stays
// an infrastructure failure with its retries; Quality reads both units' results
// and names a unit that left none. Each property is a pure function of the
// Jenkinsfile text, fed the committed file and doctored copies, as in
// jenkinsfile_test.go.
package repocheck

import (
	"regexp"
	"strings"
	"testing"
)

// coreUnitDecl returns the text of the Core unit declaration `units << [name:
// '<name>'` up to the next declaration (or the end of the units), comment
// lines left out.
func coreUnitDecl(core, name string) (string, bool) {
	start := strings.Index(core, "units << [name: '"+name+"'")
	if start < 0 {
		return "", false
	}
	rest := core[start+1:]
	end := len(rest)
	for _, stop := range []string{"units << [", "def branches"} {
		if i := strings.Index(rest, stop); i >= 0 && i < end {
			end = i
		}
	}
	var code []string
	for _, l := range strings.Split(core[start:start+1+end], "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "//") {
			code = append(code, l)
		}
	}
	return strings.Join(code, "\n"), true
}

var unitMinutesRE = regexp.MustCompile(`minutes: ([^,]+),`)

// functionBody returns the text of the Jenkinsfile function whose signature
// starts with sig, up to the next top-level function or the pipeline.
func functionBody(jf, sig string) string {
	i := strings.Index(jf, sig)
	if i < 0 {
		return ""
	}
	rest := jf[i+len(sig):]
	end := len(rest)
	if j := strings.Index(rest, "\n}\n"); j >= 0 {
		end = j
	}
	return rest[:end]
}

// e2eUnitProblems: Playwright is not inside Suite and is the whole of E2E, and
// the bounds are Suite 45 (120 EXTENDED) and E2E 40.
func e2eUnitProblems(jf string) []string {
	var problems []string
	core, _ := jenkinsStage(jf, "Core")
	suite, ok := coreUnitDecl(core, "Suite")
	if !ok {
		return []string{"Core has no Suite unit"}
	}
	e2e, ok := coreUnitDecl(core, "E2E")
	if !ok {
		return []string{"Core has no E2E unit"}
	}
	if !strings.Contains(suite, "script: 'BOARD_CI_PLAYWRIGHT=skip ci/run.sh ci/suite.sh'") {
		problems = append(problems, "Suite does not ask ci/suite.sh to skip Playwright (BOARD_CI_PLAYWRIGHT=skip)")
	}
	if strings.Contains(suite, "BOARD_CI_PLAYWRIGHT=only") || strings.Contains(strings.ToLower(suite), "npx") {
		problems = append(problems, "Suite still runs Playwright")
	}
	if !strings.Contains(e2e, "script: 'BOARD_CI_PLAYWRIGHT=only ci/run.sh ci/suite.sh'") {
		problems = append(problems, "E2E does not run only the Playwright step (BOARD_CI_PLAYWRIGHT=only)")
	}
	if m := unitMinutesRE.FindStringSubmatch(suite); m == nil || m[1] != "nightly ? 120 : 45" {
		problems = append(problems, "Suite's bound is not 45 minutes (120 EXTENDED)")
	}
	if m := unitMinutesRE.FindStringSubmatch(e2e); m == nil || m[1] != "40" {
		problems = append(problems, "E2E's bound is not 40 minutes on push and EXTENDED builds")
	}
	if !strings.Contains(e2e, "signal: 'E2E'") {
		problems = append(problems, "E2E does not signal Quality")
	}
	// The ruling's still-banned list: nothing in the pipeline shards or
	// parallelises Playwright, or retries it (ci/suite.sh runs it once).
	for _, banned := range []string{"--shard", "fullyParallel", "--workers", "--retries", "--repeat-each", "--timeout"} {
		if strings.Contains(jf, banned) {
			problems = append(problems, "the Jenkinsfile passes "+banned+" to Playwright")
		}
	}
	return problems
}

func TestJenkinsfileRunsPlaywrightInItsOwnUnit(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, e2eUnitProblems, map[string]string{
		"Playwright inside Suite":     replaceOnce(t, jf, "BOARD_CI_PLAYWRIGHT=skip ci/run.sh ci/suite.sh", "ci/run.sh ci/suite.sh"),
		"E2E running everything":      replaceOnce(t, jf, "BOARD_CI_PLAYWRIGHT=only ci/run.sh ci/suite.sh", "ci/run.sh ci/suite.sh"),
		"an E2E bound of 45":          replaceOnce(t, jf, "name: 'E2E', minutes: 40,", "name: 'E2E', minutes: 45,"),
		"an E2E bound of 120":         replaceOnce(t, jf, "name: 'E2E', minutes: 40,", "name: 'E2E', minutes: 120,"),
		"an E2E bound of 180":         replaceOnce(t, jf, "name: 'E2E', minutes: 40,", "name: 'E2E', minutes: 180,"),
		"a Suite bound of 60":         replaceOnce(t, jf, "minutes: nightly ? 120 : 45, cpus: 4, signal: 'Suite'", "minutes: nightly ? 120 : 60, cpus: 4, signal: 'Suite'"),
		"a Suite bound of 120 always": replaceOnce(t, jf, "minutes: nightly ? 120 : 45, cpus: 4, signal: 'Suite'", "minutes: 120, cpus: 4, signal: 'Suite'"),
		"E2E not signalling Quality":  replaceOnce(t, jf, "signal: 'E2E'", "signal: 'Other'"),
		"a sharded Playwright":        replaceOnce(t, jf, "BOARD_CI_PLAYWRIGHT=only ci/run.sh ci/suite.sh", "BOARD_CI_PLAYWRIGHT=only ci/run.sh ci/suite.sh --shard=1/2"),
	})
}

// timeoutPolicyProblems: a unit that ran out its own bound on a node is a
// failure that runUnit does not retry, before it reaches the branch that decides on a rerun;
// a wait with no node is infrastructure and keeps its retries.
func timeoutPolicyProblems(jf string) []string {
	var problems []string
	classify := functionBody(jf, "String classify(Throwable e, Map a) {")
	if !strings.Contains(classify, "return a.node ? 'fail:timeout' : 'infra:no-node'") {
		problems = append(problems, "classify does not make a timeout on a node a failure and one with no node an infrastructure failure")
	}
	if strings.Contains(classify, "infra:timeout") {
		problems = append(problems, "classify still calls a unit's own timeout infrastructure")
	}
	run := functionBody(jf, "def runUnit(Map u) {")
	own := strings.Index(run, "if (a.outcome == 'fail:timeout') {")
	rerun := strings.Index(run, "if (!again) {")
	if own < 0 {
		problems = append(problems, "runUnit has no branch for a unit's own timeout")
	} else {
		branch := run[own:]
		if end := strings.Index(branch, "\n    }\n"); end >= 0 {
			branch = branch[:end]
		}
		if !strings.Contains(branch, "results[u.name] = 'fail'") || !strings.Contains(branch, "error(") {
			problems = append(problems, "a unit's own timeout does not fail the unit")
		}
		if strings.Contains(branch, "continue") || strings.Contains(branch, "rerun = true") {
			problems = append(problems, "a unit's own timeout is retried")
		}
		if rerun >= 0 && own > rerun {
			problems = append(problems, "the own-timeout branch comes after the rerun branch, which would rerun it")
		}
	}
	if strings.Contains(run, "45 + u.minutes") {
		problems = append(problems, "queue wait is capped at the unit's bound plus 45 minutes again, not at 30 (queuecap_test.go pins the cap)")
	}
	if !strings.Contains(run, "infra++") || !strings.Contains(run, "if (infra >= 3 || attempts.size() >= maxAttempts) {") {
		problems = append(problems, "infrastructure failures no longer keep their retries")
	}
	return problems
}

func TestJenkinsfileDoesNotRetryAUnitsOwnTimeout(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, timeoutPolicyProblems, map[string]string{
		"own timeout counted as infra": replaceOnce(t, jf, "return a.node ? 'fail:timeout' : 'infra:no-node'", "return a.node ? 'infra:timeout' : 'infra:no-node'"),
		"no-node timeout not infra":    replaceOnce(t, jf, "return a.node ? 'fail:timeout' : 'infra:no-node'", "return 'fail:timeout'"),
		"no own-timeout branch":        replaceOnce(t, jf, "if (a.outcome == 'fail:timeout') {", "if (a.outcome == 'fail:never') {"),
		"own timeout rerun":            replaceOnce(t, jf, "      results[u.name] = 'fail'\n      error(\"${u.name}: timed out", "      rerun = true\n      continue\n      results[u.name] = 'fail'\n      error(\"${u.name}: timed out"),
		"infra retries dropped":        replaceOnce(t, jf, "if (infra >= 3 || attempts.size() >= maxAttempts) {", "if (true) {"),
	})
}

// qualityReadsBothUnitsProblems: Quality waits for and unstashes both Suite's
// and E2E's results, and a unit that left no stash is named.
func qualityReadsBothUnitsProblems(jf string) []string {
	var problems []string
	core, _ := jenkinsStage(jf, "Core")
	i := strings.Index(core, "stage('Quality') {")
	if i < 0 {
		return []string{"Core has no Quality stage"}
	}
	quality := core[i:]
	for _, want := range []string{
		"signals['Suite'] == true && signals['E2E'] == true",
		"needs: ['res-suite', 'res-e2e', 'res-image', 'image', 'web-dist']",
		"def lost = ['Suite', 'E2E', 'Image'].findAll { !stashedUnits[it] }",
		"error('Quality: no results to read from ' + lost.join(', ')",
	} {
		if !strings.Contains(quality, want) {
			problems = append(problems, "Quality lacks "+want)
		}
	}
	if !strings.Contains(jf, "stashedUnits[u.name] = true") {
		problems = append(problems, "runUnit does not record which units stashed their results")
	}
	return problems
}

func TestJenkinsfileQualityReadsBothSuiteAndE2E(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, qualityReadsBothUnitsProblems, map[string]string{
		"Quality not waiting for E2E": replaceOnce(t, jf, " && signals['E2E'] == true", ""),
		"no E2E stash read":           replaceOnce(t, jf, "'res-suite', 'res-e2e',", "'res-suite',"),
		"no Suite stash read":         replaceOnce(t, jf, "'res-suite', 'res-e2e',", "'res-e2e',"),
		"a missing stash not named":   replaceOnce(t, jf, "error('Quality: no results to read from '", "error('Quality: failed'"),
		"stashes not recorded":        strings.ReplaceAll(jf, "stashedUnits[u.name] = true", ""),
	})
}

// partialResultsProblems: an attempt cut off before its stash (its own bound,
// a lost agent) archives the logs and JUnit files its command wrote so far,
// since the post report wipes ci-results in the same workspace and E2E's first
// run is otherwise the only record of which tests failed.
func partialResultsProblems(jf string) []string {
	var problems []string
	run := functionBody(jf, "def runUnit(Map u) {")
	if !strings.Contains(run, "a.stashed = true") {
		problems = append(problems, "runUnit does not record that an attempt stashed")
	}
	i := strings.Index(run, "if (!a.stashed && a.outcome == null) {")
	if i < 0 {
		return append(problems, "runUnit does not archive an attempt cut off before its stash")
	}
	if !strings.Contains(run[i:], "archiveArtifacts artifacts: 'ci-results/*.log, ci-results/junit-*.xml") {
		problems = append(problems, "a cut-off attempt does not archive its logs and JUnit files")
	}
	return problems
}

func TestJenkinsfileArchivesACutOffUnitsPartialResults(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, partialResultsProblems, map[string]string{
		"stash not recorded": replaceOnce(t, jf, "              a.stashed = true\n", ""),
		"no archive branch":  replaceOnce(t, jf, "if (!a.stashed && a.outcome == null) {", "if (false) {"),
		"logs not archived":  replaceOnce(t, jf, "archiveArtifacts artifacts: 'ci-results/*.log, ci-results/junit-*.xml", "archiveArtifacts artifacts: 'ci-results/none"),
	})
}

// cutUnitStashProblems (task 006): an attempt cut off before its stash also stashes
// what its command wrote, with a note naming how it was cut, so Quality and the report
// read the partial JUnit files (ci/qualitycheck's units gate) instead of a missing
// stash; and the E2E unit writes a partial JUnit file as tests end, since Playwright's
// own file only exists once its run ends.
func cutUnitStashProblems(jf, config, suite string) []string {
	var problems []string
	run := functionBody(jf, "def runUnit(Map u) {")
	i := strings.Index(run, "if (!a.stashed && a.outcome == null) {")
	if i < 0 {
		return append(problems, "runUnit has no branch for an attempt cut off before its stash")
	}
	branch := run[i:]
	for _, want := range []string{"unit-cut-${slug(u.name)}.txt", "stash name: 'res-' + slug(u.name)", "stashedUnits[u.name] = true"} {
		if !strings.Contains(branch, want) {
			problems = append(problems, "a cut-off attempt does not keep its results: no "+want)
		}
	}
	if !strings.Contains(config, "'./partial-junit-reporter.ts'") {
		problems = append(problems, "e2e/playwright.config.ts has no partial JUnit reporter")
	}
	if !strings.Contains(suite, `E2E_PARTIAL_JUNIT="$playwright_partial"`) || !strings.Contains(suite, `playwright_partial="$outdir/junit-playwright-partial.xml"`) {
		problems = append(problems, "ci/suite.sh does not point the partial JUnit reporter at junit-playwright-partial.xml")
	}
	return problems
}

func TestJenkinsfileStashesACutOffUnitsPartialResults(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	config := readRepoFile(t, "e2e/playwright.config.ts")
	suite := readRepoFile(t, "ci/suite.sh")
	if problems := cutUnitStashProblems(jf, config, suite); len(problems) > 0 {
		t.Fatalf("%s", strings.Join(problems, "\n"))
	}
	for name, mutated := range map[string][3]string{
		"no cut note":        {replaceOnce(t, jf, "ci-results/unit-cut-${slug(u.name)}.txt\n", "ci-results/none\n"), config, suite},
		"no partial stash":   {replaceOnce(t, jf, "stash name: 'res-' + slug(u.name), includes: 'ci-results/**',\n                        excludes", "stash name: 'none', includes: 'x',\n                        excludes"), config, suite},
		"no reporter":        {jf, replaceOnce(t, config, "'./partial-junit-reporter.ts'", "'./none.ts'"), suite},
		"reporter not wired": {jf, config, replaceOnce(t, suite, `E2E_PARTIAL_JUNIT="$playwright_partial"`, "E2E_NONE=1")},
	} {
		if len(cutUnitStashProblems(mutated[0], mutated[1], mutated[2])) == 0 {
			t.Errorf("%s: the probe did not notice", name)
		}
	}
}
