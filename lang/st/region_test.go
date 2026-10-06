package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// #202: REGION name … END_REGION groups statements for folding and the
// outline. It nests, may sit in any statement list, and opens no scope.
func TestRegionIsANoOpGrouping(t *testing.T) {
	src := `
PROGRAM P
VAR_EXTERNAL Out : INT; END_VAR
VAR x : INT; Region : INT; END_VAR
REGION Fill the tank
    x := 1;
    REGION inner
        x := x + 10;
    END_REGION
    IF x > 5 THEN
        REGION in a branch
            x := x + 100;
        END_REGION;
    END_IF;
END_REGION
Region := 1000;
REGION
    Out := x + Region;
END_REGION
END_PROGRAM`
	h, _, _ := scanN(t, src, 1, map[string]ir.Value{"Out": ir.IntVal(0)})
	if got := h.globals["Out"].I; got != 1111 {
		t.Errorf("Out = %d, want 1111", got)
	}
	// No region node survives parsing: the statements are spliced in place.
	ast, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(ast.Statements); n != 5 {
		t.Errorf("top-level statements = %d, want 5 (x:=1, x:=x+10, IF, Region:=…, Out:=…)", n)
	}
}

func TestRegionInFBAndCase(t *testing.T) {
	src := `
FUNCTION_BLOCK F
VAR_OUTPUT o : INT; END_VAR
REGION outputs
  o := 3;
END_REGION
END_FUNCTION_BLOCK
PROGRAM P
VAR_EXTERNAL Out : INT; END_VAR
VAR f : F; s : INT := 1; END_VAR
CASE s OF
1:
  REGION one
    f();
  END_REGION
  Out := f.o;
2: Out := 2;
END_CASE;
END_PROGRAM`
	h, _, _ := scanN(t, src, 1, map[string]ir.Value{"Out": ir.IntVal(0)})
	if got := h.globals["Out"].I; got != 3 {
		t.Errorf("Out = %d, want 3", got)
	}
}

// A region declares nothing: a variable is not scoped to it.
func TestRegionOpensNoScope(t *testing.T) {
	lowerExpectErr(t, `
PROGRAM P
REGION a
  y := 1;
END_REGION
END_PROGRAM`, `undeclared identifier "y"`)
}

func TestRegionErrors(t *testing.T) {
	cases := map[string]string{
		"END_REGION alone": "PROGRAM P\nVAR x : INT; END_VAR\nx := 1;\nEND_REGION\nEND_PROGRAM\n",
		"unclosed":         "PROGRAM P\nVAR x : INT; END_VAR\nREGION Fill\nx := 1;\nEND_PROGRAM\n",
		"crosses END_IF":   "PROGRAM P\nVAR x : INT; END_VAR\nIF x > 0 THEN\nREGION r\nx := 1;\nEND_IF;\nEND_REGION\nEND_PROGRAM\n",
	}
	wants := map[string]string{
		"END_REGION alone": "line 4: END_REGION without a matching REGION",
		"unclosed":         "line 3: REGION Fill (opened here) has no END_REGION before",
		"crosses END_IF":   "REGION r (opened here) has no END_REGION before",
	}
	for name, src := range cases {
		_, err := Parse(src)
		if err == nil || !strings.Contains(err.Error(), wants[name]) {
			t.Errorf("%s: got %v, want %q", name, err, wants[name])
		}
	}
}
