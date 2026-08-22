// conformance_test.go runs the Sparkplug TCK's host-application profile
// against the real host.Driver — the host-side twin of
// sparkplug/conformance_test.go (docs/design/sparkplug-host.md §2, §6.3).
//
// Two facts shape the design, both called out in the brief:
//
//   - sparkplug-tck-go's `internal/harness` cannot be imported, so the
//     harness runs as a subprocess and we parse its `-json` output. Unlike
//     the edge test, which shells out with `go run pkg@version` and waits out
//     a fixed `-duration`, we build the tool once and run the binary
//     directly. That lets us SIGINT it the moment the scripted traffic is
//     done: the CLI's signal.NotifyContext evaluates the profile early, so
//     the test finishes in seconds instead of idling out a duration long
//     enough to be safe on a slow runner.
//
//   - the harness does NOT simulate an edge node (its -rebirth stimulus
//     targets an edge SUT), so with no traffic HostMessageOrdering,
//     HostN/DDEATHActions and HostN/DCMDCompliant all go N/A and the run
//     still exits 0. This test therefore drives its own raw-paho edge into
//     the harness broker — NBIRTH seq=0, DBIRTH, NDATA/DDATA, a deliberate
//     seq gap that must provoke an NCMD Rebirth, the edge's re-birth in
//     response, DDEATH, NDEATH — and asserts a NON-ZERO pass count so an
//     all-N/A run can never masquerade as conformance.
//
// Gated on NAUTILUS_TCK=1 like the edge test; CI sets it. Set
// NAUTILUS_TCK_LOCAL=<path to a sparkplug-tck-go checkout> to grade against
// a working tree instead of the pinned tag.
//
// One of the profile's 85 ids is unreachable and is therefore absent rather
// than N/A: tck-id-operational-behavior-host-reordering-success, the
// "missing message arrived, so the host cancelled its reorder timer" path.
// The harness's findSeqGaps is a strictly-ascending expectation counter with
// no reorder buffer of its own, so the out-of-order message that FILLS a gap
// registers as a second, phantom gap — which then has neither a recovery nor
// a rebirth and grades FAIL. Scoring -success would mean ending the run on a
// phantom gap (FAIL) or parking for the harness's 30 s reordering window
// before the real rebirth. The -rebirth path this scenario does take is the
// stronger check anyway: it proves the reorder timer fires and produces a
// conformant NCMD. Revisit if the harness grows a reorder-aware tracker.
package host_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/sparkplug"
	sphost "github.com/joyautomation/nautilus/sparkplug/host"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// tckHostVersion is the sparkplug-tck-go release the host profile is graded
// against. It is the same tag sparkplug/conformance_test.go pins, and it is
// the first tag that carries HostApplicationProfile — verified with
// `git show v0.1.2 -- internal/harness/scenarios_host_birth.go`.
const tckHostVersion = "v0.1.2"

const (
	tckGroup  = "G"
	tckEdge   = "E1"
	tckDevice = "D1"
	tckHostID = "nautilus-host-tck"

	// tckReorder is the host's ReorderTimeout. Short so the seq-gap rebirth
	// lands well inside the harness's 30 s reordering window.
	tckReorder = 1500 * time.Millisecond
)

