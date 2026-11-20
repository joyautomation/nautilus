//go:build !js

package project

// A live I/O apply (#706 PR 3a) against real Modbus slaves and OPC UA
// servers, with the scan running: a Modbus device moved to another address
// while the other device stays Good, an OPC UA driver swapped whole, every
// refusal, and the three apply guards.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gopcua/opcua/id"
	uasrv "github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/slave"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

func newSlave(t *testing.T, in uint16) *slave.Server {
	t.Helper()
	srv := slave.NewServer("", nil)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	srv.Unit(1).SetHolding(0, in)
	return srv
}

func slavePort(srv *slave.Server) int {
	_, port, _ := net.SplitHostPort(srv.Addr())
	p, _ := strconv.Atoi(port)
	return p
}

// mbSource is one source of the fixture's Modbus manifest, In on holding 0
// and Cmd (:= In + 1) on holding 10.
func mbSource(id string, port int) string {
	return fmt.Sprintf("  - {id: %s, host: 127.0.0.1, port: %d, unitid: 1, timeout: 500ms, retrymin: 20ms, retrymax: 80ms}\n", id, port)
}

func mbTags(id, prefix string) string {
	return fmt.Sprintf("  - {name: %s_In, source: %s, table: holding, address: 0, format: int16}\n", prefix, id) +
		fmt.Sprintf("  - {name: %s_Cmd, source: %s, table: holding, address: 10, format: int16, writable: true}\n", prefix, id)
}

// ioFiles is a project polling DEV_A (A_In, A_Cmd) on portA and DEV_B on
// portB; uaEndpoint, when set, adds an OPC UA driver delivering GW_Level.
func ioFiles(portA, portB int, uaEndpoint string) fstest.MapFS {
	manifest := `name: io
tasks:
  - program: main.st
    scan: 5ms
tags:
  - { name: A_In, role: input, type: DINT }
  - { name: A_Cmd, role: output, type: DINT }
  - { name: B_In, role: input, type: DINT }
  - { name: B_Cmd, role: output, type: DINT }
`
	if uaEndpoint != "" {
		manifest += "  - { name: GW_Level, role: input, type: LREAL }\n"
		manifest += fmt.Sprintf(`drivers:
  - type: modbus
    manifest: modbus.yaml
    scan-rate: 10ms
  - type: opcua-client
    endpoint: %s
    manifest: opcua.yaml
    scan-rate: 20ms
`, uaEndpoint)
	} else {
		manifest += `driver:
  type: modbus
  manifest: modbus.yaml
  scan-rate: 10ms
`
	}
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(manifest)},
		"main.st": &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR_EXTERNAL A_In : DINT; A_Cmd : DINT; B_In : DINT; B_Cmd : DINT; END_VAR
A_Cmd := A_In + 1;
B_Cmd := B_In + 1;
END_PROGRAM
`)},
		"modbus.yaml": &fstest.MapFile{Data: []byte("sources:\n" + mbSource("DEV_A", portA) + mbSource("DEV_B", portB) +
			"tags:\n" + mbTags("DEV_A", "A") + mbTags("DEV_B", "B"))},
		"opcua.yaml": &fstest.MapFile{Data: []byte(`namespaces: ["http://opcfoundation.org/UA/", "urn:io:test"]
tags:
  - {name: GW_Level, nodeid: "ns=1;s=Level", namespace: "urn:io:test", type: Double}
