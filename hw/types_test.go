package hw

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

func TestTypesRenderAndCompile(t *testing.T) {
	src, err := TypesST("snmp")
	if err != nil {
		t.Fatal(err)
	}
	// The three importers agree byte for byte apart from the header line.
	src2, _ := TypesST("prometheus")
	body := func(b []byte) string { s := string(b); return s[strings.Index(s, "*)"):] }
	if body(src) != body(src2) {
		t.Fatal("importers render different TYPE blocks")
	}
	// It compiles, and the compiled slot order is the table's order — the
	// invariant that lets a driver-built struct value be read by a program.
	prog, err := st.Parse(string(src) + "\nPROGRAM P\nVAR_EXTERNAL SW : SwitchPort; END_VAR\nEND_PROGRAM\n")
	if err != nil {
		t.Fatal(err)
	}
	lowered, err := st.Lower(prog)
	if err != nil {
		t.Fatal(err)
	}
	for _, ty := range Types {
		compiled, ok := lowered.Types[ty.Name]
		if !ok || compiled.Kind != ir.TypeStruct {
			t.Fatalf("%s did not compile to a struct", ty.Name)
		}
		if len(compiled.Struct.Fields) != len(ty.Fields) {
			t.Fatalf("%s: %d compiled fields, table has %d", ty.Name, len(compiled.Struct.Fields), len(ty.Fields))
		}
		sd := StructDef(ty.Name)
		for i, f := range ty.Fields {
			if compiled.Struct.Fields[i].Name != f.Name || sd.Fields[i].Name != f.Name {
				t.Errorf("%s slot %d: compiled %s, def %s, table %s", ty.Name, i, compiled.Struct.Fields[i].Name, sd.Fields[i].Name, f.Name)
			}
			if compiled.Struct.Fields[i].Type.Kind != f.Kind {
				t.Errorf("%s.%s: compiled kind %s, table %s", ty.Name, f.Name, compiled.Struct.Fields[i].Type.Kind, f.Kind)
			}
		}
	}
}

func TestStructDefIdentity(t *testing.T) {
	if StructDef("SwitchPort") != StructDef("SwitchPort") {
		t.Fatal("StructDef must return one pointer per type")
	}
	if StructDef("Nope") != nil {
		t.Fatal("unknown type must be nil")
	}
	z, ok := Zero("Fan")
	if !ok || z.Kind != ir.TypeStruct || z.Struct != StructDef("Fan") || len(z.Fld) != len(StructDef("Fan").Fields) {
		t.Fatalf("Zero(Fan) = %+v", z)
	}
	i, f, ok := FieldOf("SwitchPort", "Down")
	if !ok || f.Kind != ir.TypeBool || StructDef("SwitchPort").Fields[i].Name != "Down" {
		t.Fatal("FieldOf")
	}
	if _, _, ok := FieldOf("SwitchPort", "Nope"); ok {
		t.Fatal("FieldOf unknown member")
	}
	if !strings.Contains(TypeNames(), "PDUOutlet") {
		t.Fatal(TypeNames())
	}
}

// Every member name is a valid IEC identifier and unique within its type,
// and the contract never has two types sharing a name.
func TestTypesWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, ty := range Types {
		if seen[ty.Name] {
			t.Errorf("type %s declared twice", ty.Name)
		}
		seen[ty.Name] = true
		members := map[string]bool{}
		for _, f := range ty.Fields {
			if members[f.Name] {
				t.Errorf("%s.%s declared twice", ty.Name, f.Name)
			}
			members[f.Name] = true
			for i, r := range f.Name {
				if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')) {
					t.Errorf("%s.%s is not an identifier", ty.Name, f.Name)
				}
			}
		}
	}
}
