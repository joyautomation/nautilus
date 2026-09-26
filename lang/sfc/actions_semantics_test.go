package sfc

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// scanner compiles src and returns a one-scan driver over a shared host, so a
// test can set inputs, run a scan, and read globals back in virtual time.
func scanner(t *testing.T, src string) (*ir.Program, *ir.Frame, *clockHost, func(map[string]ir.Value)) {
	t.Helper()
	prog := mustCompile(t, src)
	h := &clockHost{vals: map[string]ir.Value{}}
	frame := ir.NewFrame(prog)
	run := func(in map[string]ir.Value) {
		t.Helper()
		for k, v := range in {
			h.vals[k] = v
		}
		h.now += 100
		if err := ir.Run(prog, frame, h); err != nil {
			t.Fatal(err)
		}
	}
	return prog, frame, h, run
}

func b(v bool) ir.Value { return ir.BoolVal(v) }

// TestResetOnInactiveStepDoesNotClobberActionWrite is the batch-skid repro:
// a P1 ACTION sets X; a never-reached step carries a bare `R X`. An R acts
// once, on its step's activation — an inactive step's association never
// writes — so X must stay TRUE. (It used to go TRUE for one scan and then
// FALSE forever: every S/R target was recomputed from its stored flag every
// scan.)
func TestResetOnInactiveStepDoesNotClobberActionWrite(t *testing.T) {
	src := `PROGRAM P
VAR_EXTERNAL Go : BOOL; X : BOOL; END_VAR
SFC
  INITIAL_STEP Idle:
  END_STEP
  STEP Run:
    P1 SetX;
  END_STEP
  STEP Aborted:
    R X;
  END_STEP
  TRANSITION t_go FROM Idle TO Run := Go;
  END_TRANSITION
  TRANSITION t_dead FROM Run TO Aborted := FALSE;
  END_TRANSITION
  TRANSITION t_back FROM Aborted TO Idle := TRUE;
  END_TRANSITION
  ACTION SetX:
    X := TRUE;
  END_ACTION
END_SFC
END_PROGRAM`
	_, _, h, run := scanner(t, src)
	run(map[string]ir.Value{"Go": b(true)})
	if !h.vals["X"].B {
		t.Fatal("scan 1: P1 SetX should set X")
	}
	for i := 2; i <= 10; i++ {
		run(nil)
		if !h.vals["X"].B {
			t.Fatalf("scan %d: X went FALSE — the R on never-active step Aborted wrote it", i)
		}
	}
}

// TestSetActsOnceThenActionMayClear: S sets its variable once, on its step's
// activation; an ACTION may clear it afterwards, even while the S step is
// still active, and the S does not re-assert it.
func TestSetActsOnceThenActionMayClear(t *testing.T) {
	src := `PROGRAM P
VAR_EXTERNAL Go : BOOL; Clr : BOOL; Lamp : BOOL; END_VAR
SFC
  INITIAL_STEP Idle:
  END_STEP
  STEP Run:
    S Lamp;
    N Clear;
  END_STEP
  TRANSITION FROM Idle TO Run := Go;
  END_TRANSITION
  TRANSITION FROM Run TO Idle := FALSE;
  END_TRANSITION
  ACTION Clear:
    IF Clr THEN Lamp := FALSE; END_IF;
  END_ACTION
END_SFC
END_PROGRAM`
	_, _, h, run := scanner(t, src)
	run(map[string]ir.Value{"Go": b(true)})
	if !h.vals["Lamp"].B {
		t.Fatal("S Lamp should set Lamp on Run's activation")
	}
	run(nil)
	if !h.vals["Lamp"].B {
		t.Fatal("Lamp should stay set")
	}
	run(map[string]ir.Value{"Clr": b(true)})
	if h.vals["Lamp"].B {
		t.Fatal("the ACTION's clear should stand in the scan it runs")
	}
	run(map[string]ir.Value{"Clr": b(false)})
	run(nil)
	if h.vals["Lamp"].B {
		t.Fatal("S must not re-assert Lamp while Run stays active — it acts once, on activation")
	}
}

// TestResetActsOnceThenActionMaySet: R clears its variable once, on its
// step's activation; an ACTION may set it again while the R step is active.
func TestResetActsOnceThenActionMaySet(t *testing.T) {
	src := `PROGRAM P
VAR_EXTERNAL Go : BOOL; Set : BOOL; Lamp : BOOL; END_VAR
SFC
  INITIAL_STEP Idle:
  END_STEP
  STEP Run:
    R Lamp;
    N SetIt;
  END_STEP
  TRANSITION FROM Idle TO Run := Go;
  END_TRANSITION
  TRANSITION FROM Run TO Idle := FALSE;
  END_TRANSITION
  ACTION SetIt:
    IF Set THEN Lamp := TRUE; END_IF;
  END_ACTION
END_SFC
END_PROGRAM`
	_, _, h, run := scanner(t, src)
	h.vals["Lamp"] = b(true)
	run(nil)
	if !h.vals["Lamp"].B {
		t.Fatal("before Run activates, R must not touch Lamp")
	}
	run(map[string]ir.Value{"Go": b(true)})
	if h.vals["Lamp"].B {
		t.Fatal("R Lamp should clear Lamp on Run's activation")
	}
	run(map[string]ir.Value{"Go": b(false), "Set": b(true)})
	run(map[string]ir.Value{"Set": b(false)})
	run(nil)
	if !h.vals["Lamp"].B {
		t.Fatal("R must not keep clearing Lamp while Run stays active — it acts once, on activation")
	}
}

