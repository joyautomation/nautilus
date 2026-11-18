package main

// `naut sparkplug import --broker` and `naut sparkplug browse` against an
// in-process broker with a simulated edge node. Births are never retained, so
// the edge only births when asked: it chatters NDATA (so the import has
// something to hear from) until an NCMD Node Control/Rebirth arrives, then
// sends its NBIRTH and goes quiet.

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/joyautomation/nautilus/internal/testmqtt"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

func spBroker(t *testing.T) string {
	t.Helper()
	srv := mqttsrv.New(&mqttsrv.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "t", Address: "127.0.0.1:0"})
	if err := srv.AddListener(tcp); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = testmqtt.Close(srv) }) // not srv.Close: it can deadlock in mochi v2.7.9
	return "tcp://" + tcp.Address()
}

// simulatedEdge is Plant/<node>: it publishes NDATA until it is asked to
// rebirth, then publishes its NBIRTH once. asked closes when the request lands.
func simulatedEdge(t *testing.T, url, node string) (asked <-chan struct{}) {
	t.Helper()
	ch := make(chan struct{})
	var once sync.Once
	stop := make(chan struct{})
	var cli mqtt.Client
	opts := mqtt.NewClientOptions().AddBroker(url).SetClientID(strings.ToLower(node) + "-sim").SetCleanSession(true).SetOrderMatters(true)
	cli = mqtt.NewClient(opts)
	if tok := cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("edge connect: %v", tok.Error())
	}
	publish := func(topic string, p sparkplug.Payload) {
		b, err := p.Encode()
		if err != nil {
			t.Error(err)
			return
		}
		cli.Publish(topic, 0, false, b)
	}
	level := sparkplug.Metric{Name: "Zone1/FIT_001", Datatype: spb.DataType_Double, Value: 4.5}
	tok := cli.Subscribe("spBv1.0/Plant/NCMD/"+node, 1, func(_ mqtt.Client, m mqtt.Message) {
		p, err := sparkplug.DecodePayload(m.Payload())
		if err != nil {
			return
		}
		for _, mt := range p.Metrics {
			if mt.Name == "Node Control/Rebirth" && mt.Value == true {
				once.Do(func() {
					close(stop)
					publish("spBv1.0/Plant/NBIRTH/"+node, sparkplug.Payload{
						Timestamp: uint64(time.Now().UnixMilli()),
						Metrics: []sparkplug.Metric{
							{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: int64(1)},
							{Name: "Node Control/Rebirth", Datatype: spb.DataType_Boolean, Value: false},
							level,
						},
					})
					close(ch)
				})
			}
		}
	})
	if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("edge subscribe: %v", tok.Error())
	}
	go func() {
		seq := uint64(0)
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				seq = (seq + 1) % 256
				publish("spBv1.0/Plant/NDATA/"+node, sparkplug.Payload{
					Timestamp: uint64(time.Now().UnixMilli()), Seq: seq,
					Metrics: []sparkplug.Metric{level},
				})
			}
		}
	}()
	t.Cleanup(func() {
		once.Do(func() { close(stop) })
		cli.Disconnect(50)
	})
	return ch
}

// SPH-008: a live import asks every node it heard from to rebirth (the birth
// is not retained, so it would otherwise never arrive) and generates from
// what birthed. Two nodes, so asking only the first is caught.
func TestSparkplugImportFromBrokerAsksForRebirth(t *testing.T) {
	url := spBroker(t)
	nodes := []string{"Edge1", "Edge2"}
	asked := map[string]<-chan struct{}{}
	for _, n := range nodes {
		asked[n] = simulatedEdge(t, url, n)
	}
	dir := t.TempDir()
	code := 0
	captureStdout(t, func() int {
		code = runSparkplug([]string{"import", "--broker", url, "--group", "Plant", "--listen", "20s", "--out", dir})
		return code
	})
	if code != 0 {
		t.Fatalf("import failed (%d)", code)
	}
	for _, n := range nodes {
		select {
		case <-asked[n]:
		default:
			t.Errorf("the import never asked %s to rebirth", n)
		}
	}
	manifest := read(t, filepath.Join(dir, "sparkplug_manifest.yaml"))
	tags := read(t, filepath.Join(dir, "tags", "sparkplug.yaml"))
	for _, want := range []string{"group: Plant", "metric: Zone1/FIT_001"} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest generated from the births is missing %q:\n%s", want, manifest)
		}
	}
	for _, n := range nodes {
		if !strings.Contains(manifest, "edgenode: "+n) {
			t.Errorf("manifest generated from the births is missing node %s:\n%s", n, manifest)
		}
		if !strings.Contains(tags, n+"_Zone1_FIT_001") {
			t.Errorf("tag file missing %s's birthed metric:\n%s", n, tags)
		}
	}
}

// SPH-012: browse prints what is on the wire and generates nothing.
func TestSparkplugBrowsePrintsWithoutGenerating(t *testing.T) {
	url := spBroker(t)
	asked := simulatedEdge(t, url, "Edge1")
	dir := t.TempDir()
	t.Chdir(dir)
	code := 0
	out := captureStdout(t, func() int {
		code = runSparkplug([]string{"browse", "--broker", url, "--group", "Plant", "--listen", "20s"})
		return code
	})
	if code != 0 {
		t.Fatalf("browse failed (%d)", code)
	}
	select {
	case <-asked:
	default:
		t.Fatal("browse never asked the node to rebirth")
	}
	for _, want := range []string{"NBIRTH Plant/Edge1", "Zone1/FIT_001", "Double"} {
		if !strings.Contains(out, want) {
			t.Errorf("browse output is missing %q:\n%s", want, out)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("browse wrote %d entries into the working directory, want nothing generated", len(entries))
	}
}
