package opcua

import (
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"testing"
)

// freePort gives a test server a port that is free now and that no
// outgoing connection will take before the server binds it: the gopcua
// server does not report the port it bound when asked for :0, so the test
// picks one first. A port from the kernel's ephemeral range (a :0 listen)
// could be handed, between the pick and the bind, to a client's connect as
// its local port, the drivers' own connects to the servers already started
// included: "bind: address already in use" at start, or at a restart on
// the same port (#1120, TestSixteenServersReconnectEachOnItsOwn). So it
// picks below the ephemeral range, checks the port binds, and never hands
// the same port out twice in one process.
func freePort(t testing.TB) int {
	t.Helper()
	lo, hi := freePortRange()
	portsMu.Lock()
	defer portsMu.Unlock()
	for range 200 {
		p := lo + rand.IntN(hi-lo)
		if portsGiven[p] {
			continue
		}
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue // taken (EADDRINUSE): another one
		}
		l.Close()
		portsGiven[p] = true
		return p
	}
	t.Fatalf("no free port in %d-%d", lo, hi)
	return 0
}

// FreePort is freePort for the external test package (the matrix).
func FreePort(t testing.TB) int { t.Helper(); return freePort(t) }

var (
	portsMu    sync.Mutex
	portsGiven = map[int]bool{}
)

// freePortRange is [lo, hi): from 20000 up to the ephemeral range's low
// bound, never below 1024 (no privileged ports).
func freePortRange() (lo, hi int) {
	lo, hi = 20000, 32768 // Linux's default ephemeral low bound
	var a, b int
	if raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range"); err == nil {
		if _, err := fmt.Sscan(string(raw), &a, &b); err == nil && a > 1024 {
			hi = a
		}
	}
	if hi-lo < 1000 {
		lo = max(1024, hi-4000)
	}
	return lo, hi
}

// Test servers' ports are never from the ephemeral range a client's
// connect draws from, and never handed out twice (#1120).
func TestFreePortsAreBelowTheEphemeralRange(t *testing.T) {
	lo, hi := freePortRange()
	if lo < 1024 || hi <= lo {
		t.Fatalf("range %d-%d", lo, hi)
	}
	seen := map[int]bool{}
	for range 50 {
		p := freePort(t)
		if p < lo || p >= hi {
			t.Fatalf("port %d outside %d-%d", p, lo, hi)
		}
		if seen[p] {
			t.Fatalf("port %d handed out twice", p)
		}
		seen[p] = true
	}
}
