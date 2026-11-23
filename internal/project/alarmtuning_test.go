package project

// The tuning overlay (#1249): delays a chattering alarm should run with, in
// a file of their own, applied over the composed definitions at start.

import (
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

const tuningManifest = alarmTags + `
alarms:
  tuning: alarms.tuning.yaml
  defs:
    - { id: intrusion, tag: RTU9_ZONE15_INT_001_YA, on-delay: 2s, off-delay: 3s }
    - { id: online-lost, tag: RTU9__Online, on-delay: 1s }
`

func TestTuningOverlaySetsTheDelaysItNamesAndOnlyThose(t *testing.T) {
	proj, rt := loadAlarms(t, alarmFS(tuningManifest, map[string]string{
		"alarms.tuning.yaml": `
tuning:
  - id: intrusion
    on-delay: 5s
    why: "7 bounces in 60 s become 1"
    from-journal: "2026-11-20/2026-11-22"
`}))
	defs, err := proj.AlarmDefs(rt)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]alarm.Def{}
	for _, d := range defs {
		by[d.ID] = d
	}
	if d := by["intrusion"]; d.OnDelay != 5*time.Second || d.OffDelay != 3*time.Second {
		t.Errorf("intrusion delays = %s/%s, want the overlay's 5s on and its own 3s off", d.OnDelay, d.OffDelay)
	}
	if d := by["online-lost"]; d.OnDelay != time.Second || d.OffDelay != 0 {
		t.Errorf("an alarm the overlay does not name changed: %s/%s", d.OnDelay, d.OffDelay)
	}
}

// A start gives the engine one tuned row per applied alarm, with the delays
// in force and the why.
func TestTuningOverlayReachesTheEngineAsATunedRow(t *testing.T) {
	proj, rt := loadAlarms(t, alarmFS(tuningManifest, map[string]string{
		"alarms.tuning.yaml": "tuning:\n  - { id: intrusion, on-delay: 5s, why: \"7 bounces in 60 s become 1\" }\n",
	}))
	a, err := proj.NewAlarms(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Engine.Evaluate()
	rows, err := a.Engine.Journal(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindTuned}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "intrusion" || rows[0].Name != "tuned: on-delay 5s, off-delay 3s" ||
		rows[0].Reason != "7 bounces in 60 s become 1" || rows[0].By != "system" {
		t.Fatalf("tuned rows = %+v, want one for intrusion with its delays and why", rows)
	}
}

func TestTuningOverlayRefusesWhatItCannotUse(t *testing.T) {
	for _, c := range []struct{ name, file, want string }{
		{"a mistyped key", "tuning:\n  - { id: intrusion, on-dealy: 5s, why: x }\n", "on-dealy"},
		{"no id", "tuning:\n  - { on-delay: 5s, why: x }\n", "id is required"},
		{"no delay", "tuning:\n  - { id: intrusion, why: x }\n", "needs on-delay or off-delay"},
		{"over an hour", "tuning:\n  - { id: intrusion, on-delay: 5h, why: x }\n", "outside 0 to 1h0m0s"},
		{"negative", "tuning:\n  - { id: intrusion, off-delay: -5s, why: x }\n", "outside 0 to"},
		{"no why", "tuning:\n  - { id: intrusion, on-delay: 5s }\n", "why is required"},
		{"a long why", "tuning:\n  - { id: intrusion, on-delay: 5s, why: \"" + strings.Repeat("x", 201) + "\" }\n", "at most 200"},
		{"a multi-line why", "tuning:\n  - { id: intrusion, on-delay: 5s, why: \"a\\nb\" }\n", "one line"},
		{"twice", "tuning:\n  - { id: intrusion, on-delay: 5s, why: x }\n  - { id: intrusion, off-delay: 1s, why: y }\n", "listed twice"},
	} {
		_, err := Load(alarmFS(tuningManifest, map[string]string{"alarms.tuning.yaml": c.file}), "")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.want)
		}
	}
	if _, err := Load(alarmFS(tuningManifest, nil), ""); err == nil || !strings.Contains(err.Error(), "alarms.tuning") {
		t.Errorf("a named file that is missing: err = %v", err)
	}
	if _, err := Load(alarmFS(strings.Replace(tuningManifest, "alarms.tuning.yaml", "../x.yaml", 1), nil), ""); err == nil {
		t.Error("an overlay outside the project was accepted")
	}
}

// naut check: an id no alarm has is an error naming it (a renamed alarm
// leaves a dead row); a start logs it and skips it instead of refusing to
// run a plant.
func TestTuningOverlayCheckNamesAnUnknownIDAndWarnsOnShortening(t *testing.T) {
	proj, rt := loadAlarms(t, alarmFS(tuningManifest, map[string]string{
		"alarms.tuning.yaml": "tuning:\n  - { id: gone, on-delay: 5s, why: x }\n  - { id: intrusion, on-delay: 1s, off-delay: 9s, why: y }\n",
	}))
	_, errs, warns, err := proj.CheckAlarms(rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(errs, "\n"), `no alarm has the id "gone"`) {
		t.Errorf("errors = %v, want one naming the id gone", errs)
	}
	if !strings.Contains(strings.Join(warns, "\n"), "intrusion's on-delay 1s is shorter than the alarm's own 2s") ||
		strings.Contains(strings.Join(warns, "\n"), "off-delay") {
		t.Errorf("warnings = %v, want one for the shortened on-delay only", warns)
	}
	a, err := proj.NewAlarms(rt)
	if err != nil {
		t.Fatalf("a start must not fail over an unknown overlay id: %v", err)
	}
	a.Close()
}
