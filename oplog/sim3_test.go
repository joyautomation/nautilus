package oplog

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
	"time"
)

// The T2 run for the log on three replicas (#710 PR 3b-4a, test plan
// §8.1): seeded schedules of writes, leader changes, replicas down, lost
// answers, late ones (handled after the sender stopped waiting, often
// after a new leader took over), lost commit pushes, new leaders that
// heartbeat before they catch up, chunks of one to three entries and one or
// two resend rounds, cut links, restarts, lost logs and
// rebuilt replicas, on the in-process network, with two oracles checked
// after every step:
//
//   - agreement: no two replicas hold different entries at a seq both
//     have committed;
//   - no loss: every write that was answered as committed is at its seq
//     in every replica's committed log that reaches that seq, and a leader
//     that synced holds all of them, committed;
//   - kept on a majority: each such write is held at its seq by synced
//     replicas plus unsynced ones (standing in for a copy they may have
//     lost), at least two of three, after every step.
//
// A log is lost or a replica rebuilt only while all three are synced, so a
// schedule can go on (two unsynced replicas wait for a person, by design);
// the oracles hold either way. OPLOG_SIM_SEEDS sets how many seeds run
// (default 120; the nightly runs 3,000), OPLOG_SIM_STEPS the steps each (default 80).
func TestThreeReplicaSimulation(t *testing.T) {
	seeds, steps := envInt("OPLOG_SIM_SEEDS", 120), envInt("OPLOG_SIM_STEPS", 80)
	var acked, syncs int
	for seed := range seeds {
		// One subtest per seed, so each seed's replicas and files are
		// released as it ends. A failure names its seed: rerun it with
		// OPLOG_SIM_ONE=<seed>.
		if !t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			a, s := simulate(t, uint64(seed), steps)
			acked += a
			syncs += s
		}) {
			return
		}
	}
	// Not vacuous: writes were committed and leaders synced through faults.
	if acked < seeds*steps/20 || syncs < seeds {
		t.Fatalf("the schedules did too little: %d writes committed, %d leader syncs over %d seeds", acked, syncs, seeds)
	}
	t.Logf("%d seeds × %d steps: %d writes committed, %d leader syncs", seeds, steps, acked, syncs)
}

// TestThreeReplicaSimulationOne reruns one seed with its trace.
func TestThreeReplicaSimulationOne(t *testing.T) {
	v := os.Getenv("OPLOG_SIM_ONE")
	if v == "" {
		t.Skip("set OPLOG_SIM_ONE=<seed>")
	}
	seed, _ := strconv.Atoi(v)
	simTrace = true
	defer func() { simTrace = false }()
	simulate(t, uint64(seed), envInt("OPLOG_SIM_STEPS", 80))
}

var simTrace bool

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

type ackRec struct {
	id  string
	seq uint64
}

