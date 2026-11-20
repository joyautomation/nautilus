package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/oplog"
)

// #708 PR 8c: the principal through the replicated log, the alarm lapse
// line, and the resolution of an "outcome unknown".

// entries collects what a hook records (the hooks record off the apply
// path, on goroutines).
type entries chan audit.Entry

func (c entries) record(e audit.Entry) { c <- e }
func (c entries) batch(es []audit.Entry) {
	for _, e := range es {
		c <- e
	}
}

// next is the next recorded entry; false when none comes within d.
func (c entries) next(d time.Duration) (audit.Entry, bool) {
	select {
	case e := <-c:
		return e, true
	case <-time.After(d):
		return audit.Entry{}, false
	}
}

// Each alarm action proposed through the log carries the request's
// principal in the journal's form, and a request id of the server's; By
// stays the name an operator sees.
func TestAlarmEntriesCarryThePrincipal(t *testing.T) {
	srv, toks, _ := patServer(t)
	var lead atomic.Bool
	lead.Store(true)
	al := NewAlarmLog(srv.alarms, lead.Load, noTerm)
	var got []oplog.Entry
	n, err := oplog.New(oplog.Options{Self: "a", Apply: func(e oplog.Entry) error {
		got = append(got, e)
		_, err := al.Apply(e)
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	al.Attach(n)
	srv.alarmLog = al
	for _, c := range []struct{ path, body string }{
		{"/api/alarms/ack", `{"ids":["HiTemp"]}`},
		{"/api/alarms/shelve", `{"id":"HiTemp","seconds":300}`},
		{"/api/alarms/unshelve", `{"id":"HiTemp"}`},
	} {
		if rec := call(t, srv, "POST", c.path, c.body, bearer(toks["akhan"])); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.path, rec.Code, rec.Body)
		}
		e := got[len(got)-1]
		if e.Principal != "user:akhan" || e.By != "akhan" || !strings.HasPrefix(e.Req, "srv-") {
			t.Errorf("%s: principal %q, by %q, req %q", c.path, e.Principal, e.By, e.Req)
		}
	}
}

// proposeFor stamps the principal; a substitute start keeps the client's
// request id, or none, and is not given one.
func TestProposeForStampsThePrincipal(t *testing.T) {
	srv, toks, _ := patServer(t)
	r := httptest.NewRequest("POST", "/", nil)
	bearer(toks["akhan"])(r)
	r = srv.withActor(r)
	var sent oplog.Entry
	propose := func(_ context.Context, e oplog.Entry) (oplog.Entry, error) { sent = e; return e, nil }
	srv.proposeFor(context.Background(), r, oplog.Entry{Kind: oplog.KindSubstitute, By: "user:akhan"}, false, propose)
	if sent.Principal != "user:akhan" || sent.Req != "" {
		t.Errorf("start: %+v", sent)
	}
	srv.proposeFor(context.Background(), r, oplog.Entry{Kind: oplog.KindSubstitute, Req: "client-1"}, false, propose)
	if sent.Req != "client-1" {
		t.Errorf("start with the client's id: %+v", sent)
	}
	srv.proposeFor(context.Background(), r, oplog.Entry{Kind: oplog.KindSuback}, true, propose)
	if !strings.HasPrefix(sent.Req, "srv-") {
		t.Errorf("suback: %+v", sent)
	}
}

// The journal's principal for an entry is its Principal, else its By (an
// entry an older binary re-marshalled).
func TestPrincipalOfEntryFallsBackToBy(t *testing.T) {
	if p := principalOfEntry(oplog.Entry{By: "akhan", Principal: "user:akhan"}); p != "user:akhan" {
		t.Error(p)
	}
	if p := principalOfEntry(oplog.Entry{By: "akhan"}); p != "akhan" {
		t.Error(p)
	}
}

// lapseLog is an AlarmLog attached to a node named "a", with its hooks
// recording into c.
func lapseLog(t *testing.T, attach bool) (*AlarmLog, entries) {
	t.Helper()
	al := NewAlarmLog(newTestAlarms(t, map[string]any{"HiTempAlm": true}), func() bool { return true }, noTerm)
	c := make(entries, 16)
	al.observe(func(oplog.Entry) {}, c.batch)
	if attach {
		n, err := oplog.New(oplog.Options{Self: "a"})
		if err != nil {
			t.Fatal(err)
		}
		al.Attach(n)
	}
	return al, c
}

// An operator's ack or shelve that lapsed is journalled as alarm.lapse,
// one entry per alarm, as the operator, by the leader that wrote the
// lapse, once.
func TestAlarmLapseIsJournalledOnce(t *testing.T) {
	al, c := lapseLog(t, true)
	al.Apply(oplog.Entry{Seq: 5, Kind: oplog.KindAck, IDs: []string{"HiTemp", "LoFlow"}, By: "akhan", Principal: "user:akhan", Origin: "b", Lapsed: true})
	al.Apply(oplog.Entry{Seq: 6, Kind: oplog.KindShelve, ID: "HiTemp", Until: 99, By: "jsmith", Origin: "b", Lapsed: true})
	lapse := oplog.Entry{Seq: 9, Kind: oplog.KindLapse, Refs: []uint64{5, 6}, Origin: "a"}
	al.Apply(lapse)
	got := map[string]audit.Entry{}
	for i := 0; i < 3; i++ {
		e, ok := c.next(2 * time.Second)
		if !ok {
			t.Fatalf("%d lapse entries, want 3", i)
		}
		got[e.Detail["action"].(string)+" "+e.Target] = e
	}
	if e := got["alarm.ack HiTemp"]; e.Action != "alarm.lapse" || e.User != "user:akhan" || e.Source != audit.SourceSystem || e.Detail["seq"] != uint64(5) {
		t.Errorf("ack HiTemp: %+v", e)
	}
	if e := got["alarm.ack LoFlow"]; e.User != "user:akhan" {
		t.Errorf("ack LoFlow: %+v", e)
	}
	if e := got["alarm.shelve HiTemp"]; e.User != "jsmith" || e.Detail["untilMs"] != int64(99) || e.Detail["principalFrom"] == nil { // no Principal: By, marked a label
		t.Errorf("shelve HiTemp: %+v", e)
	}
	al.Apply(lapse) // applied again: nothing more
	if e, ok := c.next(200 * time.Millisecond); ok {
		t.Errorf("journalled twice: %+v", e)
	}
}

// A replica that did not write the lapse (a standby) does not journal it,
// nor does the node replaying its file at startup (before Attach).
func TestAlarmLapseNotJournalledByOthers(t *testing.T) {
	for _, c := range []struct {
		name   string
		attach bool
		origin string
	}{{"standby", true, "b"}, {"replay", false, "a"}} {
		al, rec := lapseLog(t, c.attach)
		al.Apply(oplog.Entry{Seq: 5, Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "akhan", Origin: "a", Lapsed: true})
		al.Apply(oplog.Entry{Seq: 9, Kind: oplog.KindLapse, Refs: []uint64{5}, Origin: c.origin})
		if e, ok := rec.next(200 * time.Millisecond); ok {
			t.Errorf("%s journalled %+v", c.name, e)
		}
	}
}

// The tracker, step by step.
func newTracker() (*unknowns, entries) {
	c := make(entries, maxUnknowns+8)
	return newUnknowns(c.record), c
}

func proposal(req string) oplog.Entry {
	return oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "akhan", Principal: "user:akhan", Req: req}
}

