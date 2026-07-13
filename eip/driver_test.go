package eip

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/eip/logixserver"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

func testSpec() *logixserver.TagSurfaceSpec {
	return &logixserver.TagSurfaceSpec{
		ControllerName: "TestController",
		Templates: []logixserver.TemplateSpec{
			{Name: "Header_Type", Members: []logixserver.MemberSpec{
				{Name: "Displacement", Datatype: "REAL"},
				{Name: "Valid", Datatype: "BOOL"},
			}},
			{Name: "Plt_Type", Members: []logixserver.MemberSpec{
				{Name: "Header", Datatype: "Header_Type"},
				{Name: "Count", Datatype: "DINT"},
			}},
		},
		Symbols: []logixserver.SymbolSpec{
			{Name: "Speed", Datatype: "REAL"},
			{Name: "SpeedCmd", Datatype: "REAL"},
			{Name: "TRS", Datatype: "Plt_Type"},
			{Name: "TRSCmd", Datatype: "Plt_Type"},
			{Name: "Program:MainProgram", Program: true},
			{Name: "Motor", Scope: "Program:MainProgram", Datatype: "DINT"},
		},
		Tags: []logixserver.TagSpec{
			{Path: "Speed", Datatype: "REAL"},
			{Path: "SpeedCmd", Datatype: "REAL"},
			{Path: "TRS.Header.Displacement", Datatype: "REAL"},
			{Path: "TRS.Header.Valid", Datatype: "BOOL"},
			{Path: "TRS.Count", Datatype: "DINT"},
			{Path: "TRSCmd.Header.Displacement", Datatype: "REAL"},
			{Path: "TRSCmd.Header.Valid", Datatype: "BOOL"},
			{Path: "TRSCmd.Count", Datatype: "DINT"},
			{Path: "Program:MainProgram.Motor", Datatype: "DINT"},
		},
	}
}

func testManifest() Manifest {
	return Manifest{
		Types: []TypeDef{
			{Name: "Header_Type", Fields: []FieldDef{
				{Name: "Displacement", Type: "REAL"},
				{Name: "Valid", Type: "BOOL"},
			}},
			{Name: "Plt_Type", Fields: []FieldDef{
				{Name: "Header", Type: "Header_Type"},
				{Name: "Count", Type: "DINT"},
			}},
		},
		Tags: []TagBinding{
			{Name: "Speed", Device: "Speed", Type: "REAL"},
			{Name: "SpeedCmd", Device: "SpeedCmd", Type: "REAL", Writable: true},
			{Name: "TRS", Device: "TRS", Type: "Plt_Type"},
			{Name: "TRSCmd", Device: "TRSCmd", Type: "Plt_Type", Writable: true},
			{Name: "Motor", Device: "Program:MainProgram.Motor", Type: "DINT"},
		},
	}
}

func startEmulator(t *testing.T) (host string, port int, store *logixserver.TagStore) {
	return startEmulatorWith(t, false)
}

