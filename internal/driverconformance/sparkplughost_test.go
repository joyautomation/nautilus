package driverconformance

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/joyautomation/nautilus/internal/testmqtt"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/host"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// sparkplug/host against a broker (mochi, through internal/testmqtt) and
// an edge node stand-in on a raw MQTT client: it births node W6 and device
// PLC1, publishes DDATA every few milliseconds, answers rebirth requests,
// and applies and records every DCMD. The host reaches the broker through
// the link; the edge does not, so a cut is the host losing its broker. The
// input is PLC1's In, the output its Out. A failed read on a live link is
// the edge publishing In as null (is_null: it has no value for it).
func init() {
	register(subject{
		name: "sparkplug-host",
		gate: holdAndDeliver,
		start: func(t tb, o startOpts) *rig {
			srv := mqttsrv.New(&mqttsrv.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
				t.Fatalf("broker: %v", err)
			}
			// The host reconnects with the same client ID when its gate
			// turns (testmqtt.SameIDSerializer).
			if err := srv.AddHook(&testmqtt.SameIDSerializer{Server: srv}, nil); err != nil {
				t.Fatalf("broker: %v", err)
			}
			tcp := testmqtt.NewTCP(srv, listeners.Config{ID: "t", Address: "127.0.0.1:0"})
			if err := srv.AddListener(tcp); err != nil {
				t.Fatalf("broker: %v", err)
			}
			go func() { _ = srv.Serve() }()
			t.Cleanup(func() { _ = testmqtt.Close(srv) })

			e := startEdge(t, tcp.Address())
			l := newTCPLink(t, tcp.Address(), o.cut)
			d, err := host.New(host.Manifest{
				Group: "G",
				Nodes: []host.Node{{EdgeNode: "W6", Prefix: "W6", Devices: []host.Device{{Device: "PLC1"}}}},
				Tags: []host.Binding{
					{Name: "In", Node: "W6", Device: "PLC1", Metric: "In", Type: "Double"},
					{Name: "Out", Node: "W6", Device: "PLC1", Metric: "Out", Type: "Double", Writable: true, Init: 0.0},
				},
			}, host.Config{
				BrokerURL: "tcp://127.0.0.1:" + strconv.Itoa(l.port()),
				HostID:    "conformance",
				Primary:   true,
				// Keepalive stays at its default (30 s). A short one makes
				// the broker drop the host on a timer: mochi's read
				// deadline is keepalive + keepalive/2 in whole seconds,
				// paho pings once its last send is a keepalive old and
				// checks every keepalive/4, so 1 s gives a 1 s deadline
				// and a ping at 1-1.25 s, a drop every second. Those
				// reconnects sent held commands themselves and hid a
				// missing kick (#943). No check here needs a fast one: a
				// cut closes the connection, a freeze holds the edge's
				// DDATA in the link.
				CommandInterval: 5 * time.Millisecond,
				Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
			}, host.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("host.New: %v", err)
			}
			if o.install != nil {
				o.install(d)
			}
			ctx, cancel := context.WithCancel(context.Background())
			d.Start(ctx)
			stopOnCleanup(t, cancel, d.Stop)
			return &rig{
				drv: d, in: "In", out: "Out",
				outVals:  [2]any{float64(11), float64(22)},
				field:    e.outValue,
				sent:     e.sentValues,
				polls:    func() uint64 { return d.Status().Msgs },
				failRead: func() { e.setNull(true) },
				link:     l,
			}
		},
	})
}

// spEdge is the edge node stand-in: W6 with one device, PLC1.
type spEdge struct {
	cli     mqtt.Client
	rebirth chan struct{}

	mu   sync.Mutex
	seq  uint64
	null bool  // publish In as null
	out  any   // Out as the device holds it
	sent []any // every Out a DCMD carried
}

func startEdge(t tb, broker string) *spEdge {
	e := &spEdge{rebirth: make(chan struct{}, 1), out: float64(0)}
	e.cli = mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + broker).
		SetClientID("conformance-edge").SetCleanSession(true).SetOrderMatters(false))
	if tok := e.cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("edge connect: %v", tok.Error())
	}
	for topic, h := range map[string]mqtt.MessageHandler{
		"spBv1.0/G/NCMD/W6":      e.onNCMD,
		"spBv1.0/G/DCMD/W6/PLC1": e.onDCMD,
	} {
		if tok := e.cli.Subscribe(topic, 0, h); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
			t.Fatalf("edge subscribe %s: %v", topic, tok.Error())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.birth()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.rebirth:
				e.birth()
			case <-tick.C:
				e.data()
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done; e.cli.Disconnect(50) })
	return e
}

func (e *spEdge) onNCMD(_ mqtt.Client, m mqtt.Message) {
	p, err := sparkplug.DecodePayload(m.Payload())
	if err != nil {
		return
	}
	for _, x := range p.Metrics {
		if x.Name == "Node Control/Rebirth" && x.Value == true {
			select {
			case e.rebirth <- struct{}{}:
			default:
			}
		}
	}
}

func (e *spEdge) onDCMD(_ mqtt.Client, m mqtt.Message) {
	p, err := sparkplug.DecodePayload(m.Payload())
	if err != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, x := range p.Metrics {
		if x.Name == "Out" {
			e.out = x.Value
			e.sent = append(e.sent, x.Value)
		}
	}
}

func (e *spEdge) outValue() any { e.mu.Lock(); defer e.mu.Unlock(); return e.out }

func (e *spEdge) sentValues() []any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]any(nil), e.sent...)
}

func (e *spEdge) setNull(v bool) { e.mu.Lock(); e.null = v; e.mu.Unlock() }

// metrics is PLC1's In and Out as the device holds them. Caller holds mu.
func (e *spEdge) metrics() []sparkplug.Metric {
	in := sparkplug.Metric{Name: "In", Datatype: spb.DataType_Double, Value: 4.5}
	if e.null {
		in.Value, in.IsNull = nil, true
	}
	return []sparkplug.Metric{in, {Name: "Out", Datatype: spb.DataType_Double, Value: e.out}}
}

// birth is NBIRTH (seq 0) then DBIRTH.
func (e *spEdge) birth() {
	e.mu.Lock()
	e.seq = 0
	e.publishLocked("spBv1.0/G/NBIRTH/W6", []sparkplug.Metric{
		{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: int64(0)},
		{Name: "Node Control/Rebirth", Datatype: spb.DataType_Boolean, Value: false},
	})
	e.publishLocked("spBv1.0/G/DBIRTH/W6/PLC1", e.metrics())
	e.mu.Unlock()
}

func (e *spEdge) data() {
	e.mu.Lock()
	e.publishLocked("spBv1.0/G/DDATA/W6/PLC1", e.metrics())
	e.mu.Unlock()
}

// publishLocked sends one message with the next seq. Caller holds mu.
func (e *spEdge) publishLocked(topic string, ms []sparkplug.Metric) {
	body, err := sparkplug.Payload{Timestamp: uint64(time.Now().UnixMilli()), Seq: e.seq, Metrics: ms}.Encode()
	e.seq = (e.seq + 1) % 256
	if err != nil {
		return
	}
	e.cli.Publish(topic, 0, false, body).WaitTimeout(time.Second)
}
