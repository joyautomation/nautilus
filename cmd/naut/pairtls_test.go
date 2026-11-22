package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	goruntime "runtime"

	"github.com/joyautomation/nautilus/quorum/node"
)

// Peer TLS in naut run and naut witness (#710): on by default, a token
// too short for it refused with the fix, off only when asked for and then
// said at every start and on /api/cluster.

func warned(ws []string, sub string) bool {
	return slices.ContainsFunc(ws, func(w string) bool { return strings.Contains(w, sub) })
}

// A short peer token is refused at start with peer TLS on, naming the
// command that draws a good one and the lab opt-out; with peer TLS off it
// starts as before.
func TestPairShortTokenRefusedWithPeerTLS(t *testing.T) {
	r := loadPair(t, pairProject("127.0.0.1:9450", "127.0.0.2:9450", "127.0.0.3:9460")).Redundancy
	env := pairGoodEnv(t)
	env["NAUTILUS_PEER_TOKEN"] = "peer-secret"
	_, _, _, err := pairOptions(r, "127.0.0.1:8080", "", pairEnv(env))
	if err == nil || !strings.Contains(err.Error(), "naut redundancy new-pair") || !strings.Contains(err.Error(), "redundancy.peer-tls: off") {
		t.Fatalf("a short token with peer TLS on: %v, want a refusal naming new-pair and the opt-out", err)
	}
	r.PeerTLS = "off"
	if _, _, srv, err := pairOptions(r, "127.0.0.1:8080", "", pairEnv(env)); err != nil || srv != nil {
		t.Fatalf("peer TLS off: %v (TLS %v), want a plain start", err, srv != nil)
	}
	r.PeerTLS = ""
	env["NAUTILUS_PEER_TOKEN"] = testPeerToken
	o, _, srv, err := pairOptions(r, "127.0.0.1:8080", "", pairEnv(env))
	if err != nil || srv == nil || o.TLS == nil {
		t.Fatalf("the default: %v, server TLS %v, client TLS %v: want peer TLS on", err, srv != nil, o.TLS != nil)
	}
}

func TestWitnessPeerTLSFlag(t *testing.T) {
	dir := t.TempDir()
	run := func(token string, args ...string) (int, string) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ready := make(chan string, 1)
		var errb bytes.Buffer
		done := make(chan int, 1)
		go func() {
			done <- witnessMain(ctx, append([]string{"--state", dir, "--listen", "127.0.0.1:0"}, args...),
				witnessEnv(map[string]string{"NAUTILUS_PEER_TOKEN": token}), io.Discard, &errb, ready)
		}()
		select {
		case <-ready:
			cancel()
			return <-done, errb.String()
		case code := <-done:
			return code, errb.String()
		case <-time.After(5 * time.Second):
			t.Fatal("naut witness neither started nor stopped")
		}
		return 0, ""
	}
	if code, msg := run("tok"); code != 1 || !strings.Contains(msg, "naut redundancy new-pair") || !strings.Contains(msg, "--peer-tls=off") {
		t.Fatalf("a short token: exit %d %q, want a refusal naming new-pair and the opt-out", code, msg)
	}
	if code, msg := run("tok", "--peer-tls=off"); code != 0 || !strings.Contains(msg, "peer TLS is off") {
		t.Fatalf("--peer-tls=off: exit %d %q, want a start with the warning", code, msg)
	}
	if code, msg := run(testPeerToken, "--peer-tls=maybe"); code != 2 {
		t.Fatalf("--peer-tls=maybe: exit %d %q, want a usage error", code, msg)
	}
	if code, msg := run(testPeerToken); code != 0 || strings.Contains(msg, "peer TLS is off") {
		t.Fatalf("the default: exit %d %q, want a quiet TLS start", code, msg)
	}
}

// With peer TLS off on all three, the pair runs as before over plain HTTP
// (its log reaches the witness), and both members say it is off.
func TestPairPeerTLSOffRunsPlainAndSaysSo(t *testing.T) {
	r := newPairRigPlain(t, nil, nil, true)
	lead := r.leader()
	pairWait(t, "the witness's copy to open", 10*time.Second, func() bool { return r.witnessHolds(lead) })
	for _, n := range []string{"a", "b"} {
		if !warned(r.nodes[n].m.q.Status().Warnings, "peer TLS is off") {
			t.Errorf("%s does not say peer TLS is off: %q", n, r.nodes[n].m.q.Status().Warnings)
		}
	}
	resp, err := http.Get("http://" + r.peer["a"] + node.Prefix + r.cluster() + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("plain HTTP without the token: %d, want 401 (a plain listener, still behind the token)", resp.StatusCode)
	}
}

