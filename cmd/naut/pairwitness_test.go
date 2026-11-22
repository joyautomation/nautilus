package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/quorum/node"
	"github.com/joyautomation/nautilus/server"
)

// The witness as the operator-intent log's third copy, end to end (#710
// PR 3b-4b, test plan §8.1): it catches up after being away, takes
// replication only from the controller its vote names, is re-copied after
// a rebuild and then carries a takeover, and an unsynced leader hands the
// lead to a synced controller that can sync it.

func (r *pairRig) cluster() string { return r.env["a"]["NAUTILUS_CLUSTER"] }

// witnessLog sends one request to the witness's copy of the pair's log.
func (r *pairRig) witnessLog(method, path, body string) (int, string) {
	r.t.Helper()
	req, err := http.NewRequest(method, "http://"+r.wit+node.Prefix+r.cluster()+"/log"+path, strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set(node.TokenHeader, r.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// witnessStatus is the witness's copy's status (zero when it can't be
// read).
func (r *pairRig) witnessStatus() oplog.Status {
	var st oplog.Status
	if code, body := r.witnessLog("GET", "/api/quorum", ""); code == http.StatusOK {
		_ = json.Unmarshal([]byte(body), &st)
	}
	return st
}

// witnessHolds: the witness's copy is synced and holds the leader's log.
func (r *pairRig) witnessHolds(lead string) bool {
	st := r.witnessStatus()
	l := r.nodes[lead].m.log
	return !st.Unsynced && st.Identity != "" && st.LastSeq == l.LastSeq() && st.Commit == l.Commit()
}

// §8.1 case 2: the witness down, the controllers commit on their own; the
// witness back, the leader's heartbeat fills its copy, which then counts:
// with the standby down too, a write commits on the leader and the
// witness.
func TestPairWitnessLogCatchesUpAfterItWasDown(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	r.stopWitness()
	id := r.startedID(lead)
	r.startWitness(r.wit)
	pairWait(t, "the witness's copy to catch up", 15*time.Second, func() bool { return r.witnessHolds(lead) })
	if !hasSubstitute(r.substitutes(stand), id) {
		t.Fatal("the standby lost the start")
	}
	r.nodes[stand].crash()
	pairWait(t, "the leader to lead on the witness's vote", 15*time.Second, func() bool { return r.nodes[lead].m.q.IsLeader() })
	if code, body, _ := r.call(lead, "DELETE", "/api/substitutes/"+itoa(id), ""); code != http.StatusOK {
		t.Fatalf("stop on the leader and the caught-up witness: %d %s", code, body)
	}
}

// §8.1 case 6: the witness's copy takes replication only from the
// controller its vote names, and at no term above the one it promised.
func TestPairWitnessLogAdmitsOnlyItsGrantedLeader(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	pairWait(t, "the witness's copy to open", 10*time.Second, func() bool { return r.witnessHolds(lead) })
	term := r.nodes[lead].m.q.Term()
	send := func(from string, term uint64) (int, string) {
		b, _ := json.Marshal(map[string]any{"from": "http://x", "node": from, "term": term})
		return r.witnessLog("POST", "/api/oplog/replicate", string(b))
	}
	for _, c := range []struct {
		what string
		from string
		term uint64
	}{
		{"the standby", stand, term},
		{"a stranger", "z", term},
		{"the leader, at a term the witness never promised", lead, term + 1<<20},
	} {
		if code, body := send(c.from, c.term); code != http.StatusForbidden {
			t.Errorf("replication from %s: %d %s, want 403", c.what, code, body)
		}
	}
	if code, body := send(lead, term); code == http.StatusForbidden {
		t.Errorf("replication from the leader at its term refused: %s", body)
	}
	// It never sequences: a write sent to it is refused, not committed.
	if code, body := r.witnessLog("POST", "/api/oplog/propose", `{"kind":"ack","ids":["x"],"by":"op"}`); code != http.StatusConflict {
		t.Errorf("a propose to the witness's copy: %d %s, want 409", code, body)
	}
	// A cluster the witness has never voted for has no log there.
	req, _ := http.NewRequest("GET", "http://"+r.wit+node.Prefix+node.NewClusterID()+"/log/api/quorum", nil)
	req.Header.Set(node.TokenHeader, r.token)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unbound cluster's log: %v %v, want 404", resp, err)
	} else {
		resp.Body.Close()
	}
}

// §8.1 case 3 and F15's log clause: the witness rebuilt (its state wiped)
// is a new incarnation; once the pair has adopted it, the leader re-copies
// the log to it, and it counts: with the standby down the leader and the
// rebuilt witness commit a stop, and when the leader dies the standby's
// takeover finds the stop on the witness.
func TestPairRebuiltWitnessIsRecopiedAndCarriesATakeover(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	id := r.startedID(lead)
	inc := r.elector("a").Status().Incarnation
	r.stopWitness()
	files, _ := filepath.Glob(filepath.Join(r.witDir, "*"))
	for _, f := range files {
		if err := os.RemoveAll(f); err != nil {
			t.Fatal(err)
		}
	}
	r.startWitness(r.wit)
	pairWait(t, "the pair to adopt the rebuilt witness", 60*time.Second, func() bool {
		a, b := r.elector("a").Status(), r.elector("b").Status()
		return a.Incarnation != inc && a.Incarnation == b.Incarnation && a.Commissioned && b.Commissioned
	})
	lead = r.leader()
	stand = other(lead)
	pairWait(t, "the leader to re-copy the log to the witness, and count it (after its quiet period)", 15*time.Second, func() bool {
		st := r.witnessStatus()
		return r.witnessHolds(lead) && st.Identity == strconv.FormatUint(r.elector(lead).Status().Incarnation, 10) &&
			slices.Contains(r.nodes[lead].m.log.Status().Synced, witnessLogURL(r.wit, r.cluster()))
	})
	r.nodes[stand].crash()
	pairWait(t, "the leader to lead on the witness's vote", 15*time.Second, func() bool { return r.nodes[lead].m.q.IsLeader() })
	r.logReady(lead)
	if code, body, _ := r.call(lead, "DELETE", "/api/substitutes/"+itoa(id), ""); code != http.StatusOK {
		t.Fatalf("stop on the leader and the re-copied witness: %d %s", code, body)
	}
	r.nodes[lead].crash()
	r.restart(stand)
	if r.leader() != stand {
		t.Fatal("the standby did not take over")
	}
	if hasSubstitute(r.substitutes(stand), id) {
		t.Fatal("the takeover missed the stop the re-copied witness held")
	}
}

// An unsynced leader whose catch-up fails hands the lead to a synced
// controller that can sync the others (naut-lead's (A), #710): here the
// leader's log and the witness's were lost while both were down, and the
// other controller comes back with a vouch whose one-shot catch-up is
// still unused (as after a recommission it had no time to lead on). The
// unsynced leader yields; the vouched one leads, catches up alone, and
// syncs the other two copies, which get the substitute back.
func TestPairUnsyncedLeaderYieldsToAVouchedPeer(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	id := r.startedID(lead)
	pairWait(t, "the standby to apply the start", 5*time.Second, func() bool { return hasSubstitute(r.substitutes(stand), id) })
	r.nodes[lead].crash()
	r.nodes[stand].crash()
	r.stopWitness()
	for _, f := range []string{
		filepath.Join(r.env[stand]["STATE_DIRECTORY"], pairLogFile), filepath.Join(r.env[stand]["STATE_DIRECTORY"], pairLogMark),
		filepath.Join(r.witDir, r.cluster()+".oplog.jsonl"), filepath.Join(r.witDir, r.cluster()+".oplog.mark"),
	} {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	r.startWitness(r.wit)
	r.restart(stand)
	if r.leader() != stand {
		t.Fatal("the unsynced controller did not lead alone")
	}
	code, body, _ := r.call(stand, "POST", "/api/substitutes", `{"name":"Level","seconds":60}`)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, server.LogNotSynced) {
		t.Fatalf("a write on the unsynced leader: %d %s", code, body)
	}

	// The synced controller comes back, alone among the copies that count:
	// a person vouches for it (a vouch given before a restart does not
	// survive one, #1177), and the unsynced leader yields to it.
	r.restart(lead)
	pairWait(t, "the synced controller to hear the copies", 10*time.Second, func() bool {
		return len(r.nodes[lead].m.log.Status().Reached) == 3
	})
	_, pic, out := r.vouch(lead, "", "")
	if !strings.HasPrefix(out, "refused") || pic.Digest == "" {
		t.Fatalf("a plain vouch on the synced controller: %s %+v", out, pic)
	}
	if _, _, out := r.vouch(lead, pic.Digest, ""); !strings.HasPrefix(out, "done") {
		t.Fatalf("the accepting vouch: %s", out)
	}
	yielded := false
	pairWait(t, "the vouched controller to take the lead", 20*time.Second, func() bool {
		for _, w := range r.nodes[stand].m.q.Status().Warnings {
			yielded = yielded || strings.Contains(w, "yielded to "+lead+": own operator log unsynced")
		}
		return r.nodes[lead].m.q.IsLeader() && r.nodes[lead].rt.Leading()
	})
	if !yielded {
		t.Error("/api/cluster never said the unsynced leader yielded")
	}
	pairWait(t, "the other two copies to sync", 15*time.Second, func() bool {
		return r.nodes[stand].m.log.Synced() && r.witnessHolds(lead)
	})
	pairWait(t, "the yielded controller to get the substitute back", 10*time.Second, func() bool { return hasSubstitute(r.substitutes(stand), id) })
	r.logReady(lead)
	if code, body, _ := r.call(lead, "DELETE", "/api/substitutes/"+itoa(id), ""); code != http.StatusOK {
		t.Fatalf("stop after the recovery: %d %s", code, body)
	}
}

// The leader counts the witness's copy only at the incarnation the
// election counts.
func TestWitnessCountsOnlyAtTheCountedIncarnation(t *testing.T) {
	var counted uint64
	c := witnessCounts("W", func() uint64 { return counted })
	if !c("A", "x") {
		t.Fatal("a controller's copy must count")
	}
	if c("W", "7") {
		t.Fatal("counted with no incarnation counted")
	}
	counted = 7
	if !c("W", "7") || c("W", "8") {
		t.Fatalf("at 7: counts 7 %v, 8 %v", c("W", "7"), c("W", "8"))
	}
}

// The upgrade from 3b-3: the controllers' logs were written before marks
// (the 2-of-2 interim kept every entry on both), so a log with no mark
// counts as synced at once, and writes go on without a vouch.
func TestPairLogsFromBeforeMarksCountAsSynced(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	id := r.startedID(lead)
	for _, n := range []string{"a", "b"} {
		r.nodes[n].crash()
	}
	for _, n := range []string{"a", "b"} {
		if err := os.Remove(filepath.Join(r.env[n]["STATE_DIRECTORY"], pairLogMark)); err != nil {
			t.Fatal(err)
		}
		r.restart(n)
		if !r.nodes[n].m.log.Synced() {
			t.Fatalf("%s's log from before marks starts unsynced", n)
		}
	}
	lead = r.leader()
	r.logReady(lead)
	if code, body, _ := r.call(lead, "DELETE", "/api/substitutes/"+itoa(id), ""); code != http.StatusOK {
		t.Fatalf("stop after the upgrade: %d %s", code, body)
	}
}

// An acknowledgement whose occurrence ended before the leader died does
// not carry across the takeover from the witness: the ack-clear the
// leader proposed when Trip returned to normal is on the witness too, so
// Trip's next activation on the new leader annunciates (#1033's invariant
// across a synced takeover after the leader died).
func TestPairEndedAckDoesNotCarryAcrossATakeoverFromTheWitness(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	r.nodes[stand].crash()
	r.setTrip(t, lead, true)
	pairWait(t, "Trip to annunciate", 5*time.Second, func() bool { return r.alarmState(lead) == alarm.UnackActive })
	r.ackTrip(t, lead)
	before := r.nodes[lead].m.log.Commit()
	r.setTrip(t, lead, false)
	pairWait(t, "the ack-clear to commit on the leader and the witness", 10*time.Second, func() bool {
		return r.nodes[lead].m.log.Commit() > before && r.witnessHolds(lead)
	})
	r.nodes[lead].crash()
	r.restart(stand)
	if r.leader() != stand {
		t.Fatal("the standby did not take over")
	}
	r.setTrip(t, stand, true)
	pairWait(t, "Trip active on the new leader", 5*time.Second, func() bool { return r.alarmState(stand) != alarm.Normal })
	if s := r.alarmState(stand); s != alarm.UnackActive {
		t.Fatalf("Trip's new occurrence on the new leader is %s; want unack-active (the ended ack not carried)", s)
	}
}

// §8.1 case 4, up to "A returns" (the override waits for PR 5's heal
// rule): B down while A and the witness commit; the witness then loses its
// disk. The rebuilt witness can't be adopted without B, so A loses its
// majority; A dies; B and the rebuilt witness can't adopt it without A, so
// nothing leads. A returns: the pair adopts the rebuilt witness, and the
// entry is still there, on both controllers.
func TestPairTwoFaultsStopThePairAndLoseNothing(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	r.nodes[stand].crash()
	id := r.startedID(lead) // on A and the witness
	inc := r.elector(lead).Status().Incarnation
	r.stopWitness()
	files, _ := filepath.Glob(filepath.Join(r.witDir, "*"))
	for _, f := range files {
		if err := os.RemoveAll(f); err != nil {
			t.Fatal(err)
		}
	}
	r.startWitness(r.wit)
	pairWait(t, "A to lose its majority", 15*time.Second, func() bool { return !r.nodes[lead].m.q.IsLeader() })
	r.nodes[lead].crash()
	r.restart(stand)
	time.Sleep(3 * time.Second) // several leases: B and the rebuilt witness alone
	if r.nodes[stand].m.q.IsLeader() {
		t.Fatal("B led with a witness incarnation A never adopted")
	}
	r.restart(lead)
	pairWait(t, "the pair to adopt the rebuilt witness", 60*time.Second, func() bool {
		a, b := r.elector("a").Status(), r.elector("b").Status()
		return a.Incarnation != inc && a.Incarnation == b.Incarnation && a.Commissioned && b.Commissioned
	})
	who := r.leader()
	pairWait(t, "both controllers to hold the start", 15*time.Second, func() bool {
		return hasSubstitute(r.substitutes(lead), id) && hasSubstitute(r.substitutes(stand), id)
	})
	r.logReady(who)
	if code, body, _ := r.call(who, "DELETE", "/api/substitutes/"+itoa(id), ""); code != http.StatusOK {
		t.Fatalf("stop after the pair healed: %d %s", code, body)
	}
}

// The witness's Admit takes its granted controller at a term at or below
// the vote's: a renewal that re-terms the vote (quorum.Candidate's
// reterm) leaves it above the leader's epoch, which is the log's term, so
// "equal" would refuse the real leader after any re-term.
func TestWitnessAdmitsAtOrBelowItsPromise(t *testing.T) {
	for _, c := range []struct {
		from string
		term uint64
		ok   bool
	}{{"a", 9, true}, {"a", 7, true}, {"a", 10, false}, {"a", 0, false}, {"b", 9, false}, {"z", 9, false}} {
		err := admitRule("a", 9, true, "a,b", c.from, c.term)
		if (err == nil) != c.ok {
			t.Errorf("from %s at %d (granted a at 9): %v, want ok %v", c.from, c.term, err, c.ok)
		}
	}
	if admitRule("a", 9, false, "a,b", "a", 9) == nil {
		t.Error("admitted with no vote since the start")
	}
}

// The real path of a vouch, end to end (naut-lead's (4), #1053): the
// rig's commissioning re-commissions a, which vouches for a's empty log;
// a leads (first, or once b yields to it), catches up alone once, and
// syncs b's copy and the witness's.
func TestPairCommissioningVouchSyncsTheOtherCopies(t *testing.T) {
	r := newPairRig(t)
	pairWait(t, "every copy to sync", 15*time.Second, func() bool {
		return r.nodes["a"].m.log.Synced() && r.nodes["b"].m.log.Synced() && r.witnessHolds(r.leader())
	})
	if r.elector("a").Status().Acquisitions == 0 {
		t.Fatal("a never led, yet its vouch's catch-up was used")
	}
	var m oplog.Mark
	b, err := os.ReadFile(filepath.Join(r.env["a"]["STATE_DIRECTORY"], pairLogMark))
	if err != nil || json.Unmarshal(b, &m) != nil {
		t.Fatalf("a's mark: %s %v", b, err)
	}
	if m.Vouch == nil || m.Vouch.By == "" || m.Alone {
		t.Fatalf("a's mark after commissioning: %+v; want a vouch, its catch-up used", m)
	}
	if st := r.nodes["a"].m.q.Status(); st.Log == nil || st.Log.Vouch == nil {
		t.Fatalf("/api/cluster doesn't show the vouch: %+v", st.Log)
	}
}

// A synced leader whose other two copies are lost can't decide alone:
// entries committed while it lagged may be on them. It leads read-only,
// says so with the fix (#1053), and refuses writes as "other copies not
// synced".
func TestPairSyncedLeaderAloneIsReadOnly(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	r.nodes[lead].crash()
	r.nodes[stand].crash()
	r.stopWitness()
	for _, f := range []string{
		filepath.Join(r.env[stand]["STATE_DIRECTORY"], pairLogFile), filepath.Join(r.env[stand]["STATE_DIRECTORY"], pairLogMark),
		filepath.Join(r.witDir, r.cluster()+".oplog.jsonl"), filepath.Join(r.witDir, r.cluster()+".oplog.mark"),
	} {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	r.startWitness(r.wit)
	r.restart(lead)
	if r.leader() != lead {
		t.Fatal("the synced controller did not lead")
	}
	pairWait(t, "the read-only condition", 15*time.Second, func() bool {
		return slices.Contains(r.nodes[lead].m.q.Status().Warnings, OplogReadOnly)
	})
	code, body, h := r.call(lead, "POST", "/api/substitutes", `{"name":"Level","seconds":60}`)
	if code != http.StatusServiceUnavailable || h.Get("X-Nautilus-Outcome") != "refused" || !strings.Contains(body, server.LogCopiesNotSynced) {
		t.Fatalf("a write on the lone synced leader: %d %q %s", code, h.Get("X-Nautilus-Outcome"), body)
	}
}

// wipeState empties member n's state directory (its voter state, its log
// and mark): a lost disk.
func (r *pairRig) wipeState(n string) {
	r.t.Helper()
	files, _ := filepath.Glob(filepath.Join(r.env[n]["STATE_DIRECTORY"], "*"))
	for _, f := range files {
		if err := os.RemoveAll(f); err != nil {
			r.t.Fatal(err)
		}
	}
}

// recommissionA re-commissions a at the witness's incarnation, once both
// controllers hear it, and returns the vouch's refusal (nil: vouched).
func (r *pairRig) recommissionA() error {
	r.t.Helper()
	a, b := r.elector("a"), r.elector("b")
	pairWait(r.t, "both controllers to hear the witness", 10*time.Second, func() bool {
		return a.Status().WitnessIncarnation != 0 && b.Status().WitnessIncarnation != 0
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	refused, err := a.RecommissionBy(ctx, "op", a.Status().WitnessIncarnation, max(a.KnownFloor(), b.KnownFloor())+1000)
	if err != nil {
		r.t.Fatalf("re-commission a: %v", err)
	}
	return refused
}

// A and B lose their disks; the witness's copy is synced and longer
// (#1053 review, naut-lead's example). Re-commissioning A vouches for its
// empty copy (the witness counts, so its later log is no refusal); A and
// the witness then count, A leads, and the witness's entries come back.
func TestPairBothControllersLostRecoverFromTheWitness(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	id := r.startedID(lead)
	pairWait(t, "the witness to hold the start", 10*time.Second, func() bool { return r.witnessHolds(lead) })
	for _, n := range []string{"a", "b"} {
		r.nodes[n].crash()
	}
	for _, n := range []string{"a", "b"} {
		r.wipeState(n)
		r.restart(n)
	}
	if err := r.recommissionA(); err != nil {
		t.Fatalf("the vouch was refused: %v", err)
	}
	who := r.leader()
	pairWait(t, "the leader to sync and hold the witness's start", 15*time.Second, func() bool {
		return r.nodes[who].m.log.Synced() && hasSubstitute(r.substitutes(who), id)
	})
	r.logReady(who)
	if code, body, _ := r.call(who, "DELETE", "/api/substitutes/"+itoa(id), ""); code != http.StatusOK {
		t.Fatalf("stop after the recovery: %d %s", code, body)
	}
}

// A vouch the log refuses never refuses the re-commission (#1053 review):
// here B's copy is kept but unsynced (its mark unreadable) and holds
// entries A's empty copy lacks, so vouching A would lose them. The pair is
// commissioned and controls the plant anyway; its log stays read-only, and
// /api/cluster says why.
func TestPairRefusedVouchStillCommissions(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	r.startedID(lead)
	for _, n := range []string{"a", "b"} {
		r.nodes[n].crash()
	}
	r.stopWitness()
	r.wipeState("a")
	bdir := r.env["b"]["STATE_DIRECTORY"]
	if err := os.Remove(filepath.Join(bdir, pairStateFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bdir, pairLogMark), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{r.cluster() + ".oplog.jsonl", r.cluster() + ".oplog.mark"} {
		if err := os.Remove(filepath.Join(r.witDir, f)); err != nil {
			t.Fatal(err)
		}
	}
	r.startWitness(r.wit)
	r.restart("a")
	r.restart("b")
	refused := r.recommissionA()
	if refused == nil || !strings.Contains(refused.Error(), "controller b") || !strings.Contains(refused.Error(), "does not count") {
		t.Fatalf("the vouch's refusal: %v", refused)
	}
	who := r.leader() // control goes on
	pairWait(t, "/api/cluster to say the vouch was refused", 5*time.Second, func() bool {
		for _, w := range r.nodes["a"].m.q.Status().Warnings {
			if strings.Contains(w, "not vouched for") {
				return true
			}
		}
		return false
	})
	code, body, h := r.call(who, "POST", "/api/substitutes", `{"name":"Level","seconds":60}`)
	if code != http.StatusServiceUnavailable || h.Get("X-Nautilus-Outcome") != "refused" {
		t.Fatalf("a write with no copy synced: %d %q %s", code, h.Get("X-Nautilus-Outcome"), body)
	}
}
