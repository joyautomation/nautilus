package prom

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/hw"
)

// fakeScrape is a WithFetcher transport a test drives by hand: a single
// node_cpu_seconds_total series it can advance, and a down switch that
// makes the fetch fail the way a refused connection does.
type fakeScrape struct {
	mu   sync.Mutex
	idle float64
	down bool
}

func (f *fakeScrape) fetch(context.Context, string, map[string]string, bool, time.Duration) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("connection refused")
	}
	return []byte(fmt.Sprintf("# TYPE node_cpu_seconds_total counter\nnode_cpu_seconds_total{cpu=\"0\",mode=\"idle\"} %g\n", f.idle)), nil
}

func (f *fakeScrape) bump(delta float64) {
	f.mu.Lock()
	f.idle += delta
	f.mu.Unlock()
}

func (f *fakeScrape) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

// A source that reconnects after an outage must not compute a rate across
// the gap it never observed (docs/design/it-drivers.md §5): the first
// successful scrape after a transport failure resets every rate counter of
// that (source, class) — both a plain Rate: binding (counterF via
// resolveMember) and an expr's rate selector (counterF via resolveSelector)
// — so that scrape is a warm-up that delivers nothing for those members,
// and only the one after it yields a rate, computed from post-outage
// samples alone. poll is driven directly (no Start, no poll loop) so every
// step is deterministic.
func TestPollReconnectResetsRateCounters(t *testing.T) {
	fs := &fakeScrape{idle: 1000}
	m := Manifest{
		Sources: []Source{{ID: "NODE1", URL: "http://node1.invalid/metrics", Interval: time.Hour}},
		Tags: []Tag{{
			Name: "NODE1", Type: "Server", Source: "NODE1",
			Members: map[string]Binding{
				"Load1": {Metric: "node_cpu_seconds_total", Labels: map[string]string{"mode": "idle"}, Binding: hw.Binding{Rate: true}},
				"CpuPct": {Expr: "idle", From: map[string]Selector{
					"idle": {Metric: "node_cpu_seconds_total", Labels: map[string]string{"mode": "idle"}, Rate: true},
				}},
			},
		}},
	}
	d, err := New(m, WithFetcher(fs.fetch))
	if err != nil {
		t.Fatal(err)
	}
	class := d.classOf["NODE1.Load1"]
	if class == "" || d.classOf["NODE1.CpuPct"] != class {
		t.Fatalf("classOf = %v", d.classOf)
	}

	// step polls once and returns the rate members it delivered, keyed by
	// member name. Every poll is spaced so the rate's dt is never zero.
	step := func(wantErr bool) map[string]float64 {
		t.Helper()
		time.Sleep(5 * time.Millisecond)
		res, err := d.poll(context.Background(), "NODE1", class)
		if wantErr {
			if err == nil {
				t.Fatal("poll against a down source must return a transport error")
			}
			return nil
		}
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		if len(res.Bad) != 0 {
			t.Fatalf("a warm-up or rate poll must never mark a tag Bad: %v", res.Bad)
		}
		got := map[string]float64{}
		for _, u := range res.Updates {
			got[u.Member] = u.Value.F
		}
		return got
	}

	if got := step(false); len(got) != 0 {
		t.Fatalf("first scrape has no previous sample, want no rate updates, got %v", got)
	}
	fs.bump(1)
	got := step(false)
	if len(got) != 2 || got["Load1"] <= 0 || got["CpuPct"] <= 0 {
		t.Fatalf("second scrape must deliver both rates, got %v", got)
	}

	// Outage: the fetch fails (wasDown), and the counter jumps by an amount
	// no real rate over the next interval could explain.
	fs.setDown(true)
	step(true)
	fs.bump(1e9)
	fs.setDown(false)

	if got := step(false); len(got) != 0 {
		t.Fatalf("first scrape after reconnect must be a warm-up (counters reset), got %v — a rate spanning the outage", got)
	}
	fs.bump(1)
	got = step(false)
	if len(got) != 2 {
		t.Fatalf("second scrape after reconnect must deliver both rates again, got %v", got)
	}
	// 1 count over ≥5ms is at most a few hundred per second; a rate that
	// spanned the outage would carry the 1e9 jump.
	for member, r := range got {
		if r <= 0 || r > 1e6 {
			t.Fatalf("%s = %v after reconnect: not computed from post-outage samples alone", member, r)
		}
	}
}
