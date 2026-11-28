package runtime

import "testing"

// The exact VAR_IN_OUT copy-back for instances (#1458 slice 3b,
// lang/ir/inout.go, scanView.WriteGlobalInOut): a block that takes an
// instance as an in-out pin commits only the members it assigned on that
// call. Each test parks a scan mid-logic, writes from outside to members the
// block does not assign, and checks they survive the commit. Plain structs
// keep the old rule, the whole pin (design question 7).

const inoutLib = `
TYPE
  PumpCmd : STRUCT
    Start    : BOOL;
    Stop     : BOOL;
    SpeedRef : REAL;
  END_STRUCT;
  Pump : STRUCT
    Running  : BOOL;
    Speed    : REAL;
    RunHours : REAL;
    Note     : INT;
    Cmd      : PumpCmd;
  END_STRUCT;
  Bank : STRUCT
    Pumps : ARRAY[1..2] OF PumpCmd;
    Note  : INT;
  END_STRUCT;
END_TYPE

FUNCTION_BLOCK Reader
  VAR_IN_OUT P : Pump; END_VAR
  VAR_OUTPUT On : BOOL; END_VAR
  On := P.Running;
END_FUNCTION_BLOCK

FUNCTION_BLOCK Starter
  VAR_IN_OUT P : Pump; END_VAR
  VAR_INPUT Go : BOOL; END_VAR
  IF Go THEN
    P.Cmd.Start := TRUE;
  END_IF;
END_FUNCTION_BLOCK

FUNCTION_BLOCK Stopper
  VAR_IN_OUT Q : Pump; END_VAR
  Q.Cmd.Stop := TRUE;
END_FUNCTION_BLOCK

FUNCTION_BLOCK Outer
  VAR_IN_OUT P : Pump; END_VAR
  VAR S : Stopper; END_VAR
  S(Q := P);
END_FUNCTION_BLOCK

FUNCTION_BLOCK CmdStart
  VAR_IN_OUT C : PumpCmd; END_VAR
  C.Start := TRUE;
END_FUNCTION_BLOCK

FUNCTION_BLOCK Counter
  VAR_IN_OUT N : INT; END_VAR
  FOR N := 1 TO 3 DO
  END_FOR;
END_FUNCTION_BLOCK
`

