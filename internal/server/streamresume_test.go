package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/events"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/store"
)

// resumeRig is a rig whose database is also open to the test, so that events can
// be written beside the server and read back.
func resumeRig(t *testing.T, dbs databases) (*rig, *store.Store) {
	t.Helper()
	st := openStore(t, dbs.New(t))
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A poll of an hour: only the wake of a write through the server makes the feed read.
	return startRig(t, st, newToken(t, st), streamSettings{poll: time.Hour, heartbeat: time.Minute}), st
}

// awaitFeed waits until the change feed has delivered the event numbered n, so a
// stream opened next starts after it.
func (r *rig) awaitFeed(n int64) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		sub := r.feed.Subscribe()
		after := sub.After
		sub.Close()
		if after >= n {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("the feed is at %d, want %d", after, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// idOfMessage is the `id:` line of one event message.
func idOfMessage(t *testing.T, msg string) string {
	t.Helper()
	for _, line := range strings.Split(msg, "\n") {
		if id, ok := strings.CutPrefix(line, "id: "); ok {
			return id
		}
	}
	t.Fatalf("message %q has no id line", msg)
	return ""
}

// quiet fails when anything but a heartbeat arrives within d.
func (s *sse) quiet(d time.Duration) {
	s.t.Helper()
	deadline := time.Now().Add(d)
	for time.Until(deadline) > 0 {
		select {
		case b, ok := <-s.blocks:
			if !ok {
				s.t.Fatal("the stream ended")
			}
			if !strings.HasPrefix(b, ":") {
				s.t.Fatalf("unexpected message %q", b)
			}
		case <-time.After(time.Until(deadline)):
		}
	}
}

// createBeside writes n initiatives straight to the database, so that nothing
// wakes the server's feed.
func createBeside(t *testing.T, st *store.Store, prefix string, n int) {
	t.Helper()
	for i := range n {
		slug := fmt.Sprintf("%s%04d", prefix, i)
		if _, err := (initiatives.Store{Store: st}).Create(context.Background(), audit.Actor{Handle: "beside@host"}, "key:caller",
			initiatives.New{Slug: slug, Title: "Title of " + slug}); err != nil {
			t.Fatal(err)
		}
	}
}

// appendBeside writes n events straight to the database in one transaction,
// the kind a create writes, so that nothing wakes the server's feed. It stands
// in for n writes through createBeside where the test is about how many events
// there are, not how each came to be.
func appendBeside(t *testing.T, st *store.Store, prefix string, n int) {
	t.Helper()
	ctx := context.Background()
	tx, err := st.Writer().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	actorID, err := audit.UpsertActor(ctx, tx, st.Dialect(), audit.Actor{Handle: "beside@host"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	batch := make([]audit.Written, n)
	for i := range batch {
		slug := fmt.Sprintf("%s%04d", prefix, i)
		batch[i] = audit.Written{ActorID: actorID, Event: audit.Event{
			Principal: "key:caller", Type: initiatives.EventCreated, Summary: "initiative " + slug + " created",
			Data: map[string]any{"slug": slug, "title": "Title of " + slug, "tags": []string{}},
		}}
	}
	if _, err := audit.AppendBatch(ctx, tx, st.Dialect(), time.Now(), batch); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// resetReason checks that block is one `event: reset` without an id and returns its reason.
func resetReason(t *testing.T, block string) string {
	t.Helper()
	lines := strings.Split(block, "\n")
	if len(lines) != 2 || lines[0] != "event: reset" || !strings.HasPrefix(lines[1], "data: ") {
		t.Fatalf("block = %q, want an event: reset with data and no id", block)
	}
	var d struct{ Reason string }
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &d); err != nil {
		t.Fatalf("reset data: %v", err)
	}
	return d.Reason
}

// SPEC 7.1: a reconnect with the id of the last event it saw gets exactly the
// events written while it was away, in order, and then the live ones.
func TestResumeReplaysExactlyTheEventsWrittenWhileDisconnected(t *testing.T) {
	t.Parallel()
	for name, open := range map[string]func(r *rig, id string) *sse{
		"header": func(r *rig, id string) *sse {
			return r.openStreamWith("/api/v1/stream?topics=events", map[string]string{"Last-Event-ID": id})
		},
		"query": func(r *rig, id string) *sse { return r.openStream("/api/v1/stream?topics=events&last_event_id=" + id) },
		"header wins over the query": func(r *rig, id string) *sse {
			return r.openStreamWith("/api/v1/stream?last_event_id=nonsense", map[string]string{"Last-Event-ID": id})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
				r, _ := resumeRig(t, dbs)
				first := r.openStream("/api/v1/stream?topics=events")
				r.createInitiative("seen")
				id := idOfMessage(t, first.nextMessage())
				first.close()
				for _, slug := range []string{"missed1", "missed2", "missed3"} {
					r.createInitiative(slug)
				}
				second := open(r, id)
				for _, want := range []string{"missed1", "missed2", "missed3"} {
					if got := initiativeOf(second); got != want {
						t.Fatalf("replay gave %v, want %s", got, want)
					}
				}
				r.createInitiative("live")
				if got := initiativeOf(second); got != "live" {
					t.Fatalf("after the replay got %v, want live", got)
				}
				second.quiet(300 * time.Millisecond)
			})
		})
	}
}

// A client resuming from the newest event gets no replay and no reset.
func TestResumeFromTheNewestEventReplaysNothing(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		r, _ := resumeRig(t, dbs)
		first := r.openStream("/api/v1/stream?topics=events")
		r.createInitiative("seen")
		id := idOfMessage(t, first.nextMessage())
		second := r.openStreamWith("/api/v1/stream?topics=events", map[string]string{"Last-Event-ID": id})
		second.quiet(200 * time.Millisecond)
		r.createInitiative("live")
		if got := initiativeOf(second); got != "live" {
			t.Fatalf("got %v, want live", got)
		}
		// E<n> refs name the same events, in either case.
		third := r.openStream("/api/v1/stream?topics=events&last_event_id=e1")
		if got := initiativeOf(third); got != "live" {
			t.Fatalf("resume from e1 gave %v, want live", got)
		}
	})
}

