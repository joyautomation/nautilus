package l5x

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/st"
)

func TestTypesRendersUserUDTs(t *testing.T) {
	f := load(t, "variety.L5X")
	src, unresolved, err := Types(f, TypesOptions{})
	if err != nil {
		t.Fatalf("Types: %v", err)
	}
	for _, want := range []string{
		"Limits : STRUCT",
		"Analog_Input : STRUCT",
		"Alarms : Limits;",                // a nested UDT, declared after its dependency
		"History : ARRAY [0..9] OF REAL;", // Dimension="10" is a 0-based IEC array
		"Label : STRING;",                 // Logix's LEN+DATA structure means a string
		"Fault : BOOL;",                   // a BIT member is the authored BOOL
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in:\n%s", want, src)
		}
	}
	// The hidden SINT that hosts the BOOLs is Logix's storage detail, not
	// a member of the type anyone authored.
	if strings.Contains(src, "ZZZZZZZZZZ") {
		t.Errorf("hidden bit host leaked into the type:\n%s", src)
	}
	// Class="IO" shapes are not project code and stay out unless asked for.
	if strings.Contains(src, "Embedded_IO") {
		t.Errorf("module-defined type emitted by default:\n%s", src)
	}
	if len(unresolved) != 1 || unresolved[0] != "REF_TO_AXIS_VIRTUAL" {
		t.Errorf("unresolved = %v, want just the opaque motion handle", unresolved)
	}
	if strings.Contains(src, "Axis") {
		t.Errorf("a member of an unrenderable type must be omitted, not guessed:\n%s", src)
	}
}

// Logix reserves no words, so real projects have UDT members named
// "retain" and "Constant" — both IEC variable qualifiers. This is exactly
// the class of bug the stgen pattern exists to catch: Render compiles what
// it emitted, so a keyword collision is a returned error, not bad ST on
// disk. Four of the 52 files in the development corpus hit it.
func TestTypesRenamesIECKeywords(t *testing.T) {
	f := load(t, "variety.L5X")
	src, _, err := Types(f, TypesOptions{})
	if err != nil {
		t.Fatalf("Types: %v", err)
	}
	if !strings.Contains(src, "retain_ : BOOL;") {
		t.Errorf("keyword member not renamed:\n%s", src)
	}
	// And the proof, which is the whole point of the pattern.
	prog, err := st.Parse(src)
	if err != nil {
		t.Fatalf("generated ST does not parse: %v\n%s", err, src)
	}
	if _, err := st.Lower(prog); err != nil {
		t.Fatalf("generated ST does not compile: %v\n%s", err, src)
	}
}

func TestTypesAllIncludesModuleShapes(t *testing.T) {
	f := load(t, "variety.L5X")
	src, _, err := Types(f, TypesOptions{All: true})
	if err != nil {
		t.Fatalf("Types: %v", err)
	}
	// "AB:Embedded_IO:I:0" is not an IEC identifier; IEC has no ':'.
	if !strings.Contains(src, "AB_Embedded_IO_I_0 : STRUCT") {
		t.Errorf("module type missing or unsanitized:\n%s", src)
	}
}

func TestTypesRoots(t *testing.T) {
	f := load(t, "variety.L5X")
	src, _, err := Types(f, TypesOptions{Roots: []string{"Limits"}})
	if err != nil {
		t.Fatalf("Types: %v", err)
	}
	if strings.Contains(src, "Analog_Input") {
		t.Errorf("Roots should limit the output to what was asked for:\n%s", src)
	}
	// A root pulls in what it references, transitively.
	src, _, err = Types(f, TypesOptions{Roots: []string{"Analog_Input"}})
	if err != nil {
		t.Fatalf("Types: %v", err)
	}
	if !strings.Contains(src, "Limits : STRUCT") {
		t.Errorf("a root's dependency was not pulled in:\n%s", src)
	}
}

// Logix's predefined structures are not carried in an export — the
// firmware defines them — so a TIMER tag would reference a type that no
// generated file declares.
func TestTypesDeclaresPredefined(t *testing.T) {
	f := load(t, "variety.L5X")
	src, _, err := Types(f, TypesOptions{Roots: []string{"TIMER", "COUNTER"}})
	if err != nil {
		t.Fatalf("Types: %v", err)
	}
	for _, want := range []string{"TIMER : STRUCT", "PRE : DINT;", "DN : BOOL;", "COUNTER : STRUCT", "UN : BOOL;"} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in:\n%s", want, src)
		}
	}
}
