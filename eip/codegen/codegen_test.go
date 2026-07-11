package codegen

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/eip/logix"
	"github.com/joyautomation/nautilus/lang/st"
)

// fakeBrowse builds a BrowseResult like the client would return from a
// controller with UDTs (nested), an array tag, a program-scoped tag, and a
// string tag.
func fakeBrowse() *logix.BrowseResult {
	header := &logix.Template{
		ID: 0x101, Name: "Header_Type", Handle: 0x1001, StructSize: 8,
		Members: []logix.Member{
			{Name: "Displacement", Type: 0x00CA, Offset: 0},
			{Name: "Valid", Type: 0x00C1, Info: 0, Offset: 4},
			{Name: "ZZZZZZZZZZHost", Type: 0x00C2, Offset: 4}, // hidden bit host
		},
	}
	plt := &logix.Template{
		ID: 0x102, Name: "Plt_Type", Handle: 0x1002, StructSize: 12,
		Members: []logix.Member{
			{Name: "Header", Type: 0x8000 | 0x101, Offset: 0},
			{Name: "Count", Type: 0x00C4, Offset: 8},
		},
	}
	str82 := &logix.Template{
		ID: 0x0FCE, Name: "STRING", Handle: 0x0FCE, StructSize: 88,
		Members: []logix.Member{
			{Name: "LEN", Type: 0x00C4, Offset: 0},
			{Name: "DATA", Type: 0x2000 | 0x00C2, Info: 82, Offset: 4},
		},
	}
	return &logix.BrowseResult{
		Symbols: []logix.Symbol{
			{Name: "Speed", Type: 0x00CA},
			{Name: "TRS", Type: 0x8000 | 0x102},
			{Name: "Counts", Type: 0x00C4 | 1<<13, Dims: [3]uint32{10}},
			{Name: "Label", Type: 0x8000 | 0x0FCE},
			{Name: "Program:MainProgram.Motor", Type: 0x00C4},
			{Name: "Local:1:I", Type: 0x8000 | 0x102}, // module I/O, excluded by default
		},
		Templates: map[uint16]*logix.Template{0x101: header, 0x102: plt, 0x0FCE: str82},
		Programs:  []string{"MainProgram"},
	}
}

func TestGenerate(t *testing.T) {
	out, err := Generate(fakeBrowse(), Options{
		Host:             "192.168.1.10",
		Slot:             0,
		Package:          "main",
		WritablePatterns: []string{"Speed"},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	m := out.Manifest
	byName := map[string]bool{}
	for _, b := range m.Tags {
		byName[b.Name] = true
	}
	for _, want := range []string{"Speed", "TRS", "Counts", "Label", "MainProgram_Motor"} {
		if !byName[want] {
			t.Errorf("missing binding %q; have %v", want, m.Tags)
		}
	}
	if byName["Local_1_I"] {
		t.Errorf("module I/O tag should be excluded by default")
	}
	for _, b := range m.Tags {
		switch b.Name {
		case "Speed":
			if !b.Writable || b.Type != "REAL" {
				t.Errorf("Speed binding wrong: %+v", b)
			}
		case "Counts":
			if b.ArrayLen != 10 || b.Type != "DINT" {
				t.Errorf("Counts binding wrong: %+v", b)
			}
		case "Label":
			if b.Type != "STRING" {
				t.Errorf("Label binding wrong: %+v", b)
			}
		case "TRS":
			if b.Type != "Plt_Type" {
				t.Errorf("TRS binding wrong: %+v", b)
			}
		}
	}

	// Types: Header_Type before Plt_Type (dependency order), hidden member
	// dropped, STRING template not exported as a struct.
	if len(m.Types) != 2 || m.Types[0].Name != "Header_Type" || m.Types[1].Name != "Plt_Type" {
		t.Fatalf("types = %+v", m.Types)
	}
	if len(m.Types[0].Fields) != 2 {
		t.Errorf("Header_Type fields = %+v (hidden member should be dropped)", m.Types[0].Fields)
	}

	// The generated ST must compile together with a program that uses it.
	program := out.TypesST + `
PROGRAM Main
VAR_EXTERNAL
  Speed : REAL;
  TRS : Plt_Type;
END_VAR
IF TRS.Header.Valid THEN
  Speed := TRS.Header.Displacement;
END_IF;
END_PROGRAM`
	prog, err := st.Parse(program)
	if err != nil {
		t.Fatalf("generated ST does not parse: %v\n%s", err, out.TypesST)
	}
	if _, err := st.Lower(prog); err != nil {
		t.Fatalf("generated ST does not lower: %v\n%s", err, out.TypesST)
	}

	// The generated Go must parse.
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "eip_manifest.go", out.ManifestGo, 0); err != nil {
		t.Fatalf("generated Go does not parse: %v\n%s", err, out.ManifestGo)
	}
	if !strings.Contains(out.ManifestGo, `eip.New("192.168.1.10"`) {
		t.Errorf("manifest missing wiring hint:\n%s", out.ManifestGo)
	}
}
