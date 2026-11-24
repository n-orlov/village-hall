package features

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestGodogPathsUnsetRunsWholeDirectory: with no selector the default suite
// runs the whole directory, bundles or not.
func TestGodogPathsUnsetRunsWholeDirectory(t *testing.T) {
	for _, spec := range []string{"", " ", ",", " , "} {
		got, err := resolveGodogPaths(spec, ".", godogBundles)
		if err != nil || !reflect.DeepEqual(got, []string{"."}) {
			t.Errorf("resolveGodogPaths(%q) = %v, %v; want [.], nil", spec, got, err)
		}
	}
}

// TestGodogPathsKeysBundle: keys.feature, with or without the repository's
// features/ prefix, expands to the three API-key feature files, each once.
func TestGodogPathsKeysBundle(t *testing.T) {
	want := []string{"admin_keys.feature", "api_keys.feature", "ui_keys.feature"}
	for _, spec := range []string{
		"features/keys.feature",
		"keys.feature",
		" features/keys.feature ",
		"keys.feature,api_keys.feature",
		"features/api_keys.feature,features/keys.feature,keys.feature",
	} {
		got, err := resolveGodogPaths(spec, ".", godogBundles)
		if err != nil {
			t.Fatalf("resolveGodogPaths(%q): %v", spec, err)
		}
		if len(got) != len(want) {
			t.Errorf("resolveGodogPaths(%q) = %v; want each of %v once", spec, got, want)
			continue
		}
		for _, w := range want {
			n := 0
			for _, g := range got {
				if g == w {
					n++
				}
			}
			if n != 1 {
				t.Errorf("resolveGodogPaths(%q) = %v; want %s exactly once", spec, got, w)
			}
		}
	}
}

// TestGodogPathsBundlesNameExistingFilesOnly: every bundle member exists in
// this package and no bundle shadows a real feature file of its own name.
func TestGodogPathsBundlesNameExistingFilesOnly(t *testing.T) {
	if len(godogBundles) == 0 {
		t.Fatal("godogBundles is empty; keys.feature must stay a bundle")
	}
	for name, members := range godogBundles {
		if _, err := os.Stat(name); err == nil {
			t.Errorf("bundle %q shadows a real file of the same name", name)
		}
		if len(members) == 0 {
			t.Errorf("bundle %q has no members", name)
		}
		for _, m := range members {
			if _, err := os.Stat(m); err != nil {
				t.Errorf("bundle %q: member %q: %v", name, m, err)
			}
		}
	}
}

// TestGodogPathsFilesAndLines: plain files resolve as given, with the
// features/ prefix dropped and any :<line> suffix kept.
func TestGodogPathsFilesAndLines(t *testing.T) {
	cases := map[string][]string{
		"health.feature":                         {"health.feature"},
		"features/health.feature":                {"health.feature"},
		"health.feature:3,features/keys.feature": {"health.feature:3", "admin_keys.feature", "api_keys.feature", "ui_keys.feature"},
		"health.feature,health.feature":          {"health.feature"},
	}
	for spec, want := range cases {
		got, err := resolveGodogPaths(spec, ".", godogBundles)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("resolveGodogPaths(%q) = %v, %v; want %v, nil", spec, got, err, want)
		}
	}
}

// TestGodogPathsRejectsMissing: a selector naming no file or bundle fails, on
// its own or beside valid ones, and a bundle with a missing member fails;
// neither ever becomes a smaller run.
func TestGodogPathsRejectsMissing(t *testing.T) {
	for _, spec := range []string{
		"nope.feature",
		"features/nope.feature",
		"features/features/health.feature",
		"health.feature,nope.feature",
		"keys.feature,nope.feature",
		"keys.feature:3",
		"key.feature",
	} {
		if got, err := resolveGodogPaths(spec, ".", godogBundles); err == nil {
			t.Errorf("resolveGodogPaths(%q) = %v, nil; want an error", spec, got)
		} else if !strings.Contains(err.Error(), "names no feature file") {
			t.Errorf("resolveGodogPaths(%q) error %q does not name the missing selector", spec, err)
		}
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.feature"), []byte("Feature: a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundles := map[string][]string{"both.feature": {"a.feature", "gone.feature"}}
	if got, err := resolveGodogPaths("both.feature", dir, bundles); err == nil {
		t.Errorf("bundle with a missing member resolved to %v; want an error", got)
	} else if !strings.Contains(err.Error(), "gone.feature") {
		t.Errorf("bundle error %q does not name the missing member", err)
	}
	if got, err := resolveGodogPaths("features/a.feature", dir, bundles); err != nil || !reflect.DeepEqual(got, []string{"a.feature"}) {
		t.Errorf("resolveGodogPaths(features/a.feature) in %s = %v, %v", dir, got, err)
	}
}

// TestGodogPathsStaysInsidePackage: a selector that climbs out of the package
// directory, or names the directory itself as a bundle member's neighbour, is
// rejected like any other nonexistent selector, and a bundle name is matched
// exactly (no prefix, case or extension variants).
func TestGodogPathsStaysInsidePackage(t *testing.T) {
	for _, spec := range []string{
		"../features/health.feature",
		"/health.feature",
		"Keys.feature",
		"keys",
		"features/keys.feature.bak",
		"features/keys.feature/",
	} {
		if got, err := resolveGodogPaths(spec, ".", godogBundles); err == nil {
			t.Errorf("resolveGodogPaths(%q) = %v, nil; want an error", spec, got)
		}
	}
}
