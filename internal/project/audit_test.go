package project

import (
	"testing"
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
