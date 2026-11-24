// jenkinsfile_test.go probes the Jenkinsfile (board/ci) and Jenkinsfile.release
// (board/release), carried from deck's ci/repocheck/jenkinsfile_test.go and
// adapted to the distributed pipeline: Core, then Stability and Mutation side
// by side, then Perf, on the one AMD agent label, the nightly lane is
// scheduled on main, mutation runs in a diff or a full mode only when asked,
// the quality gates and reports stay wired in, only a fully green main build
// leaves a release candidate, and the release job validates against that
// candidate, builds it and publishes it only through ci/release-publish.sh. Each property is a
// pure function of the file text (a *Problems helper) that its test feeds the
// committed Jenkinsfile and copies of it with the property removed or altered,
// so the test fails if the property is lost. Plain text matching; both files
// are small and declarative.
package repocheck

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var stageRE = regexp.MustCompile(`stage\('([^']+)'\)\s*\{`)

// jenkinsStages returns the stage names in the order they appear.
func jenkinsStages(jf string) []string {
	var names []string
	for _, m := range stageRE.FindAllStringSubmatch(jf, -1) {
		names = append(names, m[1])
	}
	return names
}

// jenkinsStage returns the body of stage(name), up to its matching brace.
func jenkinsStage(jf, name string) (string, bool) {
	for _, loc := range stageRE.FindAllStringSubmatchIndex(jf, -1) {
		if jf[loc[2]:loc[3]] != name {
			continue
		}
		depth := 0
		for i := loc[1] - 1; i < len(jf); i++ {
			switch jf[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return jf[loc[1]:i], true
				}
			}
		}
	}
	return "", false
}

func stageBefore(jf, first, second string) bool {
	fi, si := -1, -1
	for i, n := range jenkinsStages(jf) {
		if n == first && fi < 0 {
			fi = i
		}
		if n == second && si < 0 {
			si = i
		}
	}
	return fi >= 0 && si >= 0 && fi < si
}

// topStageRE matches the stages directly under `stages {` (four spaces of
// indent): Core, Stability and Mutation, Perf. Quality, the units, and the
// Stability and Mutation stages of the parallel phase are nested.
var topStageRE = regexp.MustCompile(`(?m)^    stage\('([^']+)'\)`)

// topStages returns the pipeline's top-level stage names in order.
func topStages(jf string) []string {
	var names []string
	for _, m := range topStageRE.FindAllStringSubmatch(jf, -1) {
		names = append(names, m[1])
	}
	return names
}

var unitNameRE = regexp.MustCompile(`units << \[name: '([^']+)'`)

// coreUnits returns the units the Core stage declares, in order.
func coreUnits(coreBody string) []string {
	var names []string
	for _, m := range unitNameRE.FindAllStringSubmatch(coreBody, -1) {
		names = append(names, m[1])
	}
	return names
}

// replaceOnce returns jf with the first old replaced by new, and fails the
// test when old is not there: a doctored copy that equals the original would
// prove nothing.
func replaceOnce(t *testing.T, jf, old, repl string) string {
	t.Helper()
	if !strings.Contains(jf, old) {
		t.Fatalf("the Jenkinsfile no longer contains %q, so a case doctoring it checks nothing", old)
	}
	return strings.Replace(jf, old, repl, 1)
}

// expectProblems checks a Jenkinsfile property check from both sides: the
// committed file has no problem, and each doctored copy (the property removed
// or altered) is reported.
func expectProblems(t *testing.T, good string, check func(string) []string, doctored map[string]string) {
	t.Helper()
	for _, p := range check(good) {
		t.Errorf("the committed Jenkinsfile: %s", p)
	}
	for name, text := range doctored {
		if text == good {
			t.Errorf("%s: the doctoring changed nothing", name)
			continue
		}
		if len(check(text)) == 0 {
			t.Errorf("%s was not caught", name)
		}
	}
}

// phaseProblems: the phases Core, Stability and Mutation (side by side, the
// two parallel stages of one phase), Perf, in that order; Core's units are
// Lint, Web UI, Suite, E2E, Image and Allure smoke with Quality after Suite, E2E and Image;
// Stability needs EXTENDED or the nightly timer; Mutation is gated on
// CI_MUTATION alone (EXTENDED on its own runs no mutation); a phase runs only
// while the build has not failed; each unit runs its command.
func phaseProblems(jf string) []string {
	var problems []string
	if got := strings.Join(topStages(jf), ","); got != "Core,Stability and Mutation,Perf" {
		problems = append(problems, "top-level stages are "+got+", want Core,Stability and Mutation,Perf")
	}
	both, _ := jenkinsStage(jf, "Stability and Mutation")
	if got := strings.Join(jenkinsStages(both), ","); !strings.HasPrefix(strings.TrimSpace(both), "parallel {") ||
		got != "Stability,Mutation" {
		problems = append(problems, "Stability and Mutation are not the two parallel stages of one phase (stages "+got+")")
	}
	core, _ := jenkinsStage(jf, "Core")
	if got := strings.Join(coreUnits(core), ","); got != "Lint,Web UI,Suite,E2E,Image,Allure smoke" {
		problems = append(problems, "Core units are "+got+", want Lint,Web UI,Suite,E2E,Image,Allure smoke")
	}
	if !strings.Contains(core, "branches['Quality'] = {") || !strings.Contains(core, "stage('Quality') {") {
		problems = append(problems, "Core does not run Quality as a branch of its own")
	}
	if !strings.Contains(core, "waitUntil(initialRecurrencePeriod: 15000) { signals['Suite'] == true && signals['E2E'] == true && signals['Image'] == true }") {
		problems = append(problems, "Quality does not wait for Suite, E2E and Image")
	}
	for _, want := range []string{
		"ci/run.sh ci/lint.sh",
		"BOARD_CI_PLAYWRIGHT=skip ci/run.sh ci/suite.sh",
		"BOARD_CI_PLAYWRIGHT=only ci/run.sh ci/suite.sh",
		`ci/image-build.sh "$BOARD_CI_OUT/board-image.tar" && ci/image-check.sh "$BOARD_CI_OUT"`,
		"-run '^TestAllureFormatterWritesResultsForAFixtureRun$' ./features/",
		"ci/allure-smoke.sh allure-smoke/fixture allure-smoke/report",
		"ci/run.sh ci/quality.sh",
	} {
		if !strings.Contains(core, want) {
			problems = append(problems, "Core lacks the command "+want)
		}
	}
	stability, _ := jenkinsStage(jf, "Stability")
	if !strings.Contains(stability, "expression { env.CI_NIGHTLY == 'true' && currentBuild.currentResult != 'FAILURE' }") {
		problems = append(problems, "Stability is not gated on EXTENDED or the nightly timer and an earlier phase that did not fail")
	}
	mutation, _ := jenkinsStage(jf, "Mutation")
	problems = append(problems, mutationGateProblems(mutation)...)
	if !strings.Contains(stability, "def u = [name: 'Stability runs', minutes: 40,") || !strings.Contains(stability, "unitBranch(u).call()") ||
		strings.Count(stability, "unitBranch(") != 1 || strings.Count(stability, "runUnit(") != 0 {
		problems = append(problems, "Stability is not one unit (one executor)")
	}
	if !strings.Contains(stability, "[1, 2, 3].each { i ->") || strings.Count(stability, "ci/stability.sh 1") != 1 ||
		!strings.Contains(stability, "parallel runs") || !strings.Contains(stability, `stage("Stability ${i}")`) {
		problems = append(problems, "Stability's unit does not run ci/stability.sh 1 three times side by side, each in a stage of its own")
	}
	if !strings.Contains(stability, `git clone -q --local --no-checkout . "stability-$i"`) || !strings.Contains(stability, "cd stability-${i} && ") ||
		!strings.Contains(stability, `$BOARD_CI_OUT/stability/run-${i}`) {
		problems = append(problems, "Stability's runs do not each run in a clone of their own with their own TMPDIR")
	}
	if !strings.Contains(stability, "${i == 3 ? 'BOARD_STABILITY_PARALLEL=1 ' : ''}") {
		problems = append(problems, "no stability run is at go test -parallel 1 (BOARD_STABILITY_PARALLEL=1)")
	}
	if !strings.Contains(stability, "return failed ? 1 : 0") {
		problems = append(problems, "Stability's unit does not fail when one of its runs fails")
	}
	return problems
}

