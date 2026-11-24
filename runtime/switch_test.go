//go:build !js

package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The switching state of a planned switchover (#710 d1c, switch.go).

func scansOf(p *Program) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.scans
}

// A switchover stops the scans and refuses every write from outside the
// programs (tag writes, write sets, forces, substitutes, online edits and
// their rollback) until it ends; its final image is of the last scans,
// numbered after the streamed ones, and only of the leadership switching.
// EndSwitch (an abort) lets them all in again and the scans go on.
// switch_writes_allowed, switch_scans_on and abort_keeps_switching turn it
// red.
func TestSwitchingStopsTheScansAndRefusesWrites(t *testing.T) {
	r := newHotRig(t, redMain, redMain)
	r.coord.takeover.Store(true)
	r.stream(t, 3)
	r.takeOver(t)
	rt := r.stby
	r.sc.step(100 * time.Millisecond)
	rt.Scan()
	streamed := takeOne(t, rt, r.sc)
	if err := rt.BeginSwitch(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	if rt.Switching() != 4 {
		t.Fatalf("Switching() = %d, want 4", rt.Switching())
	}
	main := rt.TaskProgram(MainTaskName)
	before, auxBefore := scansOf(main), scansOf(rt.TaskProgram("aux"))
	for range 3 {
		r.sc.step(100 * time.Millisecond)
		rt.Scan()
		if err := rt.ScanTask("aux"); err != nil {
			t.Fatal(err)
		}
	}
	if n, a := scansOf(main), scansOf(rt.TaskProgram("aux")); n != before || a != auxBefore {
		t.Fatalf("scans went on while switching: main %d -> %d, aux %d -> %d", before, n, auxBefore, a)
	}

	tags := rt.Tags()
	if errs := tags.Write([]WriteOp{{Path: "Setpoint", Value: 9.0}}); !errors.Is(errs[0], ErrSwitching) {
		t.Fatalf("tag write while switching: %v, want ErrSwitching", errs[0])
	}
	if out := tags.WriteSetGuarded([]WriteOp{{Path: "Setpoint", Value: 9.0}}, nil, "user:op", false); !errors.Is(out[0].Err, ErrSwitching) {
		t.Fatalf("write set while switching: %v, want ErrSwitching", out[0].Err)
	}
	if err := tags.Force("Mode", int64(2)); !errors.Is(err, ErrSwitching) {
		t.Fatalf("force while switching: %v, want ErrSwitching", err)
	}
	if _, err := rt.Substitute("Setpoint", SubstituteRequest{Value: 1.0, For: time.Minute}); !errors.Is(err, ErrSwitching) {
		t.Fatalf("substitute while switching: %v, want ErrSwitching", err)
	}
	if _, err := main.SwapWarm(redMain); !errors.Is(err, ErrSwitching) {
		t.Fatalf("online edit while switching: %v, want ErrSwitching", err)
	}
	if v, _ := tags.ReadGlobal("Setpoint"); v.F != 4.5 {
		t.Fatalf("Setpoint %v after the refused writes, want 4.5", v.F)
	}

	img, err := rt.FinalImage(4)
	if err != nil {
		t.Fatal(err)
	}
	if img.Term != 4 || img.Seq <= streamed.Seq {
		t.Fatalf("final image term %d seq %d, want term 4 and a seq above the streamed %d", img.Term, img.Seq, streamed.Seq)
	}
	if len(img.Tasks) != 2 || img.Tasks[0].Scans != before || img.Tasks[0].NowMs == 0 || img.Tasks[0].Source == "" {
		t.Fatalf("final image tasks %+v: want both, main at its last scan (%d), with its time base and source", img.Tasks, before)
	}
	if _, err := rt.FinalImage(5); !errors.Is(err, ErrNotSwitching) {
		t.Fatalf("final image of another leadership: %v, want ErrNotSwitching", err)
	}

	rt.EndSwitch()
	if rt.Switching() != 0 {
		t.Fatal("still switching after EndSwitch")
	}
	if errs := tags.Write([]WriteOp{{Path: "Setpoint", Value: 9.0}}); errs[0] != nil {
		t.Fatalf("tag write after the abort: %v", errs[0])
	}
	r.sc.step(100 * time.Millisecond)
	rt.Scan()
	if n := scansOf(main); n != before+1 {
		t.Fatalf("main scans %d after the abort's scan, want %d", n, before+1)
	}
	if _, err := rt.FinalImage(4); !errors.Is(err, ErrNotSwitching) {
		t.Fatalf("final image after the abort: %v, want ErrNotSwitching", err)
	}
}

// BeginSwitch returns only once the scan under way has ended; a scan that
// does not end before ctx does ends the switching state, and the scans go
// on. switch_no_wait turns it red.
func TestSwitchingWaitsForTheScanUnderWay(t *testing.T) {
	r := newHotRig(t, redMain, redMain)
	r.stream(t, 1)
	r.takeOver(t)
	rt := r.stby
	rt.sw.inScan.Add(1) // a scan past its gate, not yet done
	done := make(chan error, 1)
	go func() { done <- rt.BeginSwitch(context.Background(), 4) }()
	select {
	case err := <-done:
		t.Fatalf("BeginSwitch returned (%v) while a scan was under way", err)
	case <-time.After(50 * time.Millisecond):
	}
	rt.scanExit()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rt.EndSwitch()

	rt.sw.inScan.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := rt.BeginSwitch(ctx, 4); err == nil {
		t.Fatal("BeginSwitch succeeded with a scan that never ended")
	}
	rt.scanExit()
	if rt.Switching() != 0 {
		t.Fatal("a switchover whose scan wait timed out is still switching")
	}
	if errs := rt.Tags().Write([]WriteOp{{Path: "Setpoint", Value: 9.0}}); errs[0] != nil {
		t.Fatalf("tag write after the timed-out start: %v", errs[0])
	}
}

// A switchover is refused while a force is held (a takeover drops forces)
// or this controller does not lead in the term asked. switch_ignores_forces
// turns it red.
func TestSwitchingRefusedWithForcesOrWhenNotLeading(t *testing.T) {
	r := newHotRig(t, redMain, redMain)
	r.stream(t, 1)
	r.takeOver(t)
	rt := r.stby
	if err := rt.BeginSwitch(context.Background(), 5); err == nil {
		t.Fatal("a switchover of a term this controller does not lead in was started")
	}
	if err := rt.Tags().Force("Mode", int64(2)); err != nil {
		t.Fatal(err)
	}
	if err := rt.BeginSwitch(context.Background(), 4); err == nil {
		t.Fatal("a switchover started with a force held")
	}
	if rt.Switching() != 0 {
		t.Fatal("a refused switchover left the switching state on")
	}
	rt.Tags().Unforce("Mode")
	if err := rt.BeginSwitch(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	rt.EndSwitch()
}

// A switchover is refused while a takeover hold holds the outputs.
// switch_ignores_hold turns it red.
func TestSwitchingRefusedWhileAHoldHolds(t *testing.T) {
	cfg := holdCfg
	r := newHoldRig(t, &cfg)
	r.maxAge = 0 // cold: the hold holds
	r.stream(t, nil)
	r.takeOver(t)
	if !r.held(t) {
		t.Fatal("the hold does not hold; the test needs it to")
	}
	if err := r.stby.BeginSwitch(context.Background(), 4); err == nil {
		t.Fatal("a switchover started while a takeover hold held")
	}
	if r.stby.Switching() != 0 {
		t.Fatal("a refused switchover left the switching state on")
	}
}

// A gate that closes for a tick and reopens in the same term (a late
// renewal) resumes the switchover: the scans stay stopped, writes stay
// refused, and the final image is still the whole state (Fable M1). The
// switching state ends at this controller's next leadership, a new term's
// takeover. resume_ends_switching and takeover_keeps_switching turn it red.
func TestSwitchingSurvivesAGateBlipAndEndsAtTheNextLeadership(t *testing.T) {
	r := newHotRig(t, redMain, redMain)
	r.stream(t, 1)
	r.takeOver(t)
	rt := r.stby
	main := rt.TaskProgram(MainTaskName)
	if err := rt.BeginSwitch(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	stopped := scansOf(main)
	r.coord.lead.Store(false) // one late renewal
	r.sc.step(100 * time.Millisecond)
	rt.Scan()
	r.coord.lead.Store(true) // the same term again
	for range 3 {
		r.sc.step(100 * time.Millisecond)
		rt.Scan()
	}
	if term, lead := rt.LeadingTerm(); !lead || term != 4 {
		t.Fatalf("not leading term 4 again after the blip: %d %v", term, lead)
	}
	if rt.Switching() != 4 {
		t.Fatal("the switching state ended at a same-term blip")
	}
	if n := scansOf(main); n != stopped {
		t.Fatalf("the scans went on after the blip: %d -> %d", stopped, n)
	}
	if errs := rt.Tags().Write([]WriteOp{{Path: "Setpoint", Value: 9.0}}); !errors.Is(errs[0], ErrSwitching) {
		t.Fatalf("a write after the blip: %v, want ErrSwitching", errs[0])
	}
	img, err := rt.FinalImage(4)
	if err != nil || img.Tasks[0].Scans != stopped {
		t.Fatalf("final image after the blip: %v, %+v", err, img)
	}

	// The resign: the gate shuts for good, and the next leadership is a
	// new term, whose takeover ends the switching state.
	r.coord.lead.Store(false)
	r.sc.step(100 * time.Millisecond)
	rt.Scan()
	r.coord.term.Store(6)
	r.coord.lead.Store(true)
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.sc.step(100 * time.Millisecond)
		rt.Scan()
		if term, lead := rt.LeadingTerm(); lead && term == 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never led term 6")
		}
		time.Sleep(time.Millisecond)
	}
	if rt.Switching() != 0 {
		t.Fatal("still switching in the next leadership: its scans would never run")
	}
	before := scansOf(main)
	r.sc.step(100 * time.Millisecond)
	rt.Scan()
	if scansOf(main) == before {
		t.Fatal("the next leadership does not scan")
	}
	if errs := rt.Tags().Write([]WriteOp{{Path: "Setpoint", Value: 8.0}}); errs[0] != nil {
		t.Fatalf("a write in the next leadership: %v", errs[0])
	}
}

// The final image closes the stream's open round: copies the last scans
// made for it never become an image numbered after the final one, which
// the standby would take and then refuse the final as older (Fable S1).
// final_keeps_round turns it red.
func TestTheFinalImageClosesTheOpenRound(t *testing.T) {
	r := newHotRig(t, redMain, redMain)
	r.stream(t, 1)
	r.takeOver(t)
	rt := r.stby
	var img Image
	scan := func() {
		r.sc.step(100 * time.Millisecond)
		rt.Scan()
		if err := rt.ScanTask("aux"); err != nil {
			t.Fatal(err)
		}
	}
	rt.RequestImage()
	scan() // both tasks copy
	if rt.TakeImage(&Image{}) {
		t.Fatal("two tasks: the first take should ask a re-copy")
	}
	scan() // the re-copy: the round is ready, not yet taken
	if err := rt.BeginSwitch(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	final, err := rt.FinalImage(4)
	if err != nil {
		t.Fatal(err)
	}
	if rt.TakeImage(&img) {
		t.Fatalf("an image of the open round was taken after the final one: seq %d after the final's %d", img.Seq, final.Seq)
	}
}

// SFC commands (set a step, fire a transition) change a frame, so they are
// refused while switching, as online edits are (Fable S2).
func TestSFCCommandsRefusedWhileSwitching(t *testing.T) {
	rt := newChart(t)
	rt.Scan()
	rt.tags.switching.Store(true)
	if _, err := rt.Program().SetStep("Fill"); !errors.Is(err, ErrSwitching) {
		t.Fatalf("SetStep while switching: %v, want ErrSwitching", err)
	}
	if _, err := rt.Program().FireTransition("Start"); !errors.Is(err, ErrSwitching) {
		t.Fatalf("FireTransition while switching: %v, want ErrSwitching", err)
	}
	if got := activeSteps(t, rt); got != "Idle" {
		t.Fatalf("active = %s after refused commands; want Idle", got)
	}
}
