//go:build !js

package project

// Live validation probes (#706 PR 4) against real Modbus slaves and OPC UA
// servers: what a probe proves, what it refuses and why, static mode, and
// that a probe never stalls the scan.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gopcua/opcua/id"
	uasrv "github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"

	"github.com/joyautomation/nautilus/devbind"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
)

func probeOf(t *testing.T, v *IOValidation) IOProbe {
	t.Helper()
	if len(v.Probes) != 1 {
		t.Fatalf("probes = %+v, want one", v.Probes)
	}
	return v.Probes[0]
}

func liveOf(v *IOValidation) IOCheck {
	for _, c := range v.Checks {
		if c.Name == "live" {
			return c
		}
	}
	return IOCheck{}
}

// deadPort is a port nothing listens on.
func deadPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return p
}

func TestIOProbeModbus(t *testing.T) {
	shortProbes(t)
	s1, s2, s3 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 4)
	strict := newSlave(t, 4)
	strict.Unit(1).Define("holding", 0, 20, true) // a real device: nothing past 19
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	r := startIORig(t, files, false)
	validate := func(y string) *IOValidation {
		t.Helper()
		return r.io.Validate(r.io.Stage(withFile(files, "modbus.yaml", y), "upload", "tester"))
	}

	t.Run("a device that answers, and nothing is written to it", func(t *testing.T) {
		s3.Unit(1).SetHolding(10, 99) // A_Cmd's register: the running program commands 5
		v := validate(modbusYAML(slavePort(s3), slavePort(s2), "", ""))
		p := probeOf(t, v)
		if !v.Valid || !p.OK || p.Mode != devbind.ValidateLive || p.Device != "DEV_A" || liveOf(v).Skipped {
			t.Fatalf("valid %v probe %+v live %+v", v.Valid, p, liveOf(v))
		}
		if got := s3.Unit(1).Holding(10); got != 99 {
			t.Fatalf("the probe wrote %d to the device", got)
		}
	})
	t.Run("a connection accepted and closed at once", func(t *testing.T) {
		one := newSlave(t, 4)
		one.SetMaxConnections(1) // the project's driver holds it below
		r1 := startIORig(t, ioFiles(slavePort(one), slavePort(s2), ""), false)
		eventually(t, "the project's driver on the one connection", func() bool { return r1.value("A_In") == "4" })
		files1 := ioFiles(slavePort(one), slavePort(s2), "")
		respelled := strings.Replace(modbusYAML(slavePort(one), slavePort(s2), "", ""), "{id: DEV_A, host: 127.0.0.1", "{id: DEV_A, host: localhost", 1)
		start := time.Now()
		v := r1.io.Validate(r1.io.Stage(withFile(files1, "modbus.yaml", respelled), "upload", "tester"))
		p := probeOf(t, v)
		if v.Valid || !strings.Contains(p.Detail, "set validate: static") || time.Since(start) > time.Second {
			t.Fatalf("valid %v probe %+v after %v: want refused at once, with the advice", v.Valid, p, time.Since(start))
		}
		eventually(t, "the project's driver still reads", func() bool { return r1.rt.TagQuality("A_In").IsGood() })
	})
	t.Run("the driver's scan classes and tag classes", func(t *testing.T) {
		// A binding in a class the driver defines (scan-classes), and one put
		// in it by the driver (tag-classes): the probe's driver must know both.
		inSlow := func(y string) string {
			return strings.Replace(y, "name: A_In, source: DEV_A, table: holding, address: 0, format: int16}",
				"name: A_In, source: DEV_A, table: holding, address: 0, format: int16, scanclass: slow}", 1)
		}
		classed := withFile(files, "nautilus.yaml", strings.Replace(string(files["nautilus.yaml"].Data),
			"  scan-rate: 10ms\n", "  scan-rate: 10ms\n  scan-classes: { slow: 10s }\n  tag-classes: { slow: [\"A_Cmd\"] }\n", 1))
		classed = withFile(classed, "modbus.yaml", inSlow(string(classed["modbus.yaml"].Data)))
		rc := startIORig(t, classed, false)
		v := rc.io.Validate(rc.io.Stage(withFile(classed, "modbus.yaml", inSlow(modbusYAML(slavePort(s3), slavePort(s2), "", ""))), "upload", "tester"))
		if p := probeOf(t, v); !p.OK {
			t.Fatalf("a valid candidate with a slow class: valid %v probe %+v checks %+v", v.Valid, p, v.Checks)
		}
	})
	t.Run("nothing at the address", func(t *testing.T) {
		start := time.Now()
		v := validate(modbusYAML(deadPort(t), slavePort(s2), "", ""))
		p := probeOf(t, v)
		if v.Valid || p.OK || !strings.Contains(p.Detail, "set validate: static") || !strings.Contains(liveOf(v).Detail, "DEV_A") {
			t.Fatalf("valid %v probe %+v", v.Valid, p)
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("a refused connection took %v to report, want well under the %v cap", d, probeCap)
		}
	})
	t.Run("a wrong unit id", func(t *testing.T) {
		y := strings.Replace(modbusYAML(slavePort(strict), slavePort(s2), "", ""), mbSource("DEV_A", slavePort(strict)),
			strings.Replace(mbSource("DEV_A", slavePort(strict)), "unitid: 1", "unitid: 9", 1), 1)
		v := validate(y)
		p := probeOf(t, v)
		if v.Valid || p.OK || !strings.Contains(p.Detail, "answered, but") || !strings.Contains(strings.Join(p.Bad, " "), "A_In") {
			t.Fatalf("valid %v probe %+v", v.Valid, p)
		}
	})
	t.Run("a register the device does not have", func(t *testing.T) {
		y := strings.Replace(modbusYAML(slavePort(strict), slavePort(s2), "", ""),
			"name: A_In, source: DEV_A, table: holding, address: 0", "name: A_In, source: DEV_A, table: holding, address: 50", 1)
		v := validate(y)
		p := probeOf(t, v)
		if v.Valid || p.OK || !strings.Contains(strings.Join(p.Bad, " "), "A_In") || strings.Contains(strings.Join(p.Bad, " "), "A_Cmd") {
			t.Fatalf("valid %v probe %+v: want A_In alone refused", v.Valid, p)
		}
	})
	t.Run("static on the driver", func(t *testing.T) {
		static := withFile(files, "nautilus.yaml", strings.Replace(string(files["nautilus.yaml"].Data), "  scan-rate: 10ms\n", "  scan-rate: 10ms\n  validate: static\n", 1))
		static = withFile(static, "modbus.yaml", modbusYAML(deadPort(t), slavePort(s2), "", ""))
		v := r.io.Validate(r.io.Stage(static, "upload", "tester"))
		p := probeOf(t, v)
		if !v.Valid || p.Mode != devbind.ValidateStatic || p.Why != "set on driver modbus" || !liveOf(v).Skipped {
			t.Fatalf("valid %v probe %+v live %+v", v.Valid, p, liveOf(v))
		}
	})
	t.Run("an invalid candidate is not probed", func(t *testing.T) {
		v := validate(modbusYAML(deadPort(t), slavePort(s2), "", "  - {name: A_More, source: DEV_A, table: holding, address: 3, format: int16}\n"))
		if v.Valid || len(v.Probes) != 0 || !liveOf(v).Skipped {
			t.Fatalf("valid %v probes %+v live %+v", v.Valid, v.Probes, liveOf(v))
		}
	})
}

