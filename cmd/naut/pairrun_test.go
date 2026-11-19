package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
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
	"github.com/joyautomation/nautilus/server"
)

// pairProject is a counting program whose redundancy: names a pair at the
// given peer and witness addresses, with a 500 ms lease.
func pairProject(a, b, w string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(fmt.Sprintf(`name: pair
tasks:
  - program: main.st
    scan: 50ms
tags:
  - { name: SP, role: setpoint, init: 0.0 }
  - { name: Level, role: input, type: REAL, init: 0.0 }
substitutes:
  rules:
    - { match: Level, substitute: 90.0, ack-on-start: true }
redundancy:
  experimental: true
  peers:
    a: %s
    b: %s
  witness: %s
  lease: 500ms
`, a, b, w))},
		"main.st": &fstest.MapFile{Data: []byte("PROGRAM Main\nVAR_EXTERNAL SP : REAL; Level : REAL; END_VAR\nVAR n : DINT; END_VAR\nn := n + 1;\nSP := SP + 0.0 * Level;\nEND_PROGRAM\n")},
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
	want, _ := filepath.Abs("/srv/naut-state")
	if o, _, _ := pairOptions(r, "0.0.0.0:8080", "", pairEnv(env)); o.StatePath != filepath.Join(want, pairStateFile) {
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
	return startPairNodeAPI(t, files, env, ln, nil)
}

