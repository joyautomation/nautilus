package node

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMeasureFailover measures an unplanned failover on loopback at
// leases of 2 s, 1 s and 500 ms (#710 (c)): the leader killed (a power
// cut: no Release) and the leader isolated from both peers. For each, the
// time from the fault to the other controller's gate opening, and the
// control gap: from the old leader's gate closing to the new one's
// opening (for a kill the gate closes with the process, so the two are
// the same). The cluster's monitor checks throughout that no two gates
// are ever open at once. Skipped unless QUORUM_MEASURE_FAILOVER=<runs per
// lease and fault>.
//
// This is the election alone, on loopback with peer TLS: a takeover on a
// controller adds the log catch-up, the inputs wait, a scan and the
// output push (design §4.5), and a real link adds its round trips.
func TestMeasureFailover(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("QUORUM_MEASURE_FAILOVER"))
	if n <= 0 {
		t.Skip("set QUORUM_MEASURE_FAILOVER=<runs per lease and fault>")
	}
	var report []string
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	for _, lease := range []time.Duration{2 * time.Second, time.Second, 500 * time.Millisecond} {
		t.Run(lease.String(), func(t *testing.T) {
			t.Parallel() // the clusters barely load the machine; each times only itself
			lines := measureFailovers(t, lease, n)
			<-mu
			report = append(report, lines...)
			mu <- struct{}{}
		})
	}
	t.Cleanup(func() {
		slices.Sort(report)
		t.Logf("failover on loopback (peer TLS), %d runs per row:\n%s", n, strings.Join(report, "\n"))
	})
}

// measureFailovers runs n kills and then n isolations of the leader of one
// pair at lease, bringing the old leader back as the standby after each.
func measureFailovers(t *testing.T, lease time.Duration, n int) []string {
	c := newClusterHere(t)
	c.lease = lease
	c.startAll()
	c.commission()
	c.settledLeader(c.leases(20))
	var out []string
	for _, fault := range []string{"kill", "isolate"} {
		var takes, gaps []time.Duration
		loadStart := loadAvg()
		for range n {
			if t.Failed() {
				// The monitor saw two gates open (or another breach): halt
				// and report, never a statistic.
				t.FailNow()
			}
			c.waitFor("a settled pair", c.leases(20), func() bool {
				who := c.leader()
				return who != "" && c.status(c.other(who)).Leader == string(who)
			})
			time.Sleep(c.leases(3)) // past any restarted voter's wait
			old := c.leader()
			next := c.other(old)
			oldE, nextE := c.elector(old), c.elector(next)
			start := time.Now()
			var closed time.Time
			if fault == "kill" {
				c.kill(old)
				closed = start
			} else {
				c.isolate(old, true)
			}
			deadline := start.Add(c.leases(20))
			for !nextE.IsLeader() {
				if closed.IsZero() && !oldE.IsLeader() {
					closed = time.Now()
				}
				if time.Now().After(deadline) {
					t.Fatalf("lease %v, %s: no takeover by %s in 20 leases", lease, fault, next)
				}
				time.Sleep(time.Millisecond)
			}
			opened := time.Now()
			if closed.IsZero() {
				t.Fatalf("lease %v, %s: %s opened while the isolated %s still led", lease, fault, next, old)
			}
			takes = append(takes, opened.Sub(start))
			gaps = append(gaps, opened.Sub(closed))
			if fault == "kill" {
				c.start(old)
			} else {
				c.isolate(old, false)
			}
		}
		if t.Failed() {
			t.FailNow()
		}
		out = append(out, fmt.Sprintf("  lease %-6v %-8s fault->new leader p50 %v p99 %v max %v | control gap p50 %v p99 %v max %v | load %s -> %s",
			lease, fault, pctl(takes, .5), pctl(takes, .99), pctl(takes, 1), pctl(gaps, .5), pctl(gaps, .99), pctl(gaps, 1), loadStart, loadAvg()))
	}
	return out
}

// pctl is the p-th percentile of d (p in [0, 1]), to the millisecond.
func pctl(d []time.Duration, p float64) time.Duration {
	s := slices.Clone(d)
	slices.Sort(s)
	return s[min(len(s)-1, int(float64(len(s))*p))].Round(time.Millisecond)
}

// loadAvg is the machine's 1-minute load average, "?" where unknown.
func loadAvg() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "?"
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return "?"
	}
	return f[0]
}
