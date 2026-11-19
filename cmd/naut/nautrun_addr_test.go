package main

import (
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A `naut run` test must take the tag API's address from the banner, never
// assume the one it configured. Picking a free port (listen, close) and
// writing it into server.addr races every other process on the runner: the
// port can be taken before naut binds it, naut then moves to the next free
// port and says so, and a test polling the configured one waited 30 s for
// nothing (TestNautRunServesOPCUA, run 44237698507). Configure port 0 and ask
// the banner instead.

var bannerAPI = regexp.MustCompile(`tag API on https?://(\S+)`)

// apiAddrFromBanner waits for naut run's banner and returns the tag API
// address it names: the port actually bound, after any move.
func apiAddrFromBanner(t *testing.T, stdout func() string, exited <-chan struct{}, stderr func() string) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		if m := bannerAPI.FindStringSubmatch(stdout()); m != nil {
			return m[1]
		}
		select {
		case <-exited:
			t.Fatalf("naut run exited before its banner named the tag API; stdout:\n%s\nstderr:\n%s", stdout(), stderr())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("naut run's banner never named the tag API; stdout:\n%s\nstderr:\n%s", stdout(), stderr())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// startAddrRun starts `naut run` on a one-tag project whose server.addr is
// addr, and returns its banner's tag API address once that answers.
func startAddrRun(t *testing.T, bin, addr string) (api string, stderr func() string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"nautilus.yaml": "name: addr\ntasks:\n  - program: main.st\n    scan: 50ms\ntags:\n  - { name: X, role: state, init: 1 }\nserver:\n  addr: \"" + addr + "\"\n",
		"main.st":       "PROGRAM Main\nVAR_EXTERNAL\n  X : DINT;\nEND_VAR\n  X := X + 1;\nEND_PROGRAM\n",
	} {
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
	var out, errOut lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() { cmd.Process.Kill(); <-exited })

	api = apiAddrFromBanner(t, out.String, exited, errOut.String)
	waitAPI(t, api, exited, errOut.String)
	return api, errOut.String
}

// waitAPI waits until GET /api/state on addr answers 200. Each GET has its
// own timeout: a port held by something that accepts and never answers must
// not hang the wait past its deadline.
func waitAPI(t *testing.T, addr string, exited <-chan struct{}, stderr func() string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if resp, err := client.Get("http://" + addr + "/api/state"); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		select {
		case <-exited:
			t.Fatalf("naut run exited before serving; stderr:\n%s", stderr())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("naut run never answered on %s; stderr:\n%s", addr, stderr())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// With server.addr on port 0 the banner names the port actually bound, and
// the tag API answers there.
func TestNautRunBannerNamesTheBoundPort(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	bin := buildNautCLI(t, t.TempDir(), nil)
	api, _ := startAddrRun(t, bin, "127.0.0.1:0")
	if _, port, _ := net.SplitHostPort(api); port == "0" || port == "" {
		t.Fatalf("banner names %q, not the bound port", api)
	}
}

// The CI race, on purpose: the configured port is taken before naut starts.
// naut moves to the next free port and says so; the address from the banner
// finds it (the configured one is held by a listener that never answers).
func TestNautRunTagAPIFoundWhenItsPortWasTaken(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	bin := buildNautCLI(t, t.TempDir(), nil)
	squatter, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer squatter.Close()
	taken := squatter.Addr().String()

	api, stderr := startAddrRun(t, bin, taken)
	if api == taken {
		t.Fatalf("banner names the taken port %s", taken)
	}
	if !strings.Contains(stderr(), taken+" is in use") {
		t.Errorf("naut run did not say it moved off %s:\n%s", taken, stderr())
	}
}
