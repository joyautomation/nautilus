package main

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #811 end to end, on `naut run`: with no NAUTILUS_TOKEN a browser write
// addressed to a rebindable name is refused, one to localhost is
// accepted, and listening beyond loopback says so loudly at startup.
func TestNautRunRefusesARebindableHostAndWarnsWhenOpen(t *testing.T) {
	bin := buildNautCLI(t, t.TempDir(), nil)
	// start runs naut on addr (port 0: the banner names the port it bound,
	// nautrun_addr_test.go) and returns its stderr, a stop, and that port.
	start := func(addr string, env ...string) (*lockedBuffer, func(), string) {
		dir := t.TempDir()
		files := map[string]string{
			"nautilus.yaml": "name: rebind\ntasks:\n  - program: main.st\n    scan: 20ms\ntags:\n  - { name: SP, role: setpoint, init: 0 }\nserver:\n  addr: \"" + addr + "\"\n",
			"main.st":       "PROGRAM Main\nVAR_EXTERNAL\n SP : DINT;\nEND_VAR\nEND_PROGRAM\n",
		}
		for n, body := range files {
			if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command(bin, "run")
		cmd.Dir = dir
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "NAUTILUS_ADDR=") && !strings.HasPrefix(kv, "NAUTILUS_TOKEN=") && !strings.HasPrefix(kv, "NAUTILUS_ALLOWED_HOSTS=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, env...)
		stderr := &lockedBuffer{}
		stdout := &lockedBuffer{}
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		_, port, _ := net.SplitHostPort(apiAddrFromBanner(t, stdout.String, exited, stderr.String))
		deadline := time.Now().Add(30 * time.Second)
		for {
			if resp, err := http.Get("http://127.0.0.1:" + port + "/api/state"); err == nil {
				resp.Body.Close()
				break
			}
			select {
			case <-exited:
				t.Fatalf("naut run exited before serving; stderr:\n%s", stderr.String())
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("naut run never served; stderr:\n%s", stderr.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
		return stderr, func() { cmd.Process.Kill(); <-exited }, port
	}
	post := func(port, host, origin string) int {
		req, _ := http.NewRequest("POST", "http://127.0.0.1:"+port+"/api/tags", strings.NewReader(`{"name":"SP","value":42}`))
		req.Host = host
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// Loopback: quiet, and a rebound page cannot write.
	stderr, stop, port := start("127.0.0.1:0")
	if got := post(port, "evil.example:"+port, "http://evil.example:"+port); got != http.StatusForbidden {
		t.Errorf("rebound Host write: %d, want 403", got)
	}
	if got := post(port, "localhost:"+port, "http://localhost:"+port); got != http.StatusNoContent {
		t.Errorf("localhost write: %d, want 204", got)
	}
	stop()
	if strings.Contains(stderr.String(), "WARNING: the tag API listens on") {
		t.Errorf("loopback listen warned:\n%s", stderr.String())
	}

	// A name the operator lists in NAUTILUS_ALLOWED_HOSTS writes; another
	// name still does not.
	_, stop, port3 := start("127.0.0.1:0", "NAUTILUS_ALLOWED_HOSTS=epic1.plant.lan")
	if got := post(port3, "epic1.plant.lan:"+port3, "http://epic1.plant.lan:"+port3); got != http.StatusNoContent {
		t.Errorf("allowed name write: %d, want 204", got)
	}
	if got := post(port3, "other.plant.lan:"+port3, "http://other.plant.lan:"+port3); got != http.StatusForbidden {
		t.Errorf("unlisted name write: %d, want 403", got)
	}
	stop()

	// Every interface, no token: it still starts (deployed controllers do
	// this), and says what that exposes.
	stderr, stop, port2 := start("0.0.0.0:0")
	stop()
	if !strings.Contains(stderr.String(), "WARNING: the tag API listens on 0.0.0.0:"+port2+" with no NAUTILUS_TOKEN") {
		t.Errorf("an open listen without a token did not warn:\n%s", stderr.String())
	}
}

// #811: the listen policy's pieces.
func TestLoopbackListenAndAllowedHosts(t *testing.T) {
	for addr, want := range map[string]bool{
		"localhost:8080": true, "127.0.0.1:8080": true, "127.9.9.9:1": true, "[::1]:8080": true,
		":8080": false, "0.0.0.0:8080": false, "192.168.1.5:8080": false, "plc.local:8080": false,
	} {
		if got := loopbackListen(addr); got != want {
			t.Errorf("loopbackListen(%q) = %v, want %v", addr, got, want)
		}
	}
	t.Setenv("NAUTILUS_ALLOWED_HOSTS", " epic1.plant.lan, ,hmi.local ")
	if got := strings.Join(allowedHosts(), "|"); got != "epic1.plant.lan|hmi.local" {
		t.Errorf("allowedHosts() = %q", got)
	}
}

// #811: `naut logix serve` writes to a running PLC, so beyond loopback it
// needs a token, and it refuses to start without one.
func TestLogixServeRefusesAnOpenListenWithoutAToken(t *testing.T) {
	t.Setenv("NAUTILUS_TOKEN", "")
	out, code := captureStderr(t, func() int { return runLogix([]string{"serve", "--listen", ":8080", "--host", "192.0.2.1"}) })
	if code != 2 || !strings.Contains(out, "--listen :8080 reaches beyond this machine") {
		t.Errorf("exit %d, stderr:\n%s", code, out)
	}
}
