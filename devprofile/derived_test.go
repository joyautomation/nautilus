package devprofile

import (
	"strings"
	"testing"
)

const derivedBase = `
profile: x
title: X
protocol: modbus
docs:
  - { id: m, title: manual }
points:
  - { name: Word,  type: WORD, table: holding, address: 1, doc: "m p. 1" }
  - { name: Code,  type: UINT, table: holding, address: 2, doc: "m p. 1" }
  - { name: Level, type: REAL, table: holding, address: 3, format: uint16, doc: "m p. 1" }
  - { name: Other, type: INT,  table: holding, address: 5, doc: "m p. 1" }
derived:
`

func TestDerivedValidation(t *testing.T) {
	for _, tc := range []struct{ row, want string }{
		{`  - { name: Tripped, from: Word, bit: 3, doc: "m p. 2" }`, ""},
		{`  - { name: Broken, from: code, eq: 39, unverified: true }`, ""},
		{`  - { name: Tripped, from: Nope, bit: 3, doc: "m p. 2" }`, `from: "Nope" is not a point`},
		{`  - { name: Tripped, from: Level, bit: 3, doc: "m p. 2" }`, "only an integer point"},
		{`  - { name: Tripped, from: Word, doc: "m p. 2" }`, "exactly one of bit: and eq:"},
		{`  - { name: Tripped, from: Word, bit: 3, eq: 1, doc: "m p. 2" }`, "exactly one of bit: and eq:"},
		{`  - { name: Tripped, from: Word, bit: 16, doc: "m p. 2" }`, "bit 16 is outside the 16 bits"},
		{`  - { name: Tripped, from: Word, bit: -1, doc: "m p. 2" }`, "outside"},
		{`  - { name: Word, from: Code, eq: 1, doc: "m p. 2" }`, "already a point"},
		{`  - { name: Tripped, from: Word, bit: 3 }`, "cite a document"},
		{`  - { name: Tripped, from: Word, bit: 3, doc: "zz p. 2" }`, "not the id of a document"},
		{`  - { name: Broken, from: Code, eq: 70000, doc: "m p. 2" }`, "outside UINT's range"},
		{`  - { name: Broken, from: Code, eq: -1, doc: "m p. 2" }`, "outside UINT's range"},
		{`  - { name: Broken, from: Other, eq: -1, doc: "m p. 2" }`, ""},
		{`  - { name: "2bad", from: Word, bit: 3, doc: "m p. 2" }`, "IEC identifier"},
	} {
		_, err := ParseProfile([]byte(derivedBase+tc.row+"\n"), "x.yaml")
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.row, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err %v, want it to contain %q", tc.row, err, tc.want)
		}
	}
}

func TestDerivedEval(t *testing.T) {
	bit, eq := 3, int64(39)
	b, e := Derived{Bit: &bit}, Derived{Eq: &eq}
	for _, tc := range []struct {
		d    Derived
		v    int64
		want bool
	}{
		{b, 0x08, true}, {b, 0x237, false}, {b, 0x23F, true}, {b, 0x07, false},
		{b, -1, true}, // an INT with every bit set
		{e, 39, true}, {e, 38, false}, {e, 0, false},
	} {
		if got := tc.d.Eval(tc.v); got != tc.want {
			t.Errorf("%+v.Eval(%d) = %v", tc.d, tc.v, got)
		}
	}
	if (Derived{}).Eval(0) {
		t.Error("a derived point with neither bit nor eq is true")
	}
}

// Selecting a derived point selects the point it reads; selecting the
// source alone does not select the derived point; a glob that matches
// neither is still an error.
func TestSelectAllPullsTheSource(t *testing.T) {
	p, err := ParseProfile([]byte(derivedBase+`  - { name: Tripped, from: Word, bit: 3, doc: "m p. 2" }`+"\n"), "x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	names := func(s Selected) string {
		var out []string
		for _, pt := range s.Points {
			out = append(out, pt.Name)
		}
		for _, d := range s.Derived {
			out = append(out, "~"+d.Name)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		globs []string
		want  string
	}{
		{nil, "Word,Code,Level,Other,~Tripped"},
		{[]string{"tripped"}, "Word,~Tripped"},
		{[]string{"Code", "Trip*"}, "Word,Code,~Tripped"},
		{[]string{"Word"}, "Word"},
	} {
		sel, err := p.SelectAll(tc.globs)
		if err != nil || names(sel) != tc.want {
			t.Errorf("SelectAll(%v) = %q, %v; want %q", tc.globs, names(sel), err, tc.want)
		}
	}
	if _, err := p.SelectAll([]string{"Nope"}); err == nil {
		t.Error("a glob that matches nothing was accepted")
	}
	if pts, err := p.Select([]string{"Tripped"}); err != nil || len(pts) != 1 || pts[0].Name != "Word" {
		t.Errorf("Select(Tripped) = %v, %v", pts, err)
	}
}

func TestTypesSTCarriesDerived(t *testing.T) {
	p, err := ParseProfile([]byte(derivedBase+`  - { name: Tripped, from: Word, bit: 3, desc: "tripped", doc: "m p. 2" }`+"\n"), "x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sel, _ := p.SelectAll(nil)
	src := TypesST([]Struct{{Name: "X", Points: sel.Points, Derived: sel.Derived}})
	if !strings.Contains(src, "    Tripped : BOOL; (* tripped *)\n") || strings.Index(src, "Tripped") < strings.Index(src, "Other") {
		t.Errorf("types:\n%s", src)
	}
}

func TestDerivedSame(t *testing.T) {
	b3, b4, e := 3, 4, int64(3)
	d := Derived{Name: "T", From: "W", Bit: &b3}
	for _, tc := range []struct {
		o    Derived
		want bool
	}{
		{Derived{Name: "t", From: "w", Bit: &b3}, true},
		{Derived{Name: "T", From: "W", Bit: &b4}, false},
		{Derived{Name: "T", From: "X", Bit: &b3}, false},
		{Derived{Name: "T", From: "W", Eq: &e}, false},
	} {
		if got := d.Same(tc.o); got != tc.want {
			t.Errorf("Same(%+v) = %v", tc.o, got)
		}
	}
}

func TestAlarmValidation(t *testing.T) {
	for _, tc := range []struct{ alarms, want string }{
		{`  - { point: Tripped, priority: high, on-delay: 2s }`, ""},
		{`  - { point: Flag }`, ""},
		{`  - { point: tripped, name: "{desc} t" }`, ""},
		{`  - { point: Word }`, "not a BOOL point or derived point"},
		{`  - { point: Nope }`, "not a BOOL point or derived point"},
		{`  - { }`, "point: is required"},
		{`  - { point: Tripped }` + "\n" + `  - { point: Tripped }`, "has an alarm already"},
		{`  - { point: Tripped, priority: urgent }`, "priority"},
		{`  - { point: Tripped, on-delay: soon }`, "on-delay"},
	} {
		src := derivedBase + `  - { name: Tripped, from: Word, bit: 3, doc: "m p. 2" }` + "\nalarms:\n" + tc.alarms + "\n"
		src = strings.Replace(src, "docs:\n", "docs:\n", 1)
		src = strings.Replace(src, "points:\n", "points:\n  - { name: Flag, type: BOOL, table: coil, address: 9, doc: \"m p. 1\" }\n", 1)
		_, err := ParseProfile([]byte(src), "x.yaml")
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.alarms, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err %v, want %q", tc.alarms, err, tc.want)
		}
	}
}
