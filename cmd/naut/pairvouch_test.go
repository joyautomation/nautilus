package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/quorum/node"
	"github.com/joyautomation/nautilus/retain"
)

// #1029 3c-3c-2a end to end: naut redundancy vouch's way out of a
// read-only operator-intent log, and a copy whose loss was accepted coming
// back.

// shelveTrip shelves Trip through member n's API for an hour.
func (r *pairRig) shelveTrip(t *testing.T, n string) {
	t.Helper()
	var code int
	var b string
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		var h http.Header
		code, b, h = r.call(n, "POST", "/api/alarms/shelve", `{"id":"Trip","seconds":3600,"by":"op"}`)
		if code == http.StatusOK || h.Get("X-Nautilus-Outcome") != "refused" && !strings.Contains(b, "no leader") || time.Now().After(end) {
			break
		}
	}
	if code != http.StatusOK {
		t.Fatalf("shelve on %s: %d %s", n, code, b)
	}
}

// recJournal records the pair's own audit rows.
type recJournal struct {
	mu sync.Mutex
	es []audit.Entry
}

func (j *recJournal) Record(e audit.Entry) (audit.Entry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.es = append(j.es, e)
	return e, nil
}

func (j *recJournal) entries() []audit.Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]audit.Entry(nil), j.es...)
}

// vouch runs naut redundancy vouch on member n (its Quorum's command), and
// returns its report and outcome.
func (r *pairRig) vouch(n, loss, lostLog string) (node.VouchReport, oplog.Picture, string) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rep, outcome, reason := r.nodes[n].m.q.Vouch(ctx, "op", loss, lostLog)
	vr := rep.(node.VouchReport)
	var p oplog.Picture
	if len(vr.Picture) > 0 {
		if err := json.Unmarshal(vr.Picture, &p); err != nil {
			r.t.Fatal(err)
		}
	}
	return vr, p, outcome + ": " + reason
}

// rebuildWitness stops the witness, deletes the pair's log copy on it
// (its voter state stays), and starts it again: a rebuilt witness's log.
func (r *pairRig) rebuildWitnessLog() {
	r.t.Helper()
	r.stopWitness()
	for _, f := range []string{r.cluster() + ".oplog.jsonl", r.cluster() + ".oplog.mark"} {
		if err := os.Remove(filepath.Join(r.witDir, f)); err != nil {
			r.t.Fatal(err)
		}
	}
	r.startWitness(r.wit)
}

