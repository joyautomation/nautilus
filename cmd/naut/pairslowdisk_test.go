package main

import (
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// Every voter's disk slow (#710, the lost switchover campaign, CI on
// #1372): each state write, both controllers' and the witness's, takes
// longer than a vote waits for its answer (lease/8), as on a slow eMMC or
// SD card. A grant leaves only after its write, so a campaign's first ask
// times out though the voters granted it; the candidate asks the same
// request again, which a voter that holds the promise answers with no new
// write. Through naut run's wiring: the pair elects, a planned switchover
// hands over inside the old leader's resign hold and stays handed over
// (the old leader took the lead back on CI), and a failover elects.
// no_reask and reask_new_term turn it red (the pair never elects).
func TestPairSlowDisksStillSwitchAndFailOver(t *testing.T) {
	const lease = 500 * time.Millisecond // the rig's
	delay := lease/8 + lease/16
	var writes atomic.Int64
	testStateWrite = func(f *os.File, b []byte) (int, error) {
		writes.Add(1)
		time.Sleep(delay)
		return f.Write(b)
	}
	t.Cleanup(func() { testStateWrite = nil })
	r := newSwitchRig(t)
	lead := r.leader()
	stand := other(lead)
	if got := r.nodes[lead].m.o.Lease; got != lease {
		t.Fatalf("the rig's lease is %v, want %v", got, lease)
	}
	r.switchReady(t, lead)

	t0 := time.Now()
	code, ans := r.readySwitchover(t, lead)
	if code != http.StatusOK || ans.Outcome != "done" {
		t.Fatalf("switchover: %d %+v", code, ans)
	}
	hold := quorum.ResignHold(lease)
	r.waitNewLeader(t, stand, hold)
	took := time.Since(t0)
	time.Sleep(2 * lease)
	if !r.nodes[stand].m.q.IsLeader() || r.nodes[lead].m.q.IsLeader() {
		t.Fatalf("two leases after %s led, %s leads: the old leader took the lead back", stand, lead)
	}

	r.nodes[stand].crash()
	t1 := time.Now()
	r.waitNewLeader(t, lead, 8*lease)
	t.Logf("every write %v: switchover command to the new leader %v (resign hold %v); failover %v; %d state writes",
		delay, took.Round(time.Millisecond), hold, time.Since(t1).Round(time.Millisecond), writes.Load())
}
