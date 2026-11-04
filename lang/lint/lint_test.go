package lint

import "testing"

func TestMagicNumber(t *testing.T) {
	for arg, want := range map[string]bool{
		"90.0": true, "2": true, "-5": true, "REAL#1.5": true, "1E3": true, "1_000": true,
		"0": false, "0.0": false, "REAL#0": false, " 0 ": false,
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

func TestKnown(t *testing.T) {
	if !Known("ld001") || !Known("SFC002") || Known("LD999") || Known("") {
		t.Error("Known disagrees with the catalog")
	}
}