func TestTCKHostConformance(t *testing.T) {
	if os.Getenv("NAUTILUS_TCK") != "1" {
		t.Skip("set NAUTILUS_TCK=1 to run the Sparkplug TCK host-application conformance test")
	}

	bin := buildTCKTool(t)
	addr := reserveAddr(t)
	resultsPath := filepath.Join(t.TempDir(), "tck-host.json")

	// ── the harness: in-process broker + host-application profile ─────────
	//
	// -duration is a backstop only; the scripted traffic below finishes in a
	// few seconds and we SIGINT to evaluate immediately.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	harness := exec.CommandContext(ctx, bin,
		"-harness", "-profile", "host-application", "-listen", addr,
		"-duration", "120s", "-json")
	results, err := os.Create(resultsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer results.Close()
	harness.Stdout = results
	harness.Stderr = tckLog{t}
	if err := harness.Start(); err != nil {
		t.Fatalf("start TCK harness: %v", err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = harness.Process.Kill()
			_ = harness.Wait()
		}
	}()
	if !waitForListen(addr, 20*time.Second) {
		t.Fatal("TCK harness broker never came up")
	}

	// ── the SUT: our host driver ─────────────────────────────────────────
	d, err := sphost.New(tckManifest(), sphost.Config{
		BrokerURL:       "tcp://" + addr,
		HostID:          tckHostID,
		GroupIDs:        []string{tckGroup},
		Primary:         true,
		ReorderTimeout:  tckReorder,
		CommandInterval: 50 * time.Millisecond,
		Log:             tckLogger(),
	})
	if err != nil {
		t.Fatalf("host.New: %v", err)
	}
	d.Start(context.Background())
	stopped := false
	defer func() {
		if !stopped {
			d.Stop()
		}
	}()

	// Wait for CONNECT → SUBSCRIBE(literal STATE) → STATE birth.
	if !waitFor(10*time.Second, func() bool { return d.Status().Connected }) {
		t.Fatal("host driver never connected to the harness broker")
	}

	// ── the stimulus: a scripted edge node ───────────────────────────────
	e := newTCKEdge(t, addr)
	// RebirthOnStart fires an NCMD Rebirth at connect, before this client
	// existed. Drop anything already queued so the gap-triggered rebirth
	// below is unambiguous.
	e.drainRebirths()

	e.nbirth() // seq 0
	e.dbirth() // seq 1
	e.ndata()  // seq 2
	e.ddata()  // seq 3
	if !waitFor(5*time.Second, func() bool { return tckNodeOnline(d) }) {
		t.Fatal("host never marked the edge node online")
	}

	// An operator write on the writable binding → DCMD (HostDCMDCompliant).
	// The runtime's first output snapshot is the BASELINE — every output at
	// its init:, describing the world rather than commanding it — and only
	// what moves after it is a command. See the host driver's WriteOutputs.
	if err := d.WriteOutputs(tckBaseline()); err != nil {
		t.Fatalf("baseline WriteOutputs: %v", err)
	}
	if err := d.WriteOutputs(nio.Values{"E1_D1_Pump_SpeedSP": 42.5}); err != nil {
		t.Fatalf("WriteOutputs: %v", err)
	}

	// A deliberate gap: seq 4 and 5 never arrive. The host must arm its
	// reorder timer and, when it expires, publish NCMD Rebirth
	// (HostMessageOrdering: -param, -start, -rebirth).
	e.gapData(6)
	if !e.waitRebirth(tckReorder + 8*time.Second) {
		t.Fatal("host published no NCMD Rebirth after a sequence gap")
	}
	// A conformant edge answers a Rebirth with a fresh NBIRTH at seq 0.
	e.nbirth()
	e.dbirth()

	// Deaths — the harness scores HostD/NDEATHActions on observing them.
	e.ddeath() // seq 2
	if !waitFor(5*time.Second, func() bool { return !tckDeviceOnline(d) }) {
		t.Fatal("host never marked the device offline after DDEATH")
	}
	e.ndeath()
	if !waitFor(5*time.Second, func() bool { return !tckNodeOnline(d) }) {
		t.Fatal("host never marked the edge node offline after NDEATH")
	}

	// ── teardown: STATE death, then a clean DISCONNECT ───────────────────
	d.Stop()
	stopped = true
	e.close()

	// Let the broker's disconnect hook land, then evaluate.
	time.Sleep(300 * time.Millisecond)
	if err := harness.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal TCK harness: %v", err)
	}
	waited = true
	if err := harness.Wait(); err != nil {
		// Non-zero exit means the profile found failures; we still parse to
		// report exactly which.
		t.Logf("TCK harness exit: %v", err)
	}
	_ = results.Close()

	assertHostProfile(t, resultsPath)
}

// ── result parsing + reporting ───────────────────────────────────────────

type tckResult struct {
	AssertionID string `json:"assertion_id"`
	Subject     string `json:"subject"`
	Status      string `json:"status"`
	Detail      string `json:"detail"`
}

