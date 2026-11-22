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

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/notes"
)

// Operator notes (#720) standing alone: who may write, the author from the
// session, an hour to change your own note and an addendum after, a
// retraction that keeps the text, every write journalled with its text.

type noteRig struct {
	srv   *Server
	store *notes.Store
	path  string
	cs    map[string]*http.Cookie
	now   time.Time
	toks  map[string]string
}

func newNoteRig(t *testing.T) *noteRig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notes.jsonl")
	st, err := notes.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, toks, _ := patServerWith(t, Options{Notes: st})
	rig := &noteRig{srv: srv, store: st, path: path, cs: map[string]*http.Cookie{}, now: time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC), toks: toks}
	srv.noteClock = func() time.Time { return rig.now }
	return rig
}

func (n *noteRig) as(t *testing.T, who string) func(*http.Request) {
	c := n.cs[who]
	if c == nil {
		c = signIn(t, n.srv, who) // once each: the sign-in throttle
		n.cs[who] = c
	}
	return func(r *http.Request) { withCookie(c.Value)(r); ownOrigin(r); asJSON(r) }
}

func (n *noteRig) post(t *testing.T, who, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, n.srv, "POST", path, body, n.as(t, who))
}

func (n *noteRig) add(t *testing.T, who, body string) noteView {
	t.Helper()
	rec := n.post(t, who, "/api/notes", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add as %s %s: %d %s", who, body, rec.Code, rec.Body)
	}
	var v noteView
	json.Unmarshal(rec.Body.Bytes(), &v)
	return v
}

const logNote = `{"target":{"kind":"log"},"text":"pump 2 vibrating, called maintenance"}`

