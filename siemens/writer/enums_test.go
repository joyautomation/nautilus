package writer

import (
	"fmt"
	"strings"
	"testing"
)

// enumLib declares enumerations as a project library does: explicit
// values, an initial member that is not the first, a declared base type,
// two enumerations sharing a member name, and a struct with an enum
// member.
const enumLib = `TYPE
  WashState : (Idle, Fill := 10, Wash := 20, Drain := 30) := Fill;
  Valve : (Closed, Open);
  Door : (Shut, Open := 5) INT;
  Unit : STRUCT
    State : WashState;
    Last : WashState := Drain;
  END_STRUCT;
END_TYPE
`

// TestEnumsWritten: an enumeration is written as its integer, each member
// a block names one of its constants, and every declaration starts where
// nautilus's does (#1299).
func TestEnumsWritten(t *testing.T) {
	src := `PROGRAM P
VAR_EXTERNAL st : WashState; v : Valve; d : Door; code : INT; u1 : Unit; n : INT; big : LINT; END_VAR
VAR
  mine : WashState;
  other : WashState := WashState#Drain;
  plain : Valve;
END_VAR
VAR CONSTANT
  K : Valve := Open;
END_VAR
CASE st OF
  Idle: st := Fill;
  WashState#Fill, Wash: st := Drain;
  Drain: st := Idle;
END_CASE;
CASE st OF
  Idle..Wash: code := 1;
END_CASE;
CASE v OF
  Open: code := 2;
END_CASE;
IF st = Fill THEN v := Open; ELSE v := Closed; END_IF;
d := Open;
IF Open = d THEN d := Door#Shut; END_IF;
code := TO_INT(st);
code := TO_INT(Door#Open);
mine := TO_WashState(n);
u1.State := other;
END_PROGRAM
`
	globals := []Global{
		{Name: "st", Type: "WashState"},
		{Name: "v", Type: "Valve", Init: "Open"},
		{Name: "d", Type: "Door"},
		{Name: "code", Type: "INT"},
		{Name: "u1", Type: "Unit"},
		{Name: "n", Type: "INT"},
		{Name: "big", Type: "LINT"},
	}
	out, diags := writeStructs(src, globals, enumLib)
	if len(diags) != 0 {
		t.Fatalf("%v", diags)
	}
	files := fileMap(out)
	db, fb, unit := files["naut_Tags.db"], files["naut_P.scl"], files["naut_Unit.udt"]
	for _, c := range []struct{ name, got, want string }{
		// A tag is its integer, starting at the type's initial member
		// (Fill, 10) or at the member its init names.
		{"tags DB", db, "      st : DINT := 10;\n"},
		{"tags DB", db, "      v : DINT := 1;\n"},
		// A declared base type is kept.
		{"tags DB", db, "      d : INT := 0;\n"},
		// A struct member of enum type starts at its initial member, or
		// at the one it names.
		{"Unit", unit, "      State : DINT := 10;\n"},
		{"Unit", unit, "      Last : DINT := 30;\n"},
		// A local likewise; an initializer naming a member is its number.
		{"FB", fb, "      mine : DINT := 10;\n"},
		{"FB", fb, "      other : DINT := 30;\n"},
		{"FB", fb, "      plain : DINT := 0;\n"},
		// The members the block names, as its constants.
		{"FB", fb, "      WashState_Fill : DINT := 10;\n"},
		{"FB", fb, "      WashState_Idle : DINT := 0;\n"},
		{"FB", fb, "      Door_Open : INT := 5;\n"},
		{"FB", fb, "      Valve_Open : DINT := 1;\n"},
		// CASE labels name the constants, bare or qualified.
		{"FB", fb, "\t\t#WashState_Idle:\n"},
		{"FB", fb, "\t\t#WashState_Fill, #WashState_Wash:\n"},
		// A range of members too (TIA V21 compiles it, 2026-11-26).
		{"FB", fb, "\t\t#WashState_Idle..#WashState_Wash:\n"},
		// Open is a member of Valve and of Door: the assignment's target
		// and the comparison's other side choose, as in nautilus.
		{"FB", fb, `"naut_Tags".v := #Valve_Open;`},
		{"FB", fb, `"naut_Tags".d := #Door_Open;`},
		{"FB", fb, `IF #Door_Open = "naut_Tags".d THEN`},
		{"FB", fb, "\t\t#Valve_Open:\n"},
		// A qualified member is its enumeration's, with no context.
		{"FB", fb, `"naut_Tags".code := #Door_Open;`},
		// A constant of enum type is the member's number.
		{"FB", fb, "      K : DINT := 1;\n"},
		// TO_INT converts the integer; TO_<Enum> keeps an INT's value in
		// the DINT.
		{"FB", fb, `"naut_Tags".code := LINT_TO_INT(`},
		{"FB", fb, `#mine := INT_TO_DINT("naut_Tags".n);`},
		{"FB", fb, `"naut_Tags".u1.State := #other;`},
	} {
		if !strings.Contains(c.got, c.want) {
			t.Errorf("%s lacks %q:\n%s", c.name, c.want, c.got)
		}
	}
	// Only the members named are declared.
	if strings.Contains(fb, "Door_Shut : INT := 0;") == false || strings.Contains(fb, "Valve_Closed") == false || strings.Contains(fb, "WashState_Wash :") == false {
		t.Errorf("named members missing:\n%s", fb)
	}
	for _, unused := range []string{"Door_Closed", "Valve_Shut"} {
		if strings.Contains(fb, unused) {
			t.Errorf("FB declares %s:\n%s", unused, fb)
		}
	}
}