func newInOutRuntime(t *testing.T, clk Clock, vars, body string, tags ...TagDef) *Runtime {
	t.Helper()
	pump := InstanceDef{Members: map[string]MemberDef{
		"Running": {Kind: MemberFrom}, "Speed": {Kind: MemberFrom},
		"Cmd.Start": {Kind: MemberTo}, "Cmd.Stop": {Kind: MemberTo}, "Cmd.SpeedRef": {Kind: MemberTo},
	}}
	all := append([]TagDef{
		InstanceTag("P101", "Pump", pump),
		InstanceTag("B1", "Bank", InstanceDef{}),
		Typed("Plain", RoleState, "Pump"),
	}, tags...)
	r, err := New(Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL P101 : Pump; B1 : Bank; Plain : Pump; " + externs(tags) + " END_VAR\n" +
			"VAR wall : LOCAL_TIME; " + vars + " END_VAR\nwall();\n" + body + "\nEND_PROGRAM",
		Libraries: []string{inoutLib},
		Driver:    &instDriver{},
		Tags:      all,
		Clock:     clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A block that only reads the instance commits nothing: an operator's
// writes to a command and to memory, landed mid-scan, survive.
func TestInOutReaderCommitsNothing(t *testing.T) {
	clk := newGateClock()
	r := newInOutRuntime(t, clk, "R : Reader;", "R(P := P101);")
	parkedScan(t, r, clk, func() {
		write(t, r, "P101.Cmd.Start", true)
		write(t, r, "P101.RunHours", 6.0)
	})
	wantPath(t, r, "P101.Cmd.Start", true)
	wantPath(t, r, "P101.RunHours", 6.0)
}

// A conditional assignment commits only on the call that assigns: the
// record is taken per call, not from the block's text.
func TestInOutConditionalAssignment(t *testing.T) {
	clk := newGateClock()
	r := newInOutRuntime(t, clk, "S : Starter; Go : BOOL;", "S(P := P101, Go := Go);")
	parkedScan(t, r, clk, func() {
		write(t, r, "P101.Cmd.Start", false) // false either way: Go is FALSE
		write(t, r, "P101.Cmd.SpeedRef", 40.0)
		write(t, r, "P101.RunHours", 6.0)
	})
	wantPath(t, r, "P101.Cmd.SpeedRef", 40.0)
	wantPath(t, r, "P101.RunHours", 6.0)

	clk2 := newGateClock()
	r2 := newInOutRuntime(t, clk2, "S : Starter;", "S(P := P101, Go := TRUE);")
	parkedScan(t, r2, clk2, func() {
		write(t, r2, "P101.Cmd.Start", false)
		write(t, r2, "P101.RunHours", 6.0)
	})
	wantPath(t, r2, "P101.Cmd.Start", true) // the block assigned it: it wins
	wantPath(t, r2, "P101.RunHours", 6.0)
}

// A pin passed on to an inner block: the outer commits only what the inner
// assigned.
func TestInOutNestedBlocks(t *testing.T) {
	clk := newGateClock()
	r := newInOutRuntime(t, clk, "O : Outer;", "O(P := P101);")
	parkedScan(t, r, clk, func() {
		write(t, r, "P101.RunHours", 6.0)
		write(t, r, "P101.Cmd.Start", true)
	})
	wantPath(t, r, "P101.Cmd.Stop", true)
	wantPath(t, r, "P101.RunHours", 6.0)
	wantPath(t, r, "P101.Cmd.Start", true)
}

// A pin bound to a member of the instance: the rest of the instance, and
// the rest of that member, survive.
func TestInOutSubStructPin(t *testing.T) {
	clk := newGateClock()
	r := newInOutRuntime(t, clk, "C : CmdStart;", "C(C := P101.Cmd);")
	parkedScan(t, r, clk, func() {
		write(t, r, "P101.Cmd.SpeedRef", 40.0)
		write(t, r, "P101.RunHours", 6.0)
	})
	wantPath(t, r, "P101.Cmd.Start", true)
	wantPath(t, r, "P101.Cmd.SpeedRef", 40.0)
	wantPath(t, r, "P101.RunHours", 6.0)
}

// A pin bound to an array element of an instance.
func TestInOutArrayElementPin(t *testing.T) {
	clk := newGateClock()
	r := newInOutRuntime(t, clk, "C : CmdStart;", "C(C := B1.Pumps[2]);")
	parkedScan(t, r, clk, func() {
		write(t, r, "B1.Pumps[1].SpeedRef", 40.0)
		write(t, r, "B1.Pumps[2].SpeedRef", 41.0)
		write(t, r, "B1.Note", int64(7))
	})
	wantPath(t, r, "B1.Pumps[2].Start", true)
	wantPath(t, r, "B1.Pumps[1].SpeedRef", 40.0)
	wantPath(t, r, "B1.Pumps[2].SpeedRef", 41.0)
	wantPath(t, r, "B1.Note", int64(7))
}

// A store the record cannot describe (a FOR counter) takes the pin whole,
// the old rule; the rest of the instance still survives.
func TestInOutUndescribableStoreTakesThePinWhole(t *testing.T) {
	clk := newGateClock()
	r := newInOutRuntime(t, clk, "K : Counter;", "K(N := P101.Note);")
	parkedScan(t, r, clk, func() {
		write(t, r, "P101.Note", int64(9))
		write(t, r, "P101.RunHours", 6.0)
	})
	wantPath(t, r, "P101.Note", int64(4)) // the FOR loop's exit value
	wantPath(t, r, "P101.RunHours", 6.0)
}

// A plain struct keeps the old rule: the whole pin is the program's, so a
// member written mid-scan is replaced by the block's copy (design Q7).
func TestInOutPlainStructKeepsTheOldRule(t *testing.T) {
	clk := newGateClock()
	r := newInOutRuntime(t, clk, "R : Reader;", "R(P := Plain);")
	parkedScan(t, r, clk, func() { write(t, r, "Plain.RunHours", 6.0) })
	wantPath(t, r, "Plain.RunHours", 0.0)
}

// The record is the call's own: a call that assigns, then one that does
// not, commits nothing on the second, so a member written mid-scan before
// it survives.
func TestInOutRecordIsPerCall(t *testing.T) {
	clk := newGateClock()
	r := newInOutRuntime(t, clk, "S : Starter;", "S(P := P101, Go := Go);", State("Go", true))
	r.Scan() // assigns Cmd.Start
	wantPath(t, r, "P101.Cmd.Start", true)
	write(t, r, "Go", false)
	parkedScan(t, r, clk, func() { write(t, r, "P101.Cmd.Start", false) })
	wantPath(t, r, "P101.Cmd.Start", false)
}

// externs declares the extra tags a test adds (all BOOL here).
func externs(tags []TagDef) string {
	s := ""
	for _, d := range tags {
		s += d.Name + " : BOOL; "
	}
	return s
}

// Recording what a block assigns reuses its storage: a scan that copies an
// instance back exactly allocates no more than the same scan copying a
// plain struct back whole, the old way.
func TestInOutRecordAllocatesNoMoreThanTheOldCopyBack(t *testing.T) {
	scanAllocs := func(target string) float64 {
		r := newInOutRuntime(t, newGateClock(), "S : Starter; O : Outer;", "S(P := "+target+", Go := TRUE);\nO(P := "+target+");")
		r.Scan()
		r.Scan()
		return testing.AllocsPerRun(50, r.Scan)
	}
	exact, old := scanAllocs("P101"), scanAllocs("Plain")
	if exact > old {
		t.Errorf("a scan with the exact copy-back allocates %v, the old copy-back %v", exact, old)
	}
}