// TestNDrivesOnlyWhileActive: N holds its variable TRUE every scan its step
// is active — winning over an ACTION that writes it in the same scan — and
// writes FALSE once on the scan the step deactivates (TestNFreeWhenInactive
// covers "and leaves it alone after that").
func TestNDrivesOnlyWhileActive(t *testing.T) {
	src := `PROGRAM P
VAR_EXTERNAL Go : BOOL; Back : BOOL; Force : BOOL; Out : BOOL; END_VAR
SFC
  INITIAL_STEP Idle:
    N Writer;
  END_STEP
  STEP Run:
    N Out;
    N Writer;
  END_STEP
  TRANSITION FROM Idle TO Run := Go;
  END_TRANSITION
  TRANSITION FROM Run TO Idle := Back;
  END_TRANSITION
  ACTION Writer:
    IF Force THEN Out := FALSE; END_IF;
  END_ACTION
END_SFC
END_PROGRAM`
	_, _, h, run := scanner(t, src)
	run(nil)
	if h.vals["Out"].B {
		t.Fatal("Out should start FALSE")
	}
	run(map[string]ir.Value{"Go": b(true)}) // Idle→Run
	if !h.vals["Out"].B {
		t.Fatal("N Out: TRUE while Run is active")
	}
	run(map[string]ir.Value{"Go": b(false), "Force": b(true)})
	if !h.vals["Out"].B {
		t.Fatal("N Out wins over the ACTION's FALSE while Run is active")
	}
	run(map[string]ir.Value{"Force": b(false), "Back": b(true)}) // Run→Idle: final scan
	if h.vals["Out"].B {
		t.Fatal("N Out: FALSE on the scan Run deactivates")
	}
}

// TestNFreeWhenInactive: with the N step inactive (after its final scan),
// nothing the association does overwrites another writer's value.
func TestNFreeWhenInactive(t *testing.T) {
	src := `PROGRAM P
VAR_EXTERNAL Go : BOOL; Out : BOOL; Other : BOOL; END_VAR
SFC
  INITIAL_STEP Idle:
    N Writer;
  END_STEP
  STEP Run:
    N Out;
  END_STEP
  TRANSITION FROM Idle TO Run := Go;
  END_TRANSITION
  TRANSITION FROM Run TO Idle := NOT Go;
  END_TRANSITION
  ACTION Writer:
    Out := Other;
  END_ACTION
END_SFC
END_PROGRAM`
	_, _, h, run := scanner(t, src)
	run(map[string]ir.Value{"Other": b(true)})
	if !h.vals["Out"].B {
		t.Fatal("N Out on inactive Run must not force FALSE over the ACTION's TRUE")
	}
	run(map[string]ir.Value{"Go": b(true), "Other": b(false)}) // Idle→Run (Writer final scan writes FALSE, N wins)
	if !h.vals["Out"].B {
		t.Fatal("N Out TRUE once Run is active")
	}
	run(map[string]ir.Value{"Go": b(false)}) // Run→Idle: N final scan FALSE
	if h.vals["Out"].B {
		t.Fatal("N Out FALSE on Run's final scan")
	}
	run(map[string]ir.Value{"Other": b(true)})
	if !h.vals["Out"].B {
		t.Fatal("after the final scan the variable is the ACTION's")
	}
}

// TestNStoreInteraction: a variable with both N and S associations follows
// IEC's Q = N OR stored — when the N step drops, the stored value remains.
func TestNStoreInteraction(t *testing.T) {
	src := `PROGRAM P
VAR_EXTERNAL A : BOOL; B2 : BOOL; C : BOOL; Out : BOOL; END_VAR
SFC
  INITIAL_STEP S0:
  END_STEP
  STEP S1:
    S Out;
  END_STEP
  STEP S2:
    N Out;
  END_STEP
  STEP S3:
    R Out;
  END_STEP
  TRANSITION FROM S0 TO S1 := A;
  END_TRANSITION
  TRANSITION FROM S1 TO S2 := B2;
  END_TRANSITION
  TRANSITION FROM S2 TO S3 := C;
  END_TRANSITION
END_SFC
END_PROGRAM`
	_, _, h, run := scanner(t, src)
	run(map[string]ir.Value{"A": b(true)})
	run(map[string]ir.Value{"A": b(false), "B2": b(true)})
	if !h.vals["Out"].B {
		t.Fatal("Out should be TRUE in S2")
	}
	run(map[string]ir.Value{"B2": b(false), "C": b(true)}) // S2→S3: N drops, R acts
	if h.vals["Out"].B {
		t.Fatal("R on S3 should clear Out")
	}
}