// committed is p as the log applies it: seq, term and time assigned, and
// (an older leader) the principal dropped.
func committed(p oplog.Entry, seq uint64) oplog.Entry {
	p.Seq, p.Term, p.TS, p.Origin, p.Principal = seq, 2, 1700000000000, "a", ""
	return p
}

func TestUnknownOutcomeResolved(t *testing.T) {
	for _, c := range []struct {
		name        string
		applyFirst  bool
		lapsed      bool
		wantResult  string
		wantOutcome string
	}{
		{"unknown then applied", false, false, audit.ResultOK, outcomeCommitted},
		{"applied then unknown", true, false, audit.ResultOK, outcomeCommitted},
		{"unknown then lapsed", false, true, audit.ResultOK, outcomeLapsed},
	} {
		u, rec := newTracker()
		p := proposal("r1")
		tr := u.begin(p)
		e := committed(p, 7)
		e.Lapsed = c.lapsed
		if c.applyFirst {
			u.applied(e)
			u.unknown(tr, "AUDIT1", "alarm.ack", "HiTemp")
		} else {
			u.unknown(tr, "AUDIT1", "alarm.ack", "HiTemp")
			u.applied(e)
		}
		got, ok := rec.next(2 * time.Second)
		if !ok || got.Result != c.wantResult || got.Detail["outcome"] != c.wantOutcome || got.Detail["resolves"] != "AUDIT1" ||
			got.Action != actionResolved || got.Detail["action"] != "alarm.ack" || got.Target != "HiTemp" ||
			got.User != "user:akhan" || got.Detail["seq"] != uint64(7) || got.Detail["principalFrom"] != nil {
			t.Errorf("%s: %+v (%v)", c.name, got, ok)
		}
		if more, ok := rec.next(100 * time.Millisecond); ok {
			t.Errorf("%s: a second line %+v", c.name, more)
		}
		if len(u.order) != 0 {
			t.Errorf("%s: still tracked", c.name)
		}
	}
}

