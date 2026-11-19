package main

// #968: a built controller run by hand next to its service (to read its
// version, say) must not start a second instance against the same I/O.
// This builds the real CLI, has it build a controller whose Modbus driver
// polls a fake device that counts connections, and runs that controller
// the ways a person would.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countingDevice accepts TCP connections and counts them: a Modbus device
// as far as "did the controller connect to its I/O" goes.
func countingDevice(t *testing.T) (port int, conns *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	conns = &atomic.Int32{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			defer c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, conns
}

func TestBuiltControllerNeverStartsASecondInstance(t *testing.T) {
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
	port, conns := countingDevice(t)
	proj := filepath.Join(dir, "proj")
	files := map[string]string{
		"nautilus.yaml": `name: single
tasks:
  - program: main.st
    scan: 20ms
tags:
  - { name: Level, role: input, type: INT }
driver:
  type: modbus
  manifest: modbus.yaml
  scan-rate: 20ms
`,
		"modbus.yaml": fmt.Sprintf(`sources:
  - {id: DEV, host: 127.0.0.1, port: %d, unitid: 1, timeout: 200ms, retrymin: 20ms, retrymax: 50ms}
tags:
  - {name: Level, source: DEV, table: holding, address: 0, format: int16}
`, port),
		"main.st": "PROGRAM Main\nVAR_EXTERNAL Level : INT; END_VAR\nEND_PROGRAM\n",
	}
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(proj, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctl := filepath.Join(dir, "controller")
	if out, err := exec.Command(naut, "build", "-o", ctl, proj).CombinedOutput(); err != nil {
		t.Fatalf("naut build: %v\n%s", err, out)
	}

	// run runs the controller with args, giving up after 10 s: a controller
	// that started its scan never exits by itself.
	run := func(addr string, args ...string) (int, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, ctl, args...)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=", "HOME=" + dir, "NAUTILUS_ADDR=" + addr}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		if ctx.Err() != nil {
			return -1, out.String() + "\n(still running after 10 s: it started)"
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), out.String()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, out.String()
	}
	freeAddr := func() string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		return ln.Addr().String()
	}
	noIO := func(what string) {
		t.Helper()
		time.Sleep(300 * time.Millisecond) // a started driver would have dialed by now
		if n := conns.Load(); n != 0 {
			t.Fatalf("%s: the controller connected to its I/O (%d connections)", what, n)
		}
	}

	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"version"}, 0, "nautilus "},
		{[]string{"--version"}, 0, "nautilus "},
		{[]string{"-v"}, 0, "nautilus "},
		{[]string{"--help"}, 0, "usage: controller"},
		{[]string{"-h"}, 0, "usage: controller"},
		{[]string{"status"}, 2, `unknown argument "status"`},
	} {
		code, out := run(freeAddr(), tc.args...)
		if code != tc.code || !strings.Contains(out, tc.want) {
			t.Fatalf("controller %s: exit %d, output %q; want exit %d and %q", strings.Join(tc.args, " "), code, out, tc.code, tc.want)
		}
		noIO("controller " + strings.Join(tc.args, " "))
	}

	// Another process holds the tag API's address (the running service):
	// a second instance refuses to start, before any driver connects.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	code, out := run(held.Addr().String())
	if code <= 0 || !strings.Contains(out, "in use") {
		t.Fatalf("a second instance: exit %d, output %q; want a refusal naming the address in use", code, out)
	}
	noIO("a second instance")

	// With no fixed address held, nothing would exclude a second instance:
	// a built controller refuses port 0 and an address it cannot bind.
	for _, addr := range []string{"127.0.0.1:0", "192.0.2.1:18090"} { // 192.0.2.0/24: TEST-NET, on no host
		code, out := run(addr)
		if code <= 0 || !strings.Contains(out, "fixed address") {
			t.Fatalf("tag api %s: exit %d, output %q; want a refusal", addr, code, out)
		}
		noIO("tag api " + addr)
	}
}
