package snmp

import (
	"strings"
	"testing"
	"time"
)

// #1040 review (b): on a pair, a source that takes commands and names no
// timeout runs with min(the 3 s default, the send budget); one that sets
// its own keeps it (naut check compares that one); a polled-only source
// is never capped.
func TestWriteTimeoutCap(t *testing.T) {
	m, err := ParseManifest([]byte(goodManifest))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m, WithWriteTimeoutCap(time.Second), WithScanClass("slow", time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.sources["UPS1"].cfg.timeout(); got != time.Second {
		t.Errorf("writing source, default timeout: %s; want the 1 s cap", got)
	}
	if got := d.sources["SW1"].cfg.timeout(); got == time.Second {
		t.Errorf("polled-only source: %s; a source that takes no commands is never capped", got)
	}
	if wt := d.WriteTimeouts(); len(wt) != 0 {
		t.Errorf("WriteTimeouts %v; want none (no timeout set in the manifest)", wt)
	}
	m, err = ParseManifest([]byte(strings.Replace(goodManifest, "    port: 1161\n", "    port: 1161\n    timeout: 5s\n", 1)))
	if err != nil {
		t.Fatal(err)
	}
	d, err = New(m, WithWriteTimeoutCap(time.Second), WithScanClass("slow", time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got, wt := d.sources["UPS1"].cfg.timeout(), d.WriteTimeouts(); got != 5*time.Second || wt["UPS1"] != 5*time.Second {
		t.Errorf("an explicit 5s: runs %s, WriteTimeouts %v; want it kept and reported", got, wt)
	}
}