`)},
	}
}

// withFile is files with one file replaced.
func withFile(files fstest.MapFS, name, content string) fstest.MapFS {
	out := fstest.MapFS{}
	for k, v := range files {
		out[k] = v
	}
	out[name] = &fstest.MapFile{Data: []byte(content)}
	return out
}

func modbusYAML(portA, portB int, extraSources, extraTags string) string {
	return "sources:\n" + mbSource("DEV_A", portA) + mbSource("DEV_B", portB) + extraSources +
		"tags:\n" + mbTags("DEV_A", "A") + mbTags("DEV_B", "B") + extraTags
}

type ioRig struct {
	p   *Project
	rt  *runtime.Runtime
	io  *IOConfig
	ctx context.Context
}

// startIORig loads files, starts its drivers and its scan, and builds the
// IOConfig over them, as naut run will.
func startIORig(t *testing.T, files fstest.MapFS, hold bool) *ioRig {
	t.Helper()
	p, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	p.Runtime.HoldOutputsOnSwap = hold
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if s, ok := p.Runtime.Driver.(interface{ Start(context.Context) }); ok {
		s.Start(ctx)
	}
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		if s, ok := p.Runtime.Driver.(interface{ Stop() }); ok {
			s.Stop()
		}
	})
	io, err := NewIOConfig(p, rt, files, "")
	if err != nil {
		t.Fatal(err)
	}
	saved := answerWait
	answerWait = time.Second // a test applying to a device that is not there waits less
	t.Cleanup(func() { answerWait = saved; io.Close() })
	return &ioRig{p: p, rt: rt, io: io, ctx: ctx}
}

func (r *ioRig) value(name string) string {
	v, _ := r.rt.Tags().ReadPath(name)
	return fmt.Sprint(v)
}

// watchGood records any moment one of names is not Good, until stopped.
func watchGood(r *ioRig, names ...string) (stop func() []string) {
	var mu sync.Mutex
	var bad []string
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-quit:
				return
			case <-time.After(time.Millisecond):
			}
			for _, n := range names {
				if q := r.rt.TagQuality(n); !q.IsGood() {
					mu.Lock()
					bad = append(bad, fmt.Sprintf("%s %v", n, q))
					mu.Unlock()
				}
			}
		}
	}()
	return func() []string {
		close(quit)
		<-done
		mu.Lock()
		defer mu.Unlock()
		return bad
	}
}

func failed(v *IOValidation) []string {
	var out []string
	for _, c := range v.Checks {
		if !c.OK {
			out = append(out, c.Name)
		}
	}
	return out
}

// A Modbus device moved to another address: applied in the running driver,
// the other device Good throughout, the new device sent the current
// command (decision 1), and the drift reported.
func TestIOApplyModbusDeviceAddress(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 6)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	r := startIORig(t, files, false)
	// The first command is the start-up baseline, never sent: drive a change.
	eventually(t, "DEV_A and DEV_B polled", func() bool { return r.value("A_In") == "4" && r.value("B_In") == "7" })
	s1.Unit(1).SetHolding(0, 6)
	eventually(t, "A_Cmd on DEV_A", func() bool { return s1.Unit(1).Holding(10) == 7 })

	cand := withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", ""))
	c := r.io.Stage(cand, "upload", "tester")
	v := r.io.Validate(c)
	if !v.Valid {
		t.Fatalf("validate: %+v", v.Checks)
	}
	if len(v.Changes) != 1 || v.Changes[0].Driver != "modbus" || v.Changes[0].Device != "DEV_A" {
		t.Fatalf("changes = %+v, want DEV_A of driver modbus alone", v.Changes)
	}
	if len(v.Notes) != 1 || !strings.Contains(v.Notes[0], "as of this validation") {
		t.Fatalf("notes = %v, want only that the values can change before the apply", v.Notes)
	}
	if got := fmt.Sprint(v.WillCommand); got != "[{A_Cmd 7}]" || v.Held {
		t.Fatalf("willCommand = %s (held %v), want [{A_Cmd 7}]", got, v.Held)
	}

	stop := watchGood(r, "B_In", "B_Cmd")
	res, err := r.io.Apply(r.ctx, c, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(res.Commanded) != "[{A_Cmd 7}]" {
		t.Fatalf("commanded = %v, want what the first scan handed DEV_A: [{A_Cmd 7}]", res.Commanded)
	}
	if len(res.Notes) == 0 || !strings.Contains(res.Notes[0], "probed before the apply") {
		t.Fatalf("notes = %v, want the probe before the apply said", res.Notes)
	}
	// The new device gets the command willCommand listed, unchanged since
	// (In is 6 on both): only the push after the swap sends it.
	eventually(t, "A_Cmd 7 on the new device", func() bool { return s3.Unit(1).Holding(10) == 7 })
	s3.Unit(1).SetHolding(0, 9)
	eventually(t, "A_In from the new device", func() bool { return r.value("A_In") == "9" && s3.Unit(1).Holding(10) == 10 })
	if bad := stop(); len(bad) > 0 {
		t.Fatalf("DEV_B's tags left Good during the apply: %v", bad[:min(5, len(bad))])
	}
	if s1.Unit(1).Holding(10) != 7 {
		t.Fatalf("the old device was written after the apply: Cmd %d", s1.Unit(1).Holding(10))
	}

	d := r.io.Differs()
	if len(d) != 1 || d[0].Device != "DEV_A" || d[0].AppliedFrom != "upload" || d[0].By != "tester" || d[0].At.IsZero() {
		t.Fatalf("differs = %+v, want DEV_A applied from upload by tester", d)
	}
}

// Under HoldOutputsOnSwap the moved device is not sent the current command
// until it changes, and willCommand says so.
func TestIOApplyModbusDeviceHoldsOutputs(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 6)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	r := startIORig(t, files, true)
	eventually(t, "DEV_A polled", func() bool { return r.value("A_In") == "4" })
	s1.Unit(1).SetHolding(0, 6)
	eventually(t, "A_Cmd on DEV_A", func() bool { return s1.Unit(1).Holding(10) == 7 })

	// The commit also changes a tag; that is noted, not applied.
	cand := withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", ""))
	cand = withFile(cand, "nautilus.yaml", strings.Replace(string(files["nautilus.yaml"].Data),
		"{ name: B_In, role: input, type: DINT }", "{ name: B_In, role: input, type: DINT, desc: inlet }", 1))
	c := r.io.Stage(cand, "abc123", "tester")
	v := r.io.Validate(c)
	if !v.Valid || !v.Held || len(v.WillCommand) != 0 {
		t.Fatalf("validate: valid %v held %v willCommand %v, want valid, held, none", v.Valid, v.Held, v.WillCommand)
	}
	if len(v.Notes) != 1 || !strings.Contains(v.Notes[0], "changes more than its I/O") {
		t.Fatalf("notes = %v, want the non-I/O change noted", v.Notes)
	}
	res, err := r.io.Apply(r.ctx, c, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Commanded) != 0 {
		t.Fatalf("held: commanded = %v, want nothing", res.Commanded)
	}
	eventually(t, "A_In read from the new device", func() bool { return r.rt.TagQuality("A_In").IsGood() && r.value("A_In") == "6" })
	time.Sleep(200 * time.Millisecond)
	if got := s3.Unit(1).Holding(10); got != 0 {
		t.Fatalf("held: the new device got Cmd %d before it changed", got)
	}
	s3.Unit(1).SetHolding(0, 9)
	eventually(t, "the changed command", func() bool { return s3.Unit(1).Holding(10) == 10 })
	if d := r.io.Differs(); len(d) != 1 || d[0].AppliedFrom != "abc123" {
		t.Fatalf("differs = %+v, want DEV_A from abc123", d)
	}
}

// Every refusal leaves what runs untouched, and Apply refuses an invalid
// candidate.
func TestIOValidateRefuses(t *testing.T) {
	s1, s2 := newSlave(t, 4), newSlave(t, 7)
	pa, pb := slavePort(s1), slavePort(s2)
	files := ioFiles(pa, pb, "")
	cases := []struct {
		name, check string
		files       fstest.MapFS
		redundant   bool
	}{
		{"broken manifest", "schema", withFile(files, "modbus.yaml", "sources: [\n"), false},
		{"a new device", "topology", withFile(files, "modbus.yaml", modbusYAML(pa, pb, mbSource("DEV_C", pb), "")), false},
		{"a new tag", "tags", withFile(files, "modbus.yaml", modbusYAML(pa, pb, "",
			"  - {name: A_More, source: DEV_A, table: holding, address: 3, format: int16}\n")), false},
		{"a retyped tag", "tags", withFile(files, "modbus.yaml", strings.Replace(modbusYAML(pa, pb, "", ""),
			"name: A_In, source: DEV_A, table: holding, address: 0, format: int16", "name: A_In, source: DEV_A, table: holding, address: 0, format: float32", 1)), false},
		{"an output bound twice", "outputs", withFile(files, "modbus.yaml", strings.Replace(modbusYAML(pa, pb, "", ""),
			"name: A_In, source: DEV_A, table: holding, address: 0, format: int16", "name: A_In, source: DEV_A, table: holding, address: 10, format: int16, writable: true", 1)), false},
		{"a driver setting on a lone driver", "composite", withFile(files, "nautilus.yaml",
			strings.Replace(string(files["nautilus.yaml"].Data), "scan-rate: 10ms", "scan-rate: 20ms", 1)), false},
		{"two coils at one address", "outputs", withFile(files, "modbus.yaml", modbusYAML(pa, pb, "",
			"  - {name: A_C1, source: DEV_A, table: coil, address: 2, writable: true}\n"+
				"  - {name: A_C2, source: DEV_A, table: coil, address: 2, writable: true}\n")), false},
		{"nothing changed", "changes", files, false},
		{"a redundant project with no retain: block", "kept", withFile(files, "modbus.yaml", modbusYAML(pb, pa, "", "")), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := startIORig(t, files, false)
			if tc.redundant {
				r.p.Redundancy = &RedundancyConfig{}
			}
			c := r.io.Stage(tc.files, "upload", "tester")
			v := r.io.Validate(c)
			if v.Valid || !strings.Contains(" "+strings.Join(failed(v), " ")+" ", " "+tc.check+" ") {
				t.Fatalf("valid %v, failed %v; want %s to fail: %+v", v.Valid, failed(v), tc.check, v.Checks)
			}
			if _, err := r.io.Apply(r.ctx, c, "tester"); err == nil {
				t.Fatal("an invalid candidate was applied")
			}
			if d := r.io.Differs(); len(d) != 0 {
				t.Fatalf("a refused candidate changed what runs: %+v", d)
			}
		})
	}
}

// One apply at a time; a candidate validated against what no longer runs
// is refused; a multi-device apply that fails part way puts back what it
// already applied.
func TestIOApplyGuards(t *testing.T) {
	s1, s2, s3, s4 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 4), newSlave(t, 7)
	files := ioFiles(slavePort(s1), slavePort(s2), "")

	t.Run("busy", func(t *testing.T) {
		r := startIORig(t, files, false)
		c := r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", "")), "upload", "tester")
		r.io.Validate(c)
		r.io.applying.Lock()
		defer r.io.applying.Unlock()
		errc := make(chan error, 1)
		go func() { _, err := r.io.Apply(r.ctx, c, "tester"); errc <- err }()
		select {
		case err := <-errc:
			if !errors.Is(err, ErrApplyBusy) {
				t.Fatalf("err = %v, want ErrApplyBusy", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("an apply waited for the one in progress instead of refusing")
		}
	})

	t.Run("stale", func(t *testing.T) {
		r := startIORig(t, files, false)
		c1 := r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", "")), "upload", "tester")
		c2 := r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(slavePort(s1), slavePort(s4), "", "")), "upload", "tester")
		r.io.Validate(c1)
		r.io.Validate(c2)
		if _, err := r.io.Apply(r.ctx, c1, "tester"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.io.Apply(r.ctx, c2, "tester"); !errors.Is(err, ErrIOStale) {
			t.Fatalf("err = %v, want ErrIOStale", err)
		}
	})

	t.Run("all or nothing", func(t *testing.T) {
		r := startIORig(t, files, false)
		eventually(t, "DEV_A polled", func() bool { return r.value("A_In") == "4" })
		var calls []string
		real := r.io.replaceSource
		r.io.replaceSource = func(mb *modbus.Driver, src modbus.Source, tags []modbus.TagBinding, opts modbus.ReplaceOptions) error {
			calls = append(calls, fmt.Sprintf("%s:%d", src.ID, src.Port))
			if src.ID == "DEV_B" {
				return errors.New("injected")
			}
			return real(mb, src, tags, opts)
		}
		c := r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s4), "", "")), "upload", "tester")
		if v := r.io.Validate(c); !v.Valid || len(v.Changes) != 2 {
			t.Fatalf("validate: %+v", v)
		}
		if _, err := r.io.Apply(r.ctx, c, "tester"); err == nil {
			t.Fatal("the failed apply reported success")
		}
		want := []string{fmt.Sprintf("DEV_A:%d", slavePort(s3)), fmt.Sprintf("DEV_B:%d", slavePort(s4)), fmt.Sprintf("DEV_A:%d", slavePort(s1))}
		if fmt.Sprint(calls) != fmt.Sprint(want) {
			t.Fatalf("replaces = %v, want %v (DEV_A put back)", calls, want)
		}
		s1.Unit(1).SetHolding(0, 11)
		eventually(t, "DEV_A back on its old address", func() bool { return r.value("A_In") == "11" })
		if d := r.io.Differs(); len(d) != 0 {
			t.Fatalf("a failed apply changed what runs: %+v", d)
		}
	})
}

// uaServer serves Level (a Double) under urn:io:test.
func uaServer(t *testing.T, level float64) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	srv := uasrv.New(
		uasrv.EnableSecurity("None", ua.MessageSecurityModeNone),
		uasrv.EnableAuthMode(ua.UserTokenTypeAnonymous),
		uasrv.EndPoint("127.0.0.1", port),
	)
	ns := uasrv.NewNodeNameSpace(srv, "urn:io:test")
	plant := ns.AddNode(uasrv.NewFolderNode(ua.NewStringNodeID(ns.ID(), "Plant"), "Plant"))
	srv.Node(uasrv.ObjectsFolder).AddRef(plant, id.Organizes, true)
	n := ns.AddNewVariableStringNode("Level", level)
	_ = n.SetAttribute(ua.AttributeIDDataType, uasrv.DataValueFromValue(ua.NewNumericNodeID(0, id.Double)))
	plant.AddRef(n, id.HasComponent, true)
	n2 := ns.AddNewVariableStringNode("Level2", level+10) // a second node to rebind GW_Level to
	_ = n2.SetAttribute(ua.AttributeIDDataType, uasrv.DataValueFromValue(ua.NewNumericNodeID(0, id.Double)))
	plant.AddRef(n2, id.HasComponent, true)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// gopcua's server (v0.9.1) races its Close against the goroutine
		// still handling a client's disconnect (channelBroker: Close writes
		// what RegisterConn reads). The drivers are stopped by now; give the
		// server a moment to see their connections go before closing it.
		time.Sleep(200 * time.Millisecond)
		_ = srv.Close()
	})
	return fmt.Sprintf("opc.tcp://127.0.0.1:%d", port)
}

// An OPC UA driver moved to another server is swapped whole between scans;
// the Modbus devices stay Good, and its status row and Sparkplug health
// follow the driver that runs, not the stopped one.
func TestIOApplyOPCUADriverSwap(t *testing.T) {
	s1, s2 := newSlave(t, 4), newSlave(t, 7)
	uaA, uaB := uaServer(t, 1.5), uaServer(t, 2.5)
	files := ioFiles(slavePort(s1), slavePort(s2), uaA)
	r := startIORig(t, files, false)
	eventually(t, "GW_Level from server A", func() bool { return r.value("GW_Level") == "1.5" })
	eventually(t, "DEV_A and DEV_B", func() bool { return r.value("A_In") == "4" && r.value("B_In") == "7" })
	devs, err := sparkplugDevicesLive(r.p.drivers, r.p.currentDriver)
	if err != nil {
		t.Fatal(err)
	}
	var uaHealth func() bool
	for _, d := range devs {
		if d.ID == "opcua-client" {
			uaHealth = d.Health
		}
	}
	if uaHealth == nil {
		t.Fatalf("no Sparkplug device for the OPC UA driver in %v", devs)
	}

	status := r.p.DriverStatus(nil, nil) // built at start, as naut run does

	cand := withFile(files, "nautilus.yaml", strings.Replace(string(files["nautilus.yaml"].Data), uaA, uaB, 1))
	c := r.io.Stage(cand, "upload", "tester")
	v := r.io.Validate(c)
	if !v.Valid || len(v.Changes) != 1 || v.Changes[0].Driver != "opcua-client" || v.Changes[0].Device != "" {
		t.Fatalf("validate: valid %v changes %+v checks %+v", v.Valid, v.Changes, v.Checks)
	}
	stop := watchGood(r, "A_In", "B_In")
	if _, err := r.io.Apply(r.ctx, c, "tester"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "GW_Level from server B", func() bool { return r.value("GW_Level") == "2.5" })
	if bad := stop(); len(bad) > 0 {
		t.Fatalf("the Modbus devices left Good during the OPC UA swap: %v", bad[:min(5, len(bad))])
	}
	var row string
	for _, s := range status() {
		if s.Kind == "opcua-client" {
			row = s.Detail
		}
	}
	if !strings.Contains(row, uaB) {
		t.Fatalf("/api/drivers row shows %q, want the new endpoint %s", row, uaB)
	}
	eventually(t, "Sparkplug health of the new driver", uaHealth)
}

// driverConfigs names drivers exactly as buildDrivers does.
func TestDriverConfigsMirrorBuildDrivers(t *testing.T) {
	for _, files := range []fstest.MapFS{ioFiles(1502, 1503, ""), ioFiles(1502, 1503, "opc.tcp://127.0.0.1:4840")} {
		m, err := ReadManifest(files, "")
		if err != nil {
			t.Fatal(err)
		}
		cfgs, err := driverConfigs(m)
		if err != nil {
			t.Fatal(err)
		}
		_, named, err := buildDrivers(files, *m)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfgs) != len(named) {
			t.Fatalf("%d configs, %d drivers", len(cfgs), len(named))
		}
		for i := range cfgs {
			if cfgs[i].Name != named[i].Name {
				t.Fatalf("config %d is %q, driver %q", i, cfgs[i].Name, named[i].Name)
			}
		}
	}
}

// A devices: row (the device library, #717) is the unit of a per-device
// apply: a new endpoint applies through the driver carrying devices, and
// the struct tag reads the new drive. Changing its points, which changes
// its struct, is refused.
func TestIOApplyDeviceRow(t *testing.T) {
	d1, d2 := newSlave(t, 0), newSlave(t, 0)
	d1.Unit(1).SetHolding(103, 125) // 12.5 A
	d2.Unit(1).SetHolding(103, 333) // 33.3 A
	manifest := func(addr, points string) string {
		return `name: d