// SPEC 7.1: an id that names no event gets one reset and the stream goes on live.
func TestResumeWithAnUnknownIDSendsOneResetAndContinuesLive(t *testing.T) {
	t.Parallel()
	for name, id := range map[string]string{
		"a ULID no event has":   "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"not an id":             "garbage",
		"an E ref past the end": "E99999",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
				r, _ := resumeRig(t, dbs)
				r.createInitiative("before")
				r.awaitFeed(1)
				s := r.openStreamWith("/api/v1/stream?topics=events", map[string]string{"Last-Event-ID": id})
				if got := resetReason(t, s.nextMessage()); got != "unknown_id" {
					t.Errorf("reset reason = %q, want unknown_id", got)
				}
				r.createInitiative("live")
				if got := initiativeOf(s); got != "live" {
					t.Fatalf("after the reset got %v, want live", got)
				}
				s.quiet(300 * time.Millisecond)
			})
		})
	}
}

// SPEC 7.1: 1000 missed events replay; 1001 are one reset instead.
func TestResumeReplaysAGapOf1000AndResetsAtAGapOf1001(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		r, st := resumeRig(t, dbs)
		page, err := (&events.Store{Store: st}).List(context.Background(), events.Query{})
		if err != nil || len(page.Items) != 0 {
			t.Fatalf("a new database has events: %v %v", err, page.Items)
		}
		first := r.openStream("/api/v1/stream?topics=events")
		r.createInitiative("seen")
		id := idOfMessage(t, first.nextMessage())
		first.close()

		appendBeside(t, st, "a", 999) // the filler: one transaction; "seen", "last" and the rest go over HTTP
		r.createInitiative("last")    // the 1000th after "seen": its write wakes the feed
		r.awaitFeed(1001)
		replay := r.openStreamWith("/api/v1/stream?topics=events", map[string]string{"Last-Event-ID": id})
		for i := range 1000 {
			if msg := replay.nextMessage(); !strings.HasPrefix(msg, "event: board") {
				t.Fatalf("message %d of the replay = %q, want an event", i, msg)
			}
		}
		replay.quiet(200 * time.Millisecond)
		replay.close()

		r.createInitiative("one-too-many") // the 1001st
		r.awaitFeed(1002)
		over := r.openStreamWith("/api/v1/stream?topics=events", map[string]string{"Last-Event-ID": id})
		if got := resetReason(t, over.nextMessage()); got != "too_many" {
			t.Errorf("reset reason = %q, want too_many", got)
		}
		over.quiet(300 * time.Millisecond)
		r.createInitiative("live")
		if got := initiativeOf(over); got != "live" {
			t.Fatalf("after the reset got %v, want live", got)
		}
	})
}

