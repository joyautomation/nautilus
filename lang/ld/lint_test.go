package ld

import (
	"fmt"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/lint"
)

// Each ladder structure rule fires on a minimal bad rung and is silent on
// the rewrite its message asks for (nautilus#561).
func TestLintLadderRules(t *testing.T) {
	prog := func(rungs string) string {
		return "PROGRAM P\nVAR a : BOOL; b : BOOL; c : BOOL; d : BOOL; e : BOOL; f : BOOL; g : BOOL; x : BOOL; y : BOOL; Level : REAL; HighSP : REAL; END_VAR\nLD\n" +
			rungs + "END_LD\nEND_PROGRAM\n"
	}
	for _, tc := range []struct {
		rule      string
		bad, good string
		want      string // a phrase the message must carry
	}{
		{"LD001",
			"  RUNG one (* r *)\n    a ( x )\n  RUNG two (* r *)\n    b ( x )\n",
			"  RUNG one (* r *)\n    [ a | b ] ( x )\n",
			"x already has a coil, ( x ) on rung one (line 4)"},
		{"LD001",
			"  RUNG set (* r *)\n    a ( S x )\n  RUNG plain (* r *)\n    b ( x )\n",
			"  RUNG set (* r *)\n    a ( S x )\n  RUNG reset (* r *)\n    b ( R x )\n",
			"x is both latched (( S x ), rung set) and driven by ( x ) (rung plain)"},
		{"LD002",
			"  RUNG deep (* r *)\n    [ a [ b | c ] | d ] ( x )\n",
			"  RUNG perm (* r *)\n    [ b | c ] ( y )\n  RUNG deep (* r *)\n    [ a y | d ] ( x )\n",
			"rung deep nests branches 2 deep (the limit is 1)"},
		{"LD003",
			"  RUNG long (* r *)\n    a b c d e f g ( x )\n",
			"  RUNG perm (* r *)\n    a b c d ( y )\n  RUNG long (* r *)\n    y e f g ( x )\n",
			"rung long has 7 elements in series (the limit is 6)"},
		{"LD004",
			"  RUNG rep (* r *)\n    [ a /b | c /b ] ( x )\n",
			"  RUNG rep (* r *)\n    [ a | c ] /b ( x )\n",
			"/b is on every leg of a branch in rung rep"},
		{"LD005",
			"  RUNG bare\n    a ( x )\n",
			"  RUNG bare (* why it exists *)\n    a ( x )\n",
			"rung bare has no comment"},
		{"LD006",
			"  RUNG hi (* r *)\n    GT(Level, 90.0) ( x )\n",
			"  RUNG hi (* r *)\n    GT(Level, HighSP) ( x )\n",
			"GT(Level, 90.0) compares against the literal 90.0"},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			bad := Lint(prog(tc.bad), lint.Defaults())
			if !has(bad, tc.rule, tc.want) {
				t.Errorf("bad sample: want %s saying %q, got %v", tc.rule, tc.want, bad)
			}
			for _, f := range bad {
				if f.Rule != tc.rule {
					t.Errorf("bad sample: an unrelated finding %v", f)
				}
			}
			if good := Lint(prog(tc.good), lint.Defaults()); len(good) != 0 {
				t.Errorf("the rewrite still warns: %v", good)
			}
		})
	}
}

// The edge cases that keep the rules precise.
func TestLintLadderQuietCases(t *testing.T) {
	src := `PROGRAM P
VAR a : BOOL; b : BOOL; x : BOOL; Count : INT; Level : REAL; t1 : TON; END_VAR
LD
  // a note above the rung counts as its comment
  RUNG noted
    a ( x )

  RUNG pair (* a set/reset pair on two rungs is the latch idiom *)
    a ( S y )
  RUNG pair2 (* r *)
    b ( R y )

  RUNG zero (* comparing with zero is not a magic number; nor is EQ on a mode *)
    GT(Count, 0) EQ(Count, 3) ( z )

  RUNG edges (* an edge contact on every leg is its own instance each time *)
    [ +a b | +a /b ] ( w )

  RUNG timer (* a TIME literal in the block that times it reads fine *)
    a t1:TON(PT := T#5S) ( v )
END_LD
END_PROGRAM
`
	if fs := Lint(src, lint.Defaults()); len(fs) != 0 {
		t.Errorf("want no findings, got %v", fs)
	}
}