// Nothing is journalled without evidence or with an ambiguous match: a
// confirmed proposal, one never committed, one with no request id, two in
// flight with one request id, a committed entry whose content differs,
// and a replica that restarted (a new tracker) hearing the commit.
func TestUnknownOutcomeStaysUnknown(t *testing.T) {
	quiet := func(name string, rec entries) {
		t.Helper()
		if e, ok := rec.next(150 * time.Millisecond); ok {
			t.Errorf("%s: journalled %+v", name, e)
		}
	}
	{
		u, rec := newTracker()
		p := proposal("r1")
		u.confirmed(u.begin(p))
		u.applied(committed(p, 7))
		quiet("confirmed", rec)
	}
	{
		// Never committed: another proposal's commit resolves nothing, and
		// it is still owed when the bound pushes it out.
		u, rec := newTracker()
		u.unknown(u.begin(proposal("r1")), "AUDIT1", "alarm.ack", "HiTemp")
		u.applied(committed(proposal("r2"), 7))
		quiet("never committed", rec)
		if !containsReq(u, "r1") {
			t.Error("never committed: no longer tracked")
		}
	}
	{
		// Applied, then a second proposal with its request id, then the
		// first answered unknown: ambiguous, it stays unknown.
		u, rec := newTracker()
		p := proposal("r1")
		a := u.begin(p)
		u.applied(committed(p, 7))
		u.begin(p)
		u.unknown(a, "AUDIT1", "alarm.ack", "HiTemp")
		quiet("applied, then reused, then unknown", rec)
	}
	{
		// A start heard confirmed but not yet applied here (a forwarded
		// proposal), its request id reused with another value and answered
		// unknown: the first one's apply resolves nothing.
		u, rec := newTracker()
		a := oplog.Entry{Kind: oplog.KindSubstitute, Tag: "SP", Value: []byte("1"), By: "user:akhan", Req: "client-1"}
		b := a
		b.Value = []byte("2")
		u.confirmed(u.begin(a))
		u.unknown(u.begin(b), "AUDIT1", "substitute.start", "SP")
		u.applied(committed(a, 7))
		quiet("confirmed start, id reused", rec)
	}
	{
		// A start retried with the same requestId and the same content
		// while the first is confirmed but not yet applied here: the
		// first's apply is the first's, so the retry stays unknown, and
		// the first is no longer tracked once applied.
		u, rec := newTracker()
		a := oplog.Entry{Kind: oplog.KindSubstitute, Tag: "SP", Value: []byte("1"), By: "user:akhan", Req: "client-1"}
		first := u.begin(a)
		u.confirmed(first)
		u.unknown(u.begin(a), "AUDIT1", "substitute.start", "SP")
		u.applied(committed(a, 7))
		quiet("confirmed start, retried identically", rec)
		if containsTracked(u, first) {
			t.Error("the confirmed start is still tracked after its apply")
		}
	}
	{
		// The same, with the first no longer tracked: its value differs, so
		// its apply is not the second's.
		u, rec := newTracker()
		a := oplog.Entry{Kind: oplog.KindSubstitute, Tag: "SP", Value: []byte("1"), GrantedMs: 60000, By: "user:akhan", Req: "client-1"}
		for _, change := range []func(*oplog.Entry){
			func(e *oplog.Entry) { e.Value = []byte("2") },
			func(e *oplog.Entry) { e.GrantedMs = 1 },
			func(e *oplog.Entry) { e.Until = 5 },
			func(e *oplog.Entry) { e.AckOnStart = true },
			func(e *oplog.Entry) { e.Reason = "expired" },
		} {
			b := a
			change(&b)
			tr := u.begin(b)
			u.unknown(tr, "AUDIT1", "substitute.start", "SP")
			u.applied(committed(a, 7))
			quiet(fmt.Sprintf("different content %+v", b), rec)
			u.mu.Lock()
			u.dropLocked(tr)
			u.mu.Unlock()
		}
	}
	{
		u, rec := newTracker()
		p := proposal("")
		if tr := u.begin(p); tr != nil {
			t.Error("tracked without a request id")
		}
		u.applied(committed(p, 7))
		quiet("no request id", rec)
	}
	{
		u, rec := newTracker()
		p := proposal("same")
		a, b := u.begin(p), u.begin(p)
		u.unknown(a, "AUDIT1", "alarm.ack", "HiTemp")
		u.unknown(b, "AUDIT2", "alarm.ack", "HiTemp")
		u.applied(committed(p, 7))
		quiet("ambiguous", rec)
	}
	{
		u, rec := newTracker()
		p := proposal("same")
		a := u.begin(p)
		other := p
		other.IDs = []string{"LoFlow"}
		u.begin(other) // a different action under the same request id
		u.unknown(a, "AUDIT1", "alarm.ack", "HiTemp")
		u.applied(committed(p, 7))
		quiet("ambiguous by request id", rec)
	}
	{
		u, rec := newTracker()
		p := proposal("same")
		other := p
		other.IDs = []string{"LoFlow"}
		u.begin(other) // first in flight under the request id
		b := u.begin(p)
		u.unknown(b, "AUDIT1", "alarm.ack", "HiTemp")
		u.applied(committed(p, 7))
		quiet("ambiguous, the later one", rec)
	}
	{
		u, rec := newTracker()
		p := proposal("r1")
		u.unknown(u.begin(p), "AUDIT1", "alarm.ack", "HiTemp")
		other := committed(p, 7)
		other.IDs = []string{"LoFlow"}
		u.applied(other)
		quiet("different content", rec)
	}
	{
		// A restart: the node replays its file before Attach, so the
		// replayed commit never reaches the tracker, even one that holds
		// the matching proposal.
		u, rec := newTracker()
		p := proposal("r1")
		u.unknown(u.begin(p), "AUDIT1", "alarm.ack", "HiTemp")
		al := NewAlarmLog(newTestAlarms(t, map[string]any{"HiTempAlm": true}), func() bool { return false }, noTerm)
		al.observe(u.applied, func([]audit.Entry) {})
		e := committed(p, 7)
		al.Apply(e)
		quiet("restart", rec)
	}
}

