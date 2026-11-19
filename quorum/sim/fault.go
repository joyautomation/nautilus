package sim

import (
	"fmt"
	"slices"
	"time"
)

// FaultKind is one kind of injected fault (test plan §5).
type FaultKind int

const (
	// Cut drops every message on From→To (and To→From when Both), or,
	// when Value is non-zero, only the frames of the kinds it names
	// (CutRequests, CutReplies, CutAdopts): a stand-in for a loss pattern
	// that happens to take, say, every renewal and no reply.
	Cut FaultKind = iota
	// Delay adds Value ns to each message on the link(s) (F21).
	Delay
	// Loss drops Value percent of the messages on the link(s).
	Loss
	// Duplicate sends a second copy of each message, up to Value ns later:
	// a late replay (F31).
	Duplicate
	// Mirror also delivers every message on From→To to Node: a reply
	// misdelivered to the other controller, or requests reaching a voter
	// the pair doesn't know.
	Mirror
	// Crash kills Node's process (or cuts its power) for For; then it
	// restarts from its disk (F1-F3, F14).
	Crash
	// Wipe is Crash with the state file lost (F15; on a controller, the
	// I4 probe).
	Wipe
	// Pause stops Node's process (SIGSTOP) for For; its clock runs (F5).
	Pause
	// Freeze pauses Node AND stops its clock (a VM pause, F22b): an
	// expected I1 breach.
	Freeze
	// SlowDisk makes Node's fsync take Value ns (F24).
	SlowDisk
	// DiskFail makes Node's writes fail (EIO, ENOSPC; F25).
	DiskFail
	// Skew runs Node's clock at Value ppm for For (F22).
	Skew
	// Start runs Node (W2 or Z) for For.
	Start
	// Replay delivers every message on the link(s) while on a second time,
	// exactly Value ns later: a deterministic late copy (a named case's
	// "a message from the peer's earlier boot arrives now").
	Replay
	nKinds
)

// The frame kinds a Cut's Value may name (wire kind bits).
const (
	CutRequests = 1 << 1
	CutReplies  = 1 << 2
	CutAdopts   = 1 << 3
)

var kindNames = [nKinds]string{"Cut", "Delay", "Loss", "Duplicate", "Mirror", "Crash", "Wipe",
	"Pause", "Freeze", "SlowDisk", "DiskFail", "Skew", "Start", "Replay"}

func (k FaultKind) String() string {
	if k >= 0 && k < nKinds {
		return kindNames[k]
	}
	return fmt.Sprintf("FaultKind(%d)", int(k))
}

// Fault is one fault: it starts at At and heals at At+For (For 0: never).
type Fault struct {
	Kind     FaultKind
	At, For  time.Duration
	Node     Addr // Crash, Wipe, Pause, Freeze, SlowDisk, DiskFail, Skew, Start; Mirror's extra destination
	From, To Addr // link faults
	Both     bool // link faults: both directions
	Value    int64
}

// GoString prints f as a Go literal, so a shrunk schedule can be pasted
// into a named regression case.
func (f Fault) GoString() string {
	s := fmt.Sprintf("{Kind: %v, At: %s, For: %s", f.Kind, durLit(f.At), durLit(f.For))
	switch f.Kind {
	case Cut, Delay, Loss, Duplicate, Mirror, Replay:
		s += fmt.Sprintf(", From: %v, To: %v", f.From, f.To)
		if f.Both {
			s += ", Both: true"
		}
	}
	switch f.Kind {
	case Cut, Delay, Loss, Duplicate, Replay:
	default:
		s += fmt.Sprintf(", Node: %v", f.Node)
	}
	if f.Value != 0 {
		s += fmt.Sprintf(", Value: %d", f.Value)
	}
	return s + "}"
}

func durLit(d time.Duration) string {
	if d%time.Millisecond == 0 {
		return fmt.Sprintf("%d * ms", d/time.Millisecond)
	}
	return fmt.Sprintf("%d", int64(d))
}

func (w *World) links(f Fault) [][2]Addr {
	l := [][2]Addr{{f.From, f.To}}
	if f.Both {
		l = append(l, [2]Addr{f.To, f.From})
	}
	return l
}

func addOrRemove[T comparable](xs []T, v T, on bool) []T {
	if on {
		return append(xs, v)
	}
	if i := slices.Index(xs, v); i >= 0 {
		return slices.Delete(xs, i, i+1)
	}
	return xs
}

// applyFault starts (on) or heals fault i.
func (w *World) applyFault(i int, on bool) {
	f := w.o.Faults[i]
	w.tracef("fault %s %#v", map[bool]string{true: "start", false: "heal"}[on], f)
	w.rec.faultEvent(w.now, i, on)
	switch f.Kind {
	case Cut, Delay, Loss, Duplicate, Replay:
		for _, l := range w.links(f) {
			x, y := l[0], l[1]
			switch f.Kind {
			case Cut:
				w.cut[x][y] = addOrRemove(w.cut[x][y], f.Value, on)
			case Delay:
				w.delay[x][y] = addOrRemove(w.delay[x][y], time.Duration(f.Value), on)
			case Loss:
				w.loss[x][y] = addOrRemove(w.loss[x][y], int(f.Value), on)
			case Duplicate:
				w.dup[x][y] = addOrRemove(w.dup[x][y], time.Duration(f.Value), on)
			case Replay:
				w.replay[x][y] = addOrRemove(w.replay[x][y], time.Duration(f.Value), on)
			}
		}
	case Mirror:
		for _, l := range w.links(f) {
			w.mirror[l[0]][l[1]] = addOrRemove(w.mirror[l[0]][l[1]], f.Node, on)
		}
	case Crash, Wipe:
		n := w.nodes[f.Node]
		if on {
			n.downs++
			w.crash(n, f.Kind == Wipe)
		} else if n.downs--; n.downs == 0 {
			w.start(n)
		}
	case Start:
		if on {
			w.start(w.nodes[f.Node])
		} else {
			w.crash(w.nodes[f.Node], false)
		}
	case Pause:
		n := w.nodes[f.Node]
		if on {
			n.pauses++
		} else {
			n.pauses--
		}
		if n.freezes == 0 {
			w.pause(n, n.pauses > 0)
		}
	case Freeze:
		n := w.nodes[f.Node]
		if on {
			n.freezes++
			if n.freezes == 1 {
				n.savedRate = n.clock.Rate()
				n.clock.SetRate(w.now, 0)
				w.pause(n, true)
			}
		} else {
			n.freezes--
			if n.freezes == 0 {
				n.clock.SetRate(w.now, n.savedRate)
				w.pause(n, n.pauses > 0)
				w.arm(n)
			}
		}
	case SlowDisk:
		n := w.nodes[f.Node]
		n.disk.slow = addOrRemove(n.disk.slow, time.Duration(f.Value), on)
	case DiskFail:
		n := w.nodes[f.Node]
		if on {
			n.disk.failing++
		} else {
			n.disk.failing--
		}
	case Skew:
		n := w.nodes[f.Node]
		rate := w.o.Rates[f.Node]
		if on {
			rate = PPM(f.Value)
		}
		if n.freezes > 0 {
			n.savedRate = rate // the clock stays stopped; it runs at this rate after the thaw
			return
		}
		n.clock.SetRate(w.now, rate)
		w.arm(n)
	}
}
