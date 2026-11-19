package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/devbind"
	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/virtdev"
)

// A device documented at one Modbus connection, end to end (#706's static
// fallback without hardware): a row cannot ask for a live check of it, its
// virtual twin serves the project's driver and refuses a second client (a
// live check's probe), counting it, and the driver keeps reading.
func TestDevicesOneConnection(t *testing.T) {
	dir := driveProject(t)
	pf := filepath.Join(dir, devprofile.ProjectFile("abb-acs355-fmbt21"))
	raw, err := os.ReadFile(pf)
	if err != nil {
		t.Fatal(err)
	}
	limited := strings.Replace(string(raw), "modbus:\n  port: 502\n", "modbus:\n  port: 502\n  max-connections: 1\n  max-connections-doc: \"fena §7.1\"\n", 1)
	if limited == string(raw) {
		t.Fatal("the fixture edit did not apply")
	}
	if err := os.WriteFile(pf, []byte(limited), 0o644); err != nil {
		t.Fatal(err)
	}

	// The row's default is a static check, and it cannot ask for live.
	m, err := project.ReadManifest(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, def, why, _ := m.DeviceSet().Validate("CONV"); def != devbind.ValidateStatic || !strings.Contains(why, "max-connections 1") {
		t.Errorf("default %q (%s), want static for a one-connection device", def, why)
	}
	manifest := filepath.Join(dir, project.ManifestName)
	orig, _ := os.ReadFile(manifest)
	live := strings.Replace(string(orig), "unit-id: 3 }", "unit-id: 3, validate: live }", 1)
	if err := os.WriteFile(manifest, []byte(live), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	out := captureStdout(t, func() int { code = runCheck([]string{dir}); return code })
	if code == 0 || !strings.Contains(out, "validate: live, but profile abb-acs355-fmbt21 documents max-connections 1") {
		t.Errorf("naut check of validate: live on a one-connection device: exit %d\n%s", code, out)
	}
	if err := os.WriteFile(manifest, orig, 0o644); err != nil {
		t.Fatal(err)
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
	ep := run.Endpoints[0]
	if ep.MaxConnections != 1 {
		t.Fatalf("endpoint %+v, want max-connections 1", ep)
	}
	var b bytes.Buffer
	printEndpoints(&b, run.Endpoints)
	if lines := strings.Split(b.String(), "\n"); !strings.Contains(lines[0], "MAX CONN") || !strings.Contains(lines[1], " 1 ") {
		t.Errorf("naut devices up does not show the limit:\n%s", b.String())
	}

	rows := m.DeviceSet().Rows()
	rows[0].Endpoint = ep.Listen
	devs, err := devbind.Resolve(os.DirFS(dir), rows)
	if err != nil {
		t.Fatal(err)
	}
	mm, err := devs.MergeModbus(modbus.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	md, err := modbus.New(mm, modbus.WithScanRate(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	w := devs.WrapModbus(md)
	w.Start(ctx)
	defer w.Stop()
	reads := func() bool {
		vals, err := w.ReadInputs()
		if err != nil {
			return false
		}
		v, ok := vals["CONV"].(ir.Value)
		return ok && v.Fld[v.Struct.FieldIndex["StatusWord"]].I == 0x1240 && vals["CONV__Online"] == true
	}
	waitFor(t, "the project's driver reads the device on its one connection", reads)

	// A second client, as a live check's probe would be: no answer.
	probe, err := net.Dial("tcp", ep.Listen)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	probe.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := probe.Write([]byte{0, 1, 0, 0, 0, 6, 3, 0x03, 0, 3, 0, 1}); err == nil {
		if _, err := io.ReadFull(probe, make([]byte, 7)); err == nil {
			t.Error("the device answered a second connection")
		}
	}
	waitFor(t, "the refusal is counted", func() bool { return run.Refused("CONV") >= 1 })
	time.Sleep(50 * time.Millisecond)
	waitFor(t, "the project's driver still reads the device", reads)
}

// #706 PR 4 against a device documented at one Modbus connection, its
// virtual twin enforcing it, the project's own driver holding that one
// connection: the default is a static check, which never connects, and a
// driver set to validate: live over it is refused at load, naming the
// device (its check's connection would be the controller's), as devbind
// refuses it on the row. Either way the device is never connected to.
func TestDevicesOneConnectionLiveCheck(t *testing.T) {
	dir := driveProject(t)
	pf := filepath.Join(dir, devprofile.ProjectFile("abb-acs355-fmbt21"))
	raw, err := os.ReadFile(pf)
	if err != nil {
		t.Fatal(err)
	}
	limited := strings.Replace(string(raw), "modbus:\n  port: 502\n", "modbus:\n  port: 502\n  max-connections: 1\n  max-connections-doc: \"fena §7.1\"\n", 1)
	if err := os.WriteFile(pf, []byte(limited), 0o644); err != nil {
		t.Fatal(err)
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
	listen := run.Endpoints[0].Listen
	manifest := filepath.Join(dir, project.ManifestName)
	orig, _ := os.ReadFile(manifest)
	onTwin := strings.Replace(string(orig), "endpoint: 10.0.0.11", "endpoint: \""+listen+"\"", 1)
	if err := os.WriteFile(manifest, []byte(onTwin), 0o644); err != nil {
		t.Fatal(err)
	}

	fsys := os.DirFS(dir)
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	drv := proj.Runtime.Driver.(interface {
		Start(context.Context)
		Stop()
	})
	drv.Start(ctx)
	defer drv.Stop()
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, "the project's driver on the device's one connection", func() bool {
		v, _ := rt.Tags().ReadPath("CONV__Online")
		return v == true
	})
	ioc, err := project.NewIOConfig(proj, rt, fsys, "")
	if err != nil {
		t.Fatal(err)
	}

	// The same device by another spelling of its address: a change, and
	// the device the project's driver already holds.
	host, port, _ := net.SplitHostPort(listen)
	if host != "127.0.0.1" {
		t.Skipf("virtual device on %s", listen)
	}
	respelled := strings.Replace(onTwin, "\""+listen+"\"", "\"localhost:"+port+"\"", 1)
	validate := func(m string) *project.IOValidation {
		return ioc.Validate(ioc.Stage(project.OverlayFS(fsys, map[string][]byte{project.ManifestName: []byte(m)}), "upload", "tester"))
	}

	v := validate(respelled)
	if !v.Valid || len(v.Probes) != 1 || v.Probes[0].Mode != devbind.ValidateStatic || !strings.Contains(v.Probes[0].Why, "max-connections 1") {
		t.Fatalf("the default: valid %v probes %+v checks %+v", v.Valid, v.Probes, v.Checks)
	}
	if n := run.Refused("CONV"); n != 0 {
		t.Fatalf("a static check connected: %d refused", n)
	}

	live := strings.Replace(respelled, "devices:", "drivers: [{ type: modbus, validate: live }]\ndevices:", 1)
	v = validate(live)
	var schema string
	for _, ck := range v.Checks {
		if ck.Name == "schema" {
			schema = ck.Detail
		}
	}
	if v.Valid || len(v.Probes) != 0 || !strings.Contains(schema, "device CONV's profile abb-acs355-fmbt21 documents max-connections 1") {
		t.Fatalf("live on the driver: valid %v schema %q probes %+v", v.Valid, schema, v.Probes)
	}
	if n := run.Refused("CONV"); n != 0 {
		t.Fatalf("the device refused %d connections: something connected to it", n)
	}
}
