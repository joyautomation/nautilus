package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
)

// The bad-retain hand-back end to end (#710 S2c-3, pairhandback.go). Each
// rig keeps every takeover cold (hot-max-age 0), the hand-back's case, and
// gives each member its own retain file.

func newHandBackRig(t *testing.T) (*pairRig, map[string]string) {
	t.Helper()
	dirs := map[string]string{"a": t.TempDir(), "b": t.TempDir()}
	r := newHotPairRig(t, func(n string, p *project.Project) {
		p.Retain.File = filepath.Join(dirs[n], "retain.json")
		cold := project.Duration(0)
		p.Redundancy.HotMaxAge = &cold
	})
	t.Cleanup(func() { setTestHandBackAsk(nil) })
	return r, dirs
}

// corrupt spoils member n's retain file. stuck: and keeps it unreadable
// (the directory read-only, so the read cannot move it aside).
func corrupt(t *testing.T, dirs map[string]string, n string, stuck bool) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dirs[n], "retain.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if stuck {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		os.Chmod(dirs[n], 0o500)
		t.Cleanup(func() { os.Chmod(dirs[n], 0o700) })
	}
}

// healthyOnAsk makes the asker's hand-back question first set the peer's
// health flag true: the peer lost its lease by an unhealthy spell, and
// is well again by the time it is asked.
func healthyOnAsk(ok *atomic.Bool) {
	setTestHandBackAsk(func(*pairStream) { ok.Store(true) })
}

// wonAfter waits until member n wins a lease in an epoch after before
// (its gate may close again at once, on a hand-back).
func (r *pairRig) wonAfter(t *testing.T, n string, before uint64) {
	t.Helper()
	pairWait(t, n+" to win the lease", 10*time.Second, func() bool { return r.nodes[n].m.q.Node().Term() > before })
}

func (r *pairRig) journalled(t *testing.T, jr *recJournal, action string) map[string]any {
	t.Helper()
	var d map[string]any
	pairWait(t, action+" in the journal", 5*time.Second, func() bool {
		for _, e := range jr.entries() {
			if e.Action == action {
				d = e.Detail
				return true
			}
		}
		return false
	})
	return d
}

// A's file corrupt, B's good: A wins the lease, asks B, and hands the
// lead back; B leads on its own file, with a condition that says so and
// its file's place, and the journal records both sides. The whole
// hand-back takes about a lease and a bit (the guide's 1.3 leases).
// handback_ineligible_peer cannot pass it (B is well when asked);
// handback_taken_unalarmed turns it red.
func TestPairHandBackToAGoodPeer(t *testing.T) {
	r, dirs := newHandBackRig(t)
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	r.set(t, lead, "SP", 41.5)
	r.imaged(t, lead)
	jr := &recJournal{}
	r.nodes[stand].m.SetJournal(jr)
	corrupt(t, dirs, stand, false)
	ok := r.healthy(lead)
	healthyOnAsk(ok)
	before := r.nodes[stand].m.q.Node().Term()
	ok.Store(false) // the leader's lease lapses; the standby wins
	r.wonAfter(t, stand, before)
	won := time.Now()
	d := r.journalled(t, jr, "redundancy.handback")
	if d["to"] != lead {
		t.Fatalf("the hand-back's journal entry: %v", d)
	}
	pairWait(t, lead+" to lead again", 10*time.Second, func() bool { return r.nodes[lead].m.q.IsLeader() && r.nodes[lead].rt.Leading() })
	took := time.Since(won)
	t.Logf("hand-back: %s from %s's election to %s leading (lease 500ms)", took.Round(time.Millisecond), stand, lead)
	if took > 3*500*time.Millisecond {
		t.Errorf("the hand-back took %s, want about 1.3 leases", took)
	}
	if r.nodes[stand].rt.Leading() {
		t.Fatalf("%s's runtime took over although it handed the lead back", stand)
	}
	if got := r.get(lead, "SP"); got != "41.5" {
		t.Errorf("SP on %s = %s, want its own file's 41.5", lead, got)
	}
	pairWait(t, "the hand-back condition on "+lead, 5*time.Second, func() bool { return r.hasWarning(lead, "took the lead from "+stand+"'s hand-back") })
	if !r.hasWarning(lead, "at term ") {
		t.Error("the condition does not give the file's place")
	}
	if rep := r.nodes[stand].rt.LastTakeover(); !rep.At.IsZero() {
		t.Errorf("%s took over (%+v) on the edge it handed back", stand, rep)
	}
	if rep := r.nodes[lead].rt.LastTakeover(); rep.NoHandBack != "" {
		t.Errorf("%s, its store readable, asked for a hand-back: %q", lead, rep.NoHandBack)
	}
}

