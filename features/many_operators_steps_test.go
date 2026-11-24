package features

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/cucumber/godog"

	"github.com/n-orlov/village-hall/internal/store"
)

// phase4Head is the last migration the Phase 4 release shipped.
const phase4Head = 11

// seedRows is one table of the Phase 4 seed: the columns written, the rows of
// values for them, and the columns left to the schema's defaults.
type seedRows struct {
	name     string
	key      string
	cols     []string
	rows     [][]any
	defaults []string
}

func (t seedRows) allColumns() []string {
	return append(append([]string{}, t.cols...), t.defaults...)
}

// phase4Seed is one operator, two agents, one initiative they work in, one API
// key and two events, in foreign-key order.
var phase4Seed = []seedRows{
	{name: "actors", key: "id", defaults: []string{"n"},
		cols: []string{"id", "handle", "kind", "display_name", "host", "way_back", "meta", "first_seen_at", "last_seen_at", "protocol_seen"},
		rows: [][]any{
			{"ACTOR1", "operator", "operator", "Op", "host", "ssh op", `{"os":"linux"}`, 1000, 2000, 1},
			{"ACTOR2", "claude@laptop", "agent", "Claude", "laptop", "claude --resume x", `{}`, 1100, 2100, 4},
			{"ACTOR3", "codex@laptop", "agent", "Codex", "laptop", "codex resume y", `{"os":"mac"}`, 1200, 2200, 3},
		}},
	{name: "initiatives", key: "id", defaults: []string{"n"},
		cols: []string{"id", "slug", "title", "description", "status", "target_start", "target_end", "created_by", "created_at", "closed_at", "closed_by", "next_task_seq"},
		rows: [][]any{{"INIT1", "phone-onboard", "Phone", "Markdown *text*", "closed", "2026-03-01", "2026-04-01", "ACTOR1", 3000, 4000, "ACTOR1", 7}}},
	{name: "api_keys", key: "id", defaults: []string{"n"},
		cols: []string{"id", "name", "key_hash", "display_prefix", "created_at", "created_by", "last_used_at", "expires_at", "revoked_at"},
		rows: [][]any{{"KEY1", "laptop", "hash1", "brd_ab", 5000, "admin-cli", 6000, 7000, 8000}}},
	{name: "events", key: "id", defaults: []string{"n"},
		cols: []string{"id", "at", "actor_id", "principal", "on_behalf_of", "initiative_id", "task_id", "type", "ref_id", "summary", "data"},
		rows: [][]any{
			{"EV1", 9300, "ACTOR1", "oidc:op@example.com", "ACTOR1", "INIT1", "TASK1", "initiative_created", "INIT1", "initiative phone-onboard created", `{"slug":"phone-onboard"}`},
			{"EV2", 9400, "ACTOR2", "key:laptop", "ACTOR3", "INIT1", "TASK2", "initiative_closed", "INIT1", "initiative phone-onboard closed", `{"slug":"phone-onboard"}`},
		}},
}

// manyOperatorState is what the seed step read before the migration.
type manyOperatorState struct {
	before map[string][][]string
}

// readSeedTable reads the table's columns, each value tagged with its Go type.
func (h *harness) readSeedTable(ctx context.Context, s *store.Store, t seedRows) ([][]string, error) {
	return h.readColumns(ctx, s, t.name, t.key, t.allColumns())
}

// seedPhase4 builds the schema at version 11 in the scenario's database,
// writes phase4Seed and remembers what it reads back.
func (h *harness) seedPhase4(ctx context.Context, st *manyOperatorState) error {
	if err := h.emptyDatabase(); err != nil {
		return err
	}
	s, err := store.Open(h.dbURL)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.MigrateTo(ctx, phase4Head); err != nil {
		return err
	}
	v, err := s.SchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("read the schema version: %w", err)
	}
	if v != phase4Head {
		return fmt.Errorf("schema version before the seed = %d, want %d", v, phase4Head)
	}
	d := s.Dialect()
	st.before = map[string][][]string{}
	for _, t := range phase4Seed {
		for _, row := range t.rows {
			if _, err := s.Writer().ExecContext(ctx, d.Rebind(d.Insert(t.name, t.cols)), row...); err != nil {
				return fmt.Errorf("seed %s: %w", t.name, err)
			}
		}
		got, err := h.readSeedTable(ctx, s, t)
		if err != nil {
			return err
		}
		if len(got) != len(t.rows) {
			return fmt.Errorf("%s holds %d rows after the seed, want %d", t.name, len(got), len(t.rows))
		}
		st.before[t.name] = got
	}
	return nil
}

