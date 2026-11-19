package driverconformance

import (
	"net"
	"sync"
	"sync/atomic"
)

// udpLink is the link for a driver on UDP (snmp): a relay between it and
// its fake. UDP has no connection to close or hold, so a cut link and a
// frozen one both swallow datagrams, which is what a driver sees of an
// unplugged device and of a hung one. Cut counts what it swallows as
// refused, frozen as held.
type udpLink struct {
	ln     *net.UDPConn
	target *net.UDPAddr

	mu       sync.Mutex
	down     bool
	frozen   bool
	ups      map[string]*net.UDPConn // client address → its socket to the fake
	refusals atomic.Int64
	holds    atomic.Int64
}

var _ link = (*udpLink)(nil)

// newUDPLink relays a UDP driver to target on 127.0.0.1; cut starts it
// down.
func newUDPLink(t tb, target string, cut bool) *udpLink {
	ta, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	ln, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	l := &udpLink{ln: ln, target: ta, down: cut, ups: map[string]*net.UDPConn{}}
	go l.serve()
	t.Cleanup(func() {
		ln.Close()
		l.mu.Lock()
		for _, u := range l.ups {
			u.Close()
		}
		l.mu.Unlock()
	})
	return l
}

// port is where the driver sends, on 127.0.0.1.
func (l *udpLink) port() int { return l.ln.LocalAddr().(*net.UDPAddr).Port }

func (l *udpLink) refused() int64 { return l.refusals.Load() }
func (l *udpLink) held() int64    { return l.holds.Load() }

// serve forwards each datagram from a client to the fake on that client's
// own socket, and the fake's answers back, unless the link swallows them.
func (l *udpLink) serve() {
	buf := make([]byte, 64<<10)
	for {
		n, from, err := l.ln.ReadFromUDP(buf)
		if err != nil {
			return
		}
		l.mu.Lock()
		down, frozen := l.down, l.frozen
		up := l.ups[from.String()]
		if up == nil && !down && !frozen {
			up, err = net.DialUDP("udp", nil, l.target)
			if err != nil {
				l.mu.Unlock()
				continue
			}
			l.ups[from.String()] = up
			go l.back(up, from)
		}
		l.mu.Unlock()
		switch {
		case down:
			l.refusals.Add(1)
		case frozen:
			l.holds.Add(1)
		default:
			_, _ = up.Write(buf[:n])
		}
	}
}

// back returns the fake's answers to client, swallowing them while the
// link is cut or frozen.
func (l *udpLink) back(up *net.UDPConn, client *net.UDPAddr) {
	buf := make([]byte, 64<<10)
	for {
		n, err := up.Read(buf)
		if err != nil {
			return
		}
		l.mu.Lock()
		pass := !l.down && !l.frozen
		l.mu.Unlock()
		if pass {
			_, _ = l.ln.WriteToUDP(buf[:n], client)
		}
	}
}

func (l *udpLink) cut()    { l.mu.Lock(); l.down = true; l.mu.Unlock() }
func (l *udpLink) heal()   { l.mu.Lock(); l.down = false; l.mu.Unlock() }
func (l *udpLink) freeze() { l.mu.Lock(); l.frozen = true; l.mu.Unlock() }
func (l *udpLink) thaw()   { l.mu.Lock(); l.frozen = false; l.mu.Unlock() }