// TestJenkinsfileStagesRunInOrder: the phases of SPEC 15.3 exist and run in
// order, the Core units are the ones SPEC 15.3 lists, and each runs its
// command, so removing one fails here.
func TestJenkinsfileStagesRunInOrder(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, phaseProblems, map[string]string{
		"Stability before Core":            replaceOnce(t, jf, "    stage('Core') {", "    stage('Stability') {\n      steps { echo 'x' }\n    }\n    stage('Core') {"),
		"Mutation after Stability":         replaceOnce(t, jf, "    stage('Stability and Mutation') {\n      parallel {", "    stage('Stability and Mutation') {\n      stages {"),
		"Mutation outside the phase":       replaceOnce(t, jf, "        stage('Mutation') {", "        stage('Mutations') {"),
		"a fourth phase":                   replaceOnce(t, jf, "    stage('Perf') {", "    stage('Extra') {\n      steps { echo 'x' }\n    }\n    stage('Perf') {"),
		"Perf before Mutation":             replaceOnce(t, replaceOnce(t, jf, "    stage('Perf') {", "    stage('Late') {"), "    stage('Stability and Mutation') {", "    stage('Perf') {\n      steps { echo 'x' }\n    }\n    stage('Stability and Mutation') {"),
		"no Lint unit":                     replaceOnce(t, jf, "units << [name: 'Lint'", "units << [name: 'Tidy'"),
		"no Allure smoke unit":             replaceOnce(t, jf, "units << [name: 'Allure smoke'", "units << [name: 'Smoke'"),
		"no Image unit":                    replaceOnce(t, jf, "units << [name: 'Image'", "units << [name: 'Picture'"),
		"Quality not waiting for Image":    replaceOnce(t, jf, " && signals['Image'] == true", ""),
		"Quality not waiting for Suite":    replaceOnce(t, jf, "signals['Suite'] == true && ", ""),
		"Quality not waiting for E2E":      replaceOnce(t, jf, " && signals['E2E'] == true", ""),
		"no E2E unit":                      replaceOnce(t, jf, "units << [name: 'E2E'", "units << [name: 'Browser'"),
		"Lint not running ci/lint.sh":      replaceOnce(t, jf, "ci/run.sh ci/lint.sh", "ci/run.sh true"),
		"Suite not running ci/suite.sh":    replaceOnce(t, jf, "BOARD_CI_PLAYWRIGHT=skip ci/run.sh ci/suite.sh", "ci/run.sh true"),
		"E2E not running ci/suite.sh":      replaceOnce(t, jf, "BOARD_CI_PLAYWRIGHT=only ci/run.sh ci/suite.sh", "ci/run.sh true"),
		"Image not checked":                replaceOnce(t, jf, ` && ci/image-check.sh "$BOARD_CI_OUT"`, ""),
		"smoke not running the fixture":    replaceOnce(t, jf, "TestAllureFormatterWritesResultsForAFixtureRun", "TestNothing"),
		"Stability on every build":         replaceOnce(t, jf, "expression { env.CI_NIGHTLY == 'true' && currentBuild", "expression { currentBuild"),
		"Stability after a failed Core":    replaceOnce(t, jf, "env.CI_NIGHTLY == 'true' && currentBuild.currentResult != 'FAILURE'", "env.CI_NIGHTLY == 'true'"),
		"Mutation on every build":          replaceOnce(t, jf, "expression { env.CI_MUTATION == 'true' && currentBuild", "expression { currentBuild"),
		"Mutation after a failed phase":    replaceOnce(t, jf, "env.CI_MUTATION == 'true' && currentBuild.currentResult != 'FAILURE'", "env.CI_MUTATION == 'true'"),
		"Mutation on EXTENDED alone":       replaceOnce(t, jf, "expression { env.CI_MUTATION == 'true'", "expression { env.CI_NIGHTLY == 'true'"),
		"Mutation on a push of main":       replaceOnce(t, jf, "        stage('Mutation') {\n          when {", "        stage('Mutation') {\n          when {\n            branch 'main'"),
		"two stability runs":               replaceOnce(t, jf, "[1, 2, 3].each { i ->", "[1, 2].each { i ->"),
		"stability running three in a row": replaceOnce(t, jf, "ci/stability.sh 1", "ci/stability.sh 3"),
		"stability runs in series":         replaceOnce(t, jf, "parallel runs\n", "runs.each { k, v -> v.call() }\n"),
		"stability runs in one tree":       replaceOnce(t, jf, "cd stability-${i} && ", ""),
		"no run at -parallel 1":            replaceOnce(t, jf, "${i == 3 ? 'BOARD_STABILITY_PARALLEL=1 ' : ''}", ""),
		"a failed run that passes":         replaceOnce(t, jf, "return failed ? 1 : 0", "return 0"),
		"a unit per stability run":         replaceOnce(t, jf, "              unitBranch(u).call()\n              env.STABILITY_RESULT", "              parallel([1, 2, 3].collectEntries { [(\"Stability ${it}\"): unitBranch(u + [name: \"Stability ${it}\"])] })\n              env.STABILITY_RESULT"),
	})
	script := readRepoFile(t, "ci", "jenkins-image.sh")
	for _, v := range []string{"BOARD_CI_CACHE_VOLUME", "BOARD_CI_NPM_VOLUME"} {
		if !strings.Contains(script, `fix_volume "$`+v+`"`) {
			t.Errorf("ci/jenkins-image.sh does not make $%s usable by the agent's uid", v)
		}
	}
	if !strings.Contains(script, "docker build -t board-ci:local -f ci/Dockerfile ci") {
		t.Error("ci/jenkins-image.sh does not build board-ci:local from ci/Dockerfile")
	}
	if !strings.Contains(jf, "ci/jenkins-image.sh") {
		t.Error("the Jenkinsfile does not build board-ci:local before a unit runs")
	}
}

var nodeCallRE = regexp.MustCompile(`\bnode\(([^)]*)\)`)

// shapeProblems: the library, one agent label for every unit (the AMD
// constant, which resolves to agent int21h-ws; no arm64 or other label),
// serial builds, a clean workspace, the three booleanParams, the 300-minute
// pipeline timeout (a backstop: every unit has its own bound), and the per-attempt cleanup of sibling containers by exact
// ID.
func shapeProblems(jf string) []string {
	var problems []string
	lib := strings.Index(jf, "@Library('homelab') _")
	if lib < 0 || lib > strings.Index(jf, "\npipeline {") {
		problems = append(problems, "the library is not loaded before the pipeline")
	}
	lines := jenkinsfileLines(jf)
	if !anyLineContains(lines, "@Field final String AMD = 'board-test && amd64 && docker'") {
		problems = append(problems, "the AMD label constant is not 'board-test && amd64 && docker'")
	}
	if !anyLineContains(lines, "agent none") {
		problems = append(problems, "the pipeline is not `agent none`: every unit must pick its node itself")
	}
	nodes := nodeCallRE.FindAllStringSubmatch(strings.Join(lines, "\n"), -1)
	if len(nodes) < 3 {
		problems = append(problems, "fewer than three node() calls (a unit, the report, the notification)")
	}
	for _, m := range nodes {
		if m[1] != "AMD" {
			problems = append(problems, "node("+m[1]+") does not use the AMD label constant")
		}
	}
	for _, l := range lines {
		if strings.Contains(l, "arm64") || strings.Contains(l, "agent {") || strings.Contains(l, "label '") {
			problems = append(problems, "line "+l+" names another agent or label")
		}
	}
	if !anyLineContains(lines, "def a = [n: attempts.size() + 1, label: AMD,") {
		problems = append(problems, "an attempt does not record the AMD label")
	}
	for _, want := range []string{
		"disableConcurrentBuilds(abortPrevious: supersedable())",
		"timeout(time: 300, unit: 'MINUTES')",
		"git clean -ffdxq",
		"booleanParam(name: 'EXTENDED', defaultValue: false",
		"booleanParam(name: 'MUTATION', defaultValue: false",
		"booleanParam(name: 'MUTATION_FULL', defaultValue: false",
		// ci/run.sh mounts both cache volumes; the agent's uid owns neither
		// until ci/jenkins-image.sh chowns them.
		"BOARD_CI_CACHE_VOLUME = 'board-go-cache-jenkins'",
		"BOARD_CI_NPM_VOLUME = 'board-npm-cache-jenkins'",
		// Each attempt's containers carry the attempt id and are removed by exact ID.
		// The id names the job: build numbers repeat across branches, so without it
		// one branch's cleanup removes another branch's containers.
		// ci/attempt-id.sh (tested by attemptid_test.go) computes it from the job, build, unit and attempt.
		`runId = sh(returnStdout: true, script: "ci/attempt-id.sh '${env.JOB_NAME}' '${env.BUILD_NUMBER}' '${u.name}' '${a.n}'").trim()`,
		`withEnv(["RALPHD_RUN_ID=${runId}"`,
		`ids=\$(docker ps -aq --filter label=ralphd.run=${runId})`,
		`docker rm -f \$ids`,
	} {
		if !anyLineContains(lines, want) {
			problems = append(problems, "lacks "+want)
		}
	}
	if n := strings.Count(jf, "booleanParam("); n != 3 {
		problems = append(problems, "declares "+strconv.Itoa(n)+" booleanParams, want EXTENDED, MUTATION and MUTATION_FULL")
	}
	if i, j := strings.Index(jf, "} finally {"), strings.Index(jf, "docker rm -f"); i < 0 || j < i {
		problems = append(problems, "the container cleanup does not run in a finally")
	}
	return problems
}

