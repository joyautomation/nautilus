package main

import (
	"context"
	"errors"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/quorum/node"
)

// The cloned-disk gap (#1029), closed by binding the voter state to its
// box: a clone (A's quorum.state and env on another box) starts
// uncommissioned and never votes, whichever link the original has lost.

// links cuts connections between members: a cut from → to refuses new
// dials and closes those open.
type links struct {
	mu   sync.Mutex
	cut  map[string]map[string]bool // from → address
	open map[string][]net.Conn      // from+" "+address
}

func newLinks() *links {
	return &links{cut: map[string]map[string]bool{}, open: map[string][]net.Conn{}}
}

func (l *links) dialer(from string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		l.mu.Lock()
		blocked := l.cut[from][addr]
		l.mu.Unlock()
		if blocked {
			return nil, errors.New("link cut")
		}
		c, err := d.DialContext(ctx, network, addr)
		if err == nil {
			l.mu.Lock()
			l.open[from+" "+addr] = append(l.open[from+" "+addr], c)
			l.mu.Unlock()
		}
		return c, err
	}
}

// cutLink cuts from → addr: no new connection, and those open close.
func (l *links) cutLink(from, addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cut[from] == nil {
		l.cut[from] = map[string]bool{}
	}
	l.cut[from][addr] = true
	for _, c := range l.open[from+" "+addr] {
		c.Close()
	}
	delete(l.open, from+" "+addr)
}

// boxRig is a commissioned pair (a leading) and a witness on loopback,
// each member on its own box (fingerprint) and dialing through links.
type boxRig struct {
	t     *testing.T
	files fstest.MapFS
	env   map[string]map[string]string
	lns   [3]net.Listener
	wit   string
	nodes map[string]*pairNode
	links *links
}

