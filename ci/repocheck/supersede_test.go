// supersede_test.go pins R8's scheduling rule in the Jenkinsfile: a newer push
// build of a branch stops the branch's older build (abort-previous, per
// branch), and main's nightly (timer) and parameterized (EXTENDED, MUTATION,
// MUTATION_FULL) builds are never stopped by a push. Plain text matching on
// the Jenkinsfile, like jenkinsfile_test.go: supersedeProblems is a pure
// function of the file text, fed the committed file and doctored copies.
package repocheck

import (
	"regexp"
	"strings"
	"testing"
)

// supersedeProblems: the job property is disableConcurrentBuilds with
// abortPrevious taken from supersedable() (never a constant true, which would
// let a push stop a nightly, and never the plain form, which never stops
// anything); supersedable() is true for a push build only (a branch push event
// or branch indexing), and false for a timer-triggered build and for one with
// EXTENDED, MUTATION or MUTATION_FULL set; a failure to tell means false; and
// a superseded build ends ABORTED (supersededEndProblems).
func supersedeProblems(jf string) []string {
	var problems []string
	if !strings.Contains(jf, "\n    disableConcurrentBuilds(abortPrevious: supersedable())\n") {
		problems = append(problems, "the options lack disableConcurrentBuilds(abortPrevious: supersedable())")
	}
	if strings.Contains(jf, "disableConcurrentBuilds()") || strings.Contains(jf, "abortPrevious: true") {
		problems = append(problems, "disableConcurrentBuilds is plain or aborts unconditionally")
	}
	body, ok := groovyFunction(jf, "boolean supersedable()")
	if !ok {
		return append(problems, "the Jenkinsfile has no supersedable() function")
	}
	for _, want := range []string{
		"boolean timer = !currentBuild.getBuildCauses('hudson.triggers.TimerTrigger$TimerTriggerCause').isEmpty()",
		"getBuildCauses('jenkins.branch.BranchEventCause')",
		"params.EXTENDED == true",
		"params.MUTATION == true",
		"params.MUTATION_FULL == true",
		"return pushed && !timer && !parameterized",
		"} catch (Throwable e) {\n    return false\n  }",
	} {
		if !strings.Contains(body, want) {
			problems = append(problems, "supersedable() lacks "+want)
		}
	}
	if strings.Count(body, "return ") != 2 {
		problems = append(problems, "supersedable() has a return other than the verdict and the failure to tell")
	}
	return append(problems, supersededEndProblems(jf)...)
}