// TestNonTransitiveGroupConvergenceFires is the batch-skid shape: a
// simultaneous branch A/B/C whose convergence t_join shares one source with
// each of three abort transitions, which share nothing with each other — in
// both declaration orders. With Abort FALSE the convergence fires; with Abort
// TRUE the chart aborts.
func TestNonTransitiveGroupConvergenceFires(t *testing.T) {
	const join = "  TRANSITION t_join FROM (A, B, C) TO D := Cond;\n  END_TRANSITION\n"
	const aborts = `  TRANSITION ab_a FROM A TO Aborted := Abort;
  END_TRANSITION
  TRANSITION ab_b FROM B TO Aborted := Abort;
  END_TRANSITION
  TRANSITION ab_c FROM C TO Aborted := Abort;
  END_TRANSITION
`
	for _, order := range []string{"aborts-first", "join-first"} {
		for _, abort := range []bool{false, true} {
			body := join + aborts
			if order == "aborts-first" {
				body = aborts + join
			}
			src := `PROGRAM P
VAR_EXTERNAL Go : BOOL; Cond : BOOL; Abort : BOOL; END_VAR
SFC
  INITIAL_STEP S:
  END_STEP
  STEP A:
  END_STEP
  STEP B:
  END_STEP
  STEP C:
  END_STEP
  STEP D:
  END_STEP
  STEP Aborted:
  END_STEP
  TRANSITION t_split FROM S TO (A, B, C) := Go;
  END_TRANSITION
` + body + `  TRANSITION FROM D TO S := FALSE;
  END_TRANSITION
  TRANSITION FROM Aborted TO S := FALSE;
  END_TRANSITION
END_SFC
END_PROGRAM`
			if diags := Check(mustParse(t, src)); len(diags) != 0 {
				t.Errorf("%s: unexpected diagnostics %v", order, diags)
			}
			prog, frame, _, run := scanner(t, src)
			run(map[string]ir.Value{"Go": b(true)}) // S→(A,B,C)
			run(map[string]ir.Value{"Go": b(false), "Cond": b(true), "Abort": b(abort)})
			gotD, gotAb := stepActive(t, prog, frame, "D"), stepActive(t, prog, frame, "Aborted")
			for _, s := range []string{"A", "B", "C"} {
				if stepActive(t, prog, frame, s) {
					t.Errorf("%s abort=%v: %s still active — the branch is stuck", order, abort, s)
				}
			}
			switch {
			case !abort && (!gotD || gotAb):
				t.Errorf("%s: Abort FALSE → want D, got D=%v Aborted=%v", order, gotD, gotAb)
			case abort && order == "aborts-first" && (gotD || !gotAb):
				t.Errorf("%s: Abort TRUE → want Aborted, got D=%v Aborted=%v", order, gotD, gotAb)
			case abort && order == "join-first" && (!gotD || gotAb):
				// t_join is declared first, so it has priority over each abort.
				t.Errorf("%s: Abort TRUE, t_join has priority → want D, got D=%v Aborted=%v", order, gotD, gotAb)
			}
		}
	}
}

// TestSuppressedOnlyByFiringTransition: t1 FROM A, t2 FROM (A, B), t3 FROM B,
// all enabled. t1 takes A's token and suppresses t2; t2 does not fire, so it
// must not suppress t3 — B's token goes to t3 in the same scan.
func TestSuppressedOnlyByFiringTransition(t *testing.T) {
	src := `PROGRAM P
VAR_EXTERNAL Go : BOOL; END_VAR
SFC
  INITIAL_STEP S:
  END_STEP
  STEP A:
  END_STEP
  STEP B:
  END_STEP
  STEP X1:
  END_STEP
  STEP X2:
  END_STEP
  STEP X3:
  END_STEP
  TRANSITION split FROM S TO (A, B) := TRUE;
  END_TRANSITION
  TRANSITION t1 FROM A TO X1 := Go;
  END_TRANSITION
  TRANSITION t2 FROM (A, B) TO X2 := Go;
  END_TRANSITION
  TRANSITION t3 FROM B TO X3 := Go;
  END_TRANSITION
END_SFC
END_PROGRAM`
	prog, frame, _, run := scanner(t, src)
	run(nil) // S→(A,B)
	run(map[string]ir.Value{"Go": b(true)})
	for step, want := range map[string]bool{"X1": true, "X2": false, "X3": true, "A": false, "B": false} {
		if got := stepActive(t, prog, frame, step); got != want {
			t.Errorf("%s active = %v, want %v", step, got, want)
		}
	}
}
