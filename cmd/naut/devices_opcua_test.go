package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/virtdev"
)

// opcuaProject writes a project with an OPC UA IO-Link master declared by
// profile, vendored as naut devices add leaves it, the row's endpoint
// being endpoint.
func opcuaProject(t *testing.T, dir, endpoint string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, project.ManifestName), []byte(`name: iolm
tasks: [{ program: main.st, scan: 20ms }]
devices:
  - { name: PF1, profile: pepperl-fuchs-ice3, endpoint: "`+endpoint+`", points: [Port1_PdiU32, Port1_PdoU32] }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.st"), []byte("PROGRAM Main\nEND_PROGRAM\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An OPC UA devices: row end to end: naut check passes and says the
// device is bound by namespace index (the profile's URI is a stand-in),
// and the project's own driver, built by Load from the row, reads the
// device's struct from its virtual twin on the wire and writes a command
// tag through to it.
func TestDevicesOPCUARowOnTheWire(t *testing.T) {
	dir := t.TempDir()
	opcuaProject(t, dir, "10.0.0.21")
	if code := runDevicesAdd([]string{"pepperl-fuchs-ice3", dir}, io.Discard); code != 0 {
		t.Fatalf("naut devices add exited %d", code)
	}
	var code int
	out := captureStdout(t, func() int { code = runCheck([]string{dir}); return code })
	if code != 0 {
		t.Fatalf("naut check exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "warning: device PF1 binds by namespace index 1") {
		t.Errorf("naut check does not warn of the index-only binding:\n%s", out)
	}
	// The driver a row makes is the profiles' documented security None,
	// which needs no certificate store.
	if strings.Contains(out, "pki-dir") {
		t.Errorf("naut check warns of a certificate store:\n%s", out)
	}

	var b strings.Builder
	if code := runDevicesRender([]string{dir}, &b); code != 0 {
		t.Fatalf("naut devices render exited %d", code)
	}
	for _, want := range []string{"# opcua PF1 (driver PF1, opc.tcp://10.0.0.21:4840)", "PF1.Port1_PdiU32  read", "PF1_Port1_PdoU32  write", "(by index)"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("render lacks %q:\n%s", want, b.String())
		}
	}
	if strings.Contains(b.String(), "# modbus") {
		t.Errorf("render shows a Modbus driver for an OPC UA device:\n%s", b.String())
	}

	set, err := loadVirtualDevices(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run, err := virtdev.Start(ctx, set, virtdev.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Stop()
	if len(run.Endpoints) != 1 || run.Endpoints[0].Endpoint != "opc.tcp://10.0.0.21:4840" {
		t.Fatalf("endpoints %+v, want PF1 standing in for opc.tcp://10.0.0.21:4840", run.Endpoints)
	}
	if err := run.Runtime.Tags().SetPath("PF1_Port1_PdiU32", int64(4242)); err != nil {
		t.Fatal(err)
	}

	opcuaProject(t, dir, run.Endpoints[0].Listen) // the project's driver, dialling the wire face
	proj, err := project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	drv := proj.Runtime.Driver
	drv.(interface{ Start(context.Context) }).Start(ctx)
	defer drv.(interface{ Stop() }).Stop()
	waitFor(t, "PF1.Port1_PdiU32 reads the device", func() bool {
		vals, err := drv.ReadInputs()
		if err != nil {
			return false
		}
		v, ok := vals["PF1"].(ir.Value)
		return ok && v.Fld[v.Struct.FieldIndex["Port1_PdiU32"]].I == 4242 && vals["PF1__Online"] == true
	})
	// The first push is the driver's start-up baseline, recorded and not
	// sent, so the command is a change made after it.
	for _, n := range []int64{0, 777} {
		if err := drv.WriteOutputs(nio.Values{"PF1_Port1_PdoU32": n}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the command tag's write reaches the device", func() bool {
		v, ok := run.Runtime.Tags().ReadPath("PF1_Port1_PdoU32")
		iv, isIR := v.(ir.Value)
		return ok && ((isIR && iv.I == 777) || v == int64(777))
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
