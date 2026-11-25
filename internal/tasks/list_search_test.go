package tasks

import (
	"strings"
	"testing"
	"unicode"

	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
)

// searchTitles lists a q search and returns the titles in order with the page's next_after.
func searchTitles(t *testing.T, s Store, q Query) (string, string) {
	t.Helper()
	p, err := s.List(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	titles := make([]string, len(p.Items))
	for i, it := range p.Items {
		titles[i] = it.Title
	}
	return strings.Join(titles, "|"), p.NextAfter
}

// prefilterRows is how many tasks the SQL prefilter of q lets through, before the Go matcher.
func prefilterRows(t *testing.T, st *store.Store, q string) int {
	t.Helper()
	pre, arg, ok := textPrefilter(q)
	if !ok {
		t.Fatalf("no prefilter for %q", q)
	}
	var n int
	row := st.Reader().QueryRowContext(ctx, st.Dialect().Rebind(`SELECT COUNT(*) FROM tasks t WHERE `+pre), arg, arg)
	if err := row.Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTextSearchPrefilterIsLooserThanTheMatcherAndTheMatcherDecides(t *testing.T) {
	st, _ := storetest.OpenMigrated(t)
	s := storeOn(st)
	initiative(t, s, "aa")
	mk(t, s, "aa", "Retake the photo")
	mk(t, s, "aa", "Tax return")
	if _, err := s.Create(ctx, who, "key:d", New{Initiative: "aa", Title: "Other", Description: "pay the TAX"}); err != nil {
		t.Fatal(err)
	}
	mk(t, s, "aa", "Cafe menu")
	// "tak" is the pattern %ta_%: the prefilter keeps "Tax" in both fields, the matcher does not.
	if got, want := prefilterRows(t, st, "tak"), 3; got != want {
		t.Fatalf("the prefilter of tak lets through %d tasks, want %d (the matcher keeps 1)", got, want)
	}
	if got, _ := searchTitles(t, s, Query{Q: "tak"}); got != "Retake the photo" {
		t.Errorf("tak found %q, want only the task the matcher accepts", got)
	}
	// "é" is a wildcard in the pattern: "Cafe" passes the prefilter, only "Café" is a match.
	mk(t, s, "aa", "Café au lait")
	if got := prefilterRows(t, st, "café"); got != 2 {
		t.Fatalf("the prefilter of café lets through %d tasks, want 2", got)
	}
	if got, _ := searchTitles(t, s, Query{Q: "CAFÉ"}); got != "Café au lait" {
		t.Errorf("CAFÉ found %q, want Café au lait only", got)
	}
}

func TestTextSearchIssuesALimitOfThePageSizePlusOneAndSetsNextAfter(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	s := storeOn(st)
	initiative(t, s, "aa")
	for _, title := range []string{"plant a", "plant b", "plant c", "plant d", "plant e"} {
		mk(t, s, "aa", title)
	}
	rec.Reset()
	p, err := s.List(ctx, Query{Q: "plant", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 2 || p.NextAfter != p.Items[1].ID {
		t.Fatalf("a search matching 5 tasks with limit 2 returned %d items, next_after %q", len(p.Items), p.NextAfter)
	}
	var reads []string
	for _, r := range rec.All() {
		if r.Pool == store.PoolReader && r.Kind == storetest.Query && strings.Contains(r.SQL, "FROM tasks t JOIN initiatives i") {
			reads = append(reads, r.SQL)
		}
	}
	if len(reads) != 1 || !strings.HasSuffix(reads[0], " LIMIT 3") || !strings.Contains(reads[0], "LIKE") {
		t.Errorf("the search issued %q, want one statement with the prefilter and LIMIT 3", reads)
	}
	if next, err := s.List(ctx, Query{Q: "plant", Limit: 2, After: p.NextAfter}); err != nil || len(next.Items) != 2 || next.Items[0].Title != "plant c" {
		t.Errorf("second page = %+v, %v, want plant c and plant d", next.Items, err)
	}
}

func TestTextSearchReadsOnPastARunOfPrefilterRowsTheMatcherRejects(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	s := storeOn(st)
	initiative(t, s, "aa")
	for _, title := range []string{"tax a", "tax b", "tax c", "tax d", "tax e", "retake 1", "retake 2", "retake 3"} {
		mk(t, s, "aa", title)
	}
	rec.Reset()
	got, next := searchTitles(t, s, Query{Q: "tak", Limit: 2})
	if got != "retake 1|retake 2" || next == "" {
		t.Fatalf("limit 2 found %q, next_after %q, want the first two matches and a next page", got, next)
	}
	got, next = searchTitles(t, s, Query{Q: "tak", Limit: 5})
	if got != "retake 1|retake 2|retake 3" || next != "" {
		t.Fatalf("limit 5 found %q, next_after %q, want all three matches and no next page", got, next)
	}
}

func TestTextSearchFindsWhatTheMatcherAcceptsThatTheDatabaseFoldsDifferently(t *testing.T) {
	st, _ := storetest.OpenMigrated(t)
	s := storeOn(st)
	initiative(t, s, "aa")
	for _, title := range []string{"TAS\u212a one", "Ta\u017fk two", "\u0130\u0131 task", "École du soir", "Straße 5", "plain"} {
		mk(t, s, "aa", title)
	}
	cases := []struct{ q, want string }{
		{"task", "TAS\u212a one|Ta\u017fk two|\u0130\u0131 task"},
		{"ta\u017fk", "TAS\u212a one|Ta\u017fk two|\u0130\u0131 task"},
		{"ÉCOLE", "École du soir"},
		{"éCoLe", "École du soir"},
		{"straße", "Straße 5"},
		{"STRASSE", ""},
	}
	for _, c := range cases {
		if got, _ := searchTitles(t, s, Query{Q: c.q}); got != c.want {
			t.Errorf("%q found %q, want %q", c.q, got, c.want)
		}
	}
}

// TestPrefilterRunesAreExactlyTheOnesTheGoFoldAgreesOnWithTheDatabase checks every rune: an ASCII rune the
// prefilter writes literally is one only itself and its capital fold to; any other rune that folds to ASCII is
// written as a wildcard, so no match of the Go matcher can fail the prefilter.
func TestPrefilterRunesAreExactlyTheOnesTheGoFoldAgreesOnWithTheDatabase(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		f := []rune(fold(string(r)))
		if len(f) != 1 {
			t.Fatalf("fold of %U is %d runes, the pattern needs one per rune", r, len(f))
		}
		if f[0] <= unicode.MaxASCII && r > unicode.MaxASCII && foldsToItself(f[0]) {
			t.Errorf("%U folds to ASCII %q, which the prefilter would write as a literal", r, f[0])
		}
		if r <= unicode.MaxASCII && f[0] != unicode.ToLower(r) {
			t.Errorf("ASCII %q folds to %q, not its lower case", r, f[0])
		}
	}
}
