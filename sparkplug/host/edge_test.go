// edge_test.go is the acceptance test D1 asks for
// (docs/design/sparkplug-host.md §6 item 2, §7): a real sparkplug.Node —
// nautilus's own EDGE implementation, built over a real *runtime.Runtime —
// publishing into the in-process mochi broker mqtt_test.go already stands
// up, consumed by this package's HOST driver. Edge and host are dogfooded
// against each other in one test, so a drift between the two sides of the
// protocol shows up here rather than in production.
//
// Reuses mqtt_test.go's harness verbatim: startBroker, brokerURL,
// quietLogger, testConfig, waitConnected, waitForValue. The one thing it
// adds is startHostDriver, a copy of startDriver that takes the manifest as
// a parameter — startDriver is hardcoded to state_test.go's testManifest(),
// which does not have the two-level-nested Motor/Drv shape this test needs
// to exercise the templates.go TemplateRef fix.

package host

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug"
)

// ── the edge fixture ─────────────────────────────────────────────────────

// edgeTypesST is a library (no PROGRAM, so it composes ahead of the main
// program) declaring the two-level-nested UDT the dogfood test needs: Motor
// has a struct member (Drive : Drv), which is exactly the shape that made
// templates.go emit an unresolvable Template definition before the
// TemplateRef fix (see state_test.go's
// TestStateNestedTemplateFallsBackToManifest for the pre-fix shape).
const edgeTypesST = `
TYPE
	Drv : STRUCT
		Torque : REAL;
		Fault : BOOL;
	END_STRUCT;
	Motor : STRUCT
		Speed : REAL;
		Run : BOOL;
		Drive : Drv;
	END_STRUCT;
END_TYPE
`

// edgeProgramST declares every tag the edge publishes as VAR_EXTERNAL, the
// way a real project would; the body is empty; the test drives the tag
// store directly instead of through ST logic.
const edgeProgramST = `
PROGRAM Main
VAR_EXTERNAL
	Well_Level : REAL;
	PumpRun : BOOL;
	SpeedSP : REAL;
	Pump1 : Motor;
END_VAR
END_PROGRAM
`

// buildEdgeRuntime compiles edgeProgramST + edgeTypesST and seeds its tags:
// Well_Level (REAL, 42.5), PumpRun (BOOL, false), SpeedSP (REAL, the
// setpoint the host writes), and Pump1, a two-level nested Motor/Drv struct
// with every field non-zero so the assertions can tell a real value from a
// zeroed one.
func buildEdgeRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program:   edgeProgramST,
		Libraries: []string{edgeTypesST},
		Driver:    nio.NewMemory(),
		Scan:      50 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.State("Well_Level", 42.5),
			runtime.State("PumpRun", false),
			runtime.State("SpeedSP", 0.0),
			runtime.Typed("Pump1", runtime.RoleState, "Motor"),
		},
	})
	if err != nil {
		t.Fatalf("build edge runtime: %v", err)
	}
	seedPump1(t, rt)
	return rt
}

// seedPump1 overwrites the zero-valued Pump1 TagDef seeded with concrete,
// distinguishable values, reaching two levels deep (Pump1.Drive.Torque) —
// the field the TemplateRef fix is what makes resolvable on the host side.
func seedPump1(t *testing.T, rt *runtime.Runtime) {
	t.Helper()
	motorT, ok := rt.Types()["Motor"]
	if !ok || motorT.Struct == nil {
		t.Fatal("TYPE Motor did not compile into the runtime's type table")
	}
	pump1 := ir.Zero(motorT)
	setField(&pump1, "Speed", ir.RealVal(1450))
	setField(&pump1, "Run", ir.BoolVal(true))
	drvIdx := pump1.Struct.FieldIndex["Drive"]
	drv := pump1.Fld[drvIdx]
	setField(&drv, "Torque", ir.RealVal(88.5))
	setField(&drv, "Fault", ir.BoolVal(false))
	pump1.Fld[drvIdx] = drv
	rt.Tags().Set("Pump1", pump1)
}

