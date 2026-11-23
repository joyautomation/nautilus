package project

import (
	"fmt"
	"strings"
	"testing"
)

const uncoveredHeader = alarmTags + `
alarms:
  defs:
    - { id: intrusion, tag: RTU9_ZONE15_INT_001_YA, area: zone15, priority: high, class: Safety }
    - { id: online, tag: RTU9__Online, area: yard, priority: low, class: process }
  oncall:
    rosters:
      - { id: ops, members: [alice] }
`

func uncoveredWarns(t *testing.T, manifest string) []string {
	t.Helper()
	proj, rt := loadAlarms(t, alarmFS(manifest, nil))
	_, errs, warns, err := proj.CheckAlarms(rt)
	if err != nil || len(errs) != 0 {
		t.Fatalf("err %v, errs %v", err, errs)
	}
	var out []string
	for _, w := range warns {
		if strings.Contains(w, "no escalation policy covers") {
			out = append(out, w)
		}
	}
	return out
}

// With escalation configured, an alarm no policy covers pages nobody, and
// naut check says so; a class that differs only in case is covered, and
// with no policies at all (no escalation) there is nothing to warn of.
func TestCheckAlarmsWarnsOfAlarmNoPolicyCovers(t *testing.T) {
	// The policy covers class `safety`: `intrusion` is class Safety (covered
	// despite the case), `online` (process) is not.
	warns := uncoveredWarns(t, uncoveredHeader+`
  escalation:
    - id: safety
      match: { class: safety }
      levels:
        - { after: 0s, notify: [roster:ops], via: [log] }
`)
	if len(warns) != 1 || !strings.Contains(warns[0], "alarm online:") ||
		!strings.Contains(warns[0], `class "process"`) || !strings.Contains(warns[0], "pages nobody") {
		t.Fatalf("want one warning for online only, got %v", warns)
	}

	if warns := uncoveredWarns(t, uncoveredHeader); len(warns) != 0 {
		t.Errorf("no policies means no escalation, so no warning: %v", warns)
	}

	if warns := uncoveredWarns(t, uncoveredHeader+`
  escalation:
    - id: all
      levels:
        - { after: 0s, notify: [roster:ops], via: [log] }
`); len(warns) != 0 {
		t.Errorf("a catch-all policy covers everything: %v", warns)
	}
}

// Past 20 uncovered alarms the warnings are cut to 20 and a line counts
// the rest.
func TestCheckAlarmsUncoveredWarningsAreCapped(t *testing.T) {
	var defs strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&defs, "    - { id: a%02d, tag: RTU9_ZONE15_INT_001_YA, class: process }\n", i)
	}
	manifest := alarmTags + "\nalarms:\n  defs:\n" + defs.String() + `  oncall:
    rosters:
      - { id: ops, members: [alice] }
  escalation:
    - id: safety
      match: { class: safety }
      levels:
        - { after: 0s, notify: [roster:ops], via: [log] }
`
	proj, rt := loadAlarms(t, alarmFS(manifest, nil))
	_, _, warns, err := proj.CheckAlarms(rt)
	if err != nil {
		t.Fatal(err)
	}
	n, more := 0, ""
	for _, w := range warns {
		if strings.Contains(w, "no escalation policy covers it") {
			n++
		}
		if strings.HasPrefix(w, "...and ") {
			more = w
		}
	}
	if n != 20 || !strings.HasPrefix(more, "...and 5 more") {
		t.Errorf("want 20 warnings and a 5-more line, got %d and %q", n, more)
	}
}
