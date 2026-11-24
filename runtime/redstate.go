package runtime

import "sync/atomic"

// The program-visible redundancy state (#710 (b)): what the nautilus
// system functions TAKEOVER_SCAN(), HOT_TAKEOVER(), PEER_READY(),
// TAKEOVERS() and TAKEOVER_HOLD() answer (ir.RedundancyInfo). Each scan
// latches it at its start, per task, so one scan reads one answer.

// redState is the runtime-wide part, kept in the tag store every program
// scans against. Outside a pair it stays zero: every flag FALSE, the count 0.
type redState struct {
	peerReady atomic.Bool
	hold      atomic.Bool
	takeovers atomic.Int64
}

// SetPeerReady sets what PEER_READY() answers from the next scan on: a
// takeover by this leader's standby would go hot now (the pair decides,
// cmd/naut). FALSE on a standby.
func (r *Runtime) SetPeerReady(ready bool) {
	if mutRuntime("peer_ready_never_set") {
		return
	}
	r.tags.red.peerReady.Store(ready)
}

// PeerReady is what PEER_READY() answers from the next scan on.
func (r *Runtime) PeerReady() bool { return r.tags.red.peerReady.Load() }

// Takeovers is how many takeovers this process made since it started
// (TAKEOVERS()).
func (r *Runtime) Takeovers() int64 { return r.tags.red.takeovers.Load() }

// TakeoverKnower is an optional Coordinator extension: whether the
// leadership this replica just acquired is a takeover from its peer (any
// but its boot's first with no peer lease seen), or a start (#710 (b)).
// A coordinator without it never takes over: every acquisition is a start.
type TakeoverKnower interface {
	IsTakeover() bool
}

// latchRed is the scan's view of the redundancy state: the runtime-wide
// part read now, the program's own takeover flags for this scan.
func (v *scanView) latchRed(takeover, hot bool) {
	red := &v.store.red
	v.takeoverScan, v.hotTakeover = takeover, hot
	v.peerReady, v.hold, v.takeovers = red.peerReady.Load(), red.hold.Load(), red.takeovers.Load()
}

// The ir.RedundancyInfo answers, latched at the scan's start.
func (v *scanView) TakeoverScan() bool { return v.takeoverScan }
func (v *scanView) HotTakeover() bool  { return v.hotTakeover }
func (v *scanView) PeerReady() bool    { return v.peerReady }
func (v *scanView) Takeovers() int64   { return v.takeovers }
func (v *scanView) TakeoverHold() bool { return v.hold }
