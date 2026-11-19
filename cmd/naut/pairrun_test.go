package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/quorum/node"
	"github.com/joyautomation/nautilus/runtime"
)

// pairProject is a counting program whose redundancy: names a pair at the
// given peer and witness addresses, with a 500 ms lease.
func pairProject(a, b, w string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(fmt.Sprintf(`name: pair
tasks:
  - program: main.st
    scan: 50ms
redundancy:
  peers:
    a: %s
    b: %s
  witness: %s
  lease: 500ms
`, a, b, w))},
		"main.st": &fstest.MapFile{Data: []byte("PROGRAM Main\nVAR n : DINT; END_VAR\nn := n + 1;\nEND_PROGRAM\n")},
	}
}

// pairEnv is a member's environment: a map, nothing from the process.
func pairEnv(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func pairGoodEnv(t *testing.T) map[string]string {
	return map[string]string{
		"NAUTILUS_NODE": "a", "NAUTILUS_PEER_TOKEN": "peer-secret",
		"NAUTILUS_CLUSTER": node.NewClusterID(), "STATE_DIRECTORY": t.TempDir(),
	}
}

func loadPair(t *testing.T, files fstest.MapFS) *project.Project {
	t.Helper()
	p, err := project.Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A pair member refuses to start without what says who it is and what it
// belongs to, and with its voter state anywhere a deploy would replace
// (design §2: misconfiguration is refused at start, never resolved
// quietly). Each refusal names what to set.
func TestPairMemberRefusesToStart(t *testing.T) {
	r := loadPair(t, pairProject("127.0.0.1:9450", "127.0.0.2:9450", "127.0.0.3:9460")).Redundancy
	if _, _, err := pairOptions(r, "127.0.0.1:8080", "", pairEnv(pairGoodEnv(t))); err != nil {
		t.Fatalf("a complete environment: %v", err)
	}
	projDir := t.TempDir()
	for _, c := range []struct {
		name, key, val, dir, want string
	}{
		{"no NAUTILUS_NODE", "NAUTILUS_NODE", "", "", "NAUTILUS_NODE is not set"},
		{"NAUTILUS_NODE not a or b", "NAUTILUS_NODE", "c", "", `NAUTILUS_NODE="c"`},
		{"no peer token", "NAUTILUS_PEER_TOKEN", "", "", "NAUTILUS_PEER_TOKEN is not set"},
		{"no cluster ID", "NAUTILUS_CLUSTER", "", "", "not commissioned"},
		{"a malformed cluster ID", "NAUTILUS_CLUSTER", "plant-a", "", "32 lowercase hex"},
		{"no state directory", "STATE_DIRECTORY", "", "", "no state directory"},
		{"a state directory in the project", "STATE_DIRECTORY", filepath.Join(projDir, ".naut"), projDir, "inside the project"},
		{"the project itself", "STATE_DIRECTORY", projDir, projDir, "inside the project"},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := pairGoodEnv(t)
			env[c.key] = c.val
			_, _, err := pairOptions(r, "127.0.0.1:8080", c.dir, pairEnv(env))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
		})
	}
	// A directory beside the project, whose name starts with the
	// project's, is not inside it.
	env := pairGoodEnv(t)
	env["STATE_DIRECTORY"] = projDir + "-state"
	if _, _, err := pairOptions(r, "127.0.0.1:8080", projDir, pairEnv(env)); err != nil {
		t.Errorf("a state directory beside the project: %v", err)
	}
}