// Thresholds come from the config, a disabled rule is silent, and an
// @allow pragma in a POU's header switches rules off for that POU only.
func TestLintLadderConfigAndPragma(t *testing.T) {
	src := `PROGRAM P
VAR a : BOOL; b : BOOL; c : BOOL; d : BOOL; e : BOOL; f : BOOL; g : BOOL; x : BOOL; END_VAR
LD
  RUNG long
    a b c d e f g ( x )
END_LD
END_PROGRAM

FUNCTION_BLOCK FB
(* @allow LD003 LD005: kept as the vendor wrote it *)
VAR_INPUT a : BOOL; b : BOOL; c : BOOL; d : BOOL; e : BOOL; f : BOOL; g : BOOL; END_VAR
VAR_OUTPUT x : BOOL; END_VAR
LD
  RUNG long
    a b c d e f g ( x )
END_LD
END_FUNCTION_BLOCK
`
	rules := func(fs []lint.Finding) string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Rule)
		}
		return strings.Join(out, ",")
	}
	if got := rules(Lint(src, lint.Defaults())); got != "LD003,LD005" {
		t.Errorf("defaults: %s, want the PROGRAM's LD003,LD005 only (the block allows both)", got)
	}
	cfg := lint.Defaults()
	cfg.MaxSeries = 7
	cfg.Disabled = map[string]bool{"LD005": true}
	if got := rules(Lint(src, cfg)); got != "" {
		t.Errorf("max-series 7, LD005 off: %s, want nothing", got)
	}
}

// LD001 explains a plain coil mixed with a latch by rung order: a later
// plain coil overwrites the latch; a later ( R x ) overrides the coil's
// result while its rung is true (the imported Line.L5X's Line_Accept,
// whose reset rung deliberately overrides the accept coil); a later
// ( S x ) forces it on. Each still fires: neither shape latches.
func TestLintLadderLD001RungOrder(t *testing.T) {
	prog := func(rungs string) string {
		return "PROGRAM P\nVAR a : BOOL; b : BOOL; x : BOOL; END_VAR\nLD\n" + rungs + "END_LD\nEND_PROGRAM\n"
	}
	for _, tc := range []struct{ name, rungs, want, not string }{
		{"plain after latch",
			"  RUNG latch (* r *)\n    a ( S x )\n  RUNG plain (* r *)\n    b ( x )\n",
			"rung plain comes later and writes x every scan, so it overwrites the latch", "forces"},
		{"reset after plain",
			"  RUNG accept (* r *)\n    a ( x )\n  RUNG interlock (* r *)\n    b ( R x )\n",
			"rung interlock comes later and forces x off while it is true, so the coil on rung accept only holds while rung interlock is false",
			"overwrites the latch"},
		{"set after plain",
			"  RUNG plain (* r *)\n    a ( x )\n  RUNG set (* r *)\n    b ( S x )\n",
			"rung set comes later and forces x on while it is true", "overwrites the latch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := Lint(prog(tc.rungs), lint.Defaults())
			if len(fs) != 1 || !has(fs, "LD001", tc.want) || strings.Contains(fs[0].Message, tc.not) {
				t.Errorf("want one LD001 saying %q (and not %q), got %v", tc.want, tc.not, fs)
			}
		})
	}
}

// LD004 suggests factoring a common contact only where moving it keeps
// the program: past plain and comparison contacts, never past a block.
func TestLintLadderLD004Order(t *testing.T) {
	prog := func(rung string) string {
		return "PROGRAM P\nVAR a : BOOL; b : BOOL; c : BOOL; y : BOOL; t1 : TON; t2 : TON; END_VAR\nLD\n  RUNG r (* r *)\n    " +
			rung + "\nEND_LD\nEND_PROGRAM\n"
	}
	for _, tc := range []struct{ rung, want string }{
		{"[ a b | a c ] ( y )", "a is on every leg of a branch in rung r: put it once, in series, before the branch"},
		// a after the timer: moving it ahead would change the timer's IN,
		// but after the branch it is the same program.
		{"[ t1:TON(PT := T#5S) a | b a ] ( y )", "after the branch"},
		// before a block on one leg, after one on the other: no move keeps it.
		{"[ a t1:TON(PT := T#5S) | t2:TON(PT := T#1S) a ] ( y )", ""},
	} {
		fs := Lint(prog(tc.rung), lint.Defaults())
		if tc.want == "" {
			if len(fs) != 0 {
				t.Errorf("%s: want nothing, got %v", tc.rung, fs)
			}
			if _, err := Graph(prog(tc.rung)); err != nil {
				t.Errorf("%s: does not parse: %v", tc.rung, err)
			}
			continue
		}
		if len(fs) != 1 || !has(fs, "LD004", tc.want) {
			t.Errorf("%s: want one LD004 saying %q, got %v", tc.rung, tc.want, fs)
		}
	}
}

