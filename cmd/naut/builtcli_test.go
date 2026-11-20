package main

// A box installed by naut deploy has no naut, only the built controller. The
// OPC UA server page tells an operator to approve a client certificate with
// NAUTILUS_CLI=1 controller opcua certs|trust (blackbox 021 F-08): the
// variable turns the controller into the command line instead of starting it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltControllerWithNautilusCLIRunsOpcuaCerts(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a controller binary")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	dir := t.TempDir()
	naut := filepath.Join(dir, "naut")
	if out, err := exec.Command(goBin, "build", "-o", naut, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/naut: %v\n%s", err, out)
	}
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"nautilus.yaml": "name: cli\ntasks:\n  - program: main.st\n    scan: 100ms\ndriver:\n  type: memory\n",
		"main.st":       "PROGRAM Main\nEND_PROGRAM\n",
	} {
		if err := os.WriteFile(filepath.Join(proj, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctl := filepath.Join(dir, "controller")
	if out, err := exec.Command(naut, "build", "-o", ctl, proj).CombinedOutput(); err != nil {
		t.Fatalf("naut build: %v\n%s", err, out)
	}
	pki := filepath.Join(dir, "pki") // the store the service would have made on its first start
	if err := os.MkdirAll(filepath.Join(pki, "rejected", "certs"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ctl, "opcua", "certs", "--pki", pki)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=", "NAUTILUS_CLI=1", "HOME=" + dir, "NAUTILUS_ADDR=127.0.0.1:0"}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Trusted clients ("+pki) {
		t.Fatalf("NAUTILUS_CLI=1 controller opcua certs --pki %s: %v\n%s", pki, err, out)
	}
}