// The member's options: the peer is the other name, the peer listener
// binds its own port on every address, the voter state is in the state
// directory (state-dir: over $STATE_DIRECTORY), and the two API addresses
// are each controller's host with this controller's API port, so a
// standby's proxy finds the leader's tag API.
func TestPairMemberOptions(t *testing.T) {
	r := loadPair(t, pairProject("127.0.0.1:9450", "127.0.0.2:9451", "127.0.0.3:9460")).Redundancy
	env := pairGoodEnv(t)
	env["NAUTILUS_NODE"] = "B"
	env["NAUTILUS_TOKEN"] = "client"
	o, listen, err := pairOptions(r, "0.0.0.0:8080", "", pairEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	if o.Self != "b" || o.Peer != "a" || o.PeerAddr != "127.0.0.1:9450" || o.WitnessAddr != "127.0.0.3:9460" || listen != ":9451" {
		t.Errorf("self %s peer %s at %s, witness %s, listen %s", o.Self, o.Peer, o.PeerAddr, o.WitnessAddr, listen)
	}
	if o.SelfAPI != "127.0.0.2:8080" || o.PeerAPI != "127.0.0.1:8080" {
		t.Errorf("API addresses %s (self) %s (peer)", o.SelfAPI, o.PeerAPI)
	}
	if o.StatePath != filepath.Join(env["STATE_DIRECTORY"], pairStateFile) || o.Lease != 500*time.Millisecond || o.ClientToken != "client" {
		t.Errorf("state %s lease %v client token %q", o.StatePath, o.Lease, o.ClientToken)
	}
	r.StateDir = "/srv/naut-state"
	if o, _, _ := pairOptions(r, "0.0.0.0:8080", "", pairEnv(env)); o.StatePath != "/srv/naut-state/quorum.state" {
		t.Errorf("state-dir: does not override $STATE_DIRECTORY: %s", o.StatePath)
	}
}

// pendingElection is an Election that has decided nothing.
type pendingElection struct{ leader.Election }

func (pendingElection) Decided() <-chan struct{} { return make(chan struct{}) }

// A pair member is a voter: with no leader to elect (a new pair awaiting
// commissioning, its peer and witness away) it still tells systemd it is
// up once firstDecisionTimeout has passed, or Type=notify's start timeout
// would restart it in a loop. A Kubernetes replica keeps waiting.
func TestPairMemberIsReadyWithoutALeader(t *testing.T) {
	var el pendingElection
	if replicaReady(false, el, true, firstDecisionTimeout-time.Millisecond) {
		t.Error("ready before firstDecisionTimeout with nothing decided")
	}
	if !replicaReady(false, el, true, firstDecisionTimeout) {
		t.Error("a pair member with no leader never becomes ready")
	}
	if replicaReady(false, el, false, time.Hour) {
		t.Error("a Kubernetes replica became ready with nothing decided")
	}
	if replicaReady(false, nil, false, time.Hour) || !replicaReady(true, nil, false, 0) {
		t.Error("a standalone controller is ready once it has scanned, and only then")
	}
}

// Without peers:, redundancy: is the Kubernetes Lease, as before: no pair
// member, no peer listener.
func TestRedundancyWithoutPeersIsTheLease(t *testing.T) {
	for _, red := range []string{"redundancy: {}\n", "redundancy:\n  lease: plant-a\n"} {
		files := pairProject("x:1", "y:1", "z:1")
		files["nautilus.yaml"] = &fstest.MapFile{Data: []byte("name: pair\ntasks:\n  - program: main.st\n    scan: 50ms\n" + red)}
		el, pair, err := electorFor(loadPair(t, files), "127.0.0.1:0", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := el.(*leader.Elector); !ok || pair != nil {
			t.Errorf("%q: elector %T, pair %v; want the Lease elector and no pair", red, el, pair)
		}
	}
}

// pairNode is one member of the pair started the way runProject starts
// it: the member (peer listener serving), the project wired to its
// election, the gate on live scans, the election and the scan loop.
type pairNode struct {
	m      *pairMember
	rt     *runtime.Runtime
	cancel context.CancelFunc
	done   chan struct{}
}

func startPairNode(t *testing.T, files fstest.MapFS, env map[string]string, ln net.Listener) *pairNode {
	t.Helper()
	proj := loadPair(t, files)
	m, err := newPairMember(proj.Redundancy, "127.0.0.1:18080", "", pairDeps{getenv: pairEnv(env), listen: func(string) (net.Listener, error) { return ln, nil }})
	if err != nil {
		t.Fatal(err)
	}
	wireRedundancy(proj, m.q)
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	gate := gateOnLiveScans(proj, m.q, rt, localGateLease)
	ctx, cancel := context.WithCancel(context.Background())
	n := &pairNode{m: m, rt: rt, cancel: cancel, done: make(chan struct{})}
	go func() {
		ran := make(chan struct{})
		go func() { rt.Run(ctx); close(ran) }()
		gate.Run(ctx)
		<-ran
		close(n.done)
	}()
	t.Cleanup(n.crash)
	return n
}

// crash stops the member without a Release, as a power cut would, and
// closes its peer listener.
func (n *pairNode) crash() {
	n.cancel()
	<-n.done
	if n.m != nil {
		n.m.Close()
		n.m = nil
	}
}

func pairWait(t *testing.T, what string, d time.Duration, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out after %s waiting for %s", d, what)
}

// T2, end to end through naut run's wiring: two pair members and `naut
// witness` on loopback. A new pair waits for the operator; commissioned,
// one member leads and scans, the other stands by without scanning and
// names the leader's tag API; when the leader dies without a word, the
// standby takes over (cold) and scans.
func TestPairElectsAndTakesOverThroughNautRun(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a pair and a witness on loopback for several leases")
	}
	if goruntime.GOOS != "linux" {
		t.Skip("a pair member needs CLOCK_BOOTTIME (Linux)")
	}
	lnA, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lnB, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		lnA.Close()
		t.Skip("no 127.0.0.2 on this host:", err)
	}
	cluster, token := node.NewClusterID(), "peer-secret"
	wctx, wstop := context.WithCancel(context.Background())
	wready, wdone := make(chan string, 1), make(chan int, 1)
	go func() {
		wdone <- witnessMain(wctx, []string{"--listen", "127.0.0.1:0", "--state", t.TempDir(), "--lease", "500ms"},
			pairEnv(map[string]string{"NAUTILUS_PEER_TOKEN": token}), io.Discard, io.Discard, wready)
	}()
	t.Cleanup(func() { wstop(); <-wdone })
	var wit string
	select {
	case wit = <-wready:
	case code := <-wdone:
		t.Fatalf("the witness exited %d", code)
	}
	files := pairProject(lnA.Addr().String(), lnB.Addr().String(), wit)
	env := func(self string) map[string]string {
		return map[string]string{"NAUTILUS_NODE": self, "NAUTILUS_PEER_TOKEN": token, "NAUTILUS_CLUSTER": cluster, "STATE_DIRECTORY": t.TempDir()}
	}
	nodes := map[string]*pairNode{"a": startPairNode(t, files, env("a"), lnA), "b": startPairNode(t, files, env("b"), lnB)}
	a, b := nodes["a"].m.q.Node(), nodes["b"].m.q.Node()

	// A new pair: both hear the witness and wait for the operator.
	pairWait(t, "both members to hear the witness", 10*time.Second, func() bool {
		return a.Status().WitnessIncarnation != 0 && b.Status().WitnessIncarnation != 0
	})
	if a.IsLeader() || b.IsLeader() || nodes["a"].rt.Stats().Count+nodes["b"].rt.Stats().Count != 0 {
		t.Fatal("an uncommissioned pair elected or scanned")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Recommission(ctx, a.Status().WitnessIncarnation, max(a.KnownFloor(), b.KnownFloor())+1000); err != nil {
		t.Fatal(err)
	}

	var lead, stand string
	pairWait(t, "a leader", 15*time.Second, func() bool {
		switch {
		case a.IsLeader():
			lead, stand = "a", "b"
		case b.IsLeader():
			lead, stand = "b", "a"
		}
		return lead != ""
	})
	l, s := nodes[lead], nodes[stand]
	pairWait(t, "the leader to scan", 5*time.Second, func() bool { return l.rt.Stats().Count > 0 })
	host := map[string]string{"a": "127.0.0.1", "b": "127.0.0.2"}
	pairWait(t, "the standby to name the leader's tag API", 5*time.Second, func() bool {
		return s.m.q.Status().LeaderAddr == host[lead]+":18080"
	})
	time.Sleep(time.Second) // two leases: the standby still does not scan
	if n := s.rt.Stats().Count; n != 0 || s.m.q.IsLeader() {
		t.Fatalf("the standby scanned %d times (leader %v) while %s led", n, s.m.q.IsLeader(), lead)
	}

	l.crash()
	pairWait(t, "the standby to take over", 15*time.Second, func() bool { return s.m.q.IsLeader() })
	pairWait(t, "the new leader to scan", 5*time.Second, func() bool { return s.rt.Stats().Count > 0 })
	t.Logf("%s led, crashed; %s took over in term %d", lead, stand, s.m.q.Term())
}

// The peer port is bound once: a second member of the same controller
// (one started by hand beside the service) stops at the state file's lock,
// before it binds anything or votes.
func TestPairMemberTwiceIsRefused(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("a pair member needs CLOCK_BOOTTIME (Linux)")
	}
	r := loadPair(t, pairProject("127.0.0.1:9450", "127.0.0.2:9450", "127.0.0.3:9460")).Redundancy
	env := pairGoodEnv(t)
	listens := 0
	deps := pairDeps{getenv: pairEnv(env), listen: func(string) (net.Listener, error) {
		listens++
		return net.Listen("tcp", "127.0.0.1:0")
	}}
	m, err := newPairMember(r, "127.0.0.1:8080", "", deps)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := newPairMember(r, "127.0.0.1:8080", "", deps); err == nil {
		t.Fatal("a second member over the same state directory started")
	}
	if listens != 1 {
		t.Errorf("the second member bound its peer port (%d binds)", listens)
	}
	if _, err := os.Stat(filepath.Join(env["STATE_DIRECTORY"], pairStateFile+".lock")); err != nil {
		t.Errorf("no lock beside the state file: %v", err)
	}
}