// LD005: the // note must be on the line above the RUNG header (blank
// lines between are fine), not inside the previous rung's body; and an
// @allow pragma is not a comment.
func TestLintLadderLD005Placement(t *testing.T) {
	src := `PROGRAM P
VAR a : BOOL; b : BOOL; c : BOOL; x : BOOL; y : BOOL; z : BOOL; w : BOOL; END_VAR
LD
  // why a1 exists, with a blank line before its header

  RUNG a1
    a ( x )
  RUNG a2 (* r *)
    b
    // a note in a2's body
    c ( y )
  RUNG a3
    c ( z )
  RUNG a4 (* @allow LD003: the pragma is not the rung's comment *)
    a ( w )
END_LD
END_PROGRAM
`
	var got []string
	for _, f := range Lint(src, lint.Defaults()) {
		got = append(got, fmt.Sprintf("%s@%d", f.Rule, f.Line))
	}
	if strings.Join(got, ",") != "LD005@12,LD005@14" {
		t.Errorf("want LD005 on a3 (line 12) and a4 (line 14), got %v", got)
	}
}

// Comparisons against 0, 1 and -1 (integer) are counts, not thresholds,
// in every comparison; EQ and NE never fire; a real 1.0 still does.
func TestLintLadderLD006SmallCounts(t *testing.T) {
	prog := func(cond string) string {
		return "PROGRAM P\nVAR Count : INT; Level : REAL; x : BOOL; END_VAR\nLD\n  RUNG r (* r *)\n    " +
			cond + " ( x )\nEND_LD\nEND_PROGRAM\n"
	}
	for cond, fire := range map[string]bool{
		"GE(Count, 1)": false, "GT(Count, 0)": false, "LT(Count, 1)": false, "LE(Count, 0)": false,
		"GT(Count, -1)": false, "GE(Count, INT#1)": false, "EQ(Count, 1)": false, "NE(Count, 5)": false,
		"GE(Count, 2)": true, "LT(Count, 3)": true, "LE(Count, -2)": true, "GT(Level, 1.0)": true,
	} {
		fs := Lint(prog(cond), lint.Defaults())
		if got := has(fs, "LD006", "compares against the literal"); got != fire {
			t.Errorf("%s: LD006 %v, want %v (%v)", cond, got, fire, fs)
		}
	}
}

// A pragma in a rung header covers that rung only; a typo in one is a
// warning that names the bad ID, and the IDs it spelled right still work.
func TestLintLadderPragmaRungScopeAndTypos(t *testing.T) {
	src := `PROGRAM P
VAR Level : REAL; x : BOOL; y : BOOL; END_VAR
LD
  RUNG vendor (* @allow LD006: the vendor's trip point, kept as imported *)
    GT(Level, 90.0) ( x )
  RUNG ours (* our own threshold, so a literal is a bug here *)
    GT(Level, 80.0) ( y )
END_LD
END_PROGRAM
`
	fs := Lint(src, lint.Defaults())
	if len(fs) != 2 || fs[0].Rule != "LD005" || fs[0].Line != 4 || fs[1].Rule != "LD006" || fs[1].Line != 6 {
		t.Errorf("rung-scoped pragma: want LD005 on vendor (a pragma is no comment) and LD006 on ours only, got %v", fs)
	}

	typo := strings.Replace(src, "PROGRAM P\n", "PROGRAM P\n(* @allow LD0O5 LD006: a typo *)\n", 1)
	fs = Lint(typo, lint.Defaults())
	var bad, other int
	for _, f := range fs {
		switch f.Rule {
		case lint.PragmaRule:
			bad++
			if f.Line != 2 || f.Col != 11 || !strings.Contains(f.Message, "LD0O5 in this @allow pragma is not a structure rule") {
				t.Errorf("typo warning: %v", f)
			}
		default:
			other++
		}
	}
	if bad != 1 || other != 1 || !has(fs, "LD005", "rung vendor") {
		t.Errorf("want the typo reported and LD006 still allowed (only vendor's LD005 left), got %v", fs)
	}
}

// Ladder findings point at the rung's name on its header line.
func TestLintLadderColumnIsRungName(t *testing.T) {
	fs := Lint("PROGRAM P\nVAR a : BOOL; x : BOOL; END_VAR\nLD\n  RUNG bare\n    a ( x )\nEND_LD\nEND_PROGRAM\n", lint.Defaults())
	if len(fs) != 1 || fs[0].Line != 4 || fs[0].Col != 8 {
		t.Errorf("want LD005 at 4:8 (the name), got %v", fs)
	}
}

func has(fs []lint.Finding, rule, phrase string) bool {
	for _, f := range fs {
		if f.Rule == rule && strings.Contains(f.Message, phrase) {
			return true
		}
	}
	return false
}
