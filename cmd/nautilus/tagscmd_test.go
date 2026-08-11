package main

// The CSV importer is shipped because CSV's FORMAT is standard even though
// every plant's columns differ — so a column mapping is the whole difference
// between one export and another, which is a contract worth owning. These
// tests pin the mapping and the type fidelity.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func importCSV(t *testing.T, csv string, args ...string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(src, []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "tags", "out.yaml")
	code := runTagsImportCSV(append(append([]string{}, args...), "-o", out, src))
	raw, err := os.ReadFile(out)
	if err != nil {
		return "", code
	}
	return string(raw), code
}

func TestImportCSVMapsColumns(t *testing.T) {
	out, code := importCSV(t, `name,role,type,init,unit,desc
P201,input,,,L/s,Pump 201 flow
P202,setpoint,Motor,,,Pump 202 drive
P203,state,,0,h,Pump 203 hours
`)
	if code != 0 {
		t.Fatalf("import failed (%d)", code)
	}
	for _, want := range []string{
		`- { name: P201, role: input, unit: "L/s", desc: "Pump 201 flow" }`,
		`- { name: P202, role: setpoint, type: Motor, desc: "Pump 202 drive" }`,
		`- { name: P203, role: state, init: 0.0, unit: "h", desc: "Pump 203 hours" }`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing:\n  %s\ngot:\n%s", want, out)
		}
	}
}

// A spreadsheet cell is text; the tag store is typed. A bool must stay a
// bool and a whole number must stay a REAL, or the seed silently retypes the
// tag it seeds.
func TestImportCSVKeepsCellTypes(t *testing.T) {
	out, _ := importCSV(t, `name,role,init
A,setpoint,true
B,setpoint,0
C,setpoint,1.5
D,setpoint,auto
`)
	for _, want := range []string{"init: true", "init: 0.0", "init: 1.5", `init: "auto"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// Columns are named by flag, because "every plant's columns differ" is
// exactly the variation a shipped tool has to absorb.
func TestImportCSVCustomColumns(t *testing.T) {
	out, code := importCSV(t, `Tag,Service
P201,Pump 201 flow
`, "--name", "Tag", "--desc", "Service", "--role-default", "output")
	if code != 0 {
		t.Fatalf("import failed (%d)", code)
	}
	if !strings.Contains(out, `- { name: P201, role: output, desc: "Pump 201 flow" }`) {
		t.Errorf("custom columns not honoured:\n%s", out)
	}
}

// Spreadsheets have blank spacer rows. They are not tags.
func TestImportCSVSkipsBlankRows(t *testing.T) {
	out, _ := importCSV(t, "name,role\nA,input\n\nB,input\n")
	if strings.Count(out, "- { name:") != 2 {
		t.Errorf("blank row became a tag:\n%s", out)
	}
}

func TestImportCSVErrors(t *testing.T) {
	if _, code := importCSV(t, "service,area\nx,y\n"); code == 0 {
		t.Error("a CSV with no name column was accepted")
	}
	if _, code := importCSV(t, "name,role\n"); code == 0 {
		t.Error("a CSV with a header but no rows was accepted")
	}
}
