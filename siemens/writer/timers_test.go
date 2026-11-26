package writer

import (
	"regexp"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// liveReads are the reads in body of a timer instance's own Q or ET
// (not its latch), which the CPU refreshes from the clock on every access
// (#1385).
func liveReads(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`(\w+)"?\.(Q|ET)\b`).FindAllStringSubmatch(body, -1) {
		if !regexp.MustCompile(`^naut_\w+_last\d*$`).MatchString(m[1]) {
			out = append(out, m[0])
		}
	}
	return out
}

// TestTimerOutputsAreLatched (#1385): an S7-1500 IEC timer updates Q and
// ET on every read; nautilus returns what the last call left. Each
// call binds Q and ET into the timer's latch, and every read in the
// program (before the call, twice in a scan, in an expression, as an
// argument, after a conditional call, through an output binding) reads
// the latch, never the timer.
func TestTimerOutputsAreLatched(t *testing.T) {
	scl, _ := write(t, "PROGRAM P\nVAR t : TON; f : TOF; p : TP; c : TON; x : BOOL; y : BOOL; e : TIME; e2 : TIME; END_VAR\n"+
		"x := t.Q;\ne := t.ET;\n"+ // before the call
		"t(IN := NOT x, PT := T#1s);\n"+
		"e2 := t.ET;\ny := t.Q AND t.ET > T#500ms;\n"+ // after, twice, in an expression
		"c(IN := t.Q OR f.Q, PT := t.PT);\n"+ // as an argument; an input stays the timer's
		"IF x THEN f(IN := x, PT := T#2s, Q => y, ET => e); END_IF;\n"+ // a conditional call, bindings
		"y := f.Q AND p.Q;\ne := p.ET;\n"+ // p never called
		"END_PROGRAM\n")
	body := scl[strings.Index(scl, "BEGIN"):]
	if m := liveReads(body); m != nil {
		t.Errorf("the body reads a timer's own Q/ET %v:\n%s", m, body)
	}
	wantLines(t, scl,
		"\t#x := #naut_t_last.Q;\n\t#e := #naut_t_last.ET;\n",
		"\t#t(IN := NOT #x, PT := T#1s, Q => #naut_t_last.Q, ET => #naut_t_last.ET);\n",
		"\t#e2 := #naut_t_last.ET;\n\t#y := #naut_t_last.Q AND #naut_t_last.ET > T#500ms;\n",
		"\t#c(IN := #naut_t_last.Q OR #naut_f_last.Q, PT := #t.PT, Q => #naut_c_last.Q, ET => #naut_c_last.ET);\n",
		"\tIF #x THEN\n\t\t#f(IN := #x, PT := T#2s, Q => #naut_f_last.Q, ET => #naut_f_last.ET);\n\t\t#y := #naut_f_last.Q;\n\t\t#e := #naut_f_last.ET;\n\tEND_IF;\n",
		"\t#y := #naut_f_last.Q AND #naut_p_last.Q;\n\t#e := #naut_p_last.ET;\n",
	)
	// Only the call writes a latch: no assignment to one anywhere.
	if strings.Contains(body, "_last.Q :=") || strings.Contains(body, "_last.ET :=") {
		t.Errorf("a latch is assigned outside its timer's call:\n%s", body)
	}
}

