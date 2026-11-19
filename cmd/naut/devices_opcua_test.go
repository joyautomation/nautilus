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
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/virtdev"
)

// opcuaProject writes a project with an OPC UA IO-Link master declared by
// profile, the row's endpoint being endpoint, and a program that reads the
// device's struct and its driver's companion and writes a command tag.
func opcuaProject(t *testing.T, dir, endpoint string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, project.ManifestName), []byte(`name: iolm
tasks: [{ program: main.st, scan: 20ms }]
devices:
  - { name: PF1, profile: pepperl-fuchs-ice3, endpoint: "`+endpoint+`", points: [Port1_PdiU32, Port1_PdoU32] }
tags:
  - { name: Seen, role: state, type: UDINT }
  - { name: Up,   role: state, type: BOOL }
  - { name: Cmd,  role: setpoint, type: UDINT, init: 0 }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.st"), []byte(`PROGRAM Main
Seen := PF1.Port1_PdiU32;
Up := PF1__Online;
PF1_Port1_PdoU32 := Cmd;
END_PROGRAM
`), 0o644); err != nil {
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
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatalf("the program does not compile against the device: %v", err)
	}
	drv := proj.Runtime.Driver
	drv.(interface{ Start(context.Context) }).Start(ctx)
	done := make(chan struct{})
	rctx, stop := context.WithCancel(ctx)
	go func() { rt.Run(rctx); close(done) }()
	defer func() {
		stop()
		<-done
		drv.(interface{ Stop() }).Stop()
	}()
	waitFor(t, "the program reads the device's struct and its driver's companion", func() bool {
		v, _ := rt.Tags().ReadPath("Seen")
		iv, isIR := v.(ir.Value)
		return rt.Tags().Bool("Up") && ((isIR && iv.I == 4242) || v == int64(4242))
	})
	q := drv.(interface{ Quality() map[string]nio.Quality }).Quality()
	if q["PF1"] != nio.Good && q["PF1"] != 0 {
		t.Errorf("PF1's quality %v, want Good: %v", q["PF1"], q)
	}
	for name, qv := range q {
		if strings.HasPrefix(name, "PF1.") || (name == "PF1_Port1_PdoU32" && qv != nio.Good) {
			t.Errorf("quality reports %s (%v): a member, or a command as not Good", name, qv)
		}
	}
	// The first push is the driver's start-up baseline, recorded and not
	// sent, so the command is a change made after it.
	if err := rt.Tags().SetPath("Cmd", int64(777)); err != nil {
		t.Fatal(err)
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
