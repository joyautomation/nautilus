package driverconformance

import (
	"net"
	"sync"
	"sync/atomic"
)

// link is a TCP relay between a driver and its fake that the suite can
// cut (close every connection and refuse new ones), freeze (keep the
// connections open but forward nothing, a hung device) or leave up. It is
// what lets one contract break the link the same way for every TCP driver.
type link struct {
	ln     net.Listener
	target string

	mu      sync.Mutex
	down    bool
	frozen  bool
	conns   map[net.Conn]bool
	thaw    chan struct{}
	refusal atomic.Int64
}

func newLink(t tb, target string, cut bool) *link {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	l := &link{ln: ln, target: target, down: cut, conns: map[net.Conn]bool{}, thaw: make(chan struct{})}
	go l.accept()
	t.Cleanup(func() {
		ln.Close()
		l.cut()
		l.mu.Lock()
		if l.frozen {
			l.frozen = false
			close(l.thaw)
		}
		l.mu.Unlock()
	})
	return l
}

// port is where the driver dials, on 127.0.0.1.
func (l *link) port() int { return l.ln.Addr().(*net.TCPAddr).Port }

// refused counts the dials refused while the link was cut.
func (l *link) refused() int64 { return l.refusal.Load() }

func (l *link) accept() {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			return
		}
		l.mu.Lock()
		down := l.down
		l.mu.Unlock()
		if down {
			l.refusal.Add(1)
			c.Close()
			continue
		}
		go l.relay(c)
	}
}

func (l *link) relay(c net.Conn) {
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

// pipe copies src to dst until either closes, holding while frozen.
func (l *link) pipe(dst, src net.Conn) {
	defer dst.Close()
	defer src.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			l.mu.Lock()
			thaw := l.thaw
			frozen := l.frozen
			l.mu.Unlock()
			if frozen {
				<-thaw
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

// cut closes every relayed connection and refuses new ones.
func (l *link) cut() {
	l.mu.Lock()
	l.down = true
	for c := range l.conns {
		c.Close()
	}
	l.conns = map[net.Conn]bool{}
	l.mu.Unlock()
}

// freeze stops forwarding without closing anything: the device has hung.
func (l *link) freeze() {
	l.mu.Lock()
	if !l.frozen {
		l.frozen = true
		l.thaw = make(chan struct{})
	}
	l.mu.Unlock()
}
