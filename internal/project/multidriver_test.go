package project

// drivers: — several field drivers on one scan. The loader's contract:
// driver: and drivers: together is an error; one entry is pure sugar for
// driver:; two or more wrap in io.Multi, where a tag owned by two drivers
// refuses to load naming both; and each child keeps its own status row.

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"

	nio "github.com/joyautomation/nautilus/io"
	sphost "github.com/joyautomation/nautilus/sparkplug/host"
)

// multiBase is a manifest with the shared driver: block stripped, ready for
// a drivers: section to be appended.
func multiBase() string {
	return strings.Replace(manifest, "driver:\n  type: memory\n", "", 1)
}

// hostDriverYAML is one sparkplug-host entry for a drivers: list — the only
// current driver that builds fully offline, which is what makes multi-driver
// projects checkable in CI with no hardware anywhere.
func hostDriverYAML(indent, hostID, group, manifestFile string) string {
	return hostDriverYAMLAt(indent, "tcp://mqtt.invalid:1883", hostID, group, manifestFile)
}

// hostDriverYAMLAt is hostDriverYAML against a broker of the caller's.
func hostDriverYAMLAt(indent, broker, hostID, group, manifestFile string) string {
	i := indent
	return i + "- type: sparkplug-host\n" +
		i + "  broker: " + broker + "\n" +
		i + "  host-id: " + hostID + "\n" +
		i + "  group-id: " + group + "\n" +
		i + "  manifest: " + manifestFile + "\n"
}

func hostManifestYAML(group, node, tag string) string {
	return "group: " + group + "\nnodes:\n    - edgenode: " + node + "\ntags:\n" +
		"    - { name: " + tag + ", node: " + node + ", device: \"\", metric: M, type: Double, arraylen: 0, writable: false }\n"
}

func TestDriverAndDriversBothSetIsAnError(t *testing.T) {
	files := fsys(manifest + "drivers:\n" + hostDriverYAML("  ", "h", "G", "m.yaml"))
	files["m.yaml"] = &fstest.MapFile{Data: []byte("group: G\n")}
	_, err := Load(files, "")
	if err == nil || !strings.Contains(err.Error(), "driver: and drivers: are both set") {
		t.Fatalf("err = %v, want the both-set teaching error", err)
	}
}

func TestDriversOneEntryIsSugarForDriver(t *testing.T) {
	files := fsys(multiBase() + "drivers:\n  - type: memory\n")
	p, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Runtime.Driver.(*nio.Memory); !ok {
		t.Fatalf("driver = %T, want *io.Memory (no Multi wrapper for one entry)", p.Runtime.Driver)
	}
	if fn := p.DriverStatus(nil, nil); fn != nil {
		t.Fatalf("a lone memory driver reports no status, got %+v", fn())
	}
}