// assertHostProfile aggregates the harness verdicts per assertion id (a
// scenario emits one Result per subject, so an id can appear many times),
// prints the table, and enforces the two gates the brief calls for: zero
// FAIL, and a non-zero PASS count so an all-N/A run cannot pass as
// conformance.
func assertHostProfile(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read TCK results: %v", err)
	}
	var items []tckResult
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("parse TCK results: %v\n%s", err, string(raw))
	}
	if len(items) == 0 {
		t.Fatal("TCK harness emitted no results")
	}

	type agg struct {
		pass, fail, na int
		detail         string // first failure detail, else first n/a reason
	}
	byID := map[string]*agg{}
	order := []string{}
	for _, it := range items {
		a := byID[it.AssertionID]
		if a == nil {
			a = &agg{}
			byID[it.AssertionID] = a
			order = append(order, it.AssertionID)
		}
		switch strings.ToLower(it.Status) {
		case "pass":
			a.pass++
		case "fail":
			a.fail++
			if a.detail == "" || a.fail == 1 {
				a.detail = it.Detail
			}
		default:
			a.na++
			if a.detail == "" {
				a.detail = it.Detail
			}
		}
	}
	sort.Strings(order)

	// An id counts as FAIL if any subject failed, PASS if any subject passed
	// and none failed, N/A otherwise.
	var passIDs, failIDs, naIDs []string
	for _, id := range order {
		a := byID[id]
		switch {
		case a.fail > 0:
			failIDs = append(failIDs, id)
		case a.pass > 0:
			passIDs = append(passIDs, id)
		default:
			naIDs = append(naIDs, id)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\nSparkplug TCK — host-application profile (%s)\n", tckHostVersion)
	fmt.Fprintf(&b, "%-6s  %-78s  %s\n", "STATUS", "ASSERTION ID", "NOTE")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 100))
	for _, id := range order {
		a := byID[id]
		status := "n/a"
		switch {
		case a.fail > 0:
			status = "FAIL"
		case a.pass > 0:
			status = "pass"
		}
		note := a.detail
		if status == "pass" {
			note = fmt.Sprintf("%d subject(s)", a.pass)
		}
		if len(note) > 60 {
			note = note[:57] + "..."
		}
		fmt.Fprintf(&b, "%-6s  %-78s  %s\n", status, id, note)
	}
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 100))
	fmt.Fprintf(&b, "%d ids: %d pass, %d fail, %d n/a (%d raw results)\n",
		len(order), len(passIDs), len(failIDs), len(naIDs), len(items))
	if len(naIDs) > 0 {
		fmt.Fprintf(&b, "\nN/A (not exercised by this scenario):\n")
		for _, id := range naIDs {
			fmt.Fprintf(&b, "  %s — %s\n", id, byID[id].detail)
		}
	}
	t.Log(b.String())

	for _, id := range failIDs {
		t.Errorf("TCK FAIL %s: %s", id, byID[id].detail)
	}
	if len(failIDs) > 0 {
		t.Fatalf("%d host-application assertion(s) failed", len(failIDs))
	}
	if len(passIDs) == 0 {
		t.Fatal("no host-application assertions passed — did the driver connect and publish STATE?")
	}
}

// ── the scripted edge node ───────────────────────────────────────────────

// tckEdgeNode is a raw paho client standing in for an edge node: everything
// the harness needs to see on the wire so the host's reactions can be graded.
// It is deliberately NOT sparkplug.Node — the scenario needs a deliberate seq
// gap, which a conformant node will never emit.
type tckEdgeNode struct {
	t     *testing.T
	cli   mqtt.Client
	seq   uint64
	bdSeq int64

	mu       sync.Mutex
	rebirths chan struct{}
}

func newTCKEdge(t *testing.T, addr string) *tckEdgeNode {
	t.Helper()
	e := &tckEdgeNode{t: t, rebirths: make(chan struct{}, 16)}

	willBody, err := sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		OmitSeq:   true,
		Metrics:   []sparkplug.Metric{{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: int64(0)}},
	}.Encode()
	if err != nil {
		t.Fatalf("encode edge will: %v", err)
	}

	opts := mqtt.NewClientOptions().
		AddBroker("tcp://"+addr).
		SetClientID("tck-edge-"+tckEdge).
		SetCleanSession(true).
		SetOrderMatters(true).
		SetBinaryWill(topicN("NDEATH"), willBody, 1, false).
		SetDefaultPublishHandler(func(_ mqtt.Client, m mqtt.Message) {
			if !isRebirthCommand(m.Topic(), m.Payload()) {
				return
			}
			select {
			case e.rebirths <- struct{}{}:
			default:
			}
		})
	e.cli = mqtt.NewClient(opts)
	if tok := e.cli.Connect(); !tok.WaitTimeout(10*time.Second) || tok.Error() != nil {
		t.Fatalf("edge connect: %v", tok.Error())
	}
	for _, f := range []string{topicN("NCMD"), topicD("DCMD")} {
		if tok := e.cli.Subscribe(f, 1, nil); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
			t.Fatalf("edge subscribe %s: %v", f, tok.Error())
		}
	}
	return e
}

func (e *tckEdgeNode) close() { e.cli.Disconnect(200) }

