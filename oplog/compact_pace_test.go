//go:build !js

package oplog

import (
	"context"
	"errors"
	"fmt"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The compaction's pacing (compact_pace.go): a chunk of work by the clock,
// then a rest that keeps the job to its share; never a rest under the
// locks; no rest once the job is cancelled.

// TestThePacerKeepsItsShareOfTheTime: on a fake clock, no stretch of work
// runs longer than its chunk and a step, and work is the duty's share of
// the time, steps shorter or longer than the chunk alike.
func TestThePacerKeepsItsShareOfTheTime(t *testing.T) {
	for _, c := range []struct {
		work, step time.Duration
		duty       float64
	}{
		{2 * time.Millisecond, 250 * time.Microsecond, 0.25},
		{2 * time.Millisecond, 300 * time.Microsecond, 0.25}, // the chunk is no whole number of steps
		{500 * time.Microsecond, 60 * time.Microsecond, 0.1},
		{2 * time.Millisecond, 3 * time.Millisecond, 0.25}, // a slow box: one step is longer than a chunk
	} {
		now := time.Unix(0, 0)
		var rested time.Duration
		rests := 0
		lim := decideLimits{paceWork: c.work, paceDuty: c.duty,
			paceNow: func() time.Time { return now },
			paceSleep: func(_ <-chan struct{}, d time.Duration) {
				rests++
				rested += d
				now = now.Add(d)
			}}
		p := newPacer(nil, lim)
		var worked, stretch, longest time.Duration
		for range 20000 {
			now = now.Add(c.step)
			worked += c.step
			stretch += c.step
			n := rests
			p.tick()
			if rests > n {
				longest, stretch = max(longest, stretch), 0
			}
		}
		share := float64(worked) / float64(worked+rested)
		if rests == 0 || longest > max(c.work+c.step, c.step) {
			t.Errorf("%+v: %d rests, the longest stretch %s", c, rests, longest)
		}
		if share > c.duty+0.005 || share < c.duty-0.02 {
			t.Errorf("%+v: work is %.3f of the time, want %.2f", c, share, c.duty)
		}
	}
}

// TestAPacerRestsNoMoreOnceDone: a rest of hours ends when done closes,
// and none begins after; a nil pacer (a caller under the locks) never
// rests.
func TestAPacerRestsNoMoreOnceDone(t *testing.T) {
	done := make(chan struct{})
	var reads atomic.Int64
	hourly := func() time.Time { return time.Unix(0, reads.Add(1)*int64(time.Hour)) } // each read an hour on
	p := newPacer(done, decideLimits{paceWork: time.Minute, paceDuty: 0.01, paceNow: hourly}.withDefaults())
	ended := make(chan struct{})
	go func() {
		p.tick() // a rest of hours
		close(ended)
	}()
	time.Sleep(20 * time.Millisecond)
	select {
	case <-ended:
		t.Fatal("the rest ended before done closed")
	default:
	}
	closed := time.Now()
	close(done)
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("a rest did not end when done closed")
	}
	if d := time.Since(closed); d > time.Second {
		t.Fatalf("the rest ended %s after done closed", d)
	}
	after := make(chan struct{})
	go func() {
		for range 100 {
			p.tick() // each a chunk of hours: a rest of hours, were one to begin
		}
		close(after)
	}()
	select {
	case <-after:
	case <-time.After(2 * time.Second):
		t.Fatal("a rest began after done closed")
	}
	var none *pacer
	none.tick() // no panic, no rest
}

// restSite is where a rest came from: the function (and line) that ticked,
// or "Pace" for CompactInput.Pace.
func restSite() string {
	pcs := make([]uintptr, 32)
	frames := goruntime.CallersFrames(pcs[:goruntime.Callers(3, pcs)])
	for {
		f, more := frames.Next()
		name := f.Function[strings.LastIndex(f.Function, "/")+1:]
		switch {
		case strings.HasSuffix(name, "(*pacer).Pace"):
			return "Pace"
		case strings.HasSuffix(name, "(*pacer).tick"), strings.HasSuffix(f.File, "_test.go"):
		case strings.HasSuffix(name, "compactOnce"):
			return fmt.Sprintf("%s:%d", name, f.Line)
		default:
			return name
		}
		if !more {
			return "?"
		}
	}
}

// TestTheLookAndTheRewriteArePaced: every loop of a look rests (the
// log's, Entries', K's body's, and CompactInput.Pace), both loops of every
// copy's file rewrite rest, and none of it under a node's locks. The clock
// is the test's (a step per read), so a coarse system clock changes nothing.
func TestTheLookAndTheRewriteArePaced(t *testing.T) {
	var mu sync.Mutex
	var tr *dtrio
	look, rewrite, locked := map[string]int{}, map[int]map[string]int{}, 0
	heldHere := func(m *sync.Mutex) bool {
		for range 20000 { // another goroutine's hold ends (up to a second); this one's never does
			if m.TryLock() {
				m.Unlock()
				return false
			}
			time.Sleep(50 * time.Microsecond)
		}
		return true
	}
	var reads atomic.Int64
	clk := func() time.Time { return time.Unix(0, reads.Add(1)*int64(time.Microsecond)) }
	tr = decideTrio(t, func(i int, o *Options) {
		o.fileSync = func(*os.File) error { return nil } // no fsync held under mu
		o.decide.paceWork = time.Nanosecond              // every step rests
		o.decide.paceDuty = 0.5
		o.decide.paceNow = clk
		o.decide.paceSleep = func(done <-chan struct{}, _ time.Duration) {
			if tr == nil {
				return
			}
			site := restSite()
			for _, n := range tr.nodes {
				if n != nil && (heldHere(&n.mu) || heldHere(&n.seqMu)) {
					mu.Lock()
					locked++
					mu.Unlock()
				}
			}
			mu.Lock()
			defer mu.Unlock()
			for j, n := range tr.nodes {
				if n != nil && done == (<-chan struct{})(n.done) {
					if rewrite[j] == nil {
						rewrite[j] = map[string]int{}
					}
					rewrite[j][site]++
					return
				}
			}
			look[site]++
		}
		o.Compact = func(ctx context.Context, in CompactInput) ([]uint64, error) {
			if in.Pace == nil {
				return nil, errors.New("no Pace handed out")
			}
			in.Pace()
			return toyCompactor(ctx, in)
		}
	})
	tr.ready()
	mu.Lock()
	look, rewrite = map[string]int{}, map[int]map[string]int{} // the rests before the look are not its own
	mu.Unlock()
	if got := tr.nodes[0].decideForTest(tr.ctx()); got != decideProposed {
		t.Fatalf("outcome %d, status %+v", got, tr.nodes[0].Status())
	}
	tr.beat(0)
	tr.settle()
	mu.Lock()
	defer mu.Unlock()
	rested := func(site string) bool { // by suffix: an inlined closure's name carries its caller's
		for k, n := range look {
			if strings.HasSuffix(k, site) && n > 0 {
				return true
			}
		}
		return false
	}
	for _, site := range []string{"(*Node).plan", "compactSince", "firstRulesTerm", "decision.input.func1", "compactBodyOf", "Pace"} {
		if !rested(site) {
			t.Errorf("the look's %s never rested (rests by site: %v)", site, look)
		}
	}
	for i := range 3 {
		if len(rewrite[i]) < 2 {
			t.Errorf("n%d's rewrite rested in %d of its 2 loops: %v", i, len(rewrite[i]), rewrite[i])
		}
	}
	if locked > 0 {
		t.Errorf("%d rests under a node's lock", locked)
	}
}
