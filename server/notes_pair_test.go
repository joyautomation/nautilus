package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		n, err := oplog.New(oplog.Options{Self: p.urls[i], Peers: p.urls[:], Leadership: seat{p: p, self: p.urls[i]},
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

// A copy's replaced history is a record: its notes are shown read-only,
// labelled with who accepted the loss and when the log was moved aside,
// are never in the live index, and a note the live history also holds is
// not repeated.
func TestSupersededHistoryNotesAreShownReadOnlyAndNeverApplied(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "oplog.jsonl.superseded-1700000000")
	var lines []string
	add := func(v any) { b, _ := json.Marshal(v); lines = append(lines, string(b)) }
	ent := func(seq uint64, r notes.Revision) oplog.Entry {
		e := notes.Entry(r)
		e.Seq, e.Term, e.TS = seq, 1, r.At
		return e
	}
	rv := func(id, text string) notes.Revision {
		return notes.Revision{Note: id, Rev: 1, At: 1700000000000, By: "alice", Principal: "user:alice", Target: notes.Target{Kind: notes.KindLog}, Text: text}
	}
	add(map[string]any{"e": ent(1, rv("only-old", "handover from the old history"))})
	add(map[string]any{"e": ent(2, rv("both", "in both histories"))})
	add(map[string]any{"e": ent(3, rv("uncommitted", "never committed"))})
	add(map[string]any{"c": 2})
	os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o600)

	nl := NewNotesLog()
	nl.supersededAt = func() (string, string) { return file, "akhan" }
	nl.Apply(ent(1, rv("both", "in both histories")))
	nl.Apply(ent(2, rv("live", "from the new history")))
	srv := New(newTestRuntime(t), Options{AuthToken: "tok", NotesLog: nl})

	rec := call(t, srv, "GET", "/api/notes?superseded=1", "", bearer("tok"))
	var got supersededAnswer
	json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || len(got.Notes) != 1 || got.Notes[0].ID != "only-old" || got.History == nil || got.History.By != "akhan" || got.History.MovedAt != 1700000000000 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// never in the live view, never changeable
	live := call(t, srv, "GET", "/api/notes", "", bearer("tok")).Body.String()
	if strings.Contains(live, "only-old") || strings.Contains(live, "uncommitted") {
		t.Errorf("the old history is in the live list: %s", live)
	}
	if _, ok := nl.Store().Get("only-old"); ok {
		t.Error("the old history was applied to the live index")
	}
	if rec := call(t, srv, "GET", "/api/notes/only-old", "", bearer("tok")); rec.Code != http.StatusNotFound {
		t.Errorf("a superseded note by id: %d", rec.Code)
	}
	b, _ := json.Marshal(map[string]string{"text": "edit"})
	if rec := call(t, srv, "POST", "/api/notes/only-old/revise", string(b), bearer("tok"), asJSON); rec.Code == http.StatusOK {
		t.Error("a superseded note was revised")
	}
	// a file with no unix time in its name has no "moved aside" time to show: it is left out, not year 1
	odd := filepath.Join(dir, "oplog.jsonl.old")
	os.WriteFile(odd, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	nl3 := NewNotesLog()
	nl3.supersededAt = func() (string, string) { return odd, "" }
	srv3 := New(newTestRuntime(t), Options{AuthToken: "tok", NotesLog: nl3})
	if body := call(t, srv3, "GET", "/api/notes?superseded=1", "", bearer("tok")).Body.String(); strings.Contains(body, "movedAt") {
		t.Errorf("an unknown move time is shown: %s", body)
	}
	// the limit counts the notes shown: a note the live history holds does not use one up
	lim := call(t, srv, "GET", "/api/notes?superseded=1&limit=1", "", bearer("tok"))
	var limited supersededAnswer
	json.Unmarshal(lim.Body.Bytes(), &limited)
	if len(limited.Notes) != 1 || limited.Notes[0].ID != "only-old" {
		t.Errorf("limit 1: %s", lim.Body)
	}
	// a controller with no replaced history says so: an empty list, no history
	nl2 := NewNotesLog()
	srv2 := New(newTestRuntime(t), Options{AuthToken: "tok", NotesLog: nl2})
	var none supersededAnswer
	rec2 := call(t, srv2, "GET", "/api/notes?superseded=1", "", bearer("tok"))
	json.Unmarshal(rec2.Body.Bytes(), &none)
	if rec2.Code != 200 || len(none.Notes) != 0 || none.History != nil {
		t.Errorf("%d %s", rec2.Code, rec2.Body)
	}
}

func TestNoteCountsByTarget(t *testing.T) {
	np := newNotesPair(t)
	for _, body := range []string{
		`{"target":{"kind":"tag","id":"SP"},"text":"a"}`, `{"target":{"kind":"tag","id":"SP"},"text":"b"}`,
		`{"target":{"kind":"tag","id":"Level"},"text":"c"}`, `{"target":{"kind":"log"},"text":"d"}`,
	} {
		if rec := tokPost(t, np.srvs[0], "/api/notes", body); rec.Code != http.StatusCreated {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	rec := call(t, np.srvs[0], "GET", "/api/notes/counts?target=tag", "", bearer("tok"))
	var got struct{ Counts map[string]int }
	json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got.Counts["SP"] != 2 || got.Counts["Level"] != 1 || len(got.Counts) != 2 {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(t, np.srvs[0], "GET", "/api/notes/counts?target=tank", "", bearer("tok")); rec.Code != http.StatusBadRequest {
		t.Errorf("bad target: %d", rec.Code)
	}
}
