package modbus

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/modbus/slave"
	"github.com/joyautomation/nautilus/runtime"
)

// logRec is a slog handler that keeps every record, for counting what the
// driver logged.
type logRec struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (h *logRec) Enabled(context.Context, slog.Level) bool { return true }
func (h *logRec) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.recs = append(h.recs, r.Clone())
	h.mu.Unlock()
	return nil
}
func (h *logRec) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *logRec) WithGroup(string) slog.Handler      { return h }

// count returns how many records carry msg, and the value of key on the
// last of them.
func (h *logRec) count(msg, key string) (n int, last slog.Value) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.recs {
		if r.Message != msg {
			continue
		}
		n++
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				last = a.Value
			}
			return true
		})
	}
	return n, last
}

const (
	msgNotDelivered = "modbus: write not delivered; will resend"
	msgDelivered    = "modbus: write delivered after it did not run"
)

// backoffRig is one writable setpoint SP at holding 500, run by the
// runtime, with a chosen RetryMin and every log record kept.
type backoffRig struct {
	d    *Driver
	u    *slave.Unit
	rec  *recorder
	logs *logRec
	rt   *runtime.Runtime
}

func newBackoffRig(t *testing.T, retryMin time.Duration) *backoffRig {
	t.Helper()
	return newBackoffRigWith(t, retryMin, false)
}

// newBackoffRigWith is newBackoffRig with SP write-only when writeOnly:
// the source then has nothing to poll.
func newBackoffRigWith(t *testing.T, retryMin time.Duration, writeOnly bool) *backoffRig {
	t.Helper()
	srv := newSlave(t)
	r := &backoffRig{u: srv.Unit(1), rec: &recorder{}, logs: &logRec{}}
	src := fastSource("DEV", srv.Addr())
	src.RetryMin, src.RetryMax = retryMin, 4*retryMin
	m := Manifest{
		Sources: []Source{src},
		Tags: []TagBinding{
			{Name: "SP", Source: "DEV", Table: TableHolding, Address: 500, Format: "uint16", Writable: true, WriteOnly: writeOnly},
		},
	}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(recordingDialer(r.rec)), WithLogger(slog.New(r.logs)))
	if err != nil {
		t.Fatal(err)
	}
	r.d = d
	startDriver(t, d)
	waitFor(t, "connect", func() bool { return d.Health().Sources[0].State == "connected" })
	r.rt = newOutputsRuntime(t, d, nil)
	r.rt.Scan() // the baseline: recorded, not sent
	return r
}

func (r *backoffRig) inject(code byte) { r.u.InjectExceptionAt(FCWriteSingleRegister, 500, code) }
func (r *backoffRig) frames() []frameRec {
	return r.rec.writes(FCWriteSingleRegister, 500)
}
func (r *backoffRig) excs() uint64 { return r.d.Health().Sources[0].Exceptions }

// scanFor scans every millisecond for dur: a fast scan, far faster than
// RetryMin.
func (r *backoffRig) scanFor(dur time.Duration) {
	for end := time.Now().Add(dur); time.Now().Before(end); {
		r.rt.Scan()
		time.Sleep(time.Millisecond)
	}
}

// A device that answers 0x06 (busy) to every write: the unchanged command
// is tried again once per RetryMin, not once per scan. Every attempt still
// counts in exceptions.
func TestNotRunWriteIsRetriedAtRetryMinNotTheScanRate(t *testing.T) {
	const retryMin = 100 * time.Millisecond
	r := newBackoffRig(t, retryMin)
	r.inject(0x06)
	start := time.Now()
	r.rt.Tags().SetReal("SP", 21)
	r.scanFor(600 * time.Millisecond)
	elapsed := time.Since(start)
	time.Sleep(20 * time.Millisecond) // the last flush in flight lands in the count

	n := len(r.frames())
	if limit := int(elapsed/retryMin) + 2; n > limit {
		t.Errorf("%d writes in %v with RetryMin %v: retried faster than the backoff (limit %d)", n, elapsed, retryMin, limit)
	}
	if n < 3 {
		t.Errorf("%d writes in %v: the command must still be retried every RetryMin", n, elapsed)
	}
	if got := r.excs(); got != uint64(n) {
		t.Errorf("exceptions = %d for %d attempts: every attempt counts", got, n)
	}
}

// During the backoff an unchanged command waits, but a changed value from
// the program is a new command and goes out at once.
func TestChangedValueGoesOutDuringTheBackoff(t *testing.T) {
	const retryMin = 3 * time.Second
	r := newBackoffRig(t, retryMin)
	r.inject(0x06)
	r.rt.Tags().SetReal("SP", 21)
	r.rt.Scan()
	waitFor(t, "the 0x06 answer", func() bool { return r.excs() == 1 })
	r.scanFor(150 * time.Millisecond)
	if n := len(r.frames()); n != 1 {
		t.Errorf("the unchanged command went out %d times inside its backoff, want once", n)
	}

	r.inject(0)
	changed := time.Now()
	r.rt.Tags().SetReal("SP", 22)
	r.rt.Scan()
	waitFor(t, "the changed value", func() bool { return r.u.Holding(500) == 22 })
	if took := time.Since(changed); took > retryMin/3 {
		t.Errorf("the changed value took %v: it must not wait for the backoff (%v)", took, retryMin)
	}
}

// A write that keeps not running is one warning, not one per attempt; when
// it finally lands, one Info line says so and how many attempts it took.
func TestStuckWriteWarnsOnceAndLogsTheLanding(t *testing.T) {
	r := newBackoffRig(t, 30*time.Millisecond)
	r.inject(0x0B)
	r.rt.Tags().SetReal("SP", 21)
	waitFor(t, "four attempts", func() bool {
		r.rt.Scan()
		return r.excs() >= 4
	})
	r.inject(0)
	waitFor(t, "the write lands", func() bool {
		r.rt.Scan()
		return r.u.Holding(500) == 21
	})
	time.Sleep(20 * time.Millisecond)
	tries := r.excs() + 1

	if n, _ := r.logs.count(msgNotDelivered, ""); n != 1 {
		t.Errorf("%q logged %d times over %d attempts, want once", msgNotDelivered, n, tries)
	}
	n, attempts := r.logs.count(msgDelivered, "attempts")
	if n != 1 {
		t.Errorf("%q logged %d times, want once", msgDelivered, n)
	} else if attempts.Int64() != int64(tries) {
		t.Errorf("delivered after %v attempts, want %d (the exceptions and the one that landed)", attempts, tries)
	}

	// A new value that lands at once logs neither.
	r.rt.Tags().SetReal("SP", 22)
	r.rt.Scan()
	waitFor(t, "the next value", func() bool { return r.u.Holding(500) == 22 })
	if again, _ := r.logs.count(msgDelivered, ""); again != n {
		t.Errorf("a write that landed first time logged %q", msgDelivered)
	}
}
