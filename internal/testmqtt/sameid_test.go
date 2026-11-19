package testmqtt

import (
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

func broker(t *testing.T, serialize bool, hold time.Duration) (*mqttsrv.Server, string, *HoldTeardown) {
	t.Helper()
	srv := mqttsrv.New(&mqttsrv.Options{Logger: quiet()})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	slow := NewHoldTeardown("same-id", hold)
	if err := srv.AddHook(slow, nil); err != nil {
		t.Fatal(err)
	}
	if serialize {
		if err := srv.AddHook(&SameIDSerializer{Server: srv}, nil); err != nil {
			t.Fatal(err)
		}
	}
	tcp := NewTCP(srv, listeners.Config{ID: "t", Address: "127.0.0.1:0"})
	if err := srv.AddListener(tcp); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = Close(srv) })
	return srv, tcp.Address(), slow
}

func connect(t *testing.T, addr, id string) mqtt.Client {
	t.Helper()
	c := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + addr).
		SetClientID(id).SetCleanSession(true).SetAutoReconnect(false))
	if tok := c.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("%s connect: %v", id, tok.Error())
	}
	t.Cleanup(func() { c.Disconnect(0) })
	return c
}

// reconnect ends a clean session of client "same-id" and, while the broker
// is still tearing it down (held past its IsTakenOver check), connects a
// new one with that ID (what a Sparkplug node does when its primary host
// goes offline, and a host on demotion) and subscribes it to t/live. It
// returns a publisher, the new session, and a channel that receives when
// the new session hears a publish on t/live.
func reconnect(t *testing.T, addr string, slow *HoldTeardown) (pub, fresh mqtt.Client, heard <-chan struct{}) {
	t.Helper()
	pub = connect(t, addr, "pub")
	old := connect(t, addr, "same-id")
	old.Subscribe("t/live", 1, nil).WaitTimeout(5 * time.Second)
	old.Disconnect(250)
	select {
	case <-slow.Disconnected: // its teardown is held, past its IsTakenOver check
	case <-time.After(5 * time.Second):
		t.Fatal("the broker never began tearing the old session down")
	}

	got := make(chan struct{}, 1)
	fresh = connect(t, addr, "same-id")
	tok := fresh.Subscribe("t/live", 1, func(mqtt.Client, mqtt.Message) {
		select {
		case got <- struct{}{}:
		default:
		}
	})
	if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("subscribe: %v", tok.Error())
	}
	return pub, fresh, got
}

// With SameIDSerializer the new session hears live messages however its
// predecessor's teardown is timed.
func TestSameIDSessionHearsLiveMessages(t *testing.T) {
	_, addr, slow := broker(t, true, 300*time.Millisecond)
	pub, _, heard := reconnect(t, addr, slow)
	time.Sleep(400 * time.Millisecond) // past the held teardown
	pub.Publish("t/live", 1, false, []byte("x")).WaitTimeout(5 * time.Second)
	select {
	case <-heard:
	case <-time.After(2 * time.Second):
		t.Fatal("the new same-ID session never heard a live publish: its predecessor's teardown removed it from the broker")
	}
}

// Without it, mochi v2.7.9 loses the new session. This pins the upstream
// bug SameIDSerializer works around: once it fails, mochi has fixed the
// race and the serializer can go.
//
// The order is forced, not left to chance: the old session's teardown is
// held past its IsTakenOver check until the new session is established
// (and so in mochi's client map), and only then reaches Clients.Delete.
// The long Hold is only a backstop for a slow runner; the new session's
// arrival ends it.
func TestMochiLosesASameIDSessionWithoutTheSerializer(t *testing.T) {
	srv, addr, slow := broker(t, false, 10*time.Second)
	pub, fresh, heard := reconnect(t, addr, slow)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := srv.Clients.Get("same-id"); !ok {
			break // the old teardown deleted the new session
		}
		if time.Now().After(deadline) {
			t.Fatal("mochi no longer loses a same-ID session in the teardown window: the race is fixed upstream, drop SameIDSerializer")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Out of the map, it hears nothing live, though it is still connected.
	if !fresh.IsConnectionOpen() {
		t.Fatal("the new session's connection closed: that, not the teardown race, emptied the client map")
	}
	pub.Publish("t/live", 1, false, []byte("x")).WaitTimeout(5 * time.Second)
	select {
	case <-heard:
		t.Fatal("mochi delivered a live publish to a session it had dropped from its client map: drop SameIDSerializer if the race is fixed upstream")
	case <-time.After(time.Second):
	}
}

// takeoverRace forces the losing order inside mochi's own takeover of a
// live same-ID session: the takeover's UnsubscribeClient(old) runs before
// it marks the old session taken over, and the old session's teardown,
// started by the takeover's DisconnectClient, checks IsTakenOver in that
// gap, then reaches Clients.Delete(id) after the new client was added.
//
// Each step waits for the one before it, not for a fixed time:
//  1. the old session's OnDisconnect (its teardown, before the IsTakenOver
//     check) waits until the takeover's OnUnsubscribed for it is held (A);
//  2. A holds the takeover until the old teardown's own OnUnsubscribed,
//     past its IsTakenOver check, begins (B);
//  3. B holds the old teardown until the new session is established, so
//     in mochi's client map, and only then lets it reach Clients.Delete.
//
// backstop bounds every wait. With SameIDSerializer no takeover runs, so
// steps 1 and 3 wait it out: twice it must stay well under the
// serializer's Wait.
type takeoverRace struct {
	mqttsrv.HookBase
	backstop time.Duration

	mu           sync.Mutex
	old          *mqttsrv.Client
	torn         bool
	heldA, heldB bool
	aHeld        chan struct{} // the takeover's unsubscribe is held
	bHeld        chan struct{} // the old teardown is past IsTakenOver
	freshIn      chan struct{} // a new same-ID session is established
}

func newTakeoverRace(backstop time.Duration) *takeoverRace {
	return &takeoverRace{backstop: backstop, aHeld: make(chan struct{}),
		bHeld: make(chan struct{}), freshIn: make(chan struct{})}
}

func (h *takeoverRace) ID() string { return "takeover-race" }

func (h *takeoverRace) Provides(b byte) bool {
	return b == mqttsrv.OnSessionEstablished || b == mqttsrv.OnDisconnect || b == mqttsrv.OnUnsubscribed
}

func (h *takeoverRace) wait(ch <-chan struct{}) {
	select {
	case <-ch:
	case <-time.After(h.backstop):
	}
}

func (h *takeoverRace) OnSessionEstablished(cl *mqttsrv.Client, _ packets.Packet) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case cl.ID != "same-id":
	case h.old == nil:
		h.old = cl
	case cl != h.old && h.heldB:
		select {
		case <-h.freshIn:
		default:
			close(h.freshIn)
		}
	}
}