// Past the bound, the oldest unresolved proposal is journalled as staying
// unknown (evicted), never dropped silently; one in flight is kept.
func TestUnknownOutcomeEvictedIsJournalled(t *testing.T) {
	u, rec := newTracker()
	inflight := u.begin(proposal("inflight"))
	for i := 0; i < maxUnknowns; i++ {
		u.unknown(u.begin(proposal(fmt.Sprint("r", i))), fmt.Sprint("AUDIT", i), "alarm.ack", "HiTemp")
	}
	got, ok := rec.next(2 * time.Second)
	if !ok || got.Result != audit.ResultUnknown || got.Detail["outcome"] != outcomeEvicted || got.Detail["resolves"] != "AUDIT0" {
		t.Fatalf("evicted: %+v (%v)", got, ok)
	}
	if more, ok := rec.next(100 * time.Millisecond); ok {
		t.Errorf("a second eviction: %+v", more)
	}
	if !containsTracked(u, inflight) {
		t.Error("the proposal in flight was evicted")
	}
}

func containsTracked(u *unknowns, t *tracked) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, o := range u.order {
		if o == t {
			return true
		}
	}
	return false
}

// End to end through the seam: a route that answers "outcome unknown" is
// journalled "unknown", and when its proposal is later applied here the
// resolution names that entry's id.
func TestUnknownOutcomeLinksTheSeamEntry(t *testing.T) {
	srv := New(newTestRuntime(t))
	var proposed oplog.Entry
	failing := func(_ context.Context, e oplog.Entry) (oplog.Entry, error) {
		proposed = e
		return oplog.Entry{}, errors.New("no answer from the leader")
	}
	mux := http.NewServeMux()
	mux.Handle("POST /x", srv.audited("alarm.ack", func(*http.Request, []byte) auditTarget { return auditTarget{target: "HiTemp"} },
		func(w http.ResponseWriter, r *http.Request) {
			if _, err := srv.proposeFor(r.Context(), r, oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "op"}, true, failing); err != nil {
				unknownOutcome(w, err)
			}
		}))
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/x", nil))
	var unknown audit.Entry
	for _, e := range srv.audit.ring.Recent() {
		if e.Result == audit.ResultUnknown {
			unknown = e
		}
	}
	if unknown.ID == "" || unknown.Action != "alarm.ack" {
		t.Fatalf("no unknown entry: %+v", srv.audit.ring.Recent())
	}
	srv.unknowns.applied(committed(proposed, 3))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range srv.audit.ring.Recent() {
			if e.Detail["resolves"] == unknown.ID {
				if e.Result != audit.ResultOK || e.Action != actionResolved || e.Target != "HiTemp" || e.Detail["outcome"] != outcomeCommitted {
					t.Fatalf("resolution: %+v", e)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no resolution naming %s: %+v", unknown.ID, srv.audit.ring.Recent())
}

// The logs tell the tracker only of entries this replica proposed, and
// never while the node replays its file at startup (before Attach): a
// restarted replica resolves nothing it did not propose in this process.
func TestLogsTellOnlyTheirOwnLiveEntries(t *testing.T) {
	for _, c := range []struct {
		name   string
		attach bool
		origin string
		want   bool
	}{{"replay", false, "a", false}, {"another replica's", true, "b", false}, {"its own", true, "a", true}} {
		al := NewAlarmLog(newTestAlarms(t, map[string]any{"HiTempAlm": true}), func() bool { return false }, noTerm)
		sl := NewSubstituteLog(newTestRuntime(t))
		var heard []string
		al.observe(func(e oplog.Entry) { heard = append(heard, "alarm") }, func([]audit.Entry) {})
		sl.observe(func(e oplog.Entry) { heard = append(heard, "substitute") })
		if c.attach {
			n, err := oplog.New(oplog.Options{Self: "a"})
			if err != nil {
				t.Fatal(err)
			}
			al.Attach(n)
			sl.Attach(n)
		}
		al.Apply(oplog.Entry{Seq: 1, Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "op", Req: "r", Origin: c.origin})
		sl.Apply(oplog.Entry{Seq: 2, Kind: oplog.KindSuback, Refs: []uint64{1}, By: "op", Req: "r", Origin: c.origin})
		if got := len(heard) == 2; got != c.want || !c.want && len(heard) != 0 {
			t.Errorf("%s: heard %v", c.name, heard)
		}
	}
}

func containsReq(u *unknowns, req string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.byReq[req]) > 0
}

// Resolutions are never coalesced: each eviction past the bound is its
// own line, naming the entry it resolves, under its own principal.
func TestResolutionsAreNotCoalesced(t *testing.T) {
	srv := New(newTestRuntime(t))
	u := srv.unknowns
	for i := 0; i < maxUnknowns+10; i++ {
		p := proposal(fmt.Sprint("r", i))
		p.Principal = fmt.Sprint("user:u", i)
		u.unknown(u.begin(p), fmt.Sprint("AUDIT", i), "alarm.ack", "HiTemp")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		seen := map[string]bool{}
		for _, e := range srv.audit.ring.Recent() {
			if r, _ := e.Detail["resolves"].(string); r != "" && e.User == "user:u"+strings.TrimPrefix(r, "AUDIT") {
				seen[r] = true
			}
			if e.Detail["coalesced"] != nil {
				t.Fatalf("a resolution was coalesced: %+v", e)
			}
		}
		if len(seen) == 10 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of 10 evictions journalled in full: %v", len(seen), seen)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A proposal refused before it was proposed (over the log's size cap) is
// not tracked: its outcome is known.
func TestProposeForTooLargeIsNotTracked(t *testing.T) {
	srv := New(newTestRuntime(t))
	r := srv.withActor(httptest.NewRequest("POST", "/", nil))
	tooLarge := func(context.Context, oplog.Entry) (oplog.Entry, error) { return oplog.Entry{}, oplog.ErrTooLarge }
	srv.proposeFor(context.Background(), r, oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}}, true, tooLarge)
	if n := len(srv.unknowns.order); n != 0 {
		t.Errorf("%d tracked after a refusal before proposing", n)
	}
}
