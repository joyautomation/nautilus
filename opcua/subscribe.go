// subscribe.go is the subscription read mode (read-mode: subscribe): each
// scan class becomes one OPC UA subscription whose publishing interval is
// the class rate, with one monitored item per binding sampled at the same
// rate. The server sends a value when it changes, instead of the driver
// asking for every value on every tick.
//
// Everything that is not "how values arrive" is the polled mode's, on
// purpose, so the two modes cannot drift apart:
//
//   - The session, the reconnect ladder and the write path are shared.
//     Subscriptions are created fresh on every new session; nothing is
//     transferred or republished from the last one (TransferSubscriptions
//     would carry server-side state across exactly the boundary where the
//     driver resets its own).
//   - A new session is primed with the polled mode's batched Read before
//     any subscription exists, so __Online, "primed" and the
//     Good/Stale/NotConnected table mean what they mean when polling. The
//     initial notification every monitored item sends repeats a value the
//     prime already delivered, harmlessly.
//   - Data change notifications go through the same apply as a Read
//     result: a non-good status is Bad, a non-scalar value is Bad.
//
// What the protocol does NOT give a client is a reliable signal that the
// session died. gopcua's publish loop pauses itself on a transport error
// and keep-alive messages never reach the notification channel, so a
// subscribed driver that only listened would hold its last values as Good
// through an outage. The driver therefore probes the session once a
// second: SetPublishingMode(enabled) on its own subscription ids, which
// fails on a dead transport and answers Bad_SubscriptionIdInvalid for a
// subscription the server has dropped. A server that does not implement
// SetPublishingMode (Bad_ServiceUnsupported) is probed with a Read of
// Server/ServerStatus/State instead, which still catches a dead transport.
// The probe also checks gopcua's client state: any service fault stops
// gopcua's publish loop while reads go on working (see probe), and the
// state is the only sign of it. A StatusChangeNotification or an error on
// the notification channel also ends the session. Ending it is the same as
// a failed read when polling: close, back off, dial again, prime,
// subscribe. On top of that each class is read in full now and then
// (refreshEvery), which bounds how long a lost notification can go
// unnoticed whatever lost it.
//
// A server that refuses a subscription (Bad_TooManySubscriptions, say) is
// a failed session, logged and retried on the ladder. The driver does not
// fall back to polling: a silent fallback would put back exactly the load
// the project chose subscriptions to avoid, and nobody would see why.
package opcua

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

// Read modes (WithReadMode, the manifest's read-mode:).
const (
	ReadModePoll      = "poll"
	ReadModeSubscribe = "subscribe"
)

// WithReadMode selects how values arrive: "poll" (the default, one batched
// Read per scan class per tick) or "subscribe" (one subscription per scan
// class, notifications on change). An unknown mode fails New.
func WithReadMode(mode string) Option { return func(d *Driver) { d.readMode = mode } }

// probeInterval is how often a subscribed session is checked (see the
// package comment above), and how often the write gate is looked at.
const probeInterval = time.Second

// maxMonitoredItemsPerCallNode is Server_ServerCapabilities_OperationLimits_
// MaxMonitoredItemsPerCall (Part 5 §6.3.11), ns=0;i=11714.
const maxMonitoredItemsPerCallNode = 11714

// orphanLifetime bounds how long a server keeps a subscription whose client
// stopped publishing without closing its session (a client that lost power
// or its link). The driver has long since given that session up and
// subscribed afresh on a new one, so a long lifetime only holds server
// resources. The lifetime is never under three keep-alives, the spec's
// floor.
const orphanLifetime = time.Minute

// subscriptionParams derives a class's subscription from its rate and the
// request timeout:
//
//   - The keep-alive window (keep-alive count × publishing interval) is at
//     most half the request timeout. gopcua gives up on a Publish after
//     max(keep-alive window, request timeout) and drops whatever answers
//     it later; with the window equal to that deadline, a quiet
//     subscription races its own keep-alive, and a notification that loses
//     the race is gone (the next one only comes with the next change).
//   - So the publishing interval is the class rate, but never more than a
//     quarter of the timeout: a 60 s class is SAMPLED every 60 s and
//     published every 1.25 s (with a 5 s timeout), which costs a keep-alive
//     message every couple of seconds and nothing else.
func subscriptionParams(rate, timeout time.Duration) (interval time.Duration, keepAlive, lifetime uint32) {
	interval = min(rate, timeout/4)
	if interval <= 0 {
		interval = rate
	}
	keepAlive = uint32(max(int64(timeout/2)/int64(interval), 1))
	lifetime = max(uint32(math.Ceil(float64(orphanLifetime)/float64(interval))), 3*keepAlive)
	return interval, keepAlive, lifetime
}

