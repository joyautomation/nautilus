package hdrvars

import (
	"strings"
	"testing"
)

func names(ds []Decl) string {
	var s []string
	for _, d := range ds {
		s = append(s, d.Section+":"+d.Name+":"+d.Type)
	}
	return strings.Join(s, ",")
}

func TestScanCompactForms(t *testing.T) {
	cases := []struct{ src, want string }{
		{"PROGRAM P\nVAR A : BOOL; B : BOOL; END_VAR\n", "VAR:A:BOOL,VAR:B:BOOL"},
		{"VAR\n  A : BOOL; B : INT := 3;\n  C : REAL;\nEND_VAR", "VAR:A:BOOL,VAR:B:INT,VAR:C:REAL"},
		{"VAR_EXTERNAL X : BOOL; END_VAR VAR Y : INT; END_VAR", "VAR_EXTERNAL:X:BOOL,VAR:Y:INT"},
		{"VAR_IN_OUT\nZ : INT;\nEND_VAR", "VAR_IN_OUT:Z:INT"},
		{"VAR RETAIN\n  Q : DINT;\nEND_VAR", "VAR:Q:DINT"},
		{"VAR\n  (* A : BOOL; *) B : BOOL; // C : BOOL;\nEND_VAR", "VAR:B:BOOL"},
		{"VAR\n  S : STRING := 'a;b'; T : BOOL;\nEND_VAR", "VAR:S:STRING,VAR:T:BOOL"},
		{"(* VAR X : BOOL; END_VAR *)\nVAR\nY : BOOL;\nEND_VAR", "VAR:Y:BOOL"},
		{"VAR\n  Arr : ARRAY[1..3] OF INT := [1,2,3];\nEND_VAR", "VAR:Arr:ARRAY[1..3] OF INT"},
	}
	for _, c := range cases {
		if got := names(Scan(c.src)); got != c.want {
			t.Errorf("Scan(%q) = %s, want %s", c.src, got, c.want)
		}
	}
	ds := Scan("PROGRAM P\nVAR A : BOOL; B : INT := 2; END_VAR\n")
	if ds[1].Line != 2 || ds[1].Init != "2" {
		t.Errorf("B = %+v", ds[1])
	}
}

func TestDeleteSpan(t *testing.T) {
	cases := []struct {
		line, name, want string // want = line after removal ("" + whole)
		whole                  bool
	}{
		{"  A : BOOL;", "A", "", true},
		{"  A : BOOL; B : INT;", "A", "  B : INT;", false},
		{"  A : BOOL; B : INT;", "b", "  A : BOOL;", false},
		{"VAR A : BOOL; B : BOOL; END_VAR", "A", "VAR B : BOOL; END_VAR", false},
		{"VAR A : BOOL; END_VAR", "A", "VAR END_VAR", false},
		{"  A : BOOL; (* note *)", "A", "", true},
	}
	for _, c := range cases {
		col, end, whole, ok := DeleteSpan(c.line, c.name)
		if !ok {
			t.Errorf("%q: %s not found", c.line, c.name)
			continue
		}
		if whole != c.whole {
			t.Errorf("%q: whole=%v", c.line, whole)
			continue
		}
		if whole {
			continue
		}
		if got := c.line[:col-1] + c.line[end-1:]; got != c.want {
			t.Errorf("%q delete %s = %q, want %q", c.line, c.name, got, c.want)
		}
	}
	if _, _, _, ok := DeleteSpan("  A : BOOL;", "Z"); ok {
		t.Error("found a missing name")
	}
}