func (h *takeoverRace) OnDisconnect(cl *mqttsrv.Client, _ error, _ bool) {
	h.mu.Lock()
	mine := cl == h.old && !h.torn
	h.mu.Unlock()
	if !mine {
		return
	}
	h.wait(h.aHeld)
	h.mu.Lock()
	h.torn = true
	h.mu.Unlock()
}

func (h *takeoverRace) OnUnsubscribed(cl *mqttsrv.Client, _ packets.Packet) {
	h.mu.Lock()
	switch {
	case cl != h.old:
		h.mu.Unlock()
	case !h.torn && !h.heldA:
		h.heldA = true
		close(h.aHeld)
		h.mu.Unlock()
		h.wait(h.bHeld)
	case h.torn && !h.heldB:
		h.heldB = true
		close(h.bHeld)
		h.mu.Unlock()
		h.wait(h.freshIn)
	default:
		h.mu.Unlock()
	}
}

// A new same-ID connection while the old session is still LIVE (its
// DISCONNECT not yet processed: the CI failure on #828, run 44248331484).
// mochi takes the old session over, and its teardown can delete the new
// client; SameIDSerializer ends the old session itself and waits for it to
// be gone, so no takeover runs.
func TestSameIDSessionSurvivesATakeoverRace(t *testing.T) {
	for _, serialize := range []bool{true, false} {
		srv := mqttsrv.New(&mqttsrv.Options{Logger: quiet()})
		if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
			t.Fatal(err)
		}
		// Without the serializer the waits are chained and end at once;
		// the backstop only keeps a fixed mochi from hanging the connect
		// (connect gives up at 5 s).
		backstop := 3 * time.Second
		if serialize {
			backstop = 300 * time.Millisecond
		}
		if err := srv.AddHook(newTakeoverRace(backstop), nil); err != nil {
			t.Fatal(err)
		}
		if serialize {
			// Wait well past the two backstops the old teardown sits out.
			if err := srv.AddHook(&SameIDSerializer{Server: srv, Wait: 5 * time.Second}, nil); err != nil {
				t.Fatal(err)
			}
		}
		tcp := NewTCP(srv, listeners.Config{ID: "t", Address: "127.0.0.1:0"})
		if err := srv.AddListener(tcp); err != nil {
			t.Fatal(err)
		}
		go func() { _ = srv.Serve() }()
		t.Cleanup(func() { _ = Close(srv) })
		addr := tcp.Address()

		pub := connect(t, addr, "pub")
		old := connect(t, addr, "same-id")
		old.Subscribe("t/live", 1, nil).WaitTimeout(5 * time.Second)
		// The old session stays connected: the new one connects over it.
		heard := make(chan struct{}, 1)
		fresh := connect(t, addr, "same-id")
		tok := fresh.Subscribe("t/live", 1, func(mqtt.Client, mqtt.Message) {
			select {
			case heard <- struct{}{}:
			default:
			}
		})
		if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
			t.Fatalf("subscribe: %v", tok.Error())
		}
		if !serialize {
			// The old teardown deletes the new client from the map.
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, ok := srv.Clients.Get("same-id"); !ok {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("mochi no longer loses a same-ID session in a takeover race: the bug is fixed upstream, drop SameIDSerializer")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !fresh.IsConnectionOpen() {
				t.Fatal("the new session's connection closed: that, not the teardown race, emptied the client map")
			}
		}
		pub.Publish("t/live", 1, false, []byte("x")).WaitTimeout(5 * time.Second)
		got := false
		select {
		case <-heard:
			got = true
		case <-time.After(time.Second):
		}
		switch {
		case serialize && !got:
			t.Error("with SameIDSerializer the new same-ID session never heard a live publish after a takeover race")
		case !serialize && got:
			t.Error("mochi delivered a live publish to a session it had dropped from its client map: drop SameIDSerializer if the race is fixed upstream")
		}
	}
}