// With peers:, naut run's elector is the pair's quorum, wired as the
// project's coordinator and its drivers' write gate, and its peer listener
// is already serving: an unauthenticated request is refused there.
func TestRedundancyWithPeersIsThePair(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("a pair member needs CLOCK_BOOTTIME (Linux)")
	}
	proj := loadPair(t, pairProject("127.0.0.1:9450", "127.0.0.2:9450", "127.0.0.3:9460"))
	var ln net.Listener
	el, pair, err := electorWith(proj, "127.0.0.1:8080", "", pairDeps{getenv: pairEnv(pairGoodEnv(t)), listen: func(string) (net.Listener, error) {
		var err error
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		return ln, err
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer pair.Close()
	if _, ok := el.(*leader.Quorum); !ok || pair == nil || el != leader.Election(pair.q) {
		t.Fatalf("elector %T, pair %v; want the pair's quorum", el, pair)
	}
	if proj.Runtime.Coordinator != pair.q || proj.WriteGate == nil {
		t.Error("the project is not wired to the pair's election")
	}
	if el.Status().Mode != "quorum" {
		t.Errorf("mode %q, want quorum", el.Status().Mode)
	}
	resp, err := http.Get("http://" + ln.Addr().String() + "/quorum/v1/x/hello")
	if err != nil {
		t.Fatalf("the peer listener does not answer: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Errorf("a request without the peer token got %d, want it refused", resp.StatusCode)
	}
}

// naut run warns about each advertised tag API name the server won't
// answer a browser by (design §6.1): an IP, localhost or a listed name
// (any case) passes.
func TestPairWarnsAboutUntrustedAdvertisedNames(t *testing.T) {
	m := &pairMember{o: node.Options{SelfAPI: "ctl-a.plant.lan:8080", PeerAPI: "ctl-b.plant.lan:8080"}}
	if got := m.untrustedAPIs(nil); len(got) != 2 || got[0] != "ctl-a.plant.lan" || got[1] != "ctl-b.plant.lan" {
		t.Errorf("nothing listed: %q, want both names", got)
	}
	if got := m.untrustedAPIs([]string{"CTL-A.plant.lan"}); len(got) != 1 || got[0] != "ctl-b.plant.lan" {
		t.Errorf("a listed: %q, want only b", got)
	}
	m.o = node.Options{SelfAPI: "10.0.0.5:8080", PeerAPI: "localhost:8080"}
	if got := m.untrustedAPIs(nil); len(got) != 0 {
		t.Errorf("an IP and localhost: %q, want no warning", got)
	}
}