// TestJenkinsfileDeclaresTheJobShape: the library, the one agent label every
// unit uses, serial builds, a clean workspace, the three parameters, the
// pipeline timeout, and the per-attempt cleanup of sibling containers.
func TestJenkinsfileDeclaresTheJobShape(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, shapeProblems, map[string]string{
		"no library":                   replaceOnce(t, jf, "@Library('homelab') _", ""),
		"an arm64 unit":                replaceOnce(t, jf, "        node(AMD) {\n          a.node", "        node('board-test && arm64') {\n          a.node"),
		"a literal label in a unit":    replaceOnce(t, jf, "            node(AMD) {\n              sh 'rm -rf", "            node('linux') {\n              sh 'rm -rf"),
		"the notification elsewhere":   replaceOnce(t, jf, "            node(AMD) {\n              notifyTelegram", "            node('other') {\n              notifyTelegram"),
		"another AMD constant":         replaceOnce(t, jf, "'board-test && amd64 && docker'", "'board-test && arm64 && docker'"),
		"a pipeline-level agent":       replaceOnce(t, jf, "  agent none", "  agent { label 'linux' }"),
		"concurrent builds":            replaceOnce(t, jf, "    disableConcurrentBuilds(abortPrevious: supersedable())\n", ""),
		"a shorter pipeline timeout":   replaceOnce(t, jf, "timeout(time: 300, unit: 'MINUTES')", "timeout(time: 120, unit: 'MINUTES')"),
		"the old 8-hour timeout":       replaceOnce(t, jf, "timeout(time: 300, unit: 'MINUTES')", "timeout(time: 480, unit: 'MINUTES')"),
		"no clean workspace":           replaceOnce(t, jf, "sh 'git clean -ffdxq'", "sh 'true'"),
		"no EXTENDED parameter":        replaceOnce(t, jf, "booleanParam(name: 'EXTENDED'", "booleanParam(name: 'WIDER'"),
		"no MUTATION parameter":        replaceOnce(t, jf, "booleanParam(name: 'MUTATION',", "booleanParam(name: 'MUTATE',"),
		"no MUTATION_FULL parameter":   replaceOnce(t, jf, "booleanParam(name: 'MUTATION_FULL'", "booleanParam(name: 'MUTATE_ALL'"),
		"a fourth parameter":           replaceOnce(t, jf, "  parameters {", "  parameters {\n    booleanParam(name: 'MORE', defaultValue: false, description: 'x')"),
		"an attempt id nobody reads":   replaceOnce(t, jf, `withEnv(["RALPHD_RUN_ID=${runId}"`, `withEnv(["RALPHD_RUN=${runId}"`),
		"cleanup of every container":   replaceOnce(t, jf, "docker ps -aq --filter label=ralphd.run=${runId}", "docker ps -aq"),
		"cleanup that removes nothing": replaceOnce(t, jf, `docker rm -f \$ids`, `echo \$ids`),
		"cleanup outside the finally":  replaceOnce(t, jf, "} finally {", "} catch (Throwable never) {"),
	})
}

// nightlyProblems: main is built nightly at 00:17 and nowhere else has a
// timer, and a nightly (or EXTENDED) build adds -race and fresh vulnerability
// databases to Core and runs the Stability phase.
func nightlyProblems(jf string) []string {
	var problems []string
	for _, want := range []string{
		`cron(env.BRANCH_NAME == 'main' ? '17 0 * * *' : '')`,
		"currentBuild.getBuildCauses('hudson.triggers.TimerTrigger$TimerTriggerCause')",
		"boolean nightly = params.EXTENDED || timer",
		"env.CI_NIGHTLY = nightly ? 'true' : 'false'",
		"env.BOARD_CI_GO_EXTRA_FLAGS = nightly ? '-race' : ''",
		"if (env.CI_NIGHTLY == 'true') {",
		"rm -rf /go-cache/trivy/db /go-cache/trivy/java-db /go-cache/xdg-cache/govulncheck",
	} {
		if !strings.Contains(jf, want) {
			problems = append(problems, "lacks "+want)
		}
	}
	if n := strings.Count(jf, "cron("); n != 1 {
		problems = append(problems, "has "+strconv.Itoa(n)+" cron() triggers, want one")
	}
	return problems
}

// TestJenkinsfileNightlyLane: main is built nightly at 00:17 (and no other
// branch has a timer), and a nightly or EXTENDED build adds -race, drops the
// vulnerability databases and runs the Stability phase.
func TestJenkinsfileNightlyLane(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, nightlyProblems, map[string]string{
		"a timer on every branch":             replaceOnce(t, jf, "cron(env.BRANCH_NAME == 'main' ? '17 0 * * *' : '')", "cron('17 0 * * *')"),
		"a timer on another branch":           replaceOnce(t, jf, "env.BRANCH_NAME == 'main' ? '17 0 * * *'", "env.BRANCH_NAME == 'dev' ? '17 0 * * *'"),
		"another time":                        replaceOnce(t, jf, "'17 0 * * *'", "'17 1 * * *'"),
		"a second timer":                      replaceOnce(t, jf, "  triggers {", "  triggers {\n    cron('5 5 * * *')"),
		"a nightly decided by the timer only": replaceOnce(t, jf, "params.EXTENDED || timer", "timer"),
		"no -race":                            replaceOnce(t, jf, "nightly ? '-race' : ''", "''"),
		"cached vulnerability databases":      replaceOnce(t, jf, "rm -rf /go-cache/trivy/db", "rm -rf /go-cache/trivy/none"),
	})
}

// qualityProblems: the Quality unit runs ci/quality.sh over the results with
// the mutation rows deferred to the Mutation phase, needs Suite's, E2E's and
// Image's stashes, and lets a red gate fail the build.
func qualityProblems(jf string) []string {
	var problems []string
	core, _ := jenkinsStage(jf, "Core")
	i := strings.Index(core, "stage('Quality') {")
	if i < 0 {
		return []string{"Core has no Quality stage"}
	}
	quality := core[i:]
	for _, want := range []string{
		`ci/run.sh ci/quality.sh "$BOARD_CI_OUT" ci/quality.json --mutation-deferred`,
		"needs: ['res-suite', 'res-e2e', 'res-image', 'image', 'web-dist']",
		"catchError(buildResult: 'FAILURE', stageResult: 'FAILURE')",
		"env.QUALITY_RESULT = results['Quality'] == 'fail' ? 'failing' : 'passing'",
	} {
		if !strings.Contains(quality, want) {
			problems = append(problems, "the Quality stage lacks "+want)
		}
	}
	return problems
}

// TestJenkinsfileRunsTheQualityGates: Quality runs ci/quality.sh over the
// image and the Suite's results, lets a red gate fail the build, and is told
// the two mutation rows are judged by the Mutation phase, whose reports exist
// only after it (and not at all without MUTATION or MUTATION_FULL).
func TestJenkinsfileRunsTheQualityGates(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, qualityProblems, map[string]string{
		"gates that read missing mutation reports": replaceOnce(t, jf, " --mutation-deferred", ""),
		"a Quality unit without the image":         replaceOnce(t, jf, "needs: ['res-suite', 'res-e2e', 'res-image', 'image', 'web-dist']", "needs: ['res-suite', 'res-e2e', 'web-dist']"),
		"a Quality unit without E2E's results":     replaceOnce(t, jf, "needs: ['res-suite', 'res-e2e', 'res-image', 'image', 'web-dist']", "needs: ['res-suite', 'res-image', 'image', 'web-dist']"),
		"a red gate that cannot fail the build":    replaceOnce(t, jf, "      catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') {\n                env.QUALITY_RESULT", "      catchError(buildResult: 'SUCCESS', stageResult: 'FAILURE') {\n                env.QUALITY_RESULT"),
		"no quality badge result":                  replaceOnce(t, jf, "env.QUALITY_RESULT = results['Quality'] == 'fail' ? 'failing' : 'passing'", ""),
	})
	build := readRepoFile(t, "ci", "image-build.sh")
	if !strings.Contains(build, "docker buildx build") {
		t.Error("ci/image-build.sh does not build the image")
	}
	if !strings.Contains(build, "docker save") {
		t.Error("ci/image-build.sh does not save the image tar the image gates scan")
	}
}

// releaseCandidateProblems: board/ci builds and never releases. Nothing
// pushes, tags or publishes, and a main build that ends SUCCESS (checked in
// post { always }, after every unit, so an UNSTABLE or failed build never
// qualifies) archives the release candidate board/release may copy.
func releaseCandidateProblems(jf string) []string {
	var problems []string
	for _, l := range jenkinsfileLines(jf) {
		for _, bad := range []string{"TAG_NAME", "tag pattern", "gh release", "release-build.sh", "git push", "docker push", "git tag"} {
			if strings.Contains(l, bad) {
				problems = append(problems, "line "+l+" mentions "+bad+": board/ci must not release")
			}
		}
	}
	_, post, ok := strings.Cut(jf, "\n  post {")
	if !ok {
		return append(problems, "there is no pipeline-level post { } block")
	}
	for _, want := range []string{
		"if (currentBuild.currentResult == 'SUCCESS' && env.BRANCH_NAME == 'main') {",
		"writeFile file: 'ci-results/release-candidate.json'",
		`'{"commit": "' + commitSha`,
		`"build_url": "' + env.BUILD_URL`,
		`"build_number": ' + env.BUILD_NUMBER`,
		"archiveArtifacts artifacts: 'ci-results/release-candidate.json'",
		"copyArtifactPermission('/board/release,/board/ci/*')",
	} {
		if !strings.Contains(jf, want) {
			problems = append(problems, "lacks "+want)
		}
	}
	if !strings.Contains(post, "release-candidate.json") {
		problems = append(problems, "the release candidate is not written in post { }")
	}
	return problems
}

