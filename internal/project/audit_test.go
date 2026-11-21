package project

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// The audit section's defaults: ./audit, a year; keep-days moves the
// retention, a negative number keeps every file.
func TestAuditJournalDefaults(t *testing.T) {
	day := 24 * time.Hour
	for _, c := range []struct {
		cfg  *AuditConfig
		dir  string
		keep time.Duration
	}{
		{nil, "audit", 365 * day},
		{&AuditConfig{}, "audit", 365 * day},
		{&AuditConfig{Path: "/var/lib/nautilus/audit", KeepDays: 30}, "/var/lib/nautilus/audit", 30 * day},
		{&AuditConfig{KeepDays: -1}, "audit", -1},
	} {
		dir, keep := (&Project{Audit: c.cfg}).AuditJournal()
		if dir != c.dir || keep != c.keep {
			t.Errorf("%+v: %s %v, want %s %v", c.cfg, dir, keep, c.dir, c.keep)
		}
	}
}

// audit.sink takes postgres or nothing; dsn-env needs the sink. The loader
// refuses anything else, naming the field.
func TestAuditSinkIsChecked(t *testing.T) {
	for _, c := range []struct {
		section, err string
	}{
		{"audit:\n  sink: postgres\n", ""},
		{"audit:\n  sink: postgres\n  dsn-env: SITE_DB\n", ""},
		{"audit:\n  sink: mongo\n", "audit.sink \"mongo\""},
		{"audit:\n  dsn-env: SITE_DB\n", "audit.dsn-env is set but audit.sink is not"},
		{"audit:\n  sink: postgres\n  anchor-every: 1m\n", ""},
		{"audit:\n  anchor-every: 1m\n", "audit.anchor-every is set but audit.sink is not"},
		{"audit:\n  sink: postgres\n  anchor-every: 10ms\n", "want at least 1s"},
		{"audit:\n  sink: postgres\n  anchor-every: soon\n", "bad duration"},
	} {
		fsys := fstest.MapFS{
			"nautilus.yaml": {Data: []byte("name: p\ntasks:\n  - program: main.st\n    scan: 20ms\n" + c.section)},
			"main.st":       {Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
		}
		_, err := Load(fsys, "nautilus.yaml")
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%q: %v", c.section, err)
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%q: %v, want %q", c.section, err, c.err)
		}
	}
}

// The copy's connection string comes from the environment: the named
// variable alone, or NAUTILUS_AUDIT_DB_URL then DATABASE_URL.
func TestAuditCopyDSN(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	pg := &Project{Audit: &AuditConfig{Sink: "postgres"}}
	named := &Project{Audit: &AuditConfig{Sink: "postgres", DSNEnv: "SITE_DB"}}
	for _, c := range []struct {
		p    *Project
		env  map[string]string
		want string
		err  string
	}{
		{&Project{}, map[string]string{"DATABASE_URL": "h"}, "", ""},
		{pg, map[string]string{"NAUTILUS_AUDIT_DB_URL": "a", "DATABASE_URL": "h"}, "a", ""},
		{pg, map[string]string{"DATABASE_URL": "h"}, "h", ""},
		{pg, nil, "", "NAUTILUS_AUDIT_DB_URL (or DATABASE_URL) is empty"},
		{named, map[string]string{"SITE_DB": "s", "DATABASE_URL": "h"}, "s", ""},
		{named, map[string]string{"DATABASE_URL": "h"}, "", "SITE_DB is empty"},
	} {
		got, err := c.p.AuditCopyDSN(env(c.env))
		if got != c.want || (c.err == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), c.err) {
			t.Errorf("%+v %v: %q %v, want %q %q", c.p.Audit, c.env, got, err, c.want, c.err)
		}
	}
}

// audit.anchor-every defaults to 5 minutes.
func TestAuditAnchorEvery(t *testing.T) {
	if got := (&Project{}).AuditAnchorEvery(); got != 5*time.Minute {
		t.Errorf("default %v", got)
	}
	if got := (&Project{Audit: &AuditConfig{Sink: "postgres", AnchorEvery: Duration(time.Minute)}}).AuditAnchorEvery(); got != time.Minute {
		t.Errorf("set %v", got)
	}
}
