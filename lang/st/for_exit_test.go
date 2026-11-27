package st

import (
	"math"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// #1427 (James 11-26: "whatever is the standard handling in most PLCs";
// naut-lead: exit overflow wraps, as the bench S7-1500 measured). A FOR's
// counter starts at start and is tested before each pass; a pass whose
// value the variable's type cannot hold faults the scan; after the loop
// the variable holds the first value that failed the test, wrapped to its
// type (end + step when the step lands on end, start when no pass ran).
// EXIT and RETURN leave the pass value.
func TestForExitValueAndOutOfRangePass(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		passes     int64
		exit       int64
		fault      string // the scan faults with this, passes counted before it
	}{
		{"lands on end", "FOR i := 1 TO 3 DO n := n + 1; END_FOR; x := i;", 3, 4, ""},
		{"step misses end", "FOR i := 1 TO 10 BY 4 DO n := n + 1; END_FOR; x := i;", 3, 13, ""},
		{"negative step", "FOR i := 3 TO 1 BY -1 DO n := n + 1; END_FOR; x := i;", 3, 0, ""},
		{"no pass takes start", "i := 7; FOR i := 5 TO 1 DO n := n + 1; END_FOR; x := i;", 0, 5, ""},
		{"no pass, negative step", "i := 7; FOR i := 1 TO 5 BY -1 DO n := n + 1; END_FOR; x := i;", 0, 1, ""},
		{"variable step", "FOR i := 0 TO 9 BY s DO n := n + 1; END_FOR; x := i;", 3, 12, ""},
		{"body write does not steer", "FOR i := 1 TO 3 DO i := 100; n := n + 1; END_FOR; x := i;", 3, 4, ""},
		{"EXIT keeps the pass value", "FOR i := 1 TO 10 DO n := n + 1; IF i = 3 THEN EXIT; END_IF; END_FOR; x := i;", 3, 3, ""},
		{"EXIT keeps a body write", "FOR i := 1 TO 10 DO n := n + 1; IF i = 3 THEN i := 50; EXIT; END_IF; END_FOR; x := i;", 3, 50, ""},
		{"EXIT on the last pass", "FOR i := 1 TO 3 DO n := n + 1; IF i = 3 THEN EXIT; END_IF; END_FOR; x := i;", 3, 3, ""},
		{"CONTINUE on the last pass still steps", "FOR i := 1 TO 3 DO n := n + 1; IF i = 3 THEN CONTINUE; END_IF; END_FOR; x := i;", 3, 4, ""},
		// The type's limit: TIA's FOR runs these passes and leaves the
		// wrapped value (B1 bench, fixture-tmax); so does nautilus.
		{"to INT's last value wraps", "FOR i := 32765 TO 32767 DO n := n + 1; END_FOR; x := i;", 3, -32768, ""},
		{"variable end at INT's last value", "FOR i := 32765 TO top DO n := n + 1; END_FOR; x := i;", 3, -32768, ""},
		{"down to INT's first value wraps", "FOR i := -32766 TO -32768 BY -1 DO n := n + 1; END_FOR; x := i;", 3, 32767, ""},
		{"USINT 0 TO 255", "FOR u := 0 TO 255 DO n := n + 1; END_FOR; x := u;", 256, 0, ""},
		// A pass the type cannot hold faults, at that pass.
		{"end past the type faults the pass", "FOR i := 32766 TO top + 5 DO n := n + 1; END_FOR; x := i;", 2, 0, "FOR i: 32768 is outside INT (-32768..32767)"},
		{"start below the type faults the first pass", "FOR i := bot - 1 TO 0 DO n := n + 1; END_FOR; x := i;", 0, 0, "FOR i: -32769 is outside INT (-32768..32767)"},
		// No pass with a start the type cannot hold: the exit value wraps.
		{"no pass, start past the type wraps", "FOR i := top + 5 TO 0 DO n := n + 1; END_FOR; x := i;", 0, -32764, ""},
		// 64 bits: the step past LINT's last value ends the loop (it used
		// to wrap round and never end) and the exit value wraps.
		{"LINT to its last value", "FOR l := 9223372036854775806 TO 9223372036854775807 DO n := n + 1; END_FOR; x := l;", 2, math.MinInt64, ""},
		{"LINT down to its first value", "FOR l := -9223372036854775807 TO lmin BY -1 DO n := n + 1; END_FOR; x := l;", 2, math.MaxInt64, ""},
		{"LINT step past its last value", "FOR l := 9223372036854775806 TO 9223372036854775807 BY 5 DO n := n + 1; END_FOR; x := l;", 1, math.MinInt64 + 3, ""},
		{"EXIT of a nested FOR", "FOR u := 1 TO 3 DO FOR i := 1 TO 10 DO IF i = 2 THEN EXIT; END_IF; END_FOR; n := n + i; END_FOR; x := u;", 6, 4, ""},
		{"step zero faults", "FOR i := 1 TO 3 BY z DO n := n + 1; END_FOR;", 0, 0, "FOR step is zero"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, f := runFrame(t, `PROGRAM P
VAR_GLOBAL n : LINT; x : LINT; END_VAR
VAR i : INT; u : USINT; l : LINT; top : INT := 32767; bot : INT := -32768; s : INT := 4; z : INT := 0;
    lmin : LINT := -9223372036854775807 - 1; END_VAR
`+tc.body+`
END_PROGRAM`)
			h := newFakeHost()
			h.vals["n"], h.vals["x"] = ir.IntVal(0), ir.IntVal(0)
			err := ir.Run(p, f, h)
			if tc.fault != "" {
				if err == nil || !strings.Contains(err.Error(), tc.fault) {
					t.Fatalf("err = %v, want %q", err, tc.fault)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if got := h.vals["n"].I; got != tc.passes {
				t.Errorf("passes = %d, want %d", got, tc.passes)
			}
			if tc.fault == "" {
				if got := h.vals["x"].I; got != tc.exit {
					t.Errorf("exit value = %d, want %d", got, tc.exit)
				}
			}
		})
	}
}

// RETURN in a pass leaves the variable as the pass left it; a program's
// VAR keeps it to the next scan.
func TestForReturnKeepsThePassValue(t *testing.T) {
	p, f := runFrame(t, `PROGRAM P
VAR_GLOBAL x : LINT; END_VAR
VAR i : INT; END_VAR
x := i;
FOR i := 1 TO 10 DO
    IF i = 3 THEN RETURN; END_IF;
END_FOR;
END_PROGRAM`)
	h := newFakeHost()
	h.vals["x"] = ir.IntVal(-1)
	scan(t, p, f, h)
	scan(t, p, f, h)
	if got := h.vals["x"].I; got != 3 {
		t.Fatalf("after RETURN in pass 3, i = %d, want 3", got)
	}
}

// A faulting pass stores nothing: the variable keeps the previous pass's
// value (a program's VAR keeps it to the next scan).
func TestForFaultKeepsThePreviousPass(t *testing.T) {
	p, f := runFrame(t, `PROGRAM P
VAR_GLOBAL x : LINT; END_VAR
VAR i : INT; top : INT := 32767; END_VAR
x := i;
FOR i := 32766 TO top + 5 DO END_FOR;
END_PROGRAM`)
	h := newFakeHost()
	h.vals["x"] = ir.IntVal(0)
	if err := ir.Run(p, f, h); err == nil {
		t.Fatal("want the out-of-range pass to fault")
	}
	_ = ir.Run(p, f, h)
	if got := h.vals["x"].I; got != 32767 {
		t.Fatalf("after the fault, i = %d, want 32767 (the last pass that ran)", got)
	}
}