// TestTimerOutputsLatchedFromOutsideABlock: a block's timer read from
// the caller (m.t.Q, one block deeper too) reads that block's latch.
// (Output.Symbol keeps the instance's own pins: the Web API's read and
// naut siemens serve's browse name them, and only the call sets them.)
func TestTimerOutputsLatchedFromOutsideABlock(t *testing.T) {
	lib := "FUNCTION_BLOCK Inner\nVAR_INPUT go : BOOL; END_VAR\nVAR t : TP; END_VAR\nt(IN := go, PT := T#1s);\nEND_FUNCTION_BLOCK\n" +
		"FUNCTION_BLOCK M\nVAR_OUTPUT q : BOOL; END_VAR\nVAR t : TON; n : Inner; END_VAR\nq := t.Q;\nt(IN := TRUE, PT := T#1s);\nn(go := t.Q);\nEND_FUNCTION_BLOCK\n"
	out, diags := writePOU(t, "PROGRAM P\nVAR m : M; x : BOOL; e : TIME; END_VAR\nx := m.t.Q;\nm();\ne := m.t.ET;\nx := m.n.t.Q;\nEND_PROGRAM\n", lib)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	p, m := fileOf(t, out, "naut_P.scl"), fileOf(t, out, "naut_M.scl")
	wantLines(t, p, "\t#x := #m.naut_t_last.Q;\n", "\t#e := #m.naut_t_last.ET;\n", "\t#x := #m.n.naut_t_last.Q;\n")
	wantLines(t, m, "\t#q := #naut_t_last.Q;\n", "\t#n(go := #naut_t_last.Q);\n")
	for _, f := range []string{p, m, fileOf(t, out, "naut_Inner.scl")} {
		if body := f[strings.Index(f, "BEGIN"):]; liveReads(body) != nil {
			t.Errorf("a body reads a timer's own Q/ET:\n%s", body)
		}
	}
}

// TestTimerLatchBeforeTheFirstCall: an uncalled nautilus timer's Q and
// ET are FALSE and 0; the latch is declared with no start value, so TIA
// starts it at FALSE and T#0s too.
func TestTimerLatchBeforeTheFirstCall(t *testing.T) {
	for typ := range timers {
		inst := ir.NewFBInstance(ir.LookupFBType(typ).FB)
		for i, s := range inst.Def.AllSlots() {
			if timerOut(s.Name) && (inst.Slots[i].B || inst.Slots[i].I != 0) {
				t.Errorf("nautilus's uncalled %s has %s = %+v", typ, s.Name, inst.Slots[i])
			}
		}
	}
	scl, _ := write(t, "PROGRAM P\nVAR t : TON; x : BOOL; END_VAR\nx := t.Q;\nEND_PROGRAM\n")
	wantLines(t, scl, "      naut_t_last : Struct\n         Q : Bool;\n         ET : Time;\n      END_STRUCT;\n")
}

// TestTimerLatchNameNeverTakesAUsersName: a variable the program
// declares keeps its name, in any case; the latch takes the next number.
func TestTimerLatchNameNeverTakesAUsersName(t *testing.T) {
	scl, _ := write(t, "PROGRAM P\nVAR NAUT_T_LAST : BOOL; t : TON; naut_t_last2 : INT; END_VAR\nNAUT_T_LAST := t.Q;\nt(IN := TRUE, PT := T#1s);\nEND_PROGRAM\n")
	wantLines(t, scl, "      naut_t_last3 : Struct\n", "\t#NAUT_T_LAST := #naut_t_last3.Q;\n", "Q => #naut_t_last3.Q, ET => #naut_t_last3.ET);\n")
	// The timer's case differs from the user's variable.
	scl, _ = write(t, "PROGRAM P\nVAR naut_t_last : BOOL; T : TON; END_VAR\nnaut_t_last := T.Q;\nT(IN := TRUE, PT := T#1s);\nEND_PROGRAM\n")
	wantLines(t, scl, "      naut_T_last2 : Struct\n", "\t#naut_t_last := #naut_T_last2.Q;\n")
	// A library's global constant is copied into the block by its name.
	out, diags := writePOU(t, "PROGRAM P\nVAR t : TON; n : INT; END_VAR\nn := naut_t_last;\nt(IN := TRUE, PT := T#1s);\nEND_PROGRAM\n", "VAR_GLOBAL CONSTANT naut_t_last : INT := 5; END_VAR\n")
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	wantLines(t, fileOf(t, out, "naut_P.scl"), "      naut_t_last2 : Struct\n", "Q => #naut_t_last2.Q, ET => #naut_t_last2.ET);\n")
}

