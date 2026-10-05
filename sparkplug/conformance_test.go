package sparkplug_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// tckVersion is the sparkplug-tck-go release the conformance test runs
// against. Bump alongside any protocol changes.
const tckVersion = "v0.1.2"

// TestTCKConformance drives an in-process edge node through its full lifecycle
// against the real Sparkplug TCK edge-node profile and asserts zero failures.
//
// It is gated on NAUTILUS_TCK=1 because it fetches and runs the external TCK
// harness (needs the module proxy) — normal `go test ./...` skips it. CI sets
// the flag. The TCK embeds its own MQTT broker, so no external broker is
// needed.
//
// The rebirth stimulus is the test's, not the harness's timer. The TCK scores
// rebirth-action-1 ("no DATA between the Rebirth NCMD and the NBIRTH") by the
// broker's receive times, so an NDATA the node sent a moment BEFORE the
// command reached it, still in flight when the broker logged the command,
// counts as a violation the node had no way to avoid. With the harness's
// `-rebirth-after` timer that was a coin toss against the publish tick and
// failed CI now and then. Instead the test quiesces the data (no heartbeat,
// no value changes), waits until the broker has delivered the last NDATA
// back to it — so the broker has logged it — and only then sends the
// NCMD/Rebirth itself. Nothing can be in flight, so the TCK sees exactly
// what the node did. The node-side half of the rule — once the command is
// accepted no DATA goes out before the NBIRTH — is pinned exactly, at the
// client seam, by rebirth_test.go.
func TestTCKConformance(t *testing.T) {
	if os.Getenv("NAUTILUS_TCK") != "1" {
		t.Skip("set NAUTILUS_TCK=1 to run the Sparkplug TCK conformance test")
	}
	const (
		group  = "TestGroup"
		node   = "TestNode"
		listen = "127.0.0.1:18899"
		broker = "tcp://" + listen
	)
	out := filepath.Join(t.TempDir(), "tck.json")

	// Start the TCK harness (embedded broker + edge-node profile).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	harness := exec.CommandContext(ctx, "go", "run",
		"github.com/joyautomation/sparkplug-tck-go/cmd/sparkplug-tck@"+tckVersion,
		"-harness", "-profile", "edge-node", "-listen", listen,
		"-duration", "24s", "-json")
	harness.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	outFile, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer outFile.Close()
	harness.Stdout = outFile
	harness.Stderr = testWriter{t}
	if err := harness.Start(); err != nil {
		t.Fatalf("start TCK harness: %v", err)
	}
	defer func() { _ = harness.Wait() }()

	// Give the harness a moment to bind its broker.
	if !waitForListen(listen, 15*time.Second) {
		t.Fatal("TCK harness broker never came up")
	}

	// The driver: a plain client on the harness broker that watches the
	// node's messages and sends the rebirth. It stays connected until the
	// harness has scored (a client that disconnects without an NDEATH
	// would be scored as an edge node that did just that).
	w := newWatcher()
	drv := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(broker).
		SetClientID("tck-conformance-driver").SetCleanSession(true).SetAutoReconnect(false))
	if tok := drv.Connect(); !tok.WaitTimeout(10*time.Second) || tok.Error() != nil {
		t.Fatalf("driver connect: %v", tok.Error())
	}
	if tok := drv.Subscribe("spBv1.0/"+group+"/+/"+node, 0, w.handle); !tok.WaitTimeout(10*time.Second) || tok.Error() != nil {
		t.Fatalf("driver subscribe: %v", tok.Error())
	}

	// Run our node against the harness broker.
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM T\nVAR_EXTERNAL\n  Speed : REAL;\n  Enable : BOOL;\nEND_VAR\nEND_PROGRAM",
		Driver:  nio.NewMemory(),
		Scan:    100 * time.Millisecond,
		Seed:    nio.Values{"Speed": 10.0, "Enable": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	rtCtx, rtCancel := context.WithCancel(context.Background())
	go rt.Run(rtCtx)

	// Report by exception with no heartbeat: DATA flows only when the test
	// changes a value, which is what lets it quiesce the node below.
	n, err := sparkplug.New(rt, sparkplug.Config{
		BrokerURL: broker, GroupID: group, EdgeNode: node,
		PublishInterval: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := n.Start(rtCtx); err != nil {
		t.Fatalf("node start: %v", err)
	}
	w.waitFor(t, "the first NBIRTH", func() bool { return w.nbirths >= 1 })

	// Change a value so NDATA flows.
	speed := 10.0
	churn := func(until time.Duration) {
		tk := time.NewTicker(300 * time.Millisecond)
		defer tk.Stop()
		for time.Since(start) < until {
			<-tk.C
			speed++
			rt.Tags().SetReal("Speed", speed)
		}
	}
	churn(8 * time.Second)

	// Quiesce, then rebirth: once the broker has handed back the NDATA
	// carrying the last value written, it has logged every DATA the node
	// will send until something changes again.
	w.waitFor(t, "the NDATA carrying the last Speed", func() bool { return w.lastSpeed == speed })
	births := w.nbirths
	if err := publishRebirth(drv, group, node); err != nil {
		t.Fatalf("publish NCMD/Rebirth: %v", err)
	}
	w.waitFor(t, "the NBIRTH answering the rebirth", func() bool { return w.nbirths > births })

	// Data flows again after the rebirth; then shut down gracefully (NDEATH
	// + clean disconnect) while the harness still watches.
	churn(16 * time.Second)
	n.Stop()
	rtCancel()

	if err := harness.Wait(); err != nil {
		// A non-zero exit means the profile found failures; we still parse to
		// report which.
		t.Logf("TCK harness exit: %v", err)
	}
	drv.Disconnect(0)
	assertNoFailures(t, out, "tck-id-operational-behavior-data-commands-rebirth-action-1")
}

// publishRebirth sends the NCMD the harness's own -rebirth stimulus sends:
// "Node Control/Rebirth" = true, QoS 1.
func publishRebirth(c mqtt.Client, group, node string) error {
	p, err := sparkplug.Payload{Timestamp: uint64(time.Now().UnixMilli()), Metrics: []sparkplug.Metric{{
		Name: "Node Control/Rebirth", Datatype: spb.DataType_Boolean, Value: true,
	}}}.Encode()
	if err != nil {
		return err
	}
	tok := c.Publish("spBv1.0/"+group+"/NCMD/"+node, 1, false, p)
	if !tok.WaitTimeout(10 * time.Second) {
		return errors.New("timed out")
	}
	return tok.Error()
}

// watcher tracks what the broker delivers of the node's messages: how many
// NBIRTHs, and the last Speed an NDATA carried.
type watcher struct {
	mu        sync.Mutex
	nbirths   int
	lastSpeed float64
}

func newWatcher() *watcher { return &watcher{} }

func (w *watcher) handle(_ mqtt.Client, msg mqtt.Message) {
	kind := strings.Split(msg.Topic(), "/")[2]
	if kind != "NBIRTH" && kind != "NDATA" {
		return
	}
	p, err := sparkplug.DecodePayload(msg.Payload())
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if kind == "NBIRTH" {
		w.nbirths++
	}
	for _, m := range p.Metrics {
		if f, ok := m.Value.(float64); ok && m.Name == "Speed" {
			w.lastSpeed = f
		}
	}
}

// waitFor polls cond (under w.mu) until it holds, failing after 10s.
func (w *watcher) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		w.mu.Lock()
		ok := cond()
		w.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// assertNoFailures fails on any TCK failure, and on any of mustPass that the
// harness did not score as a pass — so a scenario the test means to exercise
// cannot quietly turn into "not applicable".
func assertNoFailures(t *testing.T, path string, mustPass ...string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read TCK results: %v", err)
	}
	// The harness emits runner.Result, whose id field is "assertion_id" —
	// `json:"id"` silently decoded to "" and every reported failure came out
	// nameless.
	var items []struct {
		ID     string `json:"assertion_id"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(b, &items); err != nil {
		t.Fatalf("parse TCK results: %v\n%s", err, string(b))
	}
	pass, na, fail := 0, 0, 0
	passed := map[string]bool{}
	for _, it := range items {
		switch strings.ToLower(it.Status) {
		case "pass":
			pass++
			passed[it.ID] = true
		case "fail":
			fail++
			t.Errorf("TCK FAIL %s: %s", it.ID, it.Detail)
		default:
			na++
		}
	}
	t.Logf("TCK edge-node: %d pass, %d n/a, %d fail", pass, na, fail)
	if fail > 0 {
		t.Fatalf("%d TCK assertion(s) failed", fail)
	}
	if pass == 0 {
		t.Fatal("no assertions passed — did the node connect?")
	}
	for _, id := range mustPass {
		if !passed[id] {
			t.Errorf("TCK did not score %s as a pass", id)
		}
	}
}

func waitForListen(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// testWriter pipes subprocess stderr into the test log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			w.t.Log("tck: " + line)
		}
	}
	return len(p), nil
}