func newBoxRig(t *testing.T) *boxRig {
	t.Helper()
	if testing.Short() {
		t.Skip("runs members and a witness on loopback for several leases")
	}
	if goruntime.GOOS != "linux" {
		t.Skip("a pair member needs CLOCK_BOOTTIME (Linux)")
	}
	r := &boxRig{t: t, env: map[string]map[string]string{}, nodes: map[string]*pairNode{}, links: newLinks()}
	for i := range r.lns {
		ln, err := net.Listen("tcp", "127.0.0."+string(rune('1'+i))+":0")
		if err != nil {
			for _, l := range r.lns[:i] {
				l.Close()
			}
			t.Skip("no loopback address:", err)
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
	r.wit = <-wready
	r.files = pairProject(r.lns[0].Addr().String(), r.lns[1].Addr().String(), r.wit)
	for _, n := range []string{"a", "b"} {
		r.env[n] = map[string]string{"NAUTILUS_NODE": n, "NAUTILUS_PEER_TOKEN": token, "NAUTILUS_CLUSTER": cluster, "STATE_DIRECTORY": t.TempDir()}
	}
	r.start("a", "box-a", r.env["a"], r.lns[0])
	r.start("b", "box-b", r.env["b"], r.lns[1])
	ea, eb := r.el("a"), r.el("b")
	pairWait(t, "both to hear the witness", 10*time.Second, func() bool {
		return ea.Status().WitnessIncarnation != 0 && eb.Status().WitnessIncarnation != 0
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ea.Recommission(ctx, ea.Status().WitnessIncarnation, max(ea.KnownFloor(), eb.KnownFloor())+1000); err != nil {
		t.Fatal(err)
	}
	pairWait(t, "a leader", 15*time.Second, func() bool { return ea.IsLeader() || eb.IsLeader() })
	if eb.IsLeader() {
		r.nodes["b"].m.q.Release()
	}
	pairWait(t, "a to lead", 15*time.Second, func() bool { return ea.IsLeader() })
	return r
}

// start runs member id (a, b, or clone) on box with env, listening on ln.
func (r *boxRig) start(id, box string, env map[string]string, ln net.Listener) {
	r.t.Helper()
	var b atomic.Value
	b.Store(box)
	r.startBox(id, &b, env, ln)
}

// startBox is start with a box the test changes: "" while it can't be
// read (a link that doesn't resolve).
func (r *boxRig) startBox(id string, box *atomic.Value, env map[string]string, ln net.Listener) {
	r.t.Helper()
	r.nodes[id] = startPairNodeWith(r.t, r.files, env, ln, nil, func(d *pairDeps) {
		d.box = func([]string) (string, error) {
			b := box.Load().(string)
			if b == "" {
				return "", nil
			}
			return node.BoxFingerprint(b, []string{"02:00:00:00:00:" + b[len(b)-1:]}), nil
		}
		d.dial = r.links.dialer(id)
	})
}

func (r *boxRig) el(id string) *node.Elector { return r.nodes[id].m.q.Node() }

// clone starts a third box running as a, with a copy of A's state file
// and A's env, as a cloned disk or VM would.
func (r *boxRig) clone(box string) *node.Elector {
	r.t.Helper()
	dir := r.t.TempDir()
	b, err := os.ReadFile(filepath.Join(r.env["a"]["STATE_DIRECTORY"], pairStateFile))
	if err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pairStateFile), b, 0o600); err != nil {
		r.t.Fatal(err)
	}
	env := maps.Clone(r.env["a"])
	env["STATE_DIRECTORY"] = dir
	r.start("clone", box, env, r.lns[2])
	return r.el("clone")
}

// cloneCase runs the #1029 sequence after cut: A leads, its link to one
// voter is cut, that voter's promise to A lapses, and a clone of A starts
// that reaches every voter. Never two leaders, and the clone reports the
// box change and stays uncommissioned (box_unchecked turns it red).
func cloneCase(t *testing.T, cut func(r *boxRig)) {
	r := newBoxRig(t)
	cut(r)
	time.Sleep(2 * time.Second) // four leases: the cut voter's promise to A has run out
	if !r.el("a").IsLeader() {
		t.Fatal("A stopped leading after a single cut")
	}
	c := r.clone("box-c")
	led := watchOneLeader(t, 10*time.Second, map[string]*node.Elector{"a": r.el("a"), "b": r.el("b"), "clone": c})
	if led["clone"] {
		t.Fatal("the clone led")
	}
	if s := c.Status(); s.Commissioned || !strings.Contains(s.BoxChanged, "COPY") {
		t.Fatalf("the clone: commissioned %v, box changed %q; want uncommissioned and the report", s.Commissioned, s.BoxChanged)
	}
}

// Shape 1 (the #1028 re-review's): the A–B link down both ways; A leads on
// {A, W}; the clone reaches B.
func TestCloneWithTheABLinkDown(t *testing.T) {
	cloneCase(t, func(r *boxRig) {
		r.links.cutLink("a", r.lns[1].Addr().String())
		r.links.cutLink("b", r.lns[0].Addr().String())
	})
}

// Shape 2: A cut from the witness; A leads on {A, B}; the clone reaches W.
func TestCloneWithACutFromTheWitness(t *testing.T) {
	cloneCase(t, func(r *boxRig) { r.links.cutLink("a", r.wit) })
}

// One controller's box changes (its NIC replaced, or it migrated): it
// starts uncommissioned, reports it, and re-adopts the pair's witness
// incarnation from its healthy peer on its own, with no operator.
func TestOneBoxChangeReadoptsFromThePeer(t *testing.T) {
	r := newBoxRig(t)
	inc := r.el("a").Status().Incarnation
	r.nodes["b"].crash()
	ln, err := net.Listen("tcp", r.lns[1].Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	r.start("b", "box-B", r.env["b"], ln) // the same disk, another NIC
	eb := r.el("b")
	if s := eb.Status(); s.Commissioned || !strings.Contains(s.BoxChanged, "peer-link MAC changed") {
		t.Fatalf("after the NIC change: commissioned %v, box changed %q", s.Commissioned, s.BoxChanged)
	}
	pairWait(t, "b to re-adopt from a", 20*time.Second, func() bool {
		s := eb.Status()
		return s.Commissioned && s.Incarnation == inc
	})
	if !r.el("a").IsLeader() {
		t.Error("the leader lost its lease while its peer re-adopted")
	}
}

// hostBox on this host: stable across calls, and "" (skip, never guess)
// when a link's address doesn't resolve.
func TestHostBox(t *testing.T) {
	if _, err := os.Stat("/etc/machine-id"); err != nil {
		t.Skip("no /etc/machine-id here")
	}
	a, _ := hostBox([]string{"127.0.0.1:9450"})
	b, _ := hostBox([]string{"127.0.0.1:9450"})
	if a == "" || a != b {
		t.Fatalf("hostBox %q then %q; want one stable fingerprint", a, b)
	}
	if got, err := hostBox([]string{"no-such-host.invalid:9450"}); got != "" || err != nil {
		t.Fatalf("an address that doesn't resolve gave %q, %v; want \"\" (not readable yet: wait)", got, err)
	}
	old := machineIDFiles
	t.Cleanup(func() { machineIDFiles = old })
	machineIDFiles = []string{filepath.Join(t.TempDir(), "none")}
	if _, err := hostBox([]string{"127.0.0.1:9450"}); err == nil || !strings.Contains(err.Error(), "machine-id") {
		t.Fatalf("no machine-id: %v; want the start refused, naming it", err)
	}
}

// A clone booted where its links don't resolve (an isolated network, a DNS
// outage) can't read its box. It must not vote on the identity it carries
// (#1034 review): it waits, neither voting nor campaigning, so the pair
// never has two leaders in shape 1; once its box can be read, it is
// another box, so the copied state is treated as lost (box_wait_skipped
// turns this red).
func TestCloneWithAnUnresolvableLinkNeverVotes(t *testing.T) {
	r := newBoxRig(t)
	r.links.cutLink("a", r.lns[1].Addr().String())
	r.links.cutLink("b", r.lns[0].Addr().String())
	time.Sleep(2 * time.Second) // B's promise to A has run out
	dir := t.TempDir()
	b, err := os.ReadFile(filepath.Join(r.env["a"]["STATE_DIRECTORY"], pairStateFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pairStateFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	env := maps.Clone(r.env["a"])
	env["STATE_DIRECTORY"] = dir
	var box atomic.Value
	box.Store("")
	r.startBox("clone", &box, env, r.lns[2])
	c := r.el("clone")
	els := map[string]*node.Elector{"a": r.el("a"), "b": r.el("b"), "clone": c}
	if led := watchOneLeader(t, 5*time.Second, els); led["clone"] {
		t.Fatal("the clone led while its box couldn't be read")
	}
	if !c.Status().BoxPending {
		t.Fatal("the clone isn't held while its box can't be read")
	}
	box.Store("box-c") // its links resolve: it is another box
	pairWait(t, "the clone to read its box", 5*time.Second, func() bool { return !c.Status().BoxPending })
	if led := watchOneLeader(t, 5*time.Second, els); led["clone"] {
		t.Fatal("the clone led once its box could be read")
	}
	if s := c.Status(); s.Commissioned || !strings.Contains(s.BoxChanged, "COPY") {
		t.Fatalf("the clone: commissioned %v, box changed %q", s.Commissioned, s.BoxChanged)
	}
}