func TestNoteAddTakesTheAuthorFromTheSession(t *testing.T) {
	rig := newNoteRig(t)
	v := rig.add(t, "jsmith", logNote)
	if v.By != "jsmith" || v.Principal != "user:jsmith" || v.Text == "" || v.Revisions != 1 || v.Target.Kind != "log" {
		t.Fatalf("%+v", v)
	}
	if v.At != rig.now.UnixMilli() || v.EditableTo != v.At+3600000 {
		t.Errorf("time %d, window %d", v.At, v.EditableTo)
	}
	// a body that names an author is refused: the session says who
	if rec := rig.post(t, "jsmith", "/api/notes", `{"target":{"kind":"log"},"text":"x","by":"akhan"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a body naming an author: %d", rec.Code)
	}
	// the journal has the note, its target and its text
	var e *audit.Entry
	for _, x := range rig.srv.audit.ring.Recent() {
		if x.Action == "note.add" && x.Result == audit.ResultOK {
			x := x
			e = &x
		}
	}
	if e == nil || e.User != "user:jsmith" || e.Target != "log" || !strings.Contains(journalText(rig.srv), "pump 2 vibrating") {
		t.Errorf("journal %+v", e)
	}
	// it is on disk
	if b := readFile(t, rig.path); !strings.Contains(b, "pump 2 vibrating") {
		t.Errorf("not in the file: %s", b)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNoteRefusals(t *testing.T) {
	rig := newNoteRig(t)
	for name, c := range map[string]struct {
		body string
		want int
	}{
		"no text":           {`{"target":{"kind":"log"},"text":"  "}`, 400},
		"too long":          {`{"target":{"kind":"log"},"text":"` + strings.Repeat("x", notes.MaxText+1) + `"}`, 400},
		"control character": {`{"target":{"kind":"log"},"text":"a\u0000b"}`, 400},
		"no target":         {`{"text":"x"}`, 400},
		"bad target kind":   {`{"target":{"kind":"tank","id":"T1"},"text":"x"}`, 400},
		"log with an id":    {`{"target":{"kind":"log","id":"x"},"text":"x"}`, 400},
		"bad kind":          {`{"target":{"kind":"log"},"text":"x","kind":"poem"}`, 400},
		"unknown tag":       {`{"target":{"kind":"tag","id":"Nope"},"text":"x"}`, 422},
		"unknown alarm":     {`{"target":{"kind":"alarm","id":"Nope"},"text":"x"}`, 422},
		"addendum of none":  {`{"re":"0000","text":"x"}`, 422},
		"not json":          {`text=x`, 400},
	} {
		if rec := rig.post(t, "jsmith", "/api/notes", c.body); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body, c.want)
		}
	}
	if rig.store.Count() != 0 {
		t.Errorf("%d notes kept from refused requests", rig.store.Count())
	}
	// no credential
	if rec := call(t, rig.srv, "POST", "/api/notes", logNote, ownOrigin, asJSON); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	// a user the auth: block grants nothing
	if rec := call(t, rig.srv, "POST", "/api/notes", logNote, bearer(rig.toks["ghost"]), asJSON); rec.Code != http.StatusForbidden {
		t.Errorf("ghost: %d", rec.Code)
	}
}

// Areas limit a note about a tag or an alarm, as they limit a write; the
// shift log, an asset and an area are written wherever the permission is.
func TestNoteAreasLimitTagAndAlarmNotes(t *testing.T) {
	rig := newNoteRig(t)
	for body, want := range map[string]int{
		`{"target":{"kind":"tag","id":"SP"},"text":"setpoint raised"}`:       201, // jsmith's area
		`{"target":{"kind":"tag","id":"Out"},"text":"x"}`:                    403, // outside it
		`{"target":{"kind":"alarm","id":"HiTemp"},"text":"x"}`:               403, // its tag is outside it
		`{"target":{"kind":"asset","id":"Pump 2"},"text":"bearing noise"}`:   201,
		`{"target":{"kind":"area","id":"wetwell"},"text":"smell"}`:           201,
		`{"target":{"kind":"log"},"text":"shift started","kind":"handover"}`: 201,
	} {
		if rec := rig.post(t, "jsmith", "/api/notes", body); rec.Code != want {
			t.Errorf("%s: %d %s, want %d", body, rec.Code, rec.Body, want)
		}
	}
	// the engineer is over every area
	rig.add(t, "akhan", `{"target":{"kind":"alarm","id":"HiTemp","occurrence":"00ff00ff","activeMs":99},"text":"checked the sensor"}`)
}

// A token adds a note (an integration posting "batch 7 started") and
// changes none.
func TestNoteTokensAddOnly(t *testing.T) {
	rig := newNoteRig(t)
	rig.srv.authToken = "root-token"
	rec := call(t, rig.srv, "POST", "/api/notes", `{"target":{"kind":"log"},"text":"batch 7 started"}`, bearer("root-token"), asJSON)
	if rec.Code != http.StatusCreated {
		t.Fatalf("token add: %d %s", rec.Code, rec.Body)
	}
	var v noteView
	json.Unmarshal(rec.Body.Bytes(), &v)
	if v.By != "token" || v.Principal != "token" {
		t.Errorf("author %+v", v)
	}
	for _, p := range []string{"/revise", "/retract"} {
		rec := call(t, rig.srv, "POST", "/api/notes/"+v.ID+p, `{"text":"x","reason":"x"}`, bearer("root-token"), asJSON)
		if rec.Code != http.StatusForbidden {
			t.Errorf("token %s: %d %s", p, rec.Code, rec.Body)
		}
	}
}

func TestNoteReviseWithinTheHourOnlyByTheAuthor(t *testing.T) {
	rig := newNoteRig(t)
	v := rig.add(t, "jsmith", logNote)
	rev := func(who, text string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"text": text})
		return rig.post(t, who, "/api/notes/"+v.ID+"/revise", string(b))
	}
	if rec := rev("akhan", "someone else's words"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "addendum") {
		t.Errorf("another person: %d %s", rec.Code, rec.Body)
	}
	rig.now = rig.now.Add(59 * time.Minute)
	rec := rev("jsmith", "pump 2 vibrating, maintenance called, bearing")
	if rec.Code != http.StatusOK {
		t.Fatalf("author inside the hour: %d %s", rec.Code, rec.Body)
	}
	var got noteView
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Revisions != 2 || !got.Edited || got.Text != "pump 2 vibrating, maintenance called, bearing" || got.EditedBy != "jsmith" {
		t.Errorf("%+v", got)
	}
	// history keeps the first wording
	g := call(t, rig.srv, "GET", "/api/notes/"+v.ID, "", withCookie(rig.cs["jsmith"].Value))
	if !strings.Contains(g.Body.String(), "pump 2 vibrating, called maintenance") || !strings.Contains(g.Body.String(), `"history"`) {
		t.Errorf("the history lost the first wording: %s", g.Body)
	}
	// past the hour (counted from the note, not the last edit) it is an addendum
	rig.now = rig.now.Add(2 * time.Minute)
	if rec := rev("jsmith", "late edit"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "addendum") {
		t.Errorf("author after the hour: %d %s", rec.Code, rec.Body)
	}
	add := rig.add(t, "akhan", `{"re":"`+v.ID+`","text":"maintenance says the bearing is replaced"}`)
	if add.Target.Kind != "log" || add.Re != v.ID {
		t.Errorf("an addendum is about what its note is about: %+v", add)
	}
	if n, _ := rig.store.Get(v.ID); len(n.Revisions) != 2 {
		t.Errorf("an addendum changed the note: %d revisions", len(n.Revisions))
	}
}

func TestNoteRetractKeepsTheText(t *testing.T) {
	rig := newNoteRig(t)
	v := rig.add(t, "jsmith", logNote)
	ret := func(who, reason string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"reason": reason})
		return rig.post(t, who, "/api/notes/"+v.ID+"/retract", string(b))
	}
	if rec := ret("jsmith", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("no reason: %d", rec.Code)
	}
	// a supervisor-class holder (audit) retracts another's note, even past the hour
	rig.now = rig.now.Add(5 * time.Hour)
	if rec := ret("jsmith", "too late for the author"); rec.Code != http.StatusForbidden {
		t.Errorf("author past the hour: %d", rec.Code)
	}
	rec := ret("akhan", "wrong plant")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit holder: %d %s", rec.Code, rec.Body)
	}
	n, _ := rig.store.Get(v.ID)
	if !n.Retracted() || n.Latest().Text != "pump 2 vibrating, called maintenance" || !n.Latest().Override || n.Latest().Reason != "wrong plant" {
		t.Errorf("%+v", n.Latest())
	}
	list := call(t, rig.srv, "GET", "/api/notes", "", withCookie(rig.cs["jsmith"].Value)).Body.String()
	if strings.Contains(list, v.ID) {
		t.Error("a retracted note is listed by default")
	}
	list = call(t, rig.srv, "GET", "/api/notes?retracted=1", "", withCookie(rig.cs["jsmith"].Value)).Body.String()
	if !strings.Contains(list, v.ID) || !strings.Contains(list, "wrong plant") {
		t.Errorf("with retracted: %s", list)
	}
	// it cannot be changed again
	if rec := ret("akhan", "again"); rec.Code != http.StatusConflict {
		t.Errorf("retract twice: %d", rec.Code)
	}
	b, _ := json.Marshal(map[string]string{"text": "revive"})
	if rec := rig.post(t, "akhan", "/api/notes/"+v.ID+"/revise", string(b)); rec.Code != http.StatusConflict {
		t.Errorf("revise a retracted note: %d", rec.Code)
	}
	// the author within the hour retracts their own
	v2 := rig.add(t, "jsmith", logNote)
	rig.now = rig.now.Add(time.Minute)
	b, _ = json.Marshal(map[string]string{"reason": "typo"})
	if rec := rig.post(t, "jsmith", "/api/notes/"+v2.ID+"/retract", string(b)); rec.Code != http.StatusOK {
		t.Errorf("own retraction: %d %s", rec.Code, rec.Body)
	}
	if n2, _ := rig.store.Get(v2.ID); n2.Latest().Override {
		t.Error("the author's own retraction is marked an override")
	}
}

func TestNoteListFilters(t *testing.T) {
	rig := newNoteRig(t)
	rig.add(t, "jsmith", `{"target":{"kind":"tag","id":"SP"},"text":"one"}`)
	rig.now = rig.now.Add(time.Minute)
	rig.add(t, "akhan", `{"target":{"kind":"alarm","id":"HiTemp","occurrence":"00ff"},"text":"two"}`)
	rig.now = rig.now.Add(time.Minute)
	rig.add(t, "jsmith", `{"target":{"kind":"log"},"text":"three","kind":"handover"}`)
	get := func(q string) []noteView {
		rec := call(t, rig.srv, "GET", "/api/notes"+q, "", withCookie(rig.cs["jsmith"].Value))
		var a notesAnswer
		json.Unmarshal(rec.Body.Bytes(), &a)
		return a.Notes
	}
	texts := func(ns []noteView) string {
		var o []string
		for _, n := range ns {
			o = append(o, n.Text)
		}
		return strings.Join(o, ",")
	}
	for q, want := range map[string]string{
		"":            "three,two,one",
		"?target=tag": "one",
		"?target=alarm&id=HiTemp&occurrence=00ff": "two",
		"?author=jsmith": "three,one",
		"?kind=handover": "three",
		"?limit=1":       "three",
		"?since=" + itoa64(rig.now.Add(-90*time.Second).UnixMilli()): "three,two",
	} {
		if got := texts(get(q)); got != want {
			t.Errorf("%s: %q, want %q", q, got, want)
		}
	}
}

func itoa64(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestNotesOffAnswers404(t *testing.T) {
	srv, _, _ := patServer(t)
	c := signIn(t, srv, "jsmith")
	for _, p := range []string{"/api/notes", "/api/notes/x"} {
		if rec := call(t, srv, "GET", p, "", withCookie(c.Value)); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "auth: block") {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
	if rec := call(t, srv, "POST", "/api/notes", logNote, withCookie(c.Value), ownOrigin, asJSON); rec.Code != http.StatusNotFound {
		t.Errorf("POST: %d", rec.Code)
	}
}

// A viewer reads notes (read) and may not write them.
func TestNoteReadNeedsReadWriteNeedsNote(t *testing.T) {
	rig := newNoteRig(t)
	rig.add(t, "jsmith", logNote)
	// a request with no credential is refused by the read gate
	if rec := call(t, rig.srv, "GET", "/api/notes", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous read: %d", rec.Code)
	}
}

// mustNoteStore is a store for the audit table's token server.
func mustNoteStore() *notes.Store { return notes.New() }

// Naming another target does not get around the permission: an addendum is
// about its note too, so an operator over one area cannot attach one to a
// note about another area's tag by naming the shift log.
func TestNoteAddendumNeedsTheNotesTargetToo(t *testing.T) {
	rig := newNoteRig(t)
	other := rig.add(t, "akhan", `{"target":{"kind":"tag","id":"Out"},"text":"outside jsmith's area"}`)
	for name, body := range map[string]string{
		"no target of its own":    `{"re":"` + other.ID + `","text":"x"}`,
		"naming the shift log":    `{"re":"` + other.ID + `","target":{"kind":"log"},"text":"x"}`,
		"naming his own area tag": `{"re":"` + other.ID + `","target":{"kind":"tag","id":"SP"},"text":"x"}`,
	} {
		if rec := rig.post(t, "jsmith", "/api/notes", body); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", name, rec.Code, rec.Body)
		}
	}
	if rig.store.Count() != 1 {
		t.Errorf("%d notes", rig.store.Count())
	}
}

// An alarm since removed (or with no tag) is whole-controller, as
// acknowledging it is: an area-limited operator does not get the any-area
// branch.
func TestNoteOnAnAlarmThatIsGoneNeedsAWholeControllerGrant(t *testing.T) {
	rig := newNoteRig(t)
	seed := func(by string) {
		rig.store.Apply(notes.Revision{Note: "gone-" + by, Rev: 1, At: rig.now.UnixMilli(), By: by, Principal: "user:" + by,
			Target: notes.Target{Kind: notes.KindAlarm, ID: "Removed"}, Text: "about an alarm since removed"})
	}
	seed("jsmith")
	b, _ := json.Marshal(map[string]string{"text": "revised"})
	if rec := rig.post(t, "jsmith", "/api/notes/gone-jsmith/revise", string(b)); rec.Code != http.StatusForbidden {
		t.Errorf("an area-limited author: %d %s", rec.Code, rec.Body)
	}
	seed("akhan")
	if rec := rig.post(t, "akhan", "/api/notes/gone-akhan/revise", string(b)); rec.Code != http.StatusOK {
		t.Errorf("a whole-controller author: %d %s", rec.Code, rec.Body)
	}
}

// The hour is an hour from the stamp by the clock that made it: a stamp
// ahead of this controller's clock by more than a few minutes (a bad clock,
// a failover between controllers whose clocks differ) is outside it, not
// open for ever.
func TestNoteWindowDoesNotStretchWithAFutureStamp(t *testing.T) {
	rig := newNoteRig(t)
	at := func(d time.Duration) string {
		id := notes.NewID()
		rig.store.Apply(notes.Revision{Note: id, Rev: 1, At: rig.now.Add(d).UnixMilli(), By: "jsmith", Principal: "user:jsmith",
			Target: notes.Target{Kind: notes.KindLog}, Text: "stamped by another clock"})
		return id
	}
	b, _ := json.Marshal(map[string]string{"text": "revised"})
	for d, want := range map[time.Duration]int{
		-30 * time.Minute: http.StatusOK,        // half an hour ago
		2 * time.Minute:   http.StatusOK,        // a little ahead: skew
		2 * time.Hour:     http.StatusForbidden, // two hours ahead
		24 * time.Hour:    http.StatusForbidden,
		-2 * time.Hour:    http.StatusForbidden, // two hours ago
	} {
		if rec := rig.post(t, "jsmith", "/api/notes/"+at(d)+"/revise", string(b)); rec.Code != want {
			t.Errorf("stamped %v from now: %d %s, want %d", d, rec.Code, rec.Body, want)
		}
	}
}

func TestNoteRetractReasonIsOneLine(t *testing.T) {
	rig := newNoteRig(t)
	v := rig.add(t, "jsmith", logNote)
	for _, reason := range []string{"two\nlines", strings.Repeat("x", notes.MaxReason+1), "bell\a"} {
		b, _ := json.Marshal(map[string]string{"reason": reason})
		if rec := rig.post(t, "jsmith", "/api/notes/"+v.ID+"/retract", string(b)); rec.Code != http.StatusBadRequest {
			t.Errorf("reason %q: %d", reason[:min(len(reason), 12)], rec.Code)
		}
	}
}

// An addendum that names no target is journalled by the note it answers.
func TestNoteAddendumIsJournalledByItsNote(t *testing.T) {
	rig := newNoteRig(t)
	v := rig.add(t, "jsmith", logNote)
	rig.add(t, "akhan", `{"re":"`+v.ID+`","text":"maintenance replaced it"}`)
	var found bool
	for _, e := range rig.srv.audit.ring.Recent() {
		if e.Action == "note.add" && e.Result == audit.ResultOK && e.Target == "re:"+v.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("no note.add entry targeting re:%s: %s", v.ID, journalText(rig.srv))
	}
}
