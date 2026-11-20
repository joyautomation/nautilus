package server

import (
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/retain"
)

// Alarm writes through a pair's operator-intent log (#710 PR 3b-2c), at
// the server: what is proposed, what is refused before proposing, and how
// a standby and a takeover use the view. The pair end to end is
// cmd/naut's pairalarm_test.go.

// loggedAlarms is a server whose alarm writes go through a standalone
// oplog node (one replica: every write commits at once), with the entries
// it proposed recorded; leading says whether this replica leads.
func loggedAlarms(t *testing.T, cond map[string]any, leading *atomic.Bool) (*Server, *alarm.Engine, *AlarmLog, *[]oplog.Entry) {
	t.Helper()
	eng := newTestAlarms(t, cond)
	al := NewAlarmLog(eng, leading.Load, noTerm)
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
	return New(newTestRuntime(t), Options{Alarms: eng, AlarmLog: al}), eng, al, &got
}

func state(eng *alarm.Engine, id string) alarm.State {
	for _, r := range eng.Records() {
		if r.ID == id {
			return r.State
		}
	}
	return alarm.Normal
}

// "Acknowledge everything" is proposed as the leader's unacknowledged
// alarms by id (a standby can't know them); an unknown id is refused
// before anything is proposed.
func TestAlarmAckThroughTheLog(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	s, eng, _, got := loggedAlarms(t, cond, &lead)
	eng.Evaluate()
	rec := do(t, s, "POST", "/api/alarms/ack", `{"all":true,"by":"op"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"acked":1`) {
		t.Fatalf("ack all: %d %s", rec.Code, rec.Body)
	}
	if len(*got) != 1 || (*got)[0].Kind != oplog.KindAck || !slices.Equal((*got)[0].IDs, []string{"HiTemp"}) {
		t.Fatalf("proposed %+v; want one ack naming HiTemp", *got)
	}
	if st := state(eng, "HiTemp"); st != alarm.AckActive {
		t.Fatalf("HiTemp %s after its committed ack on the leader", st)
	}
	if rec := do(t, s, "POST", "/api/alarms/ack", `{"ids":["Nope"],"by":"op"}`); rec.Code != http.StatusNotFound || len(*got) != 1 {
		t.Fatalf("an unknown id: %d, %d entries proposed; want 404 and nothing proposed", rec.Code, len(*got))
	}
	// Shelve and unshelve go through the log too.
	if rec := do(t, s, "POST", "/api/alarms/shelve", `{"id":"LoFlow","seconds":600,"by":"op"}`); rec.Code != http.StatusOK {
		t.Fatalf("shelve: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, s, "POST", "/api/alarms/unshelve", `{"id":"LoFlow","by":"op"}`); rec.Code != http.StatusOK {
		t.Fatalf("unshelve: %d %s", rec.Code, rec.Body)
	}
	kinds := []string{}
	for _, e := range *got {
		kinds = append(kinds, e.Kind)
	}
	if !slices.Equal(kinds, []string{oplog.KindAck, oplog.KindShelve, oplog.KindUnshelve}) {
		t.Fatalf("proposed %v", kinds)
	}
}

// A standby records a committed ack in its view and leaves its engine
// alone (no journal row: the leader journals it); its view is what a
// synced takeover restores. An unsynced takeover carries the shelves and
// no ack, and says so.
func TestAlarmViewOnAStandby(t *testing.T) {
	var lead atomic.Bool // a standby
	cond := map[string]any{"HiTempAlm": true, "LoFlowAlm": false}
	_, eng, al, _ := loggedAlarms(t, cond, &lead)
	eng.Evaluate()
	until := time.Now().Add(time.Hour).UnixMilli()
	for _, e := range []oplog.Entry{
		{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "op", TS: 1000},
		{Kind: oplog.KindShelve, ID: "LoFlow", Until: until, By: "op"},
	} {
		if _, err := al.Apply(e); err != nil {
			t.Fatal(err)
		}
	}
	if st := state(eng, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("the standby's engine moved to %s on a committed ack", st)
	}
	held := al.Held(true, nil)
	if !held["HiTemp"].Acked || held["HiTemp"].AckBy != "op" || held["LoFlow"].ShelfUntilMs != until {
		t.Fatalf("synced view %+v", held)
	}
	retained := map[string]retain.AlarmRetain{"HiTemp": {Acked: true, AckBy: "old"}}
	held = al.Held(false, retained)
	if held["HiTemp"].Acked || held["LoFlow"].ShelfUntilMs != until {
		t.Fatalf("unsynced view %+v; want the shelf and no ack (not even the retained one)", held)
	}
	// An ack-clear drops the ack from the view.
	if _, err := al.Apply(oplog.Entry{Kind: oplog.KindAckClear, ID: "HiTemp"}); err != nil {
		t.Fatal(err)
	}
	if al.Held(true, nil)["HiTemp"].Acked {
		t.Fatal("the view kept an ack its ack-clear dropped")
	}
	// PairAlarms: an unsynced restore with an ack to drop says so in the
	// alarm journal.
	if _, err := al.Apply(oplog.Entry{Kind: oplog.KindAck, IDs: []string{"HiTemp"}, By: "op"}); err != nil {
		t.Fatal(err)
	}
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, false)
	evs, err := eng.Journal(time.Now().Add(-time.Hour), time.Now().Add(time.Hour), alarm.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(evs, func(e alarm.Event) bool { return strings.Contains(e.Name, "acks not carried: takeover unsynced") }) {
		t.Fatal("no 'acks not carried' in the alarm journal")
	}
}

// With no majority known, an alarm write is refused before it is proposed,
// with the pair's text.
func TestAlarmWriteRefusedWithoutMajority(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	eng := newTestAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false})
	eng.Evaluate()
	al := NewAlarmLog(eng, lead.Load, noTerm)
	n, err := oplog.New(oplog.Options{Self: "http://127.0.0.1:1", Peers: []string{"http://127.0.0.1:1", "http://127.0.0.1:2"},
		Leadership: oplog.Static{Self: "http://127.0.0.1:1", Leader: "http://127.0.0.1:1"}, Token: "t", Ephemeral: true, Apply: func(e oplog.Entry) error { _, err := al.Apply(e); return err }})
	if err != nil {
		t.Fatal(err)
	}
	al.Attach(n)
	s := New(newTestRuntime(t), Options{Alarms: eng, AlarmLog: al})
	rec := do(t, s, "POST", "/api/alarms/ack", `{"ids":["HiTemp"],"by":"op"}`)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get(outcomeHeader) != "refused" || !strings.Contains(rec.Body.String(), PairNoMajorityAlarms) {
		t.Fatalf("ack with no majority: %d %q %s", rec.Code, rec.Header().Get(outcomeHeader), rec.Body)
	}
	if st := state(eng, "HiTemp"); st != alarm.UnackActive {
		t.Fatalf("a refused ack moved HiTemp to %s", st)
	}
}