func (e *tckEdgeNode) publish(topic string, p sparkplug.Payload) {
	e.t.Helper()
	body, err := p.Encode()
	if err != nil {
		e.t.Fatalf("encode %s: %v", topic, err)
	}
	// QoS 1 so the harness broker and the host both see every message: this
	// scenario's verdicts hinge on the exact seq stream, and a dropped
	// publish would read as a defect in the SUT.
	if tok := e.cli.Publish(topic, 1, false, body); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		e.t.Fatalf("publish %s: %v", topic, tok.Error())
	}
}

// nbirth publishes an NBIRTH at seq 0 and resets the local sequence, exactly
// as a real edge does on birth or rebirth. bdSeq is unchanged across a
// rebirth (spec §5.4 / the edge profile's EdgeRebirthBdSeqUnchanged).
func (e *tckEdgeNode) nbirth() {
	e.seq = 0
	now := uint64(time.Now().UnixMilli())
	e.publish(topicN("NBIRTH"), sparkplug.Payload{
		Timestamp: now,
		Seq:       0,
		Metrics: []sparkplug.Metric{
			{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: e.bdSeq, Timestamp: now},
			{Name: "Node Control/Rebirth", Datatype: spb.DataType_Boolean, Value: false, Timestamp: now},
			{Name: "Well/Level", Datatype: spb.DataType_Double, Value: 12.5, Timestamp: now},
			{Name: "Site", Datatype: spb.DataType_String, Value: "TCK", Timestamp: now},
		},
	})
}

func (e *tckEdgeNode) next() uint64 {
	e.seq = (e.seq + 1) % 256
	return e.seq
}

func (e *tckEdgeNode) dbirth() {
	now := uint64(time.Now().UnixMilli())
	e.publish(topicD("DBIRTH"), sparkplug.Payload{
		Timestamp: now,
		Seq:       e.next(),
		Metrics: []sparkplug.Metric{
			{Name: "Pump/Run", Datatype: spb.DataType_Boolean, Value: true, Timestamp: now},
			{Name: "Pump/SpeedSP", Datatype: spb.DataType_Double, Value: 0.0, Timestamp: now},
		},
	})
}

func (e *tckEdgeNode) ndata() {
	now := uint64(time.Now().UnixMilli())
	e.publish(topicN("NDATA"), sparkplug.Payload{
		Timestamp: now,
		Seq:       e.next(),
		Metrics: []sparkplug.Metric{
			{Name: "Well/Level", Datatype: spb.DataType_Double, Value: 13.75, Timestamp: now},
		},
	})
}

func (e *tckEdgeNode) ddata() {
	now := uint64(time.Now().UnixMilli())
	e.publish(topicD("DDATA"), sparkplug.Payload{
		Timestamp: now,
		Seq:       e.next(),
		Metrics: []sparkplug.Metric{
			{Name: "Pump/Run", Datatype: spb.DataType_Boolean, Value: false, Timestamp: now},
		},
	})
}

// gapData jumps the sequence forward, leaving a hole the host must notice.
func (e *tckEdgeNode) gapData(seq uint64) {
	e.seq = seq
	now := uint64(time.Now().UnixMilli())
	e.publish(topicN("NDATA"), sparkplug.Payload{
		Timestamp: now,
		Seq:       seq,
		Metrics: []sparkplug.Metric{
			{Name: "Well/Level", Datatype: spb.DataType_Double, Value: 99.0, Timestamp: now},
		},
	})
}

func (e *tckEdgeNode) ddeath() {
	e.publish(topicD("DDEATH"), sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		Seq:       e.next(),
	})
}

// ndeath is the explicit death certificate. It carries only bdSeq and no seq
// at all, and must match the birth's bdSeq or the host will (correctly)
// ignore it as a stale will from a prior session.
func (e *tckEdgeNode) ndeath() {
	e.publish(topicN("NDEATH"), sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		OmitSeq:   true,
		Metrics: []sparkplug.Metric{
			{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: e.bdSeq},
		},
	})
}

func (e *tckEdgeNode) drainRebirths() {
	for {
		select {
		case <-e.rebirths:
		default:
			return
		}
	}
}

func (e *tckEdgeNode) waitRebirth(d time.Duration) bool {
	select {
	case <-e.rebirths:
		return true
	case <-time.After(d):
		return false
	}
}

