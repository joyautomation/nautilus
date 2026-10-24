package modbus

import (
	"math"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// A float the device delivers as NaN — a groov EPIC's unwired ICTD channel —
// is delivered as NaN (the program sees what the device said) and its
// quality is Bad: the link is fine, this reading is not. A finite neighbour
// on the same block stays Good.
func TestNaNFloatIsBadQuality(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	nan, _ := encodeRegisters(Format{Kind: "float32"}, math.NaN(), "", "", 0, 0)
	u.SetInput(0, nan[0])
	u.SetInput(1, nan[1])
	fin, _ := encodeRegisters(Format{Kind: "float32"}, 18.5, "", "", 0, 0)
	u.SetInput(2, fin[0])
	u.SetInput(3, fin[1])

	m := Manifest{
		Sources: []Source{fastSource("EPIC", srv.Addr())},
		Tags: []TagBinding{
			{Name: "AI01", Source: "EPIC", Table: TableInput, Address: 0, Format: "float32"},
			{Name: "AI00", Source: "EPIC", Table: TableInput, Address: 2, Format: "float32"},
		},
	}
	d, err := New(m, WithScanRate(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	var vals nio.Values
	waitFor(t, "both delivered", func() bool {
		v, err := d.ReadInputs()
		if err != nil {
			return false
		}
		vals = v
		return v["AI00"] != nil && v["AI01"] != nil
	})
	if f, ok := vals["AI01"].(float64); !ok || !math.IsNaN(f) {
		t.Errorf("AI01 = %v (%T), want NaN", vals["AI01"], vals["AI01"])
	}
	if vals["AI00"] != 18.5 {
		t.Errorf("AI00 = %v, want 18.5", vals["AI00"])
	}
	q := d.Quality()
	if q["AI01"] != nio.Bad {
		t.Errorf("AI01 quality = %v, want Bad", q["AI01"])
	}
	if _, marked := q["AI00"]; marked {
		t.Errorf("AI00 quality = %v, want Good (absent)", q["AI00"])
	}
}
