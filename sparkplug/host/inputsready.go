package host

import (
	"sync/atomic"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// defaultBirthWait is how long a node that was asked to rebirth may stay
// silent before it counts as failed for InputsReady. A live edge answers a
// rebirth request within tens of milliseconds; the runtime's own bound on a
// held first scan (2 s) is the outer limit.
const defaultBirthWait = time.Second

// inputsState is the driver's InputsReady bookkeeping, under d.mu except
// the sticky flag.
type inputsState struct {
	// srcs is, per edge node with at least one data binding, the devices
	// with data bindings under it. Built once in New, read-only after.
	srcs map[string]map[string]bool
	// born: edges whose NBIRTH has been applied; devBorn: "edge\x00device"
	// whose DBIRTH has.
	born    map[string]bool
	devBorn map[string]bool
	// askedAt is when the first rebirth request for an edge was published.
	askedAt map[string]time.Time
	// firstConn is when the first session was fully up (subscribed, STATE
	// born); zero until then.
	firstConn time.Time
	// ok is the sticky answer.
	ok atomic.Bool
}

func (d *Driver) buildInputSources() {
	d.in.srcs = map[string]map[string]bool{}
	for _, b := range d.manifest.Tags {
		if b.Member != "" {
			continue // output-only: reads come from the metric's own binding
		}
		devs := d.in.srcs[b.Node]
		if devs == nil {
			devs = map[string]bool{}
			d.in.srcs[b.Node] = devs
		}
		if b.Device != "" {
			devs[b.Device] = true
		}
	}
	d.in.born = map[string]bool{}
	d.in.devBorn = map[string]bool{}
	d.in.askedAt = map[string]time.Time{}
	d.birthWait = defaultBirthWait
}

// noteBirthLocked and noteDeviceBirthLocked record a birth for InputsReady.
// Caller holds d.mu.
func (d *Driver) noteBirthLocked(edge string) {
	if d.in.born == nil { // a Driver built by hand, not by New
		d.in.born = map[string]bool{}
	}
	d.in.born[edge] = true
}

func (d *Driver) noteDeviceBirthLocked(edge, device string) {
	if d.in.devBorn == nil {
		d.in.devBorn = map[string]bool{}
	}
	d.in.devBorn[edge+"\x00"+device] = true
}

// noteAsked records the first rebirth request published for edge.
func (d *Driver) noteAsked(edge string) {
	d.mu.Lock()
	if d.in.askedAt == nil {
		d.in.askedAt = map[string]time.Time{}
	}
	if _, ok := d.in.askedAt[edge]; !ok {
		d.in.askedAt[edge] = time.Now()
	}
	d.mu.Unlock()
}

// InputsReady is the driver's io.InputsReadier. A source is an edge node
// the manifest binds data from (a node with no data binding is no source;
// member bindings are output-only and do not count). A source has delivered
// once its NBIRTH has been applied and so has the DBIRTH of every device
// under it that has a data binding. Births are all a host gets: Sparkplug
// retains none, so the host asks each node to rebirth when it connects
// (rebirthAll) and the births are the answer.
//
// A source has failed when it did not deliver within birthWait (one
// second) of its rebirth request being published; a node not yet asked
// (the connect-time sweep staggers requests, rebirthStagger apart; a
// standby or a host with rebirth requests off asks none) is timed from
// when the sweep should have reached it, or from the session coming up.
// A dead device under a live node therefore costs at most that one
// second, once, as does a silent node.
//
// A connect attempt that fails (the broker unreachable, or a session that
// dies before it is subscribed) fails every node at once. Sticky once true,
// and true at once for a manifest with no data bindings; false until the
// first session is up or the first attempt has failed. A short mutex hold
// per call until it is true, an atomic load after.
func (d *Driver) InputsReady() bool {
	if d.in.ok.Load() || len(d.in.srcs) == 0 {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.in.firstConn.IsZero() {
		return false
	}
	now := time.Now()
	sweep := time.Duration(0)
	if d.cfg.RebirthOnStart && !d.cfg.NoRebirthRequests {
		sweep = rebirthStagger * time.Duration(len(d.manifest.Nodes))
	}
	for edge, devs := range d.in.srcs {
		delivered := d.in.born[edge]
		for dev := range devs {
			delivered = delivered && d.in.devBorn[edge+"\x00"+dev]
		}
		if delivered {
			continue
		}
		ref, asked := d.in.askedAt[edge]
		if !asked {
			ref = d.in.firstConn.Add(sweep)
		}
		if now.Sub(ref) < d.birthWait {
			return false
		}
	}
	d.in.ok.Store(true)
	return true
}

// firstAttemptFailed settles every node: the broker was not reachable, so
// the host can ask nothing and wait for nothing.
func (d *Driver) firstAttemptFailed() { d.in.ok.Store(true) }

var _ nio.InputsReadier = (*Driver)(nil)
