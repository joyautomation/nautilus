package redfish

import (
	"strings"
	"testing"
	"time"
)

// #1040 review (b): on a pair, a BMC that takes commands and names no
// timeout runs with min(the 10 s default, the send budget), and
// WriteTimeouts reports nothing for it; one whose manifest sets a timeout
// keeps it, and WriteTimeouts reports that one (naut check compares it).
func TestWriteTimeoutCap(t *testing.T) {
	noTimeout := strings.Replace(subsystemManifest, "    timeout: 1s\n", "", 1)
	if noTimeout == subsystemManifest {
		t.Fatal("the fixture no longer sets timeout: 1s; update the test")
	}
	d, err := New(manifestFor(t, noTimeout, "http://127.0.0.1:1", powerWrite), WithWriteTimeoutCap(500*time.Millisecond), WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	id := d.base.WriteSources()[0]
	if got := d.srcs[id].cfg.Timeout; got != 500*time.Millisecond {
		t.Errorf("writing BMC, default timeout: %s; want the 500 ms cap", got)
	}
	if wt := d.WriteTimeouts(); len(wt) != 0 {
		t.Errorf("WriteTimeouts %v; want none", wt)
	}
	d, err = New(manifestFor(t, subsystemManifest, "http://127.0.0.1:1", powerWrite), WithWriteTimeoutCap(500*time.Millisecond), WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	if got, wt := d.srcs[id].cfg.Timeout, d.WriteTimeouts(); got != time.Second || wt[id] != time.Second {
		t.Errorf("an explicit 1s: runs %s, WriteTimeouts %v; want it kept and reported", got, wt)
	}
}
