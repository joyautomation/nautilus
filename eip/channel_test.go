package eip

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/eip/cip"
	"github.com/joyautomation/nautilus/lang/ir"
)

// The local twins of the emulator's Header_Type and Plt_Type (testSpec).
func pltShape() (ir.Value, *ir.StructDef) {
	hdr := &ir.StructDef{Name: "Header_Type", Fields: []ir.StructField{
		{Name: "Displacement", Type: ir.RealT},
		{Name: "Valid", Type: ir.BoolT},
	}}
	plt := &ir.StructDef{Name: "Plt_Type", Fields: []ir.StructField{
		{Name: "Header", Type: &ir.Type{Kind: ir.TypeStruct, Struct: hdr}},
		{Name: "Count", Type: ir.IntNamed("DINT")},
	}}
	return ir.Zero(&ir.Type{Kind: ir.TypeStruct, Struct: plt}), plt
}

func status(err error) int {
	var s interface{ CommStatus() int }
	if errors.As(err, &s) {
		return s.CommStatus()
	}
	return 0
}

func newTestChannel(t *testing.T) (*Channel, func(path string) any, func(path string, v any)) {
	t.Helper()
	host, port, store := startEmulator(t)
	ch := NewChannel(host, 0, port, nil)
	t.Cleanup(func() { _ = ch.Close() })
	get := func(path string) any { _, v, _ := store.Resolve(path); return v }
	set := func(path string, v any) { store.UpdateValue(path, v) }
	return ch, get, set
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

// A whole nested UDT reads in one Read Tag and lands by member name.
func TestChannelReadStruct(t *testing.T) {
	ch, _, set := newTestChannel(t)
	set("TRS.Header.Displacement", float64(12.5))
	set("TRS.Header.Valid", true)
	set("TRS.Count", float64(42))
	shape, _ := pltShape()
	v, err := ch.Read(ctx(t), "TRS", shape)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Fld[0].Fld[0].F; got != 12.5 {
		t.Errorf("Header.Displacement = %v, want 12.5", got)
	}
	if !v.Fld[0].Fld[1].B {
		t.Error("Header.Valid = false, want true")
	}
	if v.Fld[1].I != 42 {
		t.Errorf("Count = %d, want 42", v.Fld[1].I)
	}
	// (A member path, TRS.Header, resolves through templateFor too; the
	// emulator serves whole structures only at a root symbol, so
	// TestSplitVariable covers the path and a controller covers the read.)
}

// A whole UDT writes in one Write Tag laid out by the peer's template (James,
// 2026-11-16), and every member lands.
func TestChannelWriteStruct(t *testing.T) {
	ch, get, _ := newTestChannel(t)
	v, _ := pltShape()
	v.Fld[0].Fld[0] = ir.RealVal(3.25)
	v.Fld[0].Fld[1] = ir.BoolVal(true)
	v.Fld[1] = ir.IntVal(-7)
	if err := ch.Write(ctx(t), "TRSCmd", v); err != nil {
		t.Fatal(err)
	}
	if got := get("TRSCmd.Header.Displacement"); got != float64(3.25) {
		t.Errorf("Displacement = %v", got)
	}
	if got := get("TRSCmd.Header.Valid"); got != true {
		t.Errorf("Valid = %v", got)
	}
	if got := get("TRSCmd.Count"); got != float64(-7) {
		t.Errorf("Count = %v", got)
	}
	// A second write reuses what the first learned.
	v.Fld[1] = ir.IntVal(8)
	if err := ch.Write(ctx(t), "TRSCmd", v); err != nil || get("TRSCmd.Count") != float64(8) {
		t.Fatalf("second write: %v, Count = %v", err, get("TRSCmd.Count"))
	}
}

func TestChannelElementary(t *testing.T) {
	ch, get, set := newTestChannel(t)
	set("Speed", float64(1.5))
	set("Program:MainProgram.Motor", float64(9))
	if v, err := ch.Read(ctx(t), "Speed", ir.RealVal(0)); err != nil || v.F != 1.5 {
		t.Fatalf("Speed = %+v, %v", v, err)
	}
	if v, err := ch.Read(ctx(t), "Program:MainProgram.Motor", ir.IntVal(0)); err != nil || v.I != 9 {
		t.Fatalf("Motor = %+v, %v", v, err)
	}
	if err := ch.Write(ctx(t), "SpeedCmd", ir.RealVal(4.5)); err != nil || get("SpeedCmd") != float64(4.5) {
		t.Fatalf("SpeedCmd write: %v, = %v", err, get("SpeedCmd"))
	}
}

// Nothing moves in part: a value that does not fit is CommTypeClash.
func TestChannelTypeClash(t *testing.T) {
	ch, get, _ := newTestChannel(t)
	shape, plt := pltShape()
	for _, c := range []struct {
		name     string
		variable string
		shape    ir.Value
	}{
		{"struct into REAL", "TRS", ir.RealVal(0)},
		{"REAL into struct", "Speed", shape},
		{"REAL into BOOL", "Speed", ir.BoolVal(false)},
	} {
		if _, err := ch.Read(ctx(t), c.variable, c.shape); status(err) != ir.CommTypeClash {
			t.Errorf("%s: err = %v, want STATUS %d", c.name, err, ir.CommTypeClash)
		}
	}
	// A local struct whose member is named differently.
	other := *plt
	other.Fields = append([]ir.StructField(nil), plt.Fields...)
	other.Fields[1].Name = "Total"
	odd := ir.Zero(&ir.Type{Kind: ir.TypeStruct, Struct: &other})
	if _, err := ch.Read(ctx(t), "TRS", odd); status(err) != ir.CommTypeClash {
		t.Errorf("renamed member: err = %v", err)
	}
	if err := ch.Write(ctx(t), "TRSCmd", odd); status(err) != ir.CommTypeClash {
		t.Errorf("renamed member write: err = %v", err)
	}
	// A width mismatch: Count is a DINT on the peer.
	narrow := *plt
	narrow.Fields = append([]ir.StructField(nil), plt.Fields...)
	narrow.Fields[1].Type = ir.IntNamed("INT")
	nv := ir.Zero(&ir.Type{Kind: ir.TypeStruct, Struct: &narrow})
	if _, err := ch.Read(ctx(t), "TRS", nv); status(err) != ir.CommTypeClash {
		t.Errorf("INT for DINT: err = %v", err)
	}
	if get("TRSCmd.Count") != float64(0) {
		t.Error("a refused write moved data")
	}
}

// The peer's own refusal is the STATUS: CIP general status, as a Logix MSG
// shows it in .ERR.
func TestChannelPeerStatus(t *testing.T) {
	ch, _, _ := newTestChannel(t)
	_, err := ch.Read(ctx(t), "NoSuchTag", ir.IntVal(0))
	if s := status(err); s != int(cip.StatusPathDestUnknown) && s != int(cip.StatusPathSegmentError) {
		t.Fatalf("err = %v (STATUS %d), want a path status", err, s)
	}
}

func TestChannelUnreachable(t *testing.T) {
	ch := NewChannel("127.0.0.1", 0, 1, nil) // nothing listens on port 1
	c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := ch.Read(c, "Speed", ir.RealVal(0)); status(err) != ir.CommUnreachable {
		t.Fatalf("err = %v, want STATUS %d", err, ir.CommUnreachable)
	}
}

func TestSplitVariable(t *testing.T) {
	for _, c := range []struct {
		in, root string
		members  []string
	}{
		{"TRS", "TRS", nil},
		{"TRS.Header", "TRS", []string{"Header"}},
		{"Line[2].Pump.Hdr", "Line", []string{"Pump", "Hdr"}},
		{"Program:Main.Line[1].Pump", "Program:Main.Line", []string{"Pump"}},
	} {
		root, members := splitVariable(c.in)
		if root != c.root || len(members) != len(c.members) {
			t.Errorf("splitVariable(%q) = %q %v", c.in, root, members)
			continue
		}
		for i := range members {
			if members[i] != c.members[i] {
				t.Errorf("splitVariable(%q) = %q %v", c.in, root, members)
			}
		}
	}
}
