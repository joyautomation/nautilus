package main

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// naut run raises a system alarm while the audit journal could not be
// opened or cannot be written (#709, design §6 question 5), beside the
// project's own alarms; it clears when the journal is back.
func TestAuditJournalSystemAlarm(t *testing.T) {
	proj, err := project.Load(runAlarmsProject, "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	var ref atomic.Pointer[server.Server]
	a, err := startAlarms(proj, rt, auditAlarm(&ref))
	if err != nil || a == nil {
		t.Fatalf("startAlarms = %v, %v", a, err)
	}
	t.Cleanup(func() { _ = a.Close() })

	rt.Scan() // no server yet: not in alarm
	if got := alarmRecord(t, a, AuditAlarmID).State; got != alarm.Normal {
		t.Fatalf("before the server: %s", got)
	}
	ref.Store(server.New(rt, server.Options{AuditUnavailable: errors.New("audit: locked")}))
	rt.Scan()
	r := alarmRecord(t, a, AuditAlarmID)
	if r.State != alarm.UnackActive || r.Priority != alarm.High || r.Shelvable {
		t.Fatalf("journal unopened: %+v", r)
	}
	if got := alarmRecord(t, a, "FIT_001.L"); got.ID == "" {
		t.Fatal("the project's own alarms are gone")
	}
	ref.Store(server.New(rt))
	rt.Scan()
	// Cleared, still to be acknowledged.
	if got := alarmRecord(t, a, AuditAlarmID).State; got != alarm.UnackRTN {
		t.Fatalf("journal healthy: %s, want unack-rtn", got)
	}
}
