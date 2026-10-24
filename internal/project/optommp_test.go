package project

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/optommp"
	"github.com/joyautomation/nautilus/optommp/optommptest"
	"github.com/joyautomation/nautilus/runtime"
)

// An optommp project loads and compiles with no processor in sight, and —
// pointed at the in-process bench chassis — its driver connects, delivers
// quality per channel, and reports a status row.
func TestLoadOptommpDriver(t *testing.T) {
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
  - { name: EPIC_M2_AI01, role: input }
  - { name: EPIC_M1_DO11, role: output, init: false }
driver:
  type: optommp
  manifest: optommp_manifest.yaml
  scan-rate: 10ms
`)},
		"program.st": &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR_EXTERNAL EPIC_M0_DI03 : BOOL; EPIC_M2_AI01 : REAL; EPIC_M1_DO11 : BOOL; END_VAR
EPIC_M1_DO11 := EPIC_M0_DI03;
END_PROGRAM`)},
		"optommp_manifest.yaml": &fstest.MapFile{Data: []byte(fmt.Sprintf(`host: 127.0.0.1
port: %d
modules:
  - { slot: 0, part: "GRV-IACDCTTL-24", type: 0x50000005, channels: 24 }
  - { slot: 1, part: "GRV-ODCI-12", type: 0x90000015, channels: 12 }
  - { slot: 2, part: "GRV-IICTD-12", type: 0x6000001F, channels: 12 }
tags:
  - { name: EPIC_M0_DI03, point: di, slot: 0, channel: 3 }
  - { name: EPIC_M2_AI01, point: ai, slot: 2, channel: 1, unit: "°C" }
  - { name: EPIC_M1_DO11, point: do, slot: 1, channel: 11 }
`, port))},
	}
	p, err := Load(files, "")
	if err != nil {
		t.Fatalf("an optommp project must load with no processor: %v", err)
	}
	drv, ok := p.Runtime.Driver.(*optommp.Driver)
	if !ok {
		t.Fatalf("driver = %T, want *optommp.Driver", p.Runtime.Driver)
	}
	if _, err := runtime.New(p.Runtime); err != nil {
		t.Fatalf("an optommp project must compile: %v", err)
	}
	fn := p.DriverStatus(nil)
	if fn == nil {
		t.Fatal("DriverStatus must report an optommp driver")
	}
	if s := fn(); len(s) != 1 || s[0].Kind != "optommp" || s[0].State != "connecting" {
		t.Fatalf("before Start: %+v", s)
	}

	drv.Start(context.Background())
	defer drv.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for drv.Health().Reads == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no scan: %+v", drv.Health())
		}
		time.Sleep(5 * time.Millisecond)
	}
	s := fn()[0]
	if s.State != "connected" || !strings.Contains(s.Detail, "GRV-EPIC-PR1") || !strings.Contains(s.Message, "1 of 3 tags bad") {
		t.Errorf("status: %+v", s)
	}
	if q := drv.Quality(); q["EPIC_M0_DI03"] != nio.Good || q["EPIC_M2_AI01"] != nio.Bad {
		t.Errorf("quality: %v", q)
	}
}

func TestLoadOptommpErrors(t *testing.T) {
	base := "tasks:\n  - program: program.fbd\ndriver:\n  type: optommp\n"
	for _, tc := range []struct{ name, extra, manifest, want string }{
		{"no manifest key", "", "", "driver optommp: manifest"},
		{"missing manifest file", "  manifest: nope.yaml\n", "", "driver optommp"},
		{"typo is an error", "  manifest: m.yaml\n", "host: h\nscanrate: 1s\n", "scanrate"},
		{"bad binding", "  manifest: m.yaml\n", "host: h\ntags:\n  - { name: T, point: di, slot: 99 }\n", "slot 99"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := fsys(base + tc.extra)
			if tc.manifest != "" {
				files["m.yaml"] = &fstest.MapFile{Data: []byte(tc.manifest)}
			}
			_, err := Load(files, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestOptommpStatus(t *testing.T) {
	h := optommp.Health{Host: "10.0.0.5", Port: 2001, Unit: "GRV-EPIC-PR1", Firmware: "4.1.2.0", Connected: true,
		Modules: 4, Tags: 72, BadTags: 11, Requests: 2, ScanUs: 1500, ScanRate: 20 * time.Millisecond}
	s := optommpStatus(h)
	if s.State != "connected" || s.Detail != "GRV-EPIC-PR1 · firmware 4.1.2.0 · 10.0.0.5:2001" {
		t.Fatalf("connected: %+v", s)
	}
	h.Faults = "slot 3 is empty (expected GRV-IV-24)"
	if s := optommpStatus(h); s.State != "error" || s.Message != h.Faults {
		t.Fatalf("fault: %+v", s)
	}
	h = optommp.Health{Host: "10.0.0.5", Port: 2001, LastError: "dial tcp: refused"}
	if s := optommpStatus(h); s.State != "error" || s.Detail != "10.0.0.5:2001" {
		t.Fatalf("down: %+v", s)
	}
}

func TestOptommpStatusStreamAndWatchdog(t *testing.T) {
	h := optommp.Health{Host: "10.0.0.5", Port: 2001, Connected: true, Mode: "stream", Stream: 20 * time.Millisecond,
		GapMaxUs: 21500, Late: 2, WatchdogMs: 500, Armed: 3, Armable: 12}
	s := optommpStatus(h)
	got := map[string]string{}
	for _, m := range s.Metrics {
		got[m.Label] = m.Text
	}
	if got["stream"] != "every 20ms, longest gap 21.5 ms" || got["watchdog"] != "500 ms, 3 of 12 bound outputs have a watchdog value" {
		t.Errorf("metrics: %+v", s.Metrics)
	}
	if _, ok := got["scan"]; ok {
		t.Error("a streaming driver shows a poll scan")
	}
	if _, ok := got["late"]; !ok {
		t.Error("no late count")
	}
}
