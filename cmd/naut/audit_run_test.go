package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
			// Answered, so already on disk: no waiting.
			jdir := filepath.Join(dir, c.dir)
			ents, err := os.ReadDir(jdir)
			if err != nil || len(ents) != 1 || !audit.IsJournalFile(ents[0].Name()) {
				t.Fatalf("journal dir %s: %v %v; stderr:\n%s", jdir, ents, err, stderr.String())
			}
			b, _ := os.ReadFile(filepath.Join(jdir, ents[0].Name()))
			if l := string(b); !strings.Contains(l, `"action":"tag.write"`) || !strings.Contains(l, `"target":"SP"`) ||
				!strings.Contains(l, `"user":"anonymous"`) || !strings.Contains(l, `"after":7`) {
				t.Fatalf("journal line:\n%s", l)
			}
			if r, err := audit.Verify(jdir); err != nil || !r.OK() || r.Entries != 1 {
				t.Fatalf("verify: %+v %v", r, err)
			}
		})
	}
}
