package testmqtt

import (
	"crypto/tls"
	"log/slog"
	"net"
	"sync"

	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
)

// TCP is a test broker's TCP listener: mochi's listeners.TCP, minus a data
// race on the server's client WaitGroup. Build test brokers with NewTCP,
// never listeners.NewTCP (TestTestBrokersListenThroughTestmqtt).
//
// The race, in mochi-mqtt v2.7.9 (unfixed upstream): listeners.TCP.Serve
// accepts a connection and spawns the server's establish callback, whose
// attachClient does Listeners.ClientsWg.Add(1) inside that goroutine.
// Listeners.CloseAll closes the listeners and then calls ClientsWg.Wait.
// Nothing orders the Add before the Wait, so an Add from zero can run
// concurrently with it, which the WaitGroup contract forbids and which Go's
// race detector reports (#909). It made the required go-race job flaky.
//
// TCP does the Add itself, in the accept loop, before spawning establish,
// and under a lock that Close also takes: every Add of ours happens before
// Close returns, hence before CloseAll's Wait, and mochi's own Add in
// attachClient never starts from zero. A connection accepted after Close
// is dropped, not established.
type TCP struct {
	srv     *mqttsrv.Server
	id      string
	address string
	tls     *tls.Config

	mu     sync.Mutex
	listen net.Listener
	closed bool
}

// testHookBeforeSpawn, when set (by tests, before Serve), runs on the accept
// loop between counting a connection and spawning its establish.
var testHookBeforeSpawn func()

// NewTCP returns a listener for srv on cfg.Address (TLS when cfg.TLSConfig
// is set). Add it with srv.AddListener, as with listeners.NewTCP.
func NewTCP(srv *mqttsrv.Server, cfg listeners.Config) *TCP {
	return &TCP{srv: srv, id: cfg.ID, address: cfg.Address, tls: cfg.TLSConfig}
}

// ID returns the listener's id.
func (l *TCP) ID() string { return l.id }

// Protocol returns "tcp".
func (l *TCP) Protocol() string { return "tcp" }

// Address returns the bound address once Init has run (so ":0" reads back
// the real port), the configured one before.
func (l *TCP) Address() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.listen != nil {
		return l.listen.Addr().String()
	}
	return l.address
}

// Init opens the network address.
func (l *TCP) Init(*slog.Logger) error {
	var ln net.Listener
	var err error
	if l.tls != nil {
		ln, err = tls.Listen("tcp", l.address, l.tls)
	} else {
		ln, err = net.Listen("tcp", l.address)
	}
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.listen = ln
	l.mu.Unlock()
	return nil
}

// Serve accepts connections until Close, handing each to establish with the
// server's client WaitGroup already counting it.
func (l *TCP) Serve(establish listeners.EstablishFn) {
	l.mu.Lock()
	ln := l.listen
	l.mu.Unlock()
	if ln == nil {
		return
	}
	wg := &l.srv.Listeners.ClientsWg
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			_ = conn.Close()
			return
		}
		wg.Add(1) // under mu: ordered before Close returns, so before CloseAll's Wait
		l.mu.Unlock()
		if testHookBeforeSpawn != nil {
			testHookBeforeSpawn()
		}
		go func() {
			defer wg.Done()
			_ = establish(l.id, conn)
		}()
	}
}

// Close stops accepting. Like mochi's listener, it runs closeClients (the
// server's client sweep) only the first time: testmqtt.Close relies on that
// to keep mochi's deadlocking sweep out of srv.Close.
func (l *TCP) Close(closeClients listeners.CloseFn) {
	l.mu.Lock()
	first := !l.closed
	l.closed = true
	ln := l.listen
	l.mu.Unlock()
	if first {
		closeClients(l.id)
	}
	if ln != nil {
		_ = ln.Close()
	}
}
