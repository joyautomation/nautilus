package wiring

import (
	"strings"
	"testing"
)

// pump is the design brief's first example (docs/design/one-struct-model.md
// §1): the file every refusal below starts from.
const pump = `
wirings:
  - type: Pump
    params: { endpoint: null, unit-id: 1 }
    sources:
      drive: { driver: modbus, endpoint: "{endpoint}", unit-id: "{unit-id}" }
    members:
      Running:      { from: drive, table: coil, address: 0 }
      Fault:        { from: drive, table: discrete, address: 2 }
      Speed:        { from: drive, table: holding, address: 4, format: int16, scale: 0.1, unit: "%" }
      Healthy:      { expr: "Running AND NOT Fault" }
      RunHours:     { memory: retained }
      Cmd.Start:    { to: drive, table: coil, address: 10 }
      Cmd.Stop:     { to: drive, table: coil, address: 11, event: true }
      Cmd.SpeedRef: { to: drive, table: holding, address: 20, format: int16, scale: 0.1, readback: Speed }
    alarms:
      - { member: Fault, name: "{desc} fault", priority: high, class: pump }
instances:
  - { name: P101, type: Pump, desc: "Lift pump 1", params: { endpoint: 10.0.0.11 } }
  - { name: P102, type: Pump, params: { endpoint: 10.0.0.12 }, set: { RunHours: 40.0 } }
`

// tank is the brief's mixed-driver example.
const tank = `
wirings:
  - type: Tank
    params: { area: null, n: null, pump: null }
    sources:
      level: { driver: plant-ua }
      pump:  { driver: modbus, endpoint: "{pump}" }
    members:
      Level:     { from: level, node: "ns=2;s={area}.LT{n}.PV", unit: "%" }
      PumpOn:    { from: pump, table: coil, address: 0 }
      Cmd.Run:   { to: pump, table: coil, address: 10, readback: PumpOn }
      HighLevel: { expr: "Level > HighSP" }
      HighSP:    { memory: retained }
instances:
  - { name: T1, type: Tank, params: { area: North, n: 1, pump: 10.0.0.21 } }
`

// peer is a type a driver fills whole, extended (design §8).
const peer = `
wirings:
  - type: Motor
    params: { tag: null }
    sources: { plc: { driver: eip } }
    fill: { from: plc, tag: "{tag}" }
    members:
      SpeedSP:  { both: plc }
      Healthy:  { expr: "NOT Faulted AND Running" }
      RunHours: { memory: retained }
    alarms:
      - { member: Healthy, name: "{name} unhealthy" }
instances:
  - { name: M1, type: Motor, params: { tag: Line1_M1 } }
`

func decode(t *testing.T, srcs ...string) []*File {
	t.Helper()
	var fs []*File
	for i, s := range srcs {
		f, err := Decode("f"+string(rune('0'+i))+".yaml", []byte(s))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		fs = append(fs, f)
	}
	return fs
}

func problems(t *testing.T, env Env, srcs ...string) string {
	t.Helper()
	var b strings.Builder
	for _, p := range Check(decode(t, srcs...), env) {
		b.WriteString(p.Error() + "\n")
	}
	return b.String()
}

func TestTheBriefsExamplesAreClean(t *testing.T) {
	for name, src := range map[string]string{"pump": pump, "tank": tank, "peer": peer} {
		if got := problems(t, Env{}, src); got != "" {
			t.Errorf("%s: the brief's own example is refused:\n%s", name, got)
		}
	}
	// The three together share one namespace and still agree.
	if got := problems(t, Env{}, pump, tank, peer); got != "" {
		t.Errorf("three files together:\n%s", got)
	}
}

