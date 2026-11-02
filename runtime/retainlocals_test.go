package runtime

import (
	"math"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
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

const edgeProgram = `PROGRAM Main
VAR RETAIN starts : CTU; delay : TON; avg : REAL; k : DINT := 5; END_VAR
VAR RETAIN CONSTANT kc : DINT := 5; END_VAR
starts(CU := TRUE, PV := 100);
delay(IN := TRUE, PT := T#1h);
END_PROGRAM`

// A retained CTU keeps its edge memory with its count: a count input held
// TRUE through a restart does not count again (a start count would gain one
// per restart). A TON keeps time against the clock, so it is not kept and
// starts over.
func TestRetainedCounterDoesNotCountARestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "retain.json")
	mk := func() *Runtime {
		rt, err := New(Options{Program: edgeProgram, Retain: retain.New("", file)})
		if err != nil {
			t.Fatal(err)
		}
		return rt
	}
	rt := mk()
	for range 3 {
		rt.Scan()
	}
	saved := rt.prog.RetainedLocals()
	if _, kept := saved["delay"]; kept {
		t.Error("a TON was kept; its start time means nothing after a restart")
	}
	rt.saveRetained(nil)
	rt2 := mk()
	rt2.Scan()
	c, _ := rt2.prog.Locals()["starts"].(map[string]any)
	if c["CV"] != int64(1) {
		t.Errorf("CTU CV after a restart with CU held = %v, want 1 (no false edge)", c["CV"])
	}
}

// One member that no longer fits does not cost an FB its other members, and
// a REAL kept as null (JSON's NaN) comes back NaN.
func TestRestoreKeepsWhatFitsAndRestoresNaN(t *testing.T) {
	rt := newRetainRuntime(t, nil)
	errs := rt.prog.RestoreLocals(map[string]any{"rh": map[string]any{"hours": 7.0, "starts": "bad", "was": true}})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "rh.starts") {
		t.Errorf("errors %v, want one naming rh.starts", errs)
	}
	rh, _ := rt.prog.Locals()["rh"].(map[string]any)
	if rh["hours"] != 7.0 || rh["was"] != true {
		t.Errorf("rh = %v: the members that fit were not restored", rh)
	}

	rt2, err := New(Options{Program: edgeProgram})
	if err != nil {
		t.Fatal(err)
	}
	if errs := rt2.prog.RestoreLocals(map[string]any{"avg": nil}); len(errs) != 0 {
		t.Fatalf("NaN: %v", errs)
	}
	rt2.prog.mu.Lock()
	v := rt2.prog.frame.Slots[rt2.prog.prog.SlotIndex["avg"]]
	rt2.prog.mu.Unlock()
	if !math.IsNaN(v.F) {
		t.Errorf("avg = %v, want NaN", v.F)
	}
}

// A VAR RETAIN CONSTANT is its declaration's value: it is neither saved nor
// restored.
func TestRetainedConstantIsNotKept(t *testing.T) {
	rt, err := New(Options{Program: edgeProgram})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	if _, kept := rt.prog.RetainedLocals()["kc"]; kept {
		t.Error("a CONSTANT was kept")
	}
}

// Before its own takeover has loaded the store, a runtime saves nothing:
// its seeds and fresh frames would overwrite what the takeover will load.
func TestNoSaveBeforeTakeover(t *testing.T) {
	store := &fakeStore{state: retain.State{Tags: map[string]any{"Stats": 9.0}}}
	rt := newRetainRuntime(t, store)
	rt.saveRetained(nil)
	if store.saves != 0 {
		t.Fatalf("saved %d times before the first scan's takeover", store.saves)
	}
	rt.Scan()
	if v, _ := rt.Tags().ReadGlobal("Stats"); v.I != 10 {
		t.Errorf("Stats = %d, want the stored 9 + 1", v.I)
	}
}

// An explicit RetainTags list replaces the setpoint default, not a tag's own
// Retain mark.
func TestExplicitRetainTagsKeepsMarkedTags(t *testing.T) {
	rt, err := New(Options{
		Program: retainMain, Libraries: []string{runHoursFB}, Retain: &fakeStore{},
		Tags:       []TagDef{State("Stats", 0, Retained()), State("Scratch", 0)},
		RetainTags: []string{"Scratch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(rt.retainTags, "Stats") || !slices.Contains(rt.retainTags, "Scratch") {
		t.Errorf("retained set %v, want Scratch and Stats", rt.retainTags)
	}
}

// What a program produced survives a restart as it was, even where it is
// outside its declared width: the VM does not wrap an INT counted past
// 32767, and NOT of an LWORD 0 sets the top bit (-1 in the store). Only a
// client's write is held to the width (Tags.Write, PR #501's review); a
// restore of the program's own state is not a client's write.
func TestRetainKeepsWhatTheProgramProduced(t *testing.T) {
	file := filepath.Join(t.TempDir(), "retain.json")
	const prog = `PROGRAM Main
VAR RETAIN mask : LWORD; END_VAR
VAR_EXTERNAL C : INT; W : LWORD; END_VAR
IF mask = 0 THEN mask := NOT mask; W := NOT W; C := 32767; END_IF;
END_PROGRAM`
	mk := func() *Runtime {
		rt, err := New(Options{
			Program: prog,
			Retain:  retain.New("", file),
			Tags: []TagDef{
				Typed("C", RoleState, "INT", Retained()),
				Typed("W", RoleState, "LWORD", Retained()),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return rt
	}
	rt := mk()
	rt.Scan()
	if err := rt.Tags().Write([]WriteOp{{"C", 1.0}})[0]; err != nil {
		t.Fatal(err)
	}
	// The program counts past the declared width, as a PLC's would not.
	_ = rt.Tags().WriteGlobal("C", ir.IntVal(40000))
	rt.saveRetained(nil)

	rt2 := mk()
	rt2.Scan()
	if v, _ := rt2.Tags().ReadGlobal("C"); v.I != 40000 {
		t.Errorf("C = %d after a restart, want the 40000 the program left", v.I)
	}
	if v, _ := rt2.Tags().ReadGlobal("W"); v.I != -1 {
		t.Errorf("W = %d after a restart, want -1 (every bit set)", v.I)
	}
	if got := rt2.prog.Locals()["mask"]; got != int64(-1) {
		t.Errorf("VAR RETAIN mask = %v after a restart, want -1", got)
	}
}