// setField writes one named struct field in place, by the StructDef's own
// FieldIndex — never assuming declaration order.
func setField(v *ir.Value, name string, val ir.Value) {
	idx, ok := v.Struct.FieldIndex[name]
	if !ok {
		panic("setField: struct " + v.Struct.Name + " has no field " + name)
	}
	v.Fld[idx] = val
}

// edgeDogfoodManifest is the host-side manifest for the dogfood edge: one
// node W6, no device, a Motor/Drv template pair matching edgeTypesST field
// for field, and the four bindings the test drives.
func edgeDogfoodManifest() Manifest {
	return Manifest{
		Group: "G",
		Types: []TypeDef{
			{Name: "Drv", Fields: []FieldDef{
				{Name: "Torque", Type: "Double"},
				{Name: "Fault", Type: "Boolean"},
			}},
			{Name: "Motor", Fields: []FieldDef{
				{Name: "Speed", Type: "Double"},
				{Name: "Run", Type: "Boolean"},
				{Name: "Drive", Type: "Drv"},
			}},
		},
		Nodes: []Node{{EdgeNode: "W6", Prefix: "W6"}},
		Tags: []Binding{
			{Name: "W6_Well_Level", Node: "W6", Metric: "Well_Level", Type: "Double"},
			{Name: "W6_PumpRun", Node: "W6", Metric: "PumpRun", Type: "Boolean"},
			{Name: "W6_SpeedSP", Node: "W6", Metric: "SpeedSP", Type: "Double",
				Writable: true, Init: 0.0},
			{Name: "W6_Pump1", Node: "W6", Metric: "Pump1", Type: "Motor"},
			// Member bindings on the SAME metric the struct binding reads:
			// one flat (Pump1.Speed), one two levels deep
			// (Pump1.Drive.Torque). Writing either must reach the edge's UDT
			// tag without disturbing the members it is not addressing.
			{Name: "W6_Pump1_Speed", Node: "W6", Metric: "Pump1", Member: "Speed",
				Type: "Motor", Writable: true},
			{Name: "W6_Pump1_Drive_Torque", Node: "W6", Metric: "Pump1", Member: "Drive.Torque",
				Type: "Motor", Writable: true},
		},
	}
}

