package eip

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/eip/cip"
	"github.com/joyautomation/nautilus/eip/logixserver"
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
	v, err := ch.Read(ctx(t), "TRS", shape, "")
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
	if err := ch.Write(ctx(t), "TRSCmd", v, ""); err != nil {
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
	if err := ch.Write(ctx(t), "TRSCmd", v, ""); err != nil || get("TRSCmd.Count") != float64(8) {
		t.Fatalf("second write: %v, Count = %v", err, get("TRSCmd.Count"))
	}
}

func TestChannelElementary(t *testing.T) {
	ch, get, set := newTestChannel(t)
	set("Speed", float64(1.5))
	set("Program:MainProgram.Motor", float64(9))
	if v, err := ch.Read(ctx(t), "Speed", ir.RealVal(0), ""); err != nil || v.F != 1.5 {
		t.Fatalf("Speed = %+v, %v", v, err)
	}
	if v, err := ch.Read(ctx(t), "Program:MainProgram.Motor", ir.IntVal(0), ""); err != nil || v.I != 9 {
		t.Fatalf("Motor = %+v, %v", v, err)
	}
	if err := ch.Write(ctx(t), "SpeedCmd", ir.RealVal(4.5), "REAL"); err != nil || get("SpeedCmd") != float64(4.5) {
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
		if _, err := ch.Read(ctx(t), c.variable, c.shape, ""); status(err) != ir.CommTypeClash {
			t.Errorf("%s: err = %v, want STATUS %d", c.name, err, ir.CommTypeClash)
		}
	}
	// A local struct whose member is named differently.
	other := *plt
	other.Fields = append([]ir.StructField(nil), plt.Fields...)
	other.Fields[1].Name = "Total"
	odd := ir.Zero(&ir.Type{Kind: ir.TypeStruct, Struct: &other})
	if _, err := ch.Read(ctx(t), "TRS", odd, ""); status(err) != ir.CommTypeClash {
		t.Errorf("renamed member: err = %v", err)
	}
	if err := ch.Write(ctx(t), "TRSCmd", odd, ""); status(err) != ir.CommTypeClash {
		t.Errorf("renamed member write: err = %v", err)
	}
	// A width mismatch: Count is a DINT on the peer.
	narrow := *plt
	narrow.Fields = append([]ir.StructField(nil), plt.Fields...)
	narrow.Fields[1].Type = ir.IntNamed("INT")
	nv := ir.Zero(&ir.Type{Kind: ir.TypeStruct, Struct: &narrow})
	if _, err := ch.Read(ctx(t), "TRS", nv, ""); status(err) != ir.CommTypeClash {
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
	_, err := ch.Read(ctx(t), "NoSuchTag", ir.IntVal(0), "")
	if s := status(err); s != int(cip.StatusPathDestUnknown) && s != int(cip.StatusPathSegmentError) {
		t.Fatalf("err = %v (STATUS %d), want a path status", err, s)
	}
}

func TestChannelUnreachable(t *testing.T) {
	ch := NewChannel("127.0.0.1", 0, 1, nil) // nothing listens on port 1
	c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := ch.Read(c, "Speed", ir.RealVal(0), ""); status(err) != ir.CommUnreachable {
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

// arraySpec serves the shapes the review asked about: a REAL array large
// enough to need fragmented transfer, an array of UDTs, and narrow
// integers.
func arraySpec() *logixserver.TagSurfaceSpec {
	s := testSpec()
	s.Symbols = append(s.Symbols,
		logixserver.SymbolSpec{Name: "Trend", Datatype: "REAL", Dims: []uint32{200}},
		logixserver.SymbolSpec{Name: "Pallets", Datatype: "Plt_Type", Dims: []uint32{3}},
		logixserver.SymbolSpec{Name: "Small", Datatype: "SINT"},
		logixserver.SymbolSpec{Name: "Word", Datatype: "INT"},
	)
	for i := 0; i < 200; i++ {
		s.Tags = append(s.Tags, logixserver.TagSpec{Path: fmt.Sprintf("Trend[%d]", i), Datatype: "REAL"})
	}
	for i := 0; i < 3; i++ {
		s.Tags = append(s.Tags,
			logixserver.TagSpec{Path: fmt.Sprintf("Pallets[%d].Header.Displacement", i), Datatype: "REAL"},
			logixserver.TagSpec{Path: fmt.Sprintf("Pallets[%d].Header.Valid", i), Datatype: "BOOL"},
			logixserver.TagSpec{Path: fmt.Sprintf("Pallets[%d].Count", i), Datatype: "DINT"})
	}
	s.Tags = append(s.Tags, logixserver.TagSpec{Path: "Small", Datatype: "SINT"}, logixserver.TagSpec{Path: "Word", Datatype: "INT"})
	return s
}

func newSpecChannel(t *testing.T, spec *logixserver.TagSurfaceSpec) (*Channel, *logixserver.TagStore) {
	t.Helper()
	schema, tags, name, err := logixserver.CompileSurface(spec)
	if err != nil {
		t.Fatal(err)
	}
	store := logixserver.NewTagStore()
	for _, tc := range tags {
		store.Set(tc.Path, tc.LeafType, tc.Default)
	}
	srv := logixserver.NewServer(store, schema, name, "127.0.0.1:0", nil)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	_, ps, _ := net.SplitHostPort(srv.Addr())
	port, _ := strconv.Atoi(ps)
	c, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Run(c) }()
	ch := NewChannel("127.0.0.1", 0, port, nil)
	t.Cleanup(func() { _ = ch.Close() })
	return ch, store
}

// A whole array moves both ways, fragmented when it is larger than one
// request (200 REALs is 800 bytes).
func TestChannelWholeArrays(t *testing.T) {
	ch, store := newSpecChannel(t, arraySpec())
	arr := make([]ir.Value, 200)
	for i := range arr {
		arr[i] = ir.RealVal(float64(i) + 0.5)
	}
	data := ir.Value{Kind: ir.TypeArray, Arr: arr}
	if err := ch.Write(ctx(t), "Trend", data, "REAL"); err != nil {
		t.Fatal(err)
	}
	if _, v, _ := store.Resolve("Trend[199]"); v != float64(199.5) {
		t.Fatalf("Trend[199] = %v after a fragmented write", v)
	}
	shape := ir.CopyValue(data)
	for i := range shape.Arr {
		shape.Arr[i] = ir.RealVal(0)
	}
	got, err := ch.Read(ctx(t), "Trend", shape, "REAL")
	if err != nil {
		t.Fatal(err)
	}
	if got.Arr[0].F != 0.5 || got.Arr[199].F != 199.5 {
		t.Fatalf("Trend read back %v … %v", got.Arr[0].F, got.Arr[199].F)
	}

	one, _ := pltShape()
	pallets := ir.Value{Kind: ir.TypeArray, Arr: []ir.Value{ir.CopyValue(one), ir.CopyValue(one), ir.CopyValue(one)}}
	pallets.Arr[2].Fld[1] = ir.IntVal(77)
	if err := ch.Write(ctx(t), "Pallets", pallets, ""); err != nil {
		t.Fatal(err)
	}
	if _, v, _ := store.Resolve("Pallets[2].Count"); v != float64(77) {
		t.Fatalf("Pallets[2].Count = %v", v)
	}
}

// A bare integer's declared width is checked both ways, and a value out of
// the peer's range is never truncated onto the wire.
func TestChannelIntegerWidths(t *testing.T) {
	ch, store := newSpecChannel(t, arraySpec())
	store.UpdateValue("Program:MainProgram.Motor", float64(100000))
	if _, err := ch.Read(ctx(t), "Program:MainProgram.Motor", ir.IntVal(0), "INT"); status(err) != ir.CommTypeClash {
		t.Errorf("a DINT into a local INT: err = %v, want STATUS %d", err, ir.CommTypeClash)
	}
	if v, err := ch.Read(ctx(t), "Program:MainProgram.Motor", ir.IntVal(0), "DINT"); err != nil || v.I != 100000 {
		t.Errorf("a DINT into a local DINT: %v, %v", v, err)
	}
	if err := ch.Write(ctx(t), "Small", ir.IntVal(300), ""); status(err) != ir.CommTypeClash {
		t.Errorf("300 into a SINT: err = %v, want STATUS %d", err, ir.CommTypeClash)
	}
	if err := ch.Write(ctx(t), "Word", ir.IntVal(5), "DINT"); status(err) != ir.CommTypeClash {
		t.Errorf("a local DINT into an INT: err = %v", err)
	}
	if err := ch.Write(ctx(t), "Small", ir.IntVal(-100), "SINT"); err != nil {
		t.Errorf("-100 into a SINT: %v", err)
	}
	if _, v, _ := store.Resolve("Small"); v != float64(-100) {
		t.Errorf("Small = %v", v)
	}
}

// The peer refusing a request makes the channel learn the peer again, so a
// tag an online edit retyped is not written with a stale type forever.
func TestChannelForgetsAfterRefusal(t *testing.T) {
	ch, _, _ := newTestChannel(t)
	if err := ch.Write(ctx(t), "SpeedCmd", ir.RealVal(1), "REAL"); err != nil {
		t.Fatal(err)
	}
	if len(ch.peer) != 1 {
		t.Fatalf("peer cache = %v", ch.peer)
	}
	if _, err := ch.Read(ctx(t), "NoSuchTag", ir.RealVal(0), ""); err == nil {
		t.Fatal("want the peer's refusal")
	}
	if len(ch.peer) != 0 {
		t.Fatal("a refusal should drop what the channel learned")
	}
}