// A witness restarted with peer TLS off beside a pair with it on is named
// on /api/cluster by both members, which run on with their two votes; the
// report clears once the witness speaks TLS again. The default pair says
// nothing about peer TLS.
func TestPairPlainWitnessIsNamedOnTheCluster(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	for _, n := range []string{"a", "b"} {
		if ws := r.nodes[n].m.q.Status().Warnings; warned(ws, "peer TLS") || warned(ws, "plain HTTP") {
			t.Fatalf("%s warns about peer TLS by default: %q", n, ws)
		}
	}
	addr := r.wit
	r.stopWitness()
	r.witArgs = []string{"--peer-tls=off"}
	r.startWitness(addr)
	for _, n := range []string{"a", "b"} {
		pairWait(t, n+" to name the plain witness", 10*time.Second, func() bool {
			return warned(r.nodes[n].m.q.Status().Warnings, "the witness answers plain HTTP")
		})
	}
	if r.leader() != lead {
		t.Errorf("the lead moved from %s while the witness spoke plain HTTP", lead)
	}
	r.stopWitness()
	r.witArgs = nil
	r.startWitness(addr)
	for _, n := range []string{"a", "b"} {
		pairWait(t, n+" to clear the report", 10*time.Second, func() bool {
			return !warned(r.nodes[n].m.q.Status().Warnings, "plain HTTP")
		})
	}
}

// naut check flags a pair's peer TLS off, and a peer token in its
// environment that naut run would refuse, before upgrade day (#710).
func TestCheckPeerTLSWarnings(t *testing.T) {
	proj := loadPair(t, pairProject("127.0.0.1:9450", "127.0.0.2:9450", "127.0.0.3:9460"))
	env := func(tok string) func(string) string {
		return func(k string) string {
			if k == "NAUTILUS_PEER_TOKEN" {
				return tok
			}
			return ""
		}
	}
	if w := peerTLSWarnings(proj, env("")); len(w) != 0 {
		t.Fatalf("no token here (a laptop): %q, want nothing", w)
	}
	if w := peerTLSWarnings(proj, env(testPeerToken)); len(w) != 0 {
		t.Fatalf("a good token: %q, want nothing", w)
	}
	if w := peerTLSWarnings(proj, env("peer-secret")); len(w) != 1 || !strings.Contains(w[0], "naut redundancy new-pair") {
		t.Fatalf("a short token: %q, want one warning naming new-pair", w)
	}
	proj.Redundancy.PeerTLS = "off"
	if w := peerTLSWarnings(proj, env("peer-secret")); len(w) != 1 || !strings.Contains(w[0], "peer TLS is off") {
		t.Fatalf("peer TLS off: %q, want the off warning", w)
	}
}

// The peer listener binds the address peers: names for this controller,
// not every interface (naut-lead's demo, #710), or NAUTILUS_PEER_LISTEN
// when the box sets it (NAT, mapped container ports).
func TestPairPeerPortBindsTheNamedAddress(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("a pair member needs CLOCK_BOOTTIME (Linux)")
	}
	r := loadPair(t, pairProject("127.0.0.1:9450", "127.0.0.2:9450", "127.0.0.3:9460")).Redundancy
	for _, c := range []struct{ override, want string }{
		{"", "127.0.0.1:9450"},
		{":19450", ":19450"},
	} {
		env := pairGoodEnv(t)
		env["NAUTILUS_PEER_TOKEN"] = testPeerToken
		env["NAUTILUS_PEER_LISTEN"] = c.override
		var bound string
		m, err := newPairMember(r, "127.0.0.1:8080", "", pairDeps{getenv: pairEnv(env), listen: func(addr string) (net.Listener, error) {
			bound = addr
			return net.Listen("tcp", "127.0.0.1:0")
		}})
		if err != nil {
			t.Fatal(err)
		}
		m.Close()
		if bound != c.want {
			t.Errorf("override %q: the peer listener bound %q, want %q", c.override, bound, c.want)
		}
	}
	env := pairGoodEnv(t)
	env["NAUTILUS_PEER_TOKEN"] = testPeerToken
	env["NAUTILUS_PEER_LISTEN"] = "nonsense"
	if _, _, _, err := pairOptions(r, "127.0.0.1:8080", "", pairEnv(env)); err == nil || !strings.Contains(err.Error(), "NAUTILUS_PEER_LISTEN") {
		t.Fatalf("a malformed override: %v, want it refused", err)
	}
}

