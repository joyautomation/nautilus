package st

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// scanN lowers src, runs it n times against one host and frame, and
// returns the host so a test can read the tags it wrote.
func scanN(t *testing.T, src string, n int, seed map[string]ir.Value) (*stubHost, *ir.Program, *ir.Frame) {
	t.Helper()
	prog := lowerSource(t, src)
	h := newStubHost()
	for k, v := range seed {
		h.globals[k] = v
	}
	frame := ir.NewFrame(prog)
	for i := 0; i < n; i++ {
		if err := ir.Run(prog, frame, h); err != nil {
			t.Fatalf("scan %d: %v", i+1, err)
		}
	}
	return h, prog, frame
}

// #196: the issue's repro. Later clauses no longer fold into the first.
func TestCaseNamedConstantLabels(t *testing.T) {
	src := `
PROGRAM P
VAR_EXTERNAL Cnt : INT; St : INT; END_VAR
VAR CONSTANT S_IDLE : INT := 0; S_RUN : INT := 10; END_VAR
CASE St OF
    S_IDLE: St := S_RUN;
    S_RUN:  Cnt := Cnt + 1;
END_CASE;
END_PROGRAM`
	seed := map[string]ir.Value{"Cnt": ir.IntVal(0), "St": ir.IntVal(0)}
	for scans, want := range []int64{0, 0, 1, 2, 3, 4} {
		if scans == 0 {
			continue
		}
		h, _, _ := scanN(t, src, scans, seed)
		if got := h.globals["Cnt"].I; got != want {
			t.Errorf("after %d scans Cnt = %d, want %d", scans, got, want)
		}
		if got := h.globals["St"].I; got != 10 {
			t.Errorf("after %d scans St = %d, want 10", scans, got)
		}
	}
}

// Constant labels mixed with literals, a list, a range bounded by constants,
// and a negated constant.
func TestCaseConstantLabelForms(t *testing.T) {
	src := `
PROGRAM P
VAR_EXTERNAL In : INT; Out : INT; END_VAR
VAR CONSTANT A : INT := 1; B : INT := 5; C : INT := 9; END_VAR
CASE In OF
    A, 2:       Out := 100;
    B..C:       Out := 200;
    -A:         Out := 400;
ELSE
    Out := 0;
END_CASE;
END_PROGRAM`
	for in, want := range map[int64]int64{1: 100, 2: 100, 5: 200, 9: 200, -1: 400, 3: 0} {
		h, _, _ := scanN(t, src, 1, map[string]ir.Value{"In": ir.IntVal(in), "Out": ir.IntVal(-7)})
		if got := h.globals["Out"].I; got != want {
			t.Errorf("In=%d: Out = %d, want %d", in, got, want)
		}
	}
}

// Two labels with the same value are an error naming both: two constants,
// a constant and a literal, a value inside a range.
func TestCaseDuplicateLabelValues(t *testing.T) {
	head := "PROGRAM P\nVAR s, o : INT; END_VAR\nVAR CONSTANT S_A : INT := 0; S_B : INT := 0; S_C : INT := 10; END_VAR\n"
	cases := []struct{ body, want string }{
		{"CASE s OF\nS_A: o := 1;\nS_B: o := 2;\nEND_CASE;\n", "duplicate CASE label: S_B (= 0) has the same value as S_A (= 0) on line 5"},
		{"CASE s OF\n10: o := 1;\nS_C: o := 2;\nEND_CASE;\n", "duplicate CASE label: S_C (= 10) has the same value as 10 on line 5"},
		{"CASE s OF\nS_C: o := 1;\n5..20: o := 2;\nEND_CASE;\n", "duplicate CASE label: 5..20 overlaps S_C (= 10) on line 5"},
		{"CASE s OF\n1, 1: o := 1;\nEND_CASE;\n", "duplicate CASE label: 1 has the same value as 1"},
	}
	for _, c := range cases {
		lowerExpectErr(t, head+c.body+"END_PROGRAM\n", c.want)
	}
}

// A label must be a constant: a variable is an error, not a run-time compare.
func TestCaseLabelMustBeConstant(t *testing.T) {
	lowerExpectErr(t, `
PROGRAM P
VAR s, o, v : INT; END_VAR
CASE s OF
  v: o := 1;
END_CASE;
END_PROGRAM`, "CASE label v is not a constant")
}

// A VAR CONSTANT cannot be written (it folds to its value everywhere).
func TestConstantCannotBeWritten(t *testing.T) {
	lowerExpectErr(t, `
PROGRAM P
VAR CONSTANT K : INT := 3; END_VAR
K := 4;
END_PROGRAM`, "K is a constant (VAR CONSTANT) and cannot be written")
	lowerExpectErr(t, `
PROGRAM P
VAR CONSTANT K : INT := 3; END_VAR
FOR K := 1 TO 2 DO END_FOR;
END_PROGRAM`, "is a constant")
}

// Constants feed array bounds and other constants.
func TestConstantInBoundsAndInitialValues(t *testing.T) {
	src := `
PROGRAM P
VAR_EXTERNAL Out : INT; END_VAR
VAR CONSTANT N : INT := 4; M : INT := N * 2; END_VAR
VAR a : ARRAY[1..M] OF INT; i : INT; start : INT := N + 1; END_VAR
FOR i := 1 TO M DO a[i] := i; END_FOR;
Out := a[M] + start;
END_PROGRAM`
	h, _, _ := scanN(t, src, 1, map[string]ir.Value{"Out": ir.IntVal(0)})
	if got := h.globals["Out"].I; got != 13 {
		t.Errorf("Out = %d, want 13 (a[8] + 5)", got)
	}
}
