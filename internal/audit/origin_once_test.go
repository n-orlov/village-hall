package audit

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/n-orlov/village-hall/internal/store/storetest"
)

// operatorLookups is how many statements since the last Reset look an operator
// row up by handle (OperatorID), the cost of resolving a request's origin.
func operatorLookups(rec *storetest.Recorder) int {
	n := 0
	for _, s := range rec.All() {
		if strings.HasPrefix(s.SQL, "SELECT id FROM actors WHERE handle = ?") {
			n++
		}
	}
	return n
}

func actorStatements(rec *storetest.Recorder) []string {
	var out []string
	for _, s := range rec.All() {
		if strings.Contains(s.SQL, "actors") && !strings.Contains(s.SQL, "INSERT INTO events") {
			out = append(out, s.SQL)
		}
	}
	return out
}

func TestTheActorIsOneUpsertStatementWhateverTheRequestSends(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	d := st.Dialect()
	bg := context.Background()
	inTx(t, st, func(tx *sql.Tx) {
		if _, err := UpsertActor(bg, tx, d, Actor{Handle: "alice-op", Operator: true}, t0); err != nil {
			t.Fatal(err)
		}
	})
	for name, tc := range map[string]struct {
		ctx context.Context
		who Actor
	}{
		"an agent with way back and meta through an owned key": {WithOrigin(bg, "alice-op"), Actor{Handle: "claude", WayBack: "cmd", Meta: `{"k":1}`}},
		"an agent through an ownerless key":                    {WithOrigin(bg, ""), Actor{Handle: "claude"}},
		"an agent outside a request":                           {bg, Actor{Handle: "claude"}},
		"a session promoting the agent row":                    {WithOrigin(bg, "alice-op"), Actor{Handle: "claude", Operator: true}},
	} {
		rec.Reset()
		inTx(t, st, func(tx *sql.Tx) {
			if _, err := UpsertActor(tc.ctx, tx, d, tc.who, t0); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		})
		got := actorStatements(rec)
		if len(got) != 1 || !strings.HasPrefix(got[0], "INSERT INTO actors") || !strings.Contains(got[0], "ON CONFLICT (handle) DO UPDATE") || !strings.Contains(got[0], "RETURNING") {
			t.Errorf("%s: actor statements = %q, want the one INSERT ... ON CONFLICT DO UPDATE ... RETURNING", name, got)
		}
	}
}

func TestTheOriginIsResolvedOncePerRequestNotPerEvent(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	d := st.Dialect()
	bg := context.Background()
	inTx(t, st, func(tx *sql.Tx) {
		if err := Append(bg, tx, d, Actor{Handle: "alice-op", Operator: true}, t0, Event{Principal: "oidc:a", Type: "t", Summary: "alice signs in"}); err != nil {
			t.Fatal(err)
		}
		if err := Append(bg, tx, d, Actor{Handle: "bob-op", Operator: true}, t0, Event{Principal: "oidc:b", Type: "t", Summary: "bob signs in"}); err != nil {
			t.Fatal(err)
		}
	})
	events := func(ctx context.Context, tx *sql.Tx, actor Actor, summaries ...string) {
		t.Helper()
		id, err := UpsertActor(ctx, tx, d, actor, t0)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range summaries {
			if err := AppendAs(ctx, tx, d, id, t0, Event{Principal: "key:k", Type: "t", Summary: s}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// An agent row's upsert resolves the origin in its own statement: no lookup at all.
	rec.Reset()
	inTx(t, st, func(tx *sql.Tx) { events(WithOrigin(bg, "alice-op"), tx, Actor{Handle: "claude"}, "a1", "a2", "a3") })
	if n := operatorLookups(rec); n != 0 {
		t.Errorf("three events of an agent row looked the origin up %d times, want 0", n)
	}
	// An operator row acting through another operator's key needs one lookup, remembered.
	rec.Reset()
	inTx(t, st, func(tx *sql.Tx) { events(WithOrigin(bg, "bob-op"), tx, Actor{Handle: "alice-op"}, "o1", "o2", "o3") })
	if n := operatorLookups(rec); n != 1 {
		t.Errorf("three events of an operator row looked the origin up %d times, want 1", n)
	}
	for _, s := range []string{"a1", "a2", "a3"} {
		if got := originOf(t, st, eventOriginQuery, s); got != "alice-op" {
			t.Errorf("%s origin = %q, want alice-op", s, got)
		}
	}
	for _, s := range []string{"o1", "o2", "o3"} {
		if got := originOf(t, st, eventOriginQuery, s); got != "bob-op" {
			t.Errorf("%s origin = %q, want bob-op", s, got)
		}
	}
}

// What one transaction resolved is not trusted by the next: the operator row it
// saw may have been created by a transaction that rolled back.
func TestAnOriginResolvedInARolledBackTransactionIsResolvedAgain(t *testing.T) {
	st, _ := storetest.OpenMigrated(t)
	d := st.Dialect()
	ctx := WithOrigin(context.Background(), "carol-op")
	tx, err := st.Writer().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertActor(ctx, tx, d, Actor{Handle: "carol-op", Operator: true}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertActor(ctx, tx, d, Actor{Handle: "claude"}, t0); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	inTx(t, st, func(tx *sql.Tx) {
		if err := Append(ctx, tx, d, Actor{Handle: "claude"}, t0, Event{Principal: "key:k", Type: "t", Summary: "after the rollback"}); err != nil {
			t.Fatal(err)
		}
	})
	if got := originOf(t, st, eventOriginQuery, "after the rollback"); got != "" {
		t.Errorf("origin = %q, want none: carol-op was never committed", got)
	}
}

// An operator row created after an agent row resolved the origin in the same
// transaction is seen by the events that follow.
func TestAnOperatorRowCreatedLaterInTheTransactionIsTheOrigin(t *testing.T) {
	st, _ := storetest.OpenMigrated(t)
	d := st.Dialect()
	ctx := WithOrigin(context.Background(), "dana-op")
	inTx(t, st, func(tx *sql.Tx) {
		if _, err := UpsertActor(ctx, tx, d, Actor{Handle: "claude"}, t0); err != nil { // no dana-op yet: origin null
			t.Fatal(err)
		}
		if err := Append(ctx, tx, d, Actor{Handle: "dana-op", Operator: true}, t0, Event{Principal: "oidc:d", Type: "t", Summary: "dana signs in"}); err != nil {
			t.Fatal(err)
		}
	})
	if got := originOf(t, st, eventOriginQuery, "dana signs in"); got != "dana-op" {
		t.Errorf("origin = %q, want dana-op", got)
	}
}