// An address that is not this box's yet (a late network at boot, a cable
// out, a name DNS does not answer yet) never stops the start: a refused
// start would spend the unit's start limit and leave the controller down
// for good (#1230 review). The member starts, says so on /api/cluster,
// keeps trying, and serves once the address is up.
func TestPairPeerPortWaitsForItsAddress(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("a pair member needs CLOCK_BOOTTIME (Linux)")
	}
	r := loadPair(t, pairProject("127.0.0.1:9450", "127.0.0.2:9450", "127.0.0.3:9460")).Redundancy
	for _, notYet := range []error{
		&net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", syscall.EADDRNOTAVAIL)},
		&net.OpError{Op: "listen", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "ctl-a", IsNotFound: true}},
	} {
		env := pairGoodEnv(t)
		env["NAUTILUS_PEER_TOKEN"] = testPeerToken
		var tries atomic.Int64
		var up atomic.Bool
		m, err := newPairMember(r, "127.0.0.1:8080", "", pairDeps{getenv: pairEnv(env), bindEvery: 10 * time.Millisecond,
			listen: func(addr string) (net.Listener, error) {
				tries.Add(1)
				if !up.Load() {
					return nil, notYet
				}
				return net.Listen("tcp", "127.0.0.1:0")
			}})
		if err != nil {
			t.Fatalf("%v at start: %v, want the member started", notYet, err)
		}
		if !warned(m.q.Status().Warnings, "redundancy.peers.a must name an address of this box") {
			t.Errorf("%v: no peer-bind condition: %q", notYet, m.q.Status().Warnings)
		}
		pairWait(t, "a few retries", 5*time.Second, func() bool { return tries.Load() >= 3 })
		up.Store(true)
		pairWait(t, "the late bind", 5*time.Second, func() bool { return !warned(m.q.Status().Warnings, "peer port") })
		m.Close()
	}
	// Close ends a bind that never comes up, at once.
	env := pairGoodEnv(t)
	env["NAUTILUS_PEER_TOKEN"] = testPeerToken
	m, err := newPairMember(r, "127.0.0.1:8080", "", pairDeps{getenv: pairEnv(env), bindEvery: 10 * time.Millisecond,
		listen: func(addr string) (net.Listener, error) {
			return nil, &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", syscall.EADDRNOTAVAIL)}
		}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end a bind still waiting for its address")
	}
	// Another error (the port is taken) refuses the start at once.
	tries := 0
	_, err = newPairMember(r, "127.0.0.1:8080", "", pairDeps{getenv: pairEnv(env),
		listen: func(addr string) (net.Listener, error) {
			tries++
			return nil, &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", syscall.EADDRINUSE)}
		}})
	if err == nil || tries != 1 || !strings.Contains(err.Error(), "another controller") {
		t.Fatalf("a port in use: %v after %d tries, want an immediate refusal", err, tries)
	}
}

// A host name in peers: that resolves to loopback on this box (a stock
// /etc/hosts maps the box's own name to 127.0.1.1; systemd-resolved does
// so while no network is up) is never served, since the peer and the
// witness could not reach it, and never refuses the start, since a
// refused start spends the unit's start limit (#1230 re-check): a
// standing condition names the fix, and the member binds once the name
// resolves to a network address. An IP literal is taken as written.
func TestPairPeerPortNamesANameBoundToLoopback(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("a pair member needs CLOCK_BOOTTIME (Linux)")
	}
	r := loadPair(t, pairProject("ctl-a.plant.test:9450", "ctl-b.plant.test:9450", "witness.plant.test:9460")).Redundancy
	env := pairGoodEnv(t)
	env["NAUTILUS_PEER_TOKEN"] = testPeerToken
	var networkUp atomic.Bool
	m, err := newPairMember(r, "0.0.0.0:8080", "", pairDeps{getenv: pairEnv(env), bindEvery: 10 * time.Millisecond,
		listen: func(addr string) (net.Listener, error) {
			ln, err := net.Listen("tcp", "127.0.0.1:0") // what ctl-a.plant.test resolves to here
			if err != nil || !networkUp.Load() {
				return ln, err
			}
			return addrListener{Listener: ln, addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 10), Port: 9450}}, nil
		}})
	if err != nil {
		t.Fatalf("a name bound to loopback refused the start: %v", err)
	}
	defer m.Close()
	ws := m.q.Status().Warnings
	if !warned(ws, "loopback") || !warned(ws, "redundancy.peers.a") {
		t.Fatalf("no standing condition naming loopback and redundancy.peers.a: %q", ws)
	}
	networkUp.Store(true)
	pairWait(t, "the bind once the name resolves to a network address", 5*time.Second, func() bool {
		return !warned(m.q.Status().Warnings, "loopback")
	})
}

// addrListener reports another address than the one it listens on.
type addrListener struct {
	net.Listener
	addr net.Addr
}

func (l addrListener) Addr() net.Addr { return l.addr }