// refreshEvery is how often a subscribed class is also read once, in full:
// every 30 s, or every ten of its periods for a slower class. A
// notification can be lost without the session failing (a publish the
// library gave up on, a publish loop it paused on an error it does not
// report); the refresh bounds how long a value can be wrong while reading
// Good to this, instead of until the value next changes.
func refreshEvery(rate, floor time.Duration) time.Duration { return max(floor, 10*rate) }

const defaultRefreshFloor = 30 * time.Second

// subSession is one session's subscriptions: the client handle of every
// monitored item indexes handles (handle-1), shared across the classes,
// so one notification channel serves them all.
type subSession struct {
	ids     []uint32
	handles []*binding
	ch      chan *opcua.PublishNotificationData
	// refresh is when each class (by index in d.classes) is next read in
	// full (refreshEvery).
	refresh []time.Time
}

// serveSubscribed is serve for read-mode: subscribe. Same entry (flush the
// queued writes, prime every class with a Read), then one subscription per
// class, then notifications, writes and the probe until the session breaks.
func (d *Driver) serveSubscribed(ctx context.Context, sess *Session) {
	if !d.flushWrites(ctx, sess) {
		return
	}
	for _, c := range d.classes {
		if !d.pollClass(ctx, sess, c) {
			return
		}
	}
	d.settled.Store(true) // every class has delivered its priming read (InputsReady)
	ss, err := d.subscribe(ctx, sess)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		d.errs.Add(1)
		d.mu.Lock()
		d.lastErr = err
		d.retries++
		d.mu.Unlock()
		d.log.Warn("opcua-client: subscribe failed", "driver", d.name, "error", err)
		return
	}
	d.mu.Lock()
	d.primed = true
	d.mu.Unlock()
	now := time.Now()
	for _, c := range d.classes {
		ss.refresh = append(ss.refresh, now.Add(refreshEvery(c.rate, d.refreshFloor)))
	}

	probe := time.NewTicker(probeInterval)
	defer probe.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.kick:
			if !d.flushWrites(ctx, sess) {
				return
			}
		case <-probe.C:
			d.gateOpen() // a look, even with nothing queued (see SetWriteGate)
			if !d.flushWrites(ctx, sess) {
				return
			}
			if !d.probe(ctx, sess, ss) {
				return
			}
			now := time.Now()
			for i, c := range d.classes {
				if now.Before(ss.refresh[i]) {
					continue
				}
				if !d.pollClass(ctx, sess, c) {
					return
				}
				ss.refresh[i] = time.Now().Add(refreshEvery(c.rate, d.refreshFloor))
			}
		case n := <-ss.ch:
			if !d.notified(ss, n) {
				return
			}
		}
	}
}

