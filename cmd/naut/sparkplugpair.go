package main

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// A Sparkplug edge node under redundancy (#798): only the leader holds the
// edge node's session. Every replica builds the node; spSupervisor starts
// it once this controller leads and its runtime has taken over in that
// term (the store is live, hot or cold), and stops it (NDEATH, then
// DISCONNECT) when the gate closes or the term moves on. Between a gate
// that closes and the stop that follows, the node's publish gate (the
// same term, still leading) keeps it silent. A switchover ends the session
// before its RESIGN (pairStream.stepDown), so the old leader's NDEATH is
// out before the new leader can birth.
//
// A switchover or failover is therefore a plain death and rebirth: the
// new leader births with its own bdSeq, never the one the old leader's
// last session carried (the state stream carries it: Image.EdgeSession),
// and its MQTT client id is its own (<group>-<edge>-<member>). Bumpless
// Sparkplug through a switchover is not a goal (James, 2026-11-25).

// spNode is the part of *sparkplug.Node the supervisor drives.
type spNode interface {
	Start(ctx context.Context) error
	Stop()
	SetPublishGate(func() bool)
}

// leadingTermer is the runtime's view of its own leadership.
type leadingTermer interface {
	LeadingTerm() (term uint64, leading bool)
}

// spSupervisor starts and stops one edge node with the leadership.
type spSupervisor struct {
	node  spNode
	gate  runtime.Coordinator
	rt    leadingTermer
	poll  time.Duration // how often it looks at the gate
	retry time.Duration // after a Start that failed
	log   *slog.Logger

	steps chan spStep
	done  chan struct{}
	// stepped is the term a switchover's StepDown ended, plus one (0:
	// none). leads reads it, so the step-down fences the session at once,
	// even while Run is inside a Start that blocks on its connect (Fable).
	stepped atomic.Uint64
}

// spStep asks the supervisor to end the session of term before a
// switchover's RESIGN, and not to start one again in that term.
type spStep struct {
	term uint64
	done chan struct{}
}

// testSPStopDelay, in a test, holds a stop the gate's close asks for: a
// switchover's step-down before its RESIGN is then the only way the old
// leader's NDEATH can come before the new leader's NBIRTH.
var testSPStopDelay time.Duration

const (
	spPoll  = 50 * time.Millisecond
	spRetry = 5 * time.Second
)

func newSPSupervisor(node spNode, gate runtime.Coordinator, rt leadingTermer) *spSupervisor {
	// Closed until the first session: a standby's node reads as one
	// (sparkplug.Status.Standby), not as a broker it cannot reach.
	node.SetPublishGate(func() bool { return false })
	return &spSupervisor{node: node, gate: gate, rt: rt, poll: spPoll, retry: spRetry, log: slog.Default(),
		steps: make(chan spStep), done: make(chan struct{})}
}

// term is the gate's term, 0 for a coordinator without one.
func (s *spSupervisor) term() uint64 {
	if t, ok := s.gate.(runtime.Termed); ok {
		return t.Term()
	}
	return 0
}

// leads reports whether a session of term may run: the gate is open in
// term. It is the node's publish gate, so it reads only atomics (or the
// elector's own lock).
func (s *spSupervisor) leads(term uint64) bool {
	if s.stepped.Load() == term+1 && !mutNaut("spark_step_unfenced") {
		return false
	}
	return s.gate.IsLeader() && s.term() == term
}

// sessionGate is one session's publish gate: open while this controller
// leads in term, and latched shut the first time it reads closed. The
// elector's gate can close and open again in the same term (an unhealthy
// scan, a renewal past the fence); a birth or rebirth that ran into the
// closed moment would leave the session connected but never live again
// (rebirthPending stays set). Latched, the session is restarted instead
// (Run sees tripped). Fable on #798.
func (s *spSupervisor) sessionGate(term uint64) (gate func() bool, tripped *atomic.Bool) {
	tripped = new(atomic.Bool)
	return func() bool {
		if tripped.Load() && !mutNaut("spark_gate_unlatched") {
			return false
		}
		if !s.leads(term) {
			tripped.Store(true)
			return false
		}
		return true
	}, tripped
}