// The attention topic alone is not resumed: Last-Event-ID is about events.
func TestResumeIsIgnoredWithoutTheEventsTopic(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		r, _ := resumeRig(t, dbs)
		s := r.openStreamWith("/api/v1/stream?topics=attention", map[string]string{"Last-Event-ID": "garbage"})
		s.quiet(200 * time.Millisecond)
	})
}

// A client can have read an event from a server whose feed is further ahead
// than this one: the live part never repeats what the client has.
func TestResumeDoesNotRepeatEventsTheFeedHasNotDeliveredYet(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		r, st := resumeRig(t, dbs)
		r.createInitiative("one")
		// The write only wakes the feed; its read can run after the writes beside
		// it below and deliver E3 too, which the stream must then replay. Let it
		// finish first, so that nothing wakes it until "live".
		r.awaitFeed(1)
		createBeside(t, st, "b", 2) // E2, E3: committed, the feed has not read them
		page, err := (&events.Store{Store: st}).List(context.Background(), events.Query{})
		if err != nil || len(page.Items) != 3 {
			t.Fatalf("events = %v, %v", page.Items, err)
		}
		s := r.openStreamWith("/api/v1/stream?topics=events", map[string]string{"Last-Event-ID": page.Items[1].ID})
		s.quiet(100 * time.Millisecond)
		r.createInitiative("live") // wakes the feed: E3 is the first the stream may send
		for _, want := range []string{"b0001", "live"} {
			if got := initiativeOf(s); got != want {
				t.Fatalf("got %v, want %s", got, want)
			}
		}
	})
}

func TestOutcomeNamesWhatAResumingStreamSends(t *testing.T) {
	t.Parallel()
	e := events.Event{N: 5}
	for name, tc := range map[string]struct {
		res  events.Resume
		err  error
		want missed
	}{
		"unknown":  {events.Resume{}, nil, missed{reset: resetUnknownID}},
		"too many": {events.Resume{Known: true, TooMany: true}, nil, missed{reset: resetTooMany}},
		"missed":   {events.Resume{Known: true, From: 4, Items: []events.Event{e}}, nil, missed{items: []events.Event{e}, skipThrough: 4}},
	} {
		got, err := outcome(tc.res, tc.err)
		if err != nil || got.reset != tc.want.reset || got.skipThrough != tc.want.skipThrough || len(got.items) != len(tc.want.items) {
			t.Errorf("%s: outcome = %+v, %v; want %+v", name, got, err, tc.want)
		}
	}
	if _, err := outcome(events.Resume{}, context.Canceled); err == nil {
		t.Error("an error from the store was swallowed")
	}
	res, err := (&streamHub{}).resume(context.Background(), "E1", 1)
	if err != nil || res.Known {
		t.Errorf("a hub with no events store knew an id: %+v, %v", res, err)
	}
}