// seededRowsUnchanged requires every seeded table to read back exactly as it
// did before the migration, row for row and column for column.
func (h *harness) seededRowsUnchanged(ctx context.Context, st *manyOperatorState) error {
	if st.before == nil {
		return errors.New("no Phase 4 database was seeded")
	}
	s, err := store.Open(h.dbURL)
	if err != nil {
		return err
	}
	defer s.Close()
	var problems []string
	for _, t := range phase4Seed {
		after, err := h.readSeedTable(ctx, s, t)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(after, st.before[t.name]) {
			problems = append(problems, fmt.Sprintf("%s reads %v after the migration, was %v before", t.name, after, st.before[t.name]))
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// insertOperator inserts one operator actors row with the given id and handle.
func (h *harness) insertOperator(ctx context.Context, s *store.Store, id, handle string) error {
	d := s.Dialect()
	_, err := s.Writer().ExecContext(ctx, d.Rebind(d.Insert("actors", []string{"id", "handle", "kind", "first_seen_at", "last_seen_at"})),
		id, handle, "operator", 1, 1)
	return err
}

// secondOperatorRefused requires the unique index to refuse a second operator.
func (h *harness) secondOperatorRefused(ctx context.Context) error {
	s, err := store.Open(h.dbURL)
	if err != nil {
		return err
	}
	defer s.Close()
	err = h.insertOperator(ctx, s, "ACTOR4", "second-operator")
	if err == nil {
		return errors.New("a second operator row was accepted before the migration")
	}
	if !s.Dialect().IsUniqueViolation(err) {
		return fmt.Errorf("a second operator row failed, but not on a unique violation: %w", err)
	}
	return nil
}

func (h *harness) secondOperatorAccepted(ctx context.Context) error {
	s, err := store.Open(h.dbURL)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := h.insertOperator(ctx, s, "ACTOR4", "second-operator"); err != nil {
		return fmt.Errorf("a second operator row was refused after the migration: %w", err)
	}
	return nil
}

// actorsHold requires the actors table to hold n rows of the kind.
func (h *harness) actorsHold(ctx context.Context, n int, kind string) error {
	got, err := h.scalar(ctx, `SELECT count(*) FROM actors WHERE kind = ?`, kind)
	if err == nil && got != fmt.Sprint(n) {
		err = fmt.Errorf("the actors table holds %s %s rows, want %d", got, kind, n)
	}
	return err
}

// operatorHandles requires the operator rows' handles to be exactly want.
func (h *harness) operatorHandles(ctx context.Context, want string) error {
	db, err := h.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT handle FROM actors WHERE kind = 'operator' ORDER BY handle`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var handle string
		if err := rows.Scan(&handle); err != nil {
			return err
		}
		got = append(got, handle)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if strings.Join(got, ",") != want {
		return fmt.Errorf("operator handles = %q, want %q", strings.Join(got, ","), want)
	}
	return nil
}

func (h *harness) initiativeCreatedBy(ctx context.Context, slug, handle string) error {
	got, err := h.scalar(ctx, `SELECT a.handle FROM initiatives i JOIN actors a ON a.id = i.created_by WHERE i.slug = ?`, slug)
	if err == nil && got != handle {
		err = fmt.Errorf("initiative %s was created by %q, want %q", slug, got, handle)
	}
	return err
}

func (h *harness) serveWithOIDCOperators(ctx context.Context, allowed, value string) error {
	extra, err := h.oidcEnv(allowed)
	if err != nil {
		return err
	}
	if err := h.freshDatabase(); err != nil {
		return err
	}
	for i, kv := range extra {
		extra[i] = strings.ReplaceAll(kv, "{addr}", "127.0.0.1:1")
	}
	return h.runWith(ctx, "vh serve", append(extra, "BOARD_OIDC_OPERATORS="+value, "BOARD_LISTEN=127.0.0.1:0")...)
}

func (h *harness) serverWithOperatorsAndHandle(ctx context.Context, allowed, operators, handle string) (context.Context, error) {
	extra, err := h.oidcEnv(allowed)
	if err != nil {
		return ctx, err
	}
	return h.startServerWith(ctx, append(extra, "BOARD_OIDC_OPERATORS="+operators, "BOARD_OPERATOR_HANDLE="+handle)...)
}

func registerManyOperatorSteps(sc *godog.ScenarioContext, h *harness) {
	st := &manyOperatorState{}
	sc.Step(`^a Phase 4 database at schema version 11 holding one operator, two agents, one API key and events$`,
		func(ctx context.Context) error { return h.seedPhase4(ctx, st) })
	sc.Step(`^the seeded actors, API key and events read back with identical column values$`,
		func(ctx context.Context) error { return h.seededRowsUnchanged(ctx, st) })
	sc.Step(`^a second operator actor cannot be inserted$`, h.secondOperatorRefused)
	sc.Step(`^a second operator actor can be inserted$`, h.secondOperatorAccepted)
	sc.Step(`^the actors table holds (\d+) operators and (\d+) agents$`, func(ctx context.Context, operators, agents int) error {
		return errors.Join(h.actorsHold(ctx, operators, "operator"), h.actorsHold(ctx, agents, "agent"))
	})
	sc.Step(`^the actors table holds the operator handles "([^"]*)"$`, h.operatorHandles)
	sc.Step(`^the initiative "([^"]*)" was created by "([^"]*)"$`, h.initiativeCreatedBy)
	sc.Step(`^I run "vh serve" with OIDC login allowing "([^"]*)" and BOARD_OIDC_OPERATORS set to "([^"]*)"$`, h.serveWithOIDCOperators)
	sc.Step(`^the board server is running with OIDC login allowing "([^"]*)" and BOARD_OIDC_OPERATORS set to "([^"]*)" and BOARD_OPERATOR_HANDLE set to "([^"]*)"$`, h.serverWithOperatorsAndHandle)
}
