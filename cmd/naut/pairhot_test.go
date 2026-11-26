package main

import (
	"fmt"
	"github.com/joyautomation/nautilus/quorum"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
)

// hotPairProject is a pair whose state only a hot takeover keeps (#710
// S2b): a TON an operator starts (Go), a counter of scans (Count, a tag
// the program writes, not retained), an SFC whose step Fill lasts 4 s by
// its step time, and Mode, a tag only an operator writes, not retained.
func hotPairProject(a, b, w string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(fmt.Sprintf(`name: pairhot
retain: {}
tasks:
  - program: main.st
    scan: 50ms
  - name: chart
    program: chart.sfc
    scan: 50ms
tags:
  - { name: SP, role: setpoint, init: 0.0 }
  - { name: Mode, role: state, init: 0 }
  - { name: Go, role: state, init: false }
  - { name: TonDone, role: state, init: false }
  - { name: Count, role: state, init: 0 }
  - { name: StartReq, role: state, init: false }
  - { name: FillOn, role: state, init: false }
  - { name: Drained, role: state, init: false }
redundancy:
  experimental: true
  peers:
    a: %s
    b: %s
  witness: %s
  lease: 500ms
  hot-max-age: 10s
`, a, b, w))},
		"main.st":   &fstest.MapFile{Data: []byte(hotPairMain)},
		"chart.sfc": &fstest.MapFile{Data: []byte(hotPairChart)},
	}
}

const hotPairMain = `PROGRAM Main
VAR_EXTERNAL SP : REAL; Go : BOOL; TonDone : BOOL; Count : INT; END_VAR
VAR tmr : TON; cnt : CTU; tog : BOOL; END_VAR
tmr(IN := Go, PT := T#4s);
TonDone := tmr.Q;
tog := NOT tog;
cnt(CU := tog, R := FALSE, PV := 30000);
Count := cnt.CV;
END_PROGRAM
`

const hotPairChart = `PROGRAM Chart
VAR_EXTERNAL StartReq : BOOL; FillOn : BOOL; Drained : BOOL; END_VAR
SFC
  INITIAL_STEP Idle:
  END_STEP
  STEP Fill:
    N FillOn;
  END_STEP
  STEP Drain:
    S Drained;
  END_STEP
  TRANSITION FROM Idle TO Fill := StartReq;
  END_TRANSITION
  TRANSITION FROM Fill TO Drain := Fill.T >= T#4s;
  END_TRANSITION
END_SFC
END_PROGRAM
`

func newHotPairRig(t *testing.T, pmod func(n string, p *project.Project)) *pairRig {
	t.Helper()
	testPairFiles = hotPairProject
	t.Cleanup(func() { testPairFiles = nil })
	return newPairRigFull(t, pmod, nil)
}

// shadowed waits until the standby applied, in sync, an image the leader
// copied after now (its retain file advances only when the retained
// values change: imaged is for those).
func (r *pairRig) shadowed(t *testing.T, lead string) {
	t.Helper()
	after := r.streamStatus(lead).Seq
	pairWait(t, "the standby to apply the leader's next image", 5*time.Second, func() bool {
		s := r.streamStatus(other(lead))
		return s != nil && s.State == "in-sync" && afterNow(s.Seq, after)
	})
}

func (r *pairRig) set(t *testing.T, n, path string, v any) {
	t.Helper()
	if err := r.nodes[n].rt.Tags().SetPath(path, v); err != nil {
		t.Fatalf("%s on %s: %v", path, n, err)
	}
}

func (r *pairRig) get(n, path string) string {
	v, _ := r.nodes[n].rt.Tags().ReadPath(path)
	return fmt.Sprint(v)
}

func (r *pairRig) count(n string) int {
	var c int
	fmt.Sscan(r.get(n, "Count"), &c)
	return c
}