// TestEnumTempsAndFunctions: a temp of enum type is reset to the type's
// initial member on every call, as nautilus resets a temp, and a
// FUNCTION's result of enum type is its integer.
func TestEnumTempsAndFunctions(t *testing.T) {
	lib := enumLib + "FUNCTION Next : WashState\nVAR_INPUT s : WashState; END_VAR\nVAR tmp : WashState; END_VAR\nIF s = Idle THEN tmp := Wash; END_IF;\nNext := tmp;\nEND_FUNCTION\n"
	src := "PROGRAM P\nVAR_EXTERNAL st : WashState; END_VAR\nst := Next(s := st);\nEND_PROGRAM\n"
	out, diags := writeStructs(src, []Global{{Name: "st", Type: "WashState"}}, lib)
	if len(diags) != 0 {
		t.Fatalf("%v", diags)
	}
	fc := fileMap(out)["naut_Next.scl"]
	for _, want := range []string{
		"FUNCTION \"naut_Next\" : DINT",
		"      s : DINT;\n",
		"\t#tmp := 10;\n",
		"\t#Next := 10;\n",
	} {
		if !strings.Contains(fc, want) {
			t.Errorf("FC lacks %q:\n%s", want, fc)
		}
	}
}

// TestEnumRefusals: what the writer cannot write faithfully is refused,
// never written approximately.
func TestEnumRefusals(t *testing.T) {
	for _, c := range []struct {
		name, lib, src string
		globals        []Global
		want           string
	}{
		// nautilus keeps a LINT's value whole as the enumeration's; the
		// CPU's DINT does not hold every one.
		{"TO_Enum of a LINT", enumLib, "PROGRAM P\nVAR_EXTERNAL st : WashState; big : LINT; END_VAR\nst := TO_WashState(big);\nEND_PROGRAM\n",
			[]Global{{Name: "st", Type: "WashState"}, {Name: "big", Type: "LINT"}}, "TO_WASHSTATE of a LINT yet (#1305)"},
		// A member does not fit the declared base type.
		{"base too small", "TYPE Big : (A, B := 70000) INT; END_TYPE\n", "PROGRAM P\nVAR b : Big; END_VAR\nEND_PROGRAM\n",
			nil, "the enumeration Big, whose member B (70000) does not fit its INT"},
		// The writer names a member's constant <Enum>_<Member>; a
		// variable already so named is refused, not shadowed.
		{"constant clash", enumLib, "PROGRAM P\nVAR Valve_Open : BOOL; v : Valve; END_VAR\nv := Open;\nEND_PROGRAM\n",
			nil, "Valve#Open next to the variable Valve_Open"},
	} {
		_, diags := writeStructs(c.src, c.globals, c.lib)
		if !strings.Contains(fmt.Sprint(diags), c.want) {
			t.Errorf("%s: %v, want %q", c.name, diags, c.want)
		}
	}
}

