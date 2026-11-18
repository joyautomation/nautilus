package ld

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// #783: rungs run top to bottom. A rung that reads a block's output ahead
// of the rung that calls it sees last scan's value (here: the count before
// this scan's count), and one that reads it after sees this scan's.
func TestRungsRunTopToBottom(t *testing.T) {
	scan, h := runLD(t, `PROGRAM p
VAR_EXTERNAL go : BOOL; Before : DINT; After : DINT; END_VAR
VAR c : CTU; END_VAR
LD
  RUNG before
    go { Before := c.CV }
  RUNG count
    +go c:CTU(PV := 100)
  RUNG after
    go { After := c.CV }
END_LD
END_PROGRAM`)
	scan(map[string]ir.Value{"go": ir.BoolVal(true)})
	if h.vals["Before"].I != 0 || h.vals["After"].I != 1 {
		t.Fatalf("scan 1: Before = %d (want 0, read ahead of the count), After = %d (want 1)", h.vals["Before"].I, h.vals["After"].I)
	}
	scan(nil)
	if h.vals["Before"].I != 1 || h.vals["After"].I != 1 {
		t.Fatalf("scan 2: Before = %d, After = %d, want 1 and 1 (the one-shot does not count again)", h.vals["Before"].I, h.vals["After"].I)
	}
}

// #783: a call on an element of an array of instances is a statement of
// its own rung's network too. It ran in network 0, ahead of its own
// rung's edge trigger, and counted a scan late.
func TestInstanceArrayCallRunsInItsRung(t *testing.T) {
	scan, h := runLD(t, `PROGRAM p
VAR_EXTERNAL go : BOOL; Before : DINT; After : DINT; END_VAR
VAR Counts : ARRAY [0..1] OF CTU; END_VAR
LD
  RUNG before
    go { Before := Counts[1].CV }
  RUNG count
    +go Counts[1]:CTU(PV := 100)
  RUNG after
    go { After := Counts[1].CV }
END_LD
END_PROGRAM`)
	scan(map[string]ir.Value{"go": ir.BoolVal(true)})
	if h.vals["Before"].I != 0 || h.vals["After"].I != 1 {
		t.Fatalf("scan 1: Before = %d (want 0), After = %d (want 1): the element call is out of rung order", h.vals["Before"].I, h.vals["After"].I)
	}
}
