package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/notes"
	"github.com/joyautomation/nautilus/oplog"
)

// Operator notes on a pair (#720): written through the replicated log,
// readable on both controllers, refused with the acknowledgements' reason
// when no majority can be counted, and gone from the live view when a
// copy's history is superseded.

type notesPair struct {
	*pair
	nls  [3]*NotesLog
	srvs [3]*Server
}

func newNotesPair(t *testing.T) *notesPair {
	t.Helper()
	p := &pair{}
	np := &notesPair{pair: p}
	servers := make([]*httptest.Server, 3)
	for i := range servers {
		servers[i] = httptest.NewUnstartedServer(nil)
		p.urls[i] = "http://" + servers[i].Listener.Addr().String()
	}
	p.lead(0, 1)
	for i := 0; i < 3; i++ {
		nl := NewNotesLog()
		np.nls[i] = nl
		n, err := oplog.New(oplog.Options{Self: p.urls[i], Peers: p.urls[:], Leadership: seat{p, p.urls[i]},
			Token: "t", Ephemeral: true, Apply: func(e oplog.Entry) error { _, err := nl.Apply(e); return err }})
		if err != nil {
			t.Fatal(err)
		}
		nl.Attach(n)
		p.nodes[i] = n
		p.gates[i] = &pairGate{h: n.Handler()}
		servers[i].Config.Handler = p.gates[i]
		servers[i].Start()
		if i < 2 {
			np.srvs[i] = New(newTestRuntime(t), Options{AuthToken: "tok", NotesLog: nl})
		}
	}
	t.Cleanup(func() {
		for i := range servers {
			servers[i].Close()
			p.nodes[i].Close()
		}
	})
	if err := p.nodes[0].SyncLeader(t.Context()); err != nil {
		t.Fatal(err)
	}
	return np
}

func tokPost(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, s, "POST", path, body, bearer("tok"), asJSON)
}

