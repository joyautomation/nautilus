package main

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/auth/users"
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

// naut run raises a system alarm while the users file cannot be read (no
// local user can authenticate, #708) and clears it when it is fixed.
func TestUsersFileSystemAlarm(t *testing.T) {
	cheapHashes(t)
	proj, err := project.Load(runAlarmsProject, "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	var ref atomic.Pointer[server.Server]
	a, err := startAlarms(proj, rt, usersAlarm(&ref))
	if err != nil || a == nil {
		t.Fatalf("startAlarms = %v, %v", a, err)
	}
	t.Cleanup(func() { _ = a.Close() })
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	path := filepath.Join(dir, users.FileName)
	f := users.New()
	f.Add("akhan", "hunter22")
	f.Save(path, "opsadmin")
	good, _ := os.ReadFile(path)
	store, err := users.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ref.Store(server.New(rt, server.Options{Users: store}))
	rt.Scan()
	if got := alarmRecord(t, a, UsersAlarmID).State; got != alarm.Normal {
		t.Fatalf("a readable users file: %s", got)
	}
	os.WriteFile(path, []byte("{"), 0o600)
	store.Reload()
	rt.Scan()
	if r := alarmRecord(t, a, UsersAlarmID); r.State != alarm.UnackActive || r.Priority != alarm.High {
		t.Fatalf("a broken users file: %+v", r)
	}
	os.WriteFile(path, good, 0o600)
	store.Reload()
	rt.Scan()
	if got := alarmRecord(t, a, UsersAlarmID).State; got != alarm.UnackRTN {
		t.Fatalf("fixed: %s, want unack-rtn", got)
	}
}
