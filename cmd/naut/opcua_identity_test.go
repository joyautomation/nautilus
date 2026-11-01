package main

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	opcuaserver "github.com/joyautomation/nautilus/opcua/server"
)

// `naut run`'s OPC UA server reports the CLI's own version, the one
// `naut version` prints, as ServerStatus.BuildInfo.SoftwareVersion, and
// advertises the project's ApplicationUri (nautilus#424, black-box F-04).
func TestNautRunOPCUAReportsTheCLIVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	// Stamped the way a release stamps it (.goreleaser.yaml), so the
	// version is the CLI's and not one the build info happens to share.
	bin := buildNautCLI(t, t.TempDir(), nil,
		"-ldflags", "-X github.com/joyautomation/nautilus/internal/lsp.Version=9.8.7")
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "nautilus"))
	if version != "9.8.7" {
		t.Fatalf("naut version printed %q", out)
	}

	dir := t.TempDir()
	apiAddr, opcAddr := freeAddr(t), freeAddr(t)
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(`name: ident
tasks:
  - program: main.st
tags:
  - { name: Level, role: state, init: 1.0 }
server:
  addr: "`+apiAddr+`"
opcua:
  endpoint: "opc.tcp://`+opcAddr+`"
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
	// A file, not a strings.Builder: the test reads it while the process
	// runs, and exec would fill a Builder from its own goroutine.
	errFile, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer errFile.Close()
	cmd.Stderr = errFile
	stderr := func() string { b, _ := os.ReadFile(errFile.Name()); return string(b) }
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
	for {
		if resp, err := http.Get("http://" + apiAddr + "/api/state"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		select {
		case <-exited:
			t.Fatalf("naut run exited before serving; stderr:\n%s", stderr())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("naut run never answered; stderr:\n%s", stderr())
		}
		time.Sleep(50 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	eps, err := opcua.GetEndpoints(ctx, "opc.tcp://"+opcAddr)
	if err != nil {
		t.Fatal(err)
	}
	want := opcuaserver.ApplicationURI("ident")
	for _, ep := range eps {
		if ep.Server.ApplicationURI != want {
			t.Errorf("endpoint %s ApplicationUri = %q, want %q", ep.EndpointURL, ep.Server.ApplicationURI, want)
		}
	}
	c, err := opcua.NewClient("opc.tcp://"+opcAddr, opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer ccancel()
		c.Close(cctx)
	}()
	resp, err := c.Read(ctx, &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{
		NodeID: ua.NewNumericNodeID(0, id.Server_ServerStatus_BuildInfo_SoftwareVersion), AttributeID: ua.AttributeIDValue}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Results[0].Value.Value(); got != version {
		t.Errorf("BuildInfo.SoftwareVersion = %v, want %q (naut version)", got, version)
	}
}