func TestIOProbeOPCUA(t *testing.T) {
	shortProbes(t)
	s1, s2 := newSlave(t, 4), newSlave(t, 7)
	ua := uaServer(t, 1.5)
	files := ioFiles(slavePort(s1), slavePort(s2), ua)
	r := startIORig(t, files, false)
	validate := func(node string) *IOValidation {
		t.Helper()
		return r.io.Validate(r.io.Stage(withFile(files, "opcua.yaml", strings.Replace(string(files["opcua.yaml"].Data), "s=Level", "s="+node, 1)), "upload", "tester"))
	}
	if v := validate("Level2"); !v.Valid || !probeOf(t, v).OK {
		t.Fatalf("a node the server has: valid %v probes %+v", v.Valid, v.Probes)
	}
	v := validate("Nope")
	if p := probeOf(t, v); v.Valid || p.OK || !strings.Contains(strings.Join(p.Bad, " "), "GW_Level") {
		t.Fatalf("a node the server lacks: valid %v probe %+v", v.Valid, p)
	}
	// A whole Modbus swap for its scan rate alone changes no device: none
	// is probed (each would be a connection of its own for nothing).
	v = r.io.Validate(r.io.Stage(withFile(files, "nautilus.yaml", strings.Replace(string(files["nautilus.yaml"].Data),
		"    scan-rate: 10ms\n", "    scan-rate: 15ms\n", 1)), "upload", "tester"))
	if !v.Valid || len(v.Changes) != 1 || len(v.Probes) != 0 {
		t.Fatalf("a scan rate alone: valid %v changes %+v probes %+v", v.Valid, v.Changes, v.Probes)
	}
	// How a change is checked is not a change: validate: alone applies nothing.
	v = r.io.Validate(r.io.Stage(withFile(files, "nautilus.yaml", strings.Replace(string(files["nautilus.yaml"].Data),
		"    scan-rate: 20ms\n", "    scan-rate: 20ms\n    validate: static\n", 1)), "upload", "tester"))
	if v.Valid || len(v.Changes) != 0 {
		t.Fatalf("validate: alone: valid %v changes %+v, want nothing to apply", v.Valid, v.Changes)
	}
}

