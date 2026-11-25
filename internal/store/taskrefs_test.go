package store_test

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/n-orlov/village-hall/internal/store"
)

func TestTaskIDsOfRefsLooksUpOnlyExactShortRefsInOneParameter(t *testing.T) {
	d := store.SQLite{}
	sql, param, ok := store.TaskIDsOfRefs(d, []string{"aa-2", "AA-3", "aa-02", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "aa-", "b-b-10"})
	if !ok || param != `[["aa",2],["b-b",10]]` {
		t.Fatalf("TaskIDsOfRefs = %q, %q, %v, want the (slug, seq) of aa-2 and b-b-10 only", sql, param, ok)
	}
	if got := strings.Count(sql, "?"); got != 1 {
		t.Errorf("%d placeholders, want 1: %s", got, sql)
	}
	if !strings.Contains(sql, d.RefSet()) || !strings.Contains(sql, "rt.seq = r.seq") {
		t.Errorf("the lookup does not go through the (initiative, seq) key: %s", sql)
	}
	for _, none := range [][]string{nil, {}, {"AA-2"}, {"aa-0"}, {"01ARZ3NDEKTSV4RRFFQ69G5FAV"}} {
		if sql, param, ok := store.TaskIDsOfRefs(d, none); ok || sql != "" || param != "" {
			t.Errorf("TaskIDsOfRefs(%q) = %q, %q, %v, want nothing to select", none, sql, param, ok)
		}
	}
	if got := (store.Postgres{}).Rebind((store.Postgres{}).RefSet()); !strings.Contains(got, "jsonb_array_elements($1::jsonb)") || strings.Contains(got, "$2") {
		t.Errorf("Postgres RefSet = %q, want one jsonb parameter", got)
	}
}

// TestRefSetIsTheSlugAndSeqOfEachPairOfOneParameter runs RefSet on every database leg: a thousand pairs bind one
// parameter, and each comes back with its slug as text and its seq as an integer.
func TestRefSetIsTheSlugAndSeqOfEachPairOfOneParameter(t *testing.T) {
	eachLeg(t, func(t *testing.T, leg string) {
		url := "sqlite://" + filepath.Join(t.TempDir(), "board.db")
		if leg == "postgres" {
			url = testPostgres.Load().create(t, "")
		}
		s, err := store.Open(url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		var refs []string
		for i := 1; i <= 1000; i++ {
			refs = append(refs, "a-b-"+strconv.Itoa(i))
		}
		_, param, ok := store.TaskIDsOfRefs(s.Dialect(), refs)
		if !ok {
			t.Fatal("no refs")
		}
		var n, sum int
		var slugs int
		err = s.Reader().QueryRow(s.Dialect().Rebind(
			`SELECT COUNT(*), SUM(seq), SUM(CASE WHEN slug = 'a-b' THEN 1 ELSE 0 END) FROM (`+s.Dialect().RefSet()+`) r`), param).Scan(&n, &sum, &slugs)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1000 || sum != 500500 || slugs != 1000 {
			t.Errorf("RefSet gave %d rows, seq sum %d, %d slugs a-b; want 1000, 500500, 1000", n, sum, slugs)
		}
	})
}
