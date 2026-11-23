package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// pairCoord is a pair's elector as the runtime sees it: IsLeader, Term,
// and SendGate (gate, budget, term in one read). sendTerm, when set,
// answers SendGate's term apart from Term's (a read straddling a lease
// lost and won again).
type pairCoord struct {
	open     atomic.Bool
	term     atomic.Uint64
	sendTerm atomic.Uint64
}

func (c *pairCoord) IsLeader() bool { return c.open.Load() }
func (c *pairCoord) Term() uint64   { return c.term.Load() }
func (c *pairCoord) SendGate() (bool, time.Duration, uint64) {
	t := c.term.Load()
	if s := c.sendTerm.Load(); s != 0 {
		t = s
	}
	return c.open.Load(), 300 * time.Millisecond, t
}

// ctxChannel records the context each write ran on.
type ctxChannel struct {
	mu     sync.Mutex
	ctxs   []context.Context
	during func(ctx context.Context) // runs inside the write, as a later fragment would
	hang   bool                      // the write waits for its context, and fails with its error
	refuse error                     // the write waits for its deadline, then fails with this (a refusal at fence.Check as the lease ends)
}

func (c *ctxChannel) Read(ctx context.Context, variable string, shape ir.Value, elem string) (ir.Value, error) {
	return shape, nil
}
func (c *ctxChannel) Write(ctx context.Context, variable string, data ir.Value, elem string) error {
	c.mu.Lock()
	c.ctxs = append(c.ctxs, ctx)
	during := c.during
	c.mu.Unlock()
	if during != nil {
		during(ctx)
	}
	if c.refuse != nil {
		<-ctx.Done()
		return c.refuse
	}
	if c.hang {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}
func (c *ctxChannel) Close() error { return nil }
func (c *ctxChannel) got() []context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]context.Context(nil), c.ctxs...)
}

const oneWrite = `PROGRAM Main
VAR_EXTERNAL P1 : BOOL; V : DINT; END_VAR
VAR w1 : WRITE; END_VAR
w1(REQ := P1, ID := 'peer', VAR_1 := 'A', SD_1 := V);
END_PROGRAM`