// The peer is unhealthy when asked: it could not keep the lead, so no
// hand-back; and a peer whose retain file is gone (none, or kept aside as
// corrupt: it reads as nothing) has no file to lead on.
// handback_ineligible_peer and handback_empty_file_good turn it red.
func TestPairNoHandBackToAPeerThatCannotLeadOnItsFile(t *testing.T) {
	for _, c := range []struct{ name, want string }{
		{"unhealthy", "unhealthy"},
		{"no file", "holds no place in the state stream"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, dirs := newHandBackRig(t)
			lead := r.leader()
			stand := other(lead)
			r.inSync(t, stand)
			r.set(t, lead, "SP", 41.5)
			r.imaged(t, lead)
			corrupt(t, dirs, stand, false)
			ok := r.healthy(lead)
			if c.name == "no file" {
				healthyOnAsk(ok)
				if err := os.Remove(filepath.Join(dirs[lead], "retain.json")); err != nil {
					t.Fatal(err)
				}
			}
			ok.Store(false)
			pairWait(t, stand+" to take over", 10*time.Second, func() bool { return r.nodes[stand].rt.Leading() })
			if rep := r.nodes[stand].rt.LastTakeover(); !strings.Contains(rep.NoHandBack, c.want) {
				t.Fatalf("no hand-back because %q, want %q", rep.NoHandBack, c.want)
			}
		})
	}
}

// Both files bad: A asks, B says no (its own store unreadable), and A
// runs on initial values, alarmed with the reason, and stays leader: no
// ping-pong. handback_peer_file_unjudged cannot pass (B's read fails).
func TestPairHandBackRefusedWhenBothFilesAreBad(t *testing.T) {
	r, dirs := newHandBackRig(t)
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	r.set(t, lead, "SP", 41.5)
	r.imaged(t, lead)
	corrupt(t, dirs, stand, false)
	corrupt(t, dirs, lead, true)
	ok := r.healthy(lead)
	healthyOnAsk(ok)
	ok.Store(false)
	pairWait(t, stand+" to take over", 10*time.Second, func() bool { return r.nodes[stand].rt.Leading() })
	rep := r.nodes[stand].rt.LastTakeover()
	if !strings.Contains(rep.NoHandBack, "could not be read either") {
		t.Fatalf("no hand-back because %q, want the peer's store unreadable", rep.NoHandBack)
	}
	if got := r.get(stand, "SP"); got != "0" {
		t.Errorf("SP on %s = %s, want the initial 0", stand, got)
	}
	time.Sleep(4 * 500 * time.Millisecond) // past a hold: still the leader
	if !r.nodes[stand].rt.Leading() || r.nodes[lead].rt.Leading() {
		t.Fatal("the lead moved after a refused hand-back")
	}
}

