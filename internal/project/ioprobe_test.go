//go:build !js

package project

// Live validation probes (#706 PR 4) against real Modbus slaves and OPC UA
// servers: what a probe proves, what it refuses and why, static mode, and
// that a probe never stalls the scan.

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/devbind"
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

	t.Run("a device that answers", func(t *testing.T) {
		v := validate(modbusYAML(slavePort(s3), slavePort(s2), "", ""))
		p := probeOf(t, v)
		if !v.Valid || !p.OK || p.Mode != devbind.ValidateLive || p.Device != "DEV_A" || liveOf(v).Skipped {
			t.Fatalf("valid %v probe %+v live %+v", v.Valid, p, liveOf(v))
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
