// Package testmqtt holds helpers for the in-process mochi-mqtt brokers the
// tests run. Nothing shipped imports it.
package testmqtt

import (
	"sync"
	"time"

	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// SameIDSerializer makes a test broker finish tearing down a client's ended
// session before a new connection with the same client ID takes its place,
// as a production broker does.
//
// mochi-mqtt v2.7.9 races the two. The ending session's teardown checks
// IsTakenOver, then unsubscribes and calls Clients.Delete(id); a new
// connection with that ID can be taken over and added in between, and the
// Delete then removes the NEW client from the client map. Live messages
// are delivered only to clients in that map, so the new session hears its
// retained messages on subscribe and nothing after: a Sparkplug node that
// reconnected for its primary host never heard the host come back
// (TestPrimaryHostOfflineDisconnectEndsTheSession, runs 44237035250,
// 44202630486, 44147322368).
//
// mochi's own takeover of a LIVE same-ID session races the same way: it
// disconnects the old session, unsubscribes it, and only then marks it
// taken over, so the old session's teardown can pass its IsTakenOver check
// in between and delete the new client after it is added (#828's CI run,
// TestSameIDSessionSurvivesATakeoverRace).
//
// So OnConnect, which runs before the takeover and before the new client
// is added, never lets a takeover of a clean session happen: while the ID
// belongs to a session mochi will delete, it ends that session itself (if
// it is still live) and waits until its teardown has removed it from the
// map, up to Wait (default 2 s). Only then does the new client go on, with
// no session to take over. A persistent session, which mochi never
// deletes, is left to mochi's takeover.
type SameIDSerializer struct {
	mqttsrv.HookBase
	Server *mqttsrv.Server
	Wait   time.Duration
}

// ID implements mqttsrv.Hook.
func (h *SameIDSerializer) ID() string { return "same-id-serializer" }

// Provides implements mqttsrv.Hook: OnConnect only.
func (h *SameIDSerializer) Provides(b byte) bool { return b == mqttsrv.OnConnect }

// OnConnect ends, and waits out, a clean session that still holds cl's ID.
func (h *SameIDSerializer) OnConnect(cl *mqttsrv.Client, _ packets.Packet) error {
	wait := h.Wait
	if wait == 0 {
		wait = 2 * time.Second
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		old, ok := h.Server.Clients.Get(cl.ID)
		if !ok || old == cl || !expires(old) {
			return nil
		}
		if !old.Closed() {
			_ = h.Server.DisconnectClient(old, packets.ErrSessionTakenOver)
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

// expires reports whether mochi deletes this client's session when it ends:
// the same test as its attachClient. It reads only the three fields mochi
// sets at connect, before the client is in the map. Copying the whole
// Properties would race with mochi's own writes to it while the session
// ends (its Will, server.go's attachClient): -race caught exactly that.
func expires(cl *mqttsrv.Client) bool {
	v := cl.Properties.ProtocolVersion
	return (v == 5 && cl.Properties.Props.SessionExpiryInterval == 0) ||
		(v < 5 && cl.Properties.Clean)
}

// HoldTeardown holds the first ending session of client ClientID inside its
// own teardown: after mochi's IsTakenOver check and before its
// Clients.Delete, where OnUnsubscribed runs (it needs the session to have
// had a subscription). That is the window SameIDSerializer closes; a test
// uses HoldTeardown to land a reconnect in it every time instead of by
// chance. Disconnected closes once that teardown is held, past its
// IsTakenOver check. The hold ends when a new session with the same ID is
// established (mochi has added it to its client map by then), or after
// Hold, whichever comes first: with SameIDSerializer the new session waits
// for the old one to be gone, so only Hold ends it. Build it with
// NewHoldTeardown.
type HoldTeardown struct {
	mqttsrv.HookBase
	ClientID     string
	Hold         time.Duration
	Disconnected chan struct{}

	mu      sync.Mutex
	tearing *mqttsrv.Client
	held    bool
	next    chan struct{} // closed when a new same-ID session is established
}

// NewHoldTeardown holds client id's first ending session for at most hold.
func NewHoldTeardown(id string, hold time.Duration) *HoldTeardown {
	return &HoldTeardown{ClientID: id, Hold: hold, Disconnected: make(chan struct{}), next: make(chan struct{})}
}

// ID implements mqttsrv.Hook.
func (h *HoldTeardown) ID() string { return "hold-teardown" }

// Provides implements mqttsrv.Hook.
func (h *HoldTeardown) Provides(b byte) bool {
	return b == mqttsrv.OnDisconnect || b == mqttsrv.OnUnsubscribed || b == mqttsrv.OnSessionEstablished
}

// OnDisconnect marks the start of the session's own teardown.
func (h *HoldTeardown) OnDisconnect(cl *mqttsrv.Client, _ error, _ bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cl.ID == h.ClientID && h.tearing == nil {
		h.tearing = cl
	}
}

// OnUnsubscribed holds that teardown, once. A takeover calls
// UnsubscribeClient on the old client too, from the new client's
// goroutine, before OnDisconnect has run there; only the client whose own
// teardown began is held.
func (h *HoldTeardown) OnUnsubscribed(cl *mqttsrv.Client, _ packets.Packet) {
	h.mu.Lock()
	hold := cl == h.tearing && !h.held
	if hold {
		h.held = true
		close(h.Disconnected)
	}
	h.mu.Unlock()
	if hold {
		select {
		case <-h.next:
		case <-time.After(h.Hold):
		}
	}
}

// OnSessionEstablished ends the hold once a new session with the ID is in
// mochi's client map (mochi adds it before this hook runs).
func (h *HoldTeardown) OnSessionEstablished(cl *mqttsrv.Client, _ packets.Packet) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held && cl.ID == h.ClientID && cl != h.tearing {
		select {
		case <-h.next:
		default:
			close(h.next)
		}
	}
}