// supersededEndProblems: Jenkins stops a superseded build with the result
// NOT_BUILT and a CancelledCause ("Superseded by #N"), terminates the steps of
// one still running seconds later and then kills it, so the Jenkinsfile:
// recognises the stop by that cause or by a newer build of the branch existing
// (isSuperseded, supersededBy); lets it propagate from classify with no retry;
// ends the build ABORTED (endSuperseded) from the attempt that sees the stop,
// from the check after every attempt (no rerun, no verdict) and from
// post { always } before the report, which a superseded build skips so it ends
// before Jenkins kills it; takes the Allure history from the newest build that
// is neither ABORTED nor NOT_BUILT; keeps nowMs() @NonCPS, since the timing
// table a superseded build prints (timingReport, @NonCPS) calls it for an
// attempt still running; and never reads an interruption's result, which the
// script sandbox refuses (FlowInterruptedException.getResult).
func supersededEndProblems(jf string) []string {
	var problems []string
	is, ok := groovyFunction(jf, "boolean isSuperseded()")
	if !ok || !strings.Contains(is, "  try {\n    return currentBuild.nextBuild != null\n  } catch (Throwable e) {\n    return false\n  }") {
		problems = append(problems, "isSuperseded() does not test for a newer build of the branch")
	}
	by, ok := groovyFunction(jf, "boolean supersededBy(Throwable e)")
	if !ok || !strings.Contains(by, "String causes = interruptionCauses(e)\n  return causes.contains('CancelledCause') || causes.contains('Superseded by') || isSuperseded()") {
		problems = append(problems, "supersededBy() does not recognise abortPrevious's stop")
	}
	classify, ok := groovyFunction(jf, "String classify(Throwable e, Map a)")
	if !ok || !strings.Contains(classify, "if (supersededBy(e)) {\n      return null\n    }") {
		problems = append(problems, "classify does not let a superseded build's interruption propagate")
	}
	end, ok := groovyFunction(jf, "def endSuperseded()")
	if !ok || !strings.Contains(end, "  }\n  currentBuild.result = 'ABORTED'") {
		problems = append(problems, "endSuperseded() does not set the result ABORTED")
	}
	run, ok := groovyFunction(jf, "def runUnit(Map u)")
	if !ok || !strings.Contains(run, "        a.outcome = 'aborted'\n        a.end = nowMs()\n        if (supersededBy(e)) {\n          endSuperseded()\n        }\n        throw e\n") {
		problems = append(problems, "an attempt stopped by a newer build does not end the build ABORTED")
	}
	if !strings.Contains(run, "    if (isSuperseded()) {\n      endSuperseded()\n      results[u.name] = 'fail'\n      error(") {
		problems = append(problems, "an attempt a newer build cut short can be rerun or judged")
	}
	_, post, _ := strings.Cut(jf, "\n  post {\n    always {\n      script {\n")
	skip := strings.Index(post, "        if (isSuperseded() || currentBuild.currentResult == 'NOT_BUILT') {\n          endSuperseded()\n          echo timingReport()\n          return\n        }\n")
	if skip < 0 || skip > strings.Index(post, "node(AMD)") {
		problems = append(problems, "post { always } does not end a superseded build ABORTED before the report")
	}
	history, ok := groovyFunction(jf, "String historyBuild()")
	if !ok || !strings.Contains(history, "(b.result == 'ABORTED' || b.result == 'NOT_BUILT')") ||
		!strings.Contains(post, "selector: historyFrom ? specific(historyFrom) : lastCompleted(),") {
		problems = append(problems, "the Allure history can come from a superseded build, which has none")
	}
	problems = append(problems, fieldGetterProblems(jf)...)
	if !strings.Contains(jf, "\n@NonCPS\nlong nowMs() {") {
		problems = append(problems, "nowMs() is not @NonCPS, so timingReport() (a superseded build's timing table) gets no value back from it")
	}
	if strings.Contains(jf, "e.result") || strings.Contains(jf, ".getResult()") {
		problems = append(problems, "the Jenkinsfile reads an interruption's result, which the sandbox refuses")
	}
	return problems
}

var (
	fieldRE    = regexp.MustCompile(`(?m)^@Field (?:final )?\S+ (\w+)`)
	functionRE = regexp.MustCompile(`(?m)^(?:@NonCPS\n)?\w+ ((?:is|get)\w+)\(\) \{`)
)

// fieldGetterProblems: no @Field has a function that Groovy takes for its
// getter (isX() or getX() for field x), which a read of the field calls
// instead of reading it: isSuperseded() beside a field named superseded
// recursed until a StackOverflowError on a superseded build.
func fieldGetterProblems(jf string) []string {
	functions := map[string]bool{}
	for _, m := range functionRE.FindAllStringSubmatch(jf, -1) {
		functions[m[1]] = true
	}
	var problems []string
	for _, m := range fieldRE.FindAllStringSubmatch(jf, -1) {
		name := strings.ToUpper(m[1][:1]) + m[1][1:]
		for _, getter := range []string{"is" + name, "get" + name} {
			if functions[getter] {
				problems = append(problems, "the field "+m[1]+" is read through the function "+getter+"(), its Groovy getter")
			}
		}
	}
	return problems
}

