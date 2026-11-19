package hw

import (
	"context"
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
		s.pending = append(s.pending, queuedWrite{name: o + "_Cmd", v: ir.BoolVal(true), at: now})
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
	b.SetSendGate(func() (bool, time.Duration) {
		open.Lock()
		defer open.Unlock()
		return leading, time.Second
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
	b.SetSendGate(func() (bool, time.Duration) { return true, 400 * time.Millisecond })
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
	b2.SetSendGate(func() (bool, time.Duration) { return true, 0 }) // open, but no time left
	b2.flushWrites(context.Background(), s2)
	if len(*sent2) != 0 {
		t.Fatalf("sent %v with no budget left; want nothing started", *sent2)
	}
}
