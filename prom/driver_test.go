// driver_test.go is an external test (package prom_test) so it can import
// prom/serve — the in-repo stand-in, which itself imports prom for
// Sample/Family — without an import cycle. It exercises only prom's public
// surface, which is all these tests need.
package prom_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/hw"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/prom"
	"github.com/joyautomation/nautilus/prom/serve"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// baseBody is a small, hand-written scrape covering everything the tests
// below need: a gauge, a counter that a test can Bump, and an hwmon-shaped
// pair for the plain metric+labels path.
const baseBody = `# HELP node_load1 1m load average.
# TYPE node_load1 gauge
node_load1 1.5
# HELP node_cpu_seconds_total Seconds the CPUs spent in each mode.
# TYPE node_cpu_seconds_total counter
node_cpu_seconds_total{cpu="0",mode="idle"} 1000
node_cpu_seconds_total{cpu="1",mode="idle"} 1000
node_cpu_seconds_total{cpu="0",mode="user"} 100
node_cpu_seconds_total{cpu="1",mode="user"} 100
# HELP node_hwmon_temp_celsius Hardware monitor temperature.
# TYPE node_hwmon_temp_celsius gauge
node_hwmon_temp_celsius{chip="c1",sensor="temp1"} 41.5
`

func testManifest(url string) prom.Manifest {
	return prom.Manifest{
		Sources: []prom.Source{{ID: "NODE1", URL: url, Interval: 20 * time.Millisecond, StaleAfter: 60 * time.Millisecond}},
		Tags: []prom.Tag{{
			Name: "NODE1", Type: "Server", Source: "NODE1",
			Members: map[string]prom.Binding{
				"PowerOn": {Binding: hw.Binding{Const: true}},
				"Load1":   {Metric: "node_load1"},
				"CpuPct": {
					Expr: "100 - 100 * idle / cpus",
					From: map[string]prom.Selector{
						"idle": {Metric: "node_cpu_seconds_total", Labels: map[string]string{"mode": "idle"}, Agg: "sum", Rate: true},
						"cpus": {Metric: "node_cpu_seconds_total", Labels: map[string]string{"mode": "idle"}, Agg: "count"},
					},
				},
			},
		}, {
			Name: "NODE1_Temp_C1", Type: "TempSensor", Source: "NODE1",
			Members: map[string]prom.Binding{
				"Name":  {Binding: hw.Binding{Const: "c1 temp1"}},
				"Value": {Metric: "node_hwmon_temp_celsius", Labels: map[string]string{"chip": "c1", "sensor": "temp1"}},
			},
		}},
	}
}