tasks: [{ program: main.st, scan: 5ms }]
drivers: [{ type: modbus, scan-rate: 10ms }]
devices:
  - { name: CONV, profile: abb-acs355-fmbt21, endpoint: "` + addr + `"` + points + ` }
tags:
  - { name: Amps, role: state, type: REAL }
`
	}
	program := "PROGRAM Main\nAmps := CONV.Current;\nEND_PROGRAM\n"
	files := devicesProject(t, manifest(d1.Addr(), ""), program)
	r := startIORig(t, files, false)
	amps := func(lo, hi float64) func() bool {
		return func() bool { a := r.rt.Tags().Real("Amps"); return a > lo && a < hi }
	}
	eventually(t, "the first drive's current", amps(12.4, 12.6))

	c := r.io.Stage(devicesProject(t, manifest(d2.Addr(), ""), program), "upload", "tester")
	v := r.io.Validate(c)
	if !v.Valid || len(v.Changes) != 1 || v.Changes[0].Device != "CONV" {
		t.Fatalf("validate: valid %v changes %+v checks %+v", v.Valid, v.Changes, v.Checks)
	}
	if _, err := r.io.Apply(r.ctx, c, "tester"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the second drive's current", amps(33.2, 33.4))

	c = r.io.Stage(devicesProject(t, manifest(d2.Addr(), `, points: ["Current"]`), program), "upload", "tester")
	if v := r.io.Validate(c); v.Valid || !strings.Contains(strings.Join(failed(v), " "), "device-type") {
		t.Fatalf("a points change: valid %v, failed %v; want device-type to fail", v.Valid, failed(v))
	}
}

// A Modbus driver's own settings swap it whole, inside the composite: its
// devices keep reading, and under HoldOutputsOnSwap willCommand lists only
// the rewrite: keep-alives, for a whole driver and for one device.
func TestIOApplyModbusWholeSwap(t *testing.T) {
	s1, s2, s4 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 7)
	ua := uaServer(t, 1.5)
	files := ioFiles(slavePort(s1), slavePort(s2), ua)
	rewrite := func(y string) string {
		return strings.Replace(y, "name: B_Cmd, source: DEV_B, table: holding, address: 10, format: int16, writable: true",
			"name: B_Cmd, source: DEV_B, table: holding, address: 10, format: int16, writable: true, rewrite: 500ms", 1)
	}
	files = withFile(files, "modbus.yaml", rewrite(modbusYAML(slavePort(s1), slavePort(s2), "", "")))
	r := startIORig(t, files, true)
	eventually(t, "DEV_A and DEV_B", func() bool { return r.value("A_In") == "4" && r.value("B_In") == "7" })

	// One device, held: only its keep-alive.
	c := r.io.Stage(withFile(files, "modbus.yaml", rewrite(modbusYAML(slavePort(s1), slavePort(s4), "", ""))), "upload", "tester")
	if v := r.io.Validate(c); !v.Valid || fmt.Sprint(v.WillCommand) != "[{B_Cmd 8}]" {
		t.Fatalf("a held device: valid %v willCommand %v, want [{B_Cmd 8}]", v.Valid, v.WillCommand)
	}

	devs, err := sparkplugDevicesLive(r.p.drivers, r.p.currentDriver)
	if err != nil {
		t.Fatal(err)
	}
	var devA func() bool
	for _, d := range devs {
		if d.ID == "DEV_A" {
			devA = d.Health
		}
	}
	cand := withFile(files, "nautilus.yaml", strings.Replace(string(files["nautilus.yaml"].Data), "scan-rate: 10ms", "scan-rate: 15ms", 1))
	c = r.io.Stage(cand, "upload", "tester")
	v := r.io.Validate(c)
	if !v.Valid || len(v.Changes) != 1 || v.Changes[0].Driver != "modbus" || v.Changes[0].Device != "" {
		t.Fatalf("validate: valid %v changes %+v checks %+v", v.Valid, v.Changes, v.Checks)
	}
	if fmt.Sprint(v.WillCommand) != "[{B_Cmd 8}]" {
		t.Fatalf("a held whole driver: willCommand %v, want [{B_Cmd 8}]", v.WillCommand)
	}
	res, err := r.io.Apply(r.ctx, c, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(res.Commanded) != "[{B_Cmd 8}]" {
		t.Fatalf("a held whole driver: commanded %v, want only the keep-alive [{B_Cmd 8}]", res.Commanded)
	}
	// Held, the new driver's first snapshot is its baseline: a command it
	// takes in that snapshot is not sent, so drive two changes.
	s1.Unit(1).SetHolding(0, 12)
	eventually(t, "DEV_A read by the new driver", func() bool { return r.value("A_In") == "12" })
	s1.Unit(1).SetHolding(0, 20)
	eventually(t, "DEV_A commanded by the new driver", func() bool { return s1.Unit(1).Holding(10) == 21 })
	time.Sleep(100 * time.Millisecond) // the old driver, stopped, reports its sources down
	eventually(t, "DEV_A's Sparkplug health from the new driver", devA)
}

// A whole-driver swap that fails puts back the swaps before it, from what
// runs (an earlier device apply included), not from the boot manifest.
func TestIOApplyRollsBackADriverSwap(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 4)
	uaA, uaB := uaServer(t, 1.5), uaServer(t, 2.5)
	files := ioFiles(slavePort(s1), slavePort(s2), uaA)
	r := startIORig(t, files, false)
	eventually(t, "polled", func() bool { return r.value("A_In") == "4" && r.value("GW_Level") == "1.5" })

	first := withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", ""))
	c := r.io.Stage(first, "upload", "tester")
	r.io.Validate(c)
	if _, err := r.io.Apply(r.ctx, c, "tester"); err != nil {
		t.Fatal(err)
	}

	var calls []string
	real := r.io.swapDriver
	r.io.swapDriver = func(ctx context.Context, name string, fsys fs.FS, cfg DriverConfig) (*swapped, error) {
		calls = append(calls, name)
		if name == "opcua-client" {
			return nil, errors.New("injected")
		}
		return real(ctx, name, fsys, cfg)
	}
	second := withFile(first, "nautilus.yaml", strings.Replace(strings.Replace(string(files["nautilus.yaml"].Data),
		"scan-rate: 10ms", "scan-rate: 15ms", 1), uaA, uaB, 1))
	c = r.io.Stage(second, "upload", "tester")
	if v := r.io.Validate(c); !v.Valid || len(v.Changes) != 2 {
		t.Fatalf("validate: %+v", v)
	}
	if _, err := r.io.Apply(r.ctx, c, "tester"); err == nil {
		t.Fatal("the failed apply reported success")
	}
	if fmt.Sprint(calls) != "[modbus opcua-client modbus]" {
		t.Fatalf("swaps = %v, want modbus, the failing opcua-client, modbus put back", calls)
	}
	s3.Unit(1).SetHolding(0, 21)
	eventually(t, "DEV_A still on the address the first apply gave it", func() bool { return r.value("A_In") == "21" })
	if d := r.io.Differs(); len(d) != 1 || d[0].Device != "DEV_A" {
		t.Fatalf("differs = %+v, want DEV_A alone", d)
	}
}

// Only Modbus and OPC UA client drivers swap whole; another type is
// refused with a restart.
func TestIOValidateRefusesAnUnprovenDriverSwap(t *testing.T) {
	s1, s2 := newSlave(t, 4), newSlave(t, 7)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	base := strings.Replace(string(files["nautilus.yaml"].Data), "driver:\n  type: modbus\n  manifest: modbus.yaml\n  scan-rate: 10ms\n",
		"drivers:\n  - { type: modbus, manifest: modbus.yaml, scan-rate: 10ms }\n  - { type: eip, host: '127.0.0.1:1', manifest: e.yaml }\n", 1)
	files = withFile(withFile(files, "nautilus.yaml", base), "e.yaml", "tags:\n  - { name: E1, device: E1, type: REAL }\n")
	r := startIORig(t, files, false)
	c := r.io.Stage(withFile(files, "nautilus.yaml", strings.Replace(base, "127.0.0.1:1", "127.0.0.1:2", 1)), "upload", "tester")
	v := r.io.Validate(c)
	if v.Valid || strings.Join(failed(v), " ") != "swappable" {
		t.Fatalf("valid %v, failed %v; want swappable alone to fail", v.Valid, failed(v))
	}
}

// An upload replaces the project's file of the same path; the rest read
// through.
func TestOverlayFS(t *testing.T) {
	base := fstest.MapFS{"a.yaml": {Data: []byte("a")}, "dir/b.yaml": {Data: []byte("b")}}
	o := OverlayFS(base, map[string][]byte{"./dir/b.yaml": []byte("B"), "c.yaml": []byte("C")})
	for name, want := range map[string]string{"a.yaml": "a", "dir/b.yaml": "B", "c.yaml": "C"} {
		got, err := fs.ReadFile(o, name)
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	if _, err := fs.ReadFile(o, "d.yaml"); err == nil {
		t.Error("a file in neither reads")
	}
}

// #960 review probes: a tag moved between devices in a whole-driver swap,
// and two devices on one endpoint writing one register, are refused.
func TestIOValidateRefusesReviewProbes(t *testing.T) {
	s1, s2 := newSlave(t, 4), newSlave(t, 7)
	pa, pb := slavePort(s1), slavePort(s2)
	ua := uaServer(t, 1.5)
	files := ioFiles(pa, pb, ua)
	r := startIORig(t, files, false)

	moved := strings.Replace(modbusYAML(pa, pb, "", ""),
		"name: A_Cmd, source: DEV_A,", "name: A_Cmd, source: DEV_B,", 1)
	cand := withFile(withFile(files, "modbus.yaml", moved), "nautilus.yaml",
		strings.Replace(string(files["nautilus.yaml"].Data), "scan-rate: 10ms", "scan-rate: 15ms", 1))
	v := r.io.Validate(r.io.Stage(cand, "upload", "tester"))
	if v.Valid || !strings.Contains(" "+strings.Join(failed(v), " ")+" ", " fields ") {
		t.Fatalf("a tag moved in a whole swap: valid %v failed %v", v.Valid, failed(v))
	}

	v = r.io.Validate(r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(pb, pb, "", "")), "upload", "tester"))
	if v.Valid || !strings.Contains(strings.Join(failed(v), " "), "outputs") {
		t.Fatalf("two devices on one endpoint: valid %v failed %v %+v", v.Valid, failed(v), v.Checks)
	}
}

// Only addressing and binding change live: every other field is refused
// by name.
func TestIOValidateAllowsOnlyAddressing(t *testing.T) {
	s1, s2 := newSlave(t, 4), newSlave(t, 7)
	pa, pb := slavePort(s1), slavePort(s2)
	ua := uaServer(t, 1.5)
	files := ioFiles(pa, pb, ua)
	// A validation of the configuration alone: the moved devices do not
	// exist, so they are checked static.
	files = withFile(files, "nautilus.yaml", strings.Replace(string(files["nautilus.yaml"].Data), "    scan-rate: 10ms\n", "    scan-rate: 10ms\n    validate: static\n", 1))
	src := modbusYAML(pa, pb, "", "")
	srcA := mbSource("DEV_A", pa)
	nau := string(files["nautilus.yaml"].Data)
	ua0 := string(files["opcua.yaml"].Data)
	r := startIORig(t, files, false)
	cases := []struct {
		name, file, content, field string
		valid                      bool
	}{
		{"host, port, unit id", "modbus.yaml", strings.Replace(src, srcA, strings.Replace(srcA, "unitid: 1", "unitid: 3", 1), 1), "", true},
		{"a register address", "modbus.yaml", strings.Replace(src, "name: A_In, source: DEV_A, table: holding, address: 0", "name: A_In, source: DEV_A, table: holding, address: 4", 1), "", true},
		{"an OPC UA node id", "opcua.yaml", strings.Replace(ua0, "s=Level", "s=Level2", 1), "", true},
		{"word order", "modbus.yaml", strings.Replace(src, srcA, strings.Replace(srcA, "unitid: 1,", "unitid: 1, wordorder: little,", 1), 1), "wordorder", false},
		{"a timeout", "modbus.yaml", strings.Replace(src, srcA, strings.Replace(srcA, "timeout: 500ms", "timeout: 900ms", 1), 1), "timeout", false},
		{"a keep-alive added", "modbus.yaml", strings.Replace(src, "address: 10, format: int16, writable: true}", "address: 10, format: int16, writable: true, rewrite: 1s}", 1), "rewrite", false},
		{"a scale", "modbus.yaml", strings.Replace(src, "name: A_In, source: DEV_A, table: holding, address: 0, format: int16}", "name: A_In, source: DEV_A, table: holding, address: 0, format: int16, scale: 2}", 1), "scale", false},
		{"an OPC UA security policy", "nautilus.yaml", strings.Replace(nau, "    endpoint: "+ua, "    endpoint: "+ua+"\n    security: Basic256Sha256", 1), "security", false},
		{"an OPC UA pki dir", "nautilus.yaml", strings.Replace(nau, "    endpoint: "+ua, "    endpoint: "+ua+"\n    pki-dir: pki", 1), "pki-dir", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := r.io.Validate(r.io.Stage(withFile(files, tc.file, tc.content), "upload", "tester"))
			if tc.valid {
				if !v.Valid {
					t.Fatalf("refused: %+v", v.Checks)
				}
				return
			}
			var detail string
			for _, c := range v.Checks {
				if c.Name == "fields" {
					detail = c.Detail
				}
			}
			if v.Valid || !strings.Contains(detail, tc.field) {
				t.Fatalf("valid %v, fields check %q; want %s refused by name", v.Valid, detail, tc.field)
			}
		})
	}
}

// A failed apply whose rollback fails too says so: ErrRollbackFailed,
// naming what is left, those units unknown in Differs, and a Fault.
func TestIOApplyRollbackFailure(t *testing.T) {
	s1, s2, s3, s4 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 4), newSlave(t, 7)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	r := startIORig(t, files, false)
	real := r.io.replaceSource
	calls := 0
	r.io.replaceSource = func(mb *modbus.Driver, src modbus.Source, tags []modbus.TagBinding, opts modbus.ReplaceOptions) error {
		calls++
		if calls == 1 {
			return real(mb, src, tags, opts) // DEV_A applied
		}
		return errors.New("injected") // DEV_B fails, and so does putting DEV_A back
	}
	c := r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s4), "", "")), "upload", "tester")
	r.io.Validate(c)
	_, err := r.io.Apply(r.ctx, c, "tester")
	if !errors.Is(err, ErrRollbackFailed) || !strings.Contains(err.Error(), "device DEV_A") || strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err = %v, want ErrRollbackFailed naming device DEV_A", err)
	}
	d := r.io.Differs()
	if len(d) != 1 || d[0].Device != "DEV_A" || d[0].RunningHash != unknownHash {
		t.Fatalf("differs = %+v, want DEV_A unknown", d)
	}
	if !strings.Contains(r.io.Fault(), "DEV_A") {
		t.Fatalf("fault = %q", r.io.Fault())
	}

	// An apply that re-establishes DEV_A clears the fault.
	r.io.replaceSource = real
	c = r.io.Stage(files, "upload", "tester")
	if v := r.io.Validate(c); !v.Valid || len(v.Changes) != 1 || v.Changes[0].Device != "DEV_A" {
		t.Fatalf("re-establishing DEV_A: %+v", v)
	}
	if _, err := r.io.Apply(r.ctx, c, "tester"); err != nil {
		t.Fatal(err)
	}
	if f, d := r.io.Fault(), r.io.Differs(); f != "" || len(d) != 0 {
		t.Fatalf("after DEV_A is re-established: fault %q, differs %+v; want neither", f, d)
	}
}

// With the write gate closed, a changed device commands nothing, and
// willCommand says why.
func TestIOWillCommandWithTheGateClosed(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 4)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	r := startIORig(t, files, false)
	modbusOf(r.p.Runtime.Driver).SetWriteGate(func() bool { return false })
	v := r.io.Validate(r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(slavePort(s3), slavePort(s2), "", "")), "upload", "tester"))
	if !v.Valid || len(v.WillCommand) != 0 || !strings.Contains(strings.Join(v.Notes, " "), "gate is closed") {
		t.Fatalf("valid %v willCommand %v notes %v; want nothing, the gate named", v.Valid, v.WillCommand, v.Notes)
	}
}

// A whole Modbus swap with the write gate closed commands nothing, and the
// apply says so: Commanded is what the new driver's sources queued, not
// what the scan handed over (#960 re-check).
func TestIOApplyWholeModbusSwapWithTheGateClosed(t *testing.T) {
	s1, s2 := newSlave(t, 4), newSlave(t, 7)
	ua := uaServer(t, 1.5)
	files := ioFiles(slavePort(s1), slavePort(s2), ua)
	r := startIORig(t, files, false)
	eventually(t, "polled", func() bool { return r.value("A_In") == "4" && r.value("B_In") == "7" })
	nio.SetPlainGate(r.p.Runtime.Driver, func() bool { return false })

	c := r.io.Stage(withFile(files, "nautilus.yaml", strings.Replace(string(files["nautilus.yaml"].Data), "scan-rate: 10ms", "scan-rate: 15ms", 1)), "upload", "tester")
	v := r.io.Validate(c)
	if !v.Valid || len(v.WillCommand) != 0 {
		t.Fatalf("validate: valid %v willCommand %v", v.Valid, v.WillCommand)
	}
	res, err := r.io.Apply(r.ctx, c, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Commanded) != 0 || !strings.Contains(strings.Join(res.Notes, " "), "gate was closed") {
		t.Fatalf("commanded %v notes %v; want nothing, the closed gate said", res.Commanded, res.Notes)
	}
}

// maxSwapStall bounds the longest gap between scans across an apply.
const maxSwapStall = 250 * time.Millisecond

// watchScanGaps records when the scan count moves; the returned func, called
// once, stops it and gives the longest gap between two scans that overlaps
// [from, to].
func watchScanGaps(r *ioRig) func(from, to time.Time) time.Duration {
	var mu sync.Mutex
	var seen []time.Time
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		last := r.rt.Stats().Count
		for {
			select {
			case <-quit:
				return
			case <-time.After(500 * time.Microsecond):
			}
			if n := r.rt.Stats().Count; n != last {
				last = n
				mu.Lock()
				seen = append(seen, time.Now())
				mu.Unlock()
			}
		}
	}()
	return func(from, to time.Time) time.Duration {
		close(quit)
		<-done
		mu.Lock()
		defer mu.Unlock()
		var worst time.Duration
		for i := 1; i < len(seen); i++ {
			if seen[i].After(from) && seen[i-1].Before(to) {
				worst = max(worst, seen[i].Sub(seen[i-1]))
			}
		}
		return worst
	}
}

// #706's done-when, over the HTTP API: under a 1 ms scan, a Modbus device's
// address and an OPC UA node id change in one apply, with no scan overrun
// and bad quality only on what changed; an invalid change is refused and
// what runs is untouched.
func TestIODoneWhenOverTheAPI(t *testing.T) {
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 4)
	ua := uaServer(t, 1.5)
	files := ioFiles(slavePort(s1), slavePort(s2), ua)
	files = withFile(files, "nautilus.yaml", strings.Replace(string(files["nautilus.yaml"].Data), "scan: 5ms", "scan: 1ms", 1))
	r := startIORig(t, files, false)
	h := server.New(r.rt, server.Options{OnlineEdits: true, IO: r.io.Hook(nil, files)}).Handler()
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			json.NewEncoder(&buf).Encode(body)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "http://localhost"+path, &buf))
		out := map[string]any{}
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	stage := func(changed map[string]string) string {
		t.Helper()
		code, st := call("POST", "/api/io/staged", map[string]any{"files": changed})
		if code != http.StatusCreated {
			t.Fatalf("stage: %d %v", code, st)
		}
		return st["id"].(string)
	}
	eventually(t, "polled", func() bool {
		return r.value("A_In") == "4" && r.value("B_In") == "7" && r.value("GW_Level") == "1.5"
	})
	overruns := r.rt.Stats().Lateness.Overruns
	stop := watchGood(r, "B_In", "B_Cmd")
	gaps := watchScanGaps(r)

	// Invalid: a new tag. Refused; nothing changes.
	id := stage(map[string]string{"modbus.yaml": modbusYAML(slavePort(s3), slavePort(s2), "",
		"  - {name: A_More, source: DEV_A, table: holding, address: 3, format: int16}\n")})
	if _, st := call("POST", "/api/io/staged/"+id+"/validate", nil); st["state"] != "invalid" {
		t.Fatalf("an invalid change validated: %v", st)
	}
	if code, _ := call("POST", "/api/io/staged/"+id+"/apply", nil); code != http.StatusConflict {
		t.Fatalf("an invalid change: apply %d, want 409", code)
	}
	if _, st := call("GET", "/api/state", nil); fmt.Sprint(st["config"]) != "map[source:manifest]" {
		t.Fatalf("after a refused change, config = %v", st["config"])
	}

	// Valid: DEV_A to another address, GW_Level to another node.
	id = stage(map[string]string{
		"modbus.yaml": modbusYAML(slavePort(s3), slavePort(s2), "", ""),
		"opcua.yaml":  strings.Replace(string(files["opcua.yaml"].Data), "s=Level", "s=Level2", 1),
	})
	if _, st := call("POST", "/api/io/staged/"+id+"/validate", nil); st["state"] != "valid" {
		t.Fatalf("validate: %v", st)
	}
	applyFrom := time.Now()
	if code, st := call("POST", "/api/io/staged/"+id+"/apply", nil); code != http.StatusOK || st["state"] != "applied" {
		t.Fatalf("apply: %d %v", code, st)
	}
	applyTo := time.Now()
	eventually(t, "GW_Level from its new node", func() bool { return r.value("GW_Level") == "11.5" })
	s3.Unit(1).SetHolding(0, 9)
	eventually(t, "A_In from its new address", func() bool { return r.value("A_In") == "9" })
	if bad := stop(); len(bad) > 0 {
		t.Fatalf("DEV_B, which did not change, left Good: %v", bad[:min(5, len(bad))])
	}
	// The scan never stalls on the swap: no gap between scans that
	// overlaps the apply is longer than a loaded -race runner's worst
	// hiccup. A swap that blocks the scan (holds it across a dial or the
	// old connection's wind-down) stalls it for hundreds of ms to seconds.
	if g := gaps(applyFrom, applyTo); g > maxSwapStall {
		t.Fatalf("the scan stalled %v across the apply (limit %v): the swap blocked it", g, maxSwapStall)
	}
	// Zero overruns at 1 ms holds on a quiet bench, not on a shared CI
	// runner under -race (#969 flaked once on one): bench-only.
	if os.Getenv("NAUTILUS_STRICT_TIMING") == "1" {
		if n := r.rt.Stats().Lateness.Overruns - overruns; n != 0 {
			t.Fatalf("%d scan overruns during the apply", n)
		}
	}
	_, st := call("GET", "/api/state", nil)
	c, _ := st["config"].(map[string]any)
	if c["source"] != "applied" || !strings.Contains(fmt.Sprint(c["differs"]), "DEV_A") || !strings.Contains(fmt.Sprint(c["differs"]), "opcua-client") {
		t.Fatalf("config = %v, want DEV_A and opcua-client differing", st["config"])
	}
}

// The server hook maps the engine's outcomes onto the statuses the routes
// answer: no snapshot 409, busy and stale conflicts, a failed rollback its
// own.
func TestIOHookMapsOutcomes(t *testing.T) {
	s1, s2, s3, s4 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 4), newSlave(t, 7)
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	r := startIORig(t, files, false)
	h := r.io.Hook(func(sha string) (fs.FS, bool) { return nil, false }, files)
	if _, err := h.Stage("abc", nil, "tester"); !errors.Is(err, server.ErrIONoSnapshot) {
		t.Fatalf("no snapshot: %v", err)
	}
	stage := func(y string) any {
		c, err := h.Stage("", map[string][]byte{"modbus.yaml": []byte(y)}, "tester")
		if err != nil {
			t.Fatal(err)
		}
		if ok, drivers, _ := h.Validate(c); !ok || fmt.Sprint(drivers) != "[modbus]" {
			t.Fatalf("validate: %v %v", ok, drivers)
		}
		return c
	}
	c1, c2 := stage(modbusYAML(slavePort(s3), slavePort(s2), "", "")), stage(modbusYAML(slavePort(s1), slavePort(s4), "", ""))
	if _, err := h.Apply(r.ctx, c1, server.IOApply{By: "tester"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Apply(r.ctx, c2, server.IOApply{By: "tester"}); !errors.Is(err, server.ErrIOConflict) || !errors.Is(err, ErrIOStale) {
		t.Fatalf("stale: %v, want a conflict", err)
	}
	c3 := stage(modbusYAML(slavePort(s1), slavePort(s4), "", ""))
	r.io.replaceSource = func(mb *modbus.Driver, src modbus.Source, tags []modbus.TagBinding, opts modbus.ReplaceOptions) error {
		if src.ID == "DEV_A" && src.Port == slavePort(s1) {
			return nil // DEV_A applied
		}
		return errors.New("injected") // DEV_B fails, and so does putting DEV_A back
	}
	if _, err := h.Apply(r.ctx, c3, server.IOApply{By: "tester"}); !errors.Is(err, server.ErrIORollbackFailed) {
		t.Fatalf("a failed rollback: %v", err)
	}
	if len(h.Differs()) == 0 || h.Fault() == "" {
		t.Fatalf("differs %v fault %q after a failed rollback", h.Differs(), h.Fault())
	}
}

// OPC UA devices: rows (#717, #971) are opcua-client drivers whose manifest
// is derived from the vendored profile, not a file. A live apply sees that
// derived manifest (#971 review probes):
//   - a profile node-id edit alone is a change, applied;
//   - a profile edit outside the allow-list (a description) is refused by
//     the per-binding check, with an endpoint change or without one.
func TestIOApplyOPCUADeviceRows(t *testing.T) {
	ice3, err := os.ReadFile("../../devprofile/profiles/pepperl-fuchs-ice3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	const manifest = `name: d