// want is whether a session should run now, and in which term: the gate
// is open, and the runtime has taken over in the gate's term.
func (s *spSupervisor) want() (bool, uint64) {
	if mutNaut("spark_ungated") {
		return true, s.term()
	}
	t := s.term()
	rtTerm, rtLead := s.rt.LeadingTerm()
	return s.gate.IsLeader() && rtLead && rtTerm == t, t
}

// Run drives the node until ctx ends, then stops it. Close waits for that.
func (s *spSupervisor) Run(ctx context.Context) {
	defer close(s.done)
	var (
		running  bool
		term     uint64       // the running session's
		tripped  *atomic.Bool // its latched gate read closed
		nextTry  time.Time
		tick     = time.NewTicker(s.poll)
		stopNode = func() {
			s.node.Stop()
			running = false
			s.log.Info("sparkplug: edge node session ended: this controller does not lead", "term", term)
		}
	)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if running {
				stopNode()
			}
			return
		case st := <-s.steps:
			if running && term == st.term {
				stopNode()
			}
			close(st.done)
			continue
		case <-tick.C:
		}
		ok, t := s.want()
		if s.stepped.Load() == t+1 {
			ok = false // a switchover ended this term's session
		}
		switch {
		case running && (!ok || t != term || tripped.Load()):
			if d := testSPStopDelay; d > 0 && !ok {
				time.Sleep(d)
			}
			stopNode()
		case !running && ok && !time.Now().Before(nextTry):
			sessionTerm := t
			var gate func() bool
			gate, tripped = s.sessionGate(sessionTerm)
			s.node.SetPublishGate(gate)
			// Not ctx: Ctrl+C must not tear the session down under the
			// NDEATH; Stop does that, in order (startDriverWhenDecided).
			if err := s.node.Start(context.Background()); err != nil {
				s.log.Warn("sparkplug: edge node session not started; trying again", "error", err, "in", s.retry)
				nextTry = time.Now().Add(s.retry)
				continue
			}
			running, term = true, sessionTerm
			s.log.Info("sparkplug: edge node session started: this controller leads", "term", term)
		}
	}
}

// Close waits for Run to have stopped the node (its context ended first).
func (s *spSupervisor) Close() { <-s.done }

// StepDown ends the session of term, if one runs, and keeps the node
// stopped for the rest of that term: a switchover calls it before its
// RESIGN. It waits at most within; the publish gate keeps a session that
// takes longer silent.
func (s *spSupervisor) StepDown(term uint64, within time.Duration) {
	s.stepped.Store(term + 1) // fenced from now, whatever Run is doing
	st := spStep{term: term, done: make(chan struct{})}
	t := time.NewTimer(within)
	defer t.Stop()
	select {
	case s.steps <- st:
	case <-s.done:
		return
	case <-t.C:
		s.log.Warn("sparkplug: the edge node session did not end before the switchover's resign", "within", within)
		return
	}
	select {
	case <-st.done:
	case <-t.C:
		s.log.Warn("sparkplug: the edge node session did not end before the switchover's resign", "within", within)
	}
}

// redundancyMetrics is the pair's Redundancy/ folder of node metrics
// (#798): a host sees the pair's health from the leader's own edge node.
// It is called by the node outside its locks, once per birth and per
// publish tick, from one goroutine at a time (the node's wire lock).
type redundancyMetrics struct {
	role     func() string
	peer     func() string
	takeover func() runtime.TakeoverReport
	hold     func() (holding bool, age, max time.Duration)
	now      func() time.Time

	holdUntil int64 // ms; fixed for one hold, so the metric does not move every tick
}

