package repocheck

import (
	"strings"
	"testing"
)

// pkgtimeProblems: the Quality unit runs ci/pkgtime over the Suite unit's Go
// junit and the committed budgets, after ci/quality.sh, and fails when either
// that check or the gates fail. The Jenkinsfile is probed in the Quality
// branch's text only.
func pkgtimeProblems(jf string) []string {
	var problems []string
	start := strings.Index(jf, "branches['Quality'] = {")
	if start < 0 {
		return []string{"the Jenkinsfile has no Quality branch"}
	}
	quality := jf[start:]
	if end := strings.Index(quality, "parallel branches"); end >= 0 {
		quality = quality[:end]
	}
	call := `go run ./ci/pkgtime -junit "$BOARD_CI_OUT/junit-go.xml" -budgets ci/pkgtime/budgets.json`
	i := strings.Index(quality, call)
	if i < 0 {
		return append(problems, "Quality never runs ci/pkgtime over $BOARD_CI_OUT/junit-go.xml and ci/pkgtime/budgets.json")
	}
	gates := strings.Index(quality, "ci/quality.sh")
	if gates < 0 || gates > i {
		problems = append(problems, "ci/pkgtime does not run after ci/quality.sh")
	}
	if !strings.Contains(quality, "returnStatus: true, script: 'ci/run.sh "+call) {
		problems = append(problems, "ci/pkgtime's status is not read")
	}
	if !strings.Contains(quality, "gates != 0 ? gates : budgets") {
		problems = append(problems, "Quality's result does not carry both the gates' and ci/pkgtime's status")
	}
	return problems
}

func TestQualityRunsThePackageTimeBudgetAfterTheGates(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, pkgtimeProblems, map[string]string{
		"no check": replaceOnce(t, jf, "go run ./ci/pkgtime -junit", "true -junit"),
		"other junit": replaceOnce(t, jf, `-junit "$BOARD_CI_OUT/junit-go.xml"`,
			`-junit "$BOARD_CI_OUT/junit-features.xml"`),
		"other budgets": replaceOnce(t, jf, "-budgets ci/pkgtime/budgets.json'", "-budgets /dev/null'"),
		"status dropped": replaceOnce(t, jf, "gates != 0 ? gates : budgets",
			"gates"),
		"status not read": replaceOnce(t, jf, "int budgets = sh(returnStatus: true, script: 'ci/run.sh go run ./ci/pkgtime",
			"int budgets = sh(script: 'ci/run.sh go run ./ci/pkgtime"),
	})
}