// subscribe creates one subscription per scan class and its monitored
// items, in chunks of the server's MaxMonitoredItemsPerCall. A monitored
// item the server refuses makes its binding Bad (it would never be
// updated); a refused subscription fails the session.
func (d *Driver) subscribe(ctx context.Context, sess *Session) (*subSession, error) {
	ss := &subSession{ch: make(chan *opcua.PublishNotificationData, 256)}
	d.mu.Lock()
	limit := d.server.maxItems
	d.mu.Unlock()
	revised := map[string]time.Duration{}
	for _, c := range d.classes {
		interval, keepAlive, lifetime := subscriptionParams(c.rate, d.conn.Timeout)
		rctx, cancel := context.WithTimeout(ctx, d.conn.Timeout)
		sub, err := sess.Client.Subscribe(rctx, &opcua.SubscriptionParameters{
			Interval:          interval,
			MaxKeepAliveCount: keepAlive,
			LifetimeCount:     lifetime,
		}, ss.ch)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("create the subscription for scan class %q: %w", c.name, err)
		}
		ss.ids = append(ss.ids, sub.SubscriptionID)
		revised[c.name] = sub.RevisedPublishingInterval
		if sub.RevisedPublishingInterval > interval {
			d.log.Info("opcua-client: the server publishes slower than asked", "driver", d.name,
				"class", c.name, "requested", interval, "revised", sub.RevisedPublishingInterval)
		}
		if window := time.Duration(sub.RevisedMaxKeepAliveCount) * sub.RevisedPublishingInterval; window >= d.conn.Timeout {
			d.log.Warn("opcua-client: the server's keep-alive is not shorter than the request timeout, so a quiet subscription can lose a notification until the next refresh read; raise the timeout",
				"driver", d.name, "class", c.name, "keepAlive", window, "timeout", d.conn.Timeout)
		}

		var reqs []*ua.MonitoredItemCreateRequest
		var bound []*binding
		d.mu.Lock()
		for _, b := range c.bindings {
			if b.missing {
				continue // not resolvable on this server (see onConnect)
			}
			ss.handles = append(ss.handles, b)
			reqs = append(reqs, &ua.MonitoredItemCreateRequest{
				ItemToMonitor: &ua.ReadValueID{
					NodeID:       b.node,
					AttributeID:  ua.AttributeIDValue,
					DataEncoding: &ua.QualifiedName{},
				},
				MonitoringMode: ua.MonitoringModeReporting,
				RequestedParameters: &ua.MonitoringParameters{
					ClientHandle:     uint32(len(ss.handles)),
					SamplingInterval: float64(c.rate) / float64(time.Millisecond),
					QueueSize:        1, // the latest value is the only one a scan reads
					DiscardOldest:    true,
				},
			})
			bound = append(bound, b)
		}
		d.mu.Unlock()

		for start := 0; start < len(reqs); {
			end := min(start+max(limit, 1), len(reqs))
			rctx, cancel := context.WithTimeout(ctx, d.conn.Timeout)
			resp, err := sub.Monitor(rctx, ua.TimestampsToReturnNeither, reqs[start:end]...)
			cancel()
			if errors.Is(err, ua.StatusBadTooManyOperations) && limit > 1 {
				limit = max(limit/2, 1)
				d.itemCap.Store(int64(limit)) // the fault closes this session; the next one starts here
				d.mu.Lock()
				d.server.maxItems = limit
				d.mu.Unlock()
				d.log.Warn("opcua-client: too many monitored items in one call, halving", "driver", d.name, "maxItems", limit)
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("create monitored items for scan class %q: %w", c.name, err)
			}
			if len(resp.Results) != end-start {
				return nil, fmt.Errorf("create monitored items for scan class %q: %d results for %d items", c.name, len(resp.Results), end-start)
			}
			d.mu.Lock()
			for i, r := range resp.Results {
				if good(r.StatusCode) {
					continue
				}
				b := bound[start+i]
				if !b.bad {
					d.lastErr = fmt.Errorf("tag %s: monitored item refused: %v", b.Name, r.StatusCode)
				}
				b.bad, b.notMonitored = true, true
				d.log.Warn("opcua-client: monitored item refused; tag reads Bad", "driver", d.name, "tag", b.Name, "status", r.StatusCode)
			}
			d.mu.Unlock()
			start = end
		}
	}
	d.mu.Lock()
	d.server.subscriptions = len(ss.ids)
	d.server.revised = revised
	d.mu.Unlock()
	return ss, nil
}