// TestRetainedTimerStartsOver: nautilus starts a timer over after a
// restart even in VAR RETAIN (runtime/retainlocals.go); TIA would keep a
// timer in a RETAIN section, so it and its latch go in the plain VAR
// section.
func TestRetainedTimerStartsOver(t *testing.T) {
	scl, _ := write(t, "PROGRAM P\nVAR RETAIN t : TON; n : INT; END_VAR\nt(IN := TRUE, PT := T#1s);\nn := 1;\nEND_PROGRAM\n")
	retain, plain := declSection(scl, "VAR RETAIN"), declSection(scl, "VAR ")
	if strings.Contains(retain, "t : TON_TIME") || strings.Contains(retain, "naut_t_last") || !strings.Contains(retain, "n : INT") {
		t.Errorf("VAR RETAIN section:\n%s", retain)
	}
	if !strings.Contains(plain, "t : TON_TIME;") || !strings.Contains(plain, "naut_t_last : Struct") {
		t.Errorf("VAR section:\n%s", plain)
	}
}

// declSection is the declaration section of scl that starts with header.
func declSection(scl, header string) string {
	i := strings.Index(scl, "   "+header+"\n")
	if i < 0 {
		i = strings.Index(scl, "   "+header)
	}
	if i < 0 {
		return ""
	}
	end := strings.Index(scl[i:], "END_VAR")
	return scl[i : i+end]
}

// TestTimerOutputWritesRefused: a write to a timer's Q or ET is refused,
// as an assignment, another instance's output binding or a block's
// in-out: the CPU's timer would not keep it, and nautilus's TP keeps its
// pulse in Q. A timer the writer cannot latch is refused as before.
func TestTimerOutputWritesRefused(t *testing.T) {
	lib := "FUNCTION_BLOCK H\nVAR_IN_OUT io : BOOL; END_VAR\nio := NOT io;\nEND_FUNCTION_BLOCK\n"
	for _, c := range []struct{ src, want string }{
		{"PROGRAM P\nVAR t : TON; END_VAR\nt.Q := TRUE;\nEND_PROGRAM\n", "a write to t.Q, a timer's output"},
		{"PROGRAM P\nVAR p : TP; END_VAR\np.ET := T#1s;\nEND_PROGRAM\n", "a write to p.ET, a timer's output"},
		{"PROGRAM P\nVAR t : TON; r : R_TRIG; END_VAR\nr(CLK := TRUE, Q => t.Q);\nEND_PROGRAM\n", "a write to t.Q, a timer's output"},
		{"PROGRAM P\nVAR t : TOF; h : H; END_VAR\nh(io := t.Q);\nEND_PROGRAM\n", "a write to t.Q, a timer's output"},
		{"PROGRAM P\nVAR p : TP; s : SR; x : BOOL; END_VAR\ns(S1 := x, R := FALSE, Q1 => p.Q);\nEND_PROGRAM\n", "a write to p.Q, a timer's output"},
		{"PROGRAM P\nVAR t : TON; u : TON; x : BOOL; END_VAR\nt(IN := TRUE, PT := T#1s);\nu := t;\nx := u.Q;\nEND_PROGRAM\n", "assigning a whole timer instance"},
		{"PROGRAM P\nVAR t : ARRAY[0..1] OF TON; x : BOOL; END_VAR\nx := t[0].Q;\nEND_PROGRAM\n", "an array of function block instances"},
	} {
		_, diags := writePOU(t, c.src, lib)
		if !hasDiag(diags, c.want) {
			t.Errorf("%q: want a refusal with %q, got %v", c.src, c.want, diags)
		}
	}
}

func hasDiag(diags []Diag, want string) bool {
	for _, d := range diags {
		if strings.Contains(d.Message, want) {
			return true
		}
	}
	return false
}