// T-S2b end to end (#710 S2b, design §4.2, test plan I10/F12): a hard
// kill of the leader mid-process. The new leader goes on from the image:
// the TON an operator started finishes 4 s after it started, not 4 s
// after the takeover; the SFC stays in its step and leaves it on its step
// time; the scan counter goes on; Mode, an operator's non-retained tag,
// survives. The takeover says hot, on status and in the journal.
func TestPairHotTakeover(t *testing.T) {
	r := newHotPairRig(t, nil)
	lead := r.leader()
	stand := other(lead)
	jr := &recJournal{}
	r.nodes[stand].m.SetJournal(jr)
	r.inSync(t, stand)
	r.set(t, lead, "Mode", 5)
	r.set(t, lead, "StartReq", true)
	r.set(t, lead, "Go", true)
	t0 := time.Now()
	pairWait(t, "the chart to enter Fill", 5*time.Second, func() bool { return r.get(lead, "FillOn") == "true" })
	r.set(t, lead, "StartReq", false)
	pairWait(t, "a counter well under way", 5*time.Second, func() bool { return r.count(lead) >= 15 })
	r.shadowed(t, lead)
	before := r.count(lead)
	if r.get(lead, "TonDone") == "true" || r.get(lead, "Drained") == "true" {
		t.Fatal("setup: the timer or the step finished before the kill; the test proves nothing")
	}
	r.nodes[lead].crash()
	if got := r.leader(); got != stand {
		t.Fatalf("after the kill: leader %q, want %s", got, stand)
	}
	pairWait(t, "the new leader's takeover report", 5*time.Second, func() bool {
		s := r.streamStatus(stand)
		return s != nil && s.Takeover != ""
	})
	if s := r.streamStatus(stand); s.Takeover != "hot" {
		t.Fatalf("takeover %q (%s), want hot", s.Takeover, s.TakeoverCold)
	}
	if got := r.get(stand, "Mode"); got != "5" {
		t.Errorf("Mode on the new leader = %s, want the operator's 5 (carried hot, though not retained)", got)
	}
	if got := r.get(stand, "FillOn"); got != "true" {
		t.Errorf("FillOn on the new leader = %s: the chart left step Fill", got)
	}
	if got := r.count(stand); got < before-10 {
		t.Errorf("Count on the new leader = %d, want it to go on from the old leader's %d", got, before)
	}
	pairWait(t, "the TON to finish on the new leader", 10*time.Second, func() bool { return r.get(stand, "TonDone") == "true" })
	ton := time.Since(t0)
	pairWait(t, "the chart to leave Fill on its step time", 10*time.Second, func() bool { return r.get(stand, "Drained") == "true" })
	step := time.Since(t0)
	// Started at t0 on the old leader; the takeover alone takes longer
	// than a lease, so a cold one would finish 4 s after it, not here.
	for what, d := range map[string]time.Duration{"the TON": ton, "step Fill": step} {
		if d < 3500*time.Millisecond || d > 5500*time.Millisecond {
			t.Errorf("%s finished %s after it started on the old leader, want about 4 s", what, d.Round(time.Millisecond))
		}
	}
	pairWait(t, "the takeover in the journal", 5*time.Second, func() bool {
		for _, e := range jr.entries() {
			if e.Action == "redundancy.takeover" && e.Detail["hot"] == true {
				return true
			}
		}
		return false
	})
	if r.hasWarning(stand, "takeover was cold") {
		t.Error("a hot takeover raised the cold-takeover warning")
	}
}

// A takeover on an image of another build is cold (#710 S2b): status, a
// warning and the journal say why, Mode (an operator's, not retained)
// reverts to its initial value, and the warning stays until a standby
// follows the new leader again (naut-lead's ruling), then clears. The
// leader's PEER_READY() is FALSE all along.
func TestPairColdTakeoverOnAnotherBuild(t *testing.T) {
	r := newHotPairRig(t, nil)
	lead := r.leader()
	stand := other(lead)
	r.nodes[stand].crash()
	streamBuildFor = func(self string) string {
		if self == stand {
			return "another-build"
		}
		return streamBuild()
	}
	t.Cleanup(func() { streamBuildFor = func(string) string { return streamBuild() } })
	r.restart(stand)
	jr := &recJournal{}
	r.nodes[stand].m.SetJournal(jr)
	r.inSync(t, stand)
	r.set(t, lead, "Mode", 5)
	r.shadowed(t, lead)
	// A standby on another build acknowledges nothing: a takeover would
	// be cold, so the leader's PEER_READY() stays FALSE (#710 (b);
	// ack_other_build turns it red), also right after it reconnects with
	// the ack its earlier boot made (ack_kept_across_reconnect).
	for end := time.Now().Add(1500 * time.Millisecond); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if r.nodes[lead].rt.PeerReady() {
			t.Fatal("PEER_READY TRUE with a standby on another build")
		}
	}
	r.nodes[lead].crash()
	if got := r.leader(); got != stand {
		t.Fatalf("after the kill: leader %q, want %s", got, stand)
	}
	pairWait(t, "the new leader's takeover report", 5*time.Second, func() bool {
		s := r.streamStatus(stand)
		return s != nil && s.Takeover != ""
	})
	if s := r.streamStatus(stand); s.Takeover != "cold" || !strings.Contains(s.TakeoverCold, "another nautilus build") {
		t.Fatalf("takeover %q (%s), want cold for another build", s.Takeover, s.TakeoverCold)
	}
	if got := r.get(stand, "Mode"); got != "0" {
		t.Errorf("Mode after a cold takeover = %s, want its initial 0 (not retained: documented)", got)
	}
	pairWait(t, "the cold-takeover warning", 5*time.Second, func() bool { return r.hasWarning(stand, "takeover was cold") })
	pairWait(t, "the cold takeover in the journal", 5*time.Second, func() bool {
		for _, e := range jr.entries() {
			if e.Action == "redundancy.takeover" && e.Detail["hot"] == false && strings.Contains(fmt.Sprint(e.Detail["reason"]), "another nautilus build") {
				return true
			}
		}
		return false
	})
	// Still there well after a minute's worth of the old one-shot timer
	// would matter: it waits for a standby, not a clock.
	time.Sleep(time.Second)
	if !r.hasWarning(stand, "takeover was cold") {
		t.Fatal("the cold-takeover warning cleared with no standby following")
	}
	r.restart(lead) // the old leader comes back and follows
	r.inSync(t, lead)
	pairWait(t, "the warning to clear once a standby follows", 10*time.Second, func() bool { return !r.hasWarning(stand, "takeover was cold") })
}

