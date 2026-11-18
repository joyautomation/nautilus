package driverconformance

import (
	"net"
	"sync"
	"sync/atomic"
)

// tcpLink is the link for a TCP driver: a relay between it and its fake
// that can cut every connection (and refuse new ones), freeze them (keep
// them open but forward nothing: a hung device), or heal and thaw again.
type tcpLink struct {
	ln     net.Listener
	target string

	mu       sync.Mutex
	down     bool
	frozen   bool
	conns    map[net.Conn]bool
	thawed   chan struct{}
	refusals atomic.Int64
	holds    atomic.Int64
}

var _ link = (*tcpLink)(nil)

// newTCPLink relays to target; cut starts it down. Its own teardown thaws
// and cuts it, so nothing stays parked in a relay.
func newTCPLink(t tb, target string, cut bool) *tcpLink {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	l := &tcpLink{ln: ln, target: target, down: cut, conns: map[net.Conn]bool{}}
	go l.accept()
	t.Cleanup(func() {
		ln.Close()
		l.thaw()
		l.cut()
	})
	return l
}

// port is where the driver dials, on 127.0.0.1.
func (l *tcpLink) port() int { return l.ln.Addr().(*net.TCPAddr).Port }

func (l *tcpLink) refused() int64 { return l.refusals.Load() }
func (l *tcpLink) held() int64    { return l.holds.Load() }

func (l *tcpLink) accept() {
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

func (l *tcpLink) relay(c net.Conn) {
	u, err := net.Dial("tcp", l.target)
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
func (l *tcpLink) pipe(dst, src net.Conn) {
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

func (l *tcpLink) cut() {
	l.mu.Lock()
	l.down = true
	for c := range l.conns {
		c.Close()
	}
	l.conns = map[net.Conn]bool{}
	l.mu.Unlock()
}

func (l *tcpLink) heal() {
	l.mu.Lock()
	l.down = false
	l.mu.Unlock()
}

func (l *tcpLink) freeze() {
	l.mu.Lock()
	if !l.frozen {
		l.frozen = true
		l.thawed = make(chan struct{})
	}
	l.mu.Unlock()
}

func (l *tcpLink) thaw() {
	l.mu.Lock()
	if l.frozen {
		l.frozen = false
		close(l.thawed)
	}
	l.mu.Unlock()
}
