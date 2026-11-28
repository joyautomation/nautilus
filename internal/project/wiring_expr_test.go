package project

import (
	"strings"
	"testing"
	"testing/fstest"
)

const gaugeTypes = `TYPE GaugeSub : STRUCT X : REAL; Y : REAL; END_STRUCT; END_TYPE
TYPE Gauge : STRUCT
  Raw     : INT;
  Fault   : BOOL;
  Healthy : BOOL;
  Pct     : REAL;
  A       : REAL;
  B       : REAL;
  Idx     : INT;
  Vals    : ARRAY[0..1] OF REAL;
  Sub     : GaugeSub;
END_STRUCT; END_TYPE
`

// gaugeProject is a project with one wiring of Gauge; members are the
// wiring's extra member lines, insts its instance lines, lib a library.
func gaugeProject(members, insts, lib, tags string) fstest.MapFS {
	manifest := "name: g\ntasks: [{ program: main.st }]\n" + tags + `wirings:
  - type: Gauge
    params: { endpoint: null, k: 1.0, peer: G1 }
    sources:
      drive: { driver: modbus, endpoint: "{endpoint}" }
    members:
      Raw:   { from: drive, table: holding, address: 0, format: int16 }
      Fault: { from: drive, table: coil, address: 0 }
` + members + "instances:\n" + insts
	fsys := fstest.MapFS{
		"nautilus.yaml": {Data: []byte(manifest)},
		"main.st":       {Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
		"types.st":      {Data: []byte(gaugeTypes)},
	}
	if lib != "" {
		fsys["lib/f.st"] = &fstest.MapFile{Data: []byte(lib)}
	}
	return fsys
}

const oneGauge = "  - { name: G1, type: Gauge, params: { endpoint: 192.0.2.1 } }\n"
const twoGauges = oneGauge + "  - { name: G2, type: Gauge, params: { endpoint: 192.0.2.2 } }\n"

const pureLib = "FUNCTION Scale : REAL\nVAR_INPUT x : INT; k : REAL; END_VAR\nScale := INT_TO_REAL(x) * k;\nEND_FUNCTION\n" +
	"FUNCTION SubSum : REAL\nVAR_INPUT s : GaugeSub; END_VAR\nSubSum := s.X + s.Y;\nEND_FUNCTION\n"

// Each expr: rule of §6 and §15 is a check problem on the member's line,
// naming the member and the fix; an expression that checks clean is still
// refused at load, since this build does not evaluate it (slice 8b).
func TestExprMemberChecks(t *testing.T) {
	for _, c := range []struct {
		name, members, insts, lib, tags, want string
	}{
		{"clean: siblings, a param, a pure FUNCTION, a project tag, another instance",
			"      Healthy: { expr: \"NOT Fault AND Level > 2.0 AND NOT G2.Fault\" }\n      Pct: { expr: \"Scale(Raw, {k}) + SubSum(Sub)\" }\n",
			twoGauges, pureLib, "tags:\n  - { name: Level, role: input, init: 0.0 }\n",
			"instances G1, G2: member \"Healthy\": expr: checks clean, but this build does not evaluate expressions yet: they run from slice 8b of struct wiring (#1458)"},
		{"a sibling wins over a tag of its name", "      Healthy: { expr: \"NOT Fault\" }\n", oneGauge, "", "tags:\n  - { name: Fault, role: input, init: 0 }\n",
			"checks clean"},
		{"a second statement", "      Healthy: { expr: \"TRUE; Fault := TRUE\" }\n", oneGauge, "", "",
			`member "Healthy": expr: "TRUE; Fault := TRUE" is not one expression`},
		{"a statement through a param", "      Healthy: { expr: \"{k}\" }\n", strings.Replace(oneGauge, "192.0.2.1 }", "192.0.2.1, k: \"TRUE; Fault := TRUE\" }", 1), "", "",
			"is not one expression"},
		{"narrowing", "      Healthy: { expr: \"Raw\" }\n", oneGauge, "", "",
			`member "Healthy": expr: "Raw" does not compile: it gives INT, but the member is BOOL; an expr: must give its member's type without narrowing`},
		{"an unknown name", "      Healthy: { expr: \"Nope AND Fault\" }\n", oneGauge, "", "",
			`"Nope AND Fault" does not compile: undeclared identifier "Nope"`},
		{"two bad units, both named", "      Healthy: { expr: \"Nope\" }\n      Pct: { expr: \"Fault\" }\n", oneGauge, "", "",
			`member "Pct": expr: "Fault" does not compile: it gives BOOL, but the member is REAL`},
		{"a function with VAR_EXTERNAL", "      Pct: { expr: \"Outer(Raw)\" }\n", oneGauge,
			"FUNCTION Bad : REAL\nVAR_EXTERNAL G : REAL; END_VAR\nBad := G;\nEND_FUNCTION\nFUNCTION Outer : REAL\nVAR_INPUT x : INT; END_VAR\nOuter := Bad();\nEND_FUNCTION\n", "",
			"calls Outer (through Outer → Bad), which at lib/f.st:2 declares VAR_EXTERNAL, so it reaches a global; an expression only computes from what it reads, so a function it calls must too: make Bad a program or a block instead"},
		{"a loop", "      Pct: { expr: \"Loopy(Raw)\" }\n", oneGauge,
			"FUNCTION Loopy : REAL\nVAR_INPUT x : INT; END_VAR\nWHILE x > 0 DO x := x - 1; END_WHILE;\nLoopy := 0.0;\nEND_FUNCTION\n", "",
			"which at lib/f.st:3 has a WHILE loop, which might not finish"},
		{"a system function", "      Healthy: { expr: \"FIRST_SCAN() AND Fault\" }\n", oneGauge, "", "",
			`"FIRST_SCAN() AND Fault" calls the system function FIRST_SCAN(), which reads controller state; an expression only computes from what it reads: compute this in a program or a block instead`},
		{"a cycle", "      A: { expr: \"B + 1.0\" }\n      B: { expr: \"A + 1.0\" }\n", oneGauge, "", "",
			"it is in a cycle, G1.A → G1.B → G1.A"},
		{"a self-read", "      A: { expr: \"A + 1.0\" }\n", oneGauge, "", "",
			"it reads itself (G1.A)"},
		{"a whole sub-struct read", "      A: { expr: \"SubSum(Sub)\" }\n      Sub.X: { expr: \"A\" }\n", oneGauge, pureLib, "",
			"it is in a cycle, G1.A → G1.Sub.X → G1.A"},
		{"a variable index reads the whole array", "      Vals[1]: { expr: \"Vals[Idx] + 1.0\" }\n", oneGauge, "", "",
			"it reads itself (G1.Vals[1])"},
		{"an FB instance held in a tag", "      Healthy: { expr: \"T1(IN := Fault, PT := T#1s).Q\" }\n", oneGauge, "", "tags:\n  - { name: T1, role: state, type: TON }\n",
			"calls T1, which is not a FUNCTION or a standard function (a function block instance keeps state)"},
		{"a comment hides nothing from the compile", "      Healthy: { expr: \"Fault // why\\n AND Nope\" }\n", oneGauge, "", "",
			`undeclared identifier "Nope"`},
		{"a comment before the rest of a clean expression", "      Healthy: { expr: \"Raw // why\\n > 3\" }\n", oneGauge, "", "",
			"checks clean"},
		{"an array member", "      Vals[0]: { expr: \"INT_TO_REAL(Raw)\" }\n", oneGauge, "", "",
			"checks clean"},
		{"an array member, narrowing", "      Vals[0]: { expr: \"Fault\" }\n", oneGauge, "", "",
			"it gives BOOL, but the member is REAL"},
		{"a cycle across instances", "      A: { expr: \"{peer}.A\" }\n", strings.Replace(twoGauges, "192.0.2.2 }", "192.0.2.2, peer: G1 }", 1) + "", "", "",
			"G1.A → G2.A → G1.A"},
	} {
		fsys := gaugeProject(c.members, strings.Replace(c.insts, "192.0.2.1 }", "192.0.2.1, peer: G2 }", 1), c.lib, c.tags)
		_, err := Load(fsys, "")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s:\n got %v\nwant %q", c.name, err, c.want)
		}
	}
}

// Members that all read the whole instance form one ring: it is one
// problem naming every member, and the check stays fast, however many
// there are (every simple cycle would be factorial).
func TestExprDenseCycleIsOneProblem(t *testing.T) {
	lib := "FUNCTION GaugeFn : REAL\nVAR_INPUT g : Gauge; END_VAR\nGaugeFn := g.A;\nEND_FUNCTION\n"
	var members string
	for _, m := range []string{"Pct", "A", "B", "Vals[0]", "Vals[1]", "Sub.X", "Sub.Y"} {
		members += "      " + m + ": { expr: \"GaugeFn(G1)\" }\n"
	}
	_, err := Load(gaugeProject(members, oneGauge, lib, ""), "")
	if err == nil || strings.Count(err.Error(), "it is in a cycle") != 1 || len(err.Error()) > 2000 ||
		!strings.Contains(err.Error(), "the expr: members G1.A, G1.B, G1.Pct, G1.Sub.X, G1.Sub.Y, G1.Vals[0], G1.Vals[1] read each other") {
		t.Fatalf("%d bytes: %v", len(fmtErr(err)), err)
	}
}

func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
