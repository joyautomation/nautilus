package host

// Discovery is the one part of the importer that cannot be a pure function:
// retained births are forbidden, so `nautilus sparkplug import` has to hear a
// node, ask it to birth, and wait. This test drives that loop against the
// in-process mochi broker (mqtt_test.go's startBroker) with a raw paho client
// standing in for an edge node that is already running — the exact case the
// import exists for.
//
// ADDED BY C2 alongside observe.go.

import (
	"context"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

func TestDiscoverAsksForRebirth(t *testing.T) {
	_, addr := startBroker(t, "")

	// A fake edge that is mid-session: it publishes NDATA (which is all a
	// mid-stream listener would ever see) and births only when asked.
	edge := mqtt.NewClient(mqtt.NewClientOptions().
		AddBroker(brokerURL(addr)).
		SetClientID("fake-edge").
		SetCleanSession(true))
	if tok := edge.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("edge connect: %v", tok.Error())
	}
	defer edge.Disconnect(100)

	pub := func(topic string, p sparkplug.Payload) {
		raw, err := p.Encode()
		if err != nil {
			t.Errorf("encode: %v", err)
			return
		}
		edge.Publish(topic, 0, false, raw).Wait()
	}

	asked := make(chan struct{}, 1)
	tok := edge.Subscribe("spBv1.0/G/NCMD/W6", 1, func(_ mqtt.Client, m mqtt.Message) {
		p, err := sparkplug.DecodePayload(m.Payload())
		if err != nil || len(p.Metrics) == 0 || p.Metrics[0].Name != RebirthMetric {
			t.Errorf("unexpected NCMD: %v %v", p, err)
			return
		}
		pub("spBv1.0/G/NBIRTH/W6", sparkplug.Payload{Timestamp: 7, Seq: 0, Metrics: []sparkplug.Metric{
			{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: int64(1)},
			{Name: "Well/Level", Datatype: spb.DataType_Double, Value: 4.5},
		}})
		pub("spBv1.0/G/DBIRTH/W6/PLC1", sparkplug.Payload{Timestamp: 7, Seq: 1, Metrics: []sparkplug.Metric{
			{Name: "Pump/Run", Datatype: spb.DataType_Boolean, Value: true},
		}})
		select {
		case asked <- struct{}{}:
		default:
		}
	})
	if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("edge subscribe: %v", tok.Error())
	}

	// Keep the node visible until the importer has subscribed: a listener
	// that joins mid-stream learns the node id from NDATA alone.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
				pub("spBv1.0/G/NDATA/W6", sparkplug.Payload{Timestamp: 8, Seq: 1,
					Metrics: []sparkplug.Metric{{Name: "Well/Level", Datatype: spb.DataType_Double, Value: 4.5}}})
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started := time.Now()
	births, err := Discover(ctx, DiscoverOptions{
		BrokerURL: brokerURL(addr),
		ClientID:  "nautilus-import-test",
		Groups:    []string{"G"},
		Listen:    10 * time.Second,
		Quiet:     500 * time.Millisecond,
		Rebirth:   true,
		Log:       quietLogger(),
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	// The fake edge keeps publishing NDATA throughout, so this also pins the
	// early exit: it counts NEW information, not silence — a real group with
	// sixty sites on it is never quiet, and every import would otherwise cost
	// the whole --listen window.
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("discovery took %s; it should have returned once the births settled", elapsed)
	}
	select {
	case <-asked:
	default:
		t.Fatal("discovery never asked the node to birth")
	}

	if len(births) != 2 {
		t.Fatalf("births = %d, want 2 (NBIRTH + DBIRTH): %+v", len(births), births)
	}
	if !births[0].IsNode() || births[0].EdgeNode != "W6" || births[0].Group != "G" {
		t.Errorf("first birth = %+v, want the NBIRTH for G/W6", births[0])
	}
	if births[1].Device != "PLC1" {
		t.Errorf("second birth = %+v, want the DBIRTH for PLC1", births[1])
	}
	// The bytes come back decoded through the driver's own decoder, so the
	// generator sees exactly what the runtime would.
	var found bool
	for _, m := range births[0].Payload.Metrics {
		if m.Name == "Well/Level" && m.Value == 4.5 {
			found = true
		}
	}
	if !found {
		t.Errorf("NBIRTH metrics = %+v", births[0].Payload.Metrics)
	}
}