// startPairNodeAPI also serves the tag API on api, as runProject does,
// when api is not nil.
func startPairNodeAPI(t *testing.T, files fstest.MapFS, env map[string]string, ln, api net.Listener) *pairNode {
	t.Helper()
	proj := loadPair(t, files)
	apiAddr := "127.0.0.1:18080"
	if api != nil {
		apiAddr = api.Addr().String()
	}
	m, err := newPairMember(proj.Redundancy, apiAddr, "", pairDeps{getenv: pairEnv(env), listen: func(string) (net.Listener, error) { return ln, nil }})
	if err != nil {
		t.Fatal(err)
	}
	wireRedundancy(proj, m.q)
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	sl, err := m.startLog(rt, 0)
	if err != nil {
		t.Fatal(err)
	}
	gate := gateOnLiveScans(proj, m.q, rt, localGateLease)
	ctx, cancel := context.WithCancel(context.Background())
	n := &pairNode{m: m, rt: rt, cancel: cancel, done: make(chan struct{})}
	if api != nil {
		sopts := proj.Server
		sopts.Cluster = m.q
		sopts.SubstituteLog = sl
		srv := server.New(rt, sopts)
		hs := &http.Server{Handler: srv.Handler()}
		go hs.Serve(api)
		go func() { <-ctx.Done(); hs.Close() }()
	}
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
	// Both tag APIs on one port, as two boxes running one manifest have.
	apiA, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, apiPort, _ := net.SplitHostPort(apiA.Addr().String())
	apiB, err := net.Listen("tcp", "127.0.0.2:"+apiPort)
	if err != nil {
		apiA.Close()
		t.Skip("127.0.0.2 does not take the same API port:", err)
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
	nodes := map[string]*pairNode{"a": startPairNodeAPI(t, files, env("a"), lnA, apiA), "b": startPairNodeAPI(t, files, env("b"), lnB, apiB)}
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
		return s.m.q.Status().LeaderAddr == host[lead]+":"+apiPort
	})
	// A write to the standby's tag API lands on the leader: the standby's
	// proxy follows Status.LeaderAddr.
	resp, err := http.Post("http://"+host[stand]+":"+apiPort+"/api/tags", "application/json", strings.NewReader(`{"name":"SP","value":7.5}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("a write to the standby: %d %s", resp.StatusCode, body)
	}
	pairWait(t, "the write to reach the leader's store", 5*time.Second, func() bool {
		v, _ := l.rt.Tags().ReadPath("SP")
		return fmt.Sprint(v) == "7.5"
	})
	if v, _ := s.rt.Tags().ReadPath("SP"); fmt.Sprint(v) == "7.5" {
		t.Error("the write landed on the standby's own store, not the leader's")
	}
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
	// Until state streaming, the pair says its retained values are not
	// replicated, wherever its status is shown.
	if w := el.Status().Warnings; len(w) != 1 || !strings.Contains(w[0], "retained state not replicated") {
		t.Errorf("status warnings %q, want the retain warning", w)
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

// dupRig is a commissioned pair a and b with a witness on loopback, and a
// third loopback address for a process that should not be there.
type dupRig struct {
	files fstest.MapFS
	env   map[string]map[string]string
	nodes map[string]*pairNode
	lns   [3]net.Listener
}

func newDupRig(t *testing.T) *dupRig {
	t.Helper()
	if testing.Short() {
		t.Skip("runs members and a witness on loopback for several leases")
	}
	if goruntime.GOOS != "linux" {
		t.Skip("a pair member needs CLOCK_BOOTTIME (Linux)")
	}
	r := &dupRig{env: map[string]map[string]string{}, nodes: map[string]*pairNode{}}
	for i := range r.lns {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.%d:0", i+1))
		if err != nil {
			for _, l := range r.lns[:i] {
				l.Close()
			}
			t.Skip("no loopback address 127.0.0.", i+1, ":", err)
		}
		r.lns[i] = ln
	}
	cluster, token := node.NewClusterID(), "peer-secret"
	wctx, wstop := context.WithCancel(context.Background())
	wready, wdone := make(chan string, 1), make(chan int, 1)
	go func() {
		wdone <- witnessMain(wctx, []string{"--listen", "127.0.0.1:0", "--state", t.TempDir(), "--lease", "500ms"},
			pairEnv(map[string]string{"NAUTILUS_PEER_TOKEN": token}), io.Discard, io.Discard, wready)
	}()
	t.Cleanup(func() { wstop(); <-wdone })
	r.files = pairProject(r.lns[0].Addr().String(), r.lns[1].Addr().String(), <-wready)
	for _, n := range []string{"a", "b"} {
		r.env[n] = map[string]string{"NAUTILUS_NODE": n, "NAUTILUS_PEER_TOKEN": token, "NAUTILUS_CLUSTER": cluster, "STATE_DIRECTORY": t.TempDir()}
	}
	r.nodes["a"] = startPairNode(t, r.files, r.env["a"], r.lns[0])
	r.nodes["b"] = startPairNode(t, r.files, r.env["b"], r.lns[1])
	ea, eb := r.nodes["a"].m.q.Node(), r.nodes["b"].m.q.Node()
	pairWait(t, "both to hear the witness", 10*time.Second, func() bool {
		return ea.Status().WitnessIncarnation != 0 && eb.Status().WitnessIncarnation != 0
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ea.Recommission(ctx, ea.Status().WitnessIncarnation, max(ea.KnownFloor(), eb.KnownFloor())+1000); err != nil {
		t.Fatal(err)
	}
	pairWait(t, "a leader", 15*time.Second, func() bool { return ea.IsLeader() || eb.IsLeader() })
	// The cases below are about the real a leading.
	if eb.IsLeader() {
		r.nodes["b"].m.q.Release()
	}
	pairWait(t, "a to lead", 15*time.Second, func() bool { return ea.IsLeader() })
	return r
}

// watch samples the electors for d and fails at the first instant two of
// them lead; it returns which led.
func watchOneLeader(t *testing.T, d time.Duration, els map[string]*node.Elector) map[string]bool {
	t.Helper()
	led := map[string]bool{}
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		var now []string
		for n, e := range els {
			if e.IsLeader() {
				now = append(now, n)
				led[n] = true
			}
		}
		if len(now) > 1 {
			t.Fatalf("two processes lead at once: %v", now)
		}
	}
	return led
}

// The #1028 review's sequence: the pair runs with a leading; B is stopped
// and started again with A's env file copied over its own (NAUTILUS_NODE=a)
// and its own state directory kept. B refuses to start: its state file
// was written by b (state_owner_unchecked turns this red).
func TestPairCopiedEnvWithKeptStateRefused(t *testing.T) {
	r := newDupRig(t)
	r.nodes["b"].crash()
	env := maps.Clone(r.env["a"])
	env["STATE_DIRECTORY"] = r.env["b"]["STATE_DIRECTORY"]
	ln, err := net.Listen("tcp", r.lns[1].Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	proj := loadPair(t, r.files)
	m, err := newPairMember(proj.Redundancy, "127.0.0.1:18080", "", pairDeps{getenv: pairEnv(env), listen: func(string) (net.Listener, error) { return ln, nil }})
	if err == nil {
		m.Close()
		t.Fatal("b started as a over its own state file")
	}
	ln.Close()
	if !errors.Is(err, node.ErrNotOwner) || !strings.Contains(err.Error(), `written by "b"`) {
		t.Fatalf("refusal %v; want it to name the file's owner, b", err)
	}
}

// A cloned box: a third process runs with a copy of A's state file and
// A's env (a VM or disk cloned while A ran), so its file's owner matches.
// The voters bind a name to the boot nonce its running promise was
// granted under: B and the witness refuse the clone while A renews, so
// the two never lead at once, and B reports two processes named a
// (duplicate_member_granted turns this red).
func TestPairClonedMemberNeverLeadsTwice(t *testing.T) {
	r := newDupRig(t)
	cloneDir := t.TempDir()
	b, err := os.ReadFile(filepath.Join(r.env["a"]["STATE_DIRECTORY"], pairStateFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloneDir, pairStateFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	env := maps.Clone(r.env["a"])
	env["STATE_DIRECTORY"] = cloneDir
	clone := startPairNode(t, r.files, env, r.lns[2])
	els := map[string]*node.Elector{"a": r.nodes["a"].m.q.Node(), "b": r.nodes["b"].m.q.Node(), "clone": clone.m.q.Node()}
	led := watchOneLeader(t, 10*time.Second, els)
	if !led["a"] || led["clone"] {
		t.Errorf("led: %v; want a throughout and never the clone", led)
	}
	if els["b"].Status().Duplicates == 0 {
		t.Error("b's voter never reported two processes named a")
	}
}

// A pair member that can't start stops naut run: it never falls back to
// another elector (the Kubernetes Lease would be standalone, always
// leader, off-cluster).
func TestPairStartErrorNeverFallsBackToTheLease(t *testing.T) {
	proj := loadPair(t, pairProject("127.0.0.1:9450", "127.0.0.2:9450", "127.0.0.3:9460"))
	for _, unset := range []string{"NAUTILUS_PEER_TOKEN", "NAUTILUS_CLUSTER", "STATE_DIRECTORY"} {
		env := pairGoodEnv(t)
		delete(env, unset)
		el, pair, err := electorWith(proj, "127.0.0.1:8080", "", pairDeps{getenv: pairEnv(env), listen: func(string) (net.Listener, error) {
			t.Fatal("a member that can't start bound its peer port")
			return nil, nil
		}})
		if err == nil || el != nil || pair != nil {
			t.Errorf("without %s: elector %T, pair %v, err %v; want an error and nothing else", unset, el, pair, err)
		}
	}
}

// NAUTILUS_NODE says a box is a pair member. Set where the manifest names
// no pair, naut run refuses to start: each box would be its own leader.
func TestNodeSetWithoutAPairRefused(t *testing.T) {
	for _, red := range []string{"", "redundancy: {}\n", "redundancy:\n  lease: plant-a\n"} {
		files := pairProject("x:1", "y:1", "z:1")
		files["nautilus.yaml"] = &fstest.MapFile{Data: []byte("name: pair\ntasks:\n  - program: main.st\n    scan: 50ms\ntags:\n  - { name: SP, role: setpoint, init: 0.0 }\n" + red)}
		proj := loadPair(t, files)
		_, _, err := electorWith(proj, "127.0.0.1:0", "", pairDeps{getenv: pairEnv(map[string]string{"NAUTILUS_NODE": "a"}), listen: nil})
		if err == nil || !strings.Contains(err.Error(), "names no pair") {
			t.Errorf("%q with NAUTILUS_NODE=a: %v; want refused", red, err)
		}
		if _, _, err := electorWith(proj, "127.0.0.1:0", "", pairDeps{getenv: pairEnv(nil)}); err != nil {
			t.Errorf("%q without NAUTILUS_NODE: %v", red, err)
		}
	}
}

// A pair member's tag API must be reachable by its peer: one bound to
// loopback while the member's peers: address is on the network is
// refused at start, saying what to set.
func TestPairAPIOnLoopbackRefused(t *testing.T) {
	for _, c := range []struct {
		api, peer string
		ok        bool
	}{
		{"localhost:8080", "ctl-a:9450", false},
		{"127.0.0.1:8080", "10.0.0.5:9450", false},
		{"[::1]:8080", "ctl-a:9450", false},
		{":8080", "ctl-a:9450", true},
		{"0.0.0.0:8080", "ctl-a:9450", true},
		{"10.0.0.5:8080", "ctl-a:9450", true},
		{"127.0.0.1:8080", "127.0.0.1:9450", true}, // the whole pair on one host
	} {
		err := checkAPIReachable(c.api, c.peer)
		if (err == nil) != c.ok {
			t.Errorf("API %s, peer address %s: %v; want ok=%v", c.api, c.peer, err, c.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "NAUTILUS_ADDR") {
			t.Errorf("the refusal does not say what to set: %v", err)
		}
	}
	r := loadPair(t, pairProject("ctl-a:9450", "ctl-b:9450", "wit:9460")).Redundancy
	if _, _, err := pairOptions(r, "localhost:8080", "", pairEnv(pairGoodEnv(t))); err == nil {
		t.Error("pairOptions took a loopback tag API with peers on the network")
	}
}