func TestDecodeReadsEveryKey(t *testing.T) {
	f := decode(t, pump)[0]
	if len(f.Wirings) != 1 || len(f.Instances) != 2 {
		t.Fatalf("decoded %d wirings, %d instances", len(f.Wirings), len(f.Instances))
	}
	w := f.Wirings[0]
	if w.Pos.File != "f0.yaml" || w.Pos.Line != 3 {
		t.Errorf("wiring position = %+v, want f0.yaml:3", w.Pos)
	}
	if f.Instances[1].Pos.Line != 20 {
		t.Errorf("second instance line = %d, want 20", f.Instances[1].Pos.Line)
	}
	if !w.Params["endpoint"].Required() || w.Params["unit-id"].Required() {
		t.Errorf("params: endpoint must be required (null), unit-id not: %+v", w.Params)
	}
	if got := w.Sources["drive"]; got.Driver != "modbus" || got.Keys["endpoint"] != "{endpoint}" {
		t.Errorf("source = %+v", got)
	}
	m := w.Members["Cmd.SpeedRef"]
	if m.Kind != To || m.Source != "drive" || m.Readback != "Speed" || m.Addr["address"] != 20 || m.Addr["scale"] != 0.1 {
		t.Errorf("member = %+v", m)
	}
	if !w.Members["Cmd.Stop"].Event || w.Members["Cmd.Start"].Event {
		t.Error("event: lost")
	}
	if got := w.Members["RunHours"]; got.Kind != Memory || got.MemoryMode != Retained {
		t.Errorf("memory member = %+v", got)
	}
	if got := w.Members["Healthy"]; got.Kind != Expr || got.Expr != "Running AND NOT Fault" {
		t.Errorf("expr member = %+v", got)
	}
	if f.Instances[0].Params["endpoint"] != "10.0.0.11" || f.Instances[1].Set["RunHours"] != 40.0 {
		t.Errorf("instances = %+v", f.Instances)
	}
	if len(w.Alarms) != 1 || w.Alarms[0].Priority != "high" {
		t.Errorf("alarms = %+v", w.Alarms)
	}
}

// A misspelled key is an error with the file and line, never a silently
// dropped binding.
func TestDecodeIsStrict(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"unknown wiring key", "wirings:\n  - type: Pump\n    membres: {}\n", "f.yaml: yaml: unmarshal errors:\n  line 3: field membres not found"},
		{"unknown top key", "wiring:\n  - type: Pump\n", "line 1: field wiring not found"},
		{"unknown instance key", "instances:\n  - { name: P, type: Pump, parms: {} }\n", "field parms not found"},
		{"unknown alarm key", "wirings:\n  - type: P\n    alarms:\n      - { member: A, nme: x }\n", "field nme not found"},
		{"two kinds", "wirings:\n  - type: P\n    members:\n      A: { from: d, to: d }\n", "a member has one kind, and this one gives from and to"},
		{"a key given twice", "wirings:\n  - type: P\n    members:\n      A: { from: d, address: 1, address: 2 }\n", `"address" is given twice`},
		{"event not a bool", "wirings:\n  - type: P\n    members:\n      A: { to: d, event: maybe }\n", "event: must be true or false"},
		{"param with a stray key", "wirings:\n  - type: P\n    params: { a: { dflt: 1 } }\n", `a param takes default and publish, not "dflt"`},
		{"param is a list", "wirings:\n  - type: P\n    params: { a: [1, 2] }\n", "a param is a scalar"},
		{"member not a mapping", "wirings:\n  - type: P\n    members:\n      A: from\n", "a member must be a mapping"},
		{"tuning true", "instances:\n  - { name: P, type: T, alarms: { A: true } }\n", "an alarm tuning is false"},
		{"tuning stray key", "instances:\n  - { name: P, type: T, alarms: { A: { prio: low } } }\n", `an alarm tuning takes name, priority, on-delay, class, notes, not "prio"`},
	} {
		_, err := Decode("f.yaml", []byte(tc.src))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", tc.name, err, tc.want)
		}
	}
}

func TestEmptyFileDecodes(t *testing.T) {
	f, err := Decode("f.yaml", nil)
	if err != nil || len(f.Wirings)+len(f.Instances) != 0 {
		t.Errorf("an empty wiring file is no wirings: %+v, %v", f, err)
	}
}

