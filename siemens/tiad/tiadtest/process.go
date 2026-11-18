package tiadtest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// Process is an agent binary running for one test: tiad-fake under Mono,
// or the real agent on a TIA machine. It listens on a loopback port of
// its own choosing, works in its own temporary work dir, and exits when
// the test ends.
type Process struct {
	URL     string
	WorkDir string

	client *tiad.Client
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	done   chan struct{}

	mu  sync.Mutex
	log bytes.Buffer
}

// Command is how bin runs here: a .exe off Windows runs under Mono
// (TIAD_MONO, default "mono").
func Command(bin string, args ...string) *exec.Cmd {
	if strings.HasSuffix(strings.ToLower(bin), ".exe") && runtime.GOOS != "windows" {
		mono := os.Getenv("TIAD_MONO")
		if mono == "" {
			mono = "mono"
		}
		return exec.Command(mono, append([]string{"--debug", bin}, args...)...)
	}
	return exec.Command(bin, args...)
}

// AgentEnv is the process environment for an agent: this process's own,
// without any TIAD_ variable, then env (later entries win).
func AgentEnv(env ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TIAD_") {
			out = append(out, kv)
		}
	}
	return append(out, env...)
}

// syncBuffer is a bytes.Buffer behind a Process's lock.
type logWriter struct{ p *Process }

func (w logWriter) Write(b []byte) (int, error) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	return w.p.log.Write(b)
}

// StartProcess starts bin with cfg and waits until it listens. env adds
// to, or overrides, the agent's environment.
func StartProcess(t testing.TB, bin string, cfg Config, env ...string) *Process {
	t.Helper()
	p := &Process{WorkDir: t.TempDir(), done: make(chan struct{})}
	p.cmd = Command(bin)
	p.cmd.Env = AgentEnv(append([]string{
		"TIAD_ADDR=http://127.0.0.1:0",
		"TIAD_WORKDIR=" + p.WorkDir,
		"TIAD_TOKEN=" + cfg.Token,
		"TIAD_TARGETS=" + strings.Join(cfg.Targets, ","),
	}, env...)...)
	p.cmd.Stderr = logWriter{p}
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if p.stdin, err = p.cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", bin, err)
	}
	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			if u, ok := strings.CutPrefix(line, "tiad: listening on "); ok {
				select {
				case ready <- u:
				default:
				}
				continue
			}
			fmt.Fprintln(logWriter{p}, line)
		}
	}()
	go func() {
		_ = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(p.Stop)

	select {
	case p.URL = <-ready:
	case <-p.done:
		t.Fatalf("%s exited before listening:\n%s", bin, p.Log())
	case <-time.After(60 * time.Second):
		t.Fatalf("%s did not listen within 60s:\n%s", bin, p.Log())
	}
	p.client = tiad.New(p.URL, cfg.Token)
	p.client.Token = cfg.Token
	return p
}

// CloseStdin closes the agent's standard input, which ends it.
func (p *Process) CloseStdin() { _ = p.stdin.Close() }

// Stop closes the agent's standard input and waits for it to exit; after
// 10 s it kills it.
func (p *Process) Stop() {
	p.CloseStdin()
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// Exited reports whether the process has ended, waiting up to d.
func (p *Process) Exited(d time.Duration) bool {
	select {
	case <-p.done:
		return true
	default:
	}
	select {
	case <-p.done:
		return true
	case <-time.After(d):
		return false
	}
}

// Log is what the agent wrote to stderr (and to stdout apart from its
// ready line) so far.
func (p *Process) Log() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.log.String()
}

// Client talks to the agent with its token.
func (p *Process) Client() *tiad.Client { return p.client }

// SetGates scripts tiad-fake's machine gates (PUT /fake/gates).
func (p *Process) SetGates(t testing.TB, gates []tiad.Gate) {
	t.Helper()
	p.control(t, http.MethodPut, "/fake/gates", map[string]any{"gates": gates})
}

func (p *Process) control(t testing.TB, method, path string, body any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, p.URL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if p.client.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.client.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		rb, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: HTTP %d %s", method, path, resp.StatusCode, rb)
	}
}

// RunToExit runs bin with env (see AgentEnv) and no listener expected,
// for a configuration the agent must refuse. It returns the exit code and
// everything the process wrote.
func RunToExit(t testing.TB, bin string, env ...string) (int, string) {
	t.Helper()
	cmd := Command(bin)
	cmd.Env = AgentEnv(env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		_ = stdin.Close()
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), out.String()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, out.String()
	case <-time.After(60 * time.Second):
		// A refused configuration exits at once; one that is not
		// refused starts listening and would run until stdin closes.
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("%s did not exit (it accepted the configuration?):\n%s", bin, out.String())
		return 0, ""
	}
}
