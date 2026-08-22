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

	// 3. a host write reaches the edge's OWN tag store over NCMD.
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
