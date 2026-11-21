package sim

import (
	"fmt"
	"slices"
	"time"

	"github.com/joyautomation/nautilus/quorum"
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
	// Recommission is the operator's command on controller Node at At
	// (Adoption.Recommission): adopt the witness's current incarnation,
	// with floor Value. For is ignored. The generator never draws it.
	Recommission
	// Clone boots CA or CB (Node) at At from a copy of A's or B's disk as
	// it is then, under the original's name, for For (0: for good): a
	// MAC-spoofed cloned disk or VM (#1029 layer 2). The default
	// generator never draws it; the clone schedules do.
	Clone
	// Unhealthy hangs Node's scans for For (#621): its candidate stops
	// renewing and doesn't campaign, while its voter keeps answering.
	Unhealthy
	// Clear is the operator's clear-duplicate at member Node's voter at At
	// (#1029 3c-3): every mark it holds is dropped. It is a model of the
	// command, not the command: the check reads every member at the same
	// instant and needs no message (the real one asks each over the peer
	// port, a few milliseconds apart). With Value ClearGuarded
	// it is refused while that voter hears two boots of a marked name
	// (quorum.Voter.CopyHeard), as the command is. For is ignored. The
	// generator never draws it; Record.Clears says what each did.
	Clear
	// FromSpare is the operator's recommission --from-spare on the spare
	// Node at At (#1029 3c-3c): refused unless Node is a spare and both
	// other voters of its name (its peer's and the witness's) are up,
	// settled and hear no boot of it; then both clear the name keeping no
	// boot (every boot either knew retired, the generation above both),
	// and Node's state file is moved aside (Node stops, its disk wiped).
	// At At+For the operator restarts it: a fresh, uncommissioned boot
	// that re-adopts from its peer. The last phase before the move fences
	// the name at the peer, then at the witness (#1029 3c-3c-1b). Value
	// FromSpareWitnessUnfenced: the witness's fence fails, so the command
	// ends INCOMPLETE with the peer fenced: nothing is moved aside and the
	// spare stays one. The generator never draws it; Record.FromSpares
	// says what each did.
	FromSpare
	nKinds
)

// FromSpareWitnessUnfenced is a FromSpare's Value: the witness's fence
// fails (an incomplete command, the peer fenced).
const FromSpareWitnessUnfenced = 1

// A Clear's Value bits. ClearGuarded: this voter's own guard. ClearPreflight:
// the command's check phase too, all or nothing: every real member (A, B,
// W) is up and passes its guard for every name any of them marks, else
// this Clear clears nothing (#1029 3c-3, naut-lead's condition (i)).
const (
	ClearGuarded   = 1
	ClearPreflight = 2
)

// The frame kinds a Cut's Value may name (wire kind bits).
const (
	CutRequests = 1 << 1
	CutReplies  = 1 << 2
	CutAdopts   = 1 << 3
)

var kindNames = [nKinds]string{"Cut", "Delay", "Loss", "Duplicate", "Mirror", "Crash", "Wipe",
	"Pause", "Freeze", "SlowDisk", "DiskFail", "Skew", "Start", "Replay", "Recommission", "Clone", "Unhealthy", "Clear", "FromSpare"}

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

// clearPlan is one clear command's check: the boot kept and the boots
// retired per name, or refused.
type clearPlan struct {
	at     time.Duration
	keep   map[quorum.NodeID]uint64
	retire map[quorum.NodeID][]quorum.BootSeq
	gen    map[quorum.NodeID]uint64
	ok     bool
}

