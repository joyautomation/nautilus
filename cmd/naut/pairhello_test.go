package main

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/quorum/node"
)

// helloRig is a witness and two members' listeners on loopback; each
// member is started with the manifest the test gives it.
type helloRig struct {
	wit     string
	lnA     net.Listener
	lnB     net.Listener
	env     map[string]map[string]string
	nodes   map[string]*pairNode
	witPort string
}

func newHelloRig(t *testing.T) *helloRig {
	t.Helper()
	if testing.Short() {
		t.Skip("runs a pair and a witness on loopback for several leases")
	}
	r := &helloRig{env: map[string]map[string]string{}, nodes: map[string]*pairNode{}}
	var err error
	if r.lnA, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if r.lnB, err = net.Listen("tcp", "127.0.0.2:0"); err != nil {
		r.lnA.Close()
		t.Skip("no 127.0.0.2 on this host:", err)
	}
	cluster, token := node.NewClusterID(), testPeerToken
	wctx, wstop := context.WithCancel(context.Background())
	wready, wdone := make(chan string, 1), make(chan int, 1)
	go func() {
		wdone <- witnessMain(wctx, []string{"--listen", "127.0.0.1:0", "--state", t.TempDir(), "--lease", "500ms"},
			pairEnv(map[string]string{"NAUTILUS_PEER_TOKEN": token}), io.Discard, io.Discard, wready)
	}()
	t.Cleanup(func() { wstop(); <-wdone })
	r.wit = <-wready
	_, r.witPort, _ = net.SplitHostPort(r.wit)
	for _, n := range []string{"a", "b"} {
		r.env[n] = map[string]string{"NAUTILUS_NODE": n, "NAUTILUS_PEER_TOKEN": token, "NAUTILUS_CLUSTER": cluster, "STATE_DIRECTORY": t.TempDir()}
	}
	return r
}

// files is the pair's manifest naming the witness as witness.
func (r *helloRig) files(witness string) fstest.MapFS {
	return pairProject(r.lnA.Addr().String(), r.lnB.Addr().String(), witness)
}

func (r *helloRig) commission(t *testing.T) {
	t.Helper()
	ea, eb := r.nodes["a"].m.q.Node(), r.nodes["b"].m.q.Node()
	pairWait(t, "both to hear the witness", 10*time.Second, func() bool {
		return ea.Status().WitnessIncarnation != 0 && eb.Status().WitnessIncarnation != 0
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ea.Recommission(ctx, ea.Status().WitnessIncarnation, max(ea.KnownFloor(), eb.KnownFloor())+1000); err != nil {
		t.Fatal(err)
	}
}

// A and B run different redundancy: sections (here B names the same
// witness by another address, as a manifest edited on one box would):
// the hello finds it, both raise config-differs, and neither campaigns,
// so the pair never elects on two views of its members (test plan F30;
// config_differs_campaigns turns it red).
func TestPairConfigDiffersHoldsCampaigns(t *testing.T) {
	r := newHelloRig(t)
	r.nodes["a"] = startPairNode(t, r.files(r.wit), r.env["a"], r.lnA)
	r.nodes["b"] = startPairNode(t, r.files("localhost:"+r.witPort), r.env["b"], r.lnB)
	r.commission(t)
	ea, eb := r.nodes["a"].m.q.Node(), r.nodes["b"].m.q.Node()
	pairWait(t, "both to see the configurations differ", 10*time.Second, func() bool {
		return ea.Status().ConfigDiffers && eb.Status().ConfigDiffers
	})
	// Loud: /api/cluster's warnings (the dashboard's banner) on both.
	pairWait(t, "the mismatch in both controllers' status warnings", 5*time.Second, func() bool {
		for _, n := range []string{"a", "b"} {
			if !strings.Contains(strings.Join(r.nodes[n].m.q.Status().Warnings, "|"), "members differ") {
				return false
			}
		}
		return true
	})
	for end := time.Now().Add(4 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ea.IsLeader() || eb.IsLeader() {
			t.Fatal("a member led while the pair's configurations differ")
		}
	}
}

// A running pair is not stopped by it: B restarts with an edited section
// while A leads; A keeps its lease (it only stops campaigning anew), B
// never leads, and both report the mismatch.
func TestPairConfigChangeKeepsTheLeader(t *testing.T) {
	r := newHelloRig(t)
	same := r.files(r.wit)
	r.nodes["a"] = startPairNode(t, same, r.env["a"], r.lnA)
	r.nodes["b"] = startPairNode(t, same, r.env["b"], r.lnB)
	r.commission(t)
	ea, eb := r.nodes["a"].m.q.Node(), r.nodes["b"].m.q.Node()
	pairWait(t, "a leader", 15*time.Second, func() bool { return ea.IsLeader() || eb.IsLeader() })
	if eb.IsLeader() {
		r.nodes["b"].m.q.Release()
	}
	pairWait(t, "a to lead", 15*time.Second, func() bool { return ea.IsLeader() })
	// Both builds keep a live I/O apply, and say so at the hello.
	pairWait(t, "the peers' capabilities", 10*time.Second, func() bool {
		return r.nodes["a"].m.peersKeepIO() == "" && r.nodes["b"].m.peersKeepIO() == ""
	})

	r.nodes["b"].crash()
	ln, err := net.Listen("tcp", r.lnB.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	r.nodes["b"] = startPairNode(t, r.files("localhost:"+r.witPort), r.env["b"], ln)
	eb = r.nodes["b"].m.q.Node()
	pairWait(t, "a to see the edited section", 10*time.Second, func() bool { return ea.Status().ConfigDiffers })
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if !ea.IsLeader() {
			t.Fatal("the leader gave up its lease when its peer's section changed")
		}
		if eb.IsLeader() {
			t.Fatal("the edited member led")
		}
	}
	if !eb.Status().ConfigDiffers {
		t.Error("the edited member did not report the mismatch")
	}
}

// Before the peer has answered a hello, a live I/O apply can't know
// whether the peer's build keeps it, and says so.
func TestPairPeersKeepIOUnknownBeforeTheHello(t *testing.T) {
	r := loadPair(t, pairProject("127.0.0.1:9450", "127.0.0.2:9450", "127.0.0.3:9460")).Redundancy
	var ln net.Listener
	m, err := newPairMember(r, "127.0.0.1:8080", "", pairDeps{getenv: pairEnv(pairGoodEnv(t)), listen: func(string) (net.Listener, error) {
		var err error
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		return ln, err
	}})
	if err != nil {
		t.Skip(err) // not Linux
	}
	defer m.Close()
	if why := m.peersKeepIO(); !strings.Contains(why, "has not answered") {
		t.Fatalf("before any hello: %q", why)
	}
}
