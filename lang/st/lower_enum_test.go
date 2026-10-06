package st

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

const enumTypes = `
TYPE
    Mode : (Idle, Run, Fault) := Idle;
    Valve : (Closed := 0, Opening := 10, Open := 20, Idle := 99) DINT;
END_TYPE
`

// #238: an enumeration's values carry their names; qualified and
// unqualified members, explicit values, the type's default, CASE labels
// and comparisons.
func TestEnumBasics(t *testing.T) {
	src := enumTypes + `
PROGRAM P
VAR_EXTERNAL Out : INT; Name : Mode; VOut : Valve; END_VAR
VAR m : Mode; v : Valve := Opening; first : Mode; n : INT; END_VAR
first := m;
CASE m OF
  Mode#Idle: m := Run;
  Run:       m := Mode#Fault;
  Fault:     n := n + 1;
END_CASE;
IF v = Valve#Opening AND m <> Mode#Idle THEN v := Open; END_IF;
IF m > Run THEN n := n + 10; END_IF;
Out := n;
Name := m;
VOut := v;
END_PROGRAM`
	h, prog, frame := scanN(t, src, 3, map[string]ir.Value{"Out": ir.IntVal(0)})
	if got := h.globals["Out"].I; got != 21 {
		t.Errorf("Out = %d, want 21", got)
	}
	name := h.globals["Name"]
	if name.Kind != ir.TypeInt || name.I != 2 || name.S != "Fault" {
		t.Errorf("Name = %+v, want INT 2 named Fault", name)
	}
	if v := h.globals["VOut"]; v.I != 20 || v.S != "Open" {
		t.Errorf("VOut = %d %q, want 20 Open", v.I, v.S)
	}
	if f := frame.Slots[prog.SlotIndex["first"]]; f.S != "Fault" {
		t.Errorf("first after 3 scans = %q", f.S)
	}
	// The type's default: Mode := Idle; a fresh frame starts there, named.
	fresh := ir.NewFrame(prog)
	if m := fresh.Slots[prog.SlotIndex["m"]]; m.I != 0 || m.S != "Idle" {
		t.Errorf("default m = %d %q, want 0 Idle", m.I, m.S)
	}
	if v := fresh.Slots[prog.SlotIndex["v"]]; v.I != 10 || v.S != "Opening" {
		t.Errorf("initial v = %d %q, want 10 Opening", v.I, v.S)
	}
	typ := prog.Types["Mode"]
	if typ == nil || typ.Enum == nil || typ.String() != "Mode" {
		t.Fatalf("Program.Types[Mode] = %v; the TYPE table must expose the enumeration", typ)
	}
}

// A tag read through an enumeration-typed VAR_EXTERNAL gains its member name
// even when the store holds a bare integer (a driver wrote it) — and a
// string naming a member converts too (ir.CoerceValue, the typed-tag hook).
func TestEnumFromTagStore(t *testing.T) {
	src := enumTypes + `
PROGRAM P
VAR_EXTERNAL In : Mode; Copy : Mode; END_VAR
Copy := In;
END_PROGRAM`
	h, _, _ := scanN(t, src, 1, map[string]ir.Value{"In": ir.IntVal(1)})
	if c := h.globals["Copy"]; c.I != 1 || c.S != "Run" {
		t.Errorf("Copy = %d %q, want 1 Run", c.I, c.S)
	}
	prog := lowerSource(t, src)
	mode := prog.Types["Mode"]
	if v := ir.CoerceValue(ir.StringVal("fault"), mode); v.Kind != ir.TypeInt || v.I != 2 || v.S != "Fault" {
		t.Errorf("CoerceValue('fault', Mode) = %+v", v)
	}
	if v := ir.CoerceValue(ir.StringVal("nope"), mode); v.I != 0 || v.S != "Idle" {
		t.Errorf("unknown name falls back to the default, got %+v", v)
	}
}

// Conversions are explicit: TO_INT / TO_DINT out, TO_<Enum> in.
func TestEnumConversions(t *testing.T) {
	src := enumTypes + `
PROGRAM P
VAR_EXTERNAL I : INT; D : DINT; M : Mode; Odd : Mode; END_VAR
VAR v : Valve := Open; END_VAR
I := TO_INT(v) + 1;
D := TO_DINT(Mode#Fault);
M := TO_Mode(1);
Odd := TO_Mode(I);
END_PROGRAM`
	h, _, _ := scanN(t, src, 1, nil)
	if h.globals["I"].I != 21 || h.globals["D"].I != 2 {
		t.Errorf("I, D = %d, %d; want 21, 2", h.globals["I"].I, h.globals["D"].I)
	}
	if m := h.globals["M"]; m.I != 1 || m.S != "Run" {
		t.Errorf("M = %+v", m)
	}
	if o := h.globals["Odd"]; o.I != 21 || o.S != "" {
		t.Errorf("TO_Mode(21) = %d %q; want 21, unnamed", o.I, o.S)
	}
}

