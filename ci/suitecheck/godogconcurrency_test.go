// godogconcurrency_test.go pins how features/godog_test.go runs the Godog scenarios (task 023, R9 c): the default
// suite runs several scenarios at once, every scenario with a harness of its own, and the scenarios that must be
// alone (@dedicated-database, which starts and stops a Postgres container, and @serve-upgrade, the tag of
// serve_upgrade.feature) run afterwards in a second suite of one. The check parses godog_test.go: the featureSuites
// table must hold a "concurrent" suite whose Concurrency is above 1 and whose tag filter excludes both tags, and a
// "serial" suite of Concurrency 1 that selects both; godog.Options must take its Concurrency and Tags from the
// suite, and the scenario initializer must build the harness for each scenario.
//
// SUITECHECK_GODOG_TEST_GO points the check at another godog_test.go (a checkout of an older commit, say); it
// defaults to the repository's own features/godog_test.go.
package suitecheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// godogSource returns the text of features/godog_test.go (or SUITECHECK_GODOG_TEST_GO).
func godogSource(t *testing.T) string {
	t.Helper()
	path := os.Getenv("SUITECHECK_GODOG_TEST_GO")
	if path == "" {
		root, err := repositoryRoot()
		if err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(root, "features", "godog_test.go")
	}
	raw, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// constEnv evaluates the constant expressions of one file: integer and string literals, names of its own
// constants, and string concatenation.
type constEnv map[string]ast.Expr

func (env constEnv) str(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.Ident:
		if def, ok := env[v.Name]; ok {
			return env.str(def)
		}
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, lok := env.str(v.X)
		r, rok := env.str(v.Y)
		return l + r, lok && rok
	case *ast.ParenExpr:
		return env.str(v.X)
	}
	return "", false
}

func (env constEnv) integer(e ast.Expr) (int, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.INT {
			return 0, false
		}
		n, err := strconv.Atoi(v.Value)
		return n, err == nil
	case *ast.Ident:
		if def, ok := env[v.Name]; ok {
			return env.integer(def)
		}
	case *ast.ParenExpr:
		return env.integer(v.X)
	}
	return 0, false
}

// godogSuiteProblems lists what is wrong with the way src runs the Godog scenarios; empty means nothing.
func godogSuiteProblems(src string) []string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "godog_test.go", src, 0)
	if err != nil {
		return []string{"godog_test.go does not parse: " + err.Error()}
	}
	env := constEnv{}
	var suites *ast.CompositeLit
	var options []*ast.CompositeLit
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if gen.Tok == token.CONST {
					env[name.Name] = vs.Values[i]
				}
				if lit, ok := vs.Values[i].(*ast.CompositeLit); ok && gen.Tok == token.VAR && name.Name == "featureSuites" {
					suites = lit
				}
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if lit, ok := n.(*ast.CompositeLit); ok {
			if sel, ok := lit.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Options" {
				options = append(options, lit)
			}
		}
		return true
	})

	var problems []string
	if suites == nil {
		return append(problems, "no `var featureSuites = []featureSuite{...}` table")
	}
	byName := map[string]map[string]ast.Expr{}
	for _, el := range suites.Elts {
		lit, ok := el.(*ast.CompositeLit)
		if !ok {
			continue
		}
		fields := map[string]ast.Expr{}
		for _, kv := range lit.Elts {
			if kv, ok := kv.(*ast.KeyValueExpr); ok {
				if key, ok := kv.Key.(*ast.Ident); ok {
					fields[key.Name] = kv.Value
				}
			}
		}
		if name, ok := env.str(fields["name"]); ok {
			byName[name] = fields
		}
	}

	concurrent, serial := byName["concurrent"], byName["serial"]
	if concurrent == nil {
		problems = append(problems, `featureSuites has no suite named "concurrent"`)
	} else {
		n, ok := env.integer(concurrent["concurrency"])
		switch {
		case concurrent["concurrency"] == nil:
			problems = append(problems, "the concurrent suite sets no concurrency")
		case !ok || n <= 1:
			problems = append(problems, "the concurrent suite's concurrency is not an integer above 1")
		}
		tags, ok := env.str(concurrent["tags"])
		if !ok {
			problems = append(problems, "the concurrent suite's tags are not a constant string")
		}
		have := strings.Split(strings.ReplaceAll(tags, " ", ""), "&&")
		for _, tag := range []string{"@dedicated-database", "@serve-upgrade"} {
			if !contains(have, "~"+tag) {
				problems = append(problems, "the concurrent suite does not exclude "+tag+" (its tags are "+strconv.Quote(tags)+")")
			}
		}
	}
	if serial == nil {
		problems = append(problems, `featureSuites has no suite named "serial"`)
	} else {
		if n, ok := env.integer(serial["concurrency"]); !ok || n != 1 {
			problems = append(problems, "the serial suite's concurrency is not 1")
		}
		tags, _ := env.str(serial["tags"])
		have := strings.Split(strings.ReplaceAll(tags, " ", ""), ",")
		for _, tag := range []string{"@dedicated-database", "@serve-upgrade"} {
			if !contains(have, tag) {
				problems = append(problems, "the serial suite does not select "+tag+" (its tags are "+strconv.Quote(tags)+")")
			}
		}
	}

	if len(options) == 0 {
		problems = append(problems, "no godog.Options literal")
	}
	for _, lit := range options {
		keys := map[string]string{}
		for _, kv := range lit.Elts {
			if kv, ok := kv.(*ast.KeyValueExpr); ok {
				if key, ok := kv.Key.(*ast.Ident); ok {
					keys[key.Name] = exprText(kv.Value)
				}
			}
		}
		if !strings.HasSuffix(keys["Concurrency"], ".concurrency") {
			problems = append(problems, "a godog.Options literal does not take its Concurrency from the suite (Concurrency: <suite>.concurrency)")
		}
		if !strings.Contains(keys["Tags"], "tags") {
			problems = append(problems, "a godog.Options literal does not take its Tags from the suite")
		}
	}
	if !strings.Contains(src, "range featureSuites {") {
		problems = append(problems, "TestFeatures does not run every suite of featureSuites")
	}
	if !strings.Contains(src, "&harness{") {
		problems = append(problems, "the scenario initializer builds no harness of its own (&harness{...} per scenario)")
	}
	if strings.Contains(src, "h := &harness{") {
		problems = append(problems, "one harness is shared by every scenario")
	}
	return problems
}