func startStandIn(t *testing.T, body string) *serve.Server {
	t.Helper()
	srv, err := serve.New([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

func TestDriverAgainstStandIn(t *testing.T) {
	srv := startStandIn(t, baseBody)
	d, err := prom.New(testManifest(srv.URL()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	waitFor(t, "first delivery", func() bool {
		v, _ := d.ReadInputs()
		_, ok := v["NODE1"]
		return ok
	})
	vals, _ := d.ReadInputs()
	if vals["NODE1__Online"] != true {
		t.Fatalf("NODE1__Online = %v", vals["NODE1__Online"])
	}
	srvVal := vals["NODE1"].(ir.Value)
	get := func(v ir.Value, m string) ir.Value { return v.Fld[v.Struct.FieldIndex[m]] }
	if !get(srvVal, "PowerOn").B {
		t.Fatal("PowerOn const:true did not apply")
	}
	if got := get(srvVal, "Load1").F; got != 1.5 {
		t.Fatalf("Load1 = %v", got)
	}
	// CpuPct's rate selector has no previous sample yet: the member holds
	// its zero value for exactly this one interval (§5).
	if got := get(srvVal, "CpuPct").F; got != 0 {
		t.Fatalf("CpuPct before any rate sample = %v, want 0", got)
	}
	tempVal := vals["NODE1_Temp_C1"].(ir.Value)
	if got := get(tempVal, "Value").F; got != 41.5 {
		t.Fatalf("TempSensor.Value = %v", got)
	}
	if got := get(tempVal, "Name").S; got != "c1 temp1" {
		t.Fatalf("TempSensor.Name = %v", got)
	}

	// A counter step across two polls: idle advances, so a second delivery
	// must land without the tag ever going Bad.
	if err := srv.Bump("node_cpu_seconds_total", map[string]string{"cpu": "0", "mode": "idle"}, 5); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a second poll landed with a rate", func() bool {
		v, _ := d.ReadInputs()
		sv, ok := v["NODE1"].(ir.Value)
		if !ok {
			return false
		}
		// The rate selector needed a second sample; once it has one, CpuPct
		// moves off its first-interval zero.
		return get(sv, "CpuPct").F != 0
	})
	vals, _ = d.ReadInputs()
	cpu := get(vals["NODE1"].(ir.Value), "CpuPct").F
	if math.IsNaN(cpu) || math.IsInf(cpu, 0) {
		t.Fatalf("CpuPct is not a finite number: %v", cpu)
	}
	if q := d.Quality(); q["NODE1"] == nio.Bad {
		t.Fatalf("NODE1 must not be Bad: %v", q)
	}

	// Stop the stand-in: Stale + __Online false, values hold, ReadInputs
	// never errors.
	srv.Stop()
	waitFor(t, "source goes stale", func() bool {
		return d.Quality()["NODE1"] == nio.Stale
	})
	vals2, err := d.ReadInputs()
	if err != nil {
		t.Fatalf("ReadInputs must never error: %v", err)
	}
	if vals2["NODE1__Online"] != false {
		t.Fatalf("NODE1__Online while stopped = %v", vals2["NODE1__Online"])
	}
	if _, ok := vals2["NODE1"]; !ok {
		t.Fatal("values must hold while the stand-in is down")
	}
}

// The exporter going away and coming back on the same address: the source
// goes Stale with __Online false, then recovers to connected with
// __Online true and Good quality, and the rate member resumes. That the
// first post-reconnect scrape resets the rate counters instead of
// computing a rate across the gap is pinned deterministically by
// TestPollReconnectResetsRateCounters (poll.go driven directly); this is
// the recovery path end to end, through hw.Base's poll loop and a real
// socket.
func TestDriverRecoversAfterStandInRestart(t *testing.T) {
	srv := startStandIn(t, baseBody)
	addr := srv.Addr()
	d, err := prom.New(testManifest(srv.URL()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()
	get := func(v ir.Value, m string) ir.Value { return v.Fld[v.Struct.FieldIndex[m]] }
	cpuPct := func() float64 {
		v, _ := d.ReadInputs()
		sv, ok := v["NODE1"].(ir.Value)
		if !ok {
			return 0
		}
		return get(sv, "CpuPct").F
	}

	if err := srv.Bump("node_cpu_seconds_total", map[string]string{"cpu": "0", "mode": "idle"}, 5); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a rate before the outage", func() bool { return cpuPct() != 0 })

	srv.Stop()
	waitFor(t, "source goes stale", func() bool { return d.Quality()["NODE1"] == nio.Stale })
	if v, _ := d.ReadInputs(); v["NODE1__Online"] != false {
		t.Fatalf("NODE1__Online while stopped = %v", v["NODE1__Online"])
	}
	if err := srv.Start(addr); err != nil {
		t.Fatalf("restart the stand-in on %s: %v", addr, err)
	}

	waitFor(t, "source reconnects", func() bool {
		return d.Health().Sources[0].State == "connected"
	})
	waitFor(t, "Good quality and __Online true again", func() bool {
		v, _ := d.ReadInputs()
		_, notGood := d.Quality()["NODE1"]
		return v["NODE1__Online"] == true && !notGood
	})
	// The rate member resumes: let a flat interval settle CpuPct at 100
	// (idle's rate 0), then advance idle again — CpuPct can only move off
	// 100 if a fresh rate is computed after the reconnect.
	waitFor(t, "a flat post-reconnect rate", func() bool { return cpuPct() == 100 })
	if err := srv.Bump("node_cpu_seconds_total", map[string]string{"cpu": "0", "mode": "idle"}, 5); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a fresh post-reconnect rate", func() bool { return cpuPct() < 100 })
}

// One failing member (a metric absent from the scrape) leaves its siblings
// Good — the tag as a whole still delivers, and Quality never marks it Bad
// for a merely-missing optional sensor.
func TestDriverAbsentMemberLeavesSiblingsGood(t *testing.T) {
	body := strings.ReplaceAll(baseBody, `node_hwmon_temp_celsius{chip="c1",sensor="temp1"} 41.5`, "")
	srv := startStandIn(t, body)
	d, err := prom.New(testManifest(srv.URL()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()
	waitFor(t, "delivery despite one absent metric", func() bool {
		v, _ := d.ReadInputs()
		_, ok := v["NODE1_Temp_C1"]
		return ok
	})
	vals, _ := d.ReadInputs()
	if q := d.Quality(); q["NODE1_Temp_C1"] == nio.Bad {
		t.Fatalf("an absent metric must not mark the tag Bad: %v", q)
	}
	tv := vals["NODE1_Temp_C1"].(ir.Value)
	get := func(v ir.Value, m string) ir.Value { return v.Fld[v.Struct.FieldIndex[m]] }
	if got := get(tv, "Value").F; got != 0 {
		t.Fatalf("an absent metric's member must stay zero-of-field, got %v", got)
	}
	if got := get(tv, "Name").S; got != "c1 temp1" {
		t.Fatal("a sibling const member must still be delivered")
	}
}

// A metric renamed away that an EXPR's selector depends on marks the whole
// tag Bad, while a sibling tag (with no expr) stays Good. Realistic order:
// clean polls first (so the tag has actually been delivered), then the
// exporter "renames" the metric mid-run — a tag that was NEVER delivered at
// all reads NotConnected, not Bad (see the report's hw/ note on this).
func TestDriverExprSelectorMissingIsBad(t *testing.T) {
	srv := startStandIn(t, baseBody)
	d, err := prom.New(testManifest(srv.URL()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()
	waitFor(t, "NODE1 delivered cleanly first", func() bool {
		return d.Quality()["NODE1"] != nio.Bad && d.Quality()["NODE1"] != nio.NotConnected
	})
	srv.RemoveAll("node_cpu_seconds_total")
	waitFor(t, "NODE1 marked Bad", func() bool {
		return d.Quality()["NODE1"] == nio.Bad
	})
	// The sibling tag (no expr, unaffected metric) stays Good.
	waitFor(t, "NODE1_Temp_C1 delivered", func() bool {
		v, _ := d.ReadInputs()
		_, ok := v["NODE1_Temp_C1"]
		return ok
	})
	if q := d.Quality()["NODE1_Temp_C1"]; q == nio.Bad {
		t.Fatalf("sibling tag must stay Good, got %v", q)
	}
}

// Enable false parks the source: no polls, values hold, Quality Stale.
func TestDriverEnableParks(t *testing.T) {
	srv := startStandIn(t, baseBody)
	m := testManifest(srv.URL())
	m.Sources[0].Enable = "CFG_Poll"
	d, err := prom.New(m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()
	time.Sleep(60 * time.Millisecond)
	if h := d.Health(); h.Sources[0].State != "parked" || h.Polls != 0 {
		t.Fatalf("with enable false at start: %+v polls=%d", h.Sources[0], h.Polls)
	}
	if err := d.WriteOutputs(nio.Values{"CFG_Poll": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "connects once enabled", func() bool {
		return d.Health().Sources[0].State == "connected"
	})
}