// B dead: the question is refused at once. A peer port that accepts and
// never answers costs the deadline (a quarter lease here, 125 ms), not
// more: from the question to the takeover it takes at most that much
// longer than a refused one (the rest of the edge, live inputs and the
// log's catch-up, is the same in both). handback_long_deadline turns it
// red.
func TestPairHandBackToASilentPeer(t *testing.T) {
	var took [2]time.Duration
	for i, hang := range []bool{false, true} {
		name := "refused"
		if hang {
			name = "hangs"
		}
		t.Run(name, func(t *testing.T) {
			r, dirs := newHandBackRig(t)
			lead := r.leader()
			stand := other(lead)
			r.inSync(t, stand)
			corrupt(t, dirs, stand, false)
			var asked atomic.Int64
			setTestHandBackAsk(func(*pairStream) { asked.Store(time.Now().UnixNano()) })
			r.nodes[lead].crash()
			if hang {
				ln, err := net.Listen("tcp", r.peer[lead])
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { ln.Close() })
				go func() {
					for {
						c, err := ln.Accept()
						if err != nil {
							return
						}
						t.Cleanup(func() { c.Close() })
					}
				}()
			}
			pairWait(t, stand+" to take over", 10*time.Second, func() bool { return r.nodes[stand].rt.Leading() })
			rep := r.nodes[stand].rt.LastTakeover()
			if asked.Load() == 0 {
				t.Fatal("the peer was never asked")
			}
			took[i] = rep.At.Sub(time.Unix(0, asked.Load()))
			t.Logf("%s: the takeover ran %s after the question", name, took[i].Round(time.Millisecond))
			if !strings.Contains(rep.NoHandBack, "did not answer") {
				t.Errorf("no hand-back because %q, want no answer", rep.NoHandBack)
			}
		})
	}
	if d := took[1] - took[0]; d > 400*time.Millisecond {
		t.Errorf("a peer that never answers cost %s more than a refused one, want the 125 ms deadline", d.Round(time.Millisecond))
	}
}

// A's store stays unreadable and it keeps coming back to the lead: it
// hands back once, then, within the guard's window and across its own
// restart (the guard is in its voter state), runs on initial values.
// The second time, B is well when asked but never asked.
// Then the first leader, its file bad too, is refused by the controller
// that handed back to it: A to B to A, no further.
// handback_guard_volatile, handback_guard_off, handback_peer_guard_off
// and handback_restart_empty_trusted turn it red.
func TestPairHandBackOnceInACrashLoop(t *testing.T) {
	r, dirs := newHandBackRig(t)
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	r.set(t, lead, "SP", 41.5)
	r.imaged(t, lead)
	corrupt(t, dirs, stand, false) // the first read keeps it aside: later reads find nothing
	ok := r.healthy(lead)
	healthyOnAsk(ok)
	before := r.nodes[stand].m.q.Node().Term()
	ok.Store(false)
	r.wonAfter(t, stand, before)
	pairWait(t, lead+" to lead again after the hand-back", 15*time.Second, func() bool {
		return r.nodes[lead].rt.Leading() && r.nodes[lead].m.q.IsLeader()
	})
	if held, _ := r.nodes[stand].m.q.Node().HandBackHeld(); !held {
		t.Fatal("no hand-back recorded")
	}
	// The restarted standby takes no image before it takes over: its
	// emptied file is all it has.
	r.nodes[lead].m.stream.hold.Store(true)
	r.nodes[stand].crash()
	r.restart(stand)
	pairWait(t, stand+" back as standby", 15*time.Second, func() bool {
		s := r.streamStatus(stand)
		return s != nil && s.Role == "standby"
	})
	ok.Store(false)
	pairWait(t, stand+" to take over", 15*time.Second, func() bool { return r.nodes[stand].rt.Leading() })
	rep := r.nodes[stand].rt.LastTakeover()
	if !strings.Contains(rep.NoHandBack, "handed the lead back") {
		t.Fatalf("no second hand-back because %q, want the guard", rep.NoHandBack)
	}
	// The emptied file is not taken for a good one (Fable M1): the
	// takeover counts the store as failed, so it streams nothing over the
	// peer's good file.
	if !r.nodes[stand].rt.RetainLoadFailed() {
		t.Fatal("the takeover over the emptied file does not count it as failed")
	}
	if b, _ := os.ReadFile(filepath.Join(dirs[lead], "retain.json")); !strings.Contains(string(b), "41.5") {
		t.Fatalf("%s's good file lost its 41.5: %s", lead, b)
	}
	// And no ping-pong the other way: the first leader, its own file now
	// unreadable too, asks; the controller that handed back to it within
	// the window says no, though well when asked.
	corrupt(t, dirs, lead, false)
	okS := r.healthy(stand)
	healthyOnAsk(okS)
	ok.Store(true)
	okS.Store(false)
	pairWait(t, lead+" to take over", 15*time.Second, func() bool { return r.nodes[lead].rt.Leading() })
	if rep := r.nodes[lead].rt.LastTakeover(); !strings.Contains(rep.NoHandBack, "handed the lead back itself") {
		t.Fatalf("no hand-back because %q, want the peer's own guard", rep.NoHandBack)
	}
}

