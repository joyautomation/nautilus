//go:build oplogbench

// The compaction bench (#1455 PR 2b): a log of a month, three months, a
// year of a busy plant's writes, compacted by the leader with the server's
// keep decision, three copies in one process. Measurement only, behind
// the bench's tag:
//
//	OPLOG_BENCH_COMPACT=72000,216000,876000 go test -tags oplogbench \
//	  -run '^TestOplogCompactBench$' -v -timeout 4h ./oplog
//
// A Verdin-class box is approached on mira1 with one core and a CPU quota
// matched to the Verdin's own oplog bench (startup per entry), e.g.
//
//	systemd-run --user --scope -p CPUQuota=7% taskset -c 0 env GOMAXPROCS=1 ...
//
// While it runs it measures: how long the leader's lock is held (a Status
// call's wait), how long a write waits (a Propose's), and how late a 10 ms
// ticker goroutine wakes (a scan's proxy: the decision is CPU spent beside
// the scan), and the decision's time and the process's peak RSS.
package oplog_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/server"
)

// maxer keeps the largest duration it is shown.
type maxer struct{ ns atomic.Int64 }

func (m *maxer) see(d time.Duration) {
	for {
		cur := m.ns.Load()
		if int64(d) <= cur || m.ns.CompareAndSwap(cur, int64(d)) {
			return
		}
	}
}

func (m *maxer) ms() float64 { return float64(m.ns.Load()) / 1e6 }

func peakRSSMB() float64 {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "VmHWM:"); ok {
			kb, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 64)
			return kb / 1024
		}
	}
	return 0
}

func TestOplogCompactBench(t *testing.T) {
	sizes := os.Getenv("OPLOG_BENCH_COMPACT")
	if sizes == "" {
		t.Skip("set OPLOG_BENCH_COMPACT (a comma list)")
	}
	host, _ := os.Hostname()
	t.Logf("host %s, GOMAXPROCS %d", host, runtime.GOMAXPROCS(0))
	fmt.Println("| entries | decision s | kept | K build ms (locked) | K carry ms (locked) | lock wait max ms: quiet → compacting | write wait max ms: quiet → compacting | 10 ms tick late max ms: quiet → compacting | file MB before → after | restart s before → after | peak RSS MB |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|")
	for _, s := range strings.Split(sizes, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			t.Fatal(err)
		}
		var decision, build, carry time.Duration
		var serr error
		keeper := server.NewKeeper()
		var b *oplog.BenchTrio
		compact := func(ctx context.Context, in oplog.CompactInput) ([]uint64, error) {
			start := time.Now()
			keep, err := keeper.Decide(ctx, in)
			decision = time.Since(start)
			if err == nil {
				build, carry, serr = b.Sections(keep, in.Through)
			}
			return keep, err
		}
		b = oplog.NewBenchTrio(t, n, compact)
		before, _ := os.Stat(b.File(1))
		start := time.Now()
		b.Restart(1)
		restartBefore := time.Since(start)

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		var lock, write, tick maxer
		run := func(f func()) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ctx.Err() == nil {
					f()
				}
			}()
		}
		run(func() { // the leader's heartbeat: every copy heard
			b.Node(0).Heartbeat(ctx)
			time.Sleep(500 * time.Millisecond)
		})
		for i := range 3 { // every copy's lock: each carries K out and swaps its file
			run(func() {
				t0 := time.Now()
				b.Node(i).Status()
				lock.see(time.Since(t0))
				time.Sleep(time.Millisecond)
			})
		}
		k := 0
		var phaseMu sync.Mutex
		var phases []string
		mark := func(f string, a ...any) {
			phaseMu.Lock()
			phases = append(phases, fmt.Sprintf("%8.3fs ", time.Since(start).Seconds())+fmt.Sprintf(f, a...))
			phaseMu.Unlock()
		}
		run(func() {
			k++
			t0 := time.Now()
			_, err := b.Node(0).Propose(ctx, oplog.Entry{Kind: oplog.KindNote, By: "op", Value: []byte(fmt.Sprintf(`{"v":1,"id":"b%d","rev":1,"text":"x"}`, k))})
			d := time.Since(t0)
			write.see(d)
			if d > 100*time.Millisecond && os.Getenv("OPLOG_BENCH_TRACE") != "" {
				mark("write %d waited %s (began %.3fs), err %v", k, d.Round(time.Millisecond), t0.Sub(start).Seconds(), err)
			}
			time.Sleep(200 * time.Millisecond)
		})
		run(func() {
			t0 := time.Now()
			time.Sleep(10 * time.Millisecond)
			tick.see(time.Since(t0) - 10*time.Millisecond)
		})
		time.Sleep(2 * time.Second) // every copy heard
		lock.ns.Store(0)
		write.ns.Store(0)
		tick.ns.Store(0)
		time.Sleep(10 * time.Second) // the quiet window: writes, heartbeats, no compaction
		qLock, qWrite, qTick := lock.ms(), write.ms(), tick.ms()
		lock.ns.Store(0)
		write.ns.Store(0)
		tick.ns.Store(0)
		mark("decision look begins")
		got := b.Decide(context.Background())
		mark("look done: %s (decision %s)", got, decision.Round(time.Millisecond))
		if got != "proposed" {
			t.Fatalf("%d entries: the look: %s, status %+v", n, got, b.Node(0).Status())
		}
		sctx, scancel := context.WithTimeout(context.Background(), 30*time.Minute)
		if err := b.Settled(sctx); err != nil {
			t.Fatal(err)
		}
		mark("every copy's file rewritten")
		scancel()
		time.Sleep(time.Second)
		cancel()
		wg.Wait()
		after, _ := os.Stat(b.File(1))
		start = time.Now()
		b.Restart(1)
		restartAfter := time.Since(start)
		st := b.Node(0).Status()
		var kept int
		for _, e := range b.Node(0).After(0) {
			if e.Seq <= st.CompactThrough {
				kept++
			}
		}
		if serr != nil {
			t.Fatalf("the sections: %v", serr)
		}
		if os.Getenv("OPLOG_BENCH_TRACE") != "" {
			for _, p := range phases {
				t.Log(p)
			}
		}
		fmt.Printf("| %d | %.2f | %d of %d | %.1f | %.1f | %.1f → %.1f | %.1f → %.1f | %.1f → %.1f | %.1f → %.1f | %.2f → %.2f | %.0f |\n",
			n, decision.Seconds(), kept, st.CompactThrough, float64(build)/1e6, float64(carry)/1e6, qLock, lock.ms(), qWrite, write.ms(), qTick, tick.ms(),
			float64(before.Size())/(1<<20), float64(after.Size())/(1<<20), restartBefore.Seconds(), restartAfter.Seconds(), peakRSSMB())
	}
}
