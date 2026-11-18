package main

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
)

// `naut run` keeps the audit journal (#709): a write through the tag API
// is one fsynced line in ./audit (or audit.path) before it is answered,
// and `naut audit verify` accepts the chain.
func TestNautRunJournalsWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	bin := buildNautCLI(t, t.TempDir(), nil)
	for _, c := range []struct{ name, section, dir string }{
		{"default", "", "audit"},
		{"audit.path", "audit:\n  path: var/journal\n  keep-days: 30\n", filepath.Join("var", "journal")},
		// A path that cannot be a directory: the controller runs, with the
		// journal in memory only, said on stderr and on the drivers block
		// (provisional, #709 question 5).
		{"unopenable", "audit:\n  path: main.st\n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{
				"nautilus.yaml": "name: journalled\ntasks:\n  - program: main.st\n    scan: 20ms\n" +
					"tags:\n  - { name: SP, role: setpoint, type: REAL, init: 1 }\n" +
					"server:\n  addr: \"127.0.0.1:0\"\n" + c.section,
				"main.st": "PROGRAM Main\nVAR_EXTERNAL\n SP : REAL;\nEND_VAR\nEND_PROGRAM\n",
			}
			for n, body := range files {
				if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(bin, "run")
			cmd.Dir = dir
			for _, kv := range os.Environ() {
				if !strings.HasPrefix(kv, "NAUTILUS_ADDR=") && !strings.HasPrefix(kv, "NAUTILUS_TOKEN=") {
					cmd.Env = append(cmd.Env, kv)
				}
			}
			var stdout, stderr lockedBuffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			go func() { cmd.Wait(); close(exited) }()
			t.Cleanup(func() {
				cmd.Process.Kill()
				<-exited
			})
			api := apiAddrFromBanner(t, stdout.String, exited, stderr.String)
			deadline := time.Now().Add(30 * time.Second)
			var resp *http.Response
			for {
				var err error
				resp, err = http.Post("http://"+api+"/api/tags", "application/json", strings.NewReader(`{"name":"SP","value":7}`))
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("no tag API on %s: %v; stderr:\n%s", api, err, stderr.String())
				}
				time.Sleep(50 * time.Millisecond)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("write = %d; stderr:\n%s", resp.StatusCode, stderr.String())
			}
			if c.dir == "" {
				if !strings.Contains(stderr.String(), "audit: ERROR") {
					t.Errorf("no error on stderr:\n%s", stderr.String())
				}
				resp, err := http.Get("http://" + api + "/api/drivers")
				if err != nil {
					t.Fatal(err)
				}
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if !strings.Contains(string(b), `"kind":"audit"`) || !strings.Contains(string(b), "memory only") {
					t.Errorf("/api/drivers = %s, want the audit journal's error card", b)
				}
				return
			}
			// Answered, so already on disk: no waiting.
			jdir := filepath.Join(dir, c.dir)
			all, err := os.ReadDir(jdir)
			var ents []os.DirEntry
			for _, e := range all {
				if audit.IsJournalFile(e.Name()) {
					ents = append(ents, e)
				}
			}
			if err != nil || len(ents) != 1 {
				t.Fatalf("journal dir %s: %v %v; stderr:\n%s", jdir, all, err, stderr.String())
			}
			b, _ := os.ReadFile(filepath.Join(jdir, ents[0].Name()))
			if l := string(b); !strings.Contains(l, `"action":"tag.write"`) || !strings.Contains(l, `"target":"SP"`) ||
				!strings.Contains(l, `"user":"anonymous"`) || !strings.Contains(l, `"after":7`) {
				t.Fatalf("journal line:\n%s", l)
			}
			if r, err := audit.Verify(jdir); err != nil || !r.OK() || r.Entries != 1 {
				t.Fatalf("verify: %+v %v", r, err)
			}
			if goruntime.GOOS == "windows" {
				return // SIGTERM
			}
			// A burst of refusals, then a stop: the open window's summary is
			// written before the journal closes.
			for i := 0; i < 8; i++ {
				resp, err := http.Post("http://"+api+"/api/tags", "application/json", strings.NewReader(`{"name":"Nope","value":1}`))
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			select {
			case <-exited:
			case <-time.After(20 * time.Second):
				t.Fatalf("naut run did not stop; stderr:\n%s", stderr.String())
			}
			b, _ = os.ReadFile(filepath.Join(jdir, ents[0].Name()))
			if n := strings.Count(string(b), `"result":"refused"`); n != 6 || !strings.Contains(string(b), `"coalesced":3`) {
				t.Fatalf("after the stop, %d refused lines, want 5 in full and the ×3 summary:\n%s", n, b)
			}
			if r, err := audit.Verify(jdir); err != nil || !r.OK() || r.Entries != 7 {
				t.Fatalf("verify after the stop: %+v %v", r, err)
			}
		})
	}
}