// The standby's own retain file is unreadable and cannot be written: its
// takeover still goes hot (#710 S2 Q3), from the image's retained values,
// and the new leader warns they are only in memory for as long as no save
// succeeds (naut-lead's ruling), clearing once one does.
func TestPairHotTakeoverOverAnUnreadableRetainFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	dirs := map[string]string{"a": t.TempDir(), "b": t.TempDir()}
	r := newHotPairRig(t, func(n string, p *project.Project) { p.Retain.File = filepath.Join(dirs[n], "retain.json") })
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	r.set(t, lead, "SP", 41.5)
	r.set(t, lead, "Mode", 5)
	r.imaged(t, lead)
	if err := os.WriteFile(filepath.Join(dirs[stand], "retain.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dirs[stand], 0o500)
	t.Cleanup(func() { os.Chmod(dirs[stand], 0o700) })
	// A hot takeover needs no file, so it never asks for a hand-back
	// (#710 S2c-3; handback_when_hot turns it red).
	var asked atomic.Bool
	setTestHandBackAsk(func(*pairStream) { asked.Store(true) })
	t.Cleanup(func() { setTestHandBackAsk(nil) })
	r.nodes[lead].crash()
	if got := r.leader(); got != stand {
		t.Fatalf("after the kill: leader %q, want %s", got, stand)
	}
	pairWait(t, "the new leader's takeover report", 5*time.Second, func() bool {
		s := r.streamStatus(stand)
		return s != nil && s.Takeover != ""
	})
	if s := r.streamStatus(stand); s.Takeover != "hot" || !s.RetainInMemory {
		t.Fatalf("takeover %q (%s), retain in memory %v; want hot, in memory", s.Takeover, s.TakeoverCold, s.RetainInMemory)
	}
	if got := r.get(stand, "SP"); got != "41.5" {
		t.Errorf("SP on the new leader = %s, want the image's 41.5", got)
	}
	if asked.Load() {
		t.Error("a hot takeover asked for a hand-back")
	}
	pairWait(t, "the in-memory warning", 5*time.Second, func() bool { return r.hasWarning(stand, "only in memory") })
	time.Sleep(3 * time.Second) // a save tick or more, every one failing
	if !r.hasWarning(stand, "only in memory") {
		t.Fatal("the in-memory warning cleared while every save fails")
	}
	os.Chmod(dirs[stand], 0o700)
	pairWait(t, "the warning to clear once a save succeeds", 10*time.Second, func() bool { return !r.hasWarning(stand, "only in memory") })
}

// At the default hot-max-age (3 × lease + 2 × the stream's interval: 1.7 s
// at the rig's 500 ms lease) a hard kill of the leader is hot: the
// default is meant to cover it (design §4.2).
func TestPairHotTakeoverAtTheDefaultHotMaxAge(t *testing.T) {
	r := newHotPairRig(t, func(_ string, p *project.Project) { p.Redundancy.HotMaxAge = nil })
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	r.set(t, lead, "Mode", 5)
	r.shadowed(t, lead)
	r.nodes[lead].crash()
	if got := r.leader(); got != stand {
		t.Fatalf("after the kill: leader %q, want %s", got, stand)
	}
	pairWait(t, "the new leader's takeover report", 5*time.Second, func() bool {
		s := r.streamStatus(stand)
		return s != nil && s.Takeover != ""
	})
	s := r.streamStatus(stand)
	t.Logf("takeover %s at the default hot-max-age, image %d ms old at the takeover", s.Takeover, s.TakeoverImageAgeMs)
	if s.Takeover != "hot" {
		t.Fatalf("takeover %q (%s) after a hard kill at the default hot-max-age, want hot", s.Takeover, s.TakeoverCold)
	}
	if got := r.get(stand, "Mode"); got != "5" {
		t.Errorf("Mode %s, want the operator's 5", got)
	}
}

// A takeover edge whose term moves while it waits flushes again (#710
// S1b review F6), and the second flush finds the shadow the first one
// took and emptied. It is not hot (another leadership may have come in
// between) but there was an image: the candidate says so, and the cold
// takeover is warned of.
func TestPairFlushOfAMovedTermStillHasAnImage(t *testing.T) {
	r := newHotPairRig(t, nil)
	lead := r.leader()
	stand := other(lead)
	r.inSync(t, stand)
	s := r.nodes[stand].m.stream
	first, err := s.takeoverFlush()
	// Flushed out of band, with no lease won: the voters' check (#710
	// S2c-1) says so, and nothing else may refuse the image.
	if err != nil || first == nil || first.Term == 0 || (first.Cold != "" && first.Cold != coldWhy(quorum.ColdNotLeading)) {
		t.Fatalf("first flush: %+v, %v; want the image", first, err)
	}
	second, err := s.takeoverFlush()
	if err != nil || second == nil || !strings.Contains(second.Cold, "term moved") {
		t.Fatalf("second flush of the same edge: %+v, %v; want a refused image", second, err)
	}
}
