package sfc

import (
	"strings"
	"testing"
)

// The shared vars panel posts {type, name, varType, section} — the lang/ld
// shape — for SFC too; these ops used to fall through to "unknown op".

func varNames(m *Model) string {
	var s []string
	for _, v := range m.Vars {
		s = append(s, v.Section+":"+v.Name+":"+v.Type)
	}
	return strings.Join(s, ",")
}

func TestOpDeclareVar(t *testing.T) {
	// Into the existing VAR section.
	out, m := applyOp(t, baseline, EditOp{Type: "declareVar", Name: "Count", VarType: "INT", Section: "VAR"})
	if got := varNames(m); got != "VAR:X:BOOL,VAR:Lamp:BOOL,VAR:Count:INT" {
		t.Fatalf("vars = %s\n%s", got, out)
	}
	// Default section VAR_EXTERNAL: none exists yet, so it's created above SFC.
	out, m = applyOp(t, out, EditOp{Type: "declareVar", Name: "Level", VarType: "REAL"})
	if got := varNames(m); !strings.HasSuffix(got, "VAR_EXTERNAL:Level:REAL") {
		t.Fatalf("vars = %s\n%s", got, out)
	}
	// ...and the next external lands in that same section.
	_, m = applyOp(t, out, EditOp{Type: "declareVar", Name: "Temp", VarType: "REAL", Section: "VAR_EXTERNAL"})
	if got := varNames(m); !strings.HasSuffix(got, "VAR_EXTERNAL:Level:REAL,VAR_EXTERNAL:Temp:REAL") {
		t.Fatalf("vars = %s", got)
	}

	wantOpErr(t, baseline, EditOp{Type: "declareVar", Name: "x", VarType: "BOOL"})    // duplicate (case-insensitive)
	wantOpErr(t, baseline, EditOp{Type: "declareVar", Name: "A", VarType: "BOOL"})    // a step's name
	wantOpErr(t, baseline, EditOp{Type: "declareVar", Name: "1bad", VarType: "BOOL"}) // not an identifier
	wantOpErr(t, baseline, EditOp{Type: "declareVar", Name: "Y", VarType: "BOOL", Section: "VAR_INPUT"})
}

func TestOpDeleteVar(t *testing.T) {
	out, m := applyOp(t, baseline, EditOp{Type: "deleteVar", Name: "X"})
	if got := varNames(m); got != "VAR:Lamp:BOOL" {
		t.Fatalf("vars = %s\n%s", got, out)
	}
	if strings.Contains(out, "X : BOOL") {
		t.Fatalf("declaration left behind:\n%s", out)
	}
	wantOpErr(t, baseline, EditOp{Type: "deleteVar", Name: "Nope"})

	// A compact one-line section keeps its neighbours.
	compact := strings.Replace(baseline, "VAR\n  X : BOOL;\n  Lamp : BOOL;\nEND_VAR", "VAR X : BOOL; Lamp : BOOL; END_VAR", 1)
	out, m = applyOp(t, compact, EditOp{Type: "deleteVar", Name: "X"})
	if got := varNames(m); got != "VAR:Lamp:BOOL" || !strings.Contains(out, "VAR Lamp : BOOL; END_VAR") {
		t.Fatalf("compact delete: vars = %s\n%s", got, out)
	}
}

// Every declaration on a compact line is listed, not just the first.
func TestGraphCompactVars(t *testing.T) {
	compact := strings.Replace(baseline, "VAR\n  X : BOOL;\n  Lamp : BOOL;\nEND_VAR", "VAR X : BOOL; Lamp : BOOL; END_VAR", 1)
	m, err := Graph(compact)
	if err != nil {
		t.Fatal(err)
	}
	if got := varNames(m); got != "VAR:X:BOOL,VAR:Lamp:BOOL" {
		t.Fatalf("vars = %s", got)
	}
}
