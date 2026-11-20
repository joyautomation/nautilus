package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"testing/fstest"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	mqttsrv "github.com/mochi-mqtt/server/v2"
	mochiauth "github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/testmqtt"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// An NCMD over a real broker, to a node wired as naut run wires it under an
// auth: block (#708 PR 8b): the node built by the project (fails closed
// until it has an identity), handed the tag API's server.Surface, then
// started. The command's metric inside auth.sparkplug's area is written,
// the one outside it is refused, and both reach the server's journal as
// sparkplug:<broker host>, source sparkplug. A node the project built for
// an auth: block but never handed its identity refuses both.
func TestSparkplugCommandUnderAuthEndToEnd(t *testing.T) {
	t.Run("wired", func(t *testing.T) { sparkplugUnderAuth(t, true) })
	t.Run("never wired", func(t *testing.T) { sparkplugUnderAuth(t, false) })
}

func sparkplugUnderAuth(t *testing.T, wired bool) {
	broker := mqttsrv.New(&mqttsrv.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := broker.AddHook(new(mochiauth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	tcp := testmqtt.NewTCP(broker, listeners.Config{ID: "plain", Address: "127.0.0.1:0"})
	if err := broker.AddListener(tcp); err != nil {
		t.Fatal(err)
	}
	go func() { _ = broker.Serve() }()
	t.Cleanup(func() { _ = testmqtt.Close(broker) })

	manifest := "name: p\ntasks:\n  - program: main.st\n    scan: 50ms\n" +
		"tags:\n  - { name: RCV_SP, role: setpoint, type: REAL, init: 1 }\n  - { name: RST_SP, role: setpoint, type: REAL, init: 1 }\n" +
		"sparkplug:\n  broker: tcp://" + tcp.Address() + "\n  group-id: G\n  edge-node: E\n  writable: [\"*\"]\n" +
		"auth:\n  areas:\n    receiving: {tags: [\"RCV_*\"]}\n  sparkplug: [{role: operator, areas: [receiving]}]\n"
	proj, err := project.Load(fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(manifest)},
		"main.st":       &fstest.MapFile{Data: []byte("PROGRAM Main\nVAR_EXTERNAL RCV_SP : REAL; RST_SP : REAL; END_VAR\nEND_PROGRAM\n")},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go rt.Run(ctx)

	ring := audit.NewRing(100)
	sopts := proj.Server
	sopts.Audit = ring
	if err := withAuth(&sopts, proj.Auth, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	srv := server.New(rt, sopts)
	node, err := proj.Sparkplug(rt)
	if err != nil {
		t.Fatal(err)
	}
	if wired {
		node.SetIdentity(sparkplugIdentity(proj, srv))
	}
	if err := node.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(node.Stop)

	host := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + tcp.Address()).SetClientID("scada"))
	if tok := host.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("host connect: %v", tok.Error())
	}
	t.Cleanup(func() { host.Disconnect(50) })
	body, err := sparkplug.Payload{Metrics: []sparkplug.Metric{
		{Name: "RCV_SP", Datatype: spb.DataType_Float, Value: 5.0},
		{Name: "RST_SP", Datatype: spb.DataType_Float, Value: 7.0},
	}}.Encode()
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	var got map[string]audit.Entry
	for time.Now().Before(deadline) {
		// The node subscribes once it is connected: publish until the
		// journal shows the command arrived.
		host.Publish("spBv1.0/G/NCMD/E", 0, false, body).WaitTimeout(time.Second)
		time.Sleep(100 * time.Millisecond)
		got = map[string]audit.Entry{}
		for _, e := range ring.Recent() {
			if e.Source == audit.SourceSparkplug {
				got[e.Target] = e
			}
		}
		if len(got) == 2 || !wired && node.Status().CommandsRefused >= 2 {
			break
		}
	}
	if !wired {
		if n := node.Status().CommandsRefused; n < 2 {
			t.Fatalf("CommandsRefused = %d: the command never arrived", n)
		}
		if a, b := rt.Tags().Real("RCV_SP"), rt.Tags().Real("RST_SP"); a != 1 || b != 1 {
			t.Errorf("RCV_SP, RST_SP = %v, %v: written with no identity under auth:", a, b)
		}
		return
	}
	ok, refused := got["RCV_SP"], got["RST_SP"]
	if ok.Result != audit.ResultOK || ok.User != "sparkplug:127.0.0.1" || ok.Action != "tag.write" || ok.From != tcp.Address() {
		t.Errorf("RCV_SP entry = %+v", ok)
	}
	if refused.Result != audit.ResultRefused || refused.User != "sparkplug:127.0.0.1" {
		t.Errorf("RST_SP entry = %+v", refused)
	}
	if v := rt.Tags().Real("RCV_SP"); v != 5 {
		t.Errorf("RCV_SP = %v, want 5", v)
	}
	if v := rt.Tags().Real("RST_SP"); v != 1 {
		t.Errorf("RST_SP = %v, want 1: outside auth.sparkplug's area", v)
	}
}
