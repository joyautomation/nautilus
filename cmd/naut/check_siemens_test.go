package main

import (
	"strings"
	"testing"
)

const siemensCheckManifest = `name: s7
tasks:
  - program: main.st
    scan: 100ms
tags:
  - {name: Level, role: input, type: REAL}
target:
  siemens:
    agent: AGENT
    station:
      cpu: {name: PLC_1, order: 6ES7 510-1DK03-0AB0, firmware: V3.1, address: 192.0.2.10, subnet-mask: 255.255.255.0, profinet-name: NAME}
`

const siemensCheckProgram = "PROGRAM main\nVAR x : REAL; END_VAR\nx := Level;\nEND_PROGRAM\n"

// TestCheckReportsSiemensStationProblems: an unfinished station loads (so
// the rest of check still runs) and each problem is an error with its
// YAML path. A refused agent URL never echoes its credentials.
func TestCheckReportsSiemensStationProblems(t *testing.T) {
	m := strings.NewReplacer("AGENT", "https://u:hunter2@tiad:8190", "NAME", "PLC_1").Replace(siemensCheckManifest)
	out, code := checkIn(t, map[string]string{"nautilus.yaml": m, "main.st": siemensCheckProgram})
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	for _, want := range []string{
		"error: target.siemens.agent: carries user info",
		"error: target.siemens.station.cpu.profinet-name:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "hunter2") {
		t.Fatalf("the password was echoed:\n%s", out)
	}
}

func TestCheckPassesAGoodSiemensStation(t *testing.T) {
	m := strings.NewReplacer("AGENT", "http://127.0.0.1:8190", "NAME", "plc-1").Replace(siemensCheckManifest)
	out, code := checkIn(t, map[string]string{"nautilus.yaml": m, "main.st": siemensCheckProgram})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
