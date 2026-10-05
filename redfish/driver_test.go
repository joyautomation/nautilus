package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/hw"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/redfish/mockup"
)

// The driver tests run against redfish/testdata/subsystem-1u (a 2021+
// ThermalSubsystem/PowerSubsystem 1U: 6 fans, 2 PSUs, CPU/inlet/exhaust
// sensors with thresholds) served by the in-repo mockup on 127.0.0.1:0,
// with a hand-written manifest so the driver is tested apart from the
// importer (codegen's tests run the generated manifests).

const subsystemManifest = `
sources:
  - id: NODE1
    host: %s
    user: admin
    password-env: RF_DRIVER_PASSWORD
    interval: 40ms
    stale-after: 250ms
    timeout: 1s
tags:
  - name: NODE1
    type: Server
    source: NODE1
    resource: /redfish/v1/Systems/1
    members:
      Online: {path: Id, exists: true}
      PowerOn: {path: PowerState, eq: "On"}
      Health: {path: Status.Health, map: {OK: 0, Warning: 1, Critical: 2}}
      Fault: {derived: "Health == 2"}
      Warning: {derived: "Health == 1"}
      Model: {path: Model}
      MaxTempC: {resource: /redfish/v1/Chassis/1/ThermalSubsystem/ThermalMetrics, path: "TemperatureReadingsCelsius[*].Reading", agg: max}
      PowerW: {resource: /redfish/v1/Chassis/1/EnvironmentMetrics, path: PowerWatts.Reading}
      FanCount: {const: 1}
  - name: NODE1_Fan1
    type: Fan
    source: NODE1
    resource: /redfish/v1/Chassis/1/ThermalSubsystem/Fans/Fan1
    members:
      Name: {path: Name}
      Present: {path: Status.State, map: {Absent: false, Enabled: true, Disabled: true}}
      RPM: {path: SpeedPercent.SpeedRPM}
      Pct: {path: SpeedPercent.Reading}
      Fault: {path: Status.Health, map: {OK: false, Warning: true, Critical: true}}
  - name: NODE1_PSU1
    type: PSU
    source: NODE1
    resource: /redfish/v1/Chassis/1/PowerSubsystem/PowerSupplies/PSU1
    members:
      Name: {path: Name}
      InputOk: {path: LineInputStatus, map: {Normal: true, LossOfInput: false, OutOfRange: false}}
      InputV: {resource: /redfish/v1/Chassis/1/PowerSubsystem/PowerSupplies/PSU1/Metrics, path: InputVoltage.Reading}
      CapacityW: {path: PowerCapacityWatts}
  - name: NODE1_Temp_CPU1
    type: TempSensor
    source: NODE1
    resource: /redfish/v1/Chassis/1/Sensors/CPU1Temp
    members:
      Name: {path: Name}
      Value: {path: Reading}
      Fault: {path: Reading, exists: false}
      HighSP: {path: Thresholds.UpperCaution.Reading}
      HighHighSP: {path: Thresholds.UpperCritical.Reading}
      High: {derived: "HighSP > 0 && Value >= HighSP"}
      HighHigh: {derived: "HighHighSP > 0 && Value >= HighHighSP"}
`

