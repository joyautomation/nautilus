package modbus

import (
	"reflect"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// Each source is one connection: its own bindings and __Online and __Outages companions,
// and an up/down verdict of its own — a dead meter does not take its
// neighbour down with it.
func TestConnectionsSplitBySource(t *testing.T) {
	a, b := newSlave(t), newSlave(t)
	// Up means the unit answers: a slave with no unit 1 answers 0x0B.
	a.Unit(1)
	b.Unit(1)
	m := Manifest{
		Sources: []Source{fastSource("M1", a.Addr()), fastSource("M2", b.Addr())},
		Tags: []TagBinding{
			{Name: "M1_KW", Source: "M1", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "M1_PF", Source: "M1", Table: TableHolding, Address: 1, Format: "uint16"},
			{Name: "M2_KW", Source: "M2", Table: TableHolding, Address: 0, Format: "uint16"},
		},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	var _ nio.ConnectionReporter = d

	want := []nio.Connection{
		{ID: "M1", Inputs: []string{"M1_KW", "M1_PF", "M1__Online", "M1__Outages"}},
		{ID: "M2", Inputs: []string{"M2_KW", "M2__Online", "M2__Outages"}},
	}
	if got := d.Connections(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Connections = %+v, want %+v", got, want)
	}
	if d.ConnectionUp("M1") || d.ConnectionUp("nope") {
		t.Fatal("nothing is up before Start, and an unknown id is never up")
	}

	startDriver(t, d)
	waitFor(t, "both sources up", func() bool { return d.ConnectionUp("M1") && d.ConnectionUp("M2") })
	b.Stop()
	waitFor(t, "M2 down", func() bool { return !d.ConnectionUp("M2") })
	if !d.ConnectionUp("M1") {
		t.Fatal("M1 went down with M2")
	}
}
