package runtime_test

import (
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

const takeoverFirstScanProgram = `PROGRAM Main
VAR_EXTERNAL Out : DINT; Firsts : DINT; END_VAR
IF FIRST_SCAN() THEN
  Firsts := Firsts + 1;
END_IF;
Out := 5;
END_PROGRAM`

// FIRST_SCAN() is TRUE on the first scan after every takeover: a takeover
// resets the program's frames, so it is a start, and a program's
// initialisation must run on it whether or not this box led before (#710).
// It used to fire only on a box's first leadership since its start (a
// standby never scans), and never on a later takeover by a box that had
// led. A resumed term keeps the frames and is not a start.
func TestFirstScanOnEveryTakeover(t *testing.T) {
	coord := &termFlag{}
	coord.lead.Store(true)
	coord.term.Store(1)
	rt, err := runtime.New(runtime.Options{
		Program:     takeoverFirstScanProgram,
		Driver:      &hangDriver{},
		Outputs:     []string{"Out"},
		Seed:        nio.Values{"Out": int64(0), "Firsts": int64(0)},
		Scan:        time.Hour,
		Coordinator: coord,
	})
	if err != nil {
		t.Fatal(err)
	}
	firsts := func() int64 {
		t.Helper()
		v, err := rt.Tags().ReadGlobal("Firsts")
		if err != nil {
			t.Fatal(err)
		}
		return v.I
	}
	scan := func(n int) {
		for range n {
			rt.Scan()
		}
	}
	scan(3)
	if n := firsts(); n != 1 {
		t.Fatalf("the first leadership: %d first scans, want 1", n)
	}

	coord.lead.Store(false) // stalled, then resumed in the same term
	scan(1)
	coord.lead.Store(true)
	scan(2)
	if n := firsts(); n != 1 {
		t.Fatalf("a resumed term: %d first scans, want still 1 (its frames carry on)", n)
	}

	coord.lead.Store(false) // standby while another replica leads term 2
	scan(1)
	coord.term.Store(3)
	coord.lead.Store(true)
	scan(2)
	if n := firsts(); n != 2 {
		t.Fatalf("a takeover by a box that led before: %d first scans, want 2", n)
	}

	coord.term.Store(4) // lost and regained between two scans: a takeover too
	scan(2)
	if n := firsts(); n != 3 {
		t.Fatalf("a new term seen while leading: %d first scans, want 3", n)
	}
}