// A probe that waits out a device that never answers runs off the scan:
// a 1 ms scan does not stall.
func TestIOProbeDoesNotStallTheScan(t *testing.T) {
	s1, s2 := newSlave(t, 4), newSlave(t, 7)
	mute, err := net.Listen("tcp", "127.0.0.1:0") // accepts, never answers
	if err != nil {
		t.Fatal(err)
	}
	defer mute.Close()
	go func() {
		for {
			c, err := mute.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	files := ioFiles(slavePort(s1), slavePort(s2), "")
	files = withFile(files, "nautilus.yaml", strings.Replace(string(files["nautilus.yaml"].Data), "scan: 5ms", "scan: 1ms", 1))
	r := startIORig(t, files, false)
	eventually(t, "polled", func() bool { return r.value("A_In") == "4" })
	saved := probeCap
	probeCap = time.Second
	defer func() { probeCap = saved }()
	gaps := watchScanGaps(r)
	from := time.Now()
	v := r.io.Validate(r.io.Stage(withFile(files, "modbus.yaml", modbusYAML(mute.Addr().(*net.TCPAddr).Port, slavePort(s2), "", "")), "upload", "tester"))
	to := time.Now()
	if v.Valid || to.Sub(from) < 900*time.Millisecond {
		t.Fatalf("valid %v after %v: want the mute device refused at the cap", v.Valid, to.Sub(from))
	}
	if g := gaps(from, to); g > maxSwapStall {
		t.Fatalf("the scan stalled %v during a probe", g)
	}
}

// shortProbes caps a probe at 1.5 s for the test: a device that answers
// but fails a tag is only reported at the cap.
func shortProbes(t *testing.T) {
	saved := probeCap
	probeCap = 1500 * time.Millisecond
	t.Cleanup(func() { probeCap = saved })
}

// Probes go one address at a time (devices behind one gateway share it)
// and at most probeParallel addresses at once.
func TestIOProbeOneAtATimePerAddress(t *testing.T) {
	var mu sync.Mutex
	perAddr, maxPerAddr := map[string]int{}, map[string]int{}
	total, maxTotal := 0, 0
	target := func(addr string) probeTarget {
		return probeTarget{
			IOProbe: IOProbe{Mode: devbind.ValidateLive},
			addr:    addr,
			build:   func() (nio.Driver, error) { return nio.NewMemory(), nil },
			inputs:  func(nio.Driver) []string { return []string{"x"} },
			quality: func(nio.Driver) map[string]nio.Quality {
				mu.Lock()
				perAddr[addr]++
				total++
				maxPerAddr[addr] = max(maxPerAddr[addr], perAddr[addr])
				maxTotal = max(maxTotal, total)
				mu.Unlock()
				time.Sleep(50 * time.Millisecond)
				mu.Lock()
				perAddr[addr]--
				total--
				mu.Unlock()
				return nil
			},
			health: func(nio.Driver) (bool, string, uint64) { return true, "", 0 },
		}
	}
	var targets []probeTarget
	for i := 0; i < 3; i++ {
		targets = append(targets, target("gateway:502"))
	}
	for _, a := range []string{"b:502", "c:502", "d:502", "e:502", "f:502", "g:502"} {
		targets = append(targets, target(a))
	}
	for _, p := range probe(targets) {
		if !p.OK {
			t.Fatalf("probe %+v", p)
		}
	}
	if maxPerAddr["gateway:502"] != 1 || maxTotal > probeParallel {
		t.Fatalf("at once: %d on the gateway (want 1), %d in all (want at most %d)", maxPerAddr["gateway:502"], maxTotal, probeParallel)
	}
}

// An OPC UA devices: row probed live: its members read under their own
// names, so a node the server lacks fails the probe, naming the member.
func TestIOProbeOPCUADeviceRow(t *testing.T) {
	ice3, err := os.ReadFile("../../devprofile/profiles/pepperl-fuchs-ice3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	serve := func() string {
		t.Helper()
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		srv := uasrv.New(uasrv.EnableSecurity("None", ua.MessageSecurityModeNone), uasrv.EnableAuthMode(ua.UserTokenTypeAnonymous), uasrv.EndPoint("127.0.0.1", port))
		ns := uasrv.NewNodeNameSpace(srv, "urn:virtual:pepperl-fuchs:iolm")
		root := ns.AddNode(uasrv.NewFolderNode(ua.NewStringNodeID(ns.ID(), "IOLM"), "IOLM"))
		srv.Node(uasrv.ObjectsFolder).AddRef(root, id.Organizes, true)
		for name, v := range map[string]string{"IOLM/Model": "ICE3-8IOL-K45S-RJ45", "IOLM/Serial": "40000123"} {
			n := ns.AddNewVariableStringNode(name, v)
			_ = n.SetAttribute(ua.AttributeIDDataType, uasrv.DataValueFromValue(ua.NewNumericNodeID(0, id.String)))
			root.AddRef(n, id.HasComponent, true)
		}
		if err := srv.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { time.Sleep(200 * time.Millisecond); _ = srv.Close() })
		return fmt.Sprintf("127.0.0.1:%d", port)
	}
	a, b := serve(), serve()
	manifest := func(pf1 string) string {
		return `name: d
tasks: [{ program: main.st, scan: 20ms }]
devices:
  - { name: PF1, profile: pepperl-fuchs-ice3, endpoint: "` + pf1 + `", points: [Model, Serial] }
  - { name: PF2, profile: pepperl-fuchs-ice3, endpoint: "127.0.0.1:1", points: [Model, Serial], validate: static }
`
	}
	project := func(m, profile string) fstest.MapFS {
		fsys := devicesProject(t, m, "PROGRAM Main\nEND_PROGRAM\n")
		fsys["profiles/pepperl-fuchs-ice3.yaml"] = &fstest.MapFile{Data: []byte(profile)}
		return fsys
	}
	shortProbes(t)
	r := startIORig(t, project(manifest(a), string(ice3)), false)

	v := r.io.Validate(r.io.Stage(project(manifest(b), string(ice3)), "upload", "tester"))
	if !v.Valid || len(v.Probes) != 1 || !v.Probes[0].OK || v.Probes[0].Device != "PF1" {
		t.Fatalf("PF1 to a server with its nodes: valid %v probes %+v checks %+v", v.Valid, v.Probes, v.Checks)
	}
	missing := strings.Replace(string(ice3), `node-id: "ns=1;s=IOLM/Serial"`, `node-id: "ns=1;s=NoSuchNode"`, 1)
	v = r.io.Validate(r.io.Stage(project(manifest(a), missing), "upload", "tester"))
	var pf1 IOProbe
	for _, p := range v.Probes {
		if p.Device == "PF1" {
			pf1 = p
		}
	}
	if v.Valid || pf1.OK || !strings.Contains(strings.Join(pf1.Bad, " "), "PF1.Serial") {
		t.Fatalf("a node the server lacks: valid %v probes %+v", v.Valid, v.Probes)
	}
}

// A devices: row's validate: overrides its driver's.
func TestIOProbeRowOverridesDriver(t *testing.T) {
	d1 := newSlave(t, 0)
	manifest := func(addr, row string) string {
		return `name: d
tasks: [{ program: main.st, scan: 20ms }]
drivers: [{ type: modbus, scan-rate: 20ms, validate: live }]
devices:
  - { name: CONV, profile: abb-acs355-fmbt21, endpoint: "` + addr + `"` + row + ` }
`
	}
	files := devicesProject(t, manifest(d1.Addr(), ", validate: static"), "PROGRAM Main\nEND_PROGRAM\n")
	r := startIORig(t, files, false)
	v := r.io.Validate(r.io.Stage(devicesProject(t, manifest(fmt.Sprintf("127.0.0.1:%d", deadPort(t)), ", validate: static"), "PROGRAM Main\nEND_PROGRAM\n"), "upload", "tester"))
	p := probeOf(t, v)
	if !v.Valid || p.Mode != devbind.ValidateStatic || !strings.Contains(p.Why, "devices: row") {
		t.Fatalf("row static under driver live: valid %v probe %+v", v.Valid, p)
	}
}

// writingDriver writes on start unless a write gate says no: a driver that
// asserts its outputs on connect. A probe must close its gate first.
type writingDriver struct {
	gate   func() bool
	writes *int
}

func (d *writingDriver) ReadInputs() (nio.Values, error) { return nio.Values{}, nil }
func (d *writingDriver) WriteOutputs(nio.Values) error   { return nil }
func (d *writingDriver) SetWriteGate(g func() bool)      { d.gate = g }
func (d *writingDriver) Start(context.Context) {
	if d.gate == nil || d.gate() {
		*d.writes++
	}
}

// A probe's driver writes nothing: its write gate is closed before it
// starts, whatever the driver would assert on its own.
func TestIOProbeClosesTheWriteGate(t *testing.T) {
	writes := 0
	p := runProbe(probeTarget{
		IOProbe: IOProbe{Mode: devbind.ValidateLive},
		addr:    "device:502",
		build:   func() (nio.Driver, error) { return &writingDriver{writes: &writes}, nil },
		inputs:  func(nio.Driver) []string { return []string{"x"} },
		quality: func(nio.Driver) map[string]nio.Quality { return nil },
		health:  func(nio.Driver) (bool, string, uint64) { return true, "", 0 },
	})
	if !p.OK || writes != 0 {
		t.Fatalf("probe %+v, %d writes: want none", p, writes)
	}
}

// waitWatch waits for the last apply's watch to end, and returns it.
func waitWatch(t *testing.T, r *ioRig) *IOWatch {
	t.Helper()
	var w *IOWatch
	eventually(t, "the watch to end", func() bool {
		w = r.io.Watch()
		return w != nil && w.State != WatchWatching
	})
	return w
}

// After an apply, its watch follows the changed devices off the apply lock
// and on a context of its own. A silent one stays applied and reads Bad,
// unless the apply asked to revert if silent (#706, James's decision).
func TestIOApplyAnswersAndRevertIfSilent(t *testing.T) {
	s1, s2, s3, s4 := newSlave(t, 4), newSlave(t, 7), newSlave(t, 4), newSlave(t, 7)
	base := ioFiles(slavePort(s1), slavePort(s2), "")
	// Static, so a candidate pointing at nothing passes validation, as a
	// device that answered its probe and dropped after would.
	base = withFile(base, "nautilus.yaml", strings.Replace(string(base["nautilus.yaml"].Data), "  scan-rate: 10ms\n", "  scan-rate: 10ms\n  validate: static\n", 1))
	stage := func(r *ioRig, y string) *IOCandidate {
		t.Helper()
		c := r.io.Stage(withFile(base, "modbus.yaml", y), "upload", "tester")
		if v := r.io.Validate(c); !v.Valid {
			t.Fatalf("validate: %+v", v.Checks)
		}
		return c
	}
	apply := func(r *ioRig, y string, opts ApplyOptions) (*IOResult, error) {
		t.Helper()
		return r.io.ApplyWith(r.ctx, stage(r, y), "tester", opts)
	}

	t.Run("a device that answers", func(t *testing.T) {
		r := startIORig(t, base, false)
		if _, err := apply(r, modbusYAML(slavePort(s3), slavePort(s2), "", ""), ApplyOptions{}); err != nil {
			t.Fatal(err)
		}
		if w := waitWatch(t, r); w.State != WatchAnswered || fmt.Sprint(w.Answered) != "[device DEV_A]" {
			t.Fatalf("watch %+v", w)
		}
	})
	t.Run("silent, no revert asked", func(t *testing.T) {
		r := startIORig(t, base, false)
		if _, err := apply(r, modbusYAML(deadPort(t), slavePort(s2), "", ""), ApplyOptions{}); err != nil {
			t.Fatal(err)
		}
		if w := waitWatch(t, r); w.State != WatchSilent || fmt.Sprint(w.Silent) != "[device DEV_A]" || !strings.Contains(w.Reason, "the change stays") {
			t.Fatalf("watch %+v", w)
		}
		if d := r.io.Differs(); len(d) != 1 || d[0].Device != "DEV_A" {
			t.Fatalf("differs %+v, want DEV_A applied", d)
		}
		eventually(t, "A_In reads Bad", func() bool { return !r.rt.TagQuality("A_In").IsGood() })
	})
	t.Run("silent, revert asked", func(t *testing.T) {
		r := startIORig(t, base, false)
		eventually(t, "polled", func() bool { return r.value("A_In") == "4" })
		if _, err := apply(r, modbusYAML(deadPort(t), slavePort(s2), "", ""), ApplyOptions{RevertIfSilent: time.Second}); err != nil {
			t.Fatal(err)
		}
		if w := waitWatch(t, r); w.State != WatchReverted || !strings.Contains(w.Reason, "DEV_A") {
			t.Fatalf("watch %+v", w)
		}
		if d := r.io.Differs(); len(d) != 0 {
			t.Fatalf("differs %+v after the revert, want none", d)
		}
		s1.Unit(1).SetHolding(0, 13)
		eventually(t, "A_In from DEV_A's old address again", func() bool { return r.value("A_In") == "13" })
	})
	t.Run("silent, the revert fails", func(t *testing.T) {
		r := startIORig(t, base, false)
		real := r.io.replaceSource
		calls := 0
		r.io.replaceSource = func(mb *modbus.Driver, src modbus.Source, tags []modbus.TagBinding, opts modbus.ReplaceOptions) error {
			calls++
			if calls > 1 {
				return errors.New("injected") // putting it back
			}
			return real(mb, src, tags, opts)
		}
		if _, err := apply(r, modbusYAML(deadPort(t), slavePort(s2), "", ""), ApplyOptions{RevertIfSilent: 500 * time.Millisecond}); err != nil {
			t.Fatal(err)
		}
		if w := waitWatch(t, r); w.State != WatchRevertFail || !strings.Contains(r.io.Fault(), "DEV_A") {
			t.Fatalf("watch %+v fault %q", w, r.io.Fault())
		}
	})
	t.Run("the apply answers at once and keeps no lock", func(t *testing.T) {
		r := startIORig(t, base, false)
		deadA := deadPort(t)
		start := time.Now()
		res, err := apply(r, modbusYAML(deadA, slavePort(s2), "", ""), ApplyOptions{RevertIfSilent: 3 * time.Second})
		if err != nil || res.Watch == nil || res.Watch.State != WatchWatching || !res.Watch.Revert {
			t.Fatalf("err %v result %+v", err, res)
		}
		if d := time.Since(start); d > 2500*time.Millisecond {
			t.Fatalf("the apply took %v: it waited for its devices", d)
		}
		// Another apply goes through meanwhile, and the first is then not
		// reverted: a newer apply landed.
		if _, err := apply(r, modbusYAML(deadA, slavePort(s4), "", ""), ApplyOptions{}); err != nil {
			t.Fatalf("an apply during the first one's watch: %v", err)
		}
		time.Sleep(3500 * time.Millisecond)
		if d := r.io.Differs(); len(d) != 2 {
			t.Fatalf("differs %+v, want DEV_A and DEV_B still applied (the first apply not reverted)", d)
		}
	})
	t.Run("a client that goes away makes no silence", func(t *testing.T) {
		slow := newSlave(t, 4)
		slow.SetLatency(400 * time.Millisecond) // answers, slowly
		r := startIORig(t, base, false)
		ctx, cancel := context.WithCancel(r.ctx)
		c := stage(r, modbusYAML(slavePort(slow), slavePort(s2), "", ""))
		if _, err := r.io.ApplyWith(ctx, c, "tester", ApplyOptions{RevertIfSilent: 3 * time.Second}); err != nil {
			t.Fatal(err)
		}
		cancel() // the browser tab closed
		if w := waitWatch(t, r); w.State != WatchAnswered {
			t.Fatalf("watch %+v: a client going away must not decide the device was silent", w)
		}
		if d := r.io.Differs(); len(d) != 1 {
			t.Fatalf("differs %+v, want DEV_A still applied", d)
		}
	})
	t.Run("a pending revert cancelled", func(t *testing.T) {
		r := startIORig(t, base, false)
		if _, err := apply(r, modbusYAML(deadPort(t), slavePort(s2), "", ""), ApplyOptions{RevertIfSilent: time.Second}); err != nil {
			t.Fatal(err)
		}
		w, ok := r.io.CancelRevert("tester")
		if !ok || w.State != WatchCancelled || !strings.Contains(w.Reason, "tester") {
			t.Fatalf("cancel: %v %+v", ok, w)
		}
		time.Sleep(1500 * time.Millisecond)
		if d := r.io.Differs(); len(d) != 1 || d[0].Device != "DEV_A" {
			t.Fatalf("differs %+v, want DEV_A still applied after the cancelled revert's deadline", d)
		}
		if _, ok := r.io.CancelRevert("tester"); ok {
			t.Fatal("a second cancel found a revert pending")
		}
	})
}