func (r *pairRig) hasWarning(n, sub string) bool {
	for _, w := range r.nodes[n].m.q.Status().Warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

// The synced leader alone among the copies (the other controller's disk
// wiped, the witness's log rebuilt) leads read-only. naut redundancy vouch
// prints the picture and refuses; with --accept-loss=<digest> the leader
// syncs the others and writes again. Nothing was overridden (every copy
// answered, none later), so no new history starts.
func TestPairAcceptLossFreesASyncedLeaderAlone(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	r.startedID(lead)
	r.nodes[stand].crash()
	r.nodes[lead].crash()
	r.wipeState(stand)
	r.rebuildWitnessLog()
	r.restart(lead)
	if r.leader() != lead {
		t.Fatal("the synced controller did not lead")
	}
	pairWait(t, "the read-only condition naming the vouch", 15*time.Second, func() bool {
		return r.hasWarning(lead, "naut redundancy vouch")
	})
	r.unpinOn(lead, stand) // its key went with its disk (#710 2b)
	r.restart(stand)       // wiped: it re-adopts, its log empty and unsynced
	vr, pic, out := r.vouch(lead, "", "")
	if !strings.HasPrefix(out, "refused") || !slices.Contains(pic.Needs, oplog.NeedLoss) || !strings.Contains(vr.Refused, "--accept-loss="+pic.Digest) {
		t.Fatalf("a plain vouch: %s %+v", out, pic)
	}
	if _, _, out := r.vouch(lead, pic.Digest, ""); !strings.HasPrefix(out, "done") {
		t.Fatalf("the accepting vouch: %s", out)
	}
	r.logReady(lead)
	if own, _ := r.nodes[lead].m.log.History(); own.History != 0 {
		t.Fatalf("a catch-up that overrode nothing started history %d", own.History)
	}
	r.startedID(lead)
	pairWait(t, "the other copies to sync", 10*time.Second, func() bool {
		return r.nodes[stand].m.log.Synced() && r.witnessStatus().Unsynced == false
	})
}

// The B-box-silent accept, and ruling 20d. The leader that started a
// substitute goes silent; the witness's log is rebuilt; the other
// controller, alone among the copies, accepts the silent box's loss: its
// catch-up overrides it and starts history 1, which the witness takes in
// place. The substitute is stopped at the new history. The old box comes
// back with its disk (its log at history 0, the substitute active in it
// and in its retain store): it is superseded from first contact, counts
// for nothing, and, run by a supervisor while the other leads synced at
// history 1, asks to restart. Taking over before that restart, it runs
// NONE of the old history's substitutes. Its restart moves the old log
// aside before replaying it and drops the retain store's substitutes; it
// then syncs the new history.
func TestPairAnAcceptedSilentBoxComesBackSuperseded(t *testing.T) {
	retainDir := map[string]string{"a": t.TempDir(), "b": t.TempDir()}
	retainFile := func(n string) string { return filepath.Join(retainDir[n], "retain.json") }
	r := newPairRigWith(t, func(n string, p *project.Project) {
		p.Runtime.Retain = retain.New("", retainFile(n))
	})
	old := r.leader()
	keep := other(old)
	id := r.startedID(old)
	r.shelveTrip(t, old)
	pairWait(t, "every copy to hold the start and the shelf", 10*time.Second, func() bool {
		return r.witnessHolds(old) && hasSubstitute(r.substitutes(keep), id) && r.nodes[keep].al.Held(false, nil)["Trip"].ShelfUntilMs > 0
	})
	retained := func(n string) int {
		st, err := retain.New("", retainFile(n)).Load()
		if err != nil {
			t.Fatal(err)
		}
		if st.Substitutes == nil {
			return 0
		}
		return len(st.Substitutes.Entries)
	}
	pairWait(t, "the leader's retain store to hold the start", 10*time.Second, func() bool { return retained(old) == 1 })

	// The old box goes silent; the witness's log is rebuilt.
	r.nodes[old].crash()
	r.rebuildWitnessLog()
	pairWait(t, "the other controller to lead read-only", 20*time.Second, func() bool {
		return r.nodes[keep].m.q.IsLeader() && r.hasWarning(keep, "no second counting copy")
	})
	_, pic, out := r.vouch(keep, "", "")
	if !strings.HasPrefix(out, "refused") || len(pic.Copies) != 2 {
		t.Fatalf("a plain vouch: %s %+v", out, pic)
	}
	if _, _, out := r.vouch(keep, pic.Digest, ""); !strings.HasPrefix(out, "done") {
		t.Fatalf("the accepting vouch: %s", out)
	}
	r.logReady(keep)
	if own, _ := r.nodes[keep].m.log.History(); own.History != 1 {
		t.Fatalf("the override started history %d, want 1", own.History)
	}
	if code, body, _ := r.call(keep, "DELETE", "/api/substitutes/"+itoa(id), ""); code != http.StatusOK {
		t.Fatalf("stop at the new history: %d %s", code, body)
	}
	if hasSubstitute(r.substitutes(keep), id) {
		t.Fatal("setup: the stop did not end it")
	}

	// The old box comes back, under a supervisor.
	r.env[old]["INVOCATION_ID"] = "test"
	r.restart(old)
	jr := &recJournal{}
	r.nodes[old].m.SetJournal(jr)
	pairWait(t, "the old box to be superseded and ask to restart", 15*time.Second, func() bool {
		select {
		case <-r.nodes[old].m.RestartRequested():
			return true
		default:
			return false
		}
	})
	if _, to := r.nodes[old].m.log.History(); to.History != 1 || r.nodes[old].m.log.Synced() {
		t.Fatalf("the old box's log: superseding to %d, synced %v", to.History, r.nodes[old].m.log.Synced())
	}
	if !r.hasWarning(old, "superseded by history 1") {
		t.Fatalf("no superseded condition: %v", r.nodes[old].m.q.Status().Warnings)
	}
	if es := jr.entries(); len(es) != 1 || es[0].Action != "redundancy.oplog-superseded-restart" || es[0].Detail["acceptedBy"] != "op" || es[0].Detail["to"] != uint64(1) {
		t.Fatalf("the self-restart's journal rows: %+v", es)
	}

	// It takes over before its restart: none of the old history's
	// substitutes runs.
	r.nodes[keep].crash()
	pairWait(t, "the old box to take over", 20*time.Second, func() bool {
		return r.nodes[old].m.q.IsLeader() && r.nodes[old].rt.Leading()
	})
	if hasSubstitute(r.substitutes(old), id) {
		t.Fatal("the superseded box took over running the old history's substitute")
	}
	if r.alarmState(old) == alarm.Shelved {
		t.Fatal("the superseded box took over carrying the old history's shelf")
	}

	// Its restart: the old log aside before replay, the retain store's
	// substitutes dropped; then it syncs the new history from the other.
	r.nodes[old].crash()
	r.restart(old)
	if own, to := r.nodes[old].m.log.History(); own.History != 1 || to.History != 0 {
		t.Fatalf("after its restart: at %d, superseding to %d", own.History, to.History)
	}
	if st := r.nodes[old].m.log.Status(); !strings.Contains(st.Superseded, ".superseded-") || st.Lost != "" {
		t.Fatalf("after its restart: %+v", st)
	}
	if hasSubstitute(r.substitutes(old), id) {
		t.Fatal("the restart replayed the old history's substitute")
	}
	if k := retained(old); k != 0 {
		t.Fatalf("the retain store still holds %d of the old history's substitutes", k)
	}
	r.restart(keep)
	pairWait(t, "the old box to sync the new history", 20*time.Second, func() bool {
		return r.nodes[old].m.log.Synced() && r.nodes[keep].m.log.Synced()
	})
	who := r.leader()
	if hasSubstitute(r.substitutes(who), id) || hasSubstitute(r.substitutes(other(who)), id) {
		t.Fatal("the stopped substitute came back")
	}
}

// A refused vouch at recommission (the other controller silent), then the
// other controller returns, then a plain naut redundancy vouch goes ahead
// (nothing to accept).
func TestPairAPlainVouchAfterARefusedOneGoesAhead(t *testing.T) {
	r := newPairRig(t)
	r.leader()
	for _, n := range []string{"a", "b"} {
		r.nodes[n].crash()
	}
	r.wipeState("a")
	r.rebuildWitnessLog()
	// b, the survivor, is down: the operator unpins a on the witness box,
	// and on b once it is back (#710 2b).
	r.witnessUnpinOffline("a")
	r.restart("a")
	a := r.elector("a")
	pairWait(t, "a to hear the witness", 10*time.Second, func() bool { return a.Status().WitnessIncarnation != 0 })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	refused, err := a.RecommissionBy(ctx, "op", a.Status().WitnessIncarnation, a.KnownFloor()+5000)
	if err != nil {
		t.Fatal(err)
	}
	if refused == nil || !strings.Contains(refused.Error(), "controller b does not answer") {
		t.Fatalf("the vouch's refusal: %v", refused)
	}
	pairWait(t, "the vouch-refused condition to name the command", 10*time.Second, func() bool {
		return r.hasWarning("a", "not vouched for") && r.hasWarning("a", "naut redundancy vouch --accept-loss=")
	})
	r.restart("b")
	if rep := r.unpinOn("b", "a"); rep.Witness != node.UnpinOther {
		t.Fatalf("b's unpin of a after the witness re-pinned a's new key: %+v, want the witness's pin left as it is", rep)
	}
	pairWait(t, "a to hear b's log", 10*time.Second, func() bool {
		return slices.Contains(r.nodes["a"].m.log.Status().Reached, peerURL(r.peer["b"]))
	})
	if _, _, out := r.vouch("a", "", ""); !strings.HasPrefix(out, "done") {
		t.Fatalf("the plain vouch with b back: %s", out)
	}
}

// A lost log on both controllers (missing under the held mark), the
// witness's synced: --accept-lost-log takes the witness's entries back.
func TestPairAcceptLostLogTakesTheWitnessesEntries(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	id := r.startedID(lead)
	pairWait(t, "the witness to hold the start", 10*time.Second, func() bool { return r.witnessHolds(lead) })
	for _, n := range []string{"a", "b"} {
		r.nodes[n].crash()
		if err := os.Remove(filepath.Join(r.env[n]["STATE_DIRECTORY"], pairLogFile)); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"a", "b"} {
		r.restart(n)
	}
	who := r.leader()
	pairWait(t, "the lost condition to name the flag", 10*time.Second, func() bool {
		return r.hasWarning(who, "--accept-lost-log")
	})
	_, pic, out := r.vouch(who, "", "")
	if !strings.HasPrefix(out, "refused") || !slices.Contains(pic.Needs, oplog.NeedLostLog) || slices.Contains(pic.Needs, oplog.NeedLoss) {
		t.Fatalf("a plain vouch on a lost log: %s %+v", out, pic)
	}
	if _, _, out := r.vouch(who, "", pic.Digest); !strings.HasPrefix(out, "done") {
		t.Fatalf("--accept-lost-log: %s", out)
	}
	pairWait(t, "the leader to take the witness's start back", 15*time.Second, func() bool {
		l := r.leader()
		return r.nodes[l].m.log.Synced() && hasSubstitute(r.substitutes(l), id)
	})
}

// A leader that learns a higher history while leading (the window (i)
// leaves: the override's first heartbeat never reached the witness, and
// the old side led on at the old history) stops counting itself synced at
// once and ends the old history's substitutes at the learn, not at a
// later takeover; none runs again while it leads on, read-only, for
// control. superseded_lead_keeps_substitutes turns it red.
func TestPairALeaderThatLearnsANewerHistoryRunsNoneOfTheOld(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	peer := other(lead)
	id := r.startedID(lead)
	r.shelveTrip(t, lead)
	pairWait(t, "every copy to hold the start, and Trip shelved", 10*time.Second, func() bool {
		return r.witnessHolds(lead) && hasSubstitute(r.substitutes(peer), id) && r.alarmState(lead) == alarm.Shelved
	})
	// The other controller's copy, at history 1 (as an override left it).
	r.nodes[peer].crash()
	markFile := filepath.Join(r.env[peer]["STATE_DIRECTORY"], pairLogMark)
	b, err := os.ReadFile(markFile)
	if err != nil {
		t.Fatal(err)
	}
	var mk oplog.Mark
	if err := json.Unmarshal(b, &mk); err != nil {
		t.Fatal(err)
	}
	mk.Lineage = oplog.Lineage{History: 1, HistoryID: 0x77, HistoryBy: "op"}
	b, _ = json.Marshal(mk)
	if err := os.WriteFile(markFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if !hasSubstitute(r.substitutes(lead), id) {
		t.Fatal("setup: the leader lost the substitute")
	}
	r.restart(peer)
	pairWait(t, "the leader to learn history 1", 10*time.Second, func() bool {
		_, to := r.nodes[lead].m.log.History()
		return to.History == 1
	})
	learnt := time.Now()
	pairWait(t, "the leader to end the old history's substitute", 2*time.Second, func() bool {
		return !hasSubstitute(r.substitutes(lead), id)
	})
	if d := time.Since(learnt); d > 500*time.Millisecond {
		t.Fatalf("the old history's substitute ran %v after the learn", d)
	}
	if r.nodes[lead].m.log.Synced() {
		t.Fatal("the superseded leader still counts itself synced")
	}
	pairWait(t, "the leader to end the old history's shelf", 2*time.Second, func() bool {
		return r.alarmState(lead) != alarm.Shelved && len(r.nodes[lead].al.Held(false, nil)) == 0
	})
	for end := time.Now().Add(1500 * time.Millisecond); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if r.nodes[lead].m.q.IsLeader() && hasSubstitute(r.substitutes(lead), id) {
			t.Fatal("the old history's substitute came back on the leader")
		}
	}
}

// A pair whose copies are all empty at history 0 (#1029 3c-3c-2b, S2):
// here a new pair that never wrote, its leader alone among the copies
// (the other controller's disk wiped, the witness's log rebuilt). That is
// the picture of a new pair's controller restarted before its first
// catch-up (the oplog suite runs that sequence itself,
// TestANewPairsPlainVouchAfterARestartNeedsNoAcceptLoss): nothing can be
// lost. The read-only condition names the plain vouch, and the plain
// vouch goes ahead (empty_pair_needs_accept).
func TestPairANewPairsPlainVouchNeedsNoAcceptLoss(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	r.nodes[stand].crash()
	r.nodes[lead].crash()
	r.wipeState(stand)
	r.rebuildWitnessLog()
	r.restart(lead)
	if r.leader() != lead {
		t.Fatal("the synced controller did not lead")
	}
	pairWait(t, "the read-only condition naming the plain vouch", 15*time.Second, func() bool {
		return r.hasWarning(lead, OplogReadOnlyNewPair)
	})
	r.unpinOn(lead, stand) // its key went with its disk (#710 2b)
	r.restart(stand)       // wiped: it re-adopts, its log empty and unsynced
	var out string
	var pic oplog.Picture
	pairWait(t, "the plain vouch to go ahead", 15*time.Second, func() bool {
		// The wiped standby answers once it is up: until then it is a
		// silent copy, and the vouch refuses naming it.
		_, pic, out = r.vouch(lead, "", "")
		return strings.HasPrefix(out, "done")
	})
	if len(pic.Needs) != 0 {
		t.Fatalf("the plain vouch: %s %+v", out, pic)
	}
	r.logReady(lead)
	if own, _ := r.nodes[lead].m.log.History(); own.History != 0 {
		t.Fatalf("a catch-up that overrode nothing started history %d", own.History)
	}
	r.startedID(lead)
	pairWait(t, "the other copies to sync", 10*time.Second, func() bool {
		return r.nodes[stand].m.log.Synced() && r.witnessStatus().Unsynced == false
	})
}