// exprText renders an expression as its source-like text, enough to read a selector chain or a call.
func exprText(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	case *ast.CallExpr:
		args := make([]string, len(v.Args))
		for i, a := range v.Args {
			args[i] = exprText(a)
		}
		return exprText(v.Fun) + "(" + strings.Join(args, ",") + ")"
	case *ast.BasicLit:
		return v.Value
	}
	return "?"
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestGodogDefaultSuiteRunsConcurrentlyWithASerialSuiteForTheScenariosThatMustBeAlone(t *testing.T) {
	for _, problem := range godogSuiteProblems(godogSource(t)) {
		t.Error(problem)
	}
}

// TestGodogSuiteCheckFailsOnADoctoredGodogTest doctors the real file one way at a time; each must be named.
func TestGodogSuiteCheckFailsOnADoctoredGodogTest(t *testing.T) {
	src := godogSource(t)
	cases := []struct{ name, from, to, want string }{
		{"no concurrency on the default suite", "concurrency: godogConcurrency, ", "", "sets no concurrency"},
		{"concurrency of one", "const godogConcurrency = 2", "const godogConcurrency = 1", "not an integer above 1"},
		{"serve-upgrade exclusion dropped", ` + " && ~" + tagServeUpgrade`, ``, "does not exclude @serve-upgrade"},
		{"dedicated-database exclusion dropped", `"~" + tagDedicated + " && ~" + tagServeUpgrade`, `"~" + tagServeUpgrade`, "does not exclude @dedicated-database"},
		{"serial suite not serial", "concurrency: 1,", "concurrency: 2,", "serial suite's concurrency is not 1"},
		{"serial suite loses serve-upgrade", `tagDedicated + "," + tagServeUpgrade`, `tagDedicated`, "does not select @serve-upgrade"},
		{"options without concurrency", "Concurrency: fs.concurrency,", "", "does not take its Concurrency from the suite"},
		{"options with fixed concurrency", "Concurrency: fs.concurrency,", "Concurrency: 1,", "does not take its Concurrency from the suite"},
		{"shared harness", "&harness{binary: bin, leg: leg, shared: shared}", "h", "builds no harness of its own"},
		{"only the first suite runs", "range featureSuites {", "range featureSuites[:1] {", "does not run every suite"},
	}
	for _, c := range cases {
		if !strings.Contains(src, c.from) {
			t.Fatalf("%s: godog_test.go no longer contains %q; update this test with it", c.name, c.from)
		}
		doctored := strings.Replace(src, c.from, c.to, 1)
		problems := godogSuiteProblems(doctored)
		if !anyContains(problems, c.want) {
			t.Errorf("%s: problems %q do not name %q", c.name, problems, c.want)
		}
	}
	if problems := godogSuiteProblems(src); len(problems) != 0 {
		t.Errorf("the real godog_test.go has problems: %q", problems)
	}
}

func anyContains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestServeUpgradeFeatureCarriesTheSerialTag: the serial suite selects @serve-upgrade, so every scenario of
// serve_upgrade.feature must carry it, on the Feature line's tag block.
func TestServeUpgradeFeatureCarriesTheSerialTag(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "features", "serve_upgrade.feature"))
	if err != nil {
		t.Fatal(err)
	}
	var tagged bool
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Feature:") {
			break
		}
		if contains(strings.Fields(line), "@serve-upgrade") {
			tagged = true
		}
	}
	if !tagged {
		t.Error("features/serve_upgrade.feature has no @serve-upgrade tag above its Feature line")
	}
}
