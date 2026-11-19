package redfish

import (
	"strings"
	"testing"
	"time"
)

// WriteTimeouts reports only a timeout the manifest sets on a BMC that
// takes commands (naut check compares it with a pair's send budget); with
// none set it reports nothing, and the timeout is never changed (#1040
// re-review).
func TestWriteTimeoutsReportOnlyExplicit(t *testing.T) {
	noTimeout := strings.Replace(subsystemManifest, "    timeout: 1s\n", "", 1)
	if noTimeout == subsystemManifest {
		t.Fatal("the fixture no longer sets timeout: 1s; update the test")
	}
	d, err := New(manifestFor(t, noTimeout, "http://127.0.0.1:1", powerWrite), WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	id := d.base.WriteSources()[0]
	if wt := d.WriteTimeouts(); len(wt) != 0 {
		t.Errorf("WriteTimeouts %v; want none", wt)
	}
	if got := d.srcs[id].cfg.Timeout; got != 0 {
		t.Errorf("a BMC with no timeout runs %s from its manifest; want it unchanged (0: the client's default)", got)
	}
	d, err = New(manifestFor(t, subsystemManifest, "http://127.0.0.1:1", powerWrite), WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	if wt := d.WriteTimeouts(); wt[id] != time.Second {
		t.Errorf("an explicit 1s: WriteTimeouts %v; want it reported", wt)
	}
}
