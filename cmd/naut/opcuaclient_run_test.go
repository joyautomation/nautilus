package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gopcua "github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	uaserver "github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
)

// The OPC UA client end to end, through the real CLI binary, against
// gopcua's in-process server as the foreign server: `naut opcua browse`
// lists its variables, `naut opcua import` writes the manifest and the tag
// file (companion named after the project's driver), `naut check` passes,
// and `naut run` reads the server's values into the tag store, follows a
// change, and writes a changed output back — only to the binding the
// import was told is writable.
func TestNautOPCUAClientImportAndRun(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	srv := startForeignOPCUA(t)
	bin := buildNautCLI(t, t.TempDir(), nil)
	dir := t.TempDir()
	apiAddr := freeAddr(t)

	naut := func(args ...string) (string, string, int) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return stdout.String(), stderr.String(), code
	}

	// browse: every variable, typed, with its access.
	out, errOut, code := naut("opcua", "browse", "--endpoint", srv.url)
	if code != 0 {
		t.Fatalf("browse exited %d:\n%s", code, errOut)
	}
	for _, want := range []string{"urn:example:plant", "Plant/Level", "Double", "Plant/Setpoint", "rw", "Plant/Count", "Int32"} {
		if !strings.Contains(out, want) {
			t.Errorf("browse output lacks %q:\n%s", want, out)
		}
	}

	// import into a project whose nautilus.yaml already names the driver:
	// the companion in the tag file is that driver's.
	manifest := fmt.Sprintf(`name: plant
tasks:
  - program: main.st
    scan: 20ms
tag-files: [tags/opcua.yaml]
tags:
  - { name: Doubled, role: state, init: -1.0 }
  - { name: Seen, role: state, init: false }
server:
  addr: "%s"
driver:
  type: opcua
  name: gateway
  endpoint: %s
  manifest: opcua_manifest.yaml
  security: None
  scan-rate: 50ms
`, apiAddr, srv.url)
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, code = naut("opcua", "import", "--endpoint", srv.url, "--tags", "Plant_*", "--writable", "Plant_Setpoint")
	if code != 0 {
		t.Fatalf("import exited %d:\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "online tag: gateway__Online") {
		t.Errorf("import did not name the project's driver's companion:\n%s", out)
	}
	tagFile, err := os.ReadFile(filepath.Join(dir, "tags", "opcua.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: Plant_Level, role: input", "name: Plant_Count, role: input", "name: Plant_Setpoint, role: output", "name: gateway__Online, role: input"} {
		if !strings.Contains(string(tagFile), want) {
			t.Errorf("tags/opcua.yaml lacks %q:\n%s", want, tagFile)
		}
	}
	if strings.Contains(string(tagFile), "Plant_Level, role: output") {
		t.Errorf("a binding not named by --writable is an output:\n%s", tagFile)
	}
	first, _ := os.ReadFile(filepath.Join(dir, "opcua_manifest.yaml"))
	if _, _, code := naut("opcua", "import", "--endpoint", srv.url, "--tags", "Plant_*", "--writable", "Plant_Setpoint"); code != 0 {
		t.Fatal("re-import failed")
	}
	if again, _ := os.ReadFile(filepath.Join(dir, "opcua_manifest.yaml")); string(again) != string(first) {
		t.Errorf("re-import against an unchanged server is not byte-identical:\n%s\n---\n%s", first, again)
	}

	if err := os.WriteFile(filepath.Join(dir, "main.st"), []byte(`PROGRAM Main
VAR_EXTERNAL
  Plant_Level : LREAL; Plant_Setpoint : LREAL; gateway__Online : BOOL;
  Doubled : LREAL; Seen : BOOL;
END_VAR
  Doubled := Plant_Level * 2.0;
  Seen := gateway__Online;
  Plant_Setpoint := Plant_Level + 1.0;
END_PROGRAM
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, errOut, code := naut("check"); code != 0 {
		t.Fatalf("naut check exited %d:\n%s\n%s", code, out, errOut)
	}

	// run: values arrive, a change follows, the output goes back.
	cmd := exec.Command(bin, "run")
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NAUTILUS_ADDR=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-exited
	})
	state := func() map[string]any {
		resp, err := http.Get("http://" + apiAddr + "/api/state")
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		var f struct {
			Tags map[string]any `json:"tags"`
		}
		json.NewDecoder(resp.Body).Decode(&f)
		return f.Tags
	}
	waitTags := func(what string, cond func(map[string]any) bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			if tags := state(); tags != nil && cond(tags) {
				return
			}
			select {
			case <-exited:
				t.Fatalf("naut run exited; stderr:\n%s", stderr.String())
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; last state %v\nstderr:\n%s", what, state(), stderr.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitTags("the server's values", func(tags map[string]any) bool {
		return tags["Plant_Level"] == 12.5 && tags["Plant_Count"] == float64(7) && tags["gateway__Online"] == true &&
			tags["Doubled"] == 25.0 && tags["Seen"] == true
	})
	srv.set(t, "Level", 20.0)
	waitTags("a changed value", func(tags map[string]any) bool { return tags["Doubled"] == 40.0 })
	deadline := time.Now().Add(10 * time.Second)
	for srv.get(t, "Setpoint") != 21.0 {
		if time.Now().After(deadline) {
			t.Fatalf("the output never reached the server: Setpoint = %v\nstderr:\n%s", srv.get(t, "Setpoint"), stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The status row.
	resp, err := http.Get("http://" + apiAddr + "/api/drivers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rows []struct {
		Kind  string `json:"kind"`
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Kind != "opcua" || rows[0].Name != "gateway" || rows[0].State != "connected" {
		t.Errorf("/api/drivers = %+v, want one connected opcua row named gateway", rows)
	}
}

// foreignOPCUA is gopcua's server with a small plant: the implementation the
// client is tested against, one it did not write.
type foreignOPCUA struct {
	url  string
	vars map[string]*uaserver.Node
	c    *gopcua.Client
}

func startForeignOPCUA(t *testing.T) *foreignOPCUA {
	t.Helper()
	host, port := splitAddr(t, freeAddr(t))
	srv := uaserver.New(
		uaserver.EnableSecurity("None", ua.MessageSecurityModeNone),
		uaserver.EnableAuthMode(ua.UserTokenTypeAnonymous),
		uaserver.EndPoint(host, port),
	)
	ns := uaserver.NewNodeNameSpace(srv, "urn:example:plant")
	plant := ns.AddNode(uaserver.NewFolderNode(ua.NewStringNodeID(ns.ID(), "Plant"), "Plant"))
	srv.Node(uaserver.ObjectsFolder).AddRef(plant, id.Organizes, true)
	f := &foreignOPCUA{url: fmt.Sprintf("opc.tcp://%s:%d", host, port), vars: map[string]*uaserver.Node{}}
	add := func(name string, v any, typ uint32, access uint8) {
		n := ns.AddNewVariableStringNode(name, v)
		_ = n.SetAttribute(ua.AttributeIDDataType, uaserver.DataValueFromValue(ua.NewNumericNodeID(0, typ)))
		_ = n.SetAttribute(ua.AttributeIDAccessLevel, uaserver.DataValueFromValue(access))
		plant.AddRef(n, id.HasComponent, true)
		f.vars[name] = n
	}
	const rd, rw = uint8(ua.AccessLevelTypeCurrentRead), uint8(ua.AccessLevelTypeCurrentRead | ua.AccessLevelTypeCurrentWrite)
	add("Level", 12.5, id.Double, rw)
	add("Count", int32(7), id.Int32, rd)
	add("Setpoint", 0.0, id.Double, rw)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// The fixture reads and writes through a session of its own: gopcua's
	// nodes are unsynchronized, and its server answers every service on one
	// goroutine, so a request is the only race-free way in.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := gopcua.NewClient(f.url, gopcua.SecurityMode(ua.MessageSecurityModeNone), gopcua.AuthAnonymous(), gopcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	f.c = c
	return f
}

func (f *foreignOPCUA) set(t *testing.T, name string, v any) {
	t.Helper()
	resp, err := f.c.Write(context.Background(), &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{
		NodeID: f.vars[name].ID(), AttributeID: ua.AttributeIDValue,
		Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(v)},
	}}})
	if err != nil || resp.Results[0] != ua.StatusOK {
		t.Fatalf("fixture write %s: %v %v", name, err, resp)
	}
}

func (f *foreignOPCUA) get(t *testing.T, name string) any {
	t.Helper()
	resp, err := f.c.Read(context.Background(), &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{
		NodeID: f.vars[name].ID(), AttributeID: ua.AttributeIDValue}}})
	if err != nil || resp.Results[0].Value == nil {
		t.Fatalf("fixture read %s: %v", name, err)
	}
	return resp.Results[0].Value.Value()
}

func splitAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	i := strings.LastIndex(addr, ":")
	var port int
	if _, err := fmt.Sscanf(addr[i+1:], "%d", &port); err != nil {
		t.Fatal(err)
	}
	return addr[:i], port
}
