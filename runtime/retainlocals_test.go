package runtime

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/retain"
)

const runHoursFB = `FUNCTION_BLOCK RunHours
VAR_INPUT run : BOOL; END_VAR
VAR_OUTPUT hours : REAL; END_VAR
VAR starts : DINT; was : BOOL; END_VAR
IF run THEN hours := hours + 1.0; END_IF;
IF run AND NOT was THEN starts := starts + 1; END_IF;
was := run;
END_FUNCTION_BLOCK`

const retainMain = `PROGRAM Main
VAR RETAIN total : DINT; rh : RunHours; END_VAR
VAR plain : DINT; END_VAR
VAR_EXTERNAL Stats : DINT; Scratch : DINT; END_VAR
total := total + 1;
plain := plain + 1;
rh(run := TRUE);
Stats := Stats + 1;
Scratch := Scratch + 1;
END_PROGRAM`

func newRetainRuntime(t *testing.T, store retain.Store) *Runtime {
	t.Helper()
	rt, err := New(Options{
		Program:   retainMain,
		Libraries: []string{runHoursFB},
		Retain:    store,
		Tags: []TagDef{
			State("Stats", 0, Retained()), // state the logic owns, kept
			State("Scratch", 0),           // state, not kept
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// What survives a restart through the file store (a JSON round trip, as on
// a controller): a state tag marked Retain, a program's VAR RETAIN locals,
// and a VAR RETAIN function-block instance with its own variables; a plain
// VAR local and an unmarked state tag start over (brief 002 F-05).
func TestVarRetainAndRetainedStateSurviveARestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "retain.json")
	rt := newRetainRuntime(t, retain.New("", file))
	for range 3 {
		rt.Scan()
	}
	rt.saveRetained(nil)

	rt2 := newRetainRuntime(t, retain.New("", file))
	rt2.Scan() // the load runs before the first scan
	locals := rt2.prog.Locals()
	if locals["total"] != int64(4) {
		t.Errorf("VAR RETAIN total = %v, want 4 (3 kept + 1)", locals["total"])
	}
	if locals["plain"] != int64(1) {
		t.Errorf("plain VAR = %v, want 1: only RETAIN is kept", locals["plain"])
	}
	rh, _ := locals["rh"].(map[string]any)
	if rh["hours"] != 4.0 || rh["starts"] != int64(1) {
		t.Errorf("VAR RETAIN RunHours = %v, want hours 4 and starts 1 (its was flag kept too, so no new start)", rh)
	}
	if v, _ := rt2.Tags().ReadGlobal("Stats"); v.I != 4 {
		t.Errorf("retained state tag Stats = %d, want 4", v.I)
	}
	if v, _ := rt2.Tags().ReadGlobal("Scratch"); v.I != 1 {
		t.Errorf("unmarked state tag Scratch = %d, want 1: state re-seeds unless marked", v.I)
	}
}

// A retained local whose type changed keeps its initial value and is
// reported; one the program no longer declares RETAIN is ignored.
func TestRestoreLocalsSkipsWhatNoLongerFits(t *testing.T) {
	rt := newRetainRuntime(t, nil)
	errs := rt.prog.RestoreLocals(map[string]any{"total": "not a number", "plain": 99, "gone": 1})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "total") {
		t.Errorf("errors %v, want one naming total", errs)
	}
	rt.Scan()
	if got := rt.prog.Locals()["plain"]; got != int64(1) {
		t.Errorf("plain = %v: a non-RETAIN local was written by the restore", got)
	}
}

// An input comes back from the field and an output from the logic on the
// first scan, so neither may be retained.
func TestRetainRefusedOnInputsAndOutputs(t *testing.T) {
	for _, d := range []TagDef{Input("Level", Retained()), Output("Pump", Retained())} {
		_, err := New(Options{Program: "PROGRAM P\nEND_PROGRAM", Tags: []TagDef{d}})
		if err == nil || !strings.Contains(err.Error(), "never retained") {
			t.Errorf("%s: %v, want a refusal", d.Name, err)
		}
	}
}
