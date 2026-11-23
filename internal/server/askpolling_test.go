package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/store"
)

// polling is a stream of the events topic that tells when an ask_polling event of ask arrives.
type polling struct{ s *sse }

// next reads the next event message within d and returns it, "" when none came.
func (p polling) next(t *testing.T, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Until(deadline) > 0 {
		select {
		case b, ok := <-p.s.blocks:
			if !ok {
				t.Fatal("the stream ended")
			}
			if strings.Contains(b, `"type":"ask_polling"`) {
				return b
			}
		case <-time.After(time.Until(deadline)):
		}
	}
	return ""
}

// migrated opens and migrates an empty database of the leg.
func migrated(t *testing.T, dbs databases) *store.Store {
	t.Helper()
	st := openStore(t, dbs.New(t))
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

// replicas serves one database through two servers, the way two replicas share
// it, and returns them with the clock they and the ask store use.
func replicas(t *testing.T, dbs databases) (writer, reader *rig, clock *testClock, as *asks.Store) {
	t.Helper()
	url := dbs.New(t)
	a := openStore(t, url)
	if err := a.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	token := newToken(t, a)
	clock = newTestClock()
	writer, _ = askRigQuick(t, a, token, clock, time.Hour)
	reader, as = askRig(t, openStore(t, url), token, clock, time.Hour)
	return writer, reader, clock, as
}

// A wait that makes the asker of a poll ask waiting reaches the change feed of
// every replica on the database: a stream held open through another server gets
// the ask_polling event within the feed's tail interval, and the ask it is about
// reads asker_waiting from that stream's own server.
func TestAWaitMakingTheAskerWaitingReachesTheStreamOfAnotherReplica(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		writer, reader, _, _ := replicas(t, dbs)
		ref := writer.raiseAsk()
		stream := polling{reader.openStream("/api/v1/stream?topics=events")}
		w := writer.startWait(ref, "1")
		msg := stream.next(t, 5*time.Second)
		if msg == "" {
			t.Fatal("no ask_polling event reached the stream of the other replica within 5 s")
		}
		if !strings.Contains(msg, `"summary":"ask `+ref+` asker waiting"`) {
			t.Errorf("event = %q, want the summary to say the asker is waiting on %s", msg, ref)
		}
		resp := reader.do(http.MethodGet, "/api/v1/asks/"+ref, "", reader.token)
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `"asker_waiting":true`) {
			t.Error("the ask does not read asker_waiting true through the other replica after the event")
		}
		w.ends(t)
	})
}

// Only a change of the displayed state is an event: the renewals of a held
// wait and a second wait started while the first still counts write none; a
// wait started after the 60 seconds ran out of the first writes one again.
// A second of ?wait= lasts a quarter second here, so each wait is held for
// five renewals, and the window in which nothing may arrive is six renew
// periods (three polls of the feed).
func TestOnlyAChangeOfTheAskerWaitingStateReachesTheFeed(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		st := migrated(t, dbs)
		token := newToken(t, st)
		clock := newTestClock()
		r, _ := askRigQuick(t, st, token, clock, 50*time.Millisecond) // the held wait renews every 50 ms
		ref := r.raiseAsk()
		stream := polling{r.openStream("/api/v1/stream?topics=events")}
		r.startWait(ref, "1").ends(t)
		if stream.next(t, time.Second) == "" {
			t.Fatal("the first wait appended no ask_polling event")
		}
		clock.Advance(30 * time.Second)
		r.startWait(ref, "1").ends(t)
		if msg := stream.next(t, 300*time.Millisecond); msg != "" {
			t.Errorf("a wait on an asker already waiting (and its renewals) appended %q", msg)
		}
		clock.Advance(61 * time.Second)
		r.startWait(ref, "1").ends(t)
		if stream.next(t, 5*time.Second) == "" {
			t.Error("a wait started after the asker stopped waiting appended no ask_polling event")
		}
	})
}

// An ask that is not a poll ask, or is no longer open, never changes its
// displayed state by a wait, so no event is appended for it.
func TestAWaitOnAStopAskOrAnEndedAskAppendsNoPollingEvent(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		st := migrated(t, dbs)
		token := newToken(t, st)
		r, _ := askRigQuick(t, st, token, newTestClock(), time.Hour)
		stream := polling{r.openStream("/api/v1/stream?topics=events")}
		resp := r.do(http.MethodPost, "/api/v1/asks", `{"question":"Stop?"}`, token)
		_ = resp.Body.Close()
		r.startWait("A1", "1").ends(t)
		ended := r.raiseAsk()
		r.answer(ended)
		r.startWait(ended, "1").ends(t)
		if msg := stream.next(t, 300*time.Millisecond); msg != "" {
			t.Errorf("a wait on a stop ask or an answered ask appended %q", msg)
		}
	})
}