const driverPassword = "driver-test-password"

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// standIn serves a testdata fixture with session auth.
func standIn(t *testing.T, fixture string) *mockup.Server {
	t.Helper()
	tree, err := mockup.LoadDir("testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	srv := mockup.New(tree, mockup.Options{Auth: mockup.AuthSession, User: "admin", Password: driverPassword})
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

func manifestFor(t *testing.T, tmpl, url string, extra string) Manifest {
	t.Helper()
	m, err := ParseManifest([]byte(fmt.Sprintf(tmpl, url) + extra))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func startDriver(t *testing.T, d *Driver) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	t.Cleanup(func() { cancel(); d.Stop() })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func member(t *testing.T, vals nio.Values, tag, m string) ir.Value {
	t.Helper()
	v, ok := vals[tag].(ir.Value)
	if !ok {
		t.Fatalf("%s not delivered (%T)", tag, vals[tag])
	}
	i, ok := v.Struct.FieldIndex[m]
	if !ok {
		t.Fatalf("%s has no member %s", tag, m)
	}
	return v.Fld[i]
}

func online(d *Driver) bool {
	v, _ := d.ReadInputs()
	return v["NODE1__Online"] == true
}

func TestDriverAgainstStandIn(t *testing.T) {
	t.Setenv("RF_DRIVER_PASSWORD", driverPassword)
	srv := standIn(t, "subsystem-1u")
	d, err := New(manifestFor(t, subsystemManifest, srv.URL(), ""), WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.InputNames(), ","); got != "NODE1,NODE1_Fan1,NODE1_PSU1,NODE1_Temp_CPU1,NODE1__LastPollMs,NODE1__Online" {
		t.Fatalf("InputNames = %s", got)
	}
	startDriver(t, d)
	waitFor(t, "first poll", func() bool { return online(d) })

	vals, err := d.ReadInputs()
	if err != nil {
		t.Fatal(err)
	}
	// Types and the shared StructDef (the store's identity check).
	for tag, typ := range map[string]string{"NODE1": "Server", "NODE1_Fan1": "Fan", "NODE1_PSU1": "PSU", "NODE1_Temp_CPU1": "TempSensor"} {
		if v := vals[tag].(ir.Value); v.Struct != hw.StructDef(typ) {
			t.Fatalf("%s does not carry the %s StructDef", tag, typ)
		}
	}
	checks := []struct {
		tag, m string
		want   ir.Value
	}{
		{"NODE1", "Online", ir.BoolVal(true)},
		{"NODE1", "PowerOn", ir.BoolVal(true)},
		{"NODE1", "Health", ir.IntVal(0)},
		{"NODE1", "Fault", ir.BoolVal(false)},
		{"NODE1", "Model", ir.StringVal("R1U-S")},
		{"NODE1", "MaxTempC", ir.RealVal(55)},
		{"NODE1", "PowerW", ir.RealVal(187)},
		{"NODE1", "FanCount", ir.IntVal(1)},
		{"NODE1_Fan1", "Name", ir.StringVal("Fan 1")},
		{"NODE1_Fan1", "Present", ir.BoolVal(true)},
		{"NODE1_Fan1", "RPM", ir.RealVal(6100)},
		{"NODE1_Fan1", "Pct", ir.RealVal(42)},
		{"NODE1_PSU1", "InputOk", ir.BoolVal(true)},
		{"NODE1_PSU1", "InputV", ir.RealVal(230.5)},
		{"NODE1_PSU1", "CapacityW", ir.RealVal(800)},
		{"NODE1_Temp_CPU1", "Value", ir.RealVal(55)},
		{"NODE1_Temp_CPU1", "Fault", ir.BoolVal(false)},
		{"NODE1_Temp_CPU1", "HighSP", ir.RealVal(85)},
		{"NODE1_Temp_CPU1", "High", ir.BoolVal(false)},
	}
	for _, c := range checks {
		if got := member(t, vals, c.tag, c.m); got.Kind != c.want.Kind || got.B != c.want.B || got.I != c.want.I || got.F != c.want.F || got.S != c.want.S {
			t.Errorf("%s.%s = %+v, want %+v", c.tag, c.m, got, c.want)
		}
	}
	if q := d.Quality(); len(q) != 0 {
		t.Fatalf("quality after a clean poll = %v", q)
	}
	// Each resource is fetched once per poll however many members read it:
	// the Server's five Systems/1 members cost one GET.
	sys, fan := srv.Gets("/redfish/v1/Systems/1"), srv.Gets("/redfish/v1/Chassis/1/ThermalSubsystem/Fans/Fan1")
	if sys == 0 || sys > fan+1 || fan > sys+1 {
		t.Fatalf("GETs: Systems/1 %d, Fan1 %d — want one per poll each", sys, fan)
	}
	if h := d.Health(); len(h.Sources) != 1 || h.Sources[0].State != "connected" || !h.Sources[0].Fresh {
		t.Fatalf("health = %+v", h)
	}

	// A sensor whose Reading goes null: Value holds, Fault (exists: false)
	// turns true, the tag stays Good — one dead sensor must not grey out
	// its siblings — and Absent names it.
	if err := srv.Patch("/redfish/v1/Chassis/1/Sensors/CPU1Temp", "Reading", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "sensor fault", func() bool {
		v, _ := d.ReadInputs()
		return member(t, v, "NODE1_Temp_CPU1", "Fault").B
	})
	vals, _ = d.ReadInputs()
	if member(t, vals, "NODE1_Temp_CPU1", "Value").F != 55 {
		t.Fatal("Value must hold its last reading")
	}
	if q := d.Quality(); len(q) != 0 {
		t.Fatalf("a null reading made tags non-Good: %v", q)
	}
	if a := d.Absent(); strings.Join(a, ",") != "NODE1_Temp_CPU1.Value" {
		t.Fatalf("Absent = %v", a)
	}
	_ = srv.Patch("/redfish/v1/Chassis/1/Sensors/CPU1Temp", "Reading", 96)
	waitFor(t, "reading back, above both thresholds", func() bool {
		v, _ := d.ReadInputs()
		return member(t, v, "NODE1_Temp_CPU1", "HighHigh").B && !member(t, v, "NODE1_Temp_CPU1", "Fault").B
	})

	// One resource 404s: exactly the tag bound to it reads Bad; siblings
	// stay Good; the source stays connected.
	srv.Fail("/redfish/v1/Chassis/1/ThermalSubsystem/Fans/Fan1", 404)
	waitFor(t, "Fan1 Bad", func() bool { return d.Quality()["NODE1_Fan1"] == nio.Bad })
	q := d.Quality()
	if len(q) != 1 || !online(d) {
		t.Fatalf("a 404 on one resource: quality %v, online %v", q, online(d))
	}
	srv.Fail("/redfish/v1/Chassis/1/ThermalSubsystem/Fans/Fan1", 0)
	waitFor(t, "Fan1 Good again", func() bool { return len(d.Quality()) == 0 })

	// The BMC goes away: Stale, __Online false, values hold, reads never
	// error.
	srv.Stop()
	waitFor(t, "stale", func() bool { return d.Quality()["NODE1"] == nio.Stale && !online(d) })
	vals, err = d.ReadInputs()
	if err != nil || member(t, vals, "NODE1_Fan1", "RPM").F != 6100 {
		t.Fatalf("values must hold while down: %v %v", err, vals["NODE1_Fan1"])
	}
	if h := d.Health(); h.Sources[0].State != "error" || h.Sources[0].LastError == "" || strings.Contains(h.Sources[0].LastError, driverPassword) {
		t.Fatalf("health while down = %+v", h.Sources[0])
	}

	// It comes back — rebooted, so our session token is dead: the client
	// logs in again on the 401 and the source recovers.
	if err := srv.Restart(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "recovery", func() bool { return online(d) && len(d.Quality()) == 0 })
	if srv.Logins() < 2 || d.AuthMode("NODE1") != AuthSession {
		t.Fatalf("logins = %d, auth %s: want a fresh session after the reboot", srv.Logins(), d.AuthMode("NODE1"))
	}

	// Stop logs the session out.
	d.Stop()
	if s := srv.Sessions(); len(s) != 0 {
		t.Fatalf("sessions after Stop = %v", s)
	}
}

func TestDriverEnableParks(t *testing.T) {
	t.Setenv("RF_DRIVER_PASSWORD", driverPassword)
	srv := standIn(t, "subsystem-1u")
	m := manifestFor(t, subsystemManifest, srv.URL(), "")
	m.Sources[0].Enable = "CFG_PollNode1"
	d, err := New(m, WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.OutputNames(), ","); got != "CFG_PollNode1" {
		t.Fatalf("OutputNames = %s", got)
	}
	startDriver(t, d)
	time.Sleep(100 * time.Millisecond)
	if h := d.Health(); h.Sources[0].State != "parked" || srv.Gets("/redfish/v1/Systems/1") != 0 {
		t.Fatalf("enable unset: %+v, %d GETs", h.Sources[0], srv.Gets("/redfish/v1/Systems/1"))
	}
	if q := d.Quality(); q["NODE1"] != nio.NotConnected {
		t.Fatalf("parked before any poll: %v", q)
	}
	_ = d.WriteOutputs(nio.Values{"CFG_PollNode1": true})
	waitFor(t, "poll after enable", func() bool { return online(d) })
	_ = d.WriteOutputs(nio.Values{"CFG_PollNode1": false})
	waitFor(t, "parked", func() bool { return d.Health().Sources[0].State == "parked" })
	n := srv.Gets("/redfish/v1/Systems/1")
	time.Sleep(120 * time.Millisecond)
	if srv.Gets("/redfish/v1/Systems/1") != n || online(d) || d.Quality()["NODE1"] != nio.Stale {
		t.Fatal("a parked source must not poll, and reads Stale")
	}
}

const powerWrite = `writes:
  - name: NODE1_PowerCmd
    tag: NODE1
    member: PowerOn
    target: /redfish/v1/Systems/1/Actions/ComputerSystem.Reset
`

// A PowerCmd reaches the wire exactly once per change to non-zero, and
// never as its baseline (the first value the runtime hands over after
// Start describes the world, it does not command it).
func TestDriverPowerCommand(t *testing.T) {
	t.Setenv("RF_DRIVER_PASSWORD", driverPassword)
	srv := standIn(t, "subsystem-1u")
	d, err := New(manifestFor(t, subsystemManifest, srv.URL(), powerWrite), WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.OutputNames(), ","); got != "NODE1_PowerCmd" {
		t.Fatalf("OutputNames = %s", got)
	}
	startDriver(t, d)
	waitFor(t, "first poll", func() bool { return online(d) })

	// Baseline: a non-zero first value is NOT sent.
	_ = d.WriteOutputs(nio.Values{"NODE1_PowerCmd": int64(2)})
	time.Sleep(150 * time.Millisecond)
	if a := srv.Actions(); len(a) != 0 {
		t.Fatalf("the baseline was sent: %v", a)
	}
	// Back to 0: a change, but to zero — the program resetting its
	// command is not a command.
	_ = d.WriteOutputs(nio.Values{"NODE1_PowerCmd": int64(0)})
	time.Sleep(150 * time.Millisecond)
	if a := srv.Actions(); len(a) != 0 {
		t.Fatalf("a return to 0 was sent: %v", a)
	}
	// ForceOff: exactly one POST, even when re-handed unchanged.
	_ = d.WriteOutputs(nio.Values{"NODE1_PowerCmd": int64(3)})
	_ = d.WriteOutputs(nio.Values{"NODE1_PowerCmd": int64(3)})
	waitFor(t, "the reset POST", func() bool { return len(srv.Actions()) > 0 })
	time.Sleep(150 * time.Millisecond)
	a := srv.Actions()
	if len(a) != 1 || a[0].Path != "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset" || a[0].Body["ResetType"] != "ForceOff" {
		t.Fatalf("actions = %+v", a)
	}
	// The read-back follows: PowerOn false on a later poll.
	waitFor(t, "PowerOn read-back", func() bool {
		v, _ := d.ReadInputs()
		return !member(t, v, "NODE1", "PowerOn").B
	})
	// An out-of-range command is refused with its error on the row, and
	// nothing is posted.
	refused := func() bool { return strings.Contains(d.Health().Sources[0].LastError, "want 0 (none)") }
	_ = d.WriteOutputs(nio.Values{"NODE1_PowerCmd": int64(9)})
	waitFor(t, "refusal on the row", refused)
	if len(srv.Actions()) != 1 {
		t.Fatal("an invalid command reached the wire")
	}
	// The refusal is latched: good polls after it must not wipe it off
	// the row before an operator has looked.
	n := srv.Gets("/redfish/v1/Systems/1")
	waitFor(t, "three more polls", func() bool { return srv.Gets("/redfish/v1/Systems/1") >= n+3 })
	if !refused() {
		t.Fatalf("a refused command left the row after good polls: %+v", d.Health().Sources[0])
	}
	// A return to 0 sends nothing and does not clear it either.
	_ = d.WriteOutputs(nio.Values{"NODE1_PowerCmd": int64(0)})
	n = srv.Gets("/redfish/v1/Systems/1")
	waitFor(t, "two more polls", func() bool { return srv.Gets("/redfish/v1/Systems/1") >= n+2 })
	if !refused() {
		t.Fatalf("a return to 0 cleared the refusal: %+v", d.Health().Sources[0])
	}
	// The next accepted command clears it.
	_ = d.WriteOutputs(nio.Values{"NODE1_PowerCmd": int64(1)})
	waitFor(t, "On", func() bool { return len(srv.Actions()) == 2 })
	if a := srv.Actions(); a[1].Body["ResetType"] != "On" {
		t.Fatalf("second action = %+v", a[1])
	}
	waitFor(t, "row cleared by an accepted command", func() bool { return d.Health().Sources[0].LastError == "" })
}

// ── no socket: the poll through a fake Fetcher ──────────────────────────

type fakeFetcher struct {
	mu     sync.Mutex
	bodies map[string]string
	status map[string]int
	err    error
	gets   []string
}

func (f *fakeFetcher) Get(_ context.Context, path string) (Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets = append(f.gets, path)
	if f.err != nil {
		return Response{}, f.err
	}
	if st, ok := f.status[path]; ok {
		return Response{Status: st, Body: []byte(`{"error":{"message":"injected"}}`)}, nil
	}
	b, ok := f.bodies[path]
	if !ok {
		return Response{Status: 404}, nil
	}
	return Response{Status: 200, Body: []byte(b)}, nil
}

func (f *fakeFetcher) Post(context.Context, string, any) (Response, error) {
	return Response{Status: 204}, nil
}

func (f *fakeFetcher) Close(context.Context) {}

const fakeManifest = `
sources:
  - id: S
    host: https://bmc
tags:
  - name: S
    type: Server
    source: S
    resource: /redfish/v1/Systems/1
    members:
      PowerOn: {path: PowerState, eq: "On"}
      MaxTempC: {resource: /redfish/v1/Chassis/1/Thermal, path: "Temperatures[*].ReadingCelsius", agg: max}
      PowerW: {resource: /redfish/v1/Chassis/1/Sensors/Energy, path: Reading, rate: true, scale: 3600000}
      UptimeS: {path: Big}
  - name: S_Fan1
    type: Fan
    source: S
    resource: /redfish/v1/Chassis/1/Thermal
    members:
      RPM: {path: "Fans[MemberId=0].Reading"}
      Present: {path: "Fans[MemberId=0].Status.State", map: {Enabled: true, Absent: false}}
  - name: S_Temp_CPU
    type: TempSensor
    source: S
    resource: /redfish/v1/Chassis/1/Sensors/CPU
    members:
      Value: {path: Reading}
`

func fakeDriver(t *testing.T, f *fakeFetcher, clock func() time.Time) *Driver {
	t.Helper()
	m, err := ParseManifest([]byte(fakeManifest))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m, WithLogger(quiet()), WithClock(clock), WithFetcher(func(Source) (Fetcher, error) { return f, nil }))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func updates(res hw.Result) map[string]ir.Value {
	out := map[string]ir.Value{}
	for _, u := range res.Updates {
		out[u.Tag+"."+u.Member] = u.Value
	}
	return out
}

func TestPollFetchesOnceDecodesAllAndRates(t *testing.T) {
	f := &fakeFetcher{bodies: map[string]string{
		"/redfish/v1/Systems/1":                `{"PowerState": "On", "Big": 4294967296}`,
		"/redfish/v1/Chassis/1/Thermal":        `{"Temperatures": [{"ReadingCelsius": 41}, {"ReadingCelsius": null}, {"ReadingCelsius": 57.5}], "Fans": [{"MemberId": "0", "Reading": 2100, "Status": {"State": "Enabled"}}]}`,
		"/redfish/v1/Chassis/1/Sensors/Energy": `{"Reading": 1000}`,
		"/redfish/v1/Chassis/1/Sensors/CPU":    `{"Reading": 44}`,
	}}
	t0 := time.Unix(1_800_000_000, 0)
	ticks := 0
	d := fakeDriver(t, f, func() time.Time { ticks++; return t0.Add(time.Duration(ticks) * 10 * time.Second) })
	ctx := context.Background()

	res, err := d.poll(ctx, "S", hw.DefaultClass)
	if err != nil {
		t.Fatal(err)
	}
	// Four resources, four GETs — Thermal feeds three members of two tags
	// and is fetched once.
	if len(f.gets) != 4 {
		t.Fatalf("GETs = %v", f.gets)
	}
	u := updates(res)
	if u["S.PowerOn"].B != true || u["S.MaxTempC"].F != 57.5 || u["S_Fan1.RPM"].F != 2100 || !u["S_Fan1.Present"].B || u["S_Temp_CPU.Value"].F != 44 {
		t.Fatalf("updates = %v", u)
	}
	// An integer beyond 32 bits survives as an integer.
	if u["S.UptimeS"].I != 4294967296 {
		t.Fatalf("UptimeS = %+v", u["S.UptimeS"])
	}
	// The first sample of a counter has no rate yet.
	if _, ok := u["S.PowerW"]; ok || len(res.Bad) != 0 {
		t.Fatalf("first poll delivered a rate: %v bad %v", u["S.PowerW"], res.Bad)
	}
	// +1 kWh over the 10 s the clock advanced = 360 kW.
	f.bodies["/redfish/v1/Chassis/1/Sensors/Energy"] = `{"Reading": 1001}`
	res, _ = d.poll(ctx, "S", hw.DefaultClass)
	if got := updates(res)["S.PowerW"]; got.F != 360000 {
		t.Fatalf("rate = %+v, want 360000", got)
	}

	// A 503 on the Thermal resource: both tags reading it are Bad; the
	// others' members still arrive.
	f.status = map[string]int{"/redfish/v1/Chassis/1/Thermal": 503}
	res, err = d.poll(ctx, "S", hw.DefaultClass)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Bad, ",") != "S,S_Fan1" {
		t.Fatalf("Bad = %v", res.Bad)
	}
	if _, ok := updates(res)["S_Temp_CPU.Value"]; !ok {
		t.Fatal("siblings must still deliver")
	}

	// A transport failure fails the poll, and the counter history is
	// dropped: the first poll back yields no rate.
	f.status = nil
	f.err = errors.New("connection refused")
	if _, err := d.poll(ctx, "S", hw.DefaultClass); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("transport failure: %v", err)
	}
	f.err = nil
	f.bodies["/redfish/v1/Chassis/1/Sensors/Energy"] = `{"Reading": 1002}`
	res, _ = d.poll(ctx, "S", hw.DefaultClass)
	if _, ok := updates(res)["S.PowerW"]; ok {
		t.Fatal("a rate across an outage")
	}

	// A value the binding's map does not know: logged, member left alone.
	f.bodies["/redfish/v1/Chassis/1/Thermal"] = `{"Temperatures": [], "Fans": [{"MemberId": "0", "Reading": 2000, "Status": {"State": "Quiesced"}}]}`
	res, _ = d.poll(ctx, "S", hw.DefaultClass)
	if _, ok := updates(res)["S_Fan1.Present"]; ok {
		t.Fatal("an unmapped enum value was delivered")
	}
	if _, ok := updates(res)["S.MaxTempC"]; ok {
		t.Fatal("max over no readings must not deliver")
	}
}

func TestToRawAndAggregate(t *testing.T) {
	doc, _ := mockup.Decode([]byte(`{"a": 18446744073709551615, "b": -3, "c": 2.5, "d": "x", "e": true}`))
	if r, _ := toRaw(doc["a"]); r.Kind != hw.RawUint || r.U != 18446744073709551615 {
		t.Fatalf("uint64 max: %+v", r)
	}
	if r, _ := toRaw(doc["b"]); r.Kind != hw.RawInt || r.I != -3 {
		t.Fatalf("int: %+v", r)
	}
	if r, _ := toRaw(doc["c"]); r.Kind != hw.RawFloat || r.F != 2.5 {
		t.Fatalf("float: %+v", r)
	}
	if _, ok := toRaw(map[string]any{}); ok {
		t.Fatal("an object is not a value")
	}
	vals := []any{json.Number("3"), json.Number("7.5"), "text", json.Number("-1")}
	for agg, want := range map[string]float64{"max": 7.5, "min": -1, "sum": 9.5} {
		if r, ok := aggregate(vals, agg); !ok || r.F != want {
			t.Errorf("%s = %+v", agg, r)
		}
	}
	if r, _ := aggregate(vals, "count"); r.I != 4 {
		t.Errorf("count = %+v", r)
	}
}
