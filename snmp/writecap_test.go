package snmp

import (
	"strings"
	"testing"
	"time"
)

// WriteTimeouts reports only a timeout the manifest sets on a source that
// takes commands (naut check compares it with a pair's send budget); a
// default is nothing the operator chose, and a polled-only source never
// counts. Timeouts are never changed (#1040 re-review).
func TestWriteTimeoutsReportOnlyExplicit(t *testing.T) {
	m, err := ParseManifest([]byte(goodManifest))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m, WithScanClass("slow", time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if wt := d.WriteTimeouts(); len(wt) != 0 {
		t.Errorf("WriteTimeouts %v; want none (UPS1 sets no timeout, SW1 takes no commands)", wt)
	}
	if got := d.sources["UPS1"].cfg.timeout(); got != DefaultTimeout {
		t.Errorf("UPS1 runs %s; want the %s default unchanged", got, DefaultTimeout)
	}
	m, err = ParseManifest([]byte(strings.Replace(goodManifest, "    port: 1161\n", "    port: 1161\n    timeout: 5s\n", 1)))
	if err != nil {
		t.Fatal(err)
	}
	d, err = New(m, WithScanClass("slow", time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if wt := d.WriteTimeouts(); len(wt) != 1 || wt["UPS1"] != 5*time.Second {
		t.Errorf("an explicit 5s: WriteTimeouts %v; want it reported", wt)
	}
}
