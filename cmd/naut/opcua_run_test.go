package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// `naut run` with an opcua: section serves the project over OPC UA beside
// the tag API: the real CLI binary, a real gopcua client browsing Objects
// → project → tags and reading a value, the opcua-server row on
// /api/drivers, the banner naming the endpoint, and a clean SIGTERM exit
// with the server's teardown on the way out.
func TestNautRunServesOPCUA(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	bin := buildNautCLI(t, t.TempDir(), nil)
	dir := t.TempDir()
	apiAddr, opcAddr := freeAddr(t), freeAddr(t)
	files := map[string]string{
		"nautilus.yaml": `name: wetwell
tasks:
  - program: main.st
    scan: 20ms
tags:
  - { name: Level, role: state, init: 3.5, desc: "Wet well level", unit: ft }
  - { name: Count, role: state, init: 0 }
  - { name: P101, role: state, type: Pump }
server:
  addr: "` + apiAddr + `"
opcua:
  endpoint: "opc.tcp://` + opcAddr + `"
`,
		"types.st": `TYPE Pump :
  STRUCT
    Running : BOOL;
    Speed   : REAL;
  END_STRUCT
END_TYPE
`,
		"main.st": `PROGRAM Main
VAR_EXTERNAL
  Level : REAL;
  Count : DINT;
  P101  : Pump;
END_VAR
  Count := Count + 1;
  P101.Speed := Level * 10.0;
END_PROGRAM
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command(bin, "run")
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NAUTILUS_ADDR=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-exited
	})

	get := func(path string) (int, string, bool) {
		resp, err := http.Get("http://" + apiAddr + path)
		if err != nil {
			return 0, "", false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), true
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if code, _, ok := get("/api/state"); ok && code == 200 {
			break
		}
		select {
		case <-exited:
			t.Fatalf("naut run exited before serving; stderr:\n%s", stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("naut run never answered on %s; stderr:\n%s", apiAddr, stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The OPC UA endpoint answers a real client.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := opcua.NewClient("opc.tcp://"+opcAddr, opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("connect to naut run's OPC UA endpoint %s: %v\nstderr:\n%s", opcAddr, err, stderr.String())
	}
	defer func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer ccancel()
		c.Close(cctx)
	}()
	browse := func(nid *ua.NodeID) map[string]*ua.ReferenceDescription {
		resp, err := c.Browse(ctx, &ua.BrowseRequest{NodesToBrowse: []*ua.BrowseDescription{{
			NodeID: nid, BrowseDirection: ua.BrowseDirectionForward,
			ReferenceTypeID: ua.NewNumericNodeID(0, id.HierarchicalReferences), IncludeSubtypes: true,
			NodeClassMask: uint32(ua.NodeClassAll), ResultMask: uint32(ua.BrowseResultMaskAll)}}})
		if err != nil {
			t.Fatalf("browse %s: %v", nid, err)
		}
		out := map[string]*ua.ReferenceDescription{}
		for _, r := range resp.Results[0].References {
			out[r.BrowseName.Name] = r
		}
		return out
	}
	objects := browse(ua.NewNumericNodeID(0, id.ObjectsFolder))
	folder, ok := objects["wetwell"]
	if !ok {
		t.Fatalf("Objects has no wetwell folder: %v", names(objects))
	}
	tags := browse(folder.NodeID.NodeID)
	for _, want := range []string{"Level", "Count", "P101"} {
		if tags[want] == nil {
			t.Errorf("folder lacks %s: %v", want, names(tags))
		}
	}
	ns := folder.NodeID.NodeID.Namespace()
	read := func(addr string, attr ua.AttributeID) *ua.DataValue {
		resp, err := c.Read(ctx, &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{NodeID: ua.NewStringNodeID(ns, addr), AttributeID: attr}}, TimestampsToReturn: ua.TimestampsToReturnBoth})
		if err != nil {
			t.Fatalf("read %s: %v", addr, err)
		}
		return resp.Results[0]
	}
	if dv := read("Level", ua.AttributeIDValue); dv.Status != ua.StatusGood || dv.Value.Value() != 3.5 {
		t.Errorf("Level = %v %s", dv.Value, dv.Status)
	}
	if desc := read("Level", ua.AttributeIDDescription).Value.Value().(*ua.LocalizedText).Text; desc != "Wet well level [ft]" {
		t.Errorf("Level description %q", desc)
	}
	// The program is running: the member it computes has the value, and
	// the counter it increments moves between two reads.
	if dv := read("P101.Speed", ua.AttributeIDValue); dv.Value.Value() != 35.0 {
		t.Errorf("P101.Speed = %v", dv.Value.Value())
	}
	first := read("Count", ua.AttributeIDValue).Value.Value().(int32)
	time.Sleep(100 * time.Millisecond)
	if again := read("Count", ua.AttributeIDValue).Value.Value().(int32); again <= first {
		t.Errorf("Count did not move: %d → %d", first, again)
	}
	// Writes are refused.
	wr, err := c.Write(ctx, &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{NodeID: ua.NewStringNodeID(ns, "Level"), AttributeID: ua.AttributeIDValue,
		Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(99.0)}}}})
	if err != nil {
		t.Fatal(err)
	}
	if wr.Results[0] != ua.StatusBadUserAccessDenied {
		t.Errorf("write: %s", wr.Results[0])
	}

	// The driver-status feed carries the server's row.
	_, body, _ := get("/api/drivers")
	var rows []struct {
		Kind   string `json:"kind"`
		State  string `json:"state"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("/api/drivers: %v\n%s", err, body)
	}
	found := false
	for _, r := range rows {
		if r.Kind == "opcua-server" {
			found = true
			if r.State != "connected" || r.Detail != "opc.tcp://"+opcAddr {
				t.Errorf("opcua-server row %+v", r)
			}
		}
	}
	if !found {
		t.Errorf("/api/drivers has no opcua-server row: %s", body)
	}

	if goruntime.GOOS == "windows" {
		return
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(15 * time.Second):
		t.Fatalf("naut run did not exit within 15 s of SIGTERM; stderr:\n%s", stderr.String())
	}
	if c := cmd.ProcessState.ExitCode(); c != 0 {
		t.Errorf("naut run exited %d after SIGTERM; stderr:\n%s", c, stderr.String())
	}
	if !strings.Contains(stdout.String(), "opc ua on opc.tcp://"+opcAddr) {
		t.Errorf("banner does not name the endpoint:\n%s", stdout.String())
	}
	if strings.Contains(stderr.String(), "New Node Set") || strings.Contains(stderr.String(), "Started listening") {
		t.Errorf("gopcua's startup chatter reached stderr:\n%s", stderr.String())
	}
}

