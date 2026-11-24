package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// A planned switchover end to end (#710 d1c, pairswitch.go).

// switchAnswerOf is POST /api/cluster/switchover's answer.
type switchAnswerOf struct {
	Outcome string       `json:"outcome"`
	Error   string       `json:"error"`
	Report  switchReport `json:"report"`
}

func (r *pairRig) switchover(t *testing.T, n string) (int, switchAnswerOf) {
	t.Helper()
	code, body := r.authed(n, http.MethodPost, "/api/cluster/switchover", "{}")
	var ans switchAnswerOf
	if err := json.Unmarshal([]byte(body), &ans); err != nil {
		t.Fatalf("switchover on %s: %d %s", n, code, body)
	}
	return code, ans
}

// newSwitchRig is the hot pair rig with an API token, which the pair's
// operator commands need.
func newSwitchRig(t *testing.T) *pairRig {
	t.Helper()
	testPairAPIToken = "api-secret"
	t.Cleanup(func() { testPairAPIToken = "" })
	return newHotPairRig(t, nil)
}

// readySwitchover waits for the standby to be current and asks for a
// switchover, again (at most three times) when it is refused as not
// current: under load the standby's acknowledgement can fall a moment
// behind between the wait and the ask (CI flake, #710 d1d).
func (r *pairRig) readySwitchover(t *testing.T, lead string) (int, switchAnswerOf) {
	t.Helper()
	var code int
	var ans switchAnswerOf
	for range 3 {
		r.switchReady(t, lead)
		// PEER_READY is refreshed on the stream's own tick; the switchover
		// asks the live acknowledgement rule. Just after an abort the flag
		// can still say ready while the rule says not current: wait for
		// the rule itself (the stress run on #710 S3: 3 in 26 under -race
		// on 2 contended cores, all three tries in the same second).
		pairWait(t, "the standby to be current by the live rule", 10*time.Second, func() bool {
			term, leading := r.nodes[lead].rt.LeadingTerm()
			if !leading {
				return true // the switchover will say why
			}
			_, cur := r.nodes[lead].m.stream.syncMark(term)
			return cur
		})
		code, ans = r.switchover(t, lead)
		if ans.Outcome != "refused" || !strings.Contains(ans.Error, "not current") {
			break
		}
	}
	return code, ans
}

// waitNewLeader waits for n to lead after a switchover, and on a timeout
// says what each controller's election, runtime and stream held: a
// switchover answered done whose standby never led (CI run 45402792635)
// left nothing to read in the rig job's log (#710 d1d).
func (r *pairRig) waitNewLeader(t *testing.T, n string, d time.Duration) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if r.nodes[n].m.q.IsLeader() && r.nodes[n].rt.Leading() {
			return
		}
	}
	var b strings.Builder
	for _, m := range []string{"a", "b"} {
		nd := r.nodes[m]
		term, lead := nd.rt.LeadingTerm()
		held, resigned := nd.m.q.Node().InResignHold()
		fmt.Fprintf(&b, "\n%s: election %+v\n   gate %v term %d; runtime leads %v in %d, switching %d; resign hold %v (resigned %d); last takeover %+v; stream %+v",
			m, nd.m.q.Node().Status(), nd.m.q.IsLeader(), nd.m.q.Term(), lead, term, nd.rt.Switching(), held, resigned, nd.rt.LastTakeover(), r.streamStatus(m))
	}
	t.Fatalf("%s never led within %s after the switchover:%s", n, d, b.String())
}

// switchReady waits until the leader's standby is current, as the
// switchover requires, and the voters have heard so.
func (r *pairRig) switchReady(t *testing.T, lead string) {
	t.Helper()
	r.inSync(t, other(lead))
	pairWait(t, "the standby to be current (PEER_READY)", 10*time.Second, func() bool { return r.nodes[lead].rt.PeerReady() })
	r.shadowed(t, lead)
}

