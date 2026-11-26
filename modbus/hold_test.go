package modbus

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// The takeover hold (#710 (b2), hold.go): ForgetOutputs and the safe
// values' write and delivery.

func holdDriver(t *testing.T, open bool) (*Driver, *source) {
	t.Helper()
	d, s := fencedDriver(t)
	s.mu.Lock()
	clear(s.pending)
	s.mu.Unlock()
	var mu sync.Mutex
	d.SetSendGate(func() (bool, time.Duration, uint64) { mu.Lock(); defer mu.Unlock(); return open, time.Second, 5 })
	d.look()
	return d, s
}

// A safe value is commanded even when it equals what the driver last
// wrote, is undelivered until its write lands, and delivered after.
// hold_safe_never_lands turns it red.
func TestModbusSafeValueIsCommandedAndConfirmed(t *testing.T) {
	d, s := holdDriver(t, true)
	s.mu.Lock()
	s.written["SP1"] = float64(0) // the device was last told 0 already
	s.mu.Unlock()
	if err := d.WriteSafe(map[string]any{"SP1": float64(0)}); err != nil {
		t.Fatal(err)
	}
	if got := d.SafeUndelivered(); !slices.Equal(got, []string{"SP1"}) {
		t.Fatalf("undelivered %v before the flush, want [SP1]", got)
	}
	conn := &fenceConn{}
	d.flushWrites(context.Background(), s, conn)
	if w := conn.writes(); len(w) != 1 {
		t.Fatalf("%d writes, want the safe value commanded once", len(w))
	}
	if got := d.SafeUndelivered(); len(got) != 0 {
		t.Fatalf("undelivered %v after the write landed, want none", got)
	}
	if err := d.WriteSafe(map[string]any{"Nope": 1.0}); err == nil {
		t.Fatal("a safe value for no binding accepted")
	}
}

// With the write gate closed a safe value is queued nowhere and stays
// undelivered (the hold's alarm).
func TestModbusSafeValueWithTheGateClosedStaysUndelivered(t *testing.T) {
	d, s := holdDriver(t, false)
	if err := d.WriteSafe(map[string]any{"SP1": float64(3)}); err != nil {
		t.Fatal(err)
	}
	conn := &fenceConn{}
	d.flushWrites(context.Background(), s, conn)
	if w := conn.writes(); len(w) != 0 {
		t.Fatalf("%d writes through a closed gate", len(w))
	}
	if got := d.SafeUndelivered(); !slices.Equal(got, []string{"SP1"}) {
		t.Fatalf("undelivered %v, want [SP1]", got)
	}
}

// ForgetOutputs drops what the driver wrote and queued in an earlier
// leadership: the flush sends nothing, and a rewrite: binding has nothing
// to re-assert. hold_forget_nothing turns it red.
func TestModbusForgetOutputsDropsAnEarlierLeadership(t *testing.T) {
	d, s := holdDriver(t, true)
	s.mu.Lock()
	s.written["SP1"] = float64(5)
	s.pending["SP2"] = float64(2)
	s.mu.Unlock()
	d.ForgetOutputs()
	conn := &fenceConn{}
	d.flushWrites(context.Background(), s, conn)
	if w := conn.writes(); len(w) != 0 {
		t.Fatalf("%d writes after ForgetOutputs, want none", len(w))
	}
	s.mu.Lock()
	n := len(s.written)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d written values kept: a rewrite would re-assert them", n)
	}
}
