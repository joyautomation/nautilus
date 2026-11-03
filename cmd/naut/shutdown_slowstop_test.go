package main

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

// swallowDeath is a broker hook that never acknowledges an NDEATH: the
// node's Stop waits out its publish timeout, so shutdown has a slow stopper.
// born closes on the first NBIRTH, so the test knows a death will be sent.
type swallowDeath struct {
	mqttsrv.HookBase
	once sync.Once
	born chan struct{}
}

func (h *swallowDeath) ID() string { return "swallow-death" }

func (h *swallowDeath) Provides(b byte) bool { return b == mqttsrv.OnPublish }

func (h *swallowDeath) OnPublish(_ *mqttsrv.Client, pk packets.Packet) (packets.Packet, error) {
	switch {
	case strings.Contains(pk.TopicName, "/NBIRTH/"):
		h.once.Do(func() { close(h.born) })
	case strings.Contains(pk.TopicName, "/NDEATH/"):
		return pk, packets.ErrRejectPacket // dropped: no PUBACK
	}
	return pk, nil
}

// SIGTERM to `naut run` gives the HTTP port back before the slow stops run,
// not when the process exits (nautilus#483 item 1, the listener-first order
// from #475). The Sparkplug node here waits about 10 s for a PUBACK on its
// death certificate that never comes, and the port must be free well inside
// that window. TestNautRunShutdownWithOPCUASessionIsPrompt cannot see the
// order: its process exits fast enough that the port frees either way.
func TestNautRunGivesBackTheHTTPPortBeforeSlowStops(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	if goruntime.GOOS == "windows" {
		t.Skip("SIGTERM")
	}

	hook := &swallowDeath{born: make(chan struct{})}
	broker := mqttsrv.New(&mqttsrv.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := broker.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	if err := broker.AddHook(hook, nil); err != nil {
		t.Fatal(err)
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "t", Address: "127.0.0.1:0"})
	if err := broker.AddListener(tcp); err != nil {
		t.Fatal(err)
	}
	go func() { _ = broker.Serve() }()

	bin := buildNautCLI(t, t.TempDir(), nil)
	dir := t.TempDir()
	apiAddr := freeAddr(t)
	files := map[string]string{
		"nautilus.yaml": `name: slowstop
tasks:
  - program: main.st
    scan: 20ms
tags:
  - { name: Count, role: state, init: 0 }
server:
  addr: "` + apiAddr + `"
sparkplug:
  broker: tcp://` + tcp.Address() + `
  group-id: Plant
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
	cmd.Stdout = new(bytes.Buffer)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-exited
		broker.Listeners.Close("t", func(string) {})
		for _, cl := range broker.Clients.GetAll() {
			if !cl.Closed() {
				_ = broker.DisconnectClient(cl, packets.ErrServerShuttingDown)
			}
		}
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
	select {
	case <-hook.born:
	case <-time.After(15 * time.Second):
		t.Fatalf("the node never birthed; stderr:\n%s", stderr.String())
	}

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	for {
		if ln, err := net.Listen("tcp", apiAddr); err == nil {
			ln.Close()
			break
		}
		select {
		case <-exited:
			t.Fatalf("the HTTP port stayed bound until naut run exited (%v after SIGTERM)", time.Since(start).Round(time.Millisecond))
		default:
		}
		if time.Since(start) > 3*time.Second {
			t.Fatalf("the HTTP port %s was still bound 3 s after SIGTERM", apiAddr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	freed := time.Since(start)

	// The stopper must really have been slow, or this test proves nothing:
	// the process is still running when the port is free.
	select {
	case <-exited:
		t.Fatalf("naut run exited %v after SIGTERM: the slow stopper did not hold, so the port order went untested", time.Since(start).Round(time.Millisecond))
	default:
	}
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatalf("naut run did not exit within 30 s of SIGTERM; stderr:\n%s", stderr.String())
	}
	took := time.Since(start)
	t.Logf("port free %v after SIGTERM; process exited after %v", freed.Round(time.Millisecond), took.Round(time.Millisecond))
	if took < 2*time.Second {
		t.Errorf("naut run exited %v after SIGTERM; the swallowed NDEATH should have held it for about 10 s", took.Round(time.Millisecond))
	}
}
