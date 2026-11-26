package project

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/runtime"
)

type testClock struct{ t time.Time }

func (c *testClock) Now() time.Time { return c.t }

const deviceAlarmManifest = `
tasks:
  - program: program.st
devices:
  - { name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9, desc: Conveyor drive, points: [Tripped, Current] }
alarms:
`

func deviceAlarmFS(t *testing.T, alarms string) fstest.MapFS {
	t.Helper()
	prof, err := os.ReadFile("../../devprofile/profiles/abb-acs355-fmbt21.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fsys := alarmFS(deviceAlarmManifest+alarms, nil)
	fsys["profiles/abb-acs355-fmbt21.yaml"] = &fstest.MapFile{Data: prof}
	return fsys
}

// A profile's alarm becomes a rule over the device type, enabled while the
// device answers, high priority as the profile says, named from the row.
func TestProfileAlarmBecomesADeviceRule(t *testing.T) {
	proj, rt := loadAlarms(t, deviceAlarmFS(t, "  defs: []\n"))
	defs, err := proj.AlarmDefs(rt)
	if err != nil {
		t.Fatal(err)
	}
	var d *alarm.Def
	for i := range defs {
		if defs[i].Tag == "CONV.Tripped" {
			d = &defs[i]
		}
	}
	if d == nil || len(defs) != 1 {
		t.Fatalf("defs %+v, want only CONV.Tripped", defs)
	}
	if d.Priority != alarm.High || d.Enable != "CONV__Online" || d.Name != "Conveyor drive tripped" || d.Class != "drive" || !strings.Contains(d.Source, "devices:") {
		t.Errorf("def %+v", d)
	}
}

// A row that does not select the point has no alarm on it, and a project
// rule on the same member comes first: it, not the profile's, makes the alarm.
func TestProjectRuleBeatsTheProfilesAndUnselectedPointsHaveNone(t *testing.T) {
	proj, rt := loadAlarms(t, deviceAlarmFS(t, `  rules:
    - match: { type: ABB_ACS355_FMBT21, member: Tripped }
      priority: low
      name: "drive trip"
`))
	defs, err := proj.AlarmDefs(rt)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Priority != alarm.Low || defs[0].Name != "drive trip" || defs[0].Source == "" || strings.Contains(defs[0].Source, "devices:") {
		t.Fatalf("defs %+v, want the project rule's alarm alone", defs)
	}

	fsys := deviceAlarmFS(t, "  defs: []\n")
	fsys["nautilus.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(string(fsys["nautilus.yaml"].Data), "points: [Tripped, Current]", "points: [Current]", 1))}
	proj, rt = loadAlarms(t, fsys)
	if defs, err = proj.AlarmDefs(rt); err != nil || len(defs) != 0 {
		t.Errorf("a row without Tripped has alarms %+v (%v)", defs, err)
	}
}

// An id the project declares by hand and a device rule generates is the
// ordinary duplicate-id error, naming both.
func TestHandWrittenAlarmWithTheDevicesIDIsADuplicate(t *testing.T) {
	proj, rt := loadAlarms(t, deviceAlarmFS(t, "  defs:\n    - { id: CONV.Tripped, tag: CONV.Tripped }\n"))
	_, err := proj.AlarmDefs(rt)
	if err == nil || !strings.Contains(err.Error(), "devices:") || !strings.Contains(err.Error(), "CONV.Tripped") {
		t.Errorf("err %v, want a duplicate naming the devices: rule and the project's alarm", err)
	}
}

// The reason for the suppression: the device drops off, its own alarm
// clears (suppressed, not frozen on), and the comms-loss alarm raises, so
// the operator is never looking at a healthy screen. It clears when the
// device answers again, and the device's alarm comes back.
func TestOfflineDeviceSuppressesItsAlarmsAndRaisesOne(t *testing.T) {
	proj, err := Load(deviceAlarmFS(t, "  defs: []\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	clk := &testClock{t: time.Unix(1_800_000_000, 0)}
	proj.Runtime.Clock = clk
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := proj.AlarmEngine(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	active := func() map[string]bool {
		out := map[string]bool{}
		for _, r := range eng.Active() {
			if r.Cond { // in alarm now, not an unacknowledged return to normal
				out[r.ID] = true
			}
		}
		return out
	}
	step := func() { rt.Scan() }

	rt.Tags().Set("CONV__Online", true)
	if err := rt.Tags().SetPath("CONV.Tripped", true); err != nil {
		t.Fatal(err)
	}
	step()
	if a := active(); !a["CONV.Tripped"] || a["CONV__Offline"] {
		t.Fatalf("online and tripped: active %v, want only CONV.Tripped", a)
	}

	rt.Tags().Set("CONV__Online", false)
	step()
	// the comms alarm's on-delay is walked on the runtime's clock
	for range 12 {
		clk.t = clk.t.Add(time.Second)
		step()
	}
	got := active()
	if got["CONV.Tripped"] || !got["CONV__Offline"] {
		t.Fatalf("offline: active %v, want CONV__Offline and not CONV.Tripped", got)
	}

	rt.Tags().Set("CONV__Online", true)
	step()
	got = active()
	if !got["CONV.Tripped"] || got["CONV__Offline"] {
		t.Errorf("answering again: active %v, want CONV.Tripped back and the comms alarm cleared", got)
	}
}

// devprofile cannot import alarm, so it keeps its own list of priority
// names; this keeps the two the same.
func TestProfileAlarmPrioritiesAreTheAlarmPackages(t *testing.T) {
	for _, n := range devprofile.AlarmPriorities {
		if _, err := alarm.ParsePriority(n); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	if len(devprofile.AlarmPriorities) != int(alarm.Critical)+1 {
		t.Errorf("devprofile lists %d priorities, alarm has %d", len(devprofile.AlarmPriorities), int(alarm.Critical)+1)
	}
}

// offline-alarm: tunes the comms-loss alarm's priority and on-delay, or
// turns it off on purpose; a typo or a bad value is an error, never a
// silent "off", and off is a warning on every check.
func TestOfflineAlarmRowKey(t *testing.T) {
	row := func(extra string) string {
		return strings.Replace(deviceAlarmManifest, "points: [Tripped, Current] }", "points: [Tripped, Current]"+extra+" }", 1)
	}
	load := func(m string) (*Project, *runtime.Runtime, error) {
		fsys := deviceAlarmFS(t, "  defs: []\n")
		fsys["nautilus.yaml"] = &fstest.MapFile{Data: []byte(m + "  defs: []\n")}
		proj, err := Load(fsys, "")
		if err != nil {
			return nil, nil, err
		}
		rt, err := runtime.New(proj.Runtime)
		return proj, rt, err
	}
	comms := func(proj *Project, rt *runtime.Runtime) []SystemAlarm { return proj.deviceCommsAlarms(rt) }

	proj, rt, err := load(row(", offline-alarm: { priority: low, on-delay: 30s }"))
	if err != nil {
		t.Fatal(err)
	}
	if c := comms(proj, rt); len(c) != 1 || c[0].Def.Priority != alarm.Low || c[0].Def.OnDelay != 30*time.Second {
		t.Errorf("tuned comms alarm %+v", c)
	}
	proj, rt, err = load(row(", offline-alarm: false"))
	if err != nil {
		t.Fatal(err)
	}
	if c := comms(proj, rt); len(c) != 0 {
		t.Errorf("offline-alarm: false still raises %+v", c)
	}
	_, _, warns, err := proj.CheckAlarms(rt)
	if err != nil || len(warns) != 1 || !strings.Contains(warns[0], "device CONV: offline-alarm is off") {
		t.Errorf("warns %v (%v), want one saying CONV's offline alarm is off", warns, err)
	}
	proj, rt, _ = load(row(", offline-alarm: true"))
	if c := comms(proj, rt); len(c) != 1 || c[0].Def.Priority != alarm.High {
		t.Errorf("offline-alarm: true is not the default: %+v", c)
	}
	for bad, want := range map[string]string{
		", offline-alarm: nope":                 "want false, true or",
		", offline-alarm: { priorty: low }":     "priorty",
		", offline-alarm: { priority: urgent }": "priority",
		", offline-alarm: { on-delay: soon }":   "on-delay",
	} {
		if _, _, err := load(row(bad)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err %v, want it to name %q", bad, err, want)
		}
	}
}

// Taking the device out of service (#715) suppresses its comms-loss alarm
// with the rest of its equipment: the alarm's equipment is the device.
func TestOutOfServiceDeviceSuppressesItsOfflineAlarmToo(t *testing.T) {
	proj, err := Load(deviceAlarmFS(t, "  defs: []\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	clk := &testClock{t: time.Unix(1_800_000_000, 0)}
	proj.Runtime.Clock = clk
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := proj.AlarmEngine(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	rt.Tags().Set("CONV__Online", false)
	for range 12 {
		clk.t = clk.t.Add(time.Second)
		rt.Scan()
	}
	inAlarm := func() bool {
		for _, r := range eng.Active() {
			if r.ID == "CONV__Offline" && r.Cond {
				return true
			}
		}
		return false
	}
	if !inAlarm() {
		t.Fatal("the comms alarm did not raise")
	}
	if err := eng.TakeOutOfService("CONV", clk.t.Add(time.Hour), "test", "commissioning"); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(time.Second)
	rt.Scan()
	if inAlarm() {
		t.Error("the comms alarm stayed up for a device taken out of service")
	}
}

// A device that has not answered once since boot has no <Dev>__Online value
// at all (a typed input is unseeded until its driver delivers). It is not
// known to be answering, its own alarms are suppressed (enable: reads that
// tag, and an unreadable enable suppresses), so the comms alarm must raise
// after its on-delay: never a quiet screen.
func TestDeviceThatNeverAnswersRaisesItsOfflineAlarm(t *testing.T) {
	proj, err := Load(deviceAlarmFS(t, "  defs: []\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	clk := &testClock{t: time.Unix(1_800_000_000, 0)}
	proj.Runtime.Clock = clk
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := proj.AlarmEngine(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	// The companion exists as a BOOL from load, false until the driver says
	// otherwise: that is what this test rides on.
	if v, ok := rt.Tags().ReadPath("CONV__Online"); !ok || v != false {
		t.Fatalf("CONV__Online at boot = %v, %v; want a seeded false", v, ok)
	}
	raised := func() bool {
		for _, r := range eng.Active() {
			if r.ID == "CONV__Offline" && r.Cond {
				return true
			}
		}
		return false
	}
	rt.Scan()
	if raised() {
		t.Fatal("raised before the on-delay")
	}
	for range 12 {
		clk.t = clk.t.Add(time.Second)
		rt.Scan()
	}
	if !raised() {
		t.Fatal("a device that never answered since boot raised no comms alarm")
	}
	rt.Tags().Set("CONV__Online", true)
	clk.t = clk.t.Add(time.Second)
	rt.Scan()
	if raised() {
		t.Error("still raised once the device answers")
	}
}

// And if the companion is ever missing or not a BOOL (a rename, a driver that
// never created it), the alarm fails active: it reads as not answering.
func TestOfflineAlarmFailsActiveWhenTheCompanionIsMissing(t *testing.T) {
	withDevices, err := Load(deviceAlarmFS(t, "  defs: []\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	without, rt := loadAlarms(t, alarmFS(alarmTags+"alarms:\n  defs: []\n", nil)) // no CONV__Online here
	_ = without
	c := withDevices.deviceCommsAlarms(rt)
	if len(c) != 1 || !c[0].Active() {
		t.Fatalf("with no CONV__Online tag the comms alarm is quiet: %+v", c)
	}
}