// notified applies one publish result. False ends the session: an error
// gopcua put on the channel, or a subscription the server reports as
// gone (a StatusChangeNotification with a bad status, e.g. Bad_Timeout).
func (d *Driver) notified(ss *subSession, n *opcua.PublishNotificationData) bool {
	if n == nil {
		return true
	}
	if n.Error != nil {
		d.lost(fmt.Errorf("subscription %d: %w", n.SubscriptionID, n.Error))
		return false
	}
	switch v := n.Value.(type) {
	case *ua.DataChangeNotification:
		d.notifications.Add(1)
		d.mu.Lock()
		d.answered = true
		for _, item := range v.MonitoredItems {
			if item == nil || item.ClientHandle == 0 || int(item.ClientHandle) > len(ss.handles) {
				continue // not one of ours
			}
			d.apply(ss.handles[item.ClientHandle-1], item.Value)
		}
		d.mu.Unlock()
	case *ua.StatusChangeNotification:
		if !good(v.Status) {
			d.lost(fmt.Errorf("subscription %d: %v", n.SubscriptionID, v.Status))
			return false
		}
	}
	return true
}

// probe checks the session is alive and the server still has its
// subscriptions (see the package comment above). False ends the session.
//
// It also asks gopcua whether its own subscription machinery is still
// running. In gopcua v0.9.1 ANY response whose ServiceResult is not Good
// (a service fault: Bad_ServiceUnsupported, Bad_TooManyOperations, a
// server-side Bad_Timeout on a Publish, ...) reaches the client's monitor,
// which, with AutoReconnect off, marks the client closed and stops the
// publish loop for good while the secure channel stays open. Reads keep
// working; notifications silently stop. The client's state is the only
// sign of it, so a state other than Connected ends the session.
func (d *Driver) probe(ctx context.Context, sess *Session, ss *subSession) bool {
	if !d.libraryRunning(sess) {
		return false
	}
	rctx, cancel := context.WithTimeout(ctx, d.conn.Timeout)
	defer cancel()
	kind := probeRead
	if !d.probeByRead.Load() {
		kind = probeSetPublishingMode
		var res *ua.SetPublishingModeResponse
		err := sess.Client.Send(rctx, &ua.SetPublishingModeRequest{PublishingEnabled: true, SubscriptionIDs: ss.ids},
			func(v ua.Response) error {
				r, ok := v.(*ua.SetPublishingModeResponse)
				if !ok {
					return fmt.Errorf("SetPublishingMode answered with %T", v)
				}
				res = r
				return nil
			})
		switch {
		case errors.Is(err, ua.StatusBadServiceUnsupported):
			// The fault itself has stopped gopcua's publish loop (see
			// above), so this session is done. Every later session on this
			// driver probes with a Read, which a server never faults.
			d.probeByRead.Store(true)
			d.lost(fmt.Errorf("the server does not support SetPublishingMode, and its refusal stopped the subscription loop; subscribing again, probed by a Read from now on"))
			return false
		case err != nil:
			if ctx.Err() == nil {
				d.lost(fmt.Errorf("probe: %w", err))
			}
			return false
		}
		for i, sc := range res.Results {
			if sc != ua.StatusOK && i < len(ss.ids) {
				d.lost(fmt.Errorf("subscription %d: %v", ss.ids[i], sc))
				return false
			}
		}
	} else {
		_, err := sess.Client.Read(rctx, &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{
			NodeID: ua.NewNumericNodeID(0, serverStateNode), AttributeID: ua.AttributeIDValue}}})
		if err != nil {
			if ctx.Err() == nil {
				d.lost(fmt.Errorf("probe: %w", err))
			}
			return false
		}
	}
	if !d.libraryRunning(sess) {
		return false
	}
	d.mu.Lock()
	d.server.probe = kind
	d.mu.Unlock()
	return true
}

// libraryRunning reports whether gopcua's client is still Connected, its
// publish loop alive (see probe). False records why the session ends.
func (d *Driver) libraryRunning(sess *Session) bool {
	if st := sess.Client.State(); st != opcua.Connected {
		d.lost(fmt.Errorf("the OPC UA client stopped its subscription loop (state %v), usually after the server answered a request with a service fault; subscribing again", st))
		return false
	}
	return true
}

// The session probes, as Health.Probe names them.
const (
	probeSetPublishingMode = "SetPublishingMode"
	probeRead              = "Read"
)

// lost records why a subscribed session is being given up.
func (d *Driver) lost(err error) {
	d.errs.Add(1)
	d.mu.Lock()
	d.lastErr = err
	d.retries++
	d.mu.Unlock()
	d.log.Warn("opcua-client: subscribed session failed its check", "driver", d.name, "error", err)
}