// edit replaces the first occurrence of old in pump.
func edit(old, new string) string {
	if !strings.Contains(pump, old) {
		panic("test edit does not match the pump example: " + old)
	}
	return strings.Replace(pump, old, new, 1)
}

// Every refusal names what is wrong and how to fix it (design §15).
func TestRefusalsNameTheFix(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []string
	}{
		{"no type", "wirings:\n  - name: X\n", []string{"f0.yaml:2: a wiring needs type:"}},
		{"type not an identifier", edit("type: Pump\n    params", "type: Pump-1\n    params"), []string{`wiring type "Pump-1" is not an IEC identifier`}},
		{"a source the member does not have", edit("Running:      { from: drive,", "Running:      { from: drve,"),
			[]string{`member "Running": from: "drve" is not a source of this wiring (did you mean drive?)`}},
		{"a member with no kind", edit("RunHours:     { memory: retained }", "RunHours:     { unit: h }"),
			[]string{`member "RunHours" has no kind`, "a member left out is retained memory"}},
		{"memory mode", edit("memory: retained", "memory: keep"), []string{`memory: is retained or initial, not "keep"`}},
		{"empty expr", edit(`expr: "Running AND NOT Fault"`, `expr: " "`), []string{`member "Healthy": expr: is empty`}},
		{"address keys on memory", edit("RunHours:     { memory: retained }", "RunHours:     { memory: retained, address: 3 }"),
			[]string{`member "RunHours" is memory, which takes no address keys (address)`}},
		{"address keys on expr", edit(`{ expr: "Running AND NOT Fault" }`, `{ expr: "Running AND NOT Fault", table: coil }`),
			[]string{`is expr, which takes no address keys (table)`}},
		{"bad path", edit("Cmd.Start:    {", "Cmd..Start:   {"), []string{`member "Cmd..Start" is not a member path`}},
		{"event on a from", edit("Fault:        { from: drive,", "Fault:        { event: true, from: drive,"),
			[]string{`member "Fault": event: is only for a to: member`}},
		{"readback on a from", edit("Fault:        { from: drive,", "Fault:        { readback: Speed, from: drive,"),
			[]string{`member "Fault": readback: is only for a to: member, and this one is from`}},
		{"readback names an implicit member", edit("readback: Speed", "readback: Spd"),
			[]string{`readback: "Spd" is not wired in this wiring`, "a member left out is writable memory", "Wire it from:, both: or expr:"}},
		{"readback names a memory member", edit("readback: Speed", "readback: RunHours"),
			[]string{`readback: "RunHours" is memory: a read-back must be from:, both: or expr:`}},
		{"a whole struct that is not memory", edit("Cmd.Start:    {", "Cmd: { to: drive }\n      Cmd.Start:    {"),
			[]string{`member "Cmd" is a whole struct ("Cmd.SpeedRef" is under it) but is wired to`, "only memory: may name a whole struct"}},
		{"alarm on an implicit member", edit("member: Fault,", "member: Flt,"),
			[]string{`an alarm names member "Flt", which this wiring does not wire`, "so an alarm on it could be set by hand", "(did you mean Fault?)"}},
		{"two alarms on a member", edit("class: pump }", "class: pump }\n      - { member: Fault, name: again }"), []string{`member "Fault" has two alarms`}},
		{"an unknown param in an address", edit("address: 0 }", "address: \"{regs}\" }"),
			[]string{"member Running address: {regs} is not a param of this wiring (it has params desc, endpoint, name, unit-id)"}},
		{"an unknown param in a source", edit(`unit-id: "{unit-id}"`, `unit-id: "{unti-id}"`), []string{"source drive unit-id: {unti-id} is not a param", "(did you mean unit-id?)"}},
		{"an unknown param in an alarm", edit("{desc} fault", "{nme} fault"), []string{"alarm Fault name: {nme} is not a param", "(did you mean name?)"}},
		{"a brace that is no param", edit(`"Running AND NOT Fault"`, `"Running AND {1 + 2}"`), []string{"{1 + 2} is not a param name"}},
		{"an unmatched brace", edit("{desc} fault", "{desc fault"), []string{"an unmatched brace"}},
		{"a built-in param redeclared", edit("params: { endpoint: null,", "params: { name: x, endpoint: null,"), []string{`param "name" is built in`}},
	} {
		got := problems(t, Env{}, tc.src)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: want %q in:\n%s", tc.name, w, got)
			}
		}
		if len(tc.want) > 0 && !strings.Contains(got, "f0.yaml:") {
			t.Errorf("%s: a refusal must name its file and line:\n%s", tc.name, got)
		}
	}
}

