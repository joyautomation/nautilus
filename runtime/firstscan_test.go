package runtime

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

const firstScanSrc = `PROGRAM Main
VAR_EXTERNAL
  Firsts : REAL;
  Scans : REAL;
END_VAR
IF FIRST_SCAN() THEN
  Firsts := Firsts + 1.0;
END_IF;
Scans := Scans + 1.0;
END_PROGRAM`

// FIRST_SCAN() is TRUE for one scan after a start or a download (a cold
// swap, which resets the state), and an online edit (a warm swap, which
// keeps it) or a rollback is not a start.
func TestFirstScan(t *testing.T) {
	p, err := Compile(firstScanSrc)
	if err != nil {
		t.Fatal(err)
	}
	tags := NewTags()
	tags.SetReal("Firsts", 0)
	tags.SetReal("Scans", 0)
	scan := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if err := p.Run(tags); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := func(firsts, scans float64) {
		t.Helper()
		if f, s := tags.Real("Firsts"), tags.Real("Scans"); f != firsts || s != scans {
			t.Fatalf("Firsts=%v Scans=%v, want %v %v", f, s, firsts, scans)
		}
	}
	scan(3)
	want(1, 3)

	edited := strings.Replace(firstScanSrc, "Scans + 1.0", "Scans + 1.0 + 0.0", 1)
	if _, err := p.SwapWarm(edited); err != nil {
		t.Fatal(err)
	}
	scan(2)
	want(1, 5) // an online edit is not a first scan
	if _, err := p.Rollback(); err != nil {
		t.Fatal(err)
	}
	scan(1)
	want(1, 6)

	if err := p.Swap(firstScanSrc); err != nil { // a download
		t.Fatal(err)
	}
	scan(2)
	want(2, 8)
}

// A host that does not implement ir.ScanInfo (a bare VM) has no first scan.
func TestFirstScanWithoutScanInfo(t *testing.T) {
	sig := ir.Builtins["FIRST_SCAN"]
	if v, err := sig.HostFn(nil, nil); err != nil || v.B {
		t.Fatalf("nil host: %v %v", v, err)
	}
}
