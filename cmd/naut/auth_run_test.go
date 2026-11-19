package main

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
)

// `naut run` with an auth: block (#708): service tokens from
// NAUTILUS_TOKEN_<NAME>, writes decided by the block (a viewer is refused
// and journalled as itself, an area-limited script writes only its area,
// NAUTILUS_TOKEN keeps its token-role), /api/audit needs audit, reads are
// still open and stderr says so. A token without a secret refuses to start.
func TestNautRunEnforcesAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	bin := buildNautCLI(t, t.TempDir(), nil)
	manifest := "name: guarded\ntasks:\n  - program: main.st\n    scan: 20ms\n" +
		"tags:\n  - { name: RCV_SP, role: setpoint, type: REAL, init: 1 }\n  - { name: RST_SP, role: setpoint, type: REAL, init: 1 }\n" +
		"server:\n  addr: \"127.0.0.1:0\"\n" +
		"auth:\n  token-role: supervisor\n  areas:\n    receiving: {tags: [RCV_*]}\n" +
		"  service-tokens:\n    historian: viewer\n    rcv_script: [{role: operator, areas: [receiving]}]\n"
	// launch starts naut run in a fresh project directory with env added
	// (and no NAUTILUS_* of the caller's).
	launch := func(t *testing.T, env ...string) (dir string, stdout, stderr *lockedBuffer, exited chan struct{}) {
		dir = t.TempDir()
		os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(manifest), 0o644)
		os.WriteFile(filepath.Join(dir, "main.st"), []byte("PROGRAM Main\nVAR_EXTERNAL\n RCV_SP : REAL; RST_SP : REAL;\nEND_VAR\nEND_PROGRAM\n"), 0o644)
		cmd := exec.Command(bin, "run")
		cmd.Dir = dir
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "NAUTILUS_") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, env...)
		stdout, stderr = &lockedBuffer{}, &lockedBuffer{}
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited = make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		t.Cleanup(func() {
			cmd.Process.Kill()
			<-exited
		})
		return dir, stdout, stderr, exited
	}

	t.Run("a token without a secret", func(t *testing.T) {
		_, _, stderr, exited := launch(t, "NAUTILUS_TOKEN=admin-secret", "NAUTILUS_TOKEN_HISTORIAN=h-secret")
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			t.Fatal("naut run started with a service token that has no secret")
		}
		if !strings.Contains(stderr.String(), "NAUTILUS_TOKEN_RCV_SCRIPT") || strings.Contains(stderr.String(), "h-secret") {
			t.Errorf("stderr:\n%s", stderr.String())
		}
	})

	t.Run("enforced", func(t *testing.T) {
		dir, stdout, stderr, exited := launch(t, "NAUTILUS_TOKEN=admin-secret", "NAUTILUS_TOKEN_HISTORIAN=h-secret", "NAUTILUS_TOKEN_RCV_SCRIPT=r-secret")
		api := apiAddrFromBanner(t, stdout.String, exited, stderr.String)
		do := func(method, path, body, token string) (int, string) {
			t.Helper()
			deadline := time.Now().Add(30 * time.Second)
			for {
				req, _ := http.NewRequest(method, "http://"+api+path, strings.NewReader(body))
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					b, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					return resp.StatusCode, string(b)
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s %s: %v; stderr:\n%s", method, path, err, stderr.String())
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
		for _, c := range []struct {
			what, method, path, body, token string
			want                            int
		}{
			{"anonymous write", "POST", "/api/tags", `{"name":"RCV_SP","value":2}`, "", http.StatusUnauthorized},
			{"viewer write", "POST", "/api/tags", `{"name":"RCV_SP","value":2}`, "h-secret", http.StatusForbidden},
			{"script in its area", "POST", "/api/tags", `{"name":"RCV_SP","value":3}`, "r-secret", http.StatusNoContent},
			{"script outside its area", "POST", "/api/tags", `{"name":"RST_SP","value":3}`, "r-secret", http.StatusForbidden},
			{"a wrong token", "POST", "/api/tags", `{"name":"RCV_SP","value":4}`, "guess", http.StatusUnauthorized},
			{"NAUTILUS_TOKEN (supervisor) write", "POST", "/api/tags", `{"name":"RST_SP","value":5}`, "admin-secret", http.StatusNoContent},
			{"NAUTILUS_TOKEN (supervisor) force", "POST", "/api/forces", `{"name":"RST_SP","value":5}`, "admin-secret", http.StatusForbidden},
			{"reads stay open", "GET", "/api/state", "", "", http.StatusOK},
			{"the journal needs audit (viewer)", "GET", "/api/audit", "", "h-secret", http.StatusForbidden},
			{"the journal needs audit (anonymous)", "GET", "/api/audit", "", "", http.StatusUnauthorized},
			{"the journal (supervisor)", "GET", "/api/audit", "", "admin-secret", http.StatusOK},
		} {
			if code, body := do(c.method, c.path, c.body, c.token); code != c.want {
				t.Errorf("%s: %d %s, want %d", c.what, code, body, c.want)
			}
		}
		if _, body := do("GET", "/api/me", "", "r-secret"); !strings.Contains(body, `"principal":"token:rcv_script"`) || !strings.Contains(body, `"receiving":["write"`) {
			t.Errorf("/api/me = %s", body)
		}
		if !strings.Contains(stderr.String(), "reads are NOT GATED") {
			t.Errorf("no warning on stderr:\n%s", stderr.String())
		}
		ents, _ := os.ReadDir(filepath.Join(dir, "audit"))
		var log string
		for _, e := range ents {
			if audit.IsJournalFile(e.Name()) {
				b, _ := os.ReadFile(filepath.Join(dir, "audit", e.Name()))
				log += string(b)
			}
		}
		for _, want := range []string{`"user":"token:historian"`, `"user":"token:rcv_script"`, `"user":"token"`, `"user":"anonymous"`} {
			if !strings.Contains(log, want) {
				t.Errorf("the journal has no %s:\n%s", want, log)
			}
		}
		for _, secret := range []string{"admin-secret", "h-secret", "r-secret"} {
			if strings.Contains(log, secret) || strings.Contains(stderr.String(), secret) {
				t.Errorf("a secret (%s) is in the journal or the log", secret)
			}
		}
	})
}