func TestSourceNeedsADriver(t *testing.T) {
	src := edit("drive: { driver: modbus,", "drive: { driver: '',")
	if got := problems(t, Env{}, src); !strings.Contains(got, `source "drive" needs driver:`) {
		t.Errorf("got:\n%s", got)
	}
}

func TestFillRules(t *testing.T) {
	bad := strings.Replace(peer, "fill: { from: plc,", "fill: { from: pl,", 1)
	if got := problems(t, Env{}, bad); !strings.Contains(got, `fill: from "pl" is not a source of this wiring (did you mean plc?)`) {
		t.Errorf("got:\n%s", got)
	}
	// A fill wiring may alarm and read back members the peer brings.
	ok := strings.Replace(peer, "member: Healthy", "member: Faulted", 1)
	if got := problems(t, Env{}, ok); got != "" {
		t.Errorf("an alarm on a member the peer brings is refused:\n%s", got)
	}
}

// both: is only for a peer controller (design §2.1).
func TestBothOnlyOnPeerDrivers(t *testing.T) {
	onModbus := edit("Fault:        { from: drive,", "Fault:        { both: drive,")
	got := problems(t, Env{}, onModbus)
	for _, w := range []string{`member "Fault": both: is only for a peer controller (eip, sparkplug-host)`, `source "drive" is a modbus field device`, "Use to: with readback:"} {
		if !strings.Contains(got, w) {
			t.Errorf("want %q in:\n%s", w, got)
		}
	}
	if got := problems(t, Env{}, peer); got != "" {
		t.Errorf("both: on eip is refused:\n%s", got)
	}
	// A drivers: entry's name resolves through the environment.
	env := Env{Driver: func(n string) (string, bool) {
		return map[string]string{"line-plc": "eip", "drive-bus": "modbus", "modbus": "modbus", "eip": "eip"}[n], n == "line-plc" || n == "drive-bus" || n == "modbus" || n == "eip"
	}}
	viaName := strings.Replace(peer, "driver: eip", "driver: line-plc", 1)
	if got := problems(t, env, viaName); got != "" {
		t.Errorf("both: on a driver named line-plc (an eip) is refused:\n%s", got)
	}
	viaBus := strings.Replace(onModbus, "driver: modbus", "driver: drive-bus", 1)
	if got := problems(t, env, viaBus); !strings.Contains(got, "is a modbus field device") {
		t.Errorf("both: on a named modbus entry is allowed:\n%s", got)
	}
	// An unresolvable driver is reported once, at the source.
	unknown := strings.Replace(pump, "driver: modbus", "driver: nope", 1)
	if got := problems(t, env, unknown); !strings.Contains(got, `driver "nope" is not a drivers: entry or a driver type`) {
		t.Errorf("got:\n%s", got)
	}
}

