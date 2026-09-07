package project

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/eip"
	"github.com/joyautomation/nautilus/runtime"
)

func eipHealthLike(connected bool, lastErr string, polls, pollErrors uint64) eip.Health {
	return eip.Health{
		Connected: connected, LastError: lastErr, Host: "plc", Slot: 0,
		Tags: 5, Polls: polls, PollErrors: pollErrors,
	}
}

func fsys(manifest string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(manifest)},
		"program.fbd": &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR_EXTERNAL Sensor : REAL; Setpoint : REAL; Actuator : REAL; END_VAR
FBD
  w_err = SUB(Setpoint, Sensor)
  Actuator := LIMIT(0.0, w_err, 100.0)
END_FBD
END_PROGRAM`)},
		"interlocks.ld": &fstest.MapFile{Data: []byte(`PROGRAM Interlocks
VAR_EXTERNAL Sensor : REAL; Setpoint : REAL; Alarm : BOOL; END_VAR
LD
  RUNG high
    GT(Sensor, Setpoint) ( Alarm )
END_LD
END_PROGRAM`)},
		"util.st": &fstest.MapFile{Data: []byte(`FUNCTION Doubled : REAL
VAR_INPUT X : REAL; END_VAR
Doubled := X * 2.0;
END_FUNCTION
`)},
	}
}

const manifest = `
name: test-plant
server:
  addr: "localhost:9911"
  online-edits: true
tasks:
  - program: program.fbd
    scan: 50ms
    dt-tag: ScanDtS
  - name: interlocks
    program: interlocks.ld
    scan: 200ms
tags:
  - { name: Sensor,   role: input, unit: "°C", desc: "the sensor" }
  - { name: Setpoint, role: setpoint, init: 50.0 }
  - { name: Actuator, role: output }
  - { name: Alarm,    role: output, init: false }
driver:
  type: memory
`

func TestLoad(t *testing.T) {
	p, err := Load(fsys(manifest), "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "test-plant" || p.Addr != "localhost:9911" {
		t.Fatalf("name/addr: %q %q", p.Name, p.Addr)
	}
	if !p.Server.OnlineEdits {
		t.Fatal("online-edits must carry")
	}
	if p.Runtime.Scan != 50*time.Millisecond || p.Runtime.DtTag != "ScanDtS" {
		t.Fatalf("main task scan/dt: %v %q", p.Runtime.Scan, p.Runtime.DtTag)
	}
	if len(p.Runtime.Tasks) != 1 || p.Runtime.Tasks[0].Name != "interlocks" ||
		p.Runtime.Tasks[0].Scan != 200*time.Millisecond {
		t.Fatalf("tasks: %+v", p.Runtime.Tasks)
	}
	if len(p.Runtime.Libraries) != 1 || !strings.Contains(p.Runtime.Libraries[0], "FUNCTION Doubled") {
		t.Fatalf("libraries: %v", p.Runtime.Libraries)
	}
	if len(p.Runtime.Tags) != 4 {
		t.Fatalf("tags: %+v", p.Runtime.Tags)
	}

	// And the loaded project actually compiles and scans.
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	rt.Tags().SetReal("Sensor", 20.0)
	rt.Scan()
	if v := rt.Tags().Real("Actuator"); v != 30.0 {
		t.Fatalf("main task: Actuator = %v, want 30", v)
	}
	rt.Tags().SetReal("Sensor", 80.0)
	if err := rt.ScanTask("interlocks"); err != nil {
		t.Fatal(err)
	}
	if !rt.Tags().Bool("Alarm") {
		t.Fatal("interlocks task must trip the alarm above setpoint")
	}
}

// TestLoadLibraryCommentMentioningProgram is a regression test: library
// detection used to match the word PROGRAM against raw file text, so a
// comment line beginning with "program" (block or line style) demoted a
// library file to a "program" and silently dropped it from the composed
// library set — surfacing later as "unknown type"/undeclared-identifier
// errors wherever its declarations were needed. Detection now tokenizes
// with the ST lexer, which strips comments before keyword matching.
func TestLoadLibraryCommentMentioningProgram(t *testing.T) {
	cases := []struct {
		name, comment string
	}{
		{"block comment", "(*\n   program; the site program owns the main scan.\n*)\n"},
		{"line comment", "// program note: the site program owns the main scan.\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := fsys(manifest)
			fs["util.st"] = &fstest.MapFile{Data: []byte(c.comment + `FUNCTION Doubled : REAL