func isRebirthCommand(topic string, payload []byte) bool {
	if topic != topicN("NCMD") {
		return false
	}
	p, err := sparkplug.DecodePayload(payload)
	if err != nil {
		return false
	}
	for _, m := range p.Metrics {
		if m.Name != "Node Control/Rebirth" {
			continue
		}
		if b, ok := m.Value.(bool); ok && b {
			return true
		}
	}
	return false
}

func topicN(kind string) string { return "spBv1.0/" + tckGroup + "/" + kind + "/" + tckEdge }
func topicD(kind string) string { return topicN(kind) + "/" + tckDevice }

// ── fixtures + small helpers ─────────────────────────────────────────────

// tckBaseline is the runtime's first output snapshot for tckManifest: every
// output tag at its init:. Handing it over before any operator write is what
// a real project's first scan does.
func tckBaseline() nio.Values {
	out := nio.Values{}
	for _, spec := range tckManifest().TagSpecs() {
		if spec.Role == sphost.RoleOutput && spec.Init != nil {
			out[spec.Name] = spec.Init
		}
	}
	return out
}

// tckManifest is the smallest manifest that exercises every outbound path:
// a node, a device, node- and device-level inputs, and one writable binding
// (→ DCMD). The synthesized companion tags give the state machine somewhere
// to publish online/birth/rebirth.
func tckManifest() sphost.Manifest {
	return sphost.Manifest{
		Group: tckGroup,
		Nodes: []sphost.Node{{
			EdgeNode:   tckEdge,
			Prefix:     tckEdge,
			OnlineTag:  "E1__Online",
			BirthTag:   "E1__LastBirthMs",
			RebirthTag: "E1__Rebirth",
			Devices:    []sphost.Device{{Device: tckDevice, OnlineTag: "E1_D1__Online"}},
		}},
		Tags: []sphost.Binding{
			{Name: "E1_Well_Level", Node: tckEdge, Metric: "Well/Level", Type: "Double"},
			{Name: "E1_Site", Node: tckEdge, Metric: "Site", Type: "String"},
			{Name: "E1_D1_Pump_Run", Node: tckEdge, Device: tckDevice, Metric: "Pump/Run", Type: "Boolean"},
			{Name: "E1_D1_Pump_SpeedSP", Node: tckEdge, Device: tckDevice, Metric: "Pump/SpeedSP",
				Type: "Double", Writable: true, Init: 0.0},
		},
	}
}

func tckNodeOnline(d *sphost.Driver) bool {
	for _, n := range d.Status().Nodes {
		if n.EdgeNode == tckEdge {
			return n.Online
		}
	}
	return false
}

func tckDeviceOnline(d *sphost.Driver) bool {
	for _, n := range d.Status().Nodes {
		if n.EdgeNode != tckEdge {
			continue
		}
		for _, dev := range n.Devices {
			if dev.ID == tckDevice {
				return dev.Online
			}
		}
	}
	return false
}

// buildTCKTool compiles the harness once so the test can drive the binary
// directly (and signal it). NAUTILUS_TCK_LOCAL points at a working tree;
// otherwise the pinned tag comes from the module proxy.
func buildTCKTool(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "sparkplug-tck")

	var cmd *exec.Cmd
	if local := os.Getenv("NAUTILUS_TCK_LOCAL"); local != "" {
		t.Logf("building TCK harness from %s", local)
		cmd = exec.Command("go", "build", "-o", bin, "./cmd/sparkplug-tck")
		cmd.Dir = local
		cmd.Env = append(os.Environ(), "GOFLAGS=")
	} else {
		t.Logf("installing TCK harness %s", tckHostVersion)
		cmd = exec.Command("go", "install",
			"github.com/joyautomation/sparkplug-tck-go/cmd/sparkplug-tck@"+tckHostVersion)
		// GOBIN redirects the install; GOFLAGS is cleared because
		// `go install pkg@version` rejects an inherited -mod flag.
		cmd.Env = append(os.Environ(), "GOBIN="+dir, "GOFLAGS=")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build TCK harness: %v\n%s", err, out)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("TCK harness binary missing after build: %v", err)
	}
	return bin
}

// reserveAddr picks a free loopback port and releases it, so the harness can
// bind it. Racy in principle, fine in practice and required because the
// harness only reports its address on stderr.
func reserveAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
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

func waitFor(timeout time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ok()
}

func tckLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// tckLog pipes the harness's stderr into the test log.
type tckLog struct{ t *testing.T }

func (w tckLog) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			w.t.Log("tck: " + line)
		}
	}
	return len(p), nil
}
