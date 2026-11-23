package fence

import (
	"context"
	"sync"
	"testing"
	"time"
)

// clock installs a fake clock for the window and returns its advance func.
func clock(t *testing.T) func(time.Duration) {
	t.Helper()
	resetCuts()
	base := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex
	cutNow = func() time.Time { mu.Lock(); defer mu.Unlock(); return base }
	t.Cleanup(func() { cutNow = time.Now; resetCuts() })
	return func(d time.Duration) { mu.Lock(); base = base.Add(d); mu.Unlock() }
}

func TestCutsCountPerDeviceAndExpireWithTheWindow(t *testing.T) {
	adv := clock(t)
	Cut("plc1")
	Cut("plc1")
	Cut("bmc2")
	got := CutCounts()
	if got["plc1"] != 2 || got["bmc2"] != 1 || len(got) != 2 {
		t.Fatalf("counts = %v, want plc1 2, bmc2 1", got)
	}
	adv(3 * time.Minute)
	Cut("plc1")
	if got := CutCounts(); got["plc1"] != 3 || got["bmc2"] != 1 {
		t.Fatalf("inside the window: %v", got)
	}
	adv(CutWindow - 3*time.Minute) // the first three are now a full window old
	if got := CutCounts(); got["plc1"] != 1 || len(got) != 1 {
		t.Fatalf("old buckets must expire, and a device with none must go: %v", got)
	}
	adv(CutWindow)
	if got := CutCounts(); len(got) != 0 {
		t.Fatalf("after the window every device is gone: %v", got)
	}
	cutMu.Lock()
	n := len(cuts)
	cutMu.Unlock()
	if n != 0 {
		t.Fatalf("an empty device is kept in the map (%d): the counter would grow without bound", n)
	}
}

func TestCutsKeepFixedMemoryPerDevice(t *testing.T) {
	adv := clock(t)
	for range 1000 {
		Cut("plc1")
		adv(time.Second)
	}
	cutMu.Lock()
	n := len(cuts["plc1"])
	cutMu.Unlock()
	if max := int(CutWindow / cutBucket); n > max {
		t.Fatalf("%d buckets kept for one device, want at most %d", n, max)
	}
}

func TestCutsAreSafeToRecordConcurrently(t *testing.T) {
	clock(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 500 {
				Cut("plc1")
				_ = CutCounts()
			}
		}()
	}
	wg.Wait()
	if got := CutCounts()["plc1"]; got != 4000 {
		t.Fatalf("count = %d, want 4000 (a lost update)", got)
	}
}

func TestCutSendCountsOnlyAFencedSendPastItsDeadline(t *testing.T) {
	clock(t)
	dl := cutNow().Add(time.Second)
	fenced, cancel := Context(WithDevice(context.Background(), "plc1"), dl)
	defer cancel()

	CutSend(fenced, false) // the deadline has not passed, the timer did not fire
	if got := CutCounts(); len(got) != 0 {
		t.Fatalf("a send inside its deadline is no cut: %v", got)
	}
	CutSend(fenced, true) // the timer reset the socket
	if got := CutCounts()["plc1"]; got != 1 {
		t.Fatalf("a timer that fired is one cut, got %d", got)
	}
	cutNow = func() time.Time { return dl.Add(time.Millisecond) }
	CutSend(fenced, false) // deadline passed
	if got := CutCounts()["plc1"]; got != 2 {
		t.Fatalf("a send past its deadline is a cut, got %d", got)
	}
	// Not fenced (no deadline context) or no device: nothing.
	CutSend(WithDevice(context.Background(), "x"), true)
	plain, c2 := Context(context.Background(), dl)
	defer c2()
	CutSend(plain, true)
	if got := CutCounts(); got["x"] != 0 || len(got) != 1 {
		t.Fatalf("unfenced or unnamed sends must not count: %v", got)
	}
}

func TestCutLineAndLabel(t *testing.T) {
	if got := CutLine(map[string]int{"plc1": 14, "bmc2": 3}); got != "bmc2 3, plc1 14" {
		t.Fatalf("line = %q", got)
	}
	if CutLine(nil) != "" {
		t.Fatal("no cuts, no line")
	}
	var l CutLabel
	if l.CutName("plc1") != "plc1" || l.CutDriver("fb") != "fb" {
		t.Fatal("an unlabelled driver names a source by its id")
	}
	l.SetCutLabel("bus2", false)
	if l.CutName("plc1") != "plc1" || l.CutDriver("fb") != "bus2" {
		t.Fatal("a lone driver names a source by its id")
	}
	l.SetCutLabel("bus2", true)
	if l.CutName("plc1") != "bus2/plc1" {
		t.Fatalf("among several drivers: %q", l.CutName("plc1"))
	}
}
