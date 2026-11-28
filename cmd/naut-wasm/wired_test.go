//go:build js && wasm

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The playground runs an instance on two connections (#1458 slice 9a-2):
// its drivers are simulated and answer, so each connection's companion is
// TRUE, and the runtime computes <Instance>__Online, their AND, as it does
// natively. A program reading it compiles and runs here as it does there.
func TestThePlaygroundComputesTheCombinedCompanion(t *testing.T) {
	dir := t.TempDir()
	for name, text := range map[string]string{
		"nautilus.yaml": `name: w
tasks:
  - program: main.st
    scan: 50ms
wirings:
  - type: Pump
    sources:
      x: { driver: modbus, endpoint: 192.0.2.1 }
      y: { driver: opcua-client, endpoint: "opc.tcp://192.0.2.2:4840" }
    members:
      Running: { from: x, table: coil, address: 0 }
      Speed:   { from: y, node: "ns=1;s=P/Speed", namespace: "urn:x", type: Double }
instances:
  - { name: P101, type: Pump }
`,
		"types.st": "TYPE Pump : STRUCT\n  Running : BOOL;\n  Speed : REAL;\nEND_STRUCT; END_TYPE\n",
		"main.st":  "PROGRAM Main\nVAR_EXTERNAL P101__Online : BOOL; END_VAR\nVAR up : BOOL; END_VAR\nup := P101__Online;\nEND_PROGRAM\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := load(dir); err != nil {
		t.Fatal(err)
	}
	defer stop()
	r := current()
	if r == nil {
		t.Fatal("nothing is running")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !r.rt.Tags().Bool("P101__Online") {
		time.Sleep(20 * time.Millisecond)
	}
	for _, name := range []string{"P101_x__Online", "P101_y__Online", "P101__Online"} {
		if !r.rt.Tags().Bool(name) {
			t.Errorf("%s is FALSE in the playground, want TRUE (its simulated connections answer)", name)
		}
	}
	// It is the runtime's AND, not a simulated TRUE: one connection held
	// down makes it FALSE.
	if err := r.rt.Tags().Force("P101_y__Online", false); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && r.rt.Tags().Bool("P101__Online") {
		time.Sleep(20 * time.Millisecond)
	}
	if r.rt.Tags().Bool("P101__Online") {
		t.Error("P101__Online stayed TRUE with connection y forced down")
	}
}