func TestInstanceRefusals(t *testing.T) {
	inst := func(line string) string {
		return strings.Replace(pump, "instances:\n", "instances:\n  "+line+"\n", 1)
	}
	for _, tc := range []struct {
		name string
		src  string
		want []string
	}{
		{"no name", inst("- { type: Pump }"), []string{"an instance needs name:"}},
		{"no type", inst("- { name: X }"), []string{"instance X needs type:"}},
		{"a type with no wiring", inst("- { name: X, type: Valve }"), []string{"instance X: no wiring for type Valve: declare one under wirings:"}},
		{"a missing required param", inst("- { name: X, type: Pump }"), []string{`instance X does not give param "endpoint", which wiring "Pump" requires (it has no default)`}},
		{"an unknown param", inst("- { name: X, type: Pump, params: { endpoint: a, endpint: b } }"), []string{`instance X: "endpint" is not a param of wiring "Pump" (it has endpoint, unit-id)`, "(did you mean endpoint?)"}},
		{"a structured param", inst("- { name: X, type: Pump, params: { endpoint: [a] } }"), []string{`param "endpoint" must be a scalar`}},
		{"set on a from", inst("- { name: X, type: Pump, params: { endpoint: a }, set: { Speed: 1 } }"), []string{"instance X: set: Speed is from, not memory", "a value the device or the expression decides"}},
		{"set on an expr", inst("- { name: X, type: Pump, params: { endpoint: a }, set: { Healthy: true } }"), []string{"set: Healthy is expr, not memory"}},
		{"set on a to", inst("- { name: X, type: Pump, params: { endpoint: a }, set: { Cmd: { Start: true } } }"), []string{"set: Cmd.Start is to, not memory", "a command the program sends"}},
		{"set on a name no one wired is memory", inst("- { name: X, type: Pump, params: { endpoint: a }, set: { Spare: 1 } }"), nil},
		{"wire changes the kind", inst("- { name: X, type: Pump, params: { endpoint: a }, wire: { Speed: { to: drive } } }"), []string{"instance X: wire: Speed would make it to, but the wiring makes it from", "a kind is the type's"}},
		{"wire on a memory member", inst("- { name: X, type: Pump, params: { endpoint: a }, wire: { RunHours: { address: 1 } } }"), []string{"wire: RunHours is memory: only a from:, to: or both: member has an address to change"}},
		{"wire on an unwired member", inst("- { name: X, type: Pump, params: { endpoint: a }, wire: { Sped: { address: 1 } } }"), []string{"wire: Sped is not wired by wiring", "(did you mean Speed?)"}},
		{"wire to another source", inst("- { name: X, type: Pump, params: { endpoint: a }, wire: { Speed: { from: other } } }"), []string{`wire: Speed: "other" is not a source of wiring "Pump"`}},
		{"tuning an alarm there is none of", inst("- { name: X, type: Pump, params: { endpoint: a }, alarms: { Speed: false } }"), []string{"alarms: Speed has no alarm in wiring"}},
		{"offline for a source there is none of", inst("- { name: X, type: Pump, params: { endpoint: a }, offline-alarm: { drve: { priority: low } } }"), []string{`offline-alarm: "drve" is not a source of wiring "Pump" (did you mean drive?)`}},
		{"a repeated instance", inst("- { name: P101, type: Pump, params: { endpoint: a } }"), []string{`instance "P101" is declared again (first at f0.yaml:`}},
		{"a repeated instance in another case", inst("- { name: p101, type: Pump, params: { endpoint: a } }"), []string{`instance "P101" is declared again (first at f0.yaml:`}},
		{"an unknown wiring", inst("- { name: X, type: Pump, wiring: Pmp }"), []string{`instance X: wiring "Pmp" is not declared (did you mean Pump?)`}},
	} {
		got := problems(t, Env{}, tc.src)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: want %q in:\n%s", tc.name, w, got)
			}
		}
		if len(tc.want) == 0 && got != "" {
			t.Errorf("%s: want no problem, got:\n%s", tc.name, got)
		}
	}
}