// TestJenkinsfileSupersedesOlderPushBuilds: abort-previous per branch, its
// exclusion for nightly and parameterized builds, and the abort being kept out
// of the unit retries.
func TestJenkinsfileSupersedesOlderPushBuilds(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, supersedeProblems, map[string]string{
		"plain disableConcurrentBuilds":  replaceOnce(t, jf, "disableConcurrentBuilds(abortPrevious: supersedable())", "disableConcurrentBuilds()"),
		"abort of every build":           replaceOnce(t, jf, "disableConcurrentBuilds(abortPrevious: supersedable())", "disableConcurrentBuilds(abortPrevious: true)"),
		"no concurrency control":         replaceOnce(t, jf, "    disableConcurrentBuilds(abortPrevious: supersedable())\n", ""),
		"a nightly that can be stopped":  replaceOnce(t, jf, "return pushed && !timer && !parameterized", "return pushed && !parameterized"),
		"the timer not read":             replaceOnce(t, jf, "    boolean timer = !currentBuild.getBuildCauses('hudson.triggers.TimerTrigger$TimerTriggerCause').isEmpty()\n    boolean pushed", "    boolean timer = false\n    boolean pushed"),
		"no push requirement":            replaceOnce(t, jf, "return pushed && !timer && !parameterized", "return !timer && !parameterized"),
		"EXTENDED can be stopped":        replaceOnce(t, jf, "params.EXTENDED == true || ", ""),
		"MUTATION can be stopped":        replaceOnce(t, jf, "params.MUTATION == true || ", ""),
		"MUTATION_FULL can be stopped":   replaceOnce(t, jf, " || params.MUTATION_FULL == true", ""),
		"always supersedable":            replaceOnce(t, jf, "return pushed && !timer && !parameterized", "return true"),
		"an unknown build is stopped":    replaceOnce(t, jf, "} catch (Throwable e) {\n    return false\n  }", "} catch (Throwable e) {\n    return true\n  }"),
		"no function":                    replaceOnce(t, jf, "boolean supersedable() {", "boolean supersedableNot() {"),
		"a superseded attempt retried":   replaceOnce(t, jf, "    if (supersededBy(e)) {\n      return null\n    }\n", ""),
		"the cause not recognised":       replaceOnce(t, jf, "return causes.contains('CancelledCause') || causes.contains('Superseded by') || isSuperseded()", "return false"),
		"the newer build not looked for": replaceOnce(t, jf, "return currentBuild.nextBuild != null", "return false"),
		"ends NOT_BUILT from a unit":     replaceOnce(t, jf, "        if (supersededBy(e)) {\n          endSuperseded()\n        }\n", ""),
		"a cut-short attempt rerun":      replaceOnce(t, jf, "    if (isSuperseded()) {\n      endSuperseded()\n", "    if (false) {\n      endSuperseded()\n"),
		"ends NOT_BUILT outside a unit":  replaceOnce(t, jf, "        if (isSuperseded() || currentBuild.currentResult == 'NOT_BUILT') {\n", "        if (false) {\n"),
		"the report runs, then a kill":   replaceOnce(t, jf, "          echo timingReport()\n          return\n", "          echo timingReport()\n"),
		"never set ABORTED":              replaceOnce(t, jf, "  currentBuild.result = 'ABORTED'\n", ""),
		"ABORTED set as FAILURE":         replaceOnce(t, jf, "  currentBuild.result = 'ABORTED'\n", "  currentBuild.result = 'FAILURE'\n"),
		"history from a superseded":      replaceOnce(t, jf, "(b.result == 'ABORTED' || b.result == 'NOT_BUILT')", "false"),
		"history from the last build":    replaceOnce(t, jf, "selector: historyFrom ? specific(historyFrom) : lastCompleted(),", "selector: lastCompleted(),"),
		"a field isSuperseded() reads":   replaceOnce(t, jf, "@Field boolean supersedeSeen = false", "@Field boolean superseded = false"),
		"nowMs a CPS method":             replaceOnce(t, jf, "@NonCPS\nlong nowMs() {", "long nowMs() {"),
		"reads the refused result":       replaceOnce(t, jf, "    return 'infra:interrupted'", "    if (e.result?.toString() == 'ABORTED') {\n      return null\n    }\n    return 'infra:interrupted'"),
	})
}
