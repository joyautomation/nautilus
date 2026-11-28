package project

import (
	"strings"
	"testing"
	"testing/fstest"
)

// WRITTEN(<path>) at check time (#1458 slice 8c): naut check resolves the
// path as the stage does (runtime.WrittenResolver), so a path that is not one
// to: leaf, or a project name WRITTEN in an expression that calls it, is a
// problem on the member's line, and the rest loads.
func TestExprWrittenChecks(t *testing.T) {
	const idxTo = "      Idx: { to: drive, table: holding, address: 5, format: int16 }\n"
	const fn = "FUNCTION Written : BOOL\nVAR_INPUT x : INT; END_VAR\nWritten := x > 0;\nEND_FUNCTION\n"
	for _, c := range []struct {
		name, members, insts, lib, tags, want string
	}{
		{"clean: a sibling to: leaf", idxTo + "      Healthy: { expr: \"WRITTEN(Idx) AND NOT Fault\" }\n", oneGauge, "", "", ""},
		{"clean: another instance's", idxTo + "      Healthy: { expr: \"WRITTEN(G2.Idx)\" }\n", twoGauges, "", "", ""},
		{"clean: a FUNCTION Written no expression calls", idxTo + "      Healthy: { expr: \"NOT Fault\" }\n", oneGauge, fn, "", ""},
		{"a from: member", idxTo + "      Healthy: { expr: \"WRITTEN(Raw)\" }\n", twoGauges, "", "",
			`instances G1, G2: member "Healthy": expr: "WRITTEN(Raw)" does not compile: WRITTEN: Raw of Gauge is a from: member, and WRITTEN tells whether a to: member has been written in this leadership; Idx of Gauge is one`},
		{"a group", idxTo + "      Healthy: { expr: \"WRITTEN(Sub)\" }\n", oneGauge, "", "",
			"WRITTEN: Sub of Gauge is not one leaf: name one to: member; Idx of Gauge is one"},
		{"no to: member at all", "      Healthy: { expr: \"WRITTEN(Raw)\" }\n", oneGauge, "", "",
			"Raw of Gauge is a from: member, and WRITTEN tells whether a to: member has been written in this leadership; Gauge has no to: member"},
		{"not a path", idxTo + "      Healthy: { expr: \"WRITTEN(Idx + 1)\" }\n", oneGauge, "", "",
			"WRITTEN takes one argument, the path of a to: member with integer literal indices, one per bracket (Grid[0][1]), such as WRITTEN(Cmd.SpeedRef)"},
		{"a FUNCTION Written", idxTo + "      Healthy: { expr: \"WRITTEN(Idx)\" }\n", oneGauge, fn, "",
			"the project's FUNCTION Written hides the built-in WRITTEN(<path>) this expression calls: rename it"},
		{"its path is no read, so no cycle: only the refusal", idxTo + "      Healthy: { expr: \"WRITTEN(A)\" }\n      A: { expr: \"SEL(Healthy, 0.0, 1.0)\" }\n", oneGauge, "", "",
			"WRITTEN: A of Gauge is an expr: member"},
		{"a tag Written", idxTo + "      Healthy: { expr: \"WRITTEN(Idx)\" }\n", oneGauge, "", "tags:\n  - { name: Written, role: state, type: BOOL }\n",
			"the project's tag Written hides the built-in WRITTEN(<path>)"},
	} {
		fsys := gaugeProject(c.members, strings.Replace(c.insts, "192.0.2.1 }", "192.0.2.1, peer: G2 }", 1), c.lib, c.tags)
		_, err := Load(fsys, "")
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v, want it to load", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "cycle") {
			t.Errorf("%s:\n got %v\nwant %q", c.name, err, c.want)
		}
	}
}

// The stage compiles with the main program's own FUNCTIONs and blocks,
// which the check's prelude lacks, so check refuses one named WRITTEN in the
// main file too (review of #1491: check passed and the start refused).
func TestExprWrittenMainFileClash(t *testing.T) {
	const idxTo = "      Idx: { to: drive, table: holding, address: 5, format: int16 }\n"
	const fn = "FUNCTION Written : BOOL\nVAR_INPUT x : INT; END_VAR\nWritten := x > 0;\nEND_FUNCTION\n"
	for _, c := range []struct{ name, members, want, main string }{
		{"a main-file FUNCTION Written", idxTo + "      Healthy: { expr: \"WRITTEN(Idx)\" }\n",
			`"WRITTEN(Idx)" does not compile: the project's FUNCTION Written (main.st) hides the built-in WRITTEN(<path>) this expression calls: rename it`, fn + "PROGRAM Main\nEND_PROGRAM\n"},
		{"a main-file FUNCTION_BLOCK Written", idxTo + "      Healthy: { expr: \"NOT WRITTEN(Idx)\" }\n",
			"the project's FUNCTION_BLOCK Written (main.st) hides", "FUNCTION_BLOCK Written\nVAR_INPUT x : INT; END_VAR\nEND_FUNCTION_BLOCK\nPROGRAM Main\nEND_PROGRAM\n"},
		{"clean: a main-file FUNCTION Written no expression calls", idxTo + "      Healthy: { expr: \"NOT Fault\" }\n", "", fn + "PROGRAM Main\nEND_PROGRAM\n"},
	} {
		fsys := gaugeProject(c.members, oneGauge, "", "")
		fsys["main.st"].Data = []byte(c.main)
		_, err := Load(fsys, "")
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v, want it to load", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s:\n got %v\nwant %q", c.name, err, c.want)
		}
	}
}

// A ladder main file, or an ST one of another extension, is read by its
// content, as the runtime reads it (re-check of #1491).
func TestExprWrittenMainFileClashAnyLanguage(t *testing.T) {
	const idxTo = "      Idx: { to: drive, table: holding, address: 5, format: int16 }\n"
	ladder := "FUNCTION_BLOCK Written\nVAR_INPUT Start : BOOL; END_VAR\nVAR_OUTPUT Run : BOOL; END_VAR\nLD\n  RUNG a\n    Start ( Run )\nEND_LD\nEND_FUNCTION_BLOCK\n" +
		"PROGRAM Main\nVAR a, b : BOOL; END_VAR\nLD\n  RUNG r\n    a ( b )\nEND_LD\nEND_PROGRAM\n"
	for name, c := range map[string]struct{ file, src, want string }{
		"ladder":     {"main.ld", ladder, "the project's FUNCTION_BLOCK Written (main.ld) hides"},
		"ST as .txt": {"main.txt", "FUNCTION wRiTtEn : BOOL\nVAR_INPUT x : INT; END_VAR\nwRiTtEn := x > 0;\nEND_FUNCTION\nPROGRAM Main\nEND_PROGRAM\n", "the project's FUNCTION wRiTtEn (main.txt) hides"},
	} {
		fsys := gaugeProject(idxTo+"      Healthy: { expr: \"WRITTEN(Idx)\" }\n", oneGauge, "", "")
		fsys["nautilus.yaml"].Data = []byte(strings.Replace(string(fsys["nautilus.yaml"].Data), "program: main.st", "program: "+c.file, 1))
		delete(fsys, "main.st")
		fsys[c.file] = &fstest.MapFile{Data: []byte(c.src)}
		if _, err := Load(fsys, ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v\nwant %q", name, err, c.want)
		}
	}
}