// Under an auth: block a write through the pair's log is area-checked
// before it is proposed (#708 with #710): a principal fenced to another
// area is refused an ack, a shelve and an unshelve, and nothing reaches
// the log; the administrator's ack is proposed.
func TestAlarmWritesThroughTheLogCheckTheArea(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	s, eng, _, got := loggedAlarms(t, map[string]any{"HiTempAlm": true, "LoFlowAlm": false}, &lead)
	eng.Evaluate()
	enforce(t, s)
	as := func(tok string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
	}
	for _, c := range []struct{ path, body string }{
		{"/api/alarms/ack", `{"ids":["HiTemp"],"by":"op"}`},
		{"/api/alarms/shelve", `{"id":"LoFlow","seconds":600,"by":"op"}`},
		{"/api/alarms/unshelve", `{"id":"LoFlow","by":"op"}`},
	} {
		if rec := call(t, s, "POST", c.path, c.body, as("f-secret")); rec.Code != http.StatusForbidden || len(*got) != 0 {
			t.Errorf("%s fenced to another area: %d, %d proposed; want 403 and nothing", c.path, rec.Code, len(*got))
		}
	}
	if rec := call(t, s, "POST", "/api/alarms/ack", `{"ids":["HiTemp"],"by":"op"}`, as("admin-secret")); rec.Code != http.StatusOK || len(*got) != 1 {
		t.Errorf("the administrator's ack: %d, %d proposed", rec.Code, len(*got))
	}
}

// On a redundant pair the name an ack, shelve or unshelve carries in the
// operator-intent log is recordBy's, not the client's (#708 PR 7b): a
// request acting as a user (a personal access token here; a browser
// sign-in is the same principal) writes the user into the replicated entry
// whatever "by" it sent, so the standby's alarm history agrees with the
// leader's.
func TestAlarmLogCarriesTheSignedInUser(t *testing.T) {
	srv, toks, _ := patServer(t)
	eng := srv.alarms // auditServer's: HiTemp active
	var lead atomic.Bool
	lead.Store(true)
	al := NewAlarmLog(eng, lead.Load, noTerm)
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
		{"/api/alarms/ack", `{"ids":["HiTemp"],"by":"someone-else"}`},
		{"/api/alarms/shelve", `{"id":"HiTemp","seconds":300,"by":"someone-else"}`},
		{"/api/alarms/unshelve", `{"id":"HiTemp","by":"someone-else"}`},
	} {
		before := len(got)
		if rec := call(t, srv, "POST", c.path, c.body, bearer(toks["akhan"])); rec.Code != http.StatusOK {
			t.Fatalf("%s through the log: %d %s", c.path, rec.Code, rec.Body)
		}
		if len(got) != before+1 {
			t.Fatalf("%s: %d entries proposed, want 1", c.path, len(got)-before)
		}
		if e := got[len(got)-1]; e.By != "akhan" {
			t.Errorf("%s: the log entry carries by %q, want akhan", c.path, e.By)
		}
	}
	// And the engine the entries applied to records the user too.
	for _, r := range eng.Records() {
		if r.ID == "HiTemp" && r.AckBy != "akhan" {
			t.Errorf("HiTemp acknowledged by %q through the log, want akhan", r.AckBy)
		}
	}
}
