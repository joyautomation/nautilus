//go:build oplogbench && linux

// The compaction bench (#1455 PR 2b): a log of a month, three months, a
// year of a busy plant's writes, compacted by the leader with the server's
// keep decision, three copies in one process. Measurement only, behind
// the bench's tag:
//
//	OPLOG_BENCH_COMPACT=72000,216000,876000 go test -tags oplogbench \
//	  -run '^TestOplogCompactBench$' -v -timeout 4h ./oplog
//
// A Verdin-class box is approached on mira1 with one core and a CPU quota
// matched to the Verdin's own oplog bench (a 216k open takes 16.7-17.0 s
// here, 17.2 s there), with a 1 ms quota period:
//
//	systemd-run --user --scope -p CPUQuota=10% -p CPUQuotaPeriodSec=1ms \
//	  taskset -c 0 env GOMAXPROCS=1 GODEBUG=gctrace=1 OPLOG_BENCH_COMPACT=216000 ...
//
// A longer period is an artifact: a 100 ms one stalls the process 100 ms
// at a time, which a Verdin never does, and at 10 ms any paced chunk that
// starts in a period is handed the whole period's CPU, so pacing shows no
// effect (#1455 PR 2b-1.5).
//
// While it runs it measures: how long the leader's lock is held (a Status
// call's wait), how long a write waits (a Propose's), and how late a 10 ms
// ticker goroutine wakes (a scan's proxy: the decision is CPU spent beside
// the scan; outside GC cycles, then all of them: gctrace says when each
// cycle ran, and Go's forced GC makes ticks late in a quiet log too), each
// as p99/p99.9/max over a quiet window (OPLOG_BENCH_QUIET,
// 10 s; set it as long as the compacting window to compare maxima) and
// over the compaction (the look, K's commit, every copy's rewrite); the
// GC cycles a minute and their mean wall time; the periods per second
// the scope was throttled (cgroup cpu.stat); and the
// decision's time and the process's peak RSS. OPLOG_BENCH_PACE=work,duty
// sets the pacing (1 as duty: unpaced); OPLOG_BENCH_TRACE=1 logs the
// phases, slow writes and ticks later than OPLOG_BENCH_LATE (22 ms);
// OPLOG_BENCH_SECTIONS=1 also times the locked sections (K's build and
// carry), inside the window and so with load of their own.
//
// Three copies run in one process with no delay before their rewrites:
// the carry and the rewrite are about three times one box's.
package oplog_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/server"
)

// samples keeps every sample of one probe (when it ended, how long),
// in arrays allocated before the run and holding no pointers, so the
// probes add no garbage and no GC work of their own to what is measured.
type samples struct {
	mu   sync.Mutex
	end  []int64 // ns since procStart
	d    []int64
	lost int // past the arrays' capacity: not kept
}

func newSamples(n int) *samples { return &samples{end: make([]int64, 0, n), d: make([]int64, 0, n)} }

func (m *samples) see(at time.Time, d time.Duration) {
	m.mu.Lock()
	if len(m.end) < cap(m.end) {
		m.end, m.d = append(m.end, int64(at.Sub(procStart))), append(m.d, int64(d))
	} else {
		m.lost++
	}
	m.mu.Unlock()
}

// in is the samples that ended within [from, to), sorted.
func (m *samples) in(from, to time.Time) []time.Duration { return m.inSkip(from, to, nil) }

// inSkip is in without the samples skip says to leave out (given when the
// sample ended and how long it was).
func (m *samples) inSkip(from, to time.Time, skip func(end time.Time, d time.Duration) bool) []time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []time.Duration
	for i, e := range m.end {
		a, d := procStart.Add(time.Duration(e)), time.Duration(m.d[i])
		if !a.Before(from) && a.Before(to) && (skip == nil || !skip(a, d)) {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out
}

// gcLog is the runtime's GC cycles as gctrace reports them (GODEBUG=
// gctrace=1, on this process's stderr, read back through a pipe): each
// cycle's start and its wall time (both stop-the-world phases and the
// concurrent mark), so the ticks inside a cycle are told from the rest.
// Every other stderr line passes through.
type gcLog struct {
	mu     sync.Mutex
	cycles [][2]float64 // seconds since the runtime's start: began, ended
	base   time.Time    // the runtime's start (calibrated by a forced cycle)
}

var gcLine = regexp.MustCompile(`gc \d+ @([0-9.]+)s \d+%: ([0-9.]+)\+([0-9.]+)\+([0-9.]+) ms clock`)

func startGCLog(t *testing.T) *gcLog {
	if !strings.Contains(os.Getenv("GODEBUG"), "gctrace=1") {
		t.Fatal("the bench splits ticks inside GC cycles from the rest: run it with GODEBUG=gctrace=1")
	}
	orig, err := syscall.Dup(2)
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Dup3(int(w.Fd()), 2, 0); err != nil {
		t.Fatal(err)
	}
	out := os.NewFile(uintptr(orig), "stderr")
	t.Cleanup(func() { syscall.Dup3(orig, 2, 0) }) // a late traceback goes to the real stderr
	g := &gcLog{}
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			l := sc.Text()
			fmt.Fprintln(out, l)
			m := gcLine.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			at, _ := strconv.ParseFloat(m[1], 64)
			var wall float64
			for _, f := range m[2:] {
				v, _ := strconv.ParseFloat(f, 64)
				wall += v
			}
			g.mu.Lock()
			g.cycles = append(g.cycles, [2]float64{at, at + wall/1000})
			g.mu.Unlock()
		}
	}()
	// gctrace's times are since the runtime's start, which is earlier than
	// this package's (every package's init runs between, tens of ms on a
	// small core): a cycle forced now says by how much.
	g.mu.Lock()
	n := len(g.cycles)
	g.mu.Unlock()
	t0 := time.Now()
	runtime.GC()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		g.mu.Lock()
		if len(g.cycles) > n {
			g.base = t0.Add(-time.Duration(g.cycles[len(g.cycles)-1][0] * float64(time.Second)))
			g.mu.Unlock()
			break
		}
		g.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the forced GC cycle's gctrace line never came")
		}
	}
	return g
}