// The switchover hands the lead to the standby hot, well before a lease
// would have run out, and the new leader goes on from the old one's last
// scans: Mode (an operator's, not retained) and the scan counter, never
// below where the old leader stopped. The old leader stops scanning at
// once and follows its peer. final_image_unsent, capture_ignores_switching
// and final_ack_any_seq turn it red (each in its own way: a seam, a
// dropped stream, or a resign on an image the standby never took).
func TestPairSwitchoverIsHot(t *testing.T) {
	r := newSwitchRig(t)
	lead := r.leader()
	stand := other(lead)
	r.set(t, lead, "Mode", 5)
	pairWait(t, "a counter under way", 5*time.Second, func() bool { return r.count(lead) >= 10 })
	r.switchReady(t, lead)
	term := r.nodes[lead].m.q.Term()

	t0 := time.Now()
	code, ans := r.readySwitchover(t, lead)
	if code != http.StatusOK || ans.Outcome != "done" {
		t.Fatalf("switchover: %d %+v", code, ans)
	}
	if !ans.Report.Witness || !ans.Report.Peer || ans.Report.FinalSeq == 0 || ans.Report.Epoch != term {
		t.Fatalf("report %+v: want both voters took the RESIGN of term %d, with the final image's seq", ans.Report, term)
	}
	stopped := r.count(lead) // the old leader scans no more
	r.waitNewLeader(t, stand, 5*time.Second)
	took := time.Since(t0)
	lease := r.nodes[lead].m.o.Lease
	if took >= lease {
		t.Errorf("the standby led %s after the command, no faster than the lease (%s) running out", took.Round(time.Millisecond), lease)
	}
	t.Logf("command to the new leader leading: %s (stop %.1f ms, image+ack %.1f ms, resign %.1f ms)", took.Round(time.Millisecond), ans.Report.StopMs, ans.Report.ImageMs, ans.Report.ResignMs)
	pairWait(t, "the new leader's takeover report", 5*time.Second, func() bool {
		s := r.streamStatus(stand)
		return s != nil && s.Takeover != ""
	})
	if s := r.streamStatus(stand); s.Takeover != "hot" {
		t.Fatalf("takeover %q (%s), want hot", s.Takeover, s.TakeoverCold)
	}
	if got := r.get(stand, "Mode"); got != "5" {
		t.Errorf("Mode on the new leader = %s, want the operator's 5", got)
	}
	if got := r.count(stand); got < stopped {
		t.Errorf("Count on the new leader = %d, below the old leader's last %d: scans repeated", got, stopped)
	}
	if got := r.count(lead); got != stopped {
		t.Errorf("the old leader's Count moved from %d to %d after the switchover: it scanned on", stopped, got)
	}
	r.inSync(t, lead) // the old leader follows its peer
}

// A switchover is refused, saying why, with this controller leading on
// untouched: with a force held (asked on the standby, which forwards it:
// the leader's answer comes back), with a standby that is not current, or
// with one that may not lead. switch_ignores_forces and
// switch_unready_target turn it red.
func TestPairSwitchoverRefusals(t *testing.T) {
	r := newSwitchRig(t)
	lead := r.leader()
	stand := other(lead)
	r.switchReady(t, lead)
	term := r.nodes[lead].m.q.Term()
	refused := func(n, want string) {
		t.Helper()
		code, ans := r.switchover(t, n)
		if code != http.StatusConflict || ans.Outcome != "refused" || !strings.Contains(ans.Error, want) {
			t.Fatalf("switchover: %d %q %q, want 409 refused naming %q", code, ans.Outcome, ans.Error, want)
		}
		if got := r.leader(); got != lead || r.nodes[lead].m.q.Term() != term {
			t.Fatalf("a refused switchover moved the lead: %s in term %d", got, r.nodes[lead].m.q.Term())
		}
	}

	if err := r.nodes[lead].rt.Tags().Force("Mode", int64(1)); err != nil {
		t.Fatal(err)
	}
	refused(stand, "force")
	r.nodes[lead].rt.Tags().Unforce("Mode")

	h := r.healthy(stand)
	h.Store(false)
	refused(lead, "unhealthy")
	h.Store(true)

	r.nodes[lead].m.stream.hold.Store(true) // the leader sends no image: its standby falls behind
	pairWait(t, "the standby to fall behind", 10*time.Second, func() bool { return !r.nodes[lead].rt.PeerReady() })
	refused(lead, "not current")
	r.nodes[lead].m.stream.hold.Store(false)
}

