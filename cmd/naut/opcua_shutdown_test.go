package main

import (
	"context"
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
	"github.com/gopcua/opcua/ua"
)

// SIGTERM to `naut run` with an OPC UA client's session open (and one
// subscribed) gives the HTTP port back at once and exits promptly, so
// Ctrl+C and `naut run` again with a SCADA client attached gets a whole
// controller: dashboard, tag API and all (nautilus#423, black-box F-14).
// The OPC UA server's Close used to wait out a 10 s timeout whenever a
// session was open, and the HTTP listener stayed bound until the process
// exited, so the second start ran without its tag API.
func TestNautRunShutdownWithOPCUASessionIsPrompt(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	if goruntime.GOOS == "windows" {
		t.Skip("SIGTERM")
	}
	bin := buildNautCLI(t, t.TempDir(), nil)
	for _, subscribe := range []bool{false, true} {
		name := "session"
		if subscribe {
			name = "subscription"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			apiAddr, opcAddr := freeAddr(t), freeAddr(t)
			files := map[string]string{
				"nautilus.yaml": `name: stopper
tasks:
  - program: main.st
    scan: 20ms
tags:
  - { name: Count, role: state, init: 0 }
server:
  addr: "` + apiAddr + `"
opcua:
  endpoint: "opc.tcp://` + opcAddr + `"
`,
				"main.st": "PROGRAM Main\nVAR_EXTERNAL\n Count : DINT;\nEND_VAR\n Count := Count + 1;\nEND_PROGRAM\n",
			}
			for n, body := range files {
				if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
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
			var stderr lockedBuffer
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
			for {
				if resp, err := http.Get("http://" + apiAddr + "/api/state"); err == nil {
					resp.Body.Close()
					if resp.StatusCode == 200 {
						break
					}
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

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			c, err := opcua.NewClient("opc.tcp://"+opcAddr, opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Connect(ctx); err != nil {
				t.Fatalf("connect: %v\nstderr:\n%s", err, stderr.String())
			}
			defer func() {
				cctx, ccancel := context.WithTimeout(context.Background(), time.Second)
				defer ccancel()
				c.Close(cctx)
			}()
			if subscribe {
				notes := make(chan *opcua.PublishNotificationData, 16)
				sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 100 * time.Millisecond}, notes)
				if err != nil {
					t.Fatal(err)
				}
				// ns=1 is the project's namespace: the only one this server adds.
				if _, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, opcua.NewMonitoredItemCreateRequestWithDefaults(
					ua.NewStringNodeID(1, "Count"), ua.AttributeIDValue, 1)); err != nil {
					t.Fatal(err)
				}
				select {
				case <-notes:
				case <-time.After(5 * time.Second):
					t.Fatal("no data change notification before the shutdown")
				}
			}

			start := time.Now()
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			// The HTTP port takes a new bind within a second: what a second
			// `naut run` started right away needs.
			var freed time.Duration
			for {
				if ln, err := net.Listen("tcp", apiAddr); err == nil {
					freed = time.Since(start)
					ln.Close()
					break
				}
				if time.Since(start) > 5*time.Second {
					t.Fatalf("the HTTP port %s was still bound 5 s after SIGTERM", apiAddr)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if freed > time.Second {
				t.Errorf("the HTTP port came free %v after SIGTERM; want under 1 s", freed.Round(time.Millisecond))
			}
			select {
			case <-exited:
			case <-time.After(15 * time.Second):
				t.Fatalf("naut run did not exit within 15 s of SIGTERM; stderr:\n%s", stderr.String())
			}
			if took := time.Since(start); took > 2*time.Second {
				t.Errorf("naut run took %v to exit after SIGTERM with an OPC UA %s open; want under 2 s", took.Round(time.Millisecond), name)
			}
			if code := cmd.ProcessState.ExitCode(); code != 0 {
				t.Errorf("naut run exited %d; stderr:\n%s", code, stderr.String())
			}
		})
	}
}