// at is a gctrace time as wall time.
func (g *gcLog) at(sec float64) time.Time {
	return g.base.Add(time.Duration(sec * float64(time.Second)))
}

// gcSlack widens each cycle: a tick woken just after a cycle still waited
// on it.
const gcSlack = 20 * time.Millisecond

// inside reports whether [from, to] overlaps a GC cycle.
func (g *gcLog) inside(from, to time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.cycles {
		if from.Before(g.at(c[1]).Add(gcSlack)) && to.After(g.at(c[0]).Add(-gcSlack)) {
			return true
		}
	}
	return false
}

// stats is the cycles that began within [from, to): how many a minute,
// and their mean wall time in ms.
func (g *gcLog) stats(from, to time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	n, wall := 0, time.Duration(0)
	for _, c := range g.cycles {
		if b := g.at(c[0]); !b.Before(from) && b.Before(to) {
			n++
			wall += g.at(c[1]).Sub(b)
		}
	}
	if n == 0 {
		return fmt.Sprintf("0 in %.0f s", to.Sub(from).Seconds())
	}
	return fmt.Sprintf("%.2f/min, %.0f ms", float64(n)/to.Sub(from).Minutes(), float64(wall/time.Duration(n))/1e6)
}

// stat is a window's p99 / p99.9 / max in ms.
func stat(ds []time.Duration) string {
	if len(ds) == 0 {
		return "-"
	}
	q := func(f float64) float64 { return float64(ds[min(len(ds)-1, int(f*float64(len(ds))))]) / 1e6 }
	return fmt.Sprintf("%.0f/%.0f/%.0f", q(0.99), q(0.999), float64(ds[len(ds)-1])/1e6)
}

// throttle is the scope's CPU throttling so far (cgroup v2 cpu.stat):
// periods throttled and the time throttled.
// throttled: the scope's cpu.stat was read (else its column is n/a).
var throttled bool