// startHostDriver is startDriver (mqtt_test.go) with the manifest as a
// parameter instead of hardcoded to state_test.go's testManifest() — this
// package's harness convenience, not a new one.
func startHostDriver(t *testing.T, m Manifest, cfg Config, opts ...Option) *Driver {
	t.Helper()
	opts = append(opts, WithLogger(quietLogger()))
	d, err := New(m, cfg, opts...)
	if err != nil {
		t.Fatalf("host New: %v", err)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	return d
}

// fieldVal walks a struct ir.Value by field name (nested paths allowed:
// fieldVal(t, pump1, "Drive", "Torque")), by the value's own embedded
// StructDef rather than assuming field order.
func fieldVal(t *testing.T, v ir.Value, path ...string) ir.Value {
	t.Helper()
	for _, name := range path {
		if v.Kind != ir.TypeStruct || v.Struct == nil {
			t.Fatalf("value is not a struct looking for field %q: %#v", name, v)
		}
		idx, ok := v.Struct.FieldIndex[name]
		if !ok {
			t.Fatalf("struct %s has no field %q", v.Struct.Name, name)
		}
		v = v.Fld[idx]
	}
	return v
}

// settleAfterBirth gives an edge's connect-triggered birth() goroutine a
// moment to finish its own trailing bookkeeping before the caller may Stop()
// it. This is a test-only workaround for a genuine, pre-existing race in
// sparkplug/node.go (see the BUG note in TestEdgeToHostDogfood) — not a
// general synchronization primitive, and not a fix for that race.
func settleAfterBirth() { time.Sleep(500 * time.Millisecond) }

// mustStruct fetches a snapshot value and asserts it is an ir.Value struct.
func mustStruct(t *testing.T, d *Driver, tag string) ir.Value {
	t.Helper()
	vals, err := d.ReadInputs()
	if err != nil {
		t.Fatalf("ReadInputs: %v", err)
	}
	v, ok := vals[tag].(ir.Value)
	if !ok || v.Kind != ir.TypeStruct {
		t.Fatalf("%s = %#v, want an ir.Value struct", tag, vals[tag])
	}
	return v
}

// ── the dogfood test ─────────────────────────────────────────────────────

// TestEdgeToHostDogfood runs a real sparkplug.Node edge against a real host
// Driver over an in-process broker end to end: birth, a live RBE-driven
// change (NDATA), a host-originated write reaching the edge's tag store
// (NCMD), death (values survive, __Online clears), and a restart (bdSeq
// increments, __Online recovers).
func TestEdgeToHostDogfood(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	// Host first, edge second — deliberately. With RebirthOnStart (below)
	// the host fires an NCMD Rebirth the instant it connects; started this
	// way that NCMD lands on a topic nobody has subscribed to yet and is
	// dropped (QoS 0, not retained), so it never reaches a *live* edge.
	// Starting the edge first would race it against the host's connect-time
	// NCMD, and a real sparkplug.Node has a genuine, pre-existing bug on
	// that path: command.go:22's `go n.Rebirth()` can still be running
	// birth() when Stop() is called moments later in this test, and
	// birth.go:97's `n.log.Info(..., "bdSeq", n.bdSeq, ...)` reads n.bdSeq
	// without n.mu after n.mu.Unlock() on birth.go:87, racing
	// node.go:226's locked write in Stop(). Confirmed with
	// `go test ./sparkplug/host/ -run Edge -race -count=5` before this
	// ordering fix — see the BUG note below cmd. This ordering sidesteps
	// the trigger rather than fixing sparkplug/node.go, which is out of
	// this test's scope; see the report for the owner.
	host := startHostDriver(t, edgeDogfoodManifest(), testConfig(addr, "h1"))
	waitConnected(t, host)

	rt := buildEdgeRuntime(t)
	rtCtx, rtCancel := context.WithCancel(context.Background())
	t.Cleanup(rtCancel)
	go rt.Run(rtCtx)

	bdFile := filepath.Join(t.TempDir(), "w6.bdseq")
	edge, err := sparkplug.New(rt, sparkplug.Config{
		BrokerURL:       brokerURL(addr),
		GroupID:         "G",
		EdgeNode:        "W6",
		BdSeqFile:       bdFile,
		PublishInterval: 50 * time.Millisecond,
		Log:             quietLogger(),
	})
	if err != nil {
		t.Fatalf("sparkplug.New: %v", err)
	}
	if err := edge.Start(context.Background()); err != nil {
		t.Fatalf("edge Start: %v", err)
	}
	// edge.Stop is idempotent (Node.Stop no-ops once cancel is nil) and, since
	// Node.Start rebuilds its own child context each call, this single
	// registration correctly tears down whichever session (first or
	// restarted) is live when the test ends.
	t.Cleanup(edge.Stop)

	// 1. NBIRTH reaches the host: scalars, __Online, and the two-level
	// nested Motor/Drive/Drv struct — the TemplateRef fix under test.
	vals := waitForValue(t, host, "W6_Well_Level", func(v any) bool { return v == 42.5 })
	if vals["W6__Online"] != true {
		t.Fatalf("W6__Online = %v, want true", vals["W6__Online"])
	}
	if vals["W6_PumpRun"] != false {
		t.Errorf("W6_PumpRun = %v, want false", vals["W6_PumpRun"])
	}

	pump1 := mustStruct(t, host, "W6_Pump1")
	if got := fieldVal(t, pump1, "Speed").F; got != 1450 {
		t.Errorf("Pump1.Speed = %v, want 1450", got)
	}
	if got := fieldVal(t, pump1, "Run").B; got != true {
		t.Errorf("Pump1.Run = %v, want true", got)
	}
	if got := fieldVal(t, pump1, "Drive", "Torque").F; got != 88.5 {
		t.Errorf("Pump1.Drive.Torque = %v, want 88.5 (nested TemplateRef assembly)", got)
	}
	if got := fieldVal(t, pump1, "Drive", "Fault").B; got != false {
		t.Errorf("Pump1.Drive.Fault = %v, want false", got)
	}

	// 2. a live edge change reaches the host over NDATA (RBE on-change).
	rt.Tags().SetReal("Well_Level", 50)
	waitForValue(t, host, "W6_Well_Level", func(v any) bool { return v == 50.0 })

	// 3. a host write reaches the edge's OWN tag store over NCMD. The
	// runtime's first output snapshot is the baseline — see
	// TestEdgeToHostStartKeepsSetpoints — so it goes in first, and only the
	// operator's move after it is a command.
	baselineScan(t, host)
	if err := host.WriteOutputs(map[string]any{"W6_SpeedSP": 7.5}); err != nil {
		t.Fatalf("WriteOutputs: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && rt.Tags().Real("SpeedSP") != 7.5 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := rt.Tags().Real("SpeedSP"); got != 7.5 {
		t.Fatalf("edge tag SpeedSP = %v, want 7.5 — the NCMD write never reached the edge's tag store", got)
	}
	// The edge's own scan loop should notice the change and echo it back.
	waitForValue(t, host, "W6_SpeedSP", func(v any) bool { return v == 7.5 })

	// 3b. a host write to two MEMBERS of the edge's UDT reaches its tag store
	// as a PARTIAL template, changing exactly those members. This is the
	// whole feature dogfooded: the host has no business writing Pump1.Run or
	// Pump1.Drive.Fault, and after the command the edge must still hold the
	// values its own logic put there.
	if err := host.WriteOutputs(map[string]any{
		"W6_Pump1_Speed":        61.5,
		"W6_Pump1_Drive_Torque": 12.25,
	}); err != nil {
		t.Fatalf("WriteOutputs(members): %v", err)
	}
	edgePump1 := func(t *testing.T) ir.Value {
		t.Helper()
		v, err := rt.Tags().ReadGlobal("Pump1")
		if err != nil {
			t.Fatalf("edge ReadGlobal(Pump1): %v", err)
		}
		return v
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v, err := rt.Tags().ReadGlobal("Pump1")
		if err == nil && v.Kind == ir.TypeStruct &&
			fieldVal(t, v, "Speed").F == 61.5 && fieldVal(t, v, "Drive", "Torque").F == 12.25 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ep := edgePump1(t)
	if got := fieldVal(t, ep, "Speed").F; got != 61.5 {
		t.Fatalf("edge Pump1.Speed = %v, want 61.5 — the member NCMD never landed", got)
	}
	if got := fieldVal(t, ep, "Drive", "Torque").F; got != 12.25 {
		t.Fatalf("edge Pump1.Drive.Torque = %v, want 12.25 — the nested member NCMD never landed", got)
	}
	// The siblings the host never addressed are untouched: this is what a
	// whole-struct write would have destroyed.
	if got := fieldVal(t, ep, "Run").B; got != true {
		t.Errorf("edge Pump1.Run = %v, want true — a partial template must not clobber siblings", got)
	}
	if got := fieldVal(t, ep, "Drive", "Fault").B; got != false {
		t.Errorf("edge Pump1.Drive.Fault = %v, want false — nested sibling clobbered", got)
	}
	// And the edge's own RBE publishes the merged struct back, so the host's
	// struct tag agrees with what the site now holds.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v := mustStruct(t, host, "W6_Pump1"); fieldVal(t, v, "Speed").F == 61.5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	hp := mustStruct(t, host, "W6_Pump1")
	if got := fieldVal(t, hp, "Speed").F; got != 61.5 {
		t.Errorf("host W6_Pump1.Speed = %v, want 61.5 echoed back over NDATA", got)
	}
	if got := fieldVal(t, hp, "Run").B; got != true {
		t.Errorf("host W6_Pump1.Run = %v, want true", got)
	}

	// 4. stop the edge: NDEATH clears __Online but every value survives.
	settleAfterBirth() // see settleAfterBirth's doc — genuine race, not this test's bug
	edge.Stop()
	waitForValue(t, host, "W6__Online", func(v any) bool { return v == false })
	after, err := host.ReadInputs()
	if err != nil {
		t.Fatalf("ReadInputs: %v", err)
	}
	if after["W6_Well_Level"] != 50.0 {
		t.Errorf("W6_Well_Level after death = %v, want 50 (values must survive death)", after["W6_Well_Level"])
	}

	// 5. restart: __Online recovers and bdSeq has incremented — proof the
	// BdSeqFile round-tripped across the Stop/Start boundary.
	if err := edge.Start(context.Background()); err != nil {
		t.Fatalf("edge restart: %v", err)
	}
	waitForValue(t, host, "W6__Online", func(v any) bool { return v == true })
	settleAfterBirth() // t.Cleanup(edge.Stop) fires right after this function returns
	st := host.Status()
	if len(st.Nodes) != 1 {
		t.Fatalf("Status.Nodes = %+v, want 1 row", st.Nodes)
	}
	if st.Nodes[0].BdSeq != 1 {
		t.Errorf("Status.Nodes[0].BdSeq = %d, want 1 (incremented across the restart)", st.Nodes[0].BdSeq)
	}
}

// TestEdgeToHostStatus checks Status()'s node/device shape against a real
// edge: one row per edge node with a positive metric count and, absent any
// sparkplug.WithDevice registration, no device sub-rows. The WithDevice
// subtest is the "driver-as-device" shape the design brief describes in §1
// (server.DriverDevice sub-rows) — the edge package has no Config.Device
// field (only the WithDevice(Device{...}) option), so that is what this
// drives.
func TestEdgeToHostStatus(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	// Host before edge — see the ordering note in TestEdgeToHostDogfood.
	host := startHostDriver(t, edgeDogfoodManifest(), testConfig(addr, "h-status2"))
	waitConnected(t, host)

	rt := buildEdgeRuntime(t)
	rtCtx, rtCancel := context.WithCancel(context.Background())
	t.Cleanup(rtCancel)
	go rt.Run(rtCtx)

	edge, err := sparkplug.New(rt, sparkplug.Config{
		BrokerURL:       brokerURL(addr),
		GroupID:         "G",
		EdgeNode:        "W6",
		BdSeqFile:       filepath.Join(t.TempDir(), "bdseq"),
		PublishInterval: 50 * time.Millisecond,
		Log:             quietLogger(),
	})
	if err != nil {
		t.Fatalf("sparkplug.New: %v", err)
	}
	if err := edge.Start(context.Background()); err != nil {
		t.Fatalf("edge Start: %v", err)
	}
	t.Cleanup(edge.Stop)

	waitForValue(t, host, "W6__Online", func(v any) bool { return v == true })
	settleAfterBirth() // see settleAfterBirth's doc — genuine race, not this test's bug

	st := host.Status()
	if len(st.Nodes) != 1 {
		t.Fatalf("Status.Nodes = %+v, want 1 row", st.Nodes)
	}
	if st.Nodes[0].Metrics <= 0 {
		t.Errorf("Status.Nodes[0].Metrics = %d, want > 0", st.Nodes[0].Metrics)
	}
	if len(st.Nodes[0].Devices) != 0 {
		t.Errorf("Status.Nodes[0].Devices = %+v, want empty — no device was registered", st.Nodes[0].Devices)
	}

	// A device registered on the edge (sparkplug.WithDevice) becomes a
	// device sub-row on the host side, with its own __Online companion.
	t.Run("WithDevice", func(t *testing.T) {
		srv2, addr2 := startBroker(t, "")
		t.Cleanup(func() { _ = srv2.Close() })

		// Host before edge again — see the ordering note in
		// TestEdgeToHostDogfood: it avoids racing RebirthOnStart's
		// connect-time NCMD against the real edge's Stop() (t.Cleanup
		// below), which is a genuine bug in sparkplug/node.go, not
		// something to paper over here.
		m2 := Manifest{
			Group: "G",
			Nodes: []Node{{EdgeNode: "W6", Prefix: "W6", Devices: []Device{{Device: "plc1"}}}},
			Tags: []Binding{
				{Name: "W6_plc1_Aux", Node: "W6", Device: "plc1", Metric: "Aux", Type: "Boolean"},
			},
		}
		host2 := startHostDriver(t, m2, testConfig(addr2, "h-device"))
		waitConnected(t, host2)

		rt2, err := runtime.New(runtime.Options{
			Program: "PROGRAM Main\nVAR_EXTERNAL\n\tAux : BOOL;\nEND_VAR\nEND_PROGRAM",
			Driver:  nio.NewMemory(),
			Scan:    50 * time.Millisecond,
			Seed:    nio.Values{"Aux": true},
		})
		if err != nil {
			t.Fatalf("build device edge runtime: %v", err)
		}
		rtCtx2, rtCancel2 := context.WithCancel(context.Background())
		t.Cleanup(rtCancel2)
		go rt2.Run(rtCtx2)

		edge2, err := sparkplug.New(rt2, sparkplug.Config{
			BrokerURL:       brokerURL(addr2),
			GroupID:         "G",
			EdgeNode:        "W6",
			BdSeqFile:       filepath.Join(t.TempDir(), "bdseq2"),
			PublishInterval: 50 * time.Millisecond,
			Log:             quietLogger(),
		}, sparkplug.WithDevice(sparkplug.Device{ID: "plc1", Tags: []string{"Aux"}}))
		if err != nil {
			t.Fatalf("sparkplug.New: %v", err)
		}
		if err := edge2.Start(context.Background()); err != nil {
			t.Fatalf("edge Start: %v", err)
		}
		t.Cleanup(edge2.Stop)

		waitForValue(t, host2, "W6_plc1__Online", func(v any) bool { return v == true })
		vals := waitForValue(t, host2, "W6_plc1_Aux", func(v any) bool { return v == true })
		settleAfterBirth() // see settleAfterBirth's doc — genuine race, not this test's bug
		if vals["W6__Online"] != true {
			t.Errorf("W6__Online = %v, want true", vals["W6__Online"])
		}

		st2 := host2.Status()
		if len(st2.Nodes) != 1 || len(st2.Nodes[0].Devices) != 1 || st2.Nodes[0].Devices[0].ID != "plc1" {
			t.Fatalf("Status.Nodes = %+v, want one node with device plc1", st2.Nodes)
		}
	})
}

// ── outputs are commands: the setpoint-zeroing regression ────────────────

// startDogfoodEdge brings up the edge fixture against addr with a
// commissioned SpeedSP — the stand-in for the demo's RAWMIN/HHSP — and waits
// until the host has consumed its birth. Returns the edge's runtime.
func startDogfoodEdge(t *testing.T, host *Driver, addr string, speedSP float64) *runtime.Runtime {
	t.Helper()
	rt := buildEdgeRuntime(t)
	rt.Tags().SetReal("SpeedSP", speedSP)
	rtCtx, rtCancel := context.WithCancel(context.Background())
	t.Cleanup(rtCancel)
	go rt.Run(rtCtx)

	edge, err := sparkplug.New(rt, sparkplug.Config{
		BrokerURL:       brokerURL(addr),
		GroupID:         "G",
		EdgeNode:        "W6",
		BdSeqFile:       filepath.Join(t.TempDir(), "w6.bdseq"),
		PublishInterval: 50 * time.Millisecond,
		Log:             quietLogger(),
	})
	if err != nil {
		t.Fatalf("sparkplug.New: %v", err)
	}
	if err := edge.Start(context.Background()); err != nil {
		t.Fatalf("edge Start: %v", err)
	}
	t.Cleanup(func() { settleAfterBirth(); edge.Stop() })

	waitForValue(t, host, "W6_SpeedSP", func(v any) bool { return v == speedSP })
	waitForValue(t, host, "W6__Online", func(v any) bool { return v == true })
	return rt
}

// TestEdgeToHostStartKeepsSetpoints is the regression test for the defect the
// PomonaSCADA demo diagnosed (~/Development/pomona/wrd/host/README.md, "Open
// defect: the host zeroes an edge's setpoints when it starts"):
//
//	RTU9 alone, host down:   RAWMIN=3277 RAWMAX=16383 HHSP=1000 ...
//	after a host connects:   RAWMIN=0    RAWMAX=0     HHSP=0    ...
//
// On connect the host published every writable output once. None of them had
// ever been written, so each held the zero of its type, and for a member
// binding that is a PARTIAL TEMPLATE of zeros — which the edge dutifully
// merges member by member, wiping every commissioned setpoint in the
// `--writable` globs while the members outside them survive.
//
// Nothing here is mocked: a real sparkplug.Node edge with real values, a real
// host driver, and the real merge. The host is handed the output snapshot the
// runtime would hand it — every output, every scan, all at their init: — and
// the edge's tags must not move.
func TestEdgeToHostStartKeepsSetpoints(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-keepsp", "spBv1.0/G/NCMD/+", "spBv1.0/G/DCMD/+/+")

	// Host first, edge second — see the ordering note in TestEdgeToHostDogfood.
	host := startHostDriver(t, edgeDogfoodManifest(), testConfig(addr, "h-keepsp"))
	waitConnected(t, host)
	rt := startDogfoodEdge(t, host, addr, 3277)

	// The host now scans. Nobody has written a thing: W6_SpeedSP,
	// W6_Pump1_Speed and W6_Pump1_Drive_Torque are all sitting at their init:.
	scanFor(t, host, 6, nil)

	if n := obs.count(isCommandFor("spBv1.0/G/NCMD/W6")); n != 0 {
		t.Errorf("the host published %d commands on connect; an output nobody has "+
			"written is not a command", n)
	}

	// The edge's own tags are the real assertion: this is what the operator
	// would have found zeroed.
	if got := rt.Tags().Real("SpeedSP"); got != 3277 {
		t.Errorf("edge SpeedSP = %v, want 3277 — the host zeroed a commissioned setpoint", got)
	}
	ep, err := rt.Tags().ReadGlobal("Pump1")
	if err != nil {
		t.Fatalf("edge ReadGlobal(Pump1): %v", err)
	}
	if got := fieldVal(t, ep, "Speed").F; got != 1450 {
		t.Errorf("edge Pump1.Speed = %v, want 1450 — a partial template of zeros was merged", got)
	}
	if got := fieldVal(t, ep, "Drive", "Torque").F; got != 88.5 {
		t.Errorf("edge Pump1.Drive.Torque = %v, want 88.5 — a nested member was zeroed", got)
	}
	// And the members outside the writable set were never at risk — which is
	// exactly the fingerprint that made the demo's diagnosis exact.
	if got := fieldVal(t, ep, "Run").B; got != true {
		t.Errorf("edge Pump1.Run = %v, want true", got)
	}

	// The host's own view agrees: it never told itself a lie either.
	hp := mustStruct(t, host, "W6_Pump1")
	if got := fieldVal(t, hp, "Speed").F; got != 1450 {
		t.Errorf("host W6_Pump1.Speed = %v, want 1450", got)
	}
}

// TestEdgeToHostMemberBaselineAdoptsLiveValue — once the site's template has
// arrived, the host knows what each writable member actually holds. An
// operator dialling a member to the value it already has is asking for
// nothing; dialling it anywhere else is one command. Driven against the real
// edge, so the birth that teaches the host is a real birth.
func TestEdgeToHostMemberBaselineAdoptsLiveValue(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-adoptlive", "spBv1.0/G/NCMD/+")
	host := startHostDriver(t, edgeDogfoodManifest(), testConfig(addr, "h-adoptlive"))
	waitConnected(t, host)
	rt := startDogfoodEdge(t, host, addr, 3277)

	// buildEdgeRuntime seeds Pump1.Speed = 1450 and Pump1.Drive.Torque = 88.5,
	// and the birth has already carried both to the host.
	cmd := isCommandFor("spBv1.0/G/NCMD/W6")
	scanFor(t, host, 3, map[string]any{
		"W6_Pump1_Speed":        1450.0,
		"W6_Pump1_Drive_Torque": 88.5,
	})
	if n := obs.count(cmd); n != 0 {
		t.Fatalf("writing members the values the site already reported produced %d NCMDs, want 0", n)
	}

	// A different value is a real command, and it lands.
	scanFor(t, host, 3, map[string]any{
		"W6_Pump1_Speed":        61.5,
		"W6_Pump1_Drive_Torque": 88.5,
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v, err := rt.Tags().ReadGlobal("Pump1")
		if err == nil && v.Kind == ir.TypeStruct && fieldVal(t, v, "Speed").F == 61.5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ep, err := rt.Tags().ReadGlobal("Pump1")
	if err != nil {
		t.Fatalf("edge ReadGlobal(Pump1): %v", err)
	}
	if got := fieldVal(t, ep, "Speed").F; got != 61.5 {
		t.Fatalf("edge Pump1.Speed = %v, want 61.5 — the real command never landed", got)
	}
	if got := fieldVal(t, ep, "Drive", "Torque").F; got != 88.5 {
		t.Errorf("edge Pump1.Drive.Torque = %v, want 88.5 — an unchanged member was re-commanded", got)
	}
	if n := obs.count(cmd); n != 1 {
		t.Errorf("moving one member produced %d NCMDs, want exactly 1", n)
	}
}

// TestEdgeToHostQueuedWriteReachesAWokenSite is the dark-site delivery,
// end to end against the real edge: the operator moves a member while the
// site is DOWN — no broker session, no birth, nothing to write to — the site
// then starts, and the command lands exactly once without disturbing a single
// member the operator did not touch.
//
// It is the case the old drop-and-re-raise could not survive the runtime's
// change-push contract: the write was dropped while the site was dark and
// re-raised by the NEXT scan's snapshot, but under change-push there is no
// next call until the tag moves again, so the operator's command evaporated
// and the site came back holding its old value.
func TestEdgeToHostQueuedWriteReachesAWokenSite(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-darkwrite", "spBv1.0/G/NCMD/+")
	host := startHostDriver(t, edgeDogfoodManifest(), testConfig(addr, "h-darkwrite"))
	waitConnected(t, host)

	// No edge yet: W6 is dark, and the host has never heard from it.
	baselineScan(t, host)
	if err := host.WriteOutputs(outputSnapshot(host, map[string]any{
		"W6_Pump1_Speed": 61.5,
	})); err != nil {
		t.Fatalf("WriteOutputs: %v", err)
	}
	st := waitQueued(t, host, 1)
	if st.WriteDrops != 0 {
		t.Fatalf("WriteDrops = %d, want 0 — the site is dark, not unaddressable", st.WriteDrops)
	}
	cmd := isCommandFor("spBv1.0/G/NCMD/W6")
	if n := obs.count(cmd); n != 0 {
		t.Fatalf("a write to a site that has never birthed published %d commands, want 0", n)
	}

	// The site starts. buildEdgeRuntime seeds Pump1 = {Speed: 1450, Run: true,
	// Drive: {Torque: 88.5, Fault: false}} and SpeedSP = 3277 — every one of
	// them a commissioned value the operator did NOT ask to change.
	rt := startDogfoodEdge(t, host, addr, 3277)

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		v, err := rt.Tags().ReadGlobal("Pump1")
		if err == nil && v.Kind == ir.TypeStruct && fieldVal(t, v, "Speed").F == 61.5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	ep, err := rt.Tags().ReadGlobal("Pump1")
	if err != nil {
		t.Fatalf("edge ReadGlobal(Pump1): %v", err)
	}
	if got := fieldVal(t, ep, "Speed").F; got != 61.5 {
		t.Fatalf("edge Pump1.Speed = %v, want 61.5 — the command queued while the "+
			"site was dark never arrived", got)
	}
	// Every member the operator did not touch is intact — the delivery is a
	// partial template, exactly like a live write.
	if got := fieldVal(t, ep, "Run").B; got != true {
		t.Errorf("edge Pump1.Run = %v, want true", got)
	}
	if got := fieldVal(t, ep, "Drive", "Torque").F; got != 88.5 {
		t.Errorf("edge Pump1.Drive.Torque = %v, want 88.5", got)
	}
	if got := fieldVal(t, ep, "Drive", "Fault").B; got != false {
		t.Errorf("edge Pump1.Drive.Fault = %v, want false", got)
	}
	if got := rt.Tags().Real("SpeedSP"); got != 3277 {
		t.Errorf("edge SpeedSP = %v, want 3277 — a sibling output was commanded too", got)
	}

	// Exactly once, and the queue is empty.
	time.Sleep(300 * time.Millisecond)
	if n := obs.count(cmd); n != 1 {
		t.Fatalf("the queued command was published %d times, want exactly 1", n)
	}
	if st := waitQueued(t, host, 0); st.WriteQueued != 1 {
		t.Errorf("WriteQueued = %d, want 1 (cumulative)", st.WriteQueued)
	}
}
