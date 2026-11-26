package fence

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Writes cut at the send deadline (#710). A write whose send deadline
// passed while it was on the wire (unanswered, so its socket was reset)
// has an outcome nobody knows: the device may have run it. A driver
// records each one here, by device, and the pair's status shows the last
// CutWindow's counts, so an operator sees which device is too slow for
// the send budget. A write not started because the gate was closed is not
// a cut and is never recorded here.

// CutWindow is how far back CutCounts looks.
const CutWindow = 5 * time.Minute

// cutBucket is the resolution of the window: counts are kept per bucket
// per device, so the memory a device holds is fixed (CutWindow/cutBucket
// entries) however many writes it loses.
const cutBucket = 15 * time.Second

type cutEntry struct {
	bucket int64 // unix time / cutBucket
	n      int
}

var (
	cutMu  sync.Mutex
	cuts   = map[string][]cutEntry{}
	cutNow = time.Now // test seam
)

func bucketOf(t time.Time) int64 { return t.UnixNano() / int64(cutBucket) }

// keep drops entries older than the window.
func keep(es []cutEntry, now int64) []cutEntry {
	oldest := now - int64(CutWindow/cutBucket) + 1
	i := 0
	for i < len(es) && es[i].bucket < oldest {
		i++
	}
	return es[i:]
}

// Cut records one write to device cut at its send deadline. It is called
// on the failure path only.
func Cut(device string) {
	now := bucketOf(cutNow())
	cutMu.Lock()
	defer cutMu.Unlock()
	es := keep(cuts[device], now)
	if n := len(es); n > 0 && es[n-1].bucket == now {
		es[n-1].n++
	} else {
		es = append(append(make([]cutEntry, 0, len(es)+1), es...), cutEntry{now, 1})
	}
	cuts[device] = es
}

// CutCounts is each device's count of writes cut in the last CutWindow
// (a device with none is absent).
func CutCounts() map[string]int {
	now := bucketOf(cutNow())
	cutMu.Lock()
	defer cutMu.Unlock()
	out := map[string]int{}
	for dev, es := range cuts {
		es = keep(es, now)
		if len(es) == 0 {
			delete(cuts, dev)
			continue
		}
		cuts[dev] = es
		n := 0
		for _, e := range es {
			n += e.n
		}
		out[dev] = n
	}
	return out
}

// CutLine renders counts as "plc1 14, bmc2 3" (by device name), or "".
func CutLine(counts map[string]int) string {
	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + " " + strconv.Itoa(counts[n])
	}
	return strings.Join(parts, ", ")
}

// resetCuts empties the counter (tests).
func resetCuts() {
	cutMu.Lock()
	cuts = map[string][]cutEntry{}
	cutMu.Unlock()
}

// ResetCutsForTest empties the counter, for another package's test.
func ResetCutsForTest() { resetCuts() }

type deviceKey struct{}

// WithDevice names, on ctx, the device a send is for, so a transport that
// resets its socket at the send deadline can record the cut (CutSend).
func WithDevice(ctx context.Context, device string) context.Context {
	return context.WithValue(ctx, deviceKey{}, device)
}

// CutSend records a cut for the device WithDevice put on ctx, when ctx is
// a fenced send that was on the wire and failed at its deadline: fired
// (the deadline timer reset the socket) or its deadline has passed. A
// transport calls it once per failed send, after the send began; a
// failure before the deadline (a connection lost, the driver's own
// shorter timeout) or before anything was sent (a dial) is no cut. No
// device on ctx, or no deadline: nothing.
func CutSend(ctx context.Context, fired bool) {
	dev, ok := ctx.Value(deviceKey{}).(string)
	if !ok || !Fenced(ctx) {
		return
	}
	if dl, ok := ctx.Deadline(); ok && (fired || !cutNow().Before(dl)) {
		Cut(dev)
	}
}

// CutLabel names a driver for the counter. Embed it in a driver: the
// project loader calls SetCutLabel with the driver's name and whether the
// project has several drivers, and CutName then gives a source's name as
// the operator knows it ("plc1", or "bus2/plc1" among several drivers).
type CutLabel struct {
	mu     sync.Mutex
	driver string
	multi  bool
}

// SetCutLabel is called once by the loader, before the driver runs.
func (l *CutLabel) SetCutLabel(driver string, multi bool) {
	l.mu.Lock()
	l.driver, l.multi = driver, multi
	l.mu.Unlock()
}

// CutName is the counter's name for source id.
func (l *CutLabel) CutName(id string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.multi && l.driver != "" {
		return l.driver + "/" + id
	}
	return id
}

// CutDriver is the counter's name for a driver that is one device (a
// single connection): its own name, or fallback when the loader gave none.
func (l *CutLabel) CutDriver(fallback string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.driver == "" {
		return fallback
	}
	return l.driver
}
