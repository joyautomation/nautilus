package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
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
	// wdir, wtoken, wstop, wdone: the witness's state, token and run, so
	// restartWitness can start it again on the same address and state.
	wdir, wtoken string
	wstop        func()
	wdone        chan int
	nodes        map[string]*pairNode
	links        *links
	// apis, when set, are the controllers' tag API listeners (newBoxRigAPI),
	// served by each one's first start; apiURL their base URLs.
	apis   map[string]net.Listener
	apiURL map[string]string
}

func newBoxRig(t *testing.T) *boxRig { t.Helper(); return newBoxRigOpts(t, false) }

// newBoxRigAPI is newBoxRig with each controller's tag API served (with
// NAUTILUS_TOKEN "api-secret"), for the operator commands.
func newBoxRigAPI(t *testing.T) *boxRig { t.Helper(); return newBoxRigOpts(t, true) }

func newBoxRigOpts(t *testing.T, api bool) *boxRig {
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
	r.wdir, r.wtoken = t.TempDir(), token
	r.wit = r.startWitness("127.0.0.1:0")
	t.Cleanup(func() { r.wstop(); <-r.wdone })
	r.files = pairProject(r.lns[0].Addr().String(), r.lns[1].Addr().String(), r.wit)
	for _, n := range []string{"a", "b"} {
		r.env[n] = map[string]string{"NAUTILUS_NODE": n, "NAUTILUS_PEER_TOKEN": token, "NAUTILUS_CLUSTER": cluster, "STATE_DIRECTORY": t.TempDir()}
		if api {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			if r.apis == nil {
				r.apis, r.apiURL = map[string]net.Listener{}, map[string]string{}
			}
			r.apis[n], r.apiURL[n] = ln, "http://"+ln.Addr().String()
			r.env[n]["NAUTILUS_TOKEN"] = "api-secret"
		}
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
	r.handLeadTo("a")
	return r
}

// handLeadTo makes member lead: if the other controller leads, its
// health is turned off until member leads (it stops renewing, and its
// operator log keeps serving, so member takes over synced), then on again
// (it stays the standby; its runtime's own liveness is not watched after).
// Never by Release: a released controller never campaigns again (it is
// shutting down), so a test that later needs it to take over would hang
// (#1094's CI failure: B had won the first election and was released, and
// once A was duplicated nobody could lead). Nor by a restart: the new
// leader can't sync its log with the old one down, and yields to it once
// it is back (#710 3b-4b), so who leads after is not the test's to say.
func (r *boxRig) handLeadTo(member string) {
	r.t.Helper()
	other := map[string]string{"a": "b", "b": "a"}[member]
	if r.el(other).IsLeader() {
		r.el(other).SetHealth(func() bool { return false })
		pairWait(r.t, member+" to lead", 15*time.Second, func() bool { return r.el(member).IsLeader() })
		r.el(other).SetHealth(func() bool { return true })
	}
	pairWait(r.t, member+" to lead", 15*time.Second, func() bool { return r.el(member).IsLeader() })
}

// startWitness runs the witness on addr with the rig's state, and returns
// the address it listens on.
func (r *boxRig) startWitness(addr string) string {
	wctx, wstop := context.WithCancel(context.Background())
	wready, wdone := make(chan string, 1), make(chan int, 1)
	go func() {
		wdone <- witnessMain(wctx, []string{"--listen", addr, "--state", r.wdir, "--lease", "500ms"},
			pairEnv(map[string]string{"NAUTILUS_PEER_TOKEN": r.wtoken}), io.Discard, io.Discard, wready)
	}()
	r.wstop, r.wdone = wstop, wdone
	select {
	case a := <-wready:
		return a
	case code := <-wdone:
		r.t.Fatalf("the witness exited with %d", code)
		return ""
	}
}

// clearDuplicate runs the operator's clear-duplicate (#1029 3c-3a) for
// name from whichever controller reaches both other members (a cut may
// leave one that can't). Marks are durable (#1029 3c-3b): no restart
// clears them. It reports whether a clear completed.
func (r *boxRig) clearDuplicate(name string) bool {
	r.t.Helper()
	for _, id := range []string{"a", "b"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		rep, err := r.el(id).ClearDuplicateAll(ctx, name, "user:test")
		cancel()
		if err == nil {
			return true
		}
		r.t.Logf("clear-duplicate %s from %s: %v (refused %q)", name, id, err, rep.Refused)
	}
	return false
}

// restartWitness stops the witness and starts it again on its address
// and state (its marks are kept, #1029 3c-3b).
func (r *boxRig) restartWitness() {
	r.t.Helper()
	r.wstop()
	<-r.wdone
	r.startWitness(r.wit)
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
	api := r.apis[id]
	delete(r.apis, id) // served once: a restarted member has no API here
	r.nodes[id] = startPairNodeWith(r.t, r.files, env, ln, api, func(d *pairDeps) {
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

// cloneDisk starts a third box running as member, with a copy of its
// whole state directory (the voter state, the operator log and its mark)
// and its env, as a cloned disk or VM would. On the member's own box it is
// a MAC-spoofed clone: the box fingerprint matches, so the copied voter
// state loads as its own (#1029 layer 2).
func (r *boxRig) cloneDisk(member, box string) *node.Elector {
	r.t.Helper()
	dir := r.t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(r.env[member]["STATE_DIRECTORY"])); err != nil {
		r.t.Fatal(err)
	}
	env := maps.Clone(r.env[member])
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

// spoofedCloneCase is cloneCase for a MAC-spoofed clone of member (its box
// fingerprint matches, so layer 1 can't see it): the voters relay their
// running promises (#1029 layer 2), and the clone never leads while the
// voter it needs and the one that knows better reach each other; a real
// controller leads. raised says where redundancy.duplicate-member shows:
// logs is everything logged meanwhile (the witness logs it).
func spoofedCloneCase(t *testing.T, member string, cut func(r *boxRig), raised func(r *boxRig, logs string) bool) {
	logs := &lockedBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	r := newBoxRig(t)
	cut(r)
	time.Sleep(2 * time.Second) // four leases: the cut voter's promise to A has run out
	if !r.el("a").IsLeader() {
		t.Fatal("A stopped leading after a single cut")
	}
	box := map[string]string{"a": "box-a", "b": "box-b"}[member]
	c := r.cloneDisk(member, box)
	els := map[string]*node.Elector{"a": r.el("a"), "b": r.el("b"), "clone": c}
	led := watchOneLeader(t, 10*time.Second, els)
	if led["clone"] {
		t.Fatal("the clone led")
	}
	// A duplicated controller's vote backs no lease (#1029 step 1), so a
	// clone plus a cut may leave the pair without a leader (fail-safe:
	// none beats two). A voter that heard both copies running marks the
	// name for good (#1029 3c): stopping the clone is no longer enough
	// while the cut stands. Stop the copy, then the operator's clear
	// (#1029 3c-3a), once the copy's boot is out of every member's last
	// lease and D: no restart, and none clears a mark (#1029 3c-3b).
	r.nodes["clone"].crash()
	time.Sleep(2 * time.Second)
	r.clearDuplicate(member)
	pairWait(t, "a real controller to lead once the clone is stopped and the marks cleared", 15*time.Second, func() bool { return r.el("a").IsLeader() || r.el("b").IsLeader() })
	if !raised(r, logs.String()) {
		t.Fatalf("redundancy.duplicate-member not raised where it should be; logs:\n%s", logs.String())
	}
}

// Shape 1, MAC-spoofed: the A–B link down; A leads on {A, W}; a clone of A
// reaches B, and not W. W relays its promise to A in its replies to B's
// probes, so B refuses the clone (rule (a)): B hears that promise from
// nowhere else. The clone is cut from W too, so no voter hears both copies
// and nothing is marked (#1029 3c): the relay alone keeps the clone out
// (reply_relay_dropped turns it red), and B raises the alarm.
func TestSpoofedCloneOfTheLeaderWithTheABLinkDown(t *testing.T) {
	spoofedCloneCase(t, "a", func(r *boxRig) {
		r.links.cutLink("a", r.lns[1].Addr().String())
		r.links.cutLink("b", r.lns[0].Addr().String())
		r.links.cutLink("clone", r.wit)
	}, func(r *boxRig, _ string) bool { return r.el("b").Status().Duplicates > 0 })
}

// Shape 2, MAC-spoofed: A cut from W; A leads on {A, B}; a clone of A
// reaches W. B relays its promise to A on its probes of W, so W refuses
// the clone (rule (a)).
func TestSpoofedCloneOfTheLeaderCutFromTheWitness(t *testing.T) {
	spoofedCloneCase(t, "a", func(r *boxRig) { r.links.cutLink("a", r.wit) }, func(_ *boxRig, logs string) bool {
		// B hears both copies of a running and marks a (#1029 3c).
		return strings.Contains(logs, "redundancy: two processes claim to be a: this voter heard both running at once")
	})
}

// Shape 3, MAC-spoofed: A cut from W; A leads on {A, B}; a clone of the
// STANDBY B reaches W as b. Its copied voter grants it, and W's promise to
// A has lapsed, so W alone would grant it too. B's probes of W relay that
// b's own voter is promised to a: a real b never campaigns then, so W
// refuses it (rule (b) of the layer 2 plan).
func TestSpoofedCloneOfTheStandbyWithTheLeaderCutFromTheWitness(t *testing.T) {
	spoofedCloneCase(t, "b", func(r *boxRig) { r.links.cutLink("a", r.wit) }, func(_ *boxRig, logs string) bool {
		// W hears both copies of b running (B probes it) and marks b
		// (#1029 3c); rule (b) still refuses the clone's campaign at
		// first, but the told clone soon campaigns no more.
		return strings.Contains(logs, "witness: two processes claim to be b: this voter heard both running at once")
	})
}

// #1029 step 1, end to end: a MAC-spoofed clone running while the lease
// changes hands. Before step 1 each of these could lead twice (the
// residual #1079 stated). Now a name heard under two live boots gets no
// new grant and a duplicated voter backs no lease: the clone never leads,
// never beside another leader, and a real controller leads. A running
// clone costs redundancy, not the leader.
func cloneHandover(t *testing.T, member string, handover func(r *boxRig)) {
	r := newBoxRig(t)
	box := map[string]string{"a": "box-a", "b": "box-b"}[member]
	r.cloneDisk(member, box)
	time.Sleep(2 * time.Second) // the clone is past its restart hold, refused
	handover(r)
	els := map[string]*node.Elector{"a": r.el("a"), "b": r.el("b"), "clone": r.el("clone")}
	if led := watchOneLeader(t, 10*time.Second, els); led["clone"] {
		t.Fatal("the clone led")
	}
	pairWait(t, "a real controller to lead", 10*time.Second, func() bool { return r.el("a").IsLeader() || r.el("b").IsLeader() })
}

// deployRestart restarts member the way naut deploy's update does: the
// unit is stopped (runProject's way out: the election's Run ends, then
// Release, then the member closes), and only then started again, at
// once, on its own box, address and state.
func (r *boxRig) deployRestart(member string) {
	r.t.Helper()
	i := map[string]int{"a": 0, "b": 1}[member]
	r.nodes[member].stop()
	ln, err := net.Listen("tcp", r.lns[i].Addr().String())
	if err != nil {
		r.t.Fatal(err)
	}
	r.start(member, "box-"+member, r.env[member], ln)
}

// restart stops member and starts it again on its own box and address.
func (r *boxRig) restart(member string) {
	r.t.Helper()
	i := map[string]int{"a": 0, "b": 1}[member]
	r.nodes[member].crash()
	ln, err := net.Listen("tcp", r.lns[i].Addr().String())
	if err != nil {
		r.t.Fatal(err)
	}
	r.start(member, "box-"+member, r.env[member], ln)
}

func TestSpoofedCloneWhileTheOriginalRestarts(t *testing.T) {
	cloneHandover(t, "a", func(r *boxRig) { r.restart("a") })
}

func TestSpoofedCloneWhileItsPeerRestarts(t *testing.T) {
	cloneHandover(t, "a", func(r *boxRig) { r.restart("b") })
}

func TestSpoofedCloneWhileTheLeaderGoesUnhealthy(t *testing.T) {
	cloneHandover(t, "a", func(r *boxRig) { r.el("a").SetHealth(func() bool { return false }) })
}

// A power cycle of both controllers with a clone of A booting together
// with them: no promise is held anywhere yet.
func TestSpoofedCloneBootsWithItsOriginal(t *testing.T) {
	r := newBoxRig(t)
	r.nodes["a"].crash()
	r.nodes["b"].crash()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(r.env["a"]["STATE_DIRECTORY"])); err != nil {
		t.Fatal(err)
	}
	env := maps.Clone(r.env["a"])
	env["STATE_DIRECTORY"] = dir
	for i, m := range []string{"a", "b"} {
		ln, err := net.Listen("tcp", r.lns[i].Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		r.start(m, "box-"+m, r.env[m], ln)
	}
	r.start("clone", "box-a", env, r.lns[2])
	els := map[string]*node.Elector{"a": r.el("a"), "b": r.el("b"), "clone": r.el("clone")}
	if led := watchOneLeader(t, 10*time.Second, els); led["clone"] {
		t.Fatal("the clone led")
	}
	pairWait(t, "a real controller to lead", 10*time.Second, func() bool { return r.el("a").IsLeader() || r.el("b").IsLeader() })
}

// A MAC-spoofed clone of the healthy leader, every link up: the leader
// keeps its lease (its running promises still renew), and the clone never
// leads: a clone costs redundancy, never a takeover of a healthy leader
// (#1029 step 1). Under CI load a leader can miss its renewals for a lease
// and lapse, the clone or not (it flaked on main that way, #1108); B then
// takes over, which is the pair working. So the test fails on the clone
// leading, or on two leading at once, and only logs a takeover by B.
func TestSpoofedCloneOfAHealthyLeaderCostsNoTakeover(t *testing.T) {
	r := newBoxRig(t)
	c := r.cloneDisk("a", "box-a")
	els := map[string]*node.Elector{"a": r.el("a"), "b": r.el("b"), "clone": c}
	led := watchOneLeader(t, 10*time.Second, els)
	if led["clone"] {
		t.Fatalf("the clone led: %v", led)
	}
	if led["b"] {
		t.Logf("A's lease lapsed (CI load) and B took over, never both at once and never the clone: %v", led)
	}
}

// The rig's hand-over leaves both controllers able to lead: the lead goes
// to B and back to A, then A stops, and B takes over (a Release-based
// hand-over left B released for good, and it never would).
func TestBoxRigHandOverLeavesBothAbleToLead(t *testing.T) {
	r := newBoxRig(t)
	r.handLeadTo("b")
	r.handLeadTo("a")
	r.nodes["a"].crash()
	pairWait(t, "b to take over", 10*time.Second, func() bool { return r.el("b").IsLeader() })
}

// #1029 3c: a MAC-spoofed clone of the leader runs beside it, every link
// up. The peer, hearing both copies of a running at once, marks a: the
// alarm says so, the clone never leads, and the mark stands after the
// clone is stopped (no lease of silence ends it: a split looks just the
// same), also across the witness's restart (#1029 3c-3b). The operator's
// clear gives a its grants back: it can lead again, with no restart.
func TestSpoofedCloneBesideTheOriginalIsMarkedUntilCleared(t *testing.T) {
	logs := &lockedBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	r := newBoxRig(t)
	c := r.cloneDisk("a", "box-a")
	pairWait(t, "b to mark a", 10*time.Second, func() bool { return slices.Contains(r.el("b").DuplicateMarks(), "a") })
	els := map[string]*node.Elector{"a": r.el("a"), "b": r.el("b"), "clone": c}
	if led := watchOneLeader(t, 3*time.Second, els); led["clone"] {
		t.Fatal("the clone led")
	}
	if !strings.Contains(logs.String(), "two processes claim to be a: this voter heard both running at once") {
		t.Fatalf("no confirmed duplicate-member alarm; logs:\n%s", logs.String())
	}
	r.nodes["clone"].crash()
	time.Sleep(2 * time.Second) // four leases with the clone gone
	if !slices.Contains(r.el("b").DuplicateMarks(), "a") {
		t.Fatal("b's mark on a ended without a clear once the clone fell silent")
	}
	r.restartWitness()
	time.Sleep(2 * time.Second) // the witness's voter settled (Voter.Settled)
	if !r.clearDuplicate("a") {
		t.Fatal("the clear didn't complete")
	}
	// A leader first, then the lead handed to a: handLeadTo hands over only
	// from a controller leading.
	pairWait(t, "a leader after the clear", 15*time.Second, func() bool { return r.el("a").IsLeader() || r.el("b").IsLeader() })
	r.handLeadTo("a")
	if m := r.el("b").DuplicateMarks(); len(m) != 0 {
		t.Fatalf("b marks %v after the clear with the clone gone", m)
	}
}

// naut deploy's update and rollback restart a controller through its unit
// (stop, then start): the old process is gone before the new one starts,
// so a voter never hears the old boot send after the new one's first
// frame and marks nothing (#1029 3c). Restart each controller several
// times that way, and the witness between: no duplicate-member alarm, no
// mark, no spare, a leader each time. Run under load too (taskset -c 0,1 with CPU
// hogs), as the PR says.
func TestDeployRestartsMarkNothing(t *testing.T) {
	logs := &lockedBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	r := newBoxRig(t)
	for i := range 6 {
		m := []string{"a", "b"}[i%2]
		r.deployRestart(m)
		pairWait(t, "a leader after "+m+"'s restart", 15*time.Second, func() bool { return r.el("a").IsLeader() || r.el("b").IsLeader() })
		if i == 2 {
			r.restartWitness()
		}
		time.Sleep(time.Second) // two leases: late frames, probes, renewals
	}
	for _, m := range []string{"a", "b"} {
		if marks := r.el(m).DuplicateMarks(); len(marks) != 0 {
			t.Fatalf("%s marks %v after deploy restarts", m, marks)
		}
		if r.el(m).Spare() {
			t.Fatalf("%s went spare after deploy restarts", m) // #1029 3c-2
		}
	}
	if l := logs.String(); strings.Contains(l, "two processes claim") || strings.Contains(l, "confirmed duplicate") {
		t.Fatalf("a duplicate alarm after deploy restarts; logs:\n%s", l)
	}
}

// The standby probes its PEER every look, not only the witness (#1029
// step 1), end to end through the driver (Fable on #1094): a MAC-spoofed
// clone of the standby B runs while the real B is cut from W, so only A
// can hear both copies of b, and A hears the real B only through those
// probes (a standby sends nothing else). A marks b (#1029 3c).
// standby_probes_witness_only turns it red.
func TestTheStandbysPeerProbeLetsItsPeerSeeAClone(t *testing.T) {
	r := newBoxRig(t)
	r.links.cutLink("b", r.wit)
	time.Sleep(time.Second) // two leases: A leads on, B stays the standby
	if !r.el("a").IsLeader() {
		t.Fatal("A stopped leading after B's cut from the witness")
	}
	r.cloneDisk("b", "box-b")
	pairWait(t, "A to mark b", 10*time.Second, func() bool { return slices.Contains(r.el("a").DuplicateMarks(), "b") })
}

// cloneDiskEnv is cloneDisk returning the clone's env too, so a test can
// restart it on its own state.
func (r *boxRig) cloneDiskEnv(member, box string) map[string]string {
	r.t.Helper()
	dir := r.t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(r.env[member]["STATE_DIRECTORY"])); err != nil {
		r.t.Fatal(err)
	}
	env := maps.Clone(r.env[member])
	env["STATE_DIRECTORY"] = dir
	r.start("clone", box, env, r.lns[2])
	return env
}

// restartClone stops the clone and starts it again on its box, address
// and state.
func (r *boxRig) restartClone(box string, env map[string]string) {
	r.t.Helper()
	r.nodes["clone"].crash()
	ln, err := net.Listen("tcp", r.lns[2].Addr().String())
	if err != nil {
		r.t.Fatal(err)
	}
	r.start("clone", box, env, ln)
}

// hasWarning reports whether member's /api/cluster warnings hold one
// containing s.
func (r *boxRig) hasWarning(member, s string) bool {
	return slices.ContainsFunc(r.nodes[member].m.q.Status().Warnings, func(w string) bool { return strings.Contains(w, s) })
}

// #1029 3c-2, end to end: a MAC-spoofed clone of the leader runs beside
// it, every link up. Both other members hear both copies of a running,
// mark a, and hold the original (heard first) as a: the clone demotes
// itself to a spare, writes that down, and goes silent. It never leads,
// /api/cluster on it says "duplicate of a - spare", the original keeps
// leading, and the spare stays one across its own restart (the flag is
// read before the box check). The original's lease never broke.
func TestASpoofedCloneBesideTheLeaderBecomesASpare(t *testing.T) {
	r := newBoxRig(t)
	env := r.cloneDiskEnv("a", "box-a")
	els := map[string]*node.Elector{"a": r.el("a"), "b": r.el("b"), "clone": r.el("clone")}
	pairWait(t, "the clone to go spare", 10*time.Second, func() bool { return r.el("clone").Spare() })
	if led := watchOneLeader(t, 2*time.Second, els); led["clone"] || led["b"] {
		t.Fatalf("led: %v; want a alone", led)
	}
	if !r.hasWarning("clone", "duplicate of a - spare") {
		t.Fatalf("the spare's /api/cluster warnings: %v", r.nodes["clone"].m.q.Status().Warnings)
	}
	if r.el("a").Spare() || r.el("b").Spare() {
		t.Fatal("a real controller went spare")
	}
	r.restartClone("box-a", env)
	if !r.el("clone").Spare() {
		t.Fatal("the spare forgot it is one across its restart")
	}
	els["clone"] = r.el("clone")
	if led := watchOneLeader(t, 2*time.Second, els); led["clone"] {
		t.Fatalf("the restarted spare led: %v", led)
	}
}

// A spare's peer listener answers every request 503, the vote protocol
// and the operator-intent log's replication alike, as a stopped
// controller would; a member's passes through (#1029 3c-2).
// spare_log_runs turns it red.
func TestASparesPeerListenerAnswersNobody(t *testing.T) {
	var spare atomic.Bool
	h := spareSilent(spare.Load, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, p := range []string{"/api/oplog/append", "/api/quorum", node.Prefix + "x/vote"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, p, nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("a member's %s: %d", p, rec.Code)
		}
		spare.Store(true)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, p, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("a spare's %s: %d, want 503", p, rec.Code)
		}
		spare.Store(false)
	}
}

// Once the clone is a spare, its original restarting (naut deploy's way)
// stays a member: the silent spare is no fresh boot at the voters, so
// nobody names it over the original's new boot, and the pair keeps a
// leader. (Before the #1133 review's rule, a spare that still probed,
// spare_still_looks, demoted the original too; now the voters tell a boot
// first heard after the mark nothing, and TestASpareSendsNothing pins the
// silence at the driver.)
func TestAnOriginalRestartingBesideItsSpareStaysAMember(t *testing.T) {
	r := newBoxRig(t)
	r.cloneDiskEnv("a", "box-a")
	pairWait(t, "the clone to go spare", 10*time.Second, func() bool { return r.el("clone").Spare() })
	r.deployRestart("a")
	time.Sleep(3 * time.Second) // six leases: past a decision's span
	if r.el("a").Spare() {
		t.Fatal("the original went spare beside its silent spare")
	}
	pairWait(t, "a leader", 10*time.Second, func() bool { return r.el("a").IsLeader() || r.el("b").IsLeader() })
}