// naut-lead's case for the earlier-boot records: A led a later epoch
// than B's file while B's file stood still, then restarted with its file
// corrupt and won the lease again. Its voters' records of its EARLIER
// boot set the floor, so B's older file is refused, and A runs on
// initial values rather than hand the plant to a file without A's last
// leadership. handback_own_record_ignored turns it red.
func TestPairNoHandBackToAFileOlderThanOwnEarlierLeadership(t *testing.T) {
	r, dirs := newHandBackRig(t)
	b := r.leader()
	a := other(b)
	r.inSync(t, a)
	r.set(t, b, "SP", 10.0)
	r.imaged(t, b)
	// A leads next; B's file stands still from here (its carries held).
	r.nodes[b].m.stream.persistHold.Store(true)
	okB := r.healthy(b)
	okB.Store(false)
	r.waitLeads(t, a)
	okB.Store(true)
	r.set(t, a, "SP", 77.0)
	time.Sleep(1500 * time.Millisecond) // A's own save, at its epoch
	// A goes down; with B held unhealthy, A's next boot wins the lease.
	okB.Store(false)
	r.nodes[a].crash()
	corrupt(t, dirs, a, false)
	healthyOnAsk(okB)
	r.restart(a)
	pairWait(t, a+" to take over", 20*time.Second, func() bool { return r.nodes[a].rt.Leading() })
	rep := r.nodes[a].rt.LastTakeover()
	if !strings.Contains(rep.NoHandBack, "before the newest leadership") {
		t.Fatalf("no hand-back because %q, want B's file older than A's earlier leadership", rep.NoHandBack)
	}
	if r.nodes[b].rt.Leading() {
		t.Fatal("B leads on its older file")
	}
}

// The peer said yes, then never took the lead (it went unhealthy right
// after its answer): A waits out its hold (3 leases) before it campaigns
// again, wins, and this time takes over cold, on initial values, under
// the guard. The second election comes about 3.3 leases after the first;
// the takeover then pays what any takeover on an unreadable store pays.
// handback_keeps_renewing and handback_store_bad_forgotten turn it red.
func TestPairHandBackFallsBackWhenThePeerNeverTakes(t *testing.T) {
	r, dirs := newHandBackRig(t)
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	r.set(t, lead, "SP", 41.5)
	r.imaged(t, lead)
	corrupt(t, dirs, stand, false)
	ok := r.healthy(lead)
	setTestHandBackAsk(func(*pairStream) {
		ok.Store(true)
		go func() { time.Sleep(50 * time.Millisecond); ok.Store(false) }()
	})
	before := r.nodes[stand].m.q.Node().Term()
	ok.Store(false)
	r.wonAfter(t, stand, before)
	won := time.Now()
	first := r.nodes[stand].m.q.Node().Term()
	r.wonAfter(t, stand, first)
	again := time.Since(won)
	t.Logf("fallback: elected again %s after the first election (lease 500ms)", again.Round(time.Millisecond))
	if again < 3*500*time.Millisecond || again > 4500*time.Millisecond {
		t.Errorf("elected again %s after the first, want after the 3-lease hold and within about 3.5 leases", again)
	}
	pairWait(t, stand+" to take over", 10*time.Second, func() bool { return r.nodes[stand].rt.Leading() })
	if r.nodes[lead].rt.Leading() {
		t.Fatal("the peer leads")
	}
	if rep := r.nodes[stand].rt.LastTakeover(); !strings.Contains(rep.NoHandBack, "handed the lead back") {
		t.Errorf("no second hand-back because %q, want the guard", rep.NoHandBack)
	}
	// The first edge's failed read kept the file aside; the second edge
	// reads nothing, and still counts the store as failed (Fable M1).
	if !r.nodes[stand].rt.RetainLoadFailed() {
		t.Error("the fallback takeover does not count the store as failed")
	}
	time.Sleep(time.Second)
	if b, _ := os.ReadFile(filepath.Join(dirs[lead], "retain.json")); !strings.Contains(string(b), "41.5") {
		t.Fatalf("%s's good file lost its 41.5: %s", lead, b)
	}
}

