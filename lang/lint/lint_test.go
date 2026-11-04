package lint

import (
	"strings"
	"testing"
)

func TestMagicNumber(t *testing.T) {
	for arg, want := range map[string]bool{
		"90.0": true, "2": true, "-5": true, "REAL#1.5": true, "1E3": true, "1_000": true, "1.0": true, "REAL#1": true, "-2": true,
		"0": false, "0.0": false, "REAL#0": false, " 0 ": false, "1": false, "-1": false, "INT#1": false, "+1": false,
		"T#5S": false, "HighSP": false, "'x'": false, "TRUE": false, "ADD(a, 1)": false,
	} {
		if got := MagicNumber(arg); got != want {
			t.Errorf("MagicNumber(%q) = %v, want %v", arg, got, want)
		}
	}
}

// A pragma covers the POU it sits in; one above every POU covers the file.
func TestFilterPragmaScope(t *testing.T) {
	src := `PROGRAM A
(* @allow LD003, LD005 — imported as-is *)
LD
END_LD
END_PROGRAM
FUNCTION_BLOCK B
LD
END_LD
END_FUNCTION_BLOCK
`
	fs := []Finding{
		{Rule: "LD003", Line: 3}, {Rule: "LD005", Line: 4}, {Rule: "LD004", Line: 3},
		{Rule: "LD003", Line: 7}, {Rule: "LD005", Line: 8},
	}
	got := Filter(src, Defaults(), fs)
	if len(got) != 3 || got[0].Rule != "LD004" || got[1].Line != 7 || got[2].Line != 8 {
		t.Errorf("POU-scoped pragma: got %v", got)
	}

	file := "(* @allow LD003 *)\n" + src
	got = Filter(file, Defaults(), []Finding{{Rule: "LD003", Line: 8}, {Rule: "LD005", Line: 9}})
	if len(got) != 1 || got[0].Rule != "LD005" {
		t.Errorf("file-wide pragma: got %v", got)
	}

	cfg := Defaults()
	cfg.Disabled = map[string]bool{"LD005": true}
	if got := Filter(src, cfg, []Finding{{Rule: "LD005", Line: 8}}); len(got) != 0 {
		t.Errorf("disabled rule: got %v", got)
	}
}

// A pragma in a RUNG header covers that rung, up to the next RUNG header or
// END_LD; one between rungs still covers the POU. A misspelt ID is
// reported where it is written, and the list goes on past it.
func TestFilterPragmaRungScopeAndUnknownIDs(t *testing.T) {
	src := `PROGRAM A
LD
  RUNG one (* @allow LD006: the vendor's trip point *)
    GT(L, 90.0) ( x )
  RUNG two (* r *)
    GT(L, 80.0) ( y )
  RUNG three (* @allow LD006 *)
    GT(L, 70.0) ( z )
END_LD
END_PROGRAM
FUNCTION_BLOCK B
LD
  RUNG four (* r *)
    GT(L, 60.0) ( w )
END_LD
END_FUNCTION_BLOCK
`
	fs := []Finding{{Rule: "LD006", Line: 3}, {Rule: "LD006", Line: 5}, {Rule: "LD006", Line: 7}, {Rule: "LD006", Line: 13}}
	got := Filter(src, Defaults(), fs)
	if len(got) != 2 || got[0].Line != 5 || got[1].Line != 13 {
		t.Errorf("rung-scoped pragmas: want rungs two and four left, got %v", got)
	}

	typo := "(* @allow LD0O5, LD003 FBD02: reasons *)\n" + src
	got = Filter(typo, Defaults(), []Finding{{Rule: "LD003", Line: 9}})
	if len(got) != 2 || got[0].Rule != PragmaRule || got[0].Col != 11 || got[1].Rule != PragmaRule || got[1].Col != 24 ||
		!strings.Contains(got[0].Message, "LD0O5 ") || !strings.Contains(got[1].Message, "FBD02 ") {
		t.Errorf("typos: want LD0O5 at col 11 and FBD02 at col 24 reported, LD003 allowed; got %v", got)
	}
	// Words after the IDs are the reason, not IDs, with or without a colon.
	if got := Filter("(* @allow LD003 kept as the vendor wrote it, in 2019 *)\n"+src, Defaults(), nil); len(got) != 0 {
		t.Errorf("a reason without a colon: got %v", got)
	}
}

func TestIsPragma(t *testing.T) {
	if !IsPragma(" @allow LD005: x") || IsPragma("why the rung exists") || IsPragma("") {
		t.Error("IsPragma")
	}
}

func TestKnown(t *testing.T) {
	if !Known("ld001") || !Known("SFC002") || Known("LD999") || Known("") {
		t.Error("Known disagrees with the catalog")
	}
}
