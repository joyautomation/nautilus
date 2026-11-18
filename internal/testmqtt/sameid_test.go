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

func broker(t *testing.T, serialize bool) (string, *HoldTeardown) {
	t.Helper()
	srv := mqttsrv.New(&mqttsrv.Options{Logger: quiet()})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	slow := NewHoldTeardown("same-id", 300*time.Millisecond)
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
	return tcp.Address(), slow
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

// reconnectHears ends a clean session of client "same-id" and, while the
// broker is still tearing it down, connects a new one with that ID (what a
// Sparkplug node does when its primary host goes offline, and a host on
// demotion). It reports whether the new session hears a live publish.
func reconnectHears(t *testing.T, addr string, slow *HoldTeardown) bool {
	t.Helper()
	pub := connect(t, addr, "pub")
	old := connect(t, addr, "same-id")
	old.Subscribe("t/live", 1, nil).WaitTimeout(5 * time.Second)
	old.Disconnect(250)
	select {
	case <-slow.Disconnected: // its teardown has begun, and is held
	case <-time.After(5 * time.Second):
		t.Fatal("the broker never began tearing the old session down")
	}

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
	time.Sleep(400 * time.Millisecond) // past the held teardown
	pub.Publish("t/live", 1, false, []byte("x")).WaitTimeout(5 * time.Second)
	select {
	case <-heard:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

// With SameIDSerializer the new session hears live messages however its
// predecessor's teardown is timed.
func TestSameIDSessionHearsLiveMessages(t *testing.T) {
	addr, slow := broker(t, true)
	if !reconnectHears(t, addr, slow) {
		t.Fatal("the new same-ID session never heard a live publish: its predecessor's teardown removed it from the broker")
	}
}

// Without it, mochi v2.7.9 loses the new session. This pins the upstream
// bug SameIDSerializer works around: once it fails, mochi has fixed the
// race and the serializer can go.
func TestMochiLosesASameIDSessionWithoutTheSerializer(t *testing.T) {
	addr, slow := broker(t, false)
	if reconnectHears(t, addr, slow) {
		t.Fatal("mochi no longer loses a same-ID session in the teardown window: the race is fixed upstream, drop SameIDSerializer")
	}
}

// takeoverRace forces the losing order inside mochi's own takeover of a
// live same-ID session: the takeover's UnsubscribeClient(old) runs before
// it marks the old session taken over, and the old session's teardown,
// started by the takeover's DisconnectClient, checks IsTakenOver in that
// gap, then reaches Clients.Delete(id) after the new client was added.
// The first OnUnsubscribed for the old client (the takeover's, before the
// old session's OnDisconnect) is held 200 ms; the second (the old
// teardown's own) 500 ms.
type takeoverRace struct {
	mqttsrv.HookBase
	mu    sync.Mutex
	old   *mqttsrv.Client
	torn  bool
	heldA bool
	heldB bool
}

func (h *takeoverRace) ID() string { return "takeover-race" }

func (h *takeoverRace) Provides(b byte) bool {
	return b == mqttsrv.OnSessionEstablished || b == mqttsrv.OnDisconnect || b == mqttsrv.OnUnsubscribed
}

func (h *takeoverRace) OnSessionEstablished(cl *mqttsrv.Client, _ packets.Packet) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cl.ID == "same-id" && h.old == nil {
		h.old = cl
	}
}

func (h *takeoverRace) OnDisconnect(cl *mqttsrv.Client, _ error, _ bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cl == h.old {
		h.torn = true
	}
}

func (h *takeoverRace) OnUnsubscribed(cl *mqttsrv.Client, _ packets.Packet) {
	h.mu.Lock()
	var hold time.Duration
	switch {
	case cl != h.old:
	case !h.torn && !h.heldA:
		h.heldA, hold = true, 200*time.Millisecond
	case h.torn && !h.heldB:
		h.heldB, hold = true, 500*time.Millisecond
	}
	h.mu.Unlock()
	time.Sleep(hold)
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
		if err := srv.AddHook(new(takeoverRace), nil); err != nil {
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
		time.Sleep(800 * time.Millisecond) // past both holds
		pub.Publish("t/live", 1, false, []byte("x")).WaitTimeout(5 * time.Second)
		got := false
		select {
		case <-heard:
			got = true
		case <-time.After(2 * time.Second):
		}
		switch {
		case serialize && !got:
			t.Error("with SameIDSerializer the new same-ID session never heard a live publish after a takeover race")
		case !serialize && got:
			t.Error("mochi no longer loses a same-ID session in a takeover race: the bug is fixed upstream, drop SameIDSerializer")
		}
	}
}