func TestValidInstanceOverrides(t *testing.T) {
	src := strings.Replace(pump, "instances:\n", `instances:
  - name: X
    type: Pump
    params: { endpoint: a, unit-id: 2 }
    set: { RunHours: 1, Spare: 2 }
    wire: { Speed: { address: 6 }, Running: { from: drive } }
    alarms: { Fault: { priority: low, on-delay: 5s } }
    offline-alarm: { priority: low }
`, 1)
	if got := problems(t, Env{}, src); got != "" {
		t.Errorf("valid per-instance overrides refused:\n%s", got)
	}
	f := decode(t, src)[0]
	var x Instance
	for _, in := range f.Instances {
		if in.Name == "X" {
			x = in
		}
	}
	if x.Alarms["Fault"].Priority != "low" || x.Alarms["Fault"].OnDelay != "5s" {
		t.Errorf("alarms tuning = %+v", x.Alarms)
	}
	if x.OfflineAlarm == nil || x.OfflineAlarm.All == nil || x.OfflineAlarm.All.Priority != "low" {
		t.Errorf("offline-alarm = %+v", x.OfflineAlarm)
	}
	if got := x.Wire["Speed"]; got.Addr["address"] != 6 || got.Kind != "" {
		t.Errorf("wire override = %+v", got)
	}
}

func TestOfflineAlarmForms(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want func(o *Offline) bool
	}{
		{"false", func(o *Offline) bool { return o.Off }},
		{"{ priority: low, on-delay: 2s }", func(o *Offline) bool { return o.All != nil && o.All.OnDelay == "2s" }},
		{"{ drive: { priority: low }, other: false }", func(o *Offline) bool { return o.BySource["drive"].Priority == "low" && o.BySource["other"].Off }},
	} {
		f, err := Decode("f", []byte("instances:\n  - { name: X, type: T, offline-alarm: "+tc.src+" }\n"))
		if err != nil {
			t.Fatalf("%s: %v", tc.src, err)
		}
		if o := f.Instances[0].OfflineAlarm; o == nil || !tc.want(o) {
			t.Errorf("%s decoded to %+v", tc.src, o)
		}
	}
}

// A wiring name is unique across files, and so is an instance's.
func TestOneNamespaceAcrossFiles(t *testing.T) {
	got := problems(t, Env{}, pump, pump)
	for _, w := range []string{`wiring "Pump" is declared again (first at f0.yaml:3)`, `instance "P101" is declared again (first at f0.yaml:19)`} {
		if !strings.Contains(got, w) {
			t.Errorf("want %q in:\n%s", w, got)
		}
	}
	if !strings.Contains(got, "f1.yaml:3:") {
		t.Errorf("the second declaration must be named by its file and line:\n%s", got)
	}
	clash := "instances:\n  - { name: Pump, type: Pump, params: { endpoint: a } }\n"
	if got := problems(t, Env{}, pump, clash); !strings.Contains(got, `instance "Pump" has the name of the wiring at f0.yaml:3`) {
		t.Errorf("got:\n%s", got)
	}
}

