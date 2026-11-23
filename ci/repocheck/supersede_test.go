// supersede_test.go pins R8's scheduling rule in the Jenkinsfile: a newer push
// build of a branch stops the branch's older build (abort-previous, per
// branch), and main's nightly (timer) and parameterized (EXTENDED, MUTATION,
// MUTATION_FULL) builds are never stopped by a push. Plain text matching on
// the Jenkinsfile, like jenkinsfile_test.go: supersedeProblems is a pure
// function of the file text, fed the committed file and doctored copies.
package repocheck

import (
	"strings"
	"testing"
)

// supersedeProblems: the job property is disableConcurrentBuilds with
// abortPrevious taken from supersedable() (never a constant true, which would
// let a push stop a nightly, and never the plain form, which never stops
// anything); supersedable() is true for a push build only (a branch push event
// or branch indexing), and false for a timer-triggered build and for one with
// EXTENDED, MUTATION or MUTATION_FULL set; a failure to tell means false; and
// a superseded build ends ABORTED. Jenkins stops it with the result NOT_BUILT
// and a CancelledCause ("Superseded by #N") (main #278 ended NOT_BUILT), so:
// supersededBy() recognises that cause; classify lets it propagate instead of
// retrying the attempt; the attempt that sees it sets ABORTED (endSuperseded);
// post { always } sets ABORTED for a NOT_BUILT build no unit saw it in; and
// nothing reads the interruption's result, which the sandbox refuses
// (FlowInterruptedException.getResult turned #278's abort into a FAILURE).
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

// supersededEndProblems: the ABORTED end of a superseded build (see
// supersedeProblems).
func supersededEndProblems(jf string) []string {
	var problems []string
	by, ok := groovyFunction(jf, "boolean supersededBy(Throwable e)")
	if !ok || !strings.Contains(by, "String causes = interruptionCauses(e)\n  return causes.contains('CancelledCause') || causes.contains('Superseded by')") {
		problems = append(problems, "supersededBy() does not recognise abortPrevious's CancelledCause")
	}
	classify, ok := groovyFunction(jf, "String classify(Throwable e, Map a)")
	if !ok || !strings.Contains(classify, "if (supersededBy(e)) {\n      return null\n    }") {
		problems = append(problems, "classify does not let a superseded build's interruption propagate")
	}
	end, ok := groovyFunction(jf, "def endSuperseded()")
	if !ok || !strings.Contains(end, "superseded = true\n  currentBuild.result = 'ABORTED'\n") {
		problems = append(problems, "endSuperseded() does not set the result ABORTED")
	}
	if !strings.Contains(jf, "        a.outcome = 'aborted'\n        a.end = nowMs()\n        if (supersededBy(e)) {\n          endSuperseded()\n        }\n        throw e\n") {
		problems = append(problems, "an attempt stopped by a newer build does not end the build ABORTED")
	}
	if !strings.Contains(jf, "\n  post {\n    always {\n      script {\n") ||
		!strings.Contains(jf, "        if (!superseded && currentBuild.currentResult == 'NOT_BUILT') {\n          endSuperseded()\n        }\n") {
		problems = append(problems, "post { always } does not end a NOT_BUILT (superseded) build ABORTED")
	}
	if strings.Contains(jf, "e.result") || strings.Contains(jf, ".getResult()") {
		problems = append(problems, "the Jenkinsfile reads an interruption's result, which the sandbox refuses")
	}
	return problems
}

// TestJenkinsfileSupersedesOlderPushBuilds: abort-previous per branch, its
// exclusion for nightly and parameterized builds, and the abort being kept out
// of the unit retries.
func TestJenkinsfileSupersedesOlderPushBuilds(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, supersedeProblems, map[string]string{
		"plain disableConcurrentBuilds": replaceOnce(t, jf, "disableConcurrentBuilds(abortPrevious: supersedable())", "disableConcurrentBuilds()"),
		"abort of every build":          replaceOnce(t, jf, "disableConcurrentBuilds(abortPrevious: supersedable())", "disableConcurrentBuilds(abortPrevious: true)"),
		"no concurrency control":        replaceOnce(t, jf, "    disableConcurrentBuilds(abortPrevious: supersedable())\n", ""),
		"a nightly that can be stopped": replaceOnce(t, jf, "return pushed && !timer && !parameterized", "return pushed && !parameterized"),
		"the timer not read":            replaceOnce(t, jf, "    boolean timer = !currentBuild.getBuildCauses('hudson.triggers.TimerTrigger$TimerTriggerCause').isEmpty()\n    boolean pushed", "    boolean timer = false\n    boolean pushed"),
		"no push requirement":           replaceOnce(t, jf, "return pushed && !timer && !parameterized", "return !timer && !parameterized"),
		"EXTENDED can be stopped":       replaceOnce(t, jf, "params.EXTENDED == true || ", ""),
		"MUTATION can be stopped":       replaceOnce(t, jf, "params.MUTATION == true || ", ""),
		"MUTATION_FULL can be stopped":  replaceOnce(t, jf, " || params.MUTATION_FULL == true", ""),
		"always supersedable":           replaceOnce(t, jf, "return pushed && !timer && !parameterized", "return true"),
		"an unknown build is stopped":   replaceOnce(t, jf, "} catch (Throwable e) {\n    return false\n  }", "} catch (Throwable e) {\n    return true\n  }"),
		"no function":                   replaceOnce(t, jf, "boolean supersedable() {", "boolean supersedableNot() {"),
		"a superseded attempt retried":  replaceOnce(t, jf, "    if (supersededBy(e)) {\n      return null\n    }\n", ""),
		"the cause not recognised":      replaceOnce(t, jf, "return causes.contains('CancelledCause') || causes.contains('Superseded by')", "return false"),
		"ends NOT_BUILT from a unit":    replaceOnce(t, jf, "        if (supersededBy(e)) {\n          endSuperseded()\n        }\n", ""),
		"ends NOT_BUILT outside a unit": replaceOnce(t, jf, "        if (!superseded && currentBuild.currentResult == 'NOT_BUILT') {\n          endSuperseded()\n        }\n", ""),
		"never set ABORTED":             replaceOnce(t, jf, "  currentBuild.result = 'ABORTED'\n", ""),
		"ABORTED set as FAILURE":        replaceOnce(t, jf, "  currentBuild.result = 'ABORTED'\n", "  currentBuild.result = 'FAILURE'\n"),
		"reads the refused result":      replaceOnce(t, jf, "    return 'infra:interrupted'", "    if (e.result?.toString() == 'ABORTED') {\n      return null\n    }\n    return 'infra:interrupted'"),
	})
}