// TestJenkinsfileNeverReleases: board/ci builds branches only; releasing is
// board/release's job. A main build that ends SUCCESS archives the release
// candidate, and board/release may copy it.
func TestJenkinsfileNeverReleases(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, releaseCandidateProblems, map[string]string{
		"a pushed commit":                  replaceOnce(t, jf, "        sh 'rm -rf ci-results", "        sh 'git push origin HEAD'\n        sh 'rm -rf ci-results"),
		"a pushed image":                   replaceOnce(t, jf, "        sh 'rm -rf ci-results", "        sh 'docker push board:ci'\n        sh 'rm -rf ci-results"),
		"a release":                        replaceOnce(t, jf, "        sh 'rm -rf ci-results", "        sh 'gh release create v1'\n        sh 'rm -rf ci-results"),
		"a candidate from a red build":     replaceOnce(t, jf, "currentBuild.currentResult == 'SUCCESS' && ", ""),
		"a candidate from any branch":      replaceOnce(t, jf, " && env.BRANCH_NAME == 'main') {\n                writeFile", ") {\n                writeFile"),
		"a candidate without its commit":   replaceOnce(t, jf, `'{"commit": "' + commitSha`, `'{"commit": "' + env.BRANCH_NAME`),
		"a candidate nobody can copy":      replaceOnce(t, jf, "copyArtifactPermission('/board/release,/board/ci/*')", "copyArtifactPermission('/board/ci/*')"),
		"a candidate that is not archived": replaceOnce(t, jf, "archiveArtifacts artifacts: 'ci-results/release-candidate.json'", "echo 'ci-results/release-candidate.json'"),
	})
}

// releaseHostRE finds a hostname anywhere in text: a dotted name ending in a
// well-known top-level domain, localhost, an IPv4 address, a scheme, or
// host:port. A release job that names one has the deployment hard-coded.
var releaseHostRE = regexp.MustCompile(`(?i)\b[a-z0-9-]+(\.[a-z0-9-]+)*\.(com|org|net|io|dev|app|cloud|local|lan|internal|home|corp|test|example|edu|gov|info|xyz|me|us|uk|de|ru)\b` +
	`|\blocalhost\b|\b\d{1,3}(\.\d{1,3}){3}\b|://|\b[a-z0-9.-]+:\d{2,5}\b`)