// Several wirings of one type: an instance says which, unless one is named
// after the type; and every wiring gives a member the same kind (§2.4).
func TestSeveralWiringsOfOneType(t *testing.T) {
	opc := `
wirings:
  - name: PumpUA
    type: Pump
    params: { endpoint: null }
    sources: { ua: { driver: opcua-client, endpoint: "{endpoint}" } }
    members:
      Running:      { from: ua, node: "ns=2;s=Running" }
      Fault:        { from: ua, node: "ns=2;s=Fault" }
      Speed:        { from: ua, node: "ns=2;s=Speed" }
      Healthy:      { expr: "Running" }
      RunHours:     { memory: retained }
      Cmd.Start:    { to: ua, node: "ns=2;s=Start" }
      Cmd.Stop:     { to: ua, node: "ns=2;s=Stop" }
      Cmd.SpeedRef: { to: ua, node: "ns=2;s=Ref", readback: Speed }
`
	if got := problems(t, Env{}, pump, opc); got != "" {
		t.Errorf("two consistent wirings of Pump are refused:\n%s", got)
	}
	// Neither named Pump: an instance must say which.
	renamed := strings.Replace(pump, "wirings:\n  - type: Pump", "wirings:\n  - name: PumpModbus\n    type: Pump", 1)
	got := problems(t, Env{}, renamed, opc)
	if !strings.Contains(got, "type Pump has several wirings (PumpModbus, PumpUA) and none is named Pump: say which with wiring:") {
		t.Errorf("got:\n%s", got)
	}
	// Naming one resolves it.
	named := strings.Replace(renamed, "- { name: P101, type: Pump,", "- { name: P101, type: Pump, wiring: PumpModbus,", 1)
	named = strings.Replace(named, "- { name: P102, type: Pump,", "- { name: P102, type: Pump, wiring: PumpModbus,", 1)
	if got := problems(t, Env{}, named, opc); got != "" {
		t.Errorf("named wirings still refused:\n%s", got)
	}

	// The second wiring makes Speed a command: a member has one kind.
	diff := strings.Replace(opc, `Speed:        { from: ua, node: "ns=2;s=Speed" }`, `Speed:        { to: ua, node: "ns=2;s=Speed" }`, 1)
	got = problems(t, Env{}, pump, diff)
	if !strings.Contains(got, `member "Speed" of type Pump is to in wiring "PumpUA" but from in "Pump": a member has one kind per type`) {
		t.Errorf("got:\n%s", got)
	}
	// A member the second leaves out is retained memory, which differs from
	// a from: member of the first.
	left := strings.Replace(opc, "      Fault:        { from: ua, node: \"ns=2;s=Fault\" }\n", "", 1)
	got = problems(t, Env{}, pump, left)
	if !strings.Contains(got, `member "Fault" of type Pump is memory (retained) in wiring "PumpUA" but from in "Pump"`) || !strings.Contains(got, "here it is left out, which makes it retained memory") {
		t.Errorf("got:\n%s", got)
	}
	// Memory modes differ.
	mode := strings.Replace(opc, "RunHours:     { memory: retained }", "RunHours:     { memory: initial }", 1)
	if got := problems(t, Env{}, pump, mode); !strings.Contains(got, `member "RunHours" of type Pump is memory (initial) in wiring "PumpUA" but memory (retained) in "Pump"`) {
		t.Errorf("got:\n%s", got)
	}
	// One Template definition has one version.
	v1 := strings.Replace(pump, "  - type: Pump", "  - type: Pump\n    version: \"2\"", 1)
	v2 := strings.Replace(opc, "type: Pump", "type: Pump\n    version: \"3\"", 1)
	if got := problems(t, Env{}, v1, v2); !strings.Contains(got, `type Pump has version "3" in wiring "PumpUA" and "2" in "Pump": a type publishes one Template definition`) {
		t.Errorf("got:\n%s", got)
	}
}

// With the project's types, a member the type lacks is refused with a
// did-you-mean (design §15).
func TestUnknownMembersAgainstTheType(t *testing.T) {
	paths := []string{"Running", "Fault", "Speed", "Healthy", "RunHours", "Cmd", "Cmd.Start", "Cmd.Stop", "Cmd.SpeedRef"}
	env := Env{Type: func(n string) ([]string, bool) { return paths, n == "Pump" }}
	if got := problems(t, env, pump); got != "" {
		t.Errorf("a wiring that matches its type is refused:\n%s", got)
	}
	typo := edit("Speed:        { from: drive,", "Sped:         { from: drive,")
	got := problems(t, env, typo)
	if !strings.Contains(got, `type Pump has no member "Sped" (did you mean Speed?)`) {
		t.Errorf("got:\n%s", got)
	}
	// An array index is not part of the type's paths.
	idx := edit("Speed:        { from: drive,", "Speed[2]:     { from: drive,")
	if got := problems(t, env, idx); strings.Contains(got, "no member") {
		t.Errorf("an indexed member is looked up without its index:\n%s", got)
	}
	// A wiring for a type the project does not declare.
	other := strings.Replace(tank, "type: Tank", "type: Pump2", 1)
	if got := problems(t, env, other); !strings.Contains(got, `is for type "Pump2", which the project does not declare`) {
		t.Errorf("got:\n%s", got)
	}
}

