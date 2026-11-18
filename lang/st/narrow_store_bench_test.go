package st

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// #782: what a wrapped integer store costs. The same loop of 1000 stores
// into a local: an INT (wrapped to 16 bits, Assign.Wrap) and a LINT (no
// width to keep). Reported per store.
func benchStores(b *testing.B, typ string) {
	prog, err := lowerSrc(`PROGRAM P
VAR x : ` + typ + `; k : DINT; END_VAR
FOR k := 1 TO 1000 DO
  x := x + 7;
END_FOR;
END_PROGRAM`)
	if err != nil {
		b.Fatal(err)
	}
	frame := ir.NewFrame(prog)
	h := newStubHost()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := ir.Run(prog, frame, h); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1000, "ns/store")
}

func BenchmarkStoreWrappedINT(b *testing.B) { benchStores(b, "INT") }
func BenchmarkStoreLINT(b *testing.B)       { benchStores(b, "LINT") }