// TestEnumWiderThanDINT: an enumeration with no declared base whose
// values need 64 bits is a LINT, not a DINT that would cut them.
func TestEnumWiderThanDINT(t *testing.T) {
	out, diags := writeStructs("PROGRAM P\nVAR_EXTERNAL h : Huge; END_VAR\nh := Far;\nEND_PROGRAM\n", []Global{{Name: "h", Type: "Huge"}}, "TYPE Huge : (Near, Far := 5000000000); END_TYPE\n")
	if len(diags) != 0 {
		t.Fatalf("%v", diags)
	}
	files := fileMap(out)
	if !strings.Contains(files["naut_Tags.db"], "      h : LINT := 0;\n") || !strings.Contains(files["naut_P.scl"], "Huge_Far : LINT := 5000000000;") {
		t.Errorf("%s\n%s", files["naut_Tags.db"], files["naut_P.scl"])
	}
}

// TestEnumHintFromThePin: an unqualified member two enumerations declare,
// passed to a block or function, is the pin's enumeration's, as nautilus
// lowers an argument, never the enclosing assignment's (Sonnet review).
func TestEnumHintFromThePin(t *testing.T) {
	lib := enumLib + "FUNCTION F : Door\nVAR_INPUT x : Valve; END_VAR\nF := Door#Shut;\nIF x = Open THEN F := Open; END_IF;\nEND_FUNCTION\nFUNCTION_BLOCK B\nVAR_INPUT x : Valve; END_VAR\nVAR_OUTPUT y : Door; END_VAR\ny := Open;\nEND_FUNCTION_BLOCK\n"
	src := "PROGRAM P\nVAR_EXTERNAL d : Door; END_VAR\nVAR b : B; END_VAR\nd := F(Open);\nd := F(x := Open);\nb(x := Open);\nEND_PROGRAM\n"
	out, diags := writeStructs(src, []Global{{Name: "d", Type: "Door"}}, lib)
	if len(diags) != 0 {
		t.Fatalf("%v", diags)
	}
	fb := fileMap(out)["naut_P.scl"]
	for _, want := range []string{
		`"naut_Tags".d := "naut_F"(x := #Valve_Open);`,
		`#b(x := #Valve_Open);`,
	} {
		if !strings.Contains(fb, want) {
			t.Errorf("FB lacks %q:\n%s", want, fb)
		}
	}
	if strings.Contains(fb, "Door_Open") {
		t.Errorf("an argument took the assignment's enumeration:\n%s", fb)
	}
}

// TestEnumMemberAfterConstants: the project's global constant named like
// a member is the constant, as nautilus resolves the name (Sonnet review).
func TestEnumMemberAfterConstants(t *testing.T) {
	lib := "VAR_GLOBAL CONSTANT\n  Closed : INT := 7;\nEND_VAR\nTYPE Valve : (Closed, Open); END_TYPE\n"
	out, diags := writeStructs("PROGRAM P\nVAR_EXTERNAL n : INT; END_VAR\nn := Closed;\nEND_PROGRAM\n", []Global{{Name: "n", Type: "INT"}}, lib)
	if len(diags) != 0 {
		t.Fatalf("%v", diags)
	}
	fb := fileMap(out)["naut_P.scl"]
	if !strings.Contains(fb, `"naut_Tags".n := #Closed;`) || strings.Contains(fb, "Valve_Closed") {
		t.Errorf("%s", fb)
	}
}
