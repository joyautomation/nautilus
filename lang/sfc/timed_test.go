package sfc

import (
	"strings"
	"testing"
)

// Timed qualifiers (L, D, SD, DS, SL — #190), the IEC textual association
// form (#189), the qualifier diagnostics (#184, #185) and step supervision
// (MAXTIME / Step.ERR / ERROR — #191). The scan-by-scan behaviour is pinned
// in lang/conformance/sfc-timed-qualifiers and sfc-step-maxtime; these are
// the parse, check, lowering-shape and edit round-trip layers.

const timedChart = `PROGRAM P
VAR Lamp : BOOL; Valve : BOOL; Horn : BOOL; Siren : BOOL; Pump : BOOL; tDelay : TIME := T#2S; Ovr : BOOL; END_VAR
SFC
INITIAL_STEP Idle:
  R Horn;
END_STEP
STEP Fill (MAXTIME := T#30S, ERROR := Ovr):
  L  Lamp(T#5S);
  D  Valve(tDelay);
  SD Horn(T#1M);
  Siren(DS, T#10S);
  Pump(SL, T#3S);
  Run(D, T#1S);
END_STEP
TRANSITION FROM Idle TO Fill := TRUE;
END_TRANSITION
TRANSITION FROM Fill TO Idle := Fill.ERR;
END_TRANSITION
ACTION Run:
  Lamp := Fill.X;
END_ACTION
END_SFC
END_PROGRAM
`

func errorsOf(diags []Diagnostic) []Diagnostic {
	var out []Diagnostic
	for _, d := range diags {
		if d.Severity == SeverityError {
			out = append(out, d)
		}
	}
	return out
}

func TestTimedQualifiersParseAndCheckClean(t *testing.T) {
	prog := mustParse(t, timedChart)
	fill := prog.Steps[1]
	want := []Assoc{
		{Qualifier: "L", Target: "Lamp", Time: "T#5S"},
		{Qualifier: "D", Target: "Valve", Time: "tDelay"},
		{Qualifier: "SD", Target: "Horn", Time: "T#1M"},
		{Qualifier: "DS", Target: "Siren", Time: "T#10S"},
		{Qualifier: "SL", Target: "Pump", Time: "T#3S"},
		{Qualifier: "D", Target: "Run", Time: "T#1S"},
	}
	if len(fill.Actions) != len(want) {
		t.Fatalf("Fill.Actions = %+v", fill.Actions)
	}
	for i, w := range want {
		a := fill.Actions[i]
		if a.Qualifier != w.Qualifier || a.Target != w.Target || a.Time != w.Time {
			t.Errorf("assoc %d = %s %s(%s), want %s %s(%s)", i, a.Qualifier, a.Target, a.Time, w.Qualifier, w.Target, w.Time)
		}
	}
	if fill.MaxTime() != "T#30S" || fill.Attr("error") != "Ovr" || fill.AttrText != "MAXTIME := T#30S, ERROR := Ovr" {
		t.Errorf("Fill attrs = %+v (text %q)", fill.Attrs, fill.AttrText)
	}
	if errs := errorsOf(Check(prog)); len(errs) != 0 {
		t.Fatalf("Check = %v, want no errors", errs)
	}
	// ...and it compiles through the ST hop.
	mustCompile(t, timedChart)
}

// TestTimedLoweringStableSlots: each timed association gets its own TON (and
// SD/DS/SL a latch) named by step + target + qualifier — the names a warm
// swap migrates by — and an R resets the latches on its target.
func TestTimedLoweringStableSlots(t *testing.T) {
	st, err := Transpile(timedChart)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"_q_Fill_Lamp_L_tmr : TON;",
		"_q_Fill_Valve_D_tmr : TON;",
		"_q_Fill_Horn_SD_tmr : TON;", "_q_Fill_Horn_SD_ff : BOOL;",
		"_q_Fill_Siren_DS_tmr : TON;", "_q_Fill_Siren_DS_ff : BOOL;",
		"_q_Fill_Pump_SL_tmr : TON;", "_q_Fill_Pump_SL_ff : BOOL;",
		"_q_Fill_Run_D_tmr : TON;",
		"_S_Fill_err : BOOL;",
		"_q_Fill_Valve_D_tmr(IN := _S_Fill_X, PT := tDelay);",
		"_act_Horn_stored := FALSE; _q_Fill_Horn_SD_ff := FALSE;",
		"IF _S_Fill_X AND _S_Fill_t.ET > T#30S THEN _S_Fill_err := TRUE; END_IF;",
		"Ovr := _S_Fill_err;",
		"_en_t", // Fill.ERR in a condition lowers to the flag
	} {
		if !strings.Contains(st, want) {
			t.Errorf("transpiled ST lacks %q:\n%s", want, st)
		}
	}
	if !strings.Contains(st, "_S_Fill_X AND (_S_Fill_err)") {
		t.Errorf("Fill.ERR in the condition did not lower to _S_Fill_err:\n%s", st)
	}
	// A rename-free edit keeps every name: transpiling a chart with an extra
	// step yields the same timed slots.
	st2, err := Transpile(strings.Replace(timedChart, "END_SFC", "STEP Spare:\nEND_STEP\nEND_SFC", 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"_q_Fill_Horn_SD_ff", "_q_Fill_Pump_SL_tmr", "_S_Fill_err"} {
		if !strings.Contains(st2, name+" :") {
			t.Errorf("after adding a step, %s is gone", name)
		}
	}
}