func pairWrite(t *testing.T, coord *pairCoord, during func(context.Context)) *ctxChannel {
	t.Helper()
	ch := &ctxChannel{during: during}
	rt, err := runtime.New(runtime.Options{
		Program:     oneWrite,
		Scan:        time.Hour,
		Seed:        map[string]any{"P1": false, "V": int64(7)},
		Coordinator: coord,
		Channels:    []runtime.Channel{{Name: "peer", Channel: ch, Timeout: 10 * time.Second}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan() // the takeover
	if err := rt.Tags().WriteGlobal("P1", ir.BoolVal(true)); err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	return ch
}

// 3b-3b, design §3.4 on the READ/WRITE channels: on a pair, a channel
// write runs on a fenced context, to the gate's send budget as its
// deadline, and carries the gate, so a transport that splits it into
// requests (Write Tag Fragmented) checks the gate before each: once the
// gate closes, the rest is not sent.
func TestPairChannelWriteRunsFencedWithItsGate(t *testing.T) {
	coord := &pairCoord{}
	coord.open.Store(true)
	coord.term.Store(7)
	start := time.Now()
	checks := make(chan [2]error, 1) // from the write's goroutine
	ch := pairWrite(t, coord, func(ctx context.Context) {
		before := fence.Check(ctx)
		coord.open.Store(false) // the gate closes while the write is still going
		checks <- [2]error{before, fence.Check(ctx)}
	})
	var ctxs []context.Context
	deadline := time.Now().Add(2 * time.Second)
	for len(ctxs) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		ctxs = ch.got()
	}
	if len(ctxs) != 1 {
		t.Fatalf("%d writes reached the peer; want 1", len(ctxs))
	}
	ctx := ctxs[0]
	dl, ok := ctx.Deadline()
	if !ok || !fence.Fenced(ctx) || dl.Before(start.Add(300*time.Millisecond)) || dl.After(time.Now().Add(300*time.Millisecond)) {
		t.Fatalf("the write ran with deadline %v (%v), fenced %v; want fenced, 300 ms from its gate read", dl, ok, fence.Fenced(ctx))
	}
	var before, after error
	select {
	case c := <-checks:
		before, after = c[0], c[1]
	case <-time.After(2 * time.Second):
		t.Fatal("the write never ran its checks")
	}
	if before != nil || !errors.Is(after, fence.ErrGateClosed) {
		t.Fatalf("a fragment's check before the close: %v, after: %v; want nil, then ErrGateClosed", before, after)
	}
}

// The write is judged on ONE read of the pair's gate: open and term
// together. A read whose term is not the posting one (the lease lost and
// won back between the post and the send) drops the write, even where the
// separate Term still names the posting term.
func TestPairChannelWriteJudgedOnOneRead(t *testing.T) {
	coord := &pairCoord{}
	coord.open.Store(true)
	coord.term.Store(7)
	coord.sendTerm.Store(8)
	ch := pairWrite(t, coord, nil)
	time.Sleep(300 * time.Millisecond)
	if n := len(ch.got()); n != 0 {
		t.Fatalf("%d write(s) went out under a gate read in another term; want none", n)
	}
}

// #710: a channel write still unanswered at its send deadline is one write
// cut, counted under the channel's name; a write the closed gate refused
// is not.
func TestPairChannelWriteCutAtItsDeadlineIsCounted(t *testing.T) {
	fence.ResetCutsForTest()
	coord := &pairCoord{}
	coord.open.Store(true)
	coord.term.Store(7)
	ch := &ctxChannel{hang: true}
	rt, err := runtime.New(runtime.Options{
		Program:     oneWrite,
		Scan:        time.Hour,
		Seed:        map[string]any{"P1": false, "V": int64(7)},
		Coordinator: coord,
		Channels:    []runtime.Channel{{Name: "peer", Channel: ch, Timeout: 10 * time.Second}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	if err := rt.Tags().WriteGlobal("P1", ir.BoolVal(true)); err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	deadline := time.Now().Add(3 * time.Second)
	for fence.CutCounts()["peer"] == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := fence.CutCounts(); got["peer"] != 1 || len(got) != 1 {
		t.Fatalf("writes cut = %v, want peer 1", got)
	}

	// The gate closed before the send: refused, not a cut.
	fence.ResetCutsForTest()
	coord2 := &pairCoord{}
	coord2.term.Store(7)
	ch2 := pairWrite(t, coord2, nil)
	time.Sleep(100 * time.Millisecond)
	if n := len(ch2.got()); n != 0 {
		t.Fatalf("%d writes reached the peer through a closed gate", n)
	}
	if got := fence.CutCounts(); len(got) != 0 {
		t.Fatalf("a write the closed gate refused was counted: %v", got)
	}
}

// #710: a write refused at fence.Check (the gate closed, or its request
// not started) is not a cut, even where the lease's end makes the
// deadline pass at the same moment.
func TestPairChannelWriteRefusedBeforeItBeganIsNotACut(t *testing.T) {
	for name, refusal := range map[string]error{
		"gate closed": fmt.Errorf("fragment 2: %w", fence.ErrGateClosed),
		"not started": fmt.Errorf("%w: %w", fence.ErrNotStarted, context.DeadlineExceeded),
	} {
		t.Run(name, func(t *testing.T) {
			fence.ResetCutsForTest()
			coord := &pairCoord{}
			coord.open.Store(true)
			coord.term.Store(7)
			ch := &ctxChannel{refuse: refusal}
			rt, err := runtime.New(runtime.Options{
				Program:     oneWrite,
				Scan:        time.Hour,
				Seed:        map[string]any{"P1": false, "V": int64(7)},
				Coordinator: coord,
				Channels:    []runtime.Channel{{Name: "peer", Channel: ch, Timeout: 10 * time.Second}},
			})
			if err != nil {
				t.Fatal(err)
			}
			rt.Scan()
			if err := rt.Tags().WriteGlobal("P1", ir.BoolVal(true)); err != nil {
				t.Fatal(err)
			}
			rt.Scan()
			deadline := time.Now().Add(3 * time.Second)
			for len(ch.got()) == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			time.Sleep(600 * time.Millisecond) // past the 300 ms send deadline and the refusal
			if got := fence.CutCounts(); len(got) != 0 {
				t.Fatalf("a refused write was counted as cut: %v", got)
			}
		})
	}
}
