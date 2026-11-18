package project

// Manifest-level claims of the Sparkplug guides, proved end to end against an
// in-process broker: the password comes from NAUTILUS_MQTT_PASSWORD and never
// from the file, edge-node defaults to the project name, only broker and
// group-id are required, and a sparkplug-host driver publishes STATE unless it
// says primary: false.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"

	"github.com/joyautomation/nautilus/internal/testmqtt"
	"github.com/joyautomation/nautilus/runtime"
	sphost "github.com/joyautomation/nautilus/sparkplug/host"
)

// authSpy records the username and password every connecting client presents,
// and lets them all in: the broker's view of what the node actually sent.
type authSpy struct {
	mqttsrv.HookBase
	creds chan [2]string
}

func (*authSpy) ID() string { return "auth-spy" }
func (*authSpy) Provides(b byte) bool {
	return b == mqttsrv.OnConnectAuthenticate
}
func (a *authSpy) OnConnectAuthenticate(_ *mqttsrv.Client, pk packets.Packet) bool {
	select {
	case a.creds <- [2]string{string(pk.Connect.Username), string(pk.Connect.Password)}:
	default:
	}
	return true
}

// testBroker starts an in-process broker (with an authSpy when given one) and
// returns its tcp:// URL.
func testBroker(t *testing.T, spy *authSpy) string {
	t.Helper()
	srv := mqttsrv.New(&mqttsrv.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if spy != nil {
		if err := srv.AddHook(spy, nil); err != nil {
			t.Fatal(err)
		}
	} else if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
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

func nextCreds(t *testing.T, spy *authSpy) [2]string {
	t.Helper()
	select {
	case c := <-spy.creds:
		return c
	case <-time.After(8 * time.Second):
		t.Fatal("the broker never saw a CONNECT")
		return [2]string{}
	}
}

func edgeNodeFor(t *testing.T, section string) (*Project, *runtime.Runtime) {
	t.Helper()
	p, err := Load(fsys(manifest+"\nsparkplug:\n"+section), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	return p, rt
}

// SPB-007: the edge node's password is whatever NAUTILUS_MQTT_PASSWORD holds
// when the node is built, and a `password:` key in the manifest is refused.
func TestSparkplugPasswordComesFromTheEnvironment(t *testing.T) {
	for _, pw := range []string{"env-secret-one", "env-secret-two"} {
		t.Run(pw, func(t *testing.T) {
			spy := &authSpy{creds: make(chan [2]string, 4)}
			url := testBroker(t, spy)
			t.Setenv("NAUTILUS_MQTT_PASSWORD", pw)
			p, rt := edgeNodeFor(t, "  broker: \""+url+"\"\n  group-id: Plant\n  username: edge1\n")
			node, err := p.Sparkplug(rt)
			if err != nil || node == nil {
				t.Fatalf("node: %v, %v", node, err)
			}
			if err := node.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(node.Stop)
			if got := nextCreds(t, spy); got != [2]string{"edge1", pw} {
				t.Fatalf("the broker saw username/password %q, want [edge1 %s]", got, pw)
			}
		})
	}

	// The file cannot carry one: the key is not part of the schema.
	_, err := Load(fsys(manifest+"\nsparkplug:\n  broker: tcp://x:1883\n  group-id: Plant\n  password: hunter2\n"), "")
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("a password: key in the sparkplug section must be rejected, got %v", err)
	}
}

// SPH-003: the same rule for the sparkplug-host driver.
func TestSparkplugHostPasswordComesFromTheEnvironment(t *testing.T) {
	for _, pw := range []string{"env-secret-one", "env-secret-two"} {
		t.Run(pw, func(t *testing.T) {
			spy := &authSpy{creds: make(chan [2]string, 4)}
			url := testBroker(t, spy)
			t.Setenv("NAUTILUS_MQTT_PASSWORD", pw)
			drv := loadHostDriver(t, url, "  username: central\n")
			drv.Start(context.Background())
			t.Cleanup(drv.Stop)
			if got := nextCreds(t, spy); got != [2]string{"central", pw} {
				t.Fatalf("the broker saw username/password %q, want [central %s]", got, pw)
			}
		})
	}

	_, err := Load(hostDriverFiles("tcp://x:1883", "  password: hunter2\n"), "")
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("a password: key on a sparkplug-host driver must be rejected, got %v", err)
	}
}

func hostDriverFiles(url, extra string) fstest.MapFS {
	return hostFilesWith(url, extra, `    - { name: Edge1_ZONE1_FIT_001, node: Edge1, device: "", metric: Zone1/FIT_001, type: Double, arraylen: 0, writable: false }
`)
}

// hostFilesWith is hostDriverFiles with the sparkplug manifest's tag lines
// supplied by the caller.
func hostFilesWith(url, extra, tagLines string) fstest.MapFS {
	files := fsys(strings.Replace(manifest, "driver:\n  type: memory\n", "", 1) + `
driver:
  type: sparkplug-host
  broker: "` + url + `"
  group-id: Plant
  host-id: central
  manifest: sparkplug_manifest.yaml
` + extra)
	files["sparkplug_manifest.yaml"] = &fstest.MapFile{Data: []byte(`group: Plant
nodes:
    - edgenode: Edge1
tags:
` + tagLines)}
	return files
}

func loadHostDriver(t *testing.T, url, extra string) *sphost.Driver {
	t.Helper()
	p, err := Load(hostDriverFiles(url, extra), "")
	if err != nil {
		t.Fatal(err)
	}
	drv, ok := p.Runtime.Driver.(*sphost.Driver)
	if !ok {
		t.Fatalf("driver = %T, want *host.Driver", p.Runtime.Driver)
	}
	return drv
}

// SPB-009: edge-node defaults to the project name, and an explicit one wins.
func TestSparkplugEdgeNodeDefaultsToTheProjectName(t *testing.T) {
	p, rt := edgeNodeFor(t, "  broker: tcp://x:1883\n  group-id: Plant\n")
	node, err := p.Sparkplug(rt)
	if err != nil || node == nil {
		t.Fatalf("node: %v, %v", node, err)
	}
	if got := node.Status().EdgeNode; got != "test-plant" {
		t.Errorf("edge node = %q, want the project name test-plant", got)
	}
	p, rt = edgeNodeFor(t, "  broker: tcp://x:1883\n  group-id: Plant\n  edge-node: Edge1\n")
	node, err = p.Sparkplug(rt)
	if err != nil || node == nil {
		t.Fatalf("node: %v, %v", node, err)
	}
	if got := node.Status().EdgeNode; got != "Edge1" {
		t.Errorf("edge node = %q, want the explicit Edge1", got)
	}
}

// SPB-006: broker and group-id are all a section needs, and each is required.
func TestSparkplugOnlyBrokerAndGroupIDAreRequired(t *testing.T) {
	p, rt := edgeNodeFor(t, "  broker: tcp://x:1883\n  group-id: Plant\n")
	if node, err := p.Sparkplug(rt); err != nil || node == nil {
		t.Fatalf("a section with only broker and group-id must build a node: %v, %v", node, err)
	}
	p, rt = edgeNodeFor(t, "  group-id: Plant\n")
	if _, err := p.Sparkplug(rt); err == nil || !strings.Contains(err.Error(), "broker") {
		t.Fatalf("a section without broker must error, got %v", err)
	}
	p, rt = edgeNodeFor(t, "  broker: tcp://x:1883\n")
	if _, err := p.Sparkplug(rt); err == nil || !strings.Contains(err.Error(), "group-id") {
		t.Fatalf("a section without group-id must error, got %v", err)
	}
}

// SPH-004: a sparkplug-host project that does not mention `primary:` is the
// primary host, so STATE goes out online on spBv1.0/STATE/<host-id>.
func TestSparkplugHostPrimaryDefaultsToTrue(t *testing.T) {
	url := testBroker(t, nil)
	var mu sync.Mutex
	var states []bool
	obs := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(url).SetClientID("state-obs").SetCleanSession(true))
	if tok := obs.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("observer connect: %v", tok.Error())
	}
	t.Cleanup(func() { obs.Disconnect(50) })
	got := make(chan struct{}, 1)
	if tok := obs.Subscribe("spBv1.0/STATE/central", 1, func(_ mqtt.Client, m mqtt.Message) {
		var s struct {
			Online bool `json:"online"`
		}
		if json.Unmarshal(m.Payload(), &s) == nil {
			mu.Lock()
			states = append(states, s.Online)
			mu.Unlock()
			select {
			case got <- struct{}{}:
			default:
			}
		}
	}); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("observer subscribe: %v", tok.Error())
	}

	drv := loadHostDriver(t, url, "") // no primary: key
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	select {
	case <-got:
	case <-time.After(8 * time.Second):
		t.Fatal("a host project with no primary: key published no STATE")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(states) == 0 || !states[0] {
		t.Fatalf("first STATE online = %v, want true", states)
	}
}