func TestIECAssociationForm(t *testing.T) {
	src := `PROGRAM P
VAR A : BOOL; B : BOOL; C : BOOL; END_VAR
SFC
INITIAL_STEP S:
  A(D, T#3S);
  B(N);
  C();
  N A;
END_STEP
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	got := prog.Steps[0].Actions
	if got[0].Qualifier != "D" || got[0].Target != "A" || got[0].Time != "T#3S" {
		t.Errorf("A(D, T#3S) = %+v", got[0])
	}
	if got[1].Qualifier != "N" || got[1].Target != "B" || got[1].Time != "" {
		t.Errorf("B(N) = %+v", got[1])
	}
	if got[2].Qualifier != "N" || got[2].Target != "C" {
		t.Errorf("C() = %+v (an omitted qualifier is N)", got[2])
	}
	if errs := errorsOf(Check(prog)); len(errs) != 0 {
		t.Errorf("Check = %v", errs)
	}

	for _, c := range []struct{ assoc, want string }{
		{"A(N, T#1S, Ind);", "indicator variables"},
		{"A(T#3S);", "expected a qualifier first"},
		{"A(D, );", "expected a duration"},
		{"N A(T#1S, T#2S);", "one duration"},
		{"T#3S;", "an association is"},
	} {
		_, err := Parse(strings.Replace(src, "N A;", c.assoc, 1))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Parse(%s) err = %v, want it to contain %q", c.assoc, err, c.want)
			continue
		}
		// Every association error says what IS supported.
		if !strings.Contains(err.Error(), "Target(Q, T#3S)") {
			t.Errorf("Parse(%s) err = %v, want the supported forms listed", c.assoc, err)
		}
	}
}

func timedCheck(t *testing.T, step, extraVars string) []Diagnostic {
	t.Helper()
	src := `PROGRAM P
VAR Lamp : BOOL; Ovr : BOOL; n : INT; ` + extraVars + ` END_VAR
SFC
INITIAL_STEP Idle:
END_STEP
` + step + `
TRANSITION FROM Idle TO Fill := TRUE;
END_TRANSITION
TRANSITION FROM Fill TO Idle := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	return Check(mustParse(t, src))
}

func TestCheckQualifierDiagnostics(t *testing.T) {
	// #184: an unknown qualifier names the whole supported set, the timed
	// ones included, and the Step.T recipe for anything else.
	d := timedCheck(t, "STEP Fill:\n  Q Lamp;\nEND_STEP", "")
	wantDiag(t, d, SeverityError, `unknown action qualifier "Q"; supported qualifiers are N, S, R, P, P0, P1 and the timed L, D, SD, DS, SL`)
	wantDiag(t, d, SeverityError, "Fill.T >= T#3S")

	// a timed qualifier without its duration
	wantDiag(t, timedCheck(t, "STEP Fill:\n  D Lamp;\nEND_STEP", ""), SeverityError, "timed qualifier D needs a duration: `D Lamp(T#3S);`")
	// ...or with something that is not one
	wantDiag(t, timedCheck(t, "STEP Fill:\n  L Lamp(3 + 4);\nEND_STEP", ""), SeverityError, `timed qualifier L: "3 + 4" is not a duration`)
	// an untimed qualifier still refuses a duration
	wantDiag(t, timedCheck(t, "STEP Fill:\n  N Lamp(T#3S);\nEND_STEP", ""), SeverityError, "only the timed qualifiers L, D, SD, DS, SL do")
	// every timed qualifier is accepted
	for _, q := range []string{"L", "D", "SD", "DS", "SL"} {
		if errs := errorsOf(timedCheck(t, "STEP Fill:\n  "+q+" Lamp(T#3S);\nEND_STEP", "")); len(errs) != 0 {
			t.Errorf("%s Lamp(T#3S): %v", q, errs)
		}
	}
}

