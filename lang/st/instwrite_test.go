package st

import (
	"strings"
	"testing"
)

// The pump of one-struct-model §2.4: Running and Healthy come from the
// device (from:), Cmd.* are commands (to:), Cmd.Ack is read back from the
// device, RunHours is memory, Levels[1] is read, Rate is computed.
var pumpKinds = InstanceKinds{
	"P101": {
		"Running":   "from",
		"Healthy":   "from",
		"Cmd.Start": "to",
		"Cmd.Stop":  "to",
		"Cmd.Ack":   "from",
		"Levels[1]": "from",
		"Rate":      "expr",
		"RunHours":  "memory",
	},
	"P102": {"Running": "from", "Cmd.Start": "to"},
	// A whole sub-struct read from the device: everything under it is read-only.
	"P104": {"Cmd": "from", "Cmd.Stop": "memory"},
}

const pumpTypes = `TYPE Cmd : STRUCT Start : BOOL; Stop : BOOL; Ack : BOOL; END_STRUCT; END_TYPE
TYPE Pump : STRUCT
  Running : BOOL; Healthy : BOOL; Cmd : Cmd; RunHours : REAL;
  Levels : ARRAY[0..3] OF REAL; Rate : REAL;
END_STRUCT; END_TYPE
`

const pumpCtl = `FUNCTION_BLOCK PumpCtl
  VAR_IN_OUT P : Pump; END_VAR
  VAR_INPUT Demand : BOOL; END_VAR
  P.Cmd.Start := Demand AND P.Healthy AND NOT P.Running;
  P.Cmd.Stop  := NOT Demand AND P.Running;
  IF P.Running THEN
    P.RunHours := P.RunHours + 0.1 / 3600.0;
  END_IF;
END_FUNCTION_BLOCK
`

