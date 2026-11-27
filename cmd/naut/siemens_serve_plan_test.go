package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/webapi"
	"github.com/joyautomation/nautilus/siemens/webapi/webapitest"
)

const servePlanManifest = `name: s7plan
tasks:
  - program: main.st
    scan: 100ms
tags:
  - {name: Level, role: state, type: REAL}
target:
  siemens:
    station:
      cpu: {name: PLC_1, order: 6ES7 510-1DK03-0AB0, firmware: V3.1, address: 192.0.2.10, subnet-mask: 255.255.255.0, profinet-name: plc-1}
    online: {user: naut, cert-sha256: "ab"}
`

const servePlanMain = `PROGRAM Main
VAR_EXTERNAL
    Level : REAL;
END_VAR
VAR
    count : INT;
    dwell : TON;
END_VAR
dwell(IN := Level > 1.0, PT := T#2s);
count := count + 1;
END_PROGRAM
`

// TestSiemensServeBindsWhatTheWriterGenerates (#789 A1): with no --root,
// serve binds the data blocks the writer generates for the project, the
// program's variables included, and leaves out the writer's own
// bookkeeping members. The project is read again before each
// re-discovery, so a local added to the project and downloaded since
// serve started shows without a restart.
func TestSiemensServeBindsWhatTheWriterGenerates(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("nautilus.yaml", servePlanManifest)
	write("main.st", servePlanMain)

	s := webapitest.New()
	s.Users["naut"] = webapitest.User{Password: "pw", Permissions: []string{"read_value"}}
	s.Vars[`"naut_Tags"`] = &webapitest.Var{Datatype: "struct"}
	s.Vars[`"naut_Tags".Level`] = &webapitest.Var{Datatype: "real", Value: 2.0}
	s.Vars[`"naut_Main_DB"`] = &webapitest.Var{Datatype: "struct"}
	s.Vars[`"naut_Main_DB".count`] = &webapitest.Var{Datatype: "int", Value: 4}
	s.Vars[`"naut_Main_DB".dwell`] = &webapitest.Var{Datatype: "struct"}
	s.Vars[`"naut_Main_DB".dwell.Q`] = &webapitest.Var{Datatype: "bool", Value: true}
	s.Vars[`"naut_Main_DB".naut_dwell_last`] = &webapitest.Var{Datatype: "struct"}
	s.Vars[`"naut_Main_DB".naut_dwell_last.Q`] = &webapitest.Var{Datatype: "bool", Value: true}
	url, pin := s.Start(t)

	cfg, err := resolveSiemensServe(url, "naut", pin, dir, nil, 10*time.Millisecond, "127.0.0.1:0", pw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.planNote != "" || len(cfg.roots) != 2 || cfg.roots[1].DB != "naut_Main_DB" || cfg.roots[1].Prefix != "Main" {
		t.Fatalf("roots %+v, note %q", cfg.roots, cfg.planNote)
	}
	cfg.rediscover = 100 * time.Millisecond
	c, err := webapi.New(url, webapi.Options{User: "naut", Password: "pw", CertSHA256: pin})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan int, 1)
	go func() { done <- serveSiemens(ctx, cfg, c, io.Discard, io.Discard, ready) }()
	t.Cleanup(func() { cancel(); <-done })
	base := "http://" + <-ready

	tags := func() map[string]any {
		res, err := http.Get(base + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var st struct{ Tags, Locals map[string]any }
		if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
			t.Fatal(err)
		}
		// Main's locals come back under "locals" by bare name, as under
		// naut run; flattened here as Main.<path>.
		out := map[string]any{}
		for k, v := range st.Tags {
			out[k] = v
		}
		var flat func(prefix string, m map[string]any)
		flat = func(prefix string, m map[string]any) {
			for k, v := range m {
				if sub, ok := v.(map[string]any); ok {
					flat(prefix+k+".", sub)
					continue
				}
				out[prefix+k] = v
			}
		}
		flat("Main.", st.Locals)
		return out
	}
	waitUntil := func(what string, cond func(map[string]any) bool) map[string]any {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			got := tags()
			if cond(got) {
				return got
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s: %v", what, got)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	got := waitUntil("the program's variables", func(m map[string]any) bool { return m["Main.count"] != nil && m["Level"] != nil })
	if got["Level"] != 2.0 || got["Main.dwell.Q"] != true {
		t.Fatalf("state %v", got)
	}
	for k := range got {
		if strings.Contains(k, "naut_dwell_last") {
			t.Fatalf("bound the writer's own member %s: %v", k, got)
		}
	}

	// A local added to the project, then downloaded: the CPU has it too.
	write("main.st", strings.Replace(servePlanMain, "    count : INT;\n", "    count : INT;\n    seen : BOOL;\n", 1))
	s.Put(`"naut_Main_DB".seen`, &webapitest.Var{Datatype: "bool", Value: true})
	waitUntil("Main.seen after the re-discovery", func(m map[string]any) bool { return m["Main.seen"] == true })
}
