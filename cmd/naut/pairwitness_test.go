package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
	pairWait(t, "the leader to re-copy the log to the witness", 15*time.Second, func() bool {
		st := r.witnessStatus()
		return r.witnessHolds(lead) && st.Identity == strconv.FormatUint(r.elector(lead).Status().Incarnation, 10)
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
	// The vouch's catch-up not used yet.
	mark := filepath.Join(r.env[lead]["STATE_DIRECTORY"], pairLogMark)
	var m oplog.Mark
	b, err := os.ReadFile(mark)
	if err != nil || json.Unmarshal(b, &m) != nil || !m.Synced {
		t.Fatalf("the leader's mark: %s %v", b, err)
	}
	m.Alone = true
	b, _ = json.Marshal(m)
	if err := os.WriteFile(mark, b, 0o600); err != nil {
		t.Fatal(err)
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

	r.restart(lead)
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