// A controller that resigned the lead (a switchover's RESIGN) and is then
// asked by its peer to take it back (the peer's takeover would go cold on
// an unreadable retain file) ends its own resign hold when it says yes,
// and leads again without waiting the hold's two leases out (#710 d1d).
// handback_keeps_resign_hold turns it red.
func TestPairHandBackEndsTheResignersHold(t *testing.T) {
	r, dirs := newHandBackRig(t)
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	r.set(t, lead, "SP", 41.5)
	r.imaged(t, lead)
	corrupt(t, dirs, stand, false)
	before := r.nodes[stand].m.q.Node().Term()
	resigned := time.Now()
	rep, err := r.nodes[lead].m.q.Resign(context.Background(), 1, false)
	if err != nil || !rep.Resigned || !rep.Peer {
		t.Fatalf("%s's resign: %+v %v", lead, rep, err)
	}
	r.wonAfter(t, stand, before)
	// Once it has said yes to the hand-back, its hold is over: ending it
	// now ends nothing.
	st := r.nodes[lead].m.stream
	pairWait(t, lead+" to say yes to the hand-back", 10*time.Second, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.offer != nil
	})
	if r.nodes[lead].m.q.Node().EndResignHold() {
		t.Fatalf("%s still held its resign hold after saying yes to its peer's hand-back", lead)
	}
	pairWait(t, lead+" to lead again", 10*time.Second, func() bool { return r.nodes[lead].m.q.IsLeader() && r.nodes[lead].rt.Leading() })
	t.Logf("resign to %s leading again on %s's hand-back: %s (its resign hold: 2 leases, 1s)", lead, stand, time.Since(resigned).Round(time.Millisecond))
	if got := r.get(lead, "SP"); got != "41.5" {
		t.Errorf("SP on %s = %s, want its own file's 41.5", lead, got)
	}
}

// A hand-back ask of the leadership a controller resigned (or an earlier
// one) is stale: the resigner may still say yes, but its resign hold
// stays. An ask of the peer's new leadership ends it (#710 d1d, Fable).
// handback_any_epoch turns it red.
func TestPairAStaleHandBackKeepsTheResignHold(t *testing.T) {
	r, _ := newHandBackRig(t)
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	r.set(t, lead, "SP", 41.5)
	r.imaged(t, lead)
	resigned := r.nodes[lead].m.q.Term()
	before := r.nodes[stand].m.q.Node().Term()
	if rep, err := r.nodes[lead].m.q.Resign(context.Background(), 1, false); err != nil || !rep.Resigned {
		t.Fatalf("%s's resign: %+v %v", lead, rep, err)
	}
	r.wonAfter(t, stand, before)
	ask := func(epoch uint64) handBackAnswer {
		b, _ := json.Marshal(handBackAsk{Epoch: epoch})
		rec := httptest.NewRecorder()
		r.nodes[lead].m.stream.serveHandBack(rec, httptest.NewRequest(http.MethodPost, handBackPath(r.cluster()), bytes.NewReader(b)))
		var ans handBackAnswer
		if err := json.NewDecoder(rec.Body).Decode(&ans); err != nil {
			t.Fatal(err)
		}
		return ans
	}
	ask(resigned)
	if held, epoch := r.nodes[lead].m.q.Node().InResignHold(); !held || epoch != resigned {
		t.Fatalf("a hand-back ask of the resigned leadership %d ended the hold (held %v, resigned %d)", resigned, held, epoch)
	}
	ask(r.nodes[stand].m.q.Node().Term())
	if held, _ := r.nodes[lead].m.q.Node().InResignHold(); held {
		t.Fatal("a hand-back ask of the peer's new leadership left the hold")
	}
}
