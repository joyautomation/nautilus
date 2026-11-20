//go:build unix

package main

// `naut logix serve` end to end: the command as a user runs it, against the
// in-process emulator, with its flags and environment. The facade package
// tests prove the facade; these prove the command hands it what the page
// says (the --tags globs, the --l5x descriptions, NAUTILUS_TOKEN).
//
// The command runs until SIGINT, so the helper stops it the way a user
// does. Unix only for that reason (syscall.Kill); CI runs these on Linux.

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/eip/logixserver"
	"github.com/joyautomation/nautilus/internal/project"
)

var catchInterrupts sync.Once

// running is every in-process `naut logix serve` not yet seen to exit. One
// SIGINT stops all of them, so a cleanup that finds none left must send none:
// a signal sent to nobody is delivered late, to whichever test's instance
// started next, and stops it before it answers.
var (
	runningMu sync.Mutex
	running   []chan struct{}
)

// stopServers sends one SIGINT if any instance is still up and waits for
// every one that was up to exit.
func stopServers(t *testing.T) {
	t.Helper()
	runningMu.Lock()
	var live []chan struct{}
	for _, c := range running {
		select {
		case <-c:
		default:
			live = append(live, c)
		}
	}
	running = live
	runningMu.Unlock()
	if len(live) == 0 {
		return
	}
	// The command's own signal handler takes this; it is registered before
	// the API can answer, so it is in place by now. The test binary's own
	// catcher keeps a signal that finds none from killing the run.
	_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	for _, c := range live {
		select {
		case <-c:
		case <-time.After(10 * time.Second):
			t.Error("naut logix serve did not stop on SIGINT")
		}
	}
}

// serveLogix runs `naut logix serve` in-process with args (plus --listen on
// a free loopback port) and returns the API's base URL once it answers.
func serveLogix(t *testing.T, args ...string) string {
	t.Helper()
	return serveLogixOver(t, "http", http.DefaultClient, args...)
}