func (r *redundancyMetrics) metrics() []sparkplug.Metric {
	str := func(name, v string) sparkplug.Metric {
		return sparkplug.Metric{Name: "Redundancy/" + name, Datatype: spb.DataType_String, Value: v}
	}
	at := func(name string, ms int64) sparkplug.Metric {
		m := sparkplug.Metric{Name: "Redundancy/" + name, Datatype: spb.DataType_DateTime, Value: ms}
		if ms == 0 {
			m.Value, m.IsNull = nil, true
		}
		return m
	}
	tr := r.takeover()
	kind, reason := "none", ""
	switch {
	case tr.At.IsZero():
	case tr.Hot:
		kind, reason = "hot", "hot takeover"
	default:
		kind, reason = "cold", "cold takeover"
		if tr.Reason != "" {
			reason += ": " + tr.Reason
		}
	}
	var lastAt int64
	if !tr.At.IsZero() {
		lastAt = tr.At.UnixMilli()
	}
	holding, age, max := r.hold()
	switch {
	case !holding:
		r.holdUntil = 0
	case r.holdUntil == 0:
		r.holdUntil = r.now().Add(max - age).UnixMilli()
	}
	return []sparkplug.Metric{
		str("Role", r.role()),
		str("PeerState", r.peer()),
		str("Takeover", kind),
		{Name: "Redundancy/Hold/Active", Datatype: spb.DataType_Boolean, Value: holding},
		at("Hold/Until", r.holdUntil),
		at("LastSwitchoverAt", lastAt),
		str("LastSwitchoverReason", reason),
	}
}

// sparkplugPairOptions are the edge node's options under redundancy: it
// watches its own NDEATH topic (a late will of the old leader's session),
// and in a pair its bdSeq skips the peer's last and it publishes the
// Redundancy/ folder. None without redundancy.
func sparkplugPairOptions(el runtime.Coordinator, pair *pairMember, rt *runtime.Runtime) []sparkplug.Option {
	if el == nil {
		return nil
	}
	opts := []sparkplug.Option{sparkplug.WithDeathWatch(), sparkplug.WithSendCount(fence.Enter)}
	if pair == nil {
		return opts
	}
	opts = append(opts, sparkplug.WithBdSeqAvoid(func() (uint64, bool) {
		v := pair.peerEdge.Load()
		return v - 1, v != 0
	}))
	r := &redundancyMetrics{
		role: func() string {
			if pair.q.IsLeader() {
				return "leader"
			}
			return "standby"
		},
		peer: func() string {
			if pair.stream == nil {
				return "unknown"
			}
			return pair.stream.status().State
		},
		takeover: rt.LastTakeover,
		hold: func() (bool, time.Duration, time.Duration) {
			h := pair.hold
			if h == nil {
				return false, 0, 0
			}
			st := rt.HoldStatus()
			return st.Holding, st.Age, h.max
		},
		now: time.Now,
	}
	return append(opts, sparkplug.WithNodeMetrics(r.metrics))
}

// startSparkplugLeader runs node under el's leadership (spSupervisor), and
// for a pair also names its session in the state images and ends it
// before a switchover's RESIGN. The returned stop ends the session (its
// NDEATH out) and waits.
func startSparkplugLeader(node *sparkplug.Node, el runtime.Coordinator, pair *pairMember, rt leadingTermer) (stop func()) {
	sup := newSPSupervisor(node, el, rt)
	if pair != nil && pair.stream != nil {
		edge := func() uint64 {
			if bd, up := node.SessionBdSeq(); up {
				return bd + 1
			}
			return 0
		}
		pair.stream.edge.Store(&edge)
		step := sup.StepDown
		pair.stream.stepDown.Store(&step)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go sup.Run(ctx)
	return func() {
		if pair != nil && pair.stream != nil {
			pair.stream.stepDown.Store(nil)
		}
		cancel()
		sup.Close()
	}
}
