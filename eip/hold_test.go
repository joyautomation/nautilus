package eip

import "testing"

// ForgetOutputs (#710 (b2), the takeover hold) drops what the driver wrote
// and queued, and moves its term, so nothing from an earlier leadership
// goes out while the hold holds.
func TestHoldForgetOutputsDropsAnEarlierLeadership(t *testing.T) {
	d, err := New("127.0.0.1", testManifest())
	if err != nil {
		t.Fatal(err)
	}
	d.wmu.Lock()
	d.written["x"] = 1
	d.pending["x"] = 1
	term := d.term
	d.wmu.Unlock()
	d.ForgetOutputs()
	d.wmu.Lock()
	defer d.wmu.Unlock()
	if len(d.written) != 0 || len(d.pending) != 0 || d.term == term {
		t.Fatalf("after ForgetOutputs: %d written, %d pending, term %d (was %d); want none, none, a new term", len(d.written), len(d.pending), d.term, term)
	}
}
