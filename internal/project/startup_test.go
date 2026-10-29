package project

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/optommp"
	"github.com/joyautomation/nautilus/optommp/optommptest"
	"github.com/joyautomation/nautilus/runtime"
)

// startup.wait-for-io: absent is the 60 s default, 0 does not wait, a
// duration is the longest wait, a negative one is refused.
func TestStartupWaitForIOConfig(t *testing.T) {
	for _, tc := range []struct {
		name, section string
		want          time.Duration
		err           string
	}{
		{"absent is the default", "", DefaultWaitForIO, ""},
		{"empty section is the default", "startup: {}\n", DefaultWaitForIO, ""},
		{"zero does not wait", "startup:\n  wait-for-io: 0s\n", 0, ""},
		{"bare zero", "startup:\n  wait-for-io: 0\n", 0, ""},
		{"a duration", "startup:\n  wait-for-io: 2m\n", 2 * time.Minute, ""},
		{"negative", "startup:\n  wait-for-io: -1s\n", 0, "startup.wait-for-io"},
		{"a typo is an error", "startup:\n  wait-for-i0: 1s\n", 0, "wait-for-i0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(fsys(manifest+tc.section), "")
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want containing %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Runtime.StartupWait != tc.want {
				t.Fatalf("StartupWait = %v, want %v", p.Runtime.StartupWait, tc.want)
			}
			if p.Runtime.DriverName != "memory" {
				t.Fatalf("DriverName = %q, want the lone driver's name", p.Runtime.DriverName)
			}
		})
	}
}

// End to end on a real driver that keeps its own image: an optommp
// project, default wait, with the driver's status tags (in its read from
// New on, always Good) bound as inputs. Before the unit answers, every
// scan is held —
// the program does not run, nothing is written, the stats say which
// driver it waits for — and the first scan after the first delivery runs
// with FIRST_SCAN() TRUE and pushes the output.
func TestStartupWaitHoldsAnOptommpProject(t *testing.T) {
	dev := optommptest.NewBench()
	port := dev.Listen(t)
	files := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`
name: epic
tasks:
  - program: program.st
    scan: 50ms
tags:
  - { name: EPIC_M0_DI03, role: input }
  - { name: EPIC__Online, role: input }
  - { name: EPIC__Outages, role: input }
  - { name: EPIC_M1_DO11, role: output, init: false }
  - { name: Firsts, role: state, init: 0 }
  - { name: Runs, role: state, init: 0 }
driver:
  type: optommp
  name: epic
  manifest: optommp_manifest.yaml
  scan-rate: 10ms
`)},
		"program.st": &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR_EXTERNAL EPIC_M0_DI03 : BOOL; EPIC_M1_DO11 : BOOL; Firsts : DINT; Runs : DINT; END_VAR
IF FIRST_SCAN() THEN Firsts := Firsts + 1; END_IF;
Runs := Runs + 1;
EPIC_M1_DO11 := EPIC_M0_DI03;
END_PROGRAM`)},
		"optommp_manifest.yaml": &fstest.MapFile{Data: []byte(fmt.Sprintf(`host: 127.0.0.1
port: %d
modules:
  - { slot: 0, part: "GRV-IACDCTTL-24", type: 0x50000005, channels: 24 }
  - { slot: 1, part: "GRV-ODCI-12", type: 0x90000015, channels: 12 }
tags:
  - { name: EPIC_M0_DI03, point: di, slot: 0, channel: 3 }
  - { name: EPIC_M1_DO11, point: do, slot: 1, channel: 11 }
  - { name: EPIC__Online, point: online }
  - { name: EPIC__Outages, point: outages }
`, port))},
	}
	p, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Runtime.StartupWait != DefaultWaitForIO {
		t.Fatalf("StartupWait = %v", p.Runtime.StartupWait)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	drv := p.Runtime.Driver.(*optommp.Driver)
	runs := func() int64 {
		v, _ := rt.Tags().ReadPath("Runs")
		n, _ := v.(int64)
		return n
	}

	// Not started: the driver's reads succeed with nothing in them.
	for i := 0; i < 3; i++ {
		rt.Scan()
	}
	s := rt.Stats()
	if runs() != 0 || s.Count != 0 || s.LogicErrors != 0 {
		t.Fatalf("held scans ran: Runs=%d count=%d logicErrors=%d (%s)", runs(), s.Count, s.LogicErrors, s.LastError)
	}
	if !s.WaitingForIO || len(s.WaitingFor) != 1 || s.WaitingFor[0] != "epic" {
		t.Fatalf("waitingForIO=%v waitingFor=%v, want true [epic]", s.WaitingForIO, s.WaitingFor)
	}
	if drv.Health().Writes != 0 {
		t.Fatal("an output was written during the hold")
	}
	// The driver's own status tags are in every read from New on; they
	// reach the store (an HMI can show them) but are not a delivery.
	if v, ok := rt.Tags().ReadPath("EPIC__Online"); !ok || v != false {
		t.Fatalf("EPIC__Online = %v (%v), want the status tag's false in the store while held", v, ok)
	}

	drv.Start(context.Background())
	defer drv.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for rt.Stats().WaitingForIO {
		if time.Now().After(deadline) {
			t.Fatalf("still held after the driver started: %+v", drv.Health())
		}
		rt.Scan()
		time.Sleep(5 * time.Millisecond)
	}
	if v, _ := rt.Tags().ReadPath("Firsts"); v != int64(1) {
		t.Fatalf("Firsts = %v, want FIRST_SCAN() TRUE on the first scan that ran", v)
	}
	if s := rt.Stats(); s.LogicErrors != 0 {
		t.Fatalf("logic errors after the hold: %s", s.LastError)
	}
}
