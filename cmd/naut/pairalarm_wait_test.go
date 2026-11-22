package main

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/internal/project"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/server"
)

// heldInputs is a driver whose inputs are live only while live is true: a
// takeover waits for it (#626, TakeoverInputsWait).
type heldInputs struct{ live *atomic.Bool }

func (d *heldInputs) ReadInputs() (nio.Values, error) { return nio.Values{"Level": 0.0}, nil }
func (d *heldInputs) WriteOutputs(nio.Values) error   { return nil }
func (d *heldInputs) InputsReady() bool               { return d.live.Load() }

// #1033 CI, end to end: b wins the lease while its takeover waits for its
// inputs. The log's ticks (driveLog, every 250 ms here) run meanwhile; they
// used to reconcile the carried ack away against b's engine, which the
// takeover had not restored yet, so Trip re-annunciated on b. The ack must
// carry, and an ack sent to b during the wait is refused for a retry
// rather than checked against a standby's occurrences.
func TestPairAckCarriesWhileTheTakeoverWaits(t *testing.T) {
	live := map[string]*atomic.Bool{"a": new(atomic.Bool), "b": new(atomic.Bool)}
	live["a"].Store(true)
	live["b"].Store(true)
	r := newPairRigWith(t, func(n string, p *project.Project) {
		tripOnAtStart(n, p)
		p.Runtime.Driver = &heldInputs{live: live[n]}
		p.Runtime.TakeoverInputsWait = time.Minute
	})
	a := r.leader()
	b := other(a)
	ha := r.healthy(a)
	r.setTrip(t, a, true)
	pairWait(t, "unack on a", 5*time.Second, func() bool { return r.alarmState(a) == alarm.UnackActive })
	r.ackTrip(t, a)
	pairWait(t, "ack on a", 5*time.Second, func() bool { return r.alarmState(a) == alarm.AckActive })
	pairWait(t, "b holds the ack", 5*time.Second, func() bool { return r.heldAck(b) })

	live[b].Store(false) // b's takeover will wait for its inputs
	ha.Store(false)      // a stops renewing: b wins the lease
	pairWait(t, b+" to hold the lease", 15*time.Second, func() bool { return r.nodes[b].m.q.IsLeader() })
	if r.nodes[b].rt.Leading() {
		t.Fatal("b took over with its inputs held")
	}
	if code, body, _ := r.call(b, "POST", "/api/alarms/ack", `{"ids":["Trip"],"by":"op"}`); code != http.StatusServiceUnavailable || !strings.Contains(body, server.PairAlarmsTakingOver) {
		t.Errorf("an ack on b during its takeover's wait: %d %s; want 503 %q", code, body, server.PairAlarmsTakingOver)
	}
	time.Sleep(time.Second) // four log ticks with the takeover held
	if r.nodes[b].rt.Leading() {
		t.Fatal("b took over with its inputs held")
	}
	if !r.heldAck(b) {
		t.Fatal("b's view lost the carried ack while its takeover waited for inputs")
	}

	live[b].Store(true)
	r.waitLeads(t, b)
	r.setTrip(t, b, true)
	pairWait(t, "Trip active on b", 5*time.Second, func() bool { return r.alarmState(b) != alarm.Normal })
	if s := r.alarmState(b); s != alarm.AckActive {
		t.Fatalf("Trip came up %s on b; want the ack carried across the held takeover", s)
	}
}