// clearPreflight is the clear command's check phase: every real member is
// up, and the members together hear no more than one boot of a name any
// of them marks.
func (w *World) clearPreflight() (map[quorum.NodeID]uint64, map[quorum.NodeID][]quorum.BootSeq, map[quorum.NodeID]uint64, bool) {
	var names []quorum.NodeID
	for _, m := range []Addr{A, B, W} {
		n := w.nodes[m]
		if !n.up || n.voter == nil {
			return nil, nil, nil, false
		}
		names = append(names, n.voter.Marked()...)
		if n.cand != nil && n.cand.Marked() && !quorum.MutClearIgnoresHeld() && !slices.Contains(names, nodeIDs[m]) {
			// A held controller counts as marked (node.checkPhase): its
			// marks may all be forgotten (#1144 review).
			names = append(names, nodeIDs[m])
		}
	}
	keep := map[quorum.NodeID]uint64{}
	retire := map[quorum.NodeID][]quorum.BootSeq{}
	gen := map[quorum.NodeID]uint64{}
	// Two live boots of a name, at one member or across them, refuse it.
	for _, name := range names {
		var answers [][]uint64
		var known [][]quorum.BootSeq
		for _, m := range []Addr{A, B, W} {
			n := w.nodes[m]
			answers = append(answers, n.voter.LiveBoots(w.local(n), name))
			known = append(known, n.voter.KnownBoots(name))
			// The new clear generation: above every member's and every
			// hold's (node.checkPhase).
			gen[name] = max(gen[name], n.voter.DupGen(name))
			if n.cand != nil && nodeIDs[m] == name {
				gen[name] = max(gen[name], n.cand.HoldGen())
			}
		}
		gen[name]++
		if quorum.MutClearGenIgnoresHolds() {
			gen[name] = 1
		}
		k := quorum.KeptBoot(answers...)
		if quorum.CopyLive(answers...) || k == 0 {
			return nil, nil, nil, false
		}
		keep[name] = k
		for _, b := range quorum.UnionBoots(known...) {
			if b.Nonce != k {
				retire[name] = append(retire[name], b)
			}
		}
	}
	return keep, retire, gen, true
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
	case Clear:
		n := w.nodes[f.Node]
		if !on || !n.up || n.voter == nil {
			return
		}
		now := w.local(n)
		c := ClearRec{Node: n.addr, At: w.now}
		keep := map[quorum.NodeID]uint64{}
		retire := map[quorum.NodeID][]quorum.BootSeq{}
		gen := map[quorum.NodeID]uint64{}
		if f.Value&ClearPreflight != 0 {
			var ok bool
			if w.clearPlan == nil || w.clearPlan.at != w.now {
				k, r, g, ok := w.clearPreflight()
				w.clearPlan = &clearPlan{at: w.now, keep: k, retire: r, gen: g, ok: ok}
			}
			keep, retire, gen, ok = w.clearPlan.keep, w.clearPlan.retire, w.clearPlan.gen, w.clearPlan.ok
			if !ok {
				c.Refused = n.voter.Marked()
				w.tracef("%v: the operator's clear is refused at the check phase", n.addr)
				w.rec.Clears = append(w.rec.Clears, c)
				return
			}
		}
		names := n.voter.Marked()
		for name := range keep {
			// An unmarked member retires the members' boots too.
			if !slices.Contains(names, name) && !quorum.MutClearRetiresMarkedOnly() {
				names = append(names, name)
			}
		}
		for _, name := range names {
			was := slices.Contains(n.voter.Marked(), name)
			if f.Value&ClearGuarded != 0 && n.voter.CopyHeard(now, name) {
				c.Refused = append(c.Refused, name)
				continue
			}
			k, checked := keep[name]
			if !checked {
				if bs := n.voter.LiveBoots(now, name); len(bs) == 1 {
					k = bs[0]
				}
			}
			n.voter.ClearDuplicateKeeping(name, k, retire[name], gen[name])
			if was {
				c.Cleared = append(c.Cleared, name)
			}
		}
		if p := n.voter.MarkWrite(); p != nil {
			w.persist(n, p)
		}
		w.tracef("%v: the operator clears marks: cleared %v, refused %v", n.addr, c.Cleared, c.Refused)
		w.rec.Clears = append(w.rec.Clears, c)
	case FromSpare:
		w.fromSpare(i, f, on)
	case Unhealthy:
		if on {
			w.nodes[f.Node].unhealthy++
		} else {
			w.nodes[f.Node].unhealthy--
		}
	case Clone:
		n := w.nodes[f.Node]
		if on {
			n.disk.file = append([]byte(nil), w.nodes[original(f.Node)].disk.file...)
			w.start(n)
		} else {
			w.crash(n, false)
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
	case Recommission:
		n := w.nodes[f.Node]
		if !on || !n.up {
			return
		}
		inc := w.nodes[W].voter.State().Incarnation
		p, err := n.adopt.Recommission(inc, uint64(f.Value))
		w.tracef("%v: the operator re-commissions on inc=%d with floor %d: %v", n.addr, inc, f.Value, err)
		if p != nil {
			w.persist(n, p)
		}
		w.observe(n)
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

// fromSpare applies FromSpare fault i (see FromSpare): the command's
// check and clear at both other voters at once, as the clear's model does
// (the real one asks each over the peer port, a few milliseconds apart),
// then the move aside; on heal, the operator's restart.
func (w *World) fromSpare(i int, f Fault, on bool) {
	n := w.nodes[f.Node]
	if !on {
		if w.fromSpareDone[i] {
			w.tracef("%v: the operator restarts the former spare", n.addr)
			w.start(n)
		}
		return
	}
	rec := FromSpareRec{At: w.now, Node: n.addr}
	defer func() { w.rec.FromSpares = append(w.rec.FromSpares, rec) }()
	name := nodeIDs[n.addr]
	if !n.up || !n.spare {
		rec.Refused = "not a spare"
		return
	}
	voters := []Addr{peerOf(n.addr), W}
	var known [][]quorum.BootSeq
	var gen uint64
	for _, m := range voters {
		v := w.nodes[m]
		switch {
		case !v.up || v.voter == nil || v.paused:
			rec.Refused = m.String() + " does not answer"
			return
		case !v.voter.Settled(w.local(v)):
			rec.Refused = m.String() + " not settled"
			return
		case len(v.voter.LiveBoots(w.local(v), name)) > 0:
			rec.Refused = m.String() + " hears " + string(name)
			return
		}
		known = append(known, v.voter.KnownBoots(name))
		gen = max(gen, v.voter.DupGen(name))
	}
	retire := quorum.UnionBoots(known...)
	for _, m := range voters {
		v := w.nodes[m]
		v.voter.ClearDuplicateKeeping(name, 0, retire, gen+1)
		if p := v.voter.MarkWrite(); p != nil {
			w.persist(v, p)
		}
	}
	// The fence, last: the peer's, then the witness at the peer's value
	// (#1029 3c-3c-1b).
	var fence uint64
	for _, m := range voters {
		if m == W && f.Value == FromSpareWitnessUnfenced {
			rec.Refused = "incomplete: the peer is fenced, the witness is not"
			w.tracef("%v: the operator's from-spare is incomplete: %v fenced %v at %d, the witness not", n.addr, voters[0], name, fence)
			return
		}
		v := w.nodes[m]
		fence = v.voter.Fence(name, fence)
		if p := v.voter.MarkWrite(); p != nil {
			w.persist(v, p)
		}
	}
	rec.Fence = fence
	rec.Retired = len(retire)
	w.fromSpareDone[i] = true
	if n.addr != original(n.addr) {
		w.tookAddress[n.addr] = true
	}
	w.tracef("%v: the operator's from-spare: %d boots of %v retired, the spare's file moved aside", n.addr, len(retire), name)
	w.crash(n, true)
}
