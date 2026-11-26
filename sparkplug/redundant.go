package sparkplug

import (
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// An edge node under a redundant pair (#798). Only the leader holds the
// edge node's session: naut run starts the node when its controller takes
// the lead and stops it (NDEATH, then DISCONNECT) when it gives the lead
// up, so a host sees one plain death and one plain birth per switchover.
// What the node itself does about it is here:
//
//   - SetPublishGate (and WithSendCount): while the gate is closed the node publishes no
//     NBIRTH, DBIRTH, DDEATH or DATA, samples nothing into its
//     store-and-forward buffer, and writes no command. Only the NDEATH of
//     a stop goes out. It is the fence between a gate that closes and the
//     stop that follows it.
//   - WithBdSeqAvoid: a new session's bdSeq is never the one the peer's
//     last session carried. A will the broker fires late for the old
//     leader's session then carries a bdSeq no host takes for the new
//     leader's death.
//   - WithDeathWatch: the node listens to its own NDEATH topic. A death
//     that arrives while it is born belongs to some other session (the
//     old leader's late will), and a host that matches deaths without
//     their bdSeq has just marked this node offline: the node births
//     again at once, same bdSeq.
//   - WithNodeMetrics: metrics the node publishes beside its tags (the
//     pair's Redundancy/ folder): in every NBIRTH, and in NDATA when one
//     changes.

// notLeaderRefusal is a command write's refusal while the publish gate is
// closed.
const notLeaderRefusal = "this controller does not lead its redundant pair"

// SetPublishGate sets the gate every session message but a stop's NDEATH
// waits on. nil (the default) is always open. f is called from the publish
// tick, paho's callbacks and Stop, sometimes under the node's locks: it
// must be quick and take none of them (an atomic look).
func (n *Node) SetPublishGate(f func() bool) {
	if f == nil {
		n.gate.Store(nil)
		return
	}
	n.gate.Store(&f)
}

// gateOpen reports whether the publish gate is open.
func (n *Node) gateOpen() bool {
	if mut("spark_publish_after_gate") {
		return true
	}
	g := n.gate.Load()
	return g == nil || (*g)()
}

// WithSendCount counts every fenced publish for a switchover's drain:
// enter is called before the publish gate is read, and its done once the
// publish has ended or was not sent (naut run passes fence.Enter).
func WithSendCount(enter func() (done func())) Option {
	return func(n *Node) { n.sendCount = enter }
}

// countSend counts one fenced publish; done ends it.
func (n *Node) countSend() (done func()) {
	if n.sendCount == nil || mut("spark_send_uncounted") {
		return func() {}
	}
	return n.sendCount()
}

// WithBdSeqAvoid gives the node the bdSeq the peer's last session carried
// (ok=false when it knows none): every new session's bdSeq skips it.
func WithBdSeqAvoid(peer func() (bd uint64, ok bool)) Option {
	return func(n *Node) { n.avoidPeer = peer }
}

// SessionBdSeq is the bdSeq of the node's session, the one its will
// carries, and whether a session is up (started and not stopped): a pair's
// leader names it in its state images, so the standby that takes over
// births with another.
func (n *Node) SessionBdSeq() (uint64, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.bdSeq, n.started && !n.stopping
}

// avoidBdSeq is v, or the next value when v is the peer's last.
func (n *Node) avoidBdSeq(v uint64) uint64 {
	if n.avoidPeer == nil || mut("spark_bdseq_clash") {
		return v
	}
	if peer, ok := n.avoidPeer(); ok && v == peer%256 {
		return (v + 1) % 256
	}
	return v
}

// WithDeathWatch makes the node birth again when an NDEATH for its own
// edge node arrives while it is born (see the top of this file).
func WithDeathWatch() Option {
	return func(n *Node) { n.deathWatch = true }
}

// subscribeDeaths subscribes c to the node's own NDEATH topic.
func (n *Node) subscribeDeaths(c mqtt.Client) {
	if !n.deathWatch || mut("spark_death_watch_off") {
		return
	}
	c.Subscribe(n.topic("NDEATH"), 1, n.handleDeath)
}

// handleDeath is a death on the node's own topic. One that arrives on the
// current client while the node is born and not stopping is another
// session's: the node births again at once, as for a rebirth command
// (requestRebirth: data stops now, the birth runs off paho's goroutine).
// A death the node sent itself (a stop, an ended session) finds it unborn
// or stopping.
func (n *Node) handleDeath(c mqtt.Client, msg mqtt.Message) {
	n.mu.Lock()
	ours := c == n.cli && n.born && !n.stopping
	n.mu.Unlock()
	if !ours {
		return
	}
	var bd any
	if p, err := DecodePayload(msg.Payload()); err == nil {
		for _, m := range p.Metrics {
			if m.Name == "bdSeq" {
				bd = m.Value
			}
		}
	}
	n.log.Warn("sparkplug: an NDEATH for this edge node from another session; birthing again", "theirBdSeq", bd)
	n.requestRebirth()
}

// WithNodeMetrics adds the metrics f returns to the node's own: every
// NBIRTH carries them, and an NDATA each one that changed since. Their
// names are the node's: a tag of the same name is left out (logged once).
// f is called without the node's locks held, once per birth and once per
// publish tick; it must return the same names every time.
func WithNodeMetrics(f func() []Metric) Option {
	return func(n *Node) { n.extra = f }
}

// extraMetrics samples the extra metrics (nil without any). Called before
// n.mu is taken.
func (n *Node) extraMetrics() []Metric {
	if n.extra == nil {
		return nil
	}
	return n.extra()
}

// birthExtraLocked adds the extra metrics to a birth: known, and their
// values the baseline the data path compares against. Caller holds n.mu.
func (n *Node) birthExtraLocked(ms []Metric, ts uint64) []Metric {
	n.extraLast = make(map[string]any, len(ms))
	out := make([]Metric, 0, len(ms))
	for _, m := range ms {
		m.Timestamp = ts
		n.known[m.Name] = true
		n.extraLast[m.Name] = extraValue(m)
		out = append(out, m)
	}
	return out
}

// changedExtraLocked is the extra metrics whose value changed since they
// were last published, with ts. Caller holds n.mu.
func (n *Node) changedExtraLocked(ms []Metric, ts uint64) []Metric {
	var out []Metric
	for _, m := range ms {
		if !n.known[m.Name] {
			continue // the next birth carries it
		}
		v := extraValue(m)
		if last, ok := n.extraLast[m.Name]; ok && last == v {
			continue
		}
		n.extraLast[m.Name] = v
		m.Timestamp = ts
		out = append(out, m)
	}
	return out
}

// withExtraLocked adds the extra metrics that changed to the tick's NDATA
// (one is made when the tags gave none). Caller holds n.mu.
func (n *Node) withExtraLocked(msgs []sfRecord, extra []Metric) []sfRecord {
	if len(extra) == 0 || len(n.known) == 0 {
		return msgs
	}
	ts := nowMs()
	ch := n.changedExtraLocked(extra, ts)
	if len(ch) == 0 {
		return msgs
	}
	nd := n.topic("NDATA")
	for i := range msgs {
		if msgs[i].topic == nd {
			msgs[i].metrics = append(msgs[i].metrics, ch...)
			return msgs
		}
	}
	return append([]sfRecord{{topic: nd, metrics: ch, ts: ts}}, msgs...)
}

// extraValue is what tells one value of an extra metric from another: a
// null is its own value.
func extraValue(m Metric) any {
	if m.IsNull {
		return nullValue{}
	}
	return m.Value
}

type nullValue struct{}

// extraNames is the set of extra metric names in ms.
func extraNames(ms []Metric) map[string]bool {
	if len(ms) == 0 {
		return nil
	}
	s := make(map[string]bool, len(ms))
	for _, m := range ms {
		s[m.Name] = true
	}
	return s
}

// withoutNames drops the metrics whose names are in drop (a tag that would
// shadow an extra metric), logging each once.
func (n *Node) withoutNamesLocked(ms []Metric, drop map[string]bool) []Metric {
	if len(drop) == 0 {
		return ms
	}
	out := ms[:0]
	for _, m := range ms {
		if drop[m.Name] {
			n.warnShadowLocked(m.Name)
			continue
		}
		out = append(out, m)
	}
	return out
}

// warnShadowLocked logs, once per name, a tag left out because an extra
// metric has its name. Caller holds n.mu.
func (n *Node) warnShadowLocked(name string) {
	key := "shadow:" + name
	if n.cmdWarned == nil {
		n.cmdWarned = map[string]bool{}
	}
	if n.cmdWarned[key] {
		return
	}
	n.cmdWarned[key] = true
	n.log.Warn("sparkplug: a tag has the name of one of the node's own metrics and is not published", "tag", name)
}