VAR_INPUT X : REAL; END_VAR
Doubled := X * 2.0;
END_FUNCTION
`)}
			p, err := Load(fs, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Runtime.Libraries) != 1 || !strings.Contains(p.Runtime.Libraries[0], "FUNCTION Doubled") {
				t.Fatalf("util.st must stay a library despite the comment: %v", p.Runtime.Libraries)
			}
			if _, err := runtime.New(p.Runtime); err != nil {
				t.Fatalf("composed project must still build: %v", err)
			}
		})
	}
}

// TestLoadRealProgramStillDetected makes sure the lexer-based detection
// keeps rejecting a genuine program file as a library candidate, and keeps
// accepting one as a task program — the behaviour the regex used to give
// for the non-comment case.
func TestLoadRealProgramStillDetected(t *testing.T) {
	fs := fsys(manifest)
	if !hasProgramDecl(fs["program.fbd"].Data) {
		t.Fatal("program.fbd must be detected as a program")
	}
	if hasProgramDecl(fs["util.st"].Data) {
		t.Fatal("util.st (a FUNCTION only) must not be detected as a program")
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name, manifest, want string
	}{
		{"no tasks", "name: x\n", "at least one task"},
		{"typo key", "name: x\nserver:\n  onlin-edits: true\ntasks:\n  - program: program.fbd\n", "onlin-edits"},
		{"bad role", "tasks:\n  - program: program.fbd\ntags:\n  - { name: X, role: writable, init: 1.0 }\n", "role must be"},
		{"setpoint without init", "tasks:\n  - program: program.fbd\ntags:\n  - { name: S, role: setpoint }\n", "needs init"},
		{"missing program", "tasks:\n  - program: nope.ld\n", "nope.ld"},
		{"unknown driver", "tasks:\n  - program: program.fbd\ndriver: { type: modbus }\n", "Go tier"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(fsys(tc.manifest), "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// yaml integers land as float64 for numeric seeds — an init of 50 and 50.0
// behave identically (the tag store is REAL-typed).
func TestInitNormalization(t *testing.T) {
	m := strings.Replace(manifest, "init: 50.0", "init: 50", 1)
	p, err := Load(fsys(m), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if v := rt.Tags().Real("Setpoint"); v != 50.0 {
		t.Fatalf("Setpoint = %v", v)
	}
}

func TestSparkplugConfig(t *testing.T) {
	m := manifest + `
sparkplug:
  broker: "tcp://localhost:1883"
  group-id: Joy
  device: plc
  default-class: { deadband: 0.5, max-interval: 30s }
  classes:
    fast: { deadband: 0.1, max-interval: 5s }
    alarms: { every-change: true }
  metric-classes:
    fast: ["Sensor"]
    alarms: ["Alarm", "*Alm"]
`
	p, err := Load(fsys(m), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	node, err := p.Sparkplug(rt)
	if err != nil || node == nil {
		t.Fatalf("node: %v, %v", node, err)
	}

	// No section → no node; missing group-id → a readable error.
	p2, _ := Load(fsys(manifest), "")
	if n, err := p2.Sparkplug(rt); n != nil || err != nil {
		t.Fatalf("no-section must be (nil, nil): %v %v", n, err)
	}
	p3, err := Load(fsys(manifest+"\nsparkplug:\n  broker: tcp://x:1883\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p3.Sparkplug(rt); err == nil {
		t.Fatal("missing group-id must error")
	}
}

func TestEIPStatusStates(t *testing.T) {
	// Connected → connected; connected-but-failing → degraded; error state.
	connected := eipStatus(eipHealthLike(true, "", 5, 0))
	if connected.State != "connected" || connected.Kind != "ethernet-ip" {
		t.Fatalf("connected: %+v", connected)
	}
	degraded := eipStatus(eipHealthLike(true, "", 0, 3))
	if degraded.State != "degraded" {
		t.Fatalf("failing polls must be degraded: %+v", degraded)
	}
	errored := eipStatus(eipHealthLike(false, "connection refused", 0, 0))
	if errored.State != "error" || errored.LastError == "" {
		t.Fatalf("error: %+v", errored)
	}
	connecting := eipStatus(eipHealthLike(false, "", 0, 0))
	if connecting.State != "connecting" {
		t.Fatalf("connecting: %+v", connecting)
	}
}