// `naut run` with an opcua: endpoint already in use keeps running without
// it, says so on stderr, and reports the error on /api/drivers.
func TestNautRunContinuesWhenOPCUAPortIsTaken(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	bin := buildNautCLI(t, t.TempDir(), nil)
	dir := t.TempDir()
	apiAddr := freeAddr(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	taken := l.Addr().String()
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(`tasks:
  - program: main.st
tags:
  - { name: Level, role: state, init: 1.0 }
server:
  addr: "`+apiAddr+`"
opcua:
  endpoint: "`+taken+`"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.st"), []byte("PROGRAM Main\nVAR_EXTERNAL\n Level : REAL;\nEND_VAR\nEND_PROGRAM\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "run")
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NAUTILUS_ADDR=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-exited
	})
	deadline := time.Now().Add(30 * time.Second)
	var body string
	for {
		resp, err := http.Get("http://" + apiAddr + "/api/drivers")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				body = string(b)
				break
			}
		}
		select {
		case <-exited:
			t.Fatalf("naut run exited; stderr:\n%s", stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("naut run never answered; stderr:\n%s", stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	var rows []struct {
		Kind      string `json:"kind"`
		State     string `json:"state"`
		LastError string `json:"lastError"`
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("/api/drivers: %v\n%s", err, body)
	}
	found := false
	for _, r := range rows {
		if r.Kind == "opcua-server" {
			found = true
			if r.State != "error" || r.LastError == "" {
				t.Errorf("opcua-server row %+v", r)
			}
		}
	}
	if !found {
		t.Errorf("no opcua-server row: %s", body)
	}
	if !strings.Contains(stderr.String(), "opcua:") || !strings.Contains(stderr.String(), "continuing without it") {
		t.Errorf("stderr does not report the failed listen:\n%s", stderr.String())
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func names(m map[string]*ua.ReferenceDescription) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