func startEmulatorWith(t *testing.T, denyStructRoots bool) (host string, port int, store *logixserver.TagStore) {
	t.Helper()
	schema, tags, name, err := logixserver.CompileSurface(testSpec())
	if err != nil {
		t.Fatalf("compile surface: %v", err)
	}
	store = logixserver.NewTagStore()
	for _, tc := range tags {
		store.Set(tc.Path, tc.LeafType, tc.Default)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port = ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	srv := logixserver.NewServer(store, schema, name, addr, nil)
	srv.DenyStructRoots = denyStructRoots
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return "127.0.0.1", port, store
		}
		if time.Now().After(deadline) {
			t.Fatalf("emulator never came up on %s", addr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDriverPollAndWrite(t *testing.T) {
	host, port, store := startEmulator(t)
	store.UpdateValue("Speed", 42.5)
	store.UpdateValue("TRS.Header.Displacement", 3.5)
	store.UpdateValue("TRS.Header.Valid", true)
	store.UpdateValue("TRS.Count", 12.0)
	store.UpdateValue("Program:MainProgram.Motor", -7.0)

	d, err := New(host, testManifest(), WithPort(port), WithScanRate(30*time.Millisecond))
	if err != nil {
		t.Fatalf("new driver: %v", err)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	waitFor(t, "connect", func() bool { return d.Health().Connected })
	waitFor(t, "first poll", func() bool {
		v, err := d.ReadInputs()
		return err == nil && v["Speed"] != nil && v["TRS"] != nil && v["Motor"] != nil
	})

	vals, err := d.ReadInputs()
	if err != nil {
		t.Fatalf("read inputs: %v", err)
	}
	if vals["Speed"] != 42.5 {
		t.Errorf("Speed = %v, want 42.5", vals["Speed"])
	}
	if vals["Motor"] != int64(-7) {
		t.Errorf("Motor = %v, want -7", vals["Motor"])
	}
	trs, ok := vals["TRS"].(ir.Value)
	if !ok || trs.Kind != ir.TypeStruct {
		t.Fatalf("TRS = %#v, want struct ir.Value", vals["TRS"])
	}
	hdr := trs.Fld[0]
	if hdr.Fld[0].F != 3.5 || hdr.Fld[1].B != true {
		t.Errorf("TRS.Header = %+v, want Displacement 3.5, Valid true", hdr)
	}
	if trs.Fld[1].I != 12 {
		t.Errorf("TRS.Count = %v, want 12", trs.Fld[1].I)
	}

	// Scalar write-on-change.
	if err := d.WriteOutputs(map[string]any{"SpeedCmd": 7.25}); err != nil {
		t.Fatalf("write outputs: %v", err)
	}
	waitFor(t, "SpeedCmd write", func() bool {
		_, v, _ := store.Resolve("SpeedCmd")
		return v == 7.25
	})

	// Struct write: per-leaf writes of changed members.
	cmd := ir.Value{Kind: ir.TypeStruct, Fld: []ir.Value{
		{Kind: ir.TypeStruct, Fld: []ir.Value{ir.RealVal(9.75), ir.BoolVal(true)}},
		ir.IntVal(99),
	}}
	if err := d.WriteOutputs(map[string]any{"TRSCmd": cmd}); err != nil {
		t.Fatalf("write struct: %v", err)
	}
	waitFor(t, "TRSCmd struct write", func() bool {
		_, disp, _ := store.Resolve("TRSCmd.Header.Displacement")
		_, valid, _ := store.Resolve("TRSCmd.Header.Valid")
		_, count, _ := store.Resolve("TRSCmd.Count")
		return disp == 9.75 && valid == true && count == 99.0
	})
}

// TestDriverLeafModeFallback covers the AOI case seen on real controllers:
// whole-struct reads are refused with privilege violation, so the driver
// switches to member-by-member reads and assembles the struct itself.
func TestDriverLeafModeFallback(t *testing.T) {
	host, port, store := startEmulatorWith(t, true)
	store.UpdateValue("Speed", 42.5)
	store.UpdateValue("TRS.Header.Displacement", 3.5)
	store.UpdateValue("TRS.Header.Valid", true)
	store.UpdateValue("TRS.Count", 12.0)

	d, err := New(host, testManifest(), WithPort(port), WithScanRate(30*time.Millisecond))
	if err != nil {
		t.Fatalf("new driver: %v", err)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	waitFor(t, "TRS via leaf mode", func() bool {
		v, err := d.ReadInputs()
		if err != nil {
			return false
		}
		trs, ok := v["TRS"].(ir.Value)
		return ok && trs.Kind == ir.TypeStruct && len(trs.Fld) == 2
	})
	vals, _ := d.ReadInputs()
	trs := vals["TRS"].(ir.Value)
	hdr := trs.Fld[0]
	if hdr.Fld[0].F != 3.5 || hdr.Fld[1].B != true || trs.Fld[1].I != 12 {
		t.Errorf("leaf-assembled TRS = %+v", trs)
	}
	if trs.Struct == nil || trs.Struct.Name != "Plt_Type" {
		t.Errorf("leaf-assembled TRS missing StructDef: %+v", trs.Struct)
	}
	// Scalars still work alongside.
	if vals["Speed"] != 42.5 {
		t.Errorf("Speed = %v, want 42.5", vals["Speed"])
	}
}

// TestRuntimeRoundTrip runs the full stack: emulator ← eip.Driver ← runtime
// scan loop executing an ST program that consumes a UDT input and produces
// outputs, exactly as a generated project would.
func TestRuntimeRoundTrip(t *testing.T) {
	host, port, store := startEmulator(t)
	store.UpdateValue("Speed", 10.0)
	store.UpdateValue("TRS.Header.Displacement", 2.5)
	store.UpdateValue("TRS.Header.Valid", true)
	store.UpdateValue("TRS.Count", 3.0)

	d, err := New(host, testManifest(), WithPort(port), WithScanRate(30*time.Millisecond))
	if err != nil {
		t.Fatalf("new driver: %v", err)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	waitFor(t, "first poll", func() bool {
		v, err := d.ReadInputs()
		return err == nil && v["Speed"] != nil && v["TRS"] != nil
	})

	program := `
TYPE
  Header_Type : STRUCT
    Displacement : REAL;
    Valid : BOOL;
  END_STRUCT;
  Plt_Type : STRUCT
    Header : Header_Type;
    Count : DINT;
  END_STRUCT;
END_TYPE

PROGRAM Main
VAR_EXTERNAL
  Speed : REAL;
  TRS : Plt_Type;
  SpeedCmd : REAL;
END_VAR
IF TRS.Header.Valid THEN
  SpeedCmd := Speed * 2.0 + TRS.Header.Displacement;
ELSE
  SpeedCmd := 0.0;
END_IF;
END_PROGRAM`

	rt, err := runtime.New(runtime.Options{
		Program: program,
		Driver:  d,
		Inputs:  d.InputNames(),
		Outputs: d.OutputNames(),
		Seed:    map[string]any{"SpeedCmd": 0.0},
	})
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}

	rt.Scan()

	// SpeedCmd = 10*2 + 2.5 = 22.5, written back to the device on change.
	waitFor(t, "SpeedCmd on device", func() bool {
		_, v, _ := store.Resolve("SpeedCmd")
		return v == 22.5
	})

	// The HMI JSON view renders the UDT with field names.
	all := rt.Tags().All()
	trs, ok := all["TRS"].(map[string]any)
	if !ok {
		t.Fatalf("All()[TRS] = %#v, want map", all["TRS"])
	}
	hdr, _ := trs["Header"].(map[string]any)
	if hdr == nil || hdr["Displacement"] != 2.5 || hdr["Valid"] != true {
		t.Errorf("TRS.Header JSON = %#v", trs["Header"])
	}
	if c, _ := strconv.ParseInt(fmt.Sprint(trs["Count"]), 10, 64); c != 3 {
		t.Errorf("TRS.Count JSON = %#v, want 3", trs["Count"])
	}

	// Device value changes propagate on the next poll + scan.
	store.UpdateValue("Speed", 20.0)
	waitFor(t, "Speed repoll", func() bool {
		v, _ := d.ReadInputs()
		return v["Speed"] == 20.0
	})
	rt.Scan()
	waitFor(t, "updated SpeedCmd on device", func() bool {
		_, v, _ := store.Resolve("SpeedCmd")
		return v == 42.5
	})
}

// TestScanClasses covers per-class rates and the reserved NoPoll class:
// a fast tag tracks device changes quickly, a slow tag lags, a no-poll tag
// never appears in the snapshot but still accepts writes.
func TestScanClasses(t *testing.T) {
	host, port, store := startEmulator(t)
	store.UpdateValue("Speed", 1.0)
	store.UpdateValue("TRS.Count", 5.0)
	store.UpdateValue("Program:MainProgram.Motor", 9.0)

	d, err := New(host, testManifest(), WithPort(port),
		WithScanRate(40*time.Millisecond), // default class: fast
		WithScanClass("slow", 10*time.Second),
		WithTagClass("slow", "TRS"),
		WithTagClass(NoPoll, "Program:MainProgram.*", "SpeedCmd"),
	)
	if err != nil {
		t.Fatalf("new driver: %v", err)
	}

	classes := d.ScanClasses()
	if got := classes[DefaultClass]; len(got) != 2 { // Speed + TRSCmd
		t.Fatalf("default class = %v", got)
	}
	if got := classes["slow"]; len(got) != 1 || got[0] != "TRS" {
		t.Fatalf("slow class = %v", got)
	}
	for _, n := range d.InputNames() {
		if n == "Motor" || n == "SpeedCmd" {
			t.Fatalf("no-poll binding %q still listed as input", n)
		}
	}

	d.Start(context.Background())
	t.Cleanup(d.Stop)
	waitFor(t, "prime", func() bool {
		v, err := d.ReadInputs()
		return err == nil && v["Speed"] != nil && v["TRS"] != nil
	})

	// Device changes: the fast tag updates, the slow tag holds its primed
	// value (10s interval), the no-poll tag never shows up.
	store.UpdateValue("Speed", 2.0)
	store.UpdateValue("TRS.Count", 6.0)
	waitFor(t, "fast repoll", func() bool {
		v, _ := d.ReadInputs()
		return v["Speed"] == 2.0
	})
	v, _ := d.ReadInputs()
	if trs, ok := v["TRS"].(ir.Value); !ok || trs.Fld[1].I != 5 {
		t.Errorf("slow-class TRS repolled too soon: %+v", v["TRS"])
	}
	if _, ok := v["Motor"]; ok {
		t.Errorf("no-poll Motor appeared in snapshot")
	}

	// No-poll tags still write.
	if err := d.WriteOutputs(map[string]any{"SpeedCmd": 3.5}); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, "no-poll write", func() bool {
		_, val, _ := store.Resolve("SpeedCmd")
		return val == 3.5
	})
}

// TestScanClassErrors covers misconfiguration failing loudly at New.
func TestScanClassErrors(t *testing.T) {
	if _, err := New("h", testManifest(), WithTagClass("fast", "Speed")); err == nil {
		t.Error("undefined class in WithTagClass should error")
	}
	if _, err := New("h", testManifest(), WithScanClass(NoPoll, time.Second)); err == nil {
		t.Error("rate on reserved NoPoll class should error")
	}
	if _, err := New("h", testManifest(), WithScanClass("z", 0), WithTagClass("z", "Speed")); err == nil {
		t.Error("non-positive rate should error")
	}
}