// TestCheckTimedAssocVsActionWrite (#185): the association-vs-ACTION warning
// describes a timed association's window, not a pulse.
func TestCheckTimedAssocVsActionWrite(t *testing.T) {
	src := `PROGRAM P
VAR Det : BOOL; END_VAR
SFC
INITIAL_STEP Idle:
END_STEP
STEP Fill:
  D Det(T#3S);
  N Dose;
  L Det(T#9S);
  Q Det;
END_STEP
TRANSITION FROM Idle TO Fill := TRUE;
END_TRANSITION
TRANSITION FROM Fill TO Idle := TRUE;
END_TRANSITION
ACTION Dose:
  Det := TRUE;
END_ACTION
END_SFC
END_PROGRAM
`
	d := Check(mustParse(t, src))
	wantDiag(t, d, SeverityWarning, "Det is driven by a qualifier association (D) on step Fill and assigned in ACTION Dose — the association wins once Fill has been active T#3S, until it deactivates")
	wantDiag(t, d, SeverityWarning, "(L) on step Fill and assigned in ACTION Dose — the association wins for the first T#9S Fill is active")
	wantNoDiag(t, d, "pulse")
	wantNoDiag(t, d, "association (Q)")
}

func TestCheckStepAttributes(t *testing.T) {
	ok := timedCheck(t, "STEP Fill (MAXTIME := T#30S, ERROR := Ovr):\nEND_STEP", "")
	if errs := errorsOf(ok); len(errs) != 0 {
		t.Fatalf("supervised step: %v", errs)
	}
	for _, c := range []struct{ step, want string }{
		{"STEP Fill (MAXTIME := 30):\nEND_STEP", "MAXTIME takes a positive TIME literal"},
		{"STEP Fill (MAXTIME := tMax):\nEND_STEP", "MAXTIME takes a positive TIME literal"},
		{"STEP Fill (MAXTIME := T#30S, ERROR := Nope):\nEND_STEP", `"Nope" is not a declared variable`},
		{"STEP Fill (ERROR := Ovr):\nEND_STEP", "ERROR without MAXTIME"},
		{"STEP Fill (MINTIME := T#1S):\nEND_STEP", "unknown step attribute MINTIME"},
		{"STEP Fill (MAXTIME := T#1S, MAXTIME := T#2S):\nEND_STEP", "MAXTIME is given twice"},
	} {
		wantDiag(t, timedCheck(t, c.step, "tMax : TIME;"), SeverityError, c.want)
	}
	// Step.ERR on a step with no MAXTIME
	src := `PROGRAM P
VAR END_VAR
SFC
INITIAL_STEP Idle:
END_STEP
STEP Fill:
END_STEP
TRANSITION FROM Idle TO Fill := TRUE;
END_TRANSITION
TRANSITION FROM Fill TO Idle := Fill.ERR;
END_TRANSITION
END_SFC
END_PROGRAM
`
	wantDiag(t, Check(mustParse(t, src)), SeverityError, "Fill.ERR is the step's overrun flag, but step Fill has no MAXTIME")
	if _, err := Parse(strings.Replace(src, "STEP Fill:", "STEP Fill (MAXTIME T#1S):", 1)); err == nil || !strings.Contains(err.Error(), "NAME := value") {
		t.Errorf("malformed attribute list: err = %v", err)
	}
}

// TestEditKeepsStepAttributes: an op that reprints a step (an association
// edit, a rename) keeps its MAXTIME/ERROR list, and the render model carries
// it for the chart.
func TestEditKeepsStepAttributes(t *testing.T) {
	m := mustGraph(t, timedChart)
	fill := findStepT(t, m, "st:Fill")
	if fill.MaxTime != "T#30S" || fill.Attrs != "MAXTIME := T#30S, ERROR := Ovr" {
		t.Fatalf("GStep = %+v", fill)
	}
	res, m2 := applyOp(t, timedChart, EditOp{Type: "addAssoc", Step: "st:Fill", Qualifier: "N", Target: "Lamp", Index: ip(0)})
	if !strings.Contains(res, "STEP Fill (MAXTIME := T#30S, ERROR := Ovr):") || findStepT(t, m2, "st:Fill").MaxTime != "T#30S" {
		t.Fatalf("addAssoc dropped the attributes:\n%s", res)
	}
	// the IEC-form associations are reprinted in nautilus's form, same meaning
	if !strings.Contains(res, "DS Siren(T#10S);") {
		t.Errorf("reprinted step:\n%s", res)
	}
	res2, _ := applyOp(t, res, EditOp{Type: "renameStep", Step: "st:Fill", NewName: "Charge"})
	if !strings.Contains(res2, "STEP Charge (MAXTIME := T#30S, ERROR := Ovr):") {
		t.Fatalf("renameStep dropped the attributes:\n%s", res2)
	}
}