func TestNotesOnAPairAreOnBothControllers(t *testing.T) {
	np := newNotesPair(t)
	rec := tokPost(t, np.srvs[0], "/api/notes", `{"target":{"kind":"asset","id":"Pump 2"},"text":"bearing noise","kind":"handover"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var v noteView
	json.Unmarshal(rec.Body.Bytes(), &v)
	if v.By != "token" || v.Kind != "handover" || v.At == 0 {
		t.Fatalf("%+v", v)
	}
	// the leader's heartbeat carries the commit index: the standby applies it
	np.nodes[0].Heartbeat(t.Context())
	deadline := time.Now().Add(3 * time.Second)
	var got *http.Response
	_ = got
	for time.Now().Before(deadline) {
		if _, ok := np.nls[1].Store().Get(v.ID); ok {
			break
		}
		np.nodes[0].Heartbeat(t.Context())
		time.Sleep(10 * time.Millisecond)
	}
	list := call(t, np.srvs[1], "GET", "/api/notes", "", bearer("tok"))
	if !strings.Contains(list.Body.String(), "bearing noise") || !strings.Contains(list.Body.String(), v.ID) {
		t.Fatalf("the standby does not show the leader's note: %d %s", list.Code, list.Body)
	}
	// the witness (the third replica) holds the text too: it stores whole entries
	var held bool
	for _, e := range np.nodes[2].After(0) {
		if b, ok := e.NoteBody(); ok && b.Text == "bearing noise" {
			held = true
		}
	}
	if !held {
		t.Error("the witness does not hold the note's text")
	}
	// the principal in the entry is the proposer's, and the id is the note's, not a seq
	if r, ok := notes.FromEntry(np.nodes[1].After(0)[len(np.nodes[1].After(0))-1]); !ok || r.Note != v.ID || r.Principal != "token" {
		t.Errorf("the committed entry: %+v %v", r, ok)
	}
}

// A copy that cannot count a majority refuses a note as it refuses an
// acknowledgement, and keeps nothing: the page keeps the draft.
func TestNoteIsRefusedWithoutAMajorityAndNeverQueued(t *testing.T) {
	np := newNotesPair(t)
	np.gates[1].down.Store(true)
	np.gates[2].down.Store(true)
	// let the leader notice its peers are gone
	for i := 0; i < 3; i++ {
		np.nodes[0].Ping(t.Context())
	}
	rec := tokPost(t, np.srvs[0], "/api/notes", `{"target":{"kind":"log"},"text":"must not be lost silently"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d %s, want 503", rec.Code, rec.Body)
	}
	// refused before anything was proposed (not "outcome unknown"), with the log's own reason
	if rec.Header().Get(outcomeHeader) != "refused" || !strings.Contains(rec.Body.String(), "majority") && !strings.Contains(rec.Body.String(), "synced") {
		t.Errorf("not the acknowledgements' refusal: %s %q", rec.Header().Get(outcomeHeader), rec.Body)
	}
	if np.nls[0].Store().Count() != 0 {
		t.Error("a refused note was kept in memory")
	}
	// the peers return, and the note is still not there: it was never queued
	np.gates[1].down.Store(false)
	np.gates[2].down.Store(false)
	np.nodes[0].Heartbeat(t.Context())
	time.Sleep(50 * time.Millisecond)
	for i := 0; i < 2; i++ {
		if np.nls[i].Store().Count() != 0 {
			t.Errorf("replica %d holds a note nobody was told was kept", i)
		}
	}
}

// A copy superseded by another history (naut redundancy vouch) shows
// exactly the new history's notes: the old ones leave the live view with
// the rest of that history.
func TestSupersededHistoryTakesItsNotesFromTheLiveView(t *testing.T) {
	nl := NewNotesLog()
	nl.Apply(notesEntry("n1", "from the old history"))
	if nl.Store().Count() != 1 {
		t.Fatal("setup")
	}
	nl.EndSuperseded()
	if nl.Store().Count() != 0 {
		t.Error("a superseded history's note is still shown")
	}
	// the new history's entries then replay in
	nl.Apply(notesEntry("n2", "from the new history"))
	if _, ok := nl.Store().Get("n2"); !ok || nl.Store().Count() != 1 {
		t.Error("the new history's note is not shown")
	}
}

func notesEntry(id, text string) oplog.Entry {
	return notes.Entry(notes.Revision{Note: id, Rev: 1, At: 1, By: "alice", Target: notes.Target{Kind: notes.KindLog}, Text: text})
}

// An entry kind this build does not know is left for the next handler
// (a build from before notes does the same with ours), and a note entry
// is taken here.
func TestNotesLogHandsOtherKindsOn(t *testing.T) {
	nl := NewNotesLog()
	if done, err := nl.Apply(oplog.Entry{Kind: oplog.KindAck, IDs: []string{"A"}}); done || err != nil {
		t.Errorf("an ack: %v %v", done, err)
	}
	e := notesEntry("n1", "x")
	e.TS = 5
	if done, err := nl.Apply(e); !done || err != nil {
		t.Errorf("a note: %v %v", done, err)
	}
	// a malformed note entry (no body) is taken and ignored, not passed on
	if done, _ := nl.Apply(oplog.Entry{Kind: oplog.KindNote}); !done {
		t.Error("a bodiless note entry was handed on")
	}
}

// Two changes to one note proposed together both commit in the log, and the
// second applies nowhere (its revision number is taken). The loser is told
// so: not 200, and not journalled as done.
func TestAConcurrentRevisionThatLostIsNotAnswered200(t *testing.T) {
	np := newNotesPair(t)
	rec := tokPost(t, np.srvs[0], "/api/notes", `{"target":{"kind":"log"},"text":"original"}`)
	var v noteView
	json.Unmarshal(rec.Body.Bytes(), &v)
	propose := func(text string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "http://localhost/api/notes/x/revise", nil)
		r.Header.Set("Authorization", "Bearer tok")
		// both writers read revision 1 and propose revision 2
		np.srvs[0].writeNote(w, r, notes.Revision{Note: v.ID, Rev: 2, By: "token", Principal: "token",
			Target: notes.Target{Kind: notes.KindLog}, Text: text}, http.StatusOK)
		return w
	}
	first, second := propose("first writer"), propose("second writer")
	if first.Code != http.StatusOK {
		t.Fatalf("the first: %d %s", first.Code, first.Body)
	}
	if second.Code != http.StatusConflict {
		t.Fatalf("the loser got %d %s, want 409", second.Code, second.Body)
	}
	n, _ := np.nls[0].Store().Get(v.ID)
	if len(n.Revisions) != 2 || n.Latest().Text != "first writer" {
		t.Errorf("%+v", n.Revisions)
	}
	// replay agrees: the same entries applied in order give the same note
	rs, bad := notes.Replay(np.nodes[0].After(0))
	if got, _ := rs.Get(v.ID); bad != 1 || len(got.Revisions) != 2 || got.Latest().Text != "first writer" {
		t.Errorf("replay: bad %d, %+v", bad, got.Revisions)
	}
}
