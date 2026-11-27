package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
	"github.com/joyautomation/nautilus/siemens/webapi"
	"github.com/joyautomation/nautilus/siemens/webapi/webapitest"
)

const parityManifest = `name: s7parity
tasks:
  - program: main.st
    scan: 100ms
  - program: zaux.st
    scan: 100ms
tags:
  - {name: Level, role: state, type: REAL}
  - {name: Run, role: setpoint, type: BOOL}
target:
  siemens:
    station:
      cpu: {name: PLC_1, order: 6ES7 510-1DK03-0AB0, firmware: V3.1, address: 192.0.2.10, subnet-mask: 255.255.255.0, profinet-name: plc-1}
    online: {user: naut, cert-sha256: "ab"}
`

const parityMain = `PROGRAM Main
VAR_EXTERNAL
    Level : REAL;
    Run : BOOL;
END_VAR
VAR
    count : INT;
    dwell : TON;
END_VAR
dwell(IN := Run, PT := T#2s);
count := count + 1;
END_PROGRAM
`

const parityAux = `PROGRAM Zaux
VAR_EXTERNAL
    Level : REAL;
END_VAR
VAR
    count : INT;
    gain : REAL;
END_VAR
count := count + 2;
END_PROGRAM
`

// TestSiemensServeShowsWhatNautRunShows (#789 A1b): for the same project,
// serve's /api/state and /api/meta have naut run's shape: the manifest
// tags as tags, the programs' locals under "locals" by bare name (a TON as
// its members, a name two programs share showing the main program's), and
// the same declared types.
func TestSiemensServeShowsWhatNautRunShows(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"nautilus.yaml": parityManifest, "main.st": parityMain, "zaux.st": parityAux} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// naut run's side: the project's runtime and server, as naut run
	// composes them (not started: the shape is what is compared).
	proj, err := project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	runAPI := httptest.NewServer(server.New(rt).Handler())
	defer runAPI.Close()

	// serve's side: a fake CPU holding what the writer generates.
	s := webapitest.New()
	s.Users["naut"] = webapitest.User{Password: "pw", Permissions: []string{"read_value"}}
	for sym, v := range map[string]*webapitest.Var{
		`"naut_Tags"`:                      {Datatype: "struct"},
		`"naut_Tags".Level`:                {Datatype: "real", Value: 2.0},
		`"naut_Tags".Run`:                  {Datatype: "bool", Value: true},
		`"naut_Main_DB"`:                   {Datatype: "struct"},
		`"naut_Main_DB".count`:             {Datatype: "int", Value: 4},
		`"naut_Main_DB".dwell`:             {Datatype: "struct"},
		`"naut_Main_DB".dwell.IN`:          {Datatype: "bool", Value: true},
		`"naut_Main_DB".dwell.PT`:          {Datatype: "time", Value: 2000},
		`"naut_Main_DB".dwell.ET`:          {Datatype: "time", Value: 500},
		`"naut_Main_DB".dwell.Q`:           {Datatype: "bool", Value: false},
		`"naut_Main_DB".naut_dwell_last`:   {Datatype: "struct"},
		`"naut_Main_DB".naut_dwell_last.Q`: {Datatype: "bool", Value: false},
		`"naut_Zaux_DB"`:                   {Datatype: "struct"},
		`"naut_Zaux_DB".count`:             {Datatype: "int", Value: 99},
		`"naut_Zaux_DB".gain`:              {Datatype: "real", Value: 1.5},
	} {
		s.Vars[sym] = v
	}
	url, pin := s.Start(t)
	cfg, err := resolveSiemensServe(url, "naut", pin, dir, nil, 10*time.Millisecond, "127.0.0.1:0", pw)
	if err != nil || cfg.planNote != "" {
		t.Fatalf("%v %q", err, cfg.planNote)
	}
	c, err := webapi.New(url, webapi.Options{User: "naut", Password: "pw", CertSHA256: pin})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan int, 1)
	go func() { done <- serveSiemens(ctx, cfg, c, io.Discard, io.Discard, ready) }()
	t.Cleanup(func() { cancel(); <-done })
	serveAPI := "http://" + <-ready

	get := func(base, path string, v any) {
		t.Helper()
		res, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if err := json.NewDecoder(res.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	type stateJSON struct {
		Tags   map[string]any `json:"tags"`
		Locals map[string]any `json:"locals"`
	}
	var served stateJSON
	deadline := time.Now().Add(5 * time.Second)
	for {
		served = stateJSON{}
		get(serveAPI, "/api/state", &served)
		if served.Tags["Level"] != nil && served.Locals["dwell"] != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve state never filled: %+v", served)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var ran stateJSON
	get(runAPI.URL, "/api/state", &ran)

	if a, b := shape(ran.Tags), shape(served.Tags); a != b {
		t.Errorf("tags: naut run %s, serve %s", a, b)
	}
	if a, b := shape(ran.Locals), shape(served.Locals); a != b {
		t.Errorf("locals: naut run %s, serve %s", a, b)
	}
	if served.Locals["count"] != float64(4) {
		t.Errorf("count = %v: a name two programs share shows the main program's (4), as under naut run", served.Locals["count"])
	}
	if dw, _ := served.Locals["dwell"].(map[string]any); dw == nil || dw["PT"] != float64(2000) || dw["IN"] != true {
		t.Errorf("dwell = %v", served.Locals["dwell"])
	}

	type metaJSON struct {
		Tags   map[string]json.RawMessage `json:"tags"`
		Locals map[string]json.RawMessage `json:"locals"`
	}
	var mRun, mServe metaJSON
	get(runAPI.URL, "/api/meta", &mRun)
	get(serveAPI, "/api/meta", &mServe)
	if !reflect.DeepEqual(mRun.Tags, mServe.Tags) {
		t.Errorf("meta tags:\n naut run %s\n serve    %s", raw(mRun.Tags), raw(mServe.Tags))
	}
	if !reflect.DeepEqual(mRun.Locals, mServe.Locals) {
		t.Errorf("meta locals:\n naut run %s\n serve    %s", raw(mRun.Locals), raw(mServe.Locals))
	}
}

// shape is m's keys, recursively, sorted: "count,dwell{ET,IN,PT,Q}".
func shape(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			k += "{" + shape(sub) + "}"
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func raw(m map[string]json.RawMessage) string {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		keys = append(keys, k+"="+string(v))
	}
	sort.Strings(keys)
	return strings.Join(keys, " ")
}