func writes(t *testing.T, src string, kinds InstanceKinds) []InstanceWrite {
	t.Helper()
	prog, err := Parse(pumpTypes + src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return InstanceWrites(prog, kinds)
}

// The PumpCtl example of §2.4 writes only commands and memory, so it passes
// an instance with no refusal; the same block on a plain local struct is
// fine too.
func TestInstanceWritesAcceptsPumpCtl(t *testing.T) {
	src := pumpCtl + `PROGRAM Main
VAR Ctl1, Ctl2 : PumpCtl; Spare : Pump; Level : REAL; END_VAR
  Ctl1(P := P101, Demand := Level > 5.0);
  Ctl2(P := Spare, Demand := TRUE);
  P101.Cmd.Start := TRUE;
  P101.RunHours := 0.0;
  Spare.Running := TRUE;
END_PROGRAM
`
	if got := writes(t, src, pumpKinds); len(got) != 0 {
		t.Errorf("refused: %+v", got)
	}
}

// Each way a program can write a read-only member itself.
func TestInstanceWritesDirect(t *testing.T) {
	for _, c := range []struct {
		name, body string
		member     string
		kind       string
		whole      bool
	}{
		{"a from: member", "P101.Running := TRUE;", "Running", "from", false},
		{"any case", "p101.RUNNING := TRUE;", "Running", "from", false},
		{"an expr: member", "P101.Rate := 1.0;", "Rate", "expr", false},
		{"a member of a from: sub-struct", "P101.Cmd.Ack := TRUE;", "Cmd.Ack", "from", false},
		{"an element that is from:", "P101.Levels[1] := 2.0;", "Levels[1]", "from", false},
		{"an element by a variable index (it may be the read one)", "P101.Levels[i] := 2.0;", "Levels[1]", "from", false},
		{"a sub-struct holding a from: member", "P101.Cmd := c;", "Cmd.Ack", "from", true},
		{"the whole instance", "P101 := Spare;", "Cmd.Ack", "from", true},
		{"an ENO => on a call in an assignment", "x := SQRT(EN := TRUE, IN := 4.0, ENO => P101.Healthy);", "Healthy", "from", false},
		{"an => output", "Ctl(P := Spare, Demand := TRUE, Demand => P101.Healthy);", "Healthy", "from", false},
		{"inside an IF, FOR, WHILE, REPEAT and CASE", `IF i > 1 THEN FOR i := 1 TO 3 DO WHILE TRUE DO REPEAT CASE i OF 1: P101.Running := TRUE; END_CASE; UNTIL TRUE END_REPEAT; END_WHILE; END_FOR; END_IF;`, "Running", "from", false},
	} {
		src := pumpCtl + `PROGRAM Main
VAR Ctl : PumpCtl; Spare : Pump; c : Cmd; i : INT; x : REAL; END_VAR
  ` + c.body + `
END_PROGRAM
`
		// "=>" needs an output; give the block one for that case.
		src = strings.Replace(src, "VAR_INPUT Demand : BOOL; END_VAR", "VAR_INPUT Demand : BOOL; END_VAR\n  VAR_OUTPUT Demand2 : BOOL; END_VAR", 1)
		if strings.Contains(c.body, "=>") {
			src = strings.Replace(src, "Demand => P101.Healthy", "Demand2 => P101.Healthy", 1)
		}
		got := writes(t, src, pumpKinds)
		if len(got) != 1 {
			t.Errorf("%s: %d refusals, want 1: %+v", c.name, len(got), got)
			continue
		}
		w := got[0]
		if w.Member != c.member || w.Kind != c.kind || w.Whole != c.whole || w.Block != "" {
			t.Errorf("%s: got member %q kind %q whole %v block %q, want %q %q %v", c.name, w.Member, w.Kind, w.Whole, w.Block, c.member, c.kind, c.whole)
		}
		if w.Pos.Line == 0 {
			t.Errorf("%s: no position", c.name)
		}
	}
}

// A member inside a from: sub-struct is read-only with it, unless the
// wiring gives that member a kind of its own (the nearest entry wins).
func TestInstanceWritesInsideAReadOnlyAncestor(t *testing.T) {
	src := `PROGRAM Main
  P104.Cmd.Start := TRUE;
  P104.Cmd.Stop := TRUE;
END_PROGRAM
`
	got := writes(t, src, pumpKinds)
	if len(got) != 1 || got[0].Member != "Cmd" || got[0].Whole || got[0].Pos.Line != strings.Count(pumpTypes, "\n")+2 {
		t.Errorf("got %+v, want only P104.Cmd.Start refused, naming Cmd", got)
	}
}

// Every refusal names the member, its kind and the fix.
func TestInstanceWritesMessagesNameTheFix(t *testing.T) {
	src := `PROGRAM Main
VAR Spare : Pump; END_VAR
  P101.Running := TRUE;
  P101.Rate := 1.0;
  P101 := Spare;
END_PROGRAM
`
	got := writes(t, src, pumpKinds)
	if len(got) != 3 {
		t.Fatalf("%d refusals: %+v", len(got), got)
	}
	for i, want := range []string{
		"P101.Running is read from the device (from:), so the program cannot assign it; remove the assignment, or wire the member as memory:",
		"P101.Rate is computed by its expr: each scan, so the program cannot assign it; assign the members the expression reads",
		"assigning P101 would overwrite P101.Cmd.Ack, which is read from the device (from:); assign the writable members one at a time",
	} {
		if m := got[i].Message(""); !strings.Contains(m, want) {
			t.Errorf("message %d = %q\nwant it to contain %q", i, m, want)
		}
	}
}

// A block that assigns a read-only member is refused where the instance is
// passed to it, on every branch, through inner blocks, sub-struct pins and
// array elements; the block's own line is named.
func TestInstanceWritesThroughBlocks(t *testing.T) {
	const blocks = `FUNCTION_BLOCK Bad
  VAR_IN_OUT P : Pump; END_VAR
  VAR_INPUT X : BOOL; END_VAR
  IF X THEN
    P.Running := FALSE;
  END_IF;
END_FUNCTION_BLOCK
FUNCTION_BLOCK Outer
  VAR_IN_OUT Q : Pump; END_VAR
  VAR In : Bad; END_VAR
  In(P := Q, X := TRUE);
END_FUNCTION_BLOCK
FUNCTION_BLOCK AckIt
  VAR_IN_OUT C : Cmd; END_VAR
  C.Ack := TRUE;
END_FUNCTION_BLOCK
FUNCTION_BLOCK Whole
  VAR_IN_OUT P : Pump; END_VAR
  VAR Fresh : Pump; END_VAR
  P := Fresh;
END_FUNCTION_BLOCK
FUNCTION_BLOCK Ro
  VAR_IN_OUT P : Pump; END_VAR
  VAR r : REAL; END_VAR
  r := P.Rate + P.Levels[1];
END_FUNCTION_BLOCK
`
	for _, c := range []struct {
		name, body            string
		member, block, called string
		assigns               string // the assignment inside the block, to find its line
	}{
		{"a block that assigns on one branch", "b(P := P101, X := FALSE);", "Running", "Bad", "Bad", "P.Running := FALSE;"},
		{"a block that passes the pin on", "o(Q := P101);", "Running", "Bad", "Outer", "P.Running := FALSE;"},
		{"a sub-struct pin", "a(C := P101.Cmd);", "Cmd.Ack", "AckIt", "AckIt", "C.Ack := TRUE;"},
		{"a block that assigns the whole pin", "w(P := P101);", "Cmd.Ack", "Whole", "Whole", "P := Fresh;"},
	} {
		kinds := InstanceKinds{"P101": pumpKinds["P101"]}
		src := blocks + `PROGRAM Main
VAR b : Bad; o : Outer; a : AckIt; w : Whole; END_VAR
  ` + c.body + `
END_PROGRAM
`
		got := writes(t, src, kinds)
		if len(got) == 0 {
			t.Errorf("%s: no refusal", c.name)
			continue
		}
		w := got[0]
		if w.Member != c.member || w.Block != c.block || w.Call != c.called {
			t.Errorf("%s: member %q block %q call %q, want %q %q %q", c.name, w.Member, w.Block, w.Call, c.member, c.block, c.called)
		}
		// The block's line is the assignment's, counted in the whole source.
		full := pumpTypes + src
		want := strings.Count(full[:strings.Index(full, c.assigns)], "\n") + 1
		if w.BlockPos.Line != want {
			t.Errorf("%s: block line %d, want %d", c.name, w.BlockPos.Line, want)
		}
		m := w.Message("line 9")
		for _, frag := range []string{"calling " + c.called, "would assign", "(line 9)", "or pass a local variable of that type"} {
			if !strings.Contains(m, frag) {
				t.Errorf("%s: message %q lacks %q", c.name, m, frag)
			}
		}
	}

	// Blocks that only read, or that cannot reach a read-only member, pass,
	// as does the same offending block on a plain local struct.
	src := blocks + `PROGRAM Main
VAR b : Bad; r : Ro; l : Pump; END_VAR
  r(P := P101);
  b(P := l, X := TRUE);
END_PROGRAM
`
	if got := writes(t, src, pumpKinds); len(got) != 0 {
		t.Errorf("refused: %+v", got)
	}
}

// A standard block that writes its VAR_IN_OUT pin (READ's RD_1) takes the
// instance whole.
func TestInstanceWritesBuiltinInOut(t *testing.T) {
	src := `PROGRAM Main
VAR rd : READ; l : Pump; END_VAR
  rd(REQ := TRUE, ID := 'x', VAR_1 := 'y', RD_1 := P101);
  rd(REQ := TRUE, ID := 'x', VAR_1 := 'y', RD_1 := l);
END_PROGRAM
`
	got := writes(t, src, pumpKinds)
	if len(got) != 1 || !got[0].Builtin || got[0].Call != "READ" {
		t.Fatalf("got %+v", got)
	}
	if m := got[0].Message(""); !strings.Contains(m, "pass a local variable of that type instead") {
		t.Errorf("message %q", m)
	}
}

// Only an instance is checked: a local of the same name, a pin, or a
// member of another kind is not.
func TestInstanceWritesOnlyInstances(t *testing.T) {
	src := `PROGRAM Main
VAR P101 : Pump; END_VAR
  P101.Running := TRUE;
END_PROGRAM
`
	if got := writes(t, src, pumpKinds); len(got) != 0 {
		t.Errorf("a local shadowing the instance was refused: %+v", got)
	}
	src = `FUNCTION_BLOCK F
  VAR_IN_OUT P101 : Pump; END_VAR
  P101.Running := TRUE;
END_FUNCTION_BLOCK
`
	if got := writes(t, src, pumpKinds); len(got) != 0 {
		t.Errorf("a pin was refused inside its own block: %+v", got)
	}
	// An instance with only commands and memory takes a whole assignment.
	src = `PROGRAM Main
VAR Spare : Pump; END_VAR
  P103 := Spare;
  P103.Cmd.Start := TRUE;
END_PROGRAM
`
	kinds := InstanceKinds{"P103": {"Cmd.Start": "to", "RunHours": "memory", "Cmd.Stop": "both"}}
	if got := writes(t, src, kinds); len(got) != 0 {
		t.Errorf("a to:/both:/memory-only instance was refused: %+v", got)
	}
	// A child entry overrides its ancestor's kind.
	src = `PROGRAM Main
END_PROGRAM
`
	if got := writes(t, src, nil); len(got) != 0 {
		t.Errorf("no kinds: %+v", got)
	}
}

// A block that calls itself does not hang the walk, and what it assigns
// before the recursion is still found.
func TestInstanceWritesRecursion(t *testing.T) {
	src := `FUNCTION_BLOCK Loop
  VAR_IN_OUT P : Pump; END_VAR
  VAR Again : Loop; END_VAR
  P.Running := TRUE;
  Again(P := P);
END_FUNCTION_BLOCK
PROGRAM Main
VAR l : Loop; END_VAR
  l(P := P101);
END_PROGRAM
`
	got := writes(t, src, pumpKinds)
	if len(got) == 0 || got[0].Member != "Running" {
		t.Errorf("got %+v", got)
	}
}

// A block that reaches an instance through VAR_EXTERNAL, not a pin, writes
// the instance itself: assigning a from: member there is refused on the
// block's line, and so is passing it on to a block that assigns one. The
// commands and memory stay the block's to set.
func TestInstanceWritesThroughVarExternal(t *testing.T) {
	src := `FUNCTION_BLOCK Reset
  VAR_IN_OUT P : Pump; END_VAR
  P.Running := FALSE;
END_FUNCTION_BLOCK
FUNCTION_BLOCK Ext
  VAR_EXTERNAL P101 : Pump; P102 : Pump; END_VAR
  VAR r : Reset; END_VAR
  P101.Cmd.Start := TRUE;
  P101.RunHours := 0.0;
  P101.Running := FALSE;
  r(P := P102);
END_FUNCTION_BLOCK
PROGRAM Main
VAR e : Ext; END_VAR
  e();
END_PROGRAM
`
	got := writes(t, src, pumpKinds)
	if len(got) != 2 {
		t.Fatalf("got %d refusals, want 2: %+v", len(got), got)
	}
	base := strings.Count(pumpTypes, "\n")
	byInst := map[string]InstanceWrite{}
	for _, w := range got {
		byInst[w.Instance] = w
	}
	if w := byInst["P101"]; w.Member != "Running" || w.Kind != "from" || w.Block != "" || w.Pos.Line != base+10 {
		t.Errorf("direct VAR_EXTERNAL write: %+v (want line %d)", w, base+10)
	}
	if w := byInst["P102"]; w.Member != "Running" || w.Block != "Reset" || w.Call != "Reset" || w.Pos.Line != base+11 {
		t.Errorf("VAR_EXTERNAL passed to a block: %+v (want line %d)", w, base+11)
	}
}