func throttle() (periods int64, usec int64) {
	b, _ := os.ReadFile("/proc/self/cgroup")
	path, _ := strings.CutPrefix(strings.TrimSpace(string(b)), "0::")
	st, err := os.ReadFile("/sys/fs/cgroup" + path + "/cpu.stat")
	if err != nil || !strings.Contains(string(st), "nr_throttled ") {
		throttled = false
		return 0, 0
	}
	throttled = true
	for _, l := range strings.Split(string(st), "\n") {
		if v, ok := strings.CutPrefix(l, "nr_throttled "); ok {
			periods, _ = strconv.ParseInt(v, 10, 64)
		}
		if v, ok := strings.CutPrefix(l, "throttled_usec "); ok {
			usec, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return periods, usec
}

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

var procStart = time.Now()

func envDur(k string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return d
	}
	return def
}

func TestOplogCompactBench(t *testing.T) {
	sizes := os.Getenv("OPLOG_BENCH_COMPACT")
	if sizes == "" {
		t.Skip("set OPLOG_BENCH_COMPACT (a comma list)")
	}
	trace := os.Getenv("OPLOG_BENCH_TRACE") != ""
	quietFor := envDur("OPLOG_BENCH_QUIET", 10*time.Second) // match it to the compacting window to compare maxima
	late := envDur("OPLOG_BENCH_LATE", 22*time.Millisecond) // a tick this late is traced
	sections := os.Getenv("OPLOG_BENCH_SECTIONS") != ""     // the locked sections, timed inside the window (load of their own)
	gc := startGCLog(t)
	host, _ := os.Hostname()
	t.Logf("host %s, GOMAXPROCS %d, quiet window %s", host, runtime.GOMAXPROCS(0), quietFor)
	fmt.Println("| entries | pacing | decision s | look s | kept | K build / carry ms (locked) | lock wait ms p99/p99.9/max: quiet → compacting | write wait ms p99/p99.9/max: quiet → compacting | 10 ms tick late ms, outside GC, p99/p99.9/max: quiet → compacting | the same, all ticks | GC cycles, mean wall: quiet → compacting | throttled periods/s: quiet → compacting | file MB before → after | restart s before → after | peak RSS MB |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, s := range strings.Split(sizes, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			t.Fatal(err)
		}
		var decision, build, carry time.Duration
		var serr error
		keeper := server.NewKeeper()
		var b *oplog.BenchTrio
		var phaseMu sync.Mutex
		var phases []string
		var start time.Time
		mark := func(f string, a ...any) {
			phaseMu.Lock()
			phases = append(phases, fmt.Sprintf("%8.3fs (process %8.3fs) ", time.Since(start).Seconds(), time.Since(procStart).Seconds())+fmt.Sprintf(f, a...))
			phaseMu.Unlock()
		}
		compact := func(ctx context.Context, in oplog.CompactInput) ([]uint64, error) {
			mark("keep decision begins")
			t0 := time.Now()
			keep, err := keeper.Decide(ctx, in)
			decision = time.Since(t0)
			mark("keep decision done (%s): K is built and proposed next", decision.Round(time.Millisecond))
			if err == nil && sections {
				build, carry, serr = b.Sections(keep, in.Through)
			}
			return keep, err
		}
		b = oplog.NewBenchTrio(t, n, compact)
		before, _ := os.Stat(b.File(1))
		start = time.Now()
		b.Restart(1)
		restartBefore := time.Since(start)

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		lock, write, tick := newSamples(1<<20), newSamples(1<<14), newSamples(1<<17) // ~3k, 5 and 100 a second
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
				lock.see(time.Now(), time.Since(t0))
				time.Sleep(time.Millisecond)
			})
		}
		k := 0
		run(func() {
			k++
			t0 := time.Now()
			_, err := b.Node(0).Propose(ctx, oplog.Entry{Kind: oplog.KindNote, By: "op", Value: []byte(fmt.Sprintf(`{"v":1,"id":"b%d","rev":1,"text":"x"}`, k))})
			d := time.Since(t0)
			write.see(time.Now(), d)
			if d > 100*time.Millisecond && trace {
				mark("write %d waited %s (began %.3fs), err %v", k, d.Round(time.Millisecond), t0.Sub(start).Seconds(), err)
			}
			time.Sleep(200 * time.Millisecond)
		})
		traced := 0
		run(func() {
			t0 := time.Now()
			time.Sleep(10 * time.Millisecond)
			d := time.Since(t0) - 10*time.Millisecond
			tick.see(time.Now(), d)
			if d > late && trace && traced < 300 {
				traced++
				mark("tick %s late", d.Round(time.Millisecond))
			}
		})
		time.Sleep(2 * time.Second) // every copy heard
		q0 := time.Now()
		p0, _ := throttle()
		mark("quiet window begins")
		time.Sleep(quietFor) // writes, heartbeats, no compaction
		c0 := time.Now()
		p1, _ := throttle()
		mark("decision look begins")
		got := b.Decide(context.Background())
		look := time.Since(c0)
		mark("look done: %s (keep decision %s)", got, decision.Round(time.Millisecond))
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
		c1 := time.Now()
		p2, _ := throttle()
		cancel()
		wg.Wait()
		after, _ := os.Stat(b.File(1))
		rs := time.Now()
		b.Restart(1)
		restartAfter := time.Since(rs)
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
		if trace {
			for _, p := range phases {
				t.Log(p)
			}
		}
		secs := "-"
		if sections {
			secs = fmt.Sprintf("%.1f / %.1f", float64(build)/1e6, float64(carry)/1e6)
		}
		rate := func(p int64, from, to time.Time) string {
			if !throttled {
				return "n/a"
			}
			return fmt.Sprintf("%.0f", float64(p)/to.Sub(from).Seconds())
		}
		if n := lock.lost + write.lost + tick.lost; n > 0 {
			t.Errorf("%d probe samples past the arrays' capacity were not kept: the row's percentiles cover only part of a window", n)
		}
		noGC := func(end time.Time, d time.Duration) bool { return gc.inside(end.Add(-10*time.Millisecond-d), end) }
		fmt.Printf("| %d | %s | %.2f | %.1f | %d of %d | %s | %s → %s | %s → %s | %s → %s | %s → %s | %s → %s | %s → %s | %.1f → %.1f | %.2f → %.2f | %.0f |\n",
			n, b.Pacing(), decision.Seconds(), look.Seconds(), kept, st.CompactThrough, secs,
			stat(lock.in(q0, c0)), stat(lock.in(c0, c1)), stat(write.in(q0, c0)), stat(write.in(c0, c1)),
			stat(tick.inSkip(q0, c0, noGC)), stat(tick.inSkip(c0, c1, noGC)), stat(tick.in(q0, c0)), stat(tick.in(c0, c1)),
			gc.stats(q0, c0), gc.stats(c0, c1), rate(p1-p0, q0, c0), rate(p2-p1, c0, c1),
			float64(before.Size())/(1<<20), float64(after.Size())/(1<<20), restartBefore.Seconds(), restartAfter.Seconds(), peakRSSMB())
	}
}
