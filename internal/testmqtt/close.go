package testmqtt

import (
	"sync"
	"time"

	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// Close stops a test broker. Use it instead of srv.Close, which can deadlock
// in mochi-mqtt v2.7.9 (unfixed upstream): Close's client sweep,
// Clients.GetByListener, read-locks the client map and then read-locks it
// again through Len, so a client disconnecting at that moment (a queued
// Clients.Delete, a writer) blocks both forever. CI hung ten minutes on
// exactly that (#810).
//
// A listener runs its client sweep only the first time it closes, so
// closing every listener first with a no-op sweep takes GetByListener out
// of srv.Close entirely. The clients are disconnected from GetAll's copy
// instead (one read lock), repeatedly while the close waits for their
// goroutines, which also catches one accepted just before a listener shut.
//
// Close is idempotent: a test that takes its broker down mid-run (broker
// loss) and its cleanup can both call it; mochi's own Close panics on a
// second call. TestTestBrokersCloseThroughTestmqtt holds every test in the
// module to closing its mochi brokers here.
func Close(srv *mqttsrv.Server) error {
	closesMu.Lock()
	c, ok := closes[srv]
	if !ok {
		c = new(closeOnce)
		closes[srv] = c
	}
	closesMu.Unlock()
	c.Do(func() { c.err = closeNow(srv) })
	return c.err
}

type closeOnce struct {
	sync.Once
	err error
}

var (
	closesMu sync.Mutex
	closes   = map[*mqttsrv.Server]*closeOnce{}
)

func closeNow(srv *mqttsrv.Server) error {
	done := make(chan error, 1)
	go func() {
		// CloseAll waits for every client goroutine; the loop below ends
		// the clients it waits for.
		srv.Listeners.CloseAll(func(string) {})
		done <- srv.Close()
	}()
	for {
		for _, cl := range srv.Clients.GetAll() {
			if !cl.Closed() {
				_ = srv.DisconnectClient(cl, packets.ErrServerShuttingDown)
			}
		}
		select {
		case err := <-done:
			return err
		case <-time.After(20 * time.Millisecond):
		}
	}
}