// A standby whose field I/O is not healthy (a driver in error) refuses
// the switchover. switch_ignores_standby_io turns it red.
func TestPairSwitchoverRefusedForAStandbyWithBadIO(t *testing.T) {
	r := newSwitchRig(t)
	lead := r.leader()
	stand := other(lead)
	r.switchReady(t, lead)
	r.nodes[stand].m.setIOStatus(func() []server.DriverStatus {
		return []server.DriverStatus{{Kind: "modbus", Name: "pump1", State: "error"}}
	})
	code, ans := r.switchover(t, lead)
	if code != http.StatusConflict || ans.Outcome != "refused" || !strings.Contains(ans.Error, "modbus pump1 is error") {
		t.Fatalf("switchover: %d %q %q, want 409 refused naming the driver", code, ans.Outcome, ans.Error)
	}
	if got := r.leader(); got != lead {
		t.Fatalf("a refused switchover moved the lead to %s", got)
	}
}

// A standby that does not acknowledge the final image in a quarter lease
// aborts the switchover: the leader leads on in the same term, scanning
// and taking writes, and its stream goes on (the standby is current
// again). abort_keeps_switching and abort_keeps_final turn it red.
func TestPairSwitchoverAbortsWithoutTheFinalAck(t *testing.T) {
	r := newSwitchRig(t)
	lead := r.leader()
	r.switchReady(t, lead)
	term := r.nodes[lead].m.q.Term()
	r.nodes[lead].m.stream.finalAckHold.Store(true)
	code, ans := r.switchover(t, lead)
	r.nodes[lead].m.stream.finalAckHold.Store(false)
	if code != http.StatusConflict || ans.Outcome != "aborted" || !strings.Contains(ans.Error, "did not acknowledge the final image") {
		t.Fatalf("switchover: %d %q %q, want 409 aborted for the missing acknowledgement", code, ans.Outcome, ans.Error)
	}
	if got := r.leader(); got != lead || r.nodes[lead].m.q.Term() != term {
		t.Fatalf("an aborted switchover moved the lead: %s in term %d, want %s in %d", got, r.nodes[lead].m.q.Term(), lead, term)
	}
	before := r.count(lead)
	pairWait(t, "the scans to go on", 5*time.Second, func() bool { return r.count(lead) > before+2 })
	if errs := r.nodes[lead].rt.Tags().Write([]runtime.WriteOp{{Path: "Mode", Value: 7}}); errs[0] != nil {
		t.Fatalf("a write after the abort: %v", errs[0])
	}
	r.shadowed(t, lead) // the stream goes on

	// With the scans stopped no end-of-scan copy comes: the stream's
	// capture yields to the final image at once rather than waiting out
	// its capture window and dropping the standby's connection.
	st := r.nodes[lead].m.stream
	if err := r.nodes[lead].rt.BeginSwitch(context.Background(), term); err != nil {
		t.Fatal(err)
	}
	var img runtime.Image
	t0 := time.Now()
	got := st.capture(context.Background(), term, &img)
	r.nodes[lead].rt.EndSwitch()
	if got != captureSwitching || time.Since(t0) > streamCaptureWait/2 {
		t.Fatalf("capture while switching: %d after %s, want captureSwitching at once", got, time.Since(t0).Round(time.Millisecond))
	}

	// The switching state ended before the resign (here an abort's
	// EndSwitch; in the field a new leadership's takeover): the last look
	// before the resign aborts rather than resigning a leadership whose
	// scans run (Fable M1, re-check).
	ended := func() { r.nodes[lead].rt.EndSwitch() }
	st.beforeResign.Store(&ended)
	code, ans = r.readySwitchover(t, lead)
	st.beforeResign.Store(nil)
	if code != http.StatusConflict || ans.Outcome != "aborted" || !strings.Contains(ans.Error, "ended before the resign") {
		t.Fatalf("a switchover whose switching state ended before the resign: %d %q %q, want aborted", code, ans.Outcome, ans.Error)
	}
	if got := r.leader(); got != lead {
		t.Fatalf("the lead moved to %s", got)
	}

	// A switchover after the abort, in the same leadership, goes through:
	// the aborted one's final image does not stop the next one's (the
	// bench: every switchover after an abort aborted).
	if code, ans := r.readySwitchover(t, lead); code != http.StatusOK || ans.Outcome != "done" {
		t.Fatalf("a switchover after the abort: %d %q %q", code, ans.Outcome, ans.Error)
	}
	r.waitNewLeader(t, other(lead), 15*time.Second)
}