func TestProblemsAreOrderedAndNamed(t *testing.T) {
	src := edit("Running:      { from: drive,", "Running:      { from: drve,")
	src = strings.Replace(src, "Fault:        { from: drive,", "Fault:        { from: drve,", 1)
	ps := Check(decode(t, src), Env{})
	if len(ps) != 2 || ps[0].Pos.Line >= ps[1].Pos.Line {
		t.Fatalf("problems = %+v, want two, by line", ps)
	}
	if err := Err(ps); err == nil || strings.Count(err.Error(), "\n") != 1 {
		t.Errorf("Err should join them: %v", err)
	}
	if Err(nil) != nil {
		t.Error("no problems is no error")
	}
}

func TestParamPublishForm(t *testing.T) {
	f, err := Decode("f", []byte("wirings:\n  - type: T\n    params: { a: { default: 1, publish: false }, b: { publish: true }, c: 3 }\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := f.Wirings[0].Params
	if p["a"].Default != 1 || p["a"].Publish == nil || *p["a"].Publish || p["a"].Required() {
		t.Errorf("a = %+v", p["a"])
	}
	if !p["b"].Required() || p["b"].Publish == nil || !*p["b"].Publish {
		t.Errorf("b = %+v", p["b"])
	}
	if p["c"].Default != 3 {
		t.Errorf("c = %+v", p["c"])
	}
}

// Each list of keys the schema is held to is exactly what its decoder names:
// a key in the list that lands in Addr or Keys is a key the decoder does not
// know, and a key the decoder knows that is not listed is one the editor
// would squiggle.
func TestKeyListsMatchTheDecoders(t *testing.T) {
	val := map[string]string{"event": "true", "publish": "true"}
	one := func(k string) string {
		v := "x"
		if s, ok := val[k]; ok {
			v = s
		}
		return k + ": " + v
	}
	for _, k := range MemberKeys {
		f, err := Decode("f", []byte("wirings:\n  - type: T\n    members:\n      A: { "+one(k)+" }\n"))
		if err != nil {
			t.Fatalf("member key %s: %v", k, err)
		}
		if m := f.Wirings[0].Members["A"]; len(m.Addr) != 0 {
			t.Errorf("MemberKeys lists %q, but the decoder passes it to the driver as an address key", k)
		}
	}
	for _, k := range SourceKeys {
		f, _ := Decode("f", []byte("wirings:\n  - type: T\n    sources:\n      s: { "+one(k)+" }\n"))
		if len(f.Wirings[0].Sources["s"].Keys) != 0 {
			t.Errorf("SourceKeys lists %q, but it is passed on as a driver key", k)
		}
	}
	for _, k := range FillKeys {
		f, _ := Decode("f", []byte("wirings:\n  - type: T\n    fill: { "+one(k)+" }\n"))
		if len(f.Wirings[0].Fill.Keys) != 0 {
			t.Errorf("FillKeys lists %q, but it is passed on as a driver key", k)
		}
	}
	for _, k := range ParamKeys {
		if _, err := Decode("f", []byte("wirings:\n  - type: T\n    params: { a: { "+one(k)+" } }\n")); err != nil {
			t.Errorf("ParamKeys lists %q, but the decoder refuses it: %v", k, err)
		}
	}
	for _, k := range TuningKeys {
		if _, err := Decode("f", []byte("instances:\n  - { name: P, type: T, alarms: { A: { "+one(k)+" } } }\n")); err != nil {
			t.Errorf("TuningKeys lists %q, but the decoder refuses it: %v", k, err)
		}
	}
}