// startBroker brings up an in-process MQTT broker on a free loopback port
// and returns its URL; it is closed when the test ends. The close goes
// round mochi-mqtt/server#488 the way sparkplug/host's closeBroker does
// (see there): the listener is closed with a no-op client sweep, and
// clients are disconnected from a copy while srv.Close waits.
func startBroker(t *testing.T) string {
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
	t.Cleanup(func() {
		srv.Listeners.Close("t", func(string) {})
		done := make(chan error, 1)
		go func() { done <- srv.Close() }()
		for {
			for _, cl := range srv.Clients.GetAll() {
				if !cl.Closed() {
					_ = srv.DisconnectClient(cl, packets.ErrServerShuttingDown)
				}
			}
			select {
			case <-done:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	})
	return "tcp://" + tcp.Address()
}

func TestDriversBuildAMultiWithStatusRows(t *testing.T) {
	// central-a's broker is real (and comes up below); central-b's does
	// not exist.
	broker := startBroker(t)
	files := fsys(multiBase() + "drivers:\n" +
		hostDriverYAMLAt("  ", broker, "central-a", "GA", "a.yaml") +
		hostDriverYAML("  ", "central-b", "GB", "b.yaml"))
	files["a.yaml"] = &fstest.MapFile{Data: []byte(hostManifestYAML("GA", "W1", "W1_Level"))}
	files["b.yaml"] = &fstest.MapFile{Data: []byte(hostManifestYAML("GB", "W9", "W9_Level"))}
	p, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	multi, ok := p.Runtime.Driver.(*nio.Multi)
	if !ok {
		t.Fatalf("driver = %T, want *io.Multi", p.Runtime.Driver)
	}

	// Names default to the type, deduped.
	var names []string
	for _, c := range multi.Children() {
		names = append(names, c.Name)
	}
	if want := []string{"sparkplug-host", "sparkplug-host-2"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("child names = %v, want %v", names, want)
	}

	// The merged input set spans both children (bindings + companions).
	ins := multi.InputNames()
	for _, want := range []string{"W1_Level", "W9_Level", "W1__Online", "W9__Online"} {
		if !contains(ins, want) {
			t.Errorf("merged InputNames %v is missing %q", ins, want)
		}
	}

	// One status row per child, not a blend.
	fn := p.DriverStatus(nil, nil)
	if fn == nil {
		t.Fatal("DriverStatus must report a multi-driver set")
	}
	rows := fn()
	if len(rows) != 2 {
		t.Fatalf("status rows = %d, want one per child: %+v", len(rows), rows)
	}
	if rows[0].Kind != "sparkplug-host" || rows[1].Kind != "sparkplug-host" {
		t.Fatalf("row kinds: %q %q", rows[0].Kind, rows[1].Kind)
	}
	if rows[0].Name != "central-a" || rows[1].Name != "central-b" {
		t.Fatalf("row names: %q %q", rows[0].Name, rows[1].Name)
	}

	// And each child is its own Sparkplug device with its own health gate —
	// with neither broker connected both must read unhealthy, never nil
	// (nil would birth the device unconditionally).
	devs, err := sparkplugDevices(p.drivers)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 || devs[0].ID != "sparkplug-host" || devs[1].ID != "sparkplug-host-2" {
		t.Fatalf("devices = %+v, want one per child, named after it", devs)
	}
	for _, d := range devs {
		if d.Health == nil || d.Health() {
			t.Errorf("device %s: its broker is not connected; it must not read healthy", d.ID)
		}
	}

	// One bus up and one down: the connected child's device reads healthy
	// and the other's does not — one dead bus takes only its own device
	// offline. Both-down alone cannot tell a per-child gate from one
	// shared by every child (all-up or any-up).
	up, ok := multi.Children()[0].Driver.(*sphost.Driver)
	if !ok {
		t.Fatalf("child 0 = %T, want *host.Driver", multi.Children()[0].Driver)
	}
	up.Start(context.Background())
	defer up.Stop()
	deadline := time.Now().Add(10 * time.Second) // bounds a broken gate, not a working one
	for !devs[0].Health() {
		if time.Now().After(deadline) {
			t.Fatalf("device %s: connected to its broker (%+v) but never read healthy", devs[0].ID, up.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if devs[1].Health() {
		t.Errorf("device %s: its broker is down; it read healthy because %s's is up", devs[1].ID, devs[0].ID)
	}
	if h := driverHealth(nio.NewMemory()); h != nil {
		t.Fatal("memory has no connection to be down; its health gate must stay nil")
	}
}

func TestDriversDuplicateTagNamesBothDrivers(t *testing.T) {
	files := fsys(multiBase() + "drivers:\n" +
		hostDriverYAML("  ", "central-a", "GA", "a.yaml") +
		hostDriverYAML("  ", "central-b", "GB", "b.yaml"))
	files["a.yaml"] = &fstest.MapFile{Data: []byte(hostManifestYAML("GA", "W1", "Shared_Level"))}
	files["b.yaml"] = &fstest.MapFile{Data: []byte(hostManifestYAML("GB", "W9", "Shared_Level"))}
	_, err := Load(files, "")
	if err == nil {
		t.Fatal("one tag owned by two drivers must refuse to load")
	}
	for _, want := range []string{"Shared_Level", "sparkplug-host", "sparkplug-host-2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestDriversMemoryCannotShareAScan(t *testing.T) {
	files := fsys(multiBase() + "drivers:\n" +
		"  - type: memory\n" +
		hostDriverYAML("  ", "h", "G", "a.yaml"))
	files["a.yaml"] = &fstest.MapFile{Data: []byte(hostManifestYAML("G", "W1", "W1_Level"))}
	_, err := Load(files, "")
	if err == nil || !strings.Contains(err.Error(), "InputNames") {
		t.Fatalf("err = %v, want the unroutable-driver teaching error", err)
	}
}

func TestDriversExplicitNameCollisionIsAnError(t *testing.T) {
	files := fsys(multiBase() + "drivers:\n" +
		hostDriverYAML("  ", "ha", "GA", "a.yaml") + "    name: field\n" +
		hostDriverYAML("  ", "hb", "GB", "b.yaml") + "    name: field\n")
	files["a.yaml"] = &fstest.MapFile{Data: []byte(hostManifestYAML("GA", "W1", "W1_Level"))}
	files["b.yaml"] = &fstest.MapFile{Data: []byte(hostManifestYAML("GB", "W9", "W9_Level"))}
	_, err := Load(files, "")
	if err == nil || !strings.Contains(err.Error(), `named "field"`) {
		t.Fatalf("err = %v, want the duplicate-name error", err)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
