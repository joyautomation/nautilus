package runtime

import (
	"strings"
	"testing"
	"time"
)

// The nautilus system functions a pair's program reads (#710 (b),
// redstate.go): TAKEOVER_SCAN(), HOT_TAKEOVER(), TAKEOVERS(),
// PEER_READY(), and FIRST_SCAN() FALSE on a hot takeover.

const redMain = `PROGRAM Main
VAR_EXTERNAL Run : BOOL; Setpoint : REAL; Out : REAL; Latch : INT; Mode : INT; Talk : BOOL; END_VAR
VAR ts : INT; hs : INT; fs : INT; tk : DINT; pr : BOOL; hold : BOOL; END_VAR
IF TAKEOVER_SCAN() THEN ts := ts + 1; END_IF;
IF HOT_TAKEOVER() THEN hs := hs + 1; END_IF;
IF FIRST_SCAN() THEN fs := fs + 1; END_IF;
tk := TAKEOVERS();
pr := PEER_READY();
hold := TAKEOVER_HOLD();
Out := Setpoint;
END_PROGRAM`

// redCounts are Main's counters on rt.
func redCounts(t *testing.T, rt *Runtime) (ts, hs, fs, tk int64, pr bool) {
	t.Helper()
	p := rt.TaskProgram(MainTaskName)
	return local(t, p, "ts").I, local(t, p, "hs").I, local(t, p, "fs").I, local(t, p, "tk").I, local(t, p, "pr").B
}

// A takeover sets TAKEOVER_SCAN() for one scan, HOT_TAKEOVER() too when it
// is hot, and counts in TAKEOVERS(). A hot one is no start: FIRST_SCAN()
// stays FALSE (the image's count goes on). A cold one is a start too. An
// acquisition the coordinator calls a start is neither. takeover_unmarked,
// takeover_flag_kept and hot_first_scan_true turn it red.
func TestSystemFunctionsAcrossTakeovers(t *testing.T) {
	for _, c := range []struct {
		name           string
		takeover, hot  bool
		ts, hs, fs, tk int64
	}{
		{"hot takeover", true, true, 1, 1, 1, 1},
		{"cold takeover", true, false, 1, 0, 1, 1},
		{"a start", false, false, 0, 0, 1, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newHotRig(t, redMain, redMain)
			r.coord.takeover.Store(c.takeover)
			if !c.hot {
				r.flush = r.take(0, nil) // hot-max-age 0: cold
			}
			r.stream(t, 3)
			if ts, hs, fs, _, _ := redCounts(t, r.lead); ts != 0 || hs != 0 || fs != 1 {
				t.Fatalf("the leader (not in a pair here): ts %d hs %d fs %d, want 0 0 1", ts, hs, fs)
			}
			rep := r.takeOver(t)
			if rep.Hot != c.hot {
				t.Fatalf("takeover hot %v, want %v (%s)", rep.Hot, c.hot, rep.Reason)
			}
			ts, hs, fs, tk, _ := redCounts(t, r.stby)
			if ts != c.ts || hs != c.hs || fs != c.fs || tk != c.tk {
				t.Fatalf("first scan: TAKEOVER_SCAN %d HOT_TAKEOVER %d FIRST_SCAN %d TAKEOVERS %d; want %d %d %d %d", ts, hs, fs, tk, c.ts, c.hs, c.fs, c.tk)
			}
			r.sc.step(100 * time.Millisecond)
			r.stby.Scan()
			if ts2, hs2, fs2, tk2, _ := redCounts(t, r.stby); ts2 != ts || hs2 != hs || fs2 != fs || tk2 != tk {
				t.Fatalf("second scan: %d %d %d %d, want the first's (one scan only)", ts2, hs2, fs2, tk2)
			}
		})
	}
}

// PEER_READY() reads what the pair last set, latched at the scan's start;
// FALSE outside a pair. An online edit's first scan is no takeover's, even
// one made before the program scanned after it. peer_ready_never_set
// turns it red.
func TestPeerReadyAndAnEditAfterATakeover(t *testing.T) {
	r := newHotRig(t, redMain, redMain)
	r.coord.takeover.Store(true)
	r.stream(t, 3)
	r.takeOver(t)
	if _, _, _, _, pr := redCounts(t, r.stby); pr {
		t.Fatal("PEER_READY TRUE before the pair said so")
	}
	r.stby.SetPeerReady(true)
	r.stby.Scan()
	if _, _, _, _, pr := redCounts(t, r.stby); !pr {
		t.Fatal("PEER_READY FALSE after the pair set it")
	}
	r.stby.SetPeerReady(false)
	r.stby.Scan()
	if _, _, _, _, pr := redCounts(t, r.stby); pr {
		t.Fatal("PEER_READY still TRUE")
	}
	// A takeover marked, then an online edit before the program scans.
	p := r.stby.TaskProgram(MainTaskName)
	before, _, _, _, _ := redCounts(t, r.stby)
	p.markTakeover(true)
	if _, err := p.SwapWarm(redMain + "\n"); err != nil {
		t.Fatal(err)
	}
	r.stby.Scan()
	if ts, _, _, _, _ := redCounts(t, r.stby); ts != before {
		t.Fatalf("TAKEOVER_SCAN on the first scan after an online edit: %d, want %d", ts, before)
	}
}

// A ladder program named as a system function is refused like an ST one:
// every language lowers through the ST lowering, where the check is.
func TestALadderProgramNamedAsASystemFunctionIsRefused(t *testing.T) {
	src := "PROGRAM Takeovers\nVAR_EXTERNAL Run : BOOL; Out : REAL; END_VAR\nLD\n  RUNG r\n    Run ( Out )\nEND_LD\nEND_PROGRAM\n"
	_, err := New(Options{Program: src, Tags: []TagDef{State("Run", true), State("Out", false)}})
	if err == nil || !strings.Contains(err.Error(), "nautilus system function") {
		t.Fatalf("got %v", err)
	}
}

// A takeover a program never scanned after is spent when the controller
// steps down: the resume in the same term is no takeover (Sonnet, #1350).
// takeover_flag_survives_step_down turns it red.
func TestATakeoverFlagDoesNotSurviveAStepDown(t *testing.T) {
	r := newHotRig(t, redMain, redMain)
	r.coord.takeover.Store(true)
	r.stream(t, 3)
	r.takeOver(t)
	before, _, _, _, _ := redCounts(t, r.stby)
	// A slow task's program, marked but not scanned yet (as markTakeover
	// leaves it), when the lease goes.
	r.stby.TaskProgram(MainTaskName).markTakeover(false)
	r.coord.lead.Store(false)
	r.stby.Scan() // steps down
	r.coord.lead.Store(true)
	r.stby.Scan() // resumes in term 4
	if _, lead := r.stby.LeadingTerm(); !lead {
		t.Fatal("not leading after the resume")
	}
	if ts, _, _, _, _ := redCounts(t, r.stby); ts != before {
		t.Fatalf("TAKEOVER_SCAN on a same-term resume: count %d, want %d", ts, before)
	}
}
