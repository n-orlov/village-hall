// unitparallel_test.go pins where the Go unit pass's package parallelism lives
// (task 026, R9 f): `-p` of ci/suite.sh's unit pass is read from one variable,
// unit_package_parallelism, which holds 2 or 3 and no more, and the features
// pass keeps `-p=1`. The header of ci/suite.sh documents it.
package suitecheck

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const maxUnitPackageParallelism = 3

var (
	unitParallelismAssign = regexp.MustCompile(`(?m)^unit_package_parallelism=(\S*)\s*$`)
	unitPassCommand       = regexp.MustCompile(`(?m)^\s*-- (\S+) -count=1 -skip '\^TestFeatures\$'`)
	anyPFlag              = regexp.MustCompile(`-p=`)
	featuresPassCommand   = regexp.MustCompile(`(?m)^\s*-- (\S+) -count=1 -run '\^TestFeatures\$'`)
)

// unitParallelismProblem returns "" when the unit pass's -p is defined once, is
// 2 or 3 and is the only -p the pass uses, and the features pass is at -p=1;
// otherwise what is wrong. suite is the text of ci/suite.sh.
func unitParallelismProblem(suite string) string {
	var header []string
	for _, l := range strings.Split(suite, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			break
		}
		header = append(header, l)
	}
	if !strings.Contains(strings.Join(header, "\n"), "unit_package_parallelism") {
		return "the ci/suite.sh header does not document unit_package_parallelism"
	}
	code := stripCommentLines(suite)
	assigns := unitParallelismAssign.FindAllStringSubmatch(code, -1)
	if len(assigns) != 1 {
		return "ci/suite.sh must assign unit_package_parallelism exactly once (found " + strconv.Itoa(len(assigns)) + ")"
	}
	n, err := strconv.Atoi(assigns[0][1])
	if err != nil {
		return "unit_package_parallelism is '" + assigns[0][1] + "', not a number literal"
	}
	if n < 2 || n > maxUnitPackageParallelism {
		return "unit_package_parallelism is " + strconv.Itoa(n) + ": the unit pass runs 2 to " + strconv.Itoa(maxUnitPackageParallelism) + " packages at once"
	}
	unit := unitPassCommand.FindAllStringSubmatch(code, -1)
	if len(unit) != 1 || unit[0][1] != `"-p=$unit_package_parallelism"` {
		return "the unit pass (-skip '^TestFeatures$') must take its -p from \"-p=$unit_package_parallelism\" and carry no other"
	}
	features := featuresPassCommand.FindAllStringSubmatch(code, -1)
	if len(features) != 1 || features[0][1] != "-p=1" {
		return "the features pass (-run '^TestFeatures$') must stay at -p=1"
	}
	if len(anyPFlag.FindAllString(code, -1)) != 2 {
		return "ci/suite.sh spells a -p= flag somewhere besides the two passes' own"
	}
	return ""
}

// TestUnitPassRunsPackagesAtTwoOrThreeAtOnce pins the real script.
func TestUnitPassRunsPackagesAtTwoOrThreeAtOnce(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	raw, err := os.ReadFile(suiteScriptPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if problem := unitParallelismProblem(string(raw)); problem != "" {
		t.Error(problem)
	}
}

// TestUnitParallelismProblemFlagsDoctoredScripts shows the pin fails on a -p of
// 4, of 1 or of none, on a -p spelled out in the command, on a features pass
// above -p=1 and on an undocumented header.
func TestUnitParallelismProblemFlagsDoctoredScripts(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	raw, err := os.ReadFile(suiteScriptPath(root))
	if err != nil {
		t.Fatal(err)
	}
	suite := string(raw)
	swap := func(old, repl string) string {
		if !strings.Contains(suite, old) {
			t.Fatalf("doctoring needs %q in ci/suite.sh", old)
		}
		return strings.Replace(suite, old, repl, 1)
	}
	assign := unitParallelismAssign.FindString(suite)
	if assign == "" {
		t.Fatal("no unit_package_parallelism assignment to doctor")
	}
	head, body, ok := strings.Cut(suite, "\nset -eu\n")
	if !ok {
		t.Fatal("no `set -eu` line ends the ci/suite.sh header")
	}
	undocumented := strings.ReplaceAll(head, "unit_package_parallelism", "the variable") + "\nset -eu\n" + body
	cases := map[string]string{
		"a -p of 4":                  swap(assign, "unit_package_parallelism=4"),
		"a -p of 1":                  swap(assign, "unit_package_parallelism=1"),
		"a -p of 0":                  swap(assign, "unit_package_parallelism=0"),
		"a -p that is not a number":  swap(assign, "unit_package_parallelism=${BOARD_P:-2}"),
		"a second assignment":        suite + "\nunit_package_parallelism=3\n",
		"a literal -p=4 in the pass": swap(`"-p=$unit_package_parallelism" -count=1`, "-p=4 -count=1"),
		"a literal -p=2 in the pass": swap(`"-p=$unit_package_parallelism" -count=1`, "-p=2 -count=1"),
		"a features pass at -p=2":    swap("-- -p=1 -count=1 -run '^TestFeatures$'", "-- -p=2 -count=1 -run '^TestFeatures$'"),
		"an undocumented header":     undocumented,
	}
	for name, doctored := range cases {
		if problem := unitParallelismProblem(doctored); problem == "" {
			t.Errorf("%s: the pin accepts it", name)
		}
	}
}
