//go:build oplogtest

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/oplog"
)

// #1455 PR 2b-1.9: the production composition through committed
// compactions, while the shipped build leaves compaction off. The pair rig
// as naut run wires it (the server's keep decision, sl.Base, the runtime,
// the alarm view, the notes), its keep-rules version and short timings set
// only through the oplogtest seam (oplog.TestCompaction). Run with
//
//	go test -tags oplogtest -run '^TestPairCompaction' ./cmd/naut/

// baseTold is the last base each member's log told its Base hook;
// compactHeld holds every keep decision off (a failed decision: the next
// look asks again) until the test has its snapshot, so a slow runner never
// compacts part of the churn early. Package state: these tests never run
// in parallel.
var (
	baseTold    sync.Map
	compactHeld atomic.Bool
)

// compactRig is a pair rig whose three logs compact: a 3 s margin, a look
// every 200 ms, from 20 entries, once, and only after compactHeld is let go.
func compactRig(t *testing.T) *pairRig {
	t.Helper()
	testLogTweak = func(which string, o *oplog.Options) {
		oplog.TestCompaction{Rules: 1, Every: 200 * time.Millisecond, Margin: 3 * time.Second, Gap: time.Hour, MinEntries: 20}.Apply(o)
		// What the pair's base hook is told (the runtime's retain gate and
		// substitute sequence start there): the wiring is the test's, the
		// hook stays the member's own.
		if c := o.Compact; c != nil {
			o.Compact = func(ctx context.Context, in oplog.CompactInput) ([]uint64, error) {
				if compactHeld.Load() {
					return nil, errors.New("held by the test until its snapshot")
				}
				return c(ctx, in)
			}
		}
		if base := o.Base; which == "pair" && base != nil {
			node := o.Node
			o.Base = func(through uint64) {
				baseTold.Store(node, through)
				base(through)
			}
		}
	}
	testPairAPIToken = "api-secret" // a token may add a note (only a person may revise one)
	// The retain store too, as a plant's pair keeps it: its substitutes,
	// shelves and acks are gated by what the log delivered (the base hook).
	testPairFiles = func(a, b, w string) fstest.MapFS {
		fs := pairProject(a, b, w)
		fs["nautilus.yaml"] = &fstest.MapFile{Data: append([]byte("retain: {}\n"), fs["nautilus.yaml"].Data...)}
		return fs
	}
	compactHeld.Store(true)
	t.Cleanup(func() { testLogTweak, testPairAPIToken, testPairFiles = nil, "", nil; compactHeld.Store(false) })
	return newPairRig(t)
}

// operatorState is what a person and a takeover see of the log's state on
// member n: the substitutes (without the live reading, which moves), the
// alarm view a takeover carries, the notes the API lists, and what naut
// notes export reads from the state directory.
type operatorState struct {
	Subs   []string
	Alarms string
	Notes  string
	Export string
}

