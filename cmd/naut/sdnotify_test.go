package main

import (
	"bytes"
	"errors"
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
)

// The heartbeat follows the last liveness verdict, and only a recent one.
func TestLiveWatch(t *testing.T) {
	w := &liveWatch{stale: 8 * time.Second}
	t0 := time.Unix(1000, 0)
	if ok, why := w.live(t0); ok || !strings.Contains(why, "no liveness check") {
		t.Fatalf("before any check: %v %q, want not live", ok, why)
	}
	var verdict error
	h := w.wrap(func(time.Time, time.Duration) error { return verdict })
	h(t0, time.Second)
	if ok, _ := w.live(t0.Add(time.Second)); !ok {
		t.Fatal("a live verdict a second ago must read live")
	}
	if ok, why := w.live(t0.Add(9 * time.Second)); ok || !strings.Contains(why, "no liveness check for 9s") {
		t.Fatalf("a verdict older than stale: %v %q, want not live", ok, why)
	}
	verdict = errors.New("runtime: scans are not completing: task main")
	h(t0.Add(10*time.Second), time.Second)
	if ok, why := w.live(t0.Add(10 * time.Second)); ok || why != verdict.Error() {
		t.Fatalf("a stuck verdict: %v %q, want not live with its reason", ok, why)
	}
}

// notifySocket is a stand-in for systemd's notification socket: it keeps
// every datagram naut sends, with when it came.
type notifySocket struct {
	path string
	mu   sync.Mutex
	msgs []notifyMsg
}

type notifyMsg struct {
	at   time.Time
	text string
}

func newNotifySocket(t *testing.T) *notifySocket {
	t.Helper()
	// A short directory: a unix socket path has a ~108-byte limit, and a
	// test's TempDir can exceed it.
	dir, err := os.MkdirTemp("", "sdn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s := &notifySocket{path: filepath.Join(dir, "notify")}
	c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: s.path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			s.mu.Lock()
			s.msgs = append(s.msgs, notifyMsg{time.Now(), string(buf[:n])})
			s.mu.Unlock()
		}
	}()
	return s
}

func (s *notifySocket) all() []notifyMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]notifyMsg(nil), s.msgs...)
}

// wait waits for a message containing want and returns when it came.
func (s *notifySocket) wait(t *testing.T, want string, within time.Duration) time.Time {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, m := range s.all() {
			if strings.Contains(m.text, want) {
				return m.at
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no %q within %v; got %v", want, within, s.all())
	return time.Time{}
}

func (s *notifySocket) count(want string, after time.Time) int {
	n := 0
	for _, m := range s.all() {
		if m.at.After(after) && strings.Contains(m.text, want) {
			n++
		}
	}
	return n
}

// naut run under a Type=notify unit with WatchdogSec=: READY=1 after the
// first scan, a heartbeat while the scans are live, none once a scan
// hangs (so systemd would kill and restart it), and STOPPING=1 on an
// orderly stop. The hang is real: a nested loop the program enters when
// the Hang setpoint is written, which never finishes in test time.
func TestNautRunSystemdNotify(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	if goruntime.GOOS != "linux" {
		t.Skip("systemd")
	}
	bin := buildNautCLI(t, t.TempDir(), nil)

	start := func(t *testing.T, sock *notifySocket) (*exec.Cmd, string, chan struct{}) {
		dir := t.TempDir()
		addr := "127.0.0.1:0" // the banner names the port (nautrun_addr_test.go)
		files := map[string]string{
			"nautilus.yaml": `name: sdnotify
tasks:
  - program: main.st
    scan: 10ms
tags:
  - { name: Hang, role: setpoint, init: false }
  - { name: Count, role: state, init: 0 }
server:
  addr: "` + addr + `"
`,
			"main.st": `PROGRAM Main
VAR_EXTERNAL
  Hang : BOOL;
  Count : DINT;
END_VAR
VAR
  i, j : DINT;
END_VAR
Count := Count + 1;
IF Hang THEN
  FOR i := 1 TO 1000000 DO
    FOR j := 1 TO 1000000 DO
      Count := Count + 1;
    END_FOR;
  END_FOR;
END_IF;
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
			if !strings.HasPrefix(kv, "NAUTILUS_ADDR=") && !strings.HasPrefix(kv, "NOTIFY_SOCKET=") && !strings.HasPrefix(kv, "WATCHDOG_") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		// WatchdogSec=1s: a heartbeat every 500 ms. Shorter than
		// minWatchdog, so naut warns; it keeps the test short.
		cmd.Env = append(cmd.Env, "NOTIFY_SOCKET="+sock.path, "WATCHDOG_USEC=1000000")
		stdout, stderr := &lockedBuffer{}, &lockedBuffer{}
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		t.Cleanup(func() {
			cmd.Process.Kill()
			<-exited
			if t.Failed() {
				t.Logf("stderr:\n%s", stderr.String())
			}
		})
		addr = apiAddrFromBanner(t, stdout.String, exited, stderr.String)
		return cmd, addr, exited
	}

	t.Run("ready, heartbeat, stopping", func(t *testing.T) {
		sock := newNotifySocket(t)
		cmd, _, exited := start(t, sock)
		ready := sock.wait(t, "READY=1", 30*time.Second)
		sock.wait(t, "WATCHDOG=1", 3*time.Second)
		time.Sleep(2 * time.Second)
		if n := sock.count("WATCHDOG=1", ready); n < 3 {
			t.Fatalf("%d heartbeats in the 2 s after READY at 500 ms intervals, want at least 3", n)
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			t.Fatal("naut run did not exit on SIGTERM")
		}
		sock.wait(t, "STOPPING=1", time.Second)
	})

	t.Run("a hung scan withholds the heartbeat", func(t *testing.T) {
		sock := newNotifySocket(t)
		_, addr, _ := start(t, sock)
		sock.wait(t, "READY=1", 30*time.Second)
		sock.wait(t, "WATCHDOG=1", 3*time.Second)
		res, err := http.Post("http://"+addr+"/api/tags", "application/json",
			bytes.NewBufferString(`{"name": "Hang", "value": true}`))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode/100 != 2 {
			t.Fatalf("write Hang: %s", res.Status)
		}
		hung := time.Now()
		// The stall is found a lease (4 s) or so after the scan began.
		quiet := sock.wait(t, "STATUS=scans not live", 15*time.Second)
		t.Logf("heartbeat withheld %v after the hang began", quiet.Sub(hung).Round(time.Millisecond))
		time.Sleep(2 * time.Second)
		if n := sock.count("WATCHDOG=1", quiet); n != 0 {
			t.Fatalf("%d heartbeats after the scans stopped being live, want none", n)
		}
	})
}
