package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/escalate"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
)

// `naut alarms escalation -id` prints what one alarm would page, with each
// roster read at the step's own time, and `naut alarms oncall` who is on
// call; both offline, from the manifest alone.
func TestAlarmsEscalationCommands(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.st", "PROGRAM Main\nVAR_EXTERNAL\n  Hi : BOOL;\nEND_VAR\nEND_PROGRAM\n")
	write("nautilus.yaml", `
tasks:
  - program: main.st
tags:
  - { name: Hi, role: state, init: false }
alarms:
  defs:
    - { id: Hi, tag: Hi, priority: high, area: wetwell }
    - { id: Note, tag: Hi, priority: high, area: wetwell, ack-required: false }
  oncall:
    timezone: America/Chicago
    rosters:
      - { id: standby, members: [bob, erin], rotate: daily, start: 2026-11-02, handover: "07:00" }
  escalation:
    - id: wet
      match: { area: wetwell }
      levels:
        - { after: 0s, notify: [alice], via: [log] }
        - { after: 10m, notify: [roster:standby], via: [log] }
`)
	c := loadOrFail(t, dir)
	loc := c.Oncall.Location()
	at := time.Date(2026, 11, 20, 6, 55, 0, 0, loc)

	var b bytes.Buffer
	printOncall(&b, c, at)
	if !strings.Contains(b.String(), "standby") || !strings.Contains(b.String(), "erin") {
		t.Fatalf("oncall at 06:55:\n%s", b.String())
	}

	b.Reset()
	_, defs := defsOrFail(t, dir)
	printSteps(&b, c, defs[0], at)
	out := b.String()
	for _, want := range []string{"policy wet", "06:55  1      alice", "07:05  2      roster:standby (bob)"} {
		if !strings.Contains(out, want) {
			t.Errorf("steps want %q:\n%s", want, out)
		}
	}

	b.Reset()
	printCoverage(&b, c, defs, at)
	cov := b.String()
	if !strings.Contains(cov, "wet ") || !regexp.MustCompile(`wet\s+1\s`).MatchString(cov) ||
		!regexp.MustCompile(`\(ack not required: never pages\)\s+1`).MatchString(cov) {
		t.Errorf("coverage (one paging alarm, one annunciate-only):\n%s", cov)
	}

	// An annunciate-only alarm never waits on a person: it never pages.
	b.Reset()
	for _, d := range defs {
		if d.ID == "Note" {
			printSteps(&b, c, d, at)
		}
	}
	if !strings.Contains(b.String(), "pages nobody") || strings.Contains(b.String(), "alice") {
		t.Errorf("steps for an annunciate-only alarm:\n%s", b.String())
	}
}

func loadOrFail(t *testing.T, dir string) *escalate.Config {
	t.Helper()
	_, c, err := loadEscalation(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func defsOrFail(t *testing.T, dir string) (*project.Project, []alarm.Def) {
	t.Helper()
	proj, _, err := loadEscalation(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	defs, err := proj.AlarmDefs(rt)
	if err != nil {
		t.Fatal(err)
	}
	return proj, defs
}

// naut check warns when a policy matches an alarm with ack-required: false:
// such an alarm never waits on an ack, so the policy silently never pages
// for it. A policy that matches only alarms that wait is quiet.
func TestCheckWarnsOnEscalationOfNoAckAlarms(t *testing.T) {
	files := func(area string) map[string]string {
		return map[string]string{
			"main.st": "PROGRAM Main\nVAR_EXTERNAL\n  Hi : BOOL;\nEND_VAR\nEND_PROGRAM\n",
			"nautilus.yaml": `
tasks:
  - program: main.st
tags:
  - { name: Hi, role: state, init: false }
alarms:
  defs:
    - { id: Hi, tag: Hi, priority: high, area: wetwell }
    - { id: Note, tag: Hi, priority: high, area: drywell, ack-required: false }
  escalation:
    - id: wet
      match: { area: ` + area + ` }
      levels:
        - { after: 0s, notify: [alice], via: [log] }
`,
		}
	}
	out, code := checkIn(t, files("wetwell"))
	if code != 0 || strings.Contains(out, "ack-required: false") {
		t.Errorf("a policy matching only alarms that wait warned (%d):\n%s", code, out)
	}
	out, code = checkIn(t, files(`"*"`))
	if code != 0 {
		t.Fatalf("a warning must not fail the check (%d):\n%s", code, out)
	}
	want := `warning: alarms.escalation "wet" matches 1 alarm(s) with ack-required: false (Note): they never wait on an ack, so this policy never pages for them`
	if !strings.Contains(out, want) {
		t.Errorf("want %q:\n%s", want, out)
	}
}