// serveLogixOver is serveLogix with the API's scheme and the client that
// reaches it (https, trusting a test certificate).
func serveLogixOver(t *testing.T, scheme string, client *http.Client, args ...string) string {
	t.Helper()
	catchInterrupts.Do(func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT)
		go func() {
			for range c {
			}
		}()
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	done := make(chan int, 1)
	exited := make(chan struct{})
	runningMu.Lock()
	running = append(running, exited)
	runningMu.Unlock()
	go func() { done <- runLogixServe(append(args, "--listen", addr)); close(exited) }()
	base := scheme + "://" + addr
	deadline := time.Now().Add(20 * time.Second)
	for {
		res, err := client.Get(base + "/api/meta")
		if err == nil {
			res.Body.Close()
			break
		}
		select {
		case code := <-done:
			t.Fatalf("naut logix serve exited %d before answering", code)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("naut logix serve never answered")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { stopServers(t) })
	return base
}

func emulateSpec(t *testing.T, spec *logixserver.TagSurfaceSpec) (host, port string, em *logixEmulator) {
	t.Helper()
	em, err := startLogixEmulate(&logixSurface{spec: spec, source: "test"}, "127.0.0.1:0", nil, false, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(em.Stop)
	host, port = hostPort(t, em)
	return host, port, em
}

func servedTags(t *testing.T, base string) []string {
	t.Helper()
	var frame struct {
		Tags map[string]any `json:"tags"`
	}
	waitUntil(t, "a polled frame", func() bool {
		res, err := http.Get(base + "/api/state")
		if err != nil {
			return false
		}
		defer res.Body.Close()
		frame.Tags = nil
		return json.NewDecoder(res.Body).Decode(&frame) == nil && len(frame.Tags) > 0
	})
	var names []string
	for n := range frame.Tags {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// A controller with module I/O beside its user tags: a module-defined
// input structure under "Local:1:I", the way a 1756 input card's tag
// browses, a controller-scope REAL and a program with two tags.
func moduleIOController() *logixserver.TagSurfaceSpec {
	return &logixserver.TagSurfaceSpec{
		ControllerName: "Line1",
		Templates: []logixserver.TemplateSpec{
			{Name: "AB:1756_DI:I:0", Members: []logixserver.MemberSpec{
				{Name: "Fault", Datatype: "DINT"},
				{Name: "Data", Datatype: "DINT"},
			}},
		},
		Symbols: []logixserver.SymbolSpec{
			{Name: "Local:1:I", Datatype: "AB:1756_DI:I:0"},
			{Name: "Speed", Datatype: "REAL"},
			{Name: "Program:MainProgram", Program: true},
			{Name: "Motor", Scope: "Program:MainProgram", Datatype: "DINT"},
			{Name: "Count", Scope: "Program:MainProgram", Datatype: "DINT"},
		},
		Tags: []logixserver.TagSpec{
			{Path: "Local:1:I.Fault", Datatype: "DINT"},
			{Path: "Local:1:I.Data", Datatype: "DINT"},
			{Path: "Speed", Datatype: "REAL"},
			{Path: "Program:MainProgram.Motor", Datatype: "DINT"},
			{Path: "Program:MainProgram.Count", Datatype: "DINT"},
		},
	}
}

// LGX-010: by default every user tag is served and module I/O is left out.
func TestLogixServeLeavesModuleIOOut(t *testing.T) {
	host, port, _ := emulateSpec(t, moduleIOController())
	got := servedTags(t, serveLogix(t, "--host", host, "--port", port, "--poll", "30ms"))
	want := []string{"MainProgram_Count", "MainProgram_Motor", "Speed"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("default serve = %v, want %v (every user tag, no module I/O)", got, want)
	}
	// The emulator does serve the module tag: a selection that asks for it
	// gets it, so leaving it out by default is the command's choice.
	asked := servedTags(t, serveLogix(t, "--host", host, "--port", port, "--poll", "30ms", "--tags", "Local:*"))
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "Local") {
		t.Errorf("--tags 'Local:*' served %v, want only the module tag", asked)
	}
}

// LGX-010: --tags narrows what is served, with globs.
func TestLogixServeTagsGlobNarrows(t *testing.T) {
	host, port, _ := emulateSpec(t, moduleIOController())
	got := servedTags(t, serveLogix(t, "--host", host, "--port", port, "--poll", "30ms",
		"--tags", "Program:MainProgram.*"))
	want := []string{"MainProgram_Count", "MainProgram_Motor"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("--tags 'Program:MainProgram.*' served %v, want %v", got, want)
	}
	got = servedTags(t, serveLogix(t, "--host", host, "--port", port, "--poll", "30ms",
		"--tags", "Speed,Program:MainProgram.Mot*"))
	want = []string{"MainProgram_Motor", "Speed"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("--tags 'Speed,Program:MainProgram.Mot*' served %v, want %v", got, want)
	}
}

// LGX-011: --l5x supplies the descriptions a browse cannot read, under the
// served names (a program tag as <Program>_<Tag>).
func TestLogixServeL5XSuppliesDescriptions(t *testing.T) {
	fixture := l5xFixture("DemoProgram.L5X")
	em := emulateL5X(t, fixture, nil, false)
	host, port := hostPort(t, em)

	meta := func(base string) map[string]struct{ Desc string } {
		res, err := http.Get(base + "/api/meta")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var m struct {
			Tags map[string]struct{ Desc string } `json:"tags"`
		}
		if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
			t.Fatal(err)
		}
		return m.Tags
	}

	// Without --l5x the browse has no documentation to give.
	if d := meta(serveLogix(t, "--host", host, "--port", port))["MainProgram_StartPB"].Desc; d != "" {
		t.Fatalf("a bare browse served a description %q; the --l5x case would prove nothing", d)
	}
	tags := meta(serveLogix(t, "--host", host, "--port", port, "--l5x", fixture))
	for name, want := range map[string]string{
		"MainProgram_StartPB":   "P-101 start pushbutton, field-wired NO",
		"MainProgram_HiLevelSP": "LAH-101 high-level alarm setpoint",
	} {
		if got := tags[name].Desc; got != want {
			t.Errorf("/api/meta %s desc = %q, want %q", name, got, want)
		}
	}
}

// LGX-015: with NAUTILUS_TOKEN set, a write needs the bearer token; without
// it, a write from a non-browser client is open.
func TestLogixServeNautilusTokenGuardsWrites(t *testing.T) {
	host, port, em := emulateSpec(t, moduleIOController())
	write := func(base, token string, v float64) int {
		req, _ := http.NewRequest("POST", base+"/api/tags",
			bytes.NewBufferString(fmt.Sprintf(`{"name": "Speed", "value": %g}`, v)))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	held := func() string { _, v, _ := em.Store.Resolve("Speed"); return fmt.Sprint(v) }

	t.Run("without NAUTILUS_TOKEN writes are open", func(t *testing.T) {
		t.Setenv("NAUTILUS_TOKEN", "")
		base := serveLogix(t, "--host", host, "--port", port)
		if code := write(base, "", 11); code != http.StatusNoContent {
			t.Fatalf("open write = %d, want 204", code)
		}
		if held() != "11" {
			t.Errorf("controller holds %s, want 11", held())
		}
	})
	t.Run("with NAUTILUS_TOKEN a write needs it", func(t *testing.T) {
		t.Setenv("NAUTILUS_TOKEN", "s3cret")
		base := serveLogix(t, "--host", host, "--port", port)
		if code := write(base, "", 22); code != http.StatusUnauthorized {
			t.Errorf("write without the token = %d, want 401", code)
		}
		if code := write(base, "wrong", 22); code != http.StatusUnauthorized {
			t.Errorf("write with a wrong token = %d, want 401", code)
		}
		if held() == "22" {
			t.Fatal("an unauthenticated write reached the controller")
		}
		if code := write(base, "s3cret", 33); code != http.StatusNoContent {
			t.Fatalf("write with the token = %d, want 204", code)
		}
		if held() != "33" {
			t.Errorf("controller holds %s, want 33", held())
		}
	})
}

// #708: with --project, the project's auth: block decides the serve's
// writes as under naut run. With token-role: viewer, NAUTILUS_TOKEN may
// not download (403) and a same-origin loopback client without a token
// is refused too (401); a service token's secret comes from
// NAUTILUS_TOKEN_<NAME>, and an operator's write goes through.
func TestLogixServeProjectEnforcesAuth(t *testing.T) {
	host, port, em := emulateSpec(t, moduleIOController())
	dir := t.TempDir()
	for _, name := range []string{"nautilus.yaml", "MainProgram.ld"} {
		b, err := os.ReadFile(filepath.Join(demoProject, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "nautilus.yaml" {
			b = append(b, "auth:\n  token-role: viewer\n  service-tokens:\n    script: operator\n"...)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NAUTILUS_TOKEN", "admin-secret")
	t.Setenv("NAUTILUS_TOKEN_SCRIPT", "script-secret")
	// Nothing may reach a logixd: a deploy let through fails, not 403.
	t.Setenv("NAUTILUS_LOGIXD_URL", "http://127.0.0.1:1")
	base := serveLogix(t, "--host", host, "--port", port, "--poll", "30ms", "--project", dir)
	send := func(method, path, body, token string) int {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	download := `{"mode": "download", "source": "x", "yes": true}`
	if code := send("POST", "/api/logix/deploy", download, "admin-secret"); code != http.StatusForbidden {
		t.Errorf("a download with NAUTILUS_TOKEN as a viewer = %d, want 403", code)
	}
	if code := send("POST", "/api/logix/deploy", download, ""); code != http.StatusUnauthorized {
		t.Errorf("a download without a token = %d, want 401", code)
	}
	held := func() string { _, v, _ := em.Store.Resolve("Speed"); return fmt.Sprint(v) }
	if code := send("POST", "/api/tags", `{"name": "Speed", "value": 7}`, "admin-secret"); code != http.StatusForbidden || held() == "7" {
		t.Errorf("a write by a viewer = %d, controller holds %s", code, held())
	}
	if code := send("POST", "/api/tags", `{"name": "Speed", "value": 8}`, "script-secret"); code != http.StatusNoContent || held() != "8" {
		t.Errorf("a write by the operator service token = %d, controller holds %s", code, held())
	}
}

// #708: naut logix serve serves HTTPS with a certificate (here from
// NAUTILUS_TLS_CERT and NAUTILUS_TLS_KEY), and a write over it reaches
// the controller.
func TestLogixServeOverTLS(t *testing.T) {
	host, port, em := emulateSpec(t, moduleIOController())
	cert, key, pool := selfSigned(t, t.TempDir())
	t.Setenv("NAUTILUS_TOKEN", "")
	t.Setenv("NAUTILUS_TLS_CERT", cert)
	t.Setenv("NAUTILUS_TLS_KEY", key)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Timeout: 10 * time.Second}
	base := serveLogixOver(t, "https", client, "--host", host, "--port", port, "--poll", "30ms")
	req, _ := http.NewRequest("POST", base+"/api/tags", strings.NewReader(`{"name": "Speed", "value": 44}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if _, v, _ := em.Store.Resolve("Speed"); res.StatusCode != http.StatusNoContent || fmt.Sprint(v) != "44" {
		t.Errorf("a write over TLS: %d, controller holds %v", res.StatusCode, v)
	}
}

// #708 DX-B: naut logix serve --project takes server.tls: self-signed: it
// serves HTTPS with the certificate it makes in the state directory (the
// one the client sees is the file's).
func TestLogixServeProjectSelfSigned(t *testing.T) {
	host, port, _ := emulateSpec(t, moduleIOController())
	state := isolateState(t)
	t.Setenv("NAUTILUS_TOKEN", "")
	t.Setenv("NAUTILUS_TLS_CERT", "")
	t.Setenv("NAUTILUS_TLS_KEY", "")
	dir := t.TempDir()
	for _, name := range []string{"nautilus.yaml", "MainProgram.ld"} {
		b, err := os.ReadFile(filepath.Join(demoProject, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "nautilus.yaml" {
			b = append(b, "server:\n  tls: self-signed\n"...)
		}
		os.WriteFile(filepath.Join(dir, name), b, 0o644)
	}
	var seen []byte
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, // checked below against the file
		VerifyConnection: func(cs tls.ConnectionState) error {
			seen = cs.PeerCertificates[0].Raw
			return nil
		},
	}}}
	serveLogixOver(t, "https", client, "--host", host, "--port", port, "--poll", "30ms", "--project", dir)
	m, err := project.ReadManifest(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	leaf := leafOf(t, filepath.Join(state, "nautilus", m.Name, "tls", "self-signed.pem"))
	if !bytes.Equal(seen, leaf.Raw) {
		t.Error("the serve does not serve the self-signed certificate in the state directory")
	}
}