// The type rules: no mixing with integers or another enumeration, no
// arithmetic, an ambiguous member must be qualified unless the context's
// type picks it.
func TestEnumTypeErrors(t *testing.T) {
	head := enumTypes + "PROGRAM P\nVAR m : Mode; v : Valve; i : INT; END_VAR\n"
	cases := map[string]string{
		"m := 1;":                       "cannot assign INT to Mode",
		"i := m;":                       "cannot assign Mode to INT",
		"m := v;":                       "cannot assign Valve to Mode",
		"i := m + 1;":                   "Mode is an enumeration: its values are names, not numbers",
		"IF m = 1 THEN i := 1; END_IF;": "an enumeration (Mode) compares only with a value of the same type",
		"IF m = v THEN i := 1; END_IF;": "compares only with a value of the same type",
		"i := TO_INT(Idle);":            "Idle is a value of more than one enumeration — write Mode#Idle or Valve#Idle",
		"m := Mode#Stopped;":            "enumeration Mode has no member Stopped (members: Idle, Run, Fault)",
		"m := Pump#Run;":                "unknown enumeration type",
		"Run := m;":                     "Run is a value of the enumeration Mode (Mode#Run), not a variable",
		"CASE m OF Valve#Open: i := 1; END_CASE;":            "CASE label Valve#Open is a Valve, but the selector is a Mode",
		"CASE m OF Run: i := 1; Mode#Run: i := 2; END_CASE;": "duplicate CASE label: Mode#Run (= Run, 1) has the same value as Run (= Run, 1)",
	}
	for body, want := range cases {
		lowerExpectErr(t, head+body+"\nEND_PROGRAM\n", want)
	}
}

// The context settles a member two enumerations share.
func TestEnumAmbiguousMemberResolvedByContext(t *testing.T) {
	src := enumTypes + `
PROGRAM P
VAR_EXTERNAL A : Mode; B : Valve; Same : BOOL; END_VAR
VAR m : Mode := Run; END_VAR
A := Idle;
B := Idle;
Same := Idle = m;
CASE m OF Idle: Same := TRUE; END_CASE;
END_PROGRAM`
	h, _, _ := scanN(t, src, 1, nil)
	if a := h.globals["A"]; a.I != 0 || a.S != "Idle" {
		t.Errorf("A = %+v", a)
	}
	if b := h.globals["B"]; b.I != 99 || b.S != "Idle" {
		t.Errorf("B = %+v", b)
	}
	if h.globals["Same"].B {
		t.Error("Idle = m with m = Run should be FALSE")
	}
}

func TestEnumDeclarationErrors(t *testing.T) {
	lowerExpectErr(t, "TYPE E : (A, B, A); END_TYPE\n", `duplicate enumeration member "A"`)
	lowerExpectErr(t, "TYPE E : (A := 1, B := 1); END_TYPE\n", "A and B both have the value 1")
	lowerExpectErr(t, "TYPE E : (A, B) := C; END_TYPE\n", "the initial value must be one of its members (A, B)")
	lowerExpectErr(t, "TYPE N : INT := 5; END_TYPE\n", "an initial value on a TYPE is supported for enumerations only")
	lowerExpectErr(t, "PROGRAM P\nVAR x : (A, B); END_VAR\nEND_PROGRAM\n", "declare the enumeration as a TYPE")
}

// An enumeration is a field type, an FB pin type and an array element.
func TestEnumInStructsPinsArrays(t *testing.T) {
	src := enumTypes + `
TYPE Axis : STRUCT state : Mode; END_STRUCT; END_TYPE
FUNCTION_BLOCK Drive
VAR_INPUT cmd : Mode; END_VAR
VAR_OUTPUT seen : Mode; END_VAR
seen := cmd;
END_FUNCTION_BLOCK
PROGRAM P
VAR_EXTERNAL Out : Mode; Ax : Axis; END_VAR
VAR d : Drive; hist : ARRAY[0..1] OF Mode; END_VAR
d(cmd := Fault);
hist[1] := d.seen;
Ax.state := hist[1];
Out := Ax.state;
END_PROGRAM`
	h, _, _ := scanN(t, src, 1, nil)
	if o := h.globals["Out"]; o.I != 2 || o.S != "Fault" {
		t.Errorf("Out = %+v", o)
	}
}