// simulate runs one seeded schedule; it returns how many writes were
// answered committed and how many leader syncs succeeded.
func simulate(t *testing.T, seed uint64, steps int) (int, int) {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, 0x3b4a))
	// Half the seeds replicate a few entries per chunk, so resends take
	// several chunks and run out of rounds (#1050 review).
	var lim limits
	if r.IntN(2) == 0 {
		lim.chunkEntries = 1 + r.IntN(3)
		lim.rounds = 1 + r.IntN(2) // so a peer a few entries behind runs out of rounds
	}
	tr := newTrio(t, withLimits(lim))
	tr.timeout = 300 * time.Millisecond
	for i := range 3 {
		tr.restart(i) // with the short timeout
	}
	trace := func(format string, args ...any) {
		if simTrace {
			t.Logf("step: "+format, args...)
		}
	}
	var acks []ackRec
	syncs := 0
	leader := 0
	if err := tr.nodes[0].SyncLeader(tr.ctx()); err == nil {
		syncs++
	}
	check := func(step int, what string) {
		t.Helper()
		// Agreement on committed entries.
		at := map[uint64]Entry{}
		for i := range 3 {
			n := tr.nodes[i]
			c := n.Commit()
			for _, e := range n.After(0) {
				if e.Seq > c {
					break
				}
				if o, ok := at[e.Seq]; ok && !o.same(e) {
					t.Fatalf("seed %d step %d (%s): n%d committed %+v at seq %d, another replica %+v", seed, step, what, i, e, e.Seq, o)
				}
				at[e.Seq] = e
			}
		}
		// Every answered write where any committed log reaches it.
		for _, a := range acks {
			e, ok := at[a.seq]
			if !ok {
				continue
			}
			if len(e.IDs) != 1 || e.IDs[0] != a.id {
				t.Fatalf("seed %d step %d (%s): write %s answered at seq %d, committed there: %+v", seed, step, what, a.id, a.seq, e)
			}
		}
		// Kept on a majority: every write answered as committed is held
		// at its seq by synced replicas, plus unsynced ones standing in for
		// a copy they may have lost, at least two of three. A lost log
		// moves one from the first count to the second; a committed copy
		// overwritten, a peer counted that never stored it, or a replica
		// marked synced without it drops the sum at once (#1050 review).
		var holds [3]map[uint64]string
		unsynced := 0
		for i := range 3 {
			holds[i] = map[uint64]string{}
			for _, e := range tr.nodes[i].After(0) {
				if len(e.IDs) == 1 {
					holds[i][e.Seq] = e.IDs[0]
				}
			}
			if !tr.nodes[i].Synced() {
				unsynced++
			}
		}
		for _, a := range acks {
			kept := unsynced
			for i := range 3 {
				if tr.nodes[i].Synced() && holds[i][a.seq] == a.id {
					kept++
				}
			}
			if kept < 2 {
				t.Fatalf("seed %d step %d (%s): write %s answered at seq %d is held by %d synced replicas, with %d unsynced: no longer on a majority", seed, step, what, a.id, a.seq, kept-unsynced, unsynced)
			}
		}
	}
	synced := func() bool {
		for i := range 3 {
			if !tr.nodes[i].Synced() {
				return false
			}
		}
		return true
	}
	lateUntil := -1
	for step := range steps {
		if step > lateUntil {
			tr.net.mu.Lock()
			tr.net.delay = [3]bool{}
			tr.net.mu.Unlock()
		}
		var what string
		switch k := r.IntN(100); {
		case k < 35:
			i := r.IntN(3)
			id := fmt.Sprintf("s%d", step)
			e, err := tr.propose(i, id)
			what = fmt.Sprintf("propose %s at n%d: %v", id, i, err)
			if err == nil {
				acks = append(acks, ackRec{id, e.Seq})
			}
		case k < 47:
			i := r.IntN(3)
			v := r.IntN(2) == 0
			tr.setDown(i, v)
			what = fmt.Sprintf("n%d down=%v", i, v)
		case k < 51:
			i := r.IntN(3)
			v := r.IntN(3) == 0
			tr.net.mu.Lock()
			tr.net.dropReply[i] = v
			tr.net.mu.Unlock()
			what = fmt.Sprintf("n%d drops answers=%v", i, v)
		case k < 54:
			// Late for the next step only: one write, catch-up or
			// heartbeat whose requests to it land after the sender
			// stopped waiting, maybe after a new leader.
			i := r.IntN(3)
			tr.net.mu.Lock()
			tr.net.delay[i] = true
			tr.net.mu.Unlock()
			lateUntil = step + 1
			what = fmt.Sprintf("n%d answers late for a step", i)
		case k < 57:
			// Lost commit pushes and heartbeats: a follower holds entries
			// it never learns were committed (#1050 review).
			i := r.IntN(3)
			v := r.IntN(2) == 0
			tr.net.mu.Lock()
			tr.net.dropPush[i] = v
			tr.net.mu.Unlock()
			what = fmt.Sprintf("n%d loses pushes=%v", i, v)
		case k < 60:
			i, j := r.IntN(3), r.IntN(3)
			v := r.IntN(2) == 0
			tr.net.mu.Lock()
			tr.net.cut[i][j] = v
			tr.net.mu.Unlock()
			what = fmt.Sprintf("link n%d→n%d cut=%v", i, j, v)
		case k < 64:
			i := r.IntN(3)
			tr.restart(i)
			what = fmt.Sprintf("restart n%d", i)
		case k < 68:
			if !synced() {
				continue
			}
			i := r.IntN(3)
			if r.IntN(2) == 0 {
				tr.loseLog(i)
				what = fmt.Sprintf("n%d lost its log", i)
			} else {
				tr.wipe(i)
				what = fmt.Sprintf("n%d rebuilt", i)
			}
		case k < 82:
			// A new leader catches up at once only half the time: its
			// heartbeats, and its followers' pulls, may come first (#1050
			// review), as driveLog's ticker can.
			leader = r.IntN(3)
			late := r.IntN(2) == 0
			if late {
				// Most often one holding entries past its commit index:
				// an earlier term's leftovers, which only its catch-up may
				// sort out.
				for _, i := range r.Perm(3) {
					if tr.nodes[i].LastSeq() > tr.nodes[i].Commit() && r.IntN(4) != 0 {
						leader = i
						break
					}
				}
			}
			term := tr.lead(leader)
			if late {
				what = fmt.Sprintf("n%d leads term %d, not caught up yet", leader, term)
				break
			}
			err := tr.nodes[leader].SyncLeader(tr.ctx())
			what = fmt.Sprintf("n%d leads term %d: sync %v", leader, term, err)
			if err == nil {
				syncs++
				n := tr.nodes[leader]
				for _, a := range acks {
					if seq, _ := tr.holds(leader, a.id); seq != a.seq || n.Commit() < a.seq {
						t.Fatalf("seed %d step %d: n%d synced as leader without write %s (answered at seq %d; holds it at %d, commit %d)", seed, step, leader, a.id, a.seq, seq, n.Commit())
					}
				}
			}
		default:
			tr.nodes[leader].Heartbeat(tr.ctx())
			for i := range 3 {
				if i != leader {
					_ = tr.nodes[i].CatchUp(tr.ctx())
				}
				tr.nodes[i].Ping(tr.ctx())
			}
			what = "heartbeat, catch-up, ping"
		}
		trace("%d %s", step, what)
		check(step, what)
	}
	return len(acks), syncs
}