tasks: [{ program: main.st, scan: 20ms }]
devices:
  - { name: PF1, profile: pepperl-fuchs-ice3, endpoint: "127.0.0.1:1", validate: static }
  - { name: PF2, profile: pepperl-fuchs-ice3, endpoint: "127.0.0.1:2", validate: static }
`
	project := func(manifest, profile string) fstest.MapFS {
		fsys := devicesProject(t, manifest, "PROGRAM Main\nEND_PROGRAM\n")
		fsys["profiles/pepperl-fuchs-ice3.yaml"] = &fstest.MapFile{Data: []byte(profile)}
		return fsys
	}
	nodeEdit := strings.Replace(string(ice3), `node-id: "ns=1;s=IOLM/Serial"`, `node-id: "ns=1;s=IOLM/SerialNumber"`, 1)
	descEdit := strings.Replace(string(ice3), `desc: "IO-Link master serial number"`, `desc: "the serial number"`, 1)
	if nodeEdit == string(ice3) || descEdit == string(ice3) {
		t.Fatal("the profile edits did not apply; the fixture moved")
	}
	files := project(manifest, string(ice3))
	r := startIORig(t, files, false)

	// A profile node-id edit alone: both devices change, and apply.
	c := r.io.Stage(project(manifest, nodeEdit), "upload", "tester")
	v := r.io.Validate(c)
	if !v.Valid || fmt.Sprint(v.Changes) == "[]" || len(v.Changes) != 2 {
		t.Fatalf("a profile node-id edit: valid %v changes %+v checks %+v", v.Valid, v.Changes, v.Checks)
	}
	if _, err := r.io.Apply(r.ctx, c, "tester"); err != nil {
		t.Fatal(err)
	}
	if d := r.io.Differs(); len(d) != 2 {
		t.Fatalf("differs = %+v, want PF1 and PF2", d)
	}

	// A description edit, with an endpoint change and without: refused by
	// the per-binding check, naming the field.
	moved := strings.Replace(manifest, `endpoint: "127.0.0.1:1"`, `endpoint: "127.0.0.1:3"`, 1)
	for _, m := range []string{moved, manifest} {
		v := r.io.Validate(r.io.Stage(project(m, descEdit), "upload", "tester"))
		var detail string
		for _, ck := range v.Checks {
			if ck.Name == "fields" {
				detail = ck.Detail
			}
		}
		if v.Valid || !strings.Contains(detail, "desc") || !strings.Contains(detail, "PF1.Serial") {
			t.Fatalf("a profile desc edit: valid %v, fields %q; want PF1.Serial's desc refused", v.Valid, detail)
		}
	}
}
