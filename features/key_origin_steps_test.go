package features

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cucumber/godog"

	"github.com/n-orlov/village-hall/internal/store"
)

// keyOriginState holds the keys a scenario made, by name.
type keyOriginState struct{ keys map[string]string }

// none is how a step names a null owner or origin.
const none = "-"

// seedNoOperator builds the schema at version 11 and writes an agent, a key and
// an event made through it, with no operator row at all.
func (h *harness) seedNoOperator(ctx context.Context) error {
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
	for _, q := range []string{
		`INSERT INTO actors (id, handle, kind, first_seen_at, last_seen_at) VALUES ('ACTOR2', 'claude@laptop', 'agent', 1100, 2100)`,
		`INSERT INTO api_keys (id, name, key_hash, display_prefix, created_at, created_by) VALUES ('KEY1', 'laptop', 'hash1', 'brd_ab', 5000, 'admin-cli')`,
		`INSERT INTO events (id, at, actor_id, principal, type, summary) VALUES ('EV1', 9400, 'ACTOR2', 'key:laptop', 'initiative_closed', 'closed')`,
	} {
		if _, err := s.Writer().ExecContext(ctx, q); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	}
	return nil
}

func (h *harness) operatorsExist(ctx context.Context, handles string) error {
	for _, handle := range strings.Split(handles, ",") {
		_, err := h.exec(ctx, `INSERT INTO actors (id, handle, kind, first_seen_at, last_seen_at) VALUES (?, ?, 'operator', 1, 1)`, "OP-"+handle, handle)
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *harness) agentExists(ctx context.Context, handle string) error {
	_, err := h.exec(ctx, `INSERT INTO actors (id, handle, kind, first_seen_at, last_seen_at) VALUES (?, ?, 'agent', 1, 1)`, "AG-"+handle, handle)
	return err
}

// adminKey runs `vh admin keys create` and keeps the key it printed under name.
func (h *harness) adminKey(ctx context.Context, st *keyOriginState, name, owner string) error {
	line := "vh admin keys create " + name
	if owner != "" {
		line += " --owner " + owner
	}
	if err := h.run(ctx, line); err != nil {
		return err
	}
	key, err := h.printedKey()
	if err != nil {
		return err
	}
	st.keys[name] = key
	return nil
}

func (h *harness) keyNamed(st *keyOriginState, name string) (string, error) {
	key, ok := st.keys[name]
	if !ok {
		return "", fmt.Errorf("no key named %q was made in this scenario", name)
	}
	return key, nil
}

// keyCreatesInitiative has the named key, as actor, create the initiative slug.
func (h *harness) keyCreatesInitiative(ctx context.Context, st *keyOriginState, name, actor, slug string) error {
	key, err := h.keyNamed(st, name)
	if err != nil {
		return err
	}
	return h.sendKeyed(ctx, http.MethodPost, "/api/v1/initiatives", key, actor, map[string]string{"slug": slug, "title": slug})
}

// keyCreatesKey has the named key, as actor, create the API key child and keeps it.
func (h *harness) keyCreatesKey(ctx context.Context, st *keyOriginState, name, actor, child string) error {
	key, err := h.keyNamed(st, name)
	if err != nil {
		return err
	}
	if err := h.sendKeyed(ctx, http.MethodPost, "/api/v1/keys", key, actor, map[string]string{"name": child}); err != nil {
		return err
	}
	var doc struct {
		Key string `json:"key"`
	}
	if h.status == http.StatusCreated {
		if err := json.Unmarshal(h.api.body, &doc); err != nil {
			return fmt.Errorf("response %q: %w", h.api.body, err)
		}
		st.keys[child] = doc.Key
	}
	return nil
}

// apiRead reads path with the named key and returns the body of a 200 answer.
func (h *harness) apiRead(ctx context.Context, st *keyOriginState, name, path string) ([]byte, error) {
	key, err := h.keyNamed(st, name)
	if err != nil {
		return nil, err
	}
	if err := h.sendKeyed(ctx, http.MethodGet, path, key, "godog", nil); err != nil {
		return nil, err
	}
	if h.status != http.StatusOK {
		return nil, fmt.Errorf("GET %s answered %d: %s", path, h.status, h.api.body)
	}
	return h.api.body, nil
}

// sameOrNone requires got, the string a JSON field held (empty when it was absent),
// to be want, where none means absent.
func sameOrNone(what, got, want string) error {
	if want == none {
		want = ""
	}
	if got != want {
		return fmt.Errorf("%s = %q, want %q", what, got, want)
	}
	return nil
}

func (h *harness) apiEventOrigin(ctx context.Context, st *keyOriginState, name, slug, want string) error {
	body, err := h.apiRead(ctx, st, name, "/api/v1/events?type=initiative_created&initiative="+slug)
	if err != nil {
		return err
	}
	var doc struct {
		Items []struct {
			Origin string `json:"origin"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}
	if len(doc.Items) != 1 {
		return fmt.Errorf("initiative %s has %d initiative_created events, want 1: %s", slug, len(doc.Items), body)
	}
	return sameOrNone("origin of the event of "+slug, doc.Items[0].Origin, want)
}

func (h *harness) apiActorOrigin(ctx context.Context, st *keyOriginState, name, handle, want string) error {
	body, err := h.apiRead(ctx, st, name, "/api/v1/actors/"+handle)
	if err != nil {
		return err
	}
	var doc struct {
		Origin string `json:"origin"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}
	return sameOrNone("origin of the actor "+handle, doc.Origin, want)
}

func (h *harness) apiKeyOwner(ctx context.Context, st *keyOriginState, name, listed, want string) error {
	body, err := h.apiRead(ctx, st, name, "/api/v1/keys")
	if err != nil {
		return err
	}
	var doc struct {
		Items []struct {
			Name  string `json:"name"`
			Owner string `json:"owner"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}
	for _, it := range doc.Items {
		if it.Name == listed {
			return sameOrNone("owner of the key "+listed, it.Owner, want)
		}
	}
	return fmt.Errorf("the key list %s has no key %q", body, listed)
}

func (h *harness) rowOwner(ctx context.Context, name, want string) error {
	got, err := h.scalar(ctx, `SELECT COALESCE(o.handle, '-') FROM api_keys k LEFT JOIN actors o ON o.id = k.owner WHERE k.name = ?`, name)
	if err != nil {
		return fmt.Errorf("read the owner of key %q: %w", name, err)
	}
	return sameOrNone("owner of the key "+name, strings.TrimPrefix(got, none), want)
}

func (h *harness) eventOriginByID(ctx context.Context, id, want string) error {
	got, err := h.scalar(ctx, `SELECT COALESCE(o.handle, '-') FROM events e LEFT JOIN actors o ON o.id = e.origin WHERE e.id = ?`, id)
	if err != nil {
		return fmt.Errorf("read the origin of event %q: %w", id, err)
	}
	return sameOrNone("origin of event "+id, strings.TrimPrefix(got, none), want)
}

func (h *harness) actorOrigin(ctx context.Context, handle, want string) error {
	got, err := h.scalar(ctx, `SELECT COALESCE(o.handle, '-') FROM actors a LEFT JOIN actors o ON o.id = a.origin WHERE a.handle = ?`, handle)
	if err != nil {
		return fmt.Errorf("read the origin of actor %q: %w", handle, err)
	}
	return sameOrNone("origin of the actor "+handle, strings.TrimPrefix(got, none), want)
}

// typedEventOrigin reads the origin of the event of typ about the initiative or key.
func (h *harness) typedEventOrigin(ctx context.Context, typ, kind, name, want string) error {
	q := `SELECT COALESCE(o.handle, '-') FROM events e LEFT JOIN actors o ON o.id = e.origin JOIN initiatives i ON i.id = e.ref_id WHERE e.type = ? AND i.slug = ?`
	if kind == "key" {
		q = `SELECT COALESCE(o.handle, '-') FROM events e LEFT JOIN actors o ON o.id = e.origin JOIN api_keys k ON k.id = e.ref_id WHERE e.type = ? AND k.name = ?`
	}
	got, err := h.scalar(ctx, q, typ, name)
	if err != nil {
		return fmt.Errorf("read the %s event of %s %q: %w", typ, kind, name, err)
	}
	return sameOrNone(typ+" event origin of "+name, strings.TrimPrefix(got, none), want)
}

func (h *harness) keyRowsHold(ctx context.Context, want int) error {
	got, err := h.scalar(ctx, `SELECT count(*) FROM api_keys`)
	if err == nil && got != fmt.Sprint(want) {
		err = fmt.Errorf("the api_keys table holds %s rows, want %d", got, want)
	}
	return err
}

func registerKeyOriginSteps(sc *godog.ScenarioContext, h *harness) {
	st := &keyOriginState{keys: map[string]string{}}
	sc.Step(`^a Phase 4 database at schema version 11 holding one agent, one API key and a keyed event but no operator$`, h.seedNoOperator)
	sc.Step(`^the operators "([^"]*)" exist$`, h.operatorsExist)
	sc.Step(`^the agent "([^"]*)" exists$`, h.agentExists)
	sc.Step(`^I create the admin key "([^"]*)" owned by "([^"]*)"$`, func(ctx context.Context, name, owner string) error {
		return h.adminKey(ctx, st, name, owner)
	})
	sc.Step(`^I create the admin key "([^"]*)" with no owner$`, func(ctx context.Context, name string) error {
		return h.adminKey(ctx, st, name, "")
	})
	sc.Step(`^the key "([^"]*)" as actor "([^"]*)" creates the initiative "([^"]*)"$`, func(ctx context.Context, key, actor, slug string) error {
		return h.keyCreatesInitiative(ctx, st, key, actor, slug)
	})
	sc.Step(`^the key "([^"]*)" as actor "([^"]*)" creates the API key "([^"]*)"$`, func(ctx context.Context, key, actor, child string) error {
		return h.keyCreatesKey(ctx, st, key, actor, child)
	})
	sc.Step(`^the key "([^"]*)" reads the event of the initiative "([^"]*)" with origin "([^"]*)"$`, func(ctx context.Context, key, slug, origin string) error {
		return h.apiEventOrigin(ctx, st, key, slug, origin)
	})
	sc.Step(`^the key "([^"]*)" reads the actor "([^"]*)" with origin "([^"]*)"$`, func(ctx context.Context, key, handle, origin string) error {
		return h.apiActorOrigin(ctx, st, key, handle, origin)
	})
	sc.Step(`^the key "([^"]*)" reads the event of the initiative "([^"]*)" with no origin$`, func(ctx context.Context, key, slug string) error {
		return h.apiEventOrigin(ctx, st, key, slug, none)
	})
	sc.Step(`^the key "([^"]*)" reads the actor "([^"]*)" with no origin$`, func(ctx context.Context, key, handle string) error {
		return h.apiActorOrigin(ctx, st, key, handle, none)
	})
	sc.Step(`^the key "([^"]*)" reads the key "([^"]*)" owned by "([^"]*)"$`, func(ctx context.Context, key, listed, owner string) error {
		return h.apiKeyOwner(ctx, st, key, listed, owner)
	})
	sc.Step(`^the api_keys row "([^"]*)" has owner "([^"]*)"$`, h.rowOwner)
	sc.Step(`^the api_keys row "([^"]*)" has no owner$`, func(ctx context.Context, name string) error { return h.rowOwner(ctx, name, none) })
	sc.Step(`^the event "(EV\d+)" has origin "([^"]*)"$`, h.eventOriginByID)
	sc.Step(`^the event "(EV\d+)" has no origin$`, func(ctx context.Context, id string) error { return h.eventOriginByID(ctx, id, none) })
	sc.Step(`^the actor "([^"]*)" has origin "([^"]*)"$`, h.actorOrigin)
	sc.Step(`^the actor "([^"]*)" has no origin$`, func(ctx context.Context, handle string) error { return h.actorOrigin(ctx, handle, none) })
	sc.Step(`^the event "([^"]*)" of the (initiative|key) "([^"]*)" has origin "([^"]*)"$`, h.typedEventOrigin)
	sc.Step(`^the api_keys table holds (\d+) rows$`, h.keyRowsHold)
}
