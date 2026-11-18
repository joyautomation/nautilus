package testmqtt

import (
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
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
	tcp := listeners.NewTCP(listeners.Config{ID: "t", Address: "127.0.0.1:0"})
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
