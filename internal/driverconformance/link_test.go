package driverconformance

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// relayLink is the link for a driver on a stream socket (TCP, or a Unix
// socket): a relay between it and its fake that can cut every connection
// (and refuse new ones), freeze them (keep them open but forward nothing:
// a hung device), or heal and thaw again.
type relayLink struct {
	ln      net.Listener
	network string // the fake's, which the relay dials
	target  string

	mu       sync.Mutex
	down     bool
	frozen   bool
	conns    map[net.Conn]bool
	thawed   chan struct{}
	refusals atomic.Int64
	holds    atomic.Int64
}

var _ link = (*relayLink)(nil)

// newTCPLink relays a TCP driver to target on 127.0.0.1; cut starts it
// down.
func newTCPLink(t tb, target string, cut bool) *relayLink {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	return newRelayLink(t, ln, "tcp", target, cut)
}

// newUnixLink relays a driver that dials a Unix socket to the fake's
// socket at target; cut starts it down. The driver dials path(), in a
// short temp dir of its own (a socket path is limited to about 108 bytes,
// which t.TempDir's test-named path can pass).
func newUnixLink(t tb, target string, cut bool) *relayLink {
	dir, err := os.MkdirTemp("", "conformance")
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "link.sock"))
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	return newRelayLink(t, ln, "unix", target, cut)
}

// newRelayLink relays what ln accepts to target on network. Its own
// teardown thaws and cuts it, so nothing stays parked in a relay.
func newRelayLink(t tb, ln net.Listener, network, target string, cut bool) *relayLink {
	l := &relayLink{ln: ln, network: network, target: target, down: cut, conns: map[net.Conn]bool{}}
	go l.accept()
	t.Cleanup(func() {
		ln.Close()
		l.thaw()
		l.cut()
	})
	return l
}

// port is where a TCP driver dials, on 127.0.0.1.
func (l *relayLink) port() int { return l.ln.Addr().(*net.TCPAddr).Port }

// path is the socket a Unix-socket driver dials.
func (l *relayLink) path() string { return l.ln.Addr().String() }

func (l *relayLink) refused() int64 { return l.refusals.Load() }
func (l *relayLink) held() int64    { return l.holds.Load() }

func (l *relayLink) accept() {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			return
		}
		l.mu.Lock()
		down := l.down
		l.mu.Unlock()
		if down {
			l.refusals.Add(1)
			c.Close()
			continue
		}
		go l.relay(c)
	}
}

func (l *relayLink) relay(c net.Conn) {
	u, err := net.Dial(l.network, l.target)
	if err != nil {
		c.Close()
		return
	}
	l.mu.Lock()
	if l.down {
		l.mu.Unlock()
		c.Close()
		u.Close()
		return
	}
	l.conns[c], l.conns[u] = true, true
	l.mu.Unlock()
	go l.pipe(u, c)
	l.pipe(c, u)
}

// pipe copies src to dst until either closes, holding what it read while
// the link is frozen.
func (l *relayLink) pipe(dst, src net.Conn) {
	defer dst.Close()
	defer src.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			l.mu.Lock()
			frozen, thawed := l.frozen, l.thawed
			l.mu.Unlock()
			if frozen {
				l.holds.Add(1)
				<-thawed
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil { // io.EOF or a closed connection: either way, done
			return
		}
	}
}

func (l *relayLink) cut() {
	l.mu.Lock()
	l.down = true
	for c := range l.conns {
		c.Close()
	}
	l.conns = map[net.Conn]bool{}
	l.mu.Unlock()
}

func (l *relayLink) heal() {
	l.mu.Lock()
	l.down = false
	l.mu.Unlock()
}

func (l *relayLink) freeze() {
	l.mu.Lock()
	if !l.frozen {
		l.frozen = true
		l.thawed = make(chan struct{})
	}
	l.mu.Unlock()
}

func (l *relayLink) thaw() {
	l.mu.Lock()
	if l.frozen {
		l.frozen = false
		close(l.thawed)
	}
	l.mu.Unlock()
}