// releaseParamDefault returns the literal after `name: '<name>', defaultValue:`.
func releaseParamDefault(jf, name string) (string, bool) {
	m := regexp.MustCompile(`\(name: '` + regexp.QuoteMeta(name) + `',\s*defaultValue: ([^,)\s]+)`).FindStringSubmatch(jf)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// releaseProblems is the release job's contract as a pure function of the
// file text. Each problem starts with the number of the property it breaks:
//
//	1 the stages Candidate, Checkout, Version check, Build, Publish in order
//	2 DRY_RUN defaults to true
//	3 the CI job path comes from the folder of JOB_NAME, never a literal
//	4 the repository comes from the checkout's remote, never a literal slug
//	5 REGISTRY (no default), IMAGE_NAME (default village-hall) and both
//	  credential-ID parameters are declared
//	6 no hostname anywhere in the file, defaults included
//	7 Publish's only shell step is ci/release-publish.sh
func releaseProblems(jf string) []string {
	var problems []string
	add := func(n int, msg string) { problems = append(problems, strconv.Itoa(n)+": "+msg) }

	if got := strings.Join(jenkinsStages(jf), ","); got != "Candidate,Checkout,Version check,Build,Publish" {
		add(1, "stages are "+got)
	}
	if v, ok := releaseParamDefault(jf, "DRY_RUN"); !ok || v != "true" {
		add(2, "DRY_RUN does not default to true (default "+v+")")
	}

	candidate, _ := jenkinsStage(jf, "Candidate")
	if strings.Contains(jf, "/board/ci/main") {
		add(3, "the literal /board/ci/main is in the file")
	}
	for _, want := range []string{"env.JOB_NAME.lastIndexOf('/')", "env.JOB_NAME.substring(0, slash)", "'/ci/main'", "projectName: env.CI_JOB"} {
		if !strings.Contains(candidate, want) {
			add(3, "the Candidate stage does not derive the CI job from JOB_NAME: lacks "+want)
		}
	}

	checkout, _ := jenkinsStage(jf, "Checkout")
	if !strings.Contains(checkout, "git remote get-url origin") || !strings.Contains(checkout, "env.REPO = sh(returnStdout: true") {
		add(4, "the Checkout stage does not read the repository from the remote")
	}
	if strings.Contains(jf, "n-orlov/") || regexp.MustCompile(`REPO\s*=\s*['"]`).MatchString(jf) {
		add(4, "a repository slug is hard-coded")
	}

	if v, ok := releaseParamDefault(jf, "REGISTRY"); !ok || v != "''" {
		add(5, "REGISTRY is missing or has a default ("+v+")")
	}
	if v, ok := releaseParamDefault(jf, "IMAGE_NAME"); !ok || v != "'village-hall'" {
		add(5, "IMAGE_NAME is missing or does not default to village-hall ("+v+")")
	}
	for _, p := range []string{"REGISTRY_CREDENTIALS_ID", "GITHUB_CREDENTIALS_ID"} {
		if v, ok := releaseParamDefault(jf, p); !ok || v != "''" {
			add(5, p+" is missing or has a default ("+v+")")
		}
	}

	if hosts := releaseHostRE.FindAllString(jf, -1); len(hosts) != 0 {
		add(6, "the file names a host: "+strings.Join(hosts, ", "))
	}

	publish, _ := jenkinsStage(jf, "Publish")
	shells := regexp.MustCompile(`(?:^|[\s(])sh[ (]+(?:[a-zA-Z]+: ?)?'([^']*)'`).FindAllStringSubmatch(publish, -1)
	if got := len(regexp.MustCompile(`(?:^|[\s(])sh[ (]`).FindAllString(publish, -1)); got != len(shells) || got == 0 {
		add(7, "the Publish stage has a shell step this check cannot read")
	}
	for _, m := range shells {
		if !strings.HasPrefix(m[1], "ci/release-publish.sh ") {
			add(7, "the Publish stage runs "+m[1]+", not only ci/release-publish.sh")
		}
	}
	return problems
}

// TestJenkinsfileReleasePublishesTheCandidate: board/release has the stages
// Candidate, Checkout, Version check, Build and Publish; derives its CI job
// and its repository instead of naming them; declares the registry, the image
// name and both credential IDs as parameters with no host in any default;
// defaults to a dry run; and publishes only through ci/release-publish.sh.
// Each planted violation below must be reported under the property it breaks.
func TestJenkinsfileReleasePublishesTheCandidate(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile.release")
	for _, p := range releaseProblems(jf) {
		t.Errorf("Jenkinsfile.release: %s", p)
	}
	for name, c := range map[string]struct {
		text     string
		property string
	}{
		"a hardcoded /board/ci/main": {replaceOnce(t, jf, "projectName: env.CI_JOB", "projectName: '/board/ci/main'"), "3:"},
		"a hostname default for REGISTRY": {replaceOnce(t, jf, "(name: 'REGISTRY', defaultValue: ''",
			"(name: 'REGISTRY', defaultValue: 'registry.example.com'"), "5:"},
		"an IMAGE_NAME default other than village-hall": {replaceOnce(t, jf, "(name: 'IMAGE_NAME', defaultValue: 'village-hall'",
			"(name: 'IMAGE_NAME', defaultValue: 'board'"), "5:"},
		"a missing credential-ID parameter": {replaceOnce(t, jf, "(name: 'GITHUB_CREDENTIALS_ID'", "(name: 'GITHUB_TOKEN_ID'"), "5:"},
		"a hardcoded repository slug": {replaceOnce(t, jf, "          echo 'Releasing ' + env.REPO",
			"          env.REPO = 'n-orlov/board'\n          echo 'Releasing ' + env.REPO"), "4:"},
	} {
		if c.text == jf {
			t.Errorf("%s: the doctoring changed nothing", name)
			continue
		}
		var hit bool
		for _, p := range releaseProblems(c.text) {
			hit = hit || strings.HasPrefix(p, c.property)
		}
		if !hit {
			t.Errorf("%s was not reported as a property %s violation: %q", name, c.property, releaseProblems(c.text))
		}
	}
}

// TestJenkinsfilePublishesReports: test results, the Allure report and the
// CI summary are on every build's page, red or green.
func TestJenkinsfilePublishesReports(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	_, post, ok := strings.Cut(jf, "\n  post {")
	if !ok {
		t.Fatal("Jenkinsfile has no pipeline-level post { } block")
	}
	for _, want := range []string{
		"junit testResults: 'ci-results/junit-merged/*.xml'",
		"ci/jenkins-report.sh",
		"reportName: 'Allure'",
		"reportName: 'CI summary'",
		"addSummary(",
		"ci-results/*.cdx.json",
	} {
		if !strings.Contains(post, want) {
			t.Errorf("post { always } lacks %q", want)
		}
	}
}

// TestJenkinsfileDeclaresTheReadmeBadges: every badge the README shows from
// the public badge proxy is the plugin's "build" or declared by the
// Jenkinsfile, so none of them renders as "not run" forever, and each is an
// image served from the public badge path. The README shows all four, each
// linked to the board/ci/main job as deck's are (see
// TestReadmeBadgesLinkToTheMainJob).
func TestJenkinsfileDeclaresTheReadmeBadges(t *testing.T) {
	const prefix = "https://int21h.mooo.com/jenkins-static/board/ci/main/"
	readme := readRepoFile(t, "README.md")
	jf := readRepoFile(t, "Jenkinsfile")
	shown := map[string]bool{}
	for _, l := range strings.Split(readme, "\n") {
		_, rest, ok := strings.Cut(l, "("+prefix)
		if !ok {
			continue
		}
		// The image is wrapped in a link, checked by TestReadmeBadgesLinkToTheMainJob.
		l, _, _ = strings.Cut(strings.TrimPrefix(l, "["), ")](")
		l = strings.TrimSuffix(l, ")") + ")"
		id, _, ok := strings.Cut(rest, ".svg)")
		if !ok {
			t.Errorf("README badge line %q is not <prefix><id>.svg", l)
			continue
		}
		shown[id] = true
		if want := "![" + id + "](" + prefix + id + ".svg)"; strings.TrimSpace(l) != want {
			t.Errorf("README badge line %q, want exactly %q", l, want)
		}
		if id == "build" {
			continue
		}
		if !strings.Contains(jf, "addEmbeddableBadgeConfiguration(id: '"+id+"'") {
			t.Errorf("README shows badge %q but the Jenkinsfile never declares it", id)
		}
	}
	for _, id := range []string{"build", "tests", "coverage", "quality"} {
		if !shown[id] {
			t.Errorf("README does not show the %s badge from %s", id, prefix)
		}
	}
}

// TestJenkinsfilesNotifyTelegram: both jobs load the homelab shared library
// and post their result to Telegram from post { cleanup }, which runs after
// every other post condition, so the message carries the final result and is
// the last thing in post.
func TestJenkinsfilesNotifyTelegram(t *testing.T) {
	for _, name := range []string{"Jenkinsfile", "Jenkinsfile.release"} {
		jf := readRepoFile(t, name)
		lib := strings.Index(jf, "@Library('homelab') _")
		if lib < 0 || lib > strings.Index(jf, "\npipeline {") {
			t.Errorf("%s does not load @Library('homelab') before the pipeline", name)
		}
		_, post, ok := strings.Cut(jf, "\n  post {")
		if !ok {
			t.Errorf("%s has no pipeline-level post { } block", name)
			continue
		}
		_, cleanup, ok := strings.Cut(post, "\n    cleanup {")
		if !ok || !strings.Contains(cleanup, "notifyTelegram(notifyOn: 'ALL'") {
			t.Errorf("%s does not call notifyTelegram(notifyOn: 'ALL', ...) in post { cleanup }", name)
		}
		if strings.Contains(jf, "100.71.162.65") {
			t.Errorf("%s still talks to the notifier itself; notifyTelegram owns that", name)
		}
	}
}

// mutationGateProblems says what is wrong with the Mutation phase's `when`:
// it is gated on CI_MUTATION (set by MUTATION or MUTATION_FULL or the timer)
// and on no earlier phase having failed, and on nothing else. EXTENDED alone
// sets CI_NIGHTLY, not CI_MUTATION, so it runs no mutation, and no branch
// condition decides it.
func mutationGateProblems(stageBody string) []string {
	var problems []string
	when, _, _ := strings.Cut(stageBody, "steps {") // the steps may read CI_NIGHTLY (the full-mode tuning)
	lines := jenkinsfileLines(when)
	if !anyLineContains(lines, "expression { env.CI_MUTATION == 'true' && currentBuild.currentResult != 'FAILURE' }") {
		problems = append(problems, "lacks the CI_MUTATION guard behind a passing earlier phase")
	}
	for _, banned := range []string{"CI_NIGHTLY", "EXTENDED", "anyOf", "branch "} {
		if anyLineContains(lines, banned) {
			problems = append(problems, "carries "+banned+", so a build without MUTATION or MUTATION_FULL could run it")
		}
	}
	return problems
}

// mutationModeProblems: how the Jenkinsfile picks the mutation mode, and what
// the diff-mode unit and the base it diffs from look like (full mode is
// fullMutationProblems, incremental mode incrementalMutationProblems).
// MUTATION_FULL forces full; the nightly of main (the timer, or EXTENDED on
// main) runs MUTATION_MODE, incremental by default, full when no reference
// build exists; MUTATION alone is diff mode.
func mutationModeProblems(jf string) []string {
	var problems []string
	for _, want := range []string{
		"boolean nightlyMutation = timer || (params.EXTENDED && env.BRANCH_NAME == 'main')",
		"String mode = params.MUTATION_FULL ? 'full' : nightlyMutation ? (params.MUTATION_MODE ?: 'incremental') : params.MUTATION ? 'diff' : ''",
		"choice(name: 'MUTATION_MODE', choices: ['incremental', 'full'],",
		"env.CI_MUTATION_REF = mutationReference()\n            if (!env.CI_MUTATION_REF) {",
		"mode = 'full'\n            }",
		"env.CI_MUTATION = mode ? 'true' : 'false'",
		"env.CI_MUTATION_MODE = mode ?: 'diff'",
		"} else if (env.CI_MUTATION_MODE == 'full') {\n                runFullMutation(null)",
		"ci-results/*.cdx.json, ci-results/mutation/*.json,",
		// Diff mode: one 45-minute unit over the lines changed since the base.
		"u = [name: 'Mutation diff', minutes: 45,",
		"mutationBase = mutationDiffBase()",
		`"BOARD_MUTATION_DIFF=${mutationBase} " + tune +`,
		" ci/run.sh ci/mutation.sh ci-results/mutation ci/quality.json all'",
		"catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') {\n                    runUnit(u)",
	} {
		if !strings.Contains(jf, want) {
			problems = append(problems, "lacks "+want)
		}
	}
	// The diff base: the merge base with origin/<CHANGE_TARGET or main>
	// (fetched first), HEAD~1 on main itself.
	_, rest, ok := strings.Cut(jf, "String mutationDiffBase() {")
	if !ok {
		return append(problems, "has no mutationDiffBase()")
	}
	base, _, _ := strings.Cut(rest, "\n}\n")
	for _, want := range []string{
		"if (env.BRANCH_NAME == 'main') {",
		"git rev-parse HEAD~1",
		"String target = env.CHANGE_TARGET ?: 'main'",
		"git fetch --no-tags --quiet origin +refs/heads/${target}:refs/remotes/origin/${target}",
		`git merge-base origin/${target} HEAD`,
	} {
		if !strings.Contains(base, want) {
			problems = append(problems, "the diff base lacks "+want)
		}
	}
	return problems
}

// TestJenkinsfileMutationStageRunsTheMutationGate: the Mutation stage runs
// after Core (Quality), beside Stability, is gated on CI_MUTATION and on
// nothing else, picks full, incremental or diff mode from MUTATION_FULL,
// MUTATION_MODE, MUTATION, EXTENDED on main and the timer, runs diff mode as one 45-minute unit over the lines changed since the
// merge base with origin/<CHANGE_TARGET or main> (HEAD~1 on main), runs
// `ci/run.sh ci/mutation.sh` as its command, and lets a red gate fail the build.
func TestJenkinsfileMutationStageRunsTheMutationGate(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	if !stageBefore(jf, "Core", "Mutation") || !stageBefore(jf, "Stability", "Mutation") || !stageBefore(jf, "Mutation", "Perf") {
		t.Fatalf("stages = %q, want Mutation after Core and beside Stability, before Perf", jenkinsStages(jf))
	}
	body, _ := jenkinsStage(jf, "Mutation")
	for _, problem := range mutationGateProblems(body) {
		t.Errorf("the Mutation stage's when {}: %s:\n%s", problem, body)
	}
	expectProblems(t, jf, mutationModeProblems, map[string]string{
		"the timer not running mutation":    replaceOnce(t, jf, "boolean nightlyMutation = timer || (", "boolean nightlyMutation = ("),
		"EXTENDED on a branch running it":   replaceOnce(t, jf, "(params.EXTENDED && env.BRANCH_NAME == 'main')", "params.EXTENDED"),
		"MUTATION_FULL not forcing full":    replaceOnce(t, jf, "String mode = params.MUTATION_FULL ? 'full' : nightlyMutation", "String mode = nightlyMutation"),
		"a nightly that is full by default": replaceOnce(t, jf, "choices: ['incremental', 'full'],", "choices: ['full', 'incremental'],"),
		"incremental with no reference":     replaceOnce(t, jf, "if (!env.CI_MUTATION_REF) {", "if (false) {"),
		"every mutation run full":           replaceOnce(t, jf, "env.CI_MUTATION_MODE = mode ?: 'diff'", "env.CI_MUTATION_MODE = 'full'"),
		"mutation on every build":           replaceOnce(t, jf, "env.CI_MUTATION = mode ? 'true' : 'false'", "env.CI_MUTATION = 'true'"),
		"a diff unit with another limit":    replaceOnce(t, jf, "name: 'Mutation diff', minutes: 45,", "name: 'Mutation diff', minutes: 90,"),
		"a diff run over every line":        replaceOnce(t, jf, `"BOARD_MUTATION_DIFF=${mutationBase} " + tune +`, `"" + tune +`),
		"a diff run with no base":           replaceOnce(t, jf, "mutationBase = mutationDiffBase()", "mutationBase = 'HEAD'"),
		"a main build diffing from itself":  replaceOnce(t, jf, "git rev-parse HEAD~1", "git rev-parse HEAD"),
		"a main build checked by branch":    replaceOnce(t, jf, "if (env.BRANCH_NAME == 'main') {\n    return", "if (env.BRANCH_NAME == 'dev') {\n    return"),
		"a branch diffing from main only":   replaceOnce(t, jf, "env.CHANGE_TARGET ?: 'main'", "'main'"),
		"a base that is the target's tip":   replaceOnce(t, jf, `git merge-base origin/${target} HEAD`, `git rev-parse origin/${target}`),
		"a base never fetched":              replaceOnce(t, jf, "git fetch --no-tags --quiet origin +refs/heads/${target}:refs/remotes/origin/${target}", "true"),
		"a red mutation gate that passes":   replaceOnce(t, jf, "catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') {\n                    runUnit(u)", "catchError(buildResult: 'SUCCESS', stageResult: 'FAILURE') {\n                    runUnit(u)"),
		"reports that are not archived":     replaceOnce(t, jf, "ci-results/*.cdx.json, ci-results/mutation/*.json,", "ci-results/*.cdx.json,"),
	})
}

// TestMutationGateProblemsRejectsWrongGating: the guard check fails a Mutation
// phase that runs on EXTENDED alone (the CI_NIGHTLY guard Stability uses), on a
// push build of main, behind an anyOf, or with no guard at all, and passes the
// CI_MUTATION guard.
func TestMutationGateProblemsRejectsWrongGating(t *testing.T) {
	stage := func(when string) string {
		return "stage('Mutation') {\n when {\n" + when + "\n }\n steps { sh 'x' }\n}\n"
	}
	ok := stage("beforeAgent true\nexpression { env.CI_MUTATION == 'true' && currentBuild.currentResult != 'FAILURE' }")
	if got := mutationGateProblems(ok); len(got) != 0 {
		t.Errorf("the CI_MUTATION guard is refused: %v", got)
	}
	for name, body := range map[string]string{
		"the Stability guard (EXTENDED alone)": stage("expression { env.CI_NIGHTLY == 'true' && currentBuild.currentResult != 'FAILURE' }"),
		"a push build of main":                 stage("anyOf {\nbranch 'main'\nexpression { env.CI_MUTATION == 'true' && currentBuild.currentResult != 'FAILURE' }\n}"),
		"no guard":                             "stage('Mutation') {\n steps { sh 'x' }\n}\n",
		"no earlier-phase check":               stage("expression { env.CI_MUTATION == 'true' }"),
		"EXTENDED read directly":               stage("expression { params.EXTENDED }"),
	} {
		if got := mutationGateProblems(body); len(got) == 0 {
			t.Errorf("%s passes", name)
		}
	}
}

// fullMutationLanes reads the lane lists of the Jenkinsfile's FULL_MUTATION
// units, in order: each `lanes: '<lane>,<lane>'` entry, split on commas.
var fullMutationLaneRE = regexp.MustCompile(`\[name: '([^']+)', dir: '([^']+)', lanes: '([^']+)'\]`)

func fullMutationLanes(jf string) [][]string {
	start := strings.Index(jf, "@Field final List FULL_MUTATION = [")
	if start < 0 {
		return nil
	}
	end := strings.Index(jf[start:], "\n]\n")
	if end < 0 {
		return nil
	}
	var units [][]string
	for _, m := range fullMutationLaneRE.FindAllStringSubmatch(jf[start:start+end], -1) {
		units = append(units, strings.Split(m[3], ","))
	}
	return units
}

// fullLaneProblems: the full-mode units' lanes partition ci/quality.json's
// mutation_go.lanes, every lane in exactly one unit (so their union is every
// unit of every package, which the gate judges with lane all), and the lane
// base, whose unit runs Stryker, is one of them.
func fullLaneProblems(units [][]string, lanes map[string][]string) []string {
	var problems []string
	if len(units) != 3 {
		problems = append(problems, "full mode has "+strconv.Itoa(len(units))+" units, want 3")
	}
	seen := map[string]int{}
	for _, u := range units {
		for _, l := range u {
			seen[l]++
		}
	}
	for l, n := range seen {
		if _, ok := lanes[l]; !ok {
			problems = append(problems, "full mode runs lane "+l+", which ci/quality.json does not define")
		}
		if n > 1 {
			problems = append(problems, "full mode runs lane "+l+" in "+strconv.Itoa(n)+" units")
		}
	}
	for l := range lanes {
		if seen[l] == 0 {
			problems = append(problems, "full mode runs no unit over lane "+l)
		}
	}
	if seen["base"] != 1 {
		problems = append(problems, "no full-mode unit runs the lane base, so Stryker is skipped")
	}
	sort.Strings(problems)
	return problems
}

// groovyFunction returns the body of the Jenkinsfile's function whose
// declaration line is decl, up to its closing brace at column 0.
func groovyFunction(jf, decl string) (string, bool) {
	_, rest, ok := strings.Cut(jf, "\n"+decl+" {\n")
	if !ok {
		return "", false
	}
	body, _, _ := strings.Cut(rest, "\n}\n")
	return body, true
}

// fullMutationProblems: full mode (MUTATION_FULL, MUTATION_MODE=full, or a
// nightly that cannot be incremental) is runFullMutation(null): the
// FULL_MUTATION units side by side, each `ci/mutation.sh` over its lanes into
// its own outdir, 110 minutes and a single attempt, nproc/3 gremlins workers
// (nproc/2 when at most two units run) at GOMAXPROCS 2 (nice 10 is
// ci/mutation.sh's) under the hard memory cap MUTATION_MEMORY, then the
// Mutation gate unit that merges and judges them with ci/mutation-merge.sh and
// fails the build when red.
var mutationMemoryRE = regexp.MustCompile(`\n@Field final String MUTATION_MEMORY = 'BOARD_CI_MEMORY=[1-9][0-9]?g'\n`)

func fullMutationProblems(jf string) []string {
	var problems []string
	body, ok := jenkinsStage(jf, "Mutation")
	if !ok {
		return []string{"there is no Mutation stage"}
	}
	fn, ok := groovyFunction(jf, "def runFullMutation(Map plan)")
	if !ok {
		return []string{"there is no runFullMutation(Map plan)"}
	}
	for _, want := range []string{
		"String share = selected.size() > 2 ? '3' : '2'",
		"String fullTune = MUTATION_MEMORY + ' BOARD_MUTATION_WORKERS=$(( $(nproc) / ' + share + ' )) BOARD_MUTATION_TEST_CPU=2' +\n                    ' BOARD_MUTATION_STRYKER_CONCURRENCY=$(( $(nproc) / 2 ))'",
		"selected.each { m ->",
		"String lanes = plan == null ? m.lanes : plan['group.' + m.dir]",
		"def u = [name: m.name, minutes: 110, maxAttempts: 1,",
		"body: { unitSh(script: fullTune + \" ci/run.sh ci/mutation.sh ci-results/mutation-units/${m.dir} ci/quality.json '${lanes}'\") }]",
		"parallel branches",
		"def gate = [name: 'Mutation gate', minutes: 15,",
		"unstash 'res-' + slug(name)",
		"sh(returnStatus: true, script: 'ci/run.sh ci/mutation-merge.sh ci-results/mutation ci/quality.json ' + dirs.join(' '))",
		"catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') {\n      runUnit(gate)",
	} {
		if !strings.Contains(fn, want) {
			problems = append(problems, "runFullMutation lacks "+want)
		}
	}
	if p, g := strings.Index(fn, "parallel branches"), strings.Index(fn, "def gate ="); p < 0 || g < p {
		problems = append(problems, "the Mutation gate does not run after the full-mode units")
	}
	if !mutationMemoryRE.MatchString(jf) {
		problems = append(problems, "MUTATION_MEMORY sets no BOARD_CI_MEMORY cap in gigabytes")
	}
	if !strings.Contains(body, "String tune = MUTATION_MEMORY + ' BOARD_MUTATION_WORKERS=") {
		problems = append(problems, "diff mode's unit runs without the memory cap")
	}
	if !strings.Contains(body, "} else if (env.CI_MUTATION_MODE == 'full') {\n                runFullMutation(null)\n              } else {") {
		problems = append(problems, "the Mutation stage never runs full mode")
	}
	if n := strings.Count(body, "ci/mutation.sh ci-results/mutation ci/quality.json"); n != 1 {
		problems = append(problems, "the Mutation stage runs ci/mutation.sh into ci-results/mutation "+strconv.Itoa(n)+" times, want once (diff mode)")
	}
	return problems
}

// TestFullMutationIsThreeUnitsOverEveryPackage: full mode is three parallel
// units whose lanes cover ci/quality.json's mutation_go.lanes exactly once,
// Stryker (lane base) included, judged together by one gate over lane all;
// the lanes in turn cover every package, so the union is every package.
func TestFullMutationIsThreeUnitsOverEveryPackage(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, fullMutationProblems, map[string]string{
		"a shorter limit":            replaceOnce(t, jf, "minutes: 110, maxAttempts: 1", "minutes: 30, maxAttempts: 1"),
		"a longer limit":             replaceOnce(t, jf, "minutes: 110, maxAttempts: 1", "minutes: 360, maxAttempts: 1"),
		"more attempts":              replaceOnce(t, jf, "minutes: 110, maxAttempts: 1", "minutes: 110, maxAttempts: 3"),
		"a different worker split":   replaceOnce(t, jf, "selected.size() > 2 ? '3' : '2'", "selected.size() > 2 ? '6' : '2'"),
		"Stryker on every thread":    replaceOnce(t, jf, "STRYKER_CONCURRENCY=$(( $(nproc) / 2 ))'", "STRYKER_CONCURRENCY=$(( $(nproc) / 1 ))'"),
		"no memory cap":              replaceOnce(t, jf, "String fullTune = MUTATION_MEMORY + ' BOARD", "String fullTune = ' BOARD"),
		"an empty memory cap":        replaceOnce(t, jf, "MUTATION_MEMORY = 'BOARD_CI_MEMORY=", "MUTATION_MEMORY = 'BOARD_CI_NOTHING="),
		"diff mode uncapped":         replaceOnce(t, jf, "String tune = MUTATION_MEMORY + ' BOARD", "String tune = ' BOARD"),
		"a different per-worker CPU": replaceOnce(t, jf, "+ share + ' )) BOARD_MUTATION_TEST_CPU=2'", "+ share + ' )) BOARD_MUTATION_TEST_CPU=1'"),
		"units in series":            replaceOnce(t, jf, "    parallel branches\n", "    branches.each { k, v -> v.call() }\n"),
		"no gate":                    replaceOnce(t, jf, "ci/run.sh ci/mutation-merge.sh ci-results/mutation", "ci/run.sh true ci-results/mutation"),
		"a red gate that passes":     replaceOnce(t, jf, "catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') {\n      runUnit(gate)", "catchError(buildResult: 'SUCCESS', stageResult: 'FAILURE') {\n      runUnit(gate)"),
		"one unit over lane all":     replaceOnce(t, jf, "ci/mutation.sh ci-results/mutation-units/${m.dir} ci/quality.json '${lanes}'", "ci/mutation.sh ci-results/mutation ci/quality.json all"),
		"full mode never run":        replaceOnce(t, jf, "runFullMutation(null)\n              } else {", "echo 'skipped'\n              } else {"),
	})
	var cfg struct {
		MutationGo struct {
			Packages []string            `json:"packages"`
			Lanes    map[string][]string `json:"lanes"`
		} `json:"mutation_go"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, "ci", "quality.json")), &cfg); err != nil {
		t.Fatal(err)
	}
	lanes := cfg.MutationGo.Lanes
	units := fullMutationLanes(jf)
	for _, p := range fullLaneProblems(units, lanes) {
		t.Error(p)
	}
	for name, doctored := range map[string]string{
		"a lane left out":       replaceOnce(t, jf, "lanes: 'server-core,logic'", "lanes: 'server-core'"),
		"a lane twice":          replaceOnce(t, jf, "lanes: 'server-core,logic'", "lanes: 'server-core,logic,base'"),
		"Stryker skipped":       replaceOnce(t, jf, "dir: 'base', lanes: 'server-streams,base'", "dir: 'base', lanes: 'server-streams,nope'"),
		"an unknown lane":       replaceOnce(t, jf, "lanes: 'server-core,logic'", "lanes: 'server-core,logic,extra'"),
		"a fourth unit":         replaceOnce(t, jf, "  [name: 'Mutation streams+base+Stryker', dir: 'base', lanes: 'server-streams,base'],", "  [name: 'Mutation streams+Stryker', dir: 'base', lanes: 'server-streams'],\n  [name: 'Mutation base', dir: 'b', lanes: 'base'],"),
		"no FULL_MUTATION list": replaceOnce(t, jf, "@Field final List FULL_MUTATION = [", "@Field final List OTHER = ["),
	} {
		if len(fullLaneProblems(fullMutationLanes(doctored), lanes)) == 0 {
			t.Errorf("%s passes the lane partition check", name)
		}
	}
	covered := map[string]bool{}
	for _, pkgs := range lanes {
		for _, p := range pkgs {
			pkg, _, _ := strings.Cut(p, "#") // a unit "internal/server#ops" measures part of internal/server
			covered[pkg] = true
		}
	}
	for _, p := range cfg.MutationGo.Packages {
		if !covered[p] {
			t.Errorf("%s is in no lane of ci/quality.json, so no full-mode unit measures it", p)
		}
	}
	mutation := readRepoFile(t, "ci", "mutation.sh")
	for _, want := range []string{"lane=${3:-all}", "nice -n 10 gremlins unleash", "*,all,*|*,base,*) stryker_lane=yes"} {
		if !strings.Contains(mutation, want) {
			t.Errorf("ci/mutation.sh lacks %s", want)
		}
	}
}

// TestJenkinsStageHelpers: the helpers read stage names in order, and a
// stage body runs to its own closing brace, nested blocks included.
func TestJenkinsStageHelpers(t *testing.T) {
	jf := "stages {\n stage('A') { steps { sh 'a' } }\n stage('B') {\n when { branch 'main' }\n steps { sh 'b' }\n }\n}\n"
	if got := strings.Join(jenkinsStages(jf), ","); got != "A,B" {
		t.Fatalf("jenkinsStages = %q, want A,B", got)
	}
	body, ok := jenkinsStage(jf, "B")
	if !ok || !strings.Contains(body, "branch 'main'") || !strings.Contains(body, "sh 'b'") || strings.Contains(body, "sh 'a'") {
		t.Fatalf("jenkinsStage(B) = %q, %v", body, ok)
	}
	if _, ok := jenkinsStage(jf, "C"); ok {
		t.Fatal("jenkinsStage found a stage that does not exist")
	}
	if !stageBefore(jf, "A", "B") || stageBefore(jf, "B", "A") || stageBefore(jf, "A", "C") {
		t.Fatal("stageBefore misorders A and B, or accepts a missing stage")
	}
}

var badgeLinkRE = regexp.MustCompile(`^\[!\[(\w+)\]\(https://[^)]*/board/ci/main/(\w+)\.svg\)\]\((https://[^)\s]+)\)$`)

// TestReadmeBadgesLinkToTheMainJob: each of the four README badges is an image
// wrapped in a link to the board/ci/main job's last completed build, the way
// deck's are: build to the build, tests to Allure, coverage and quality to the
// CI summary. A badge that loses its link, or whose link stops pointing at
// /job/board/job/ci/job/main/, fails.
func TestReadmeBadgesLinkToTheMainJob(t *testing.T) {
	want := map[string]string{
		"build":    "/job/board/job/ci/job/main/lastCompletedBuild/",
		"tests":    "/job/board/job/ci/job/main/lastCompletedBuild/Allure/",
		"coverage": "/job/board/job/ci/job/main/lastCompletedBuild/CI_20summary/",
		"quality":  "/job/board/job/ci/job/main/lastCompletedBuild/CI_20summary/",
	}
	for id, problem := range badgeLinkProblems(readRepoFile(t, "README.md"), want) {
		t.Errorf("README badge %s: %s", id, problem)
	}
}

// badgeLinkProblems maps a badge id to what is wrong with its README line:
// missing, unlinked, or linked somewhere other than the expected job path.
func badgeLinkProblems(readme string, want map[string]string) map[string]string {
	problems := map[string]string{}
	seen := map[string]bool{}
	for _, l := range strings.Split(readme, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "[![") && !strings.HasPrefix(l, "![") {
			continue
		}
		m := badgeLinkRE.FindStringSubmatch(l)
		if m == nil {
			for id := range want {
				if strings.Contains(l, "/board/ci/main/"+id+".svg") {
					seen[id] = true
					problems[id] = "has no link around its image: " + l
				}
			}
			continue
		}
		id, img, link := m[1], m[2], m[3]
		seen[id] = true
		path, ok := want[id]
		switch {
		case !ok:
		case img != id:
			problems[id] = "image is " + img + ".svg"
		case !strings.HasSuffix(link, path) || len(link) == len("https://")+len(path):
			problems[id] = "links to " + link + ", want a host followed by " + path
		}
	}
	for id := range want {
		if !seen[id] {
			problems[id] = "missing"
		}
	}
	return problems
}

func TestBadgeLinkProbeCatchesAnUnlinkedOrMisdirectedBadge(t *testing.T) {
	want := map[string]string{"build": "/job/board/job/ci/job/main/lastCompletedBuild/"}
	const img = "https://b.example/jenkins-static/board/ci/main/build.svg"
	good := "[![build](" + img + ")](https://j.example/job/board/job/ci/job/main/lastCompletedBuild/)"
	if p := badgeLinkProblems(good, want); len(p) != 0 {
		t.Errorf("good line flagged: %v", p)
	}
	for name, line := range map[string]string{
		"unlinked":  "![build](" + img + ")",
		"wrong job": "[![build](" + img + ")](https://j.example/job/deck/job/ci/job/main/lastCompletedBuild/)",
		"absent":    "",
	} {
		if p := badgeLinkProblems(line, want); p["build"] == "" {
			t.Errorf("%s: not flagged", name)
		}
	}
}

// perfUnitProblems: the Perf unit (R14, SPEC 14) is its own phase after
// Stability and Mutation: one unit named Perf with a 30-minute bound, running
// ci/perf.sh into the build's results directory alone on the agent (never
// beside the stability or mutation units, whose load would be part of every
// latency it measures), only when the Stability unit passed, with perf.json
// archived.
func perfUnitProblems(jf string) []string {
	var problems []string
	perf, _ := jenkinsStage(jf, "Perf")
	if !strings.Contains(perf, "def perf = [name: 'Perf', minutes: 30,") {
		problems = append(problems, "there is no Perf unit with a 30-minute bound")
	}
	if strings.Count(perf, "ci/run.sh ci/perf.sh \"$BOARD_CI_OUT\"") != 1 {
		problems = append(problems, "the Perf unit does not run ci/perf.sh once into $BOARD_CI_OUT")
	}
	if !strings.Contains(perf, "unitBranch(perf).call()") {
		problems = append(problems, "the Perf unit is not run through unitBranch, so its failure would not fail the build")
	}
	if !strings.Contains(perf, "expression { env.CI_NIGHTLY == 'true' && env.STABILITY_RESULT == 'passing' }") {
		problems = append(problems, "the Perf unit does not wait for a passing Stability unit")
	}
	if !stageBefore(jf, "Stability and Mutation", "Perf") {
		problems = append(problems, "the Perf unit does not run after the stability and mutation units")
	}
	both, _ := jenkinsStage(jf, "Stability and Mutation")
	if strings.Contains(both, "ci/perf.sh") || strings.Contains(perf, "parallel") {
		problems = append(problems, "the Perf unit runs beside other units")
	}
	stability, _ := jenkinsStage(jf, "Stability")
	if !strings.Contains(stability, "env.STABILITY_RESULT = ") {
		problems = append(problems, "Stability does not record its result for Perf")
	}
	if !strings.Contains(jf, "ci-results/perf.json") {
		problems = append(problems, "perf.json is not archived")
	}
	return problems
}

// TestJenkinsfileStabilityRunsThePerfUnit: the Perf unit runs after the
// Stability phase with its 30-minute bound, and each property is checked from
// both sides.
func TestJenkinsfileStabilityRunsThePerfUnit(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, perfUnitProblems, map[string]string{
		"no Perf unit":                 replaceOnce(t, jf, "def perf = [name: 'Perf', minutes: 30,", "def perf = [name: 'Speed', minutes: 30,"),
		"a Perf bound of 60":           replaceOnce(t, jf, "def perf = [name: 'Perf', minutes: 30,", "def perf = [name: 'Perf', minutes: 60,"),
		"a Perf bound of 20":           replaceOnce(t, jf, "def perf = [name: 'Perf', minutes: 30,", "def perf = [name: 'Perf', minutes: 20,"),
		"Perf not running the harness": replaceOnce(t, jf, "ci/run.sh ci/perf.sh \"$BOARD_CI_OUT\"", "ci/run.sh true"),
		"Perf outside unitBranch":      replaceOnce(t, jf, "unitBranch(perf).call()", "runUnit(perf)"),
		"Perf after failed stability":  replaceOnce(t, jf, "env.CI_NIGHTLY == 'true' && env.STABILITY_RESULT == 'passing'", "env.CI_NIGHTLY == 'true'"),
		"no stability result":          replaceOnce(t, jf, "env.STABILITY_RESULT = ", "env.STABILITY = "),
		"Perf beside the stability":    replaceOnce(t, jf, "          unitBranch(perf).call()\n", "          parallel(['Perf': unitBranch(perf)])\n"),
		"perf.json not archived":       replaceOnce(t, jf, " ci-results/perf.json,", ""),
	})
}

// incrementalMutationProblems: incremental mode (the nightly's default) finds
// its reference, the last successful build whose description says
// mutation(full), plans with ci/mutation-plan.sh over the reports it copied
// (full on Sundays when the timer started the build), runs only the units the
// plan gives work, merges their reports with the reused ones, and falls back
// to full mode, saying why, when there is no plan or the plan says full.
func incrementalMutationProblems(jf string) []string {
	var problems []string
	body, _ := jenkinsStage(jf, "Mutation")
	ref, _ := groovyFunction(jf, "String mutationReference()")
	plan, _ := groovyFunction(jf, "Map mutationPlan()")
	full, _ := groovyFunction(jf, "def runFullMutation(Map plan)")
	for _, c := range []struct{ where, text, want string }{
		{"mutationReference", ref, "def b = currentBuild.previousSuccessfulBuild"},
		{"mutationReference", ref, "if ((b.description ?: '').contains('mutation(full)')) {"},
		{"mutationReference", ref, "b = b.previousSuccessfulBuild"},
		{"mutationPlan", plan, "copyArtifacts projectName: env.JOB_NAME, selector: specific(env.CI_MUTATION_REF), optional: true,"},
		{"mutationPlan", plan, "ci-results/mutation/*.json, ci-results/mutation/unit-verdict.txt, ci-results/mutation/stryker/mutation.json, ci-results/commit.txt"},
		{"mutationPlan", plan, "String weekly = timer ? 'BOARD_MUTATION_WEEKLY_FULL=yes ' : ''"},
		{"mutationPlan", plan, "weekly + 'ci/run.sh ci/mutation-plan.sh mutation-ref-raw ci-results/mutation-ref ci/quality.json ci-results/mutation-plan.txt ' + groups"},
		{"mutationPlan", plan, "return [mode: 'full', reason: 'the Mutation plan unit made no plan']"},
		{"the Mutation stage", body, "if (plan.mode == 'incremental') {"},
		{"the Mutation stage", body, `describeMutation("mutation(incremental: ${plan.run} run, ${plan.reused} reused from #${env.CI_MUTATION_REF})")`},
		{"the Mutation stage", body, "runFullMutation(plan)"},
		{"the Mutation stage", body, `describeMutation("mutation(full) [${plan.reason}]")`},
		{"runFullMutation", full, "FULL_MUTATION.findAll { plan == null || plan['group.' + it.dir] }"},
		{"runFullMutation", full, "(plan == null ? [] : ['Mutation plan'])"},
		{"runFullMutation", full, "(plan == null ? [] : ['ci-results/mutation-ref'])"},
		{"the build", jf, "writeFile file: 'ci-results/commit.txt', text: commitSha"},
		{"the build", jf, " ci-results/mutation/stryker/mutation.json,"},
	} {
		if !strings.Contains(c.text, c.want) {
			problems = append(problems, c.where+" lacks "+c.want)
		}
	}
	return problems
}

// TestIncrementalMutationReusesTheLastFullRun: see incrementalMutationProblems.
func TestIncrementalMutationReusesTheLastFullRun(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, incrementalMutationProblems, map[string]string{
		"any build as the reference":   replaceOnce(t, jf, ".contains('mutation(full)')", ".contains('mutation')"),
		"an empty reference":           replaceOnce(t, jf, "selector: specific(env.CI_MUTATION_REF)", "selector: lastSuccessful()"),
		"no weekly full run":           replaceOnce(t, jf, "timer ? 'BOARD_MUTATION_WEEKLY_FULL=yes ' : ''", "''"),
		"a failed plan as no mutation": replaceOnce(t, jf, "return [mode: 'full', reason: 'the Mutation plan unit made no plan']", "return [mode: 'none']"),
		"every unit run anyway":        replaceOnce(t, jf, "FULL_MUTATION.findAll { plan == null || plan['group.' + it.dir] }", "FULL_MUTATION.findAll { true }"),
		"the reused reports unjudged":  replaceOnce(t, jf, "(plan == null ? [] : ['ci-results/mutation-ref'])", "[]"),
		"a plain description":          replaceOnce(t, jf, "reused from #${env.CI_MUTATION_REF}", "reused"),
		"no commit for the next build": replaceOnce(t, jf, "writeFile file: 'ci-results/commit.txt', text: commitSha", "echo commitSha"),
		"Stryker's report not kept":    replaceOnce(t, jf, " ci-results/mutation/stryker/mutation.json,", ""),
	})
}
