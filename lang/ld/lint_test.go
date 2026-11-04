package ld

import (
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

func has(fs []lint.Finding, rule, phrase string) bool {
	for _, f := range fs {
		if f.Rule == rule && strings.Contains(f.Message, phrase) {
			return true
		}
	}
	return false
}