func (r *pairRig) operatorState(n string) operatorState {
	r.t.Helper()
	var st operatorState
	for _, s := range r.substitutes(n) {
		st.Subs = append(st.Subs, fmt.Sprintf("%d %s %v by %s %d-%d acked %v", s.ID, s.Name, s.Value, s.By, s.StartMs, s.UntilMs, s.Acked))
	}
	held := r.nodes[n].al.Held(true, nil)
	for id, h := range held {
		h.ChatterLastMs = 0 // "as of the last time a live leader was heard": it moves with the clock
		held[id] = h
	}
	b, _ := json.Marshal(held)
	st.Alarms = string(b)
	code, body, _ := r.call(n, "GET", "/api/notes", "")
	if code != 200 {
		r.t.Fatalf("notes on %s: %d %s", n, code, body)
	}
	var list struct {
		Notes []json.RawMessage `json:"notes"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		r.t.Fatalf("notes on %s: %v", n, err)
	}
	st.Notes = joinBut(list.Notes)
	notes, _, err := loadNotes(r.env[n]["STATE_DIRECTORY"], io.Discard)
	if err != nil {
		r.t.Fatalf("notes export on %s: %v", n, err)
	}
	var raw []json.RawMessage
	for _, x := range notes {
		b, _ := json.Marshal(x)
		raw = append(raw, b)
	}
	st.Export = joinBut(raw)
	return st
}

// triggerNote is the write after the margin that lets T pass the standing
// state (T is the newest entry's TS at most); the states compare without it.
const triggerNote = "after the margin"

func joinBut(xs []json.RawMessage) string {
	var out []string
	for _, x := range xs {
		if !bytes.Contains(x, []byte(triggerNote)) {
			out = append(out, string(x))
		}
	}
	return strings.Join(out, "\n")
}

func (a operatorState) diff(b operatorState) string {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if bytes.Equal(ja, jb) {
		return ""
	}
	return fmt.Sprintf("\nbefore %s\nafter  %s", ja, jb)
}

// post sends a write through member n, retrying a certain refusal as
// startedID does, and returns the body.
func (r *pairRig) post(n, method, path, body string) string {
	r.t.Helper()
	var code int
	var b string
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		var h http.Header
		code, b, h = r.call(n, method, path, body)
		if code/100 == 2 || h.Get("X-Nautilus-Outcome") != "refused" || time.Now().After(end) {
			break
		}
	}
	if code/100 != 2 {
		r.t.Fatalf("%s %s on %s: %d %s", method, path, n, code, b)
	}
	return b
}

func TestPairCompactionKeepsTheOperatorState(t *testing.T) {
	r := compactRig(t)
	lead := r.leader()
	stand := other(lead)

	// Churn, which the compaction drops: substitutes started and ended,
	// alarm cycles acknowledged, shelves undone; and notes, always kept.
	for range 6 {
		id := r.startedID(lead)
		r.post(lead, "DELETE", fmt.Sprintf("/api/substitutes/%d", id), "")
	}
	for range 4 {
		r.setTrip(t, lead, true)
		pairWait(t, "Trip to annunciate", 5*time.Second, func() bool { return r.alarmState(lead).String() != "normal" })
		r.ackTrip(t, lead)
		r.setTrip(t, lead, false)
		pairWait(t, "Trip to clear", 5*time.Second, func() bool { return r.alarmState(lead).String() == "normal" })
	}
	for range 3 {
		r.shelveTrip(t, lead)
		r.post(lead, "POST", "/api/alarms/unshelve", `{"id":"Trip","by":"op"}`)
	}
	for i := range 3 {
		r.post(lead, "POST", "/api/notes", fmt.Sprintf(`{"target":{"kind":"log"},"text":"round %d"}`, i+1))
	}
	// What stands at the end, older than the margin once the compaction
	// looks: a running substitute, a shelf, notes.
	keep := r.startedID(lead)
	r.shelveTrip(t, lead)
	r.post(lead, "POST", "/api/notes", `{"target":{"kind":"asset","id":"Pump 2"},"text":"bearing noise","kind":"handover"}`)
	// The leader writes what it detects (Trip's chatter episode, from the
	// cycles above) at its own log tick: wait for the log to be quiet.
	var standing uint64
	var quiet time.Time
	pairWait(t, "the log to be quiet for a second, every copy holding it", 15*time.Second, func() bool {
		c := r.nodes[lead].m.log.Commit()
		if c != standing || r.nodes[stand].m.log.Commit() != c {
			standing, quiet = c, time.Now()
			return false
		}
		return time.Since(quiet) >= time.Second
	})
	for _, n := range []string{lead, stand} {
		if st := r.nodes[n].m.log.Status(); st.CompactThrough != 0 {
			t.Fatalf("%s compacted through %d before the snapshot: the hold failed", n, st.CompactThrough)
		}
	}
	before := map[string]operatorState{lead: r.operatorState(lead), stand: r.operatorState(stand)}
	if len(before[lead].Subs) != 1 {
		t.Fatalf("substitutes before: %v; want the one kept", before[lead].Subs)
	}

	// The hold off: once the all-marker is 3 s old and the standing state
	// is past the margin, a look compacts, and every copy rewrites its
	// file. T is the newest entry's TS at most, so a trigger note a second.
	compactHeld.Store(false)
	cluster := r.env["a"]["NAUTILUS_CLUSTER"]
	witFile := filepath.Join(r.witDir, cluster+".oplog.jsonl")
	var nudged time.Time
	pairWait(t, "a compaction through the standing state on all three copies", 30*time.Second, func() bool {
		if time.Since(nudged) >= time.Second {
			r.post(lead, "POST", "/api/notes", `{"target":{"kind":"log"},"text":"`+triggerNote+`"}`)
			nudged = time.Now()
		}
		for _, n := range []string{"a", "b"} {
			st := r.nodes[n].m.log.Status()
			if st.CompactThrough < standing || st.CompactPending {
				return false
			}
		}
		b, err := os.ReadFile(witFile)
		return err == nil && bytes.HasPrefix(b, []byte(`{"b":`))
	})
	st := r.nodes[lead].m.log.Status()
	var kept int
	for _, e := range r.nodes[lead].m.log.After(0) {
		if e.Seq <= st.CompactThrough {
			kept++
		}
	}
	if kept*2 > int(st.CompactThrough) {
		t.Fatalf("kept %d of %d entries up to Through: the churn was not dropped", kept, st.CompactThrough)
	}
	t.Logf("compacted through %d, kept %d", st.CompactThrough, kept)
	if _, body, _ := r.call(lead, "GET", "/api/cluster", ""); !strings.Contains(body, fmt.Sprintf(`"compactedThrough":%d`, st.CompactThrough)) {
		t.Fatalf("/api/cluster does not say the log was compacted: %s", body)
	}

	for _, n := range []string{lead, stand} {
		if d := before[n].diff(r.operatorState(n)); d != "" {
			t.Fatalf("%s after the compaction:%s", n, d)
		}
	}
	// The witness's file: the compacted log, as the leader holds it.
	wit, err := oplog.ReadCommitted(witFile)
	if err != nil {
		t.Fatalf("the witness's compacted file: %v", err)
	}
	led := r.nodes[lead].m.log.After(0)
	if len(wit) == 0 || len(wit) > len(led) {
		t.Fatalf("the witness's file holds %d entries, the leader %d", len(wit), len(led))
	}
	for i, e := range wit {
		a, _ := json.Marshal(e)
		b, _ := json.Marshal(led[i])
		if !bytes.Equal(a, b) {
			t.Fatalf("the witness's file at %d: %s; the leader holds %s", i, a, b)
		}
	}

	// A restart replays the compacted file into the same state.
	r.nodes[stand].stop()
	baseTold.Delete(stand)
	r.restart(stand)
	pairWait(t, "the restarted standby's log to sync", 15*time.Second, func() bool {
		s := r.nodes[stand].m.log.Status()
		return !s.Unsynced && s.Commit >= st.Commit && s.CompactThrough == st.CompactThrough
	})
	if d := before[stand].diff(r.operatorState(stand)); d != "" {
		t.Fatalf("the standby after its restart over the compacted log:%s", d)
	}
	if v, ok := baseTold.Load(stand); !ok || v.(uint64) != st.CompactThrough {
		t.Fatalf("the restarted standby's base hook was told %v (%v); want %d", v, ok, st.CompactThrough)
	}

	// And the restarted copy takes over with the same substitute running
	// and the shelf in force.
	r.handover(t, lead)
	subs := r.substitutes(stand)
	if len(subs) != 1 || subs[0].ID != keep {
		t.Fatalf("the new leader runs %+v; want substitute %d", subs, keep)
	}
	if h := r.nodes[stand].al.Held(true, nil)["Trip"]; h.ShelfUntilMs == 0 {
		t.Fatalf("the new leader's Trip: %+v; want shelved", h)
	}

	// The old leader, now the standby, restarts over its own compacted
	// file into the standby's state.
	r.nodes[lead].stop()
	r.restart(lead)
	pairWait(t, "the restarted old leader's log to sync", 15*time.Second, func() bool {
		s := r.nodes[lead].m.log.Status()
		return !s.Unsynced && s.Commit >= st.Commit && s.CompactThrough == st.CompactThrough
	})
	if d := before[stand].diff(r.operatorState(lead)); d != "" {
		t.Fatalf("the old leader after its restart over the compacted log:%s", d)
	}
}
