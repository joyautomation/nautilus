package hw

import (
	"context"
	"github.com/joyautomation/nautilus/internal/fence"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// fencedBase is a Base with three writable outlets on one source, its
// pending queue holding a command for each, and a write func that records
// what left, with the deadline each command ran under. after(n) is called
// once each command has left, so a test can close the gate mid-batch.
func fencedBase(t *testing.T, after func(n int)) (*Base, *Source, *[]string, *[]time.Time) {
	t.Helper()
	var mu sync.Mutex
	var sent []string
	var deadlines []time.Time
	cfg := Config{
		Kind:    "test",
		Sources: []SourceConfig{{ID: "PDU1", Interval: time.Hour, StaleAfter: time.Hour}},
		Poll:    func(context.Context, string, string) (Result, error) { return Result{}, nil },
		Write: func(ctx context.Context, source string, d WriteDecl, v ir.Value) error {
			mu.Lock()
			sent = append(sent, d.Name)
			dl, _ := ctx.Deadline()
			deadlines = append(deadlines, dl)
			n := len(sent)
			mu.Unlock()
			if after != nil {
				after(n)
			}
			return nil
		},
	}
	for _, o := range []string{"Outlet01", "Outlet02", "Outlet03"} {
		cfg.Tags = append(cfg.Tags, TagDecl{Name: o, Type: "PDUOutlet", Source: "PDU1", Members: map[string]Binding{"Index": {Const: 1}, "On": {}}})
		cfg.Writes = append(cfg.Writes, WriteDecl{Name: o + "_Cmd", Tag: o, Member: "On"})
	}
	b, err := NewBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := b.sources[0]
	now := b.now()
	for _, o := range []string{"Outlet01", "Outlet02", "Outlet03"} {
		s.pending = append(s.pending, queuedWrite{name: o + "_Cmd", v: ir.BoolVal(true), at: now, term: 1}) // the gates below answer term 1
	}
	return b, s, &sent, &deadlines
}

// Design §3.4, rule 1: the gate is read immediately before EACH command,
// never once per batch. A gate that closes after the first command stops
// the other two, which are dropped (one log line, the reason on the row),
// not put back. gate_per_batch (the old once-per-batch look) turns it red.
func TestGateClosedMidBatchStopsTheRest(t *testing.T) {
	var open sync.Mutex
	leading := true
	b, s, sent, _ := fencedBase(t, func(n int) {
		if n == 1 {
			open.Lock()
			leading = false // the lease is lost while the batch goes out
			open.Unlock()
		}
	})
	b.SetSendGate(func() (bool, time.Duration, uint64) {
		open.Lock()
		defer open.Unlock()
		return leading, time.Second, 1
	})
	b.flushWrites(context.Background(), s)
	if len(*sent) != 1 {
		t.Fatalf("sent %v; want only the command before the gate closed", *sent)
	}
	if len(s.pending) != 0 {
		t.Fatalf("the stopped commands were put back: %+v", s.pending)
	}
	if s.lastWrite == nil || !strings.Contains(s.lastWrite.Error(), "2 queued command(s) dropped") {
		t.Fatalf("the row says %v; want the two dropped commands named", s.lastWrite)
	}
}

// Rule 2: each command runs with the gate's send budget as its deadline,
// and one whose budget is already used up is not started. send_no_deadline
// turns it red.
func TestEachCommandRunsToItsSendDeadline(t *testing.T) {
	b, s, sent, deadlines := fencedBase(t, nil)
	b.SetSendGate(func() (bool, time.Duration, uint64) { return true, 400 * time.Millisecond, 1 })
	start := time.Now()
	b.flushWrites(context.Background(), s)
	if len(*sent) != 3 {
		t.Fatalf("sent %v; want all three", *sent)
	}
	for i, dl := range *deadlines {
		if dl.IsZero() || dl.Before(start.Add(400*time.Millisecond)) || dl.After(time.Now().Add(400*time.Millisecond)) {
			t.Errorf("command %d ran with deadline %v; want 400 ms from its own gate read", i, dl)
		}
	}

	b2, s2, sent2, _ := fencedBase(t, nil)
	b2.SetSendGate(func() (bool, time.Duration, uint64) { return true, 0, 1 }) // open, but no time left
	b2.flushWrites(context.Background(), s2)
	if len(*sent2) != 0 {
		t.Fatalf("sent %v with no budget left; want nothing started", *sent2)
	}
}

// #1040 review: a command is sent only under the lease's term it was
// computed in. Queued in term 5, the gate closes before the flush (the
// lease lost), and opens again in term 9: nothing from term 5 leaves,
// whatever StaleAfter says, and the row says why; a command computed in
// term 9 does. The same term reopened (scans stalled and resumed, no other
// leader in between) keeps its queue. old_term_sent turns it red.
func TestCommandsFromAnEarlierTermAreNotSent(t *testing.T) {
	var mu sync.Mutex
	open, term := true, uint64(5)
	set := func(o bool, tm uint64) { mu.Lock(); open, term = o, tm; mu.Unlock() }
	b, s, sent, _ := fencedBase(t, nil)
	s.pending = nil
	b.SetSendGate(func() (bool, time.Duration, uint64) { mu.Lock(); defer mu.Unlock(); return open, time.Second, term })
	push := func(v bool) {
		t.Helper()
		if err := b.WriteOutputs(map[string]any{"Outlet01_Cmd": v}); err != nil {
			t.Fatal(err)
		}
	}
	push(false) // the baseline
	push(true)  // queued in term 5
	set(false, 5)
	b.flushWrites(context.Background(), s) // closed: nothing goes, the queue is kept
	set(true, 9)                           // lost, and won again
	b.flushWrites(context.Background(), s)
	if len(*sent) != 0 {
		t.Fatalf("sent %v in term 9; want nothing computed in term 5", *sent)
	}
	if s.lastWrite == nil || !strings.Contains(s.lastWrite.Error(), "earlier leadership") {
		t.Fatalf("the row says %v; want the drop explained", s.lastWrite)
	}
	push(false) // computed in term 9
	b.flushWrites(context.Background(), s)
	if len(*sent) != 1 {
		t.Fatalf("sent %v; want the term-9 command", *sent)
	}

	// The same term, closed and reopened: the queue is still this term's.
	push(true)
	set(false, 9)
	b.flushWrites(context.Background(), s)
	set(true, 9)
	b.flushWrites(context.Background(), s)
	if len(*sent) != 2 {
		t.Fatalf("sent %v; want the command kept across a same-term reopen", *sent)
	}
}

// #1040 review (4): a command the device has not answered by its send
// deadline may still have run (a slow BMC's ForceOff), so the row says
// "outcome unknown", it is not counted failed, and it is never sent again.
func TestCommandPastItsDeadlineIsOutcomeUnknown(t *testing.T) {
	var calls int
	cfg := Config{
		Kind:    "test",
		Sources: []SourceConfig{{ID: "BMC1", Interval: time.Hour, StaleAfter: time.Hour}},
		Poll:    func(context.Context, string, string) (Result, error) { return Result{}, nil },
		Write: func(ctx context.Context, _ string, _ WriteDecl, _ ir.Value) error {
			calls++
			<-ctx.Done() // a BMC that answers after the deadline
			return ctx.Err()
		},
		Tags:   []TagDecl{{Name: "Sys", Type: "PDUOutlet", Source: "BMC1", Members: map[string]Binding{"Index": {Const: 1}, "On": {}}}},
		Writes: []WriteDecl{{Name: "Sys_Cmd", Tag: "Sys", Member: "On"}},
	}
	b, err := NewBase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := b.sources[0]
	s.pending = []queuedWrite{{name: "Sys_Cmd", v: ir.BoolVal(false), at: b.now(), term: 1}}
	b.SetSendGate(func() (bool, time.Duration, uint64) { return true, 100 * time.Millisecond, 1 })
	b.SetCutLabel("bmcs", true) // one of several drivers: the name carries the driver's
	fence.ResetCutsForTest()
	b.flushWrites(context.Background(), s)
	if s.lastWrite == nil || !strings.Contains(s.lastWrite.Error(), "outcome unknown") {
		t.Fatalf("the row says %v; want the outcome unknown", s.lastWrite)
	}
	// #710: one write cut at its deadline, by source id.
	if got := fence.CutCounts(); len(got) != 1 || got["bmcs/BMC1"] != 1 {
		t.Fatalf("writes cut = %v, want bmcs/BMC1 1", got)
	}
	if b.errs.Load() != 0 {
		t.Errorf("counted %d failure(s); an unknown outcome is not a failure", b.errs.Load())
	}
	b.flushWrites(context.Background(), s)
	if got := fence.CutCounts(); got["bmcs/BMC1"] != 1 {
		t.Errorf("writes cut = %v; a command not sent again is not cut again", got)
	}
	if calls != 1 || len(s.pending) != 0 {
		t.Fatalf("%d send(s), %d still queued; want the command sent once, never again", calls, len(s.pending))
	}
}
