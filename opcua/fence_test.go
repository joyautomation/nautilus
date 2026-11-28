package opcua

import (
	"github.com/joyautomation/nautilus/internal/fence"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"

	nio "github.com/joyautomation/nautilus/io"
)

// 3b-3b, design §3.4 on the OPC UA client: a fenced write the server does
// not answer by its send deadline has the session's socket reset in place
// (Socket.Abort; that a reset reaches the far side is internal/fence's
// TestSocketAbortAndCloseReset), and the session is dialled again: a
// normal reconnect over linger-0 sockets then carries the next write.
// fence_fin turns it red (no abort).
func TestOPCUAFencedWriteResetsItsSession(t *testing.T) {
	var silent atomic.Bool
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ts := silentWriteServer(t, &silent, release)
	d := startPaired(t, ts.url, manifest(2), func() (bool, time.Duration, uint64) { return true, 300 * time.Millisecond, 1 })
	waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Speed"]; return ok })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 0.0}) // baseline

	fence.ResetCutsForTest()
	silent.Store(true)
	_ = d.WriteOutputs(nio.Values{"Setpoint": 5.0})
	waitFor(t, "the unanswered write's socket reset", func() bool { return d.aborts.Load() > 0 })
	// #710: the timer and the failure both saw it; it is one write cut.
	if got := fence.CutCounts(); len(got) != 1 || got[d.name] != 1 {
		t.Fatalf("writes cut = %v, want %s 1", got, d.name)
	}
	silent.Store(false)
	_ = d.WriteOutputs(nio.Values{"Setpoint": 6.0})
	waitFor(t, "the next write over a reconnected session", func() bool { return ts.get(t, "Setpoint") == 6.0 })
}

// 3b-3b on the OPC UA client: a write queued under one term of the lease
// (the server down, the command held) never goes out under the next, even
// when no look saw the gate closed in between; the reconnect that follows
// works, and a write computed under the new term goes out. old_term_kept
// turns it red.
func TestOPCUAQueuedWriteDoesNotSurviveATermMove(t *testing.T) {
	port := freePort(t)
	ts := startServer(t, port)
	d := startDriver(t, ts.url, manifest(2))
	waitFor(t, "first poll", func() bool { return read(t, d)["plant__Online"] == true })
	var term atomic.Uint64
	term.Store(5)
	d.SetWriteGate(func() bool { return true })
	d.SetSendGate(func() (bool, time.Duration, uint64) { return true, time.Second, term.Load() })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 0.0}) // baseline, under term 5

	_ = ts.srv.Close()
	waitFor(t, "offline", func() bool { return read(t, d)["plant__Online"] == false })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 3.0}) // queued under term 5
	term.Store(9)                                   // lost and won again while down; the gate never seen closed
	ts2 := startServer(t, port)
	waitFor(t, "reconnect", func() bool { return read(t, d)["plant__Online"] == true })
	time.Sleep(200 * time.Millisecond)
	if got := ts2.get(t, "Setpoint"); got != 0.0 {
		t.Fatalf("Setpoint = %v; the command queued under term 5 went out under term 9", got)
	}
	_ = d.WriteOutputs(nio.Values{"Setpoint": 4.0}) // computed under term 9
	waitFor(t, "the term-9 write", func() bool { return ts2.get(t, "Setpoint") == 4.0 })
}

// #1047 review (4): a flush whose queue was taken before a reopen (the
// lease lost and won again, seen by a look while the flush runs) sends
// none of it; the next push, under the new term, goes out.
func TestOPCUAFlushRacingAReopenSendsNothing(t *testing.T) {
	ts := startServer(t, freePort(t))
	var term atomic.Uint64
	term.Store(5)
	d := startPaired(t, ts.url, manifest(2), func() (bool, time.Duration, uint64) { return true, time.Second, term.Load() })
	waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Speed"]; return ok })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 0.0}) // baseline
	var once sync.Once
	testHookAfterSwap = func() {
		once.Do(func() {
			term.Store(9)
			d.look() // a scan sees the reopen while the flush holds its queue
		})
	}
	t.Cleanup(func() { testHookAfterSwap = nil })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 7.0}) // computed under term 5
	time.Sleep(300 * time.Millisecond)
	if got := ts.get(t, "Setpoint"); got != 0.0 {
		t.Fatalf("Setpoint = %v; the flush sent its term-5 queue after the reopen", got)
	}
	_ = d.WriteOutputs(nio.Values{"Setpoint": 8.0})
	waitFor(t, "the term-9 write", func() bool { return ts.get(t, "Setpoint") == 8.0 })
}

// #1047 review (3): a chunk that failed is not put back after a reopen
// came between the failure and the requeue: the next flush, under the new
// term, would send the earlier leadership's value.
func TestOPCUAFailedChunkNotRequeuedAfterAReopen(t *testing.T) {
	var silent atomic.Bool
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ts := silentWriteServer(t, &silent, release)
	var term atomic.Uint64
	term.Store(5)
	d := startPaired(t, ts.url, manifest(2), func() (bool, time.Duration, uint64) { return true, 300 * time.Millisecond, term.Load() })
	waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Speed"]; return ok })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 0.0}) // baseline
	var once sync.Once
	testHookBeforeRequeue = func() {
		once.Do(func() {
			term.Store(9)
			d.look() // the reopen, between the failure and the requeue
		})
	}
	t.Cleanup(func() { testHookBeforeRequeue = nil })
	silent.Store(true)
	_ = d.WriteOutputs(nio.Values{"Setpoint": 5.0}) // computed under term 5; it fails
	waitFor(t, "the failed write", func() bool { return d.aborts.Load() > 0 })
	silent.Store(false)
	time.Sleep(500 * time.Millisecond) // the session dialled again, and a flush
	if got := ts.get(t, "Setpoint"); got != 0.0 {
		t.Fatalf("Setpoint = %v; the failed term-5 write was put back and sent under term 9", got)
	}
}

// silentWriteServer is a test server whose Write service, while silent
// holds, answers nothing (a partition), and writes the value otherwise.
func silentWriteServer(t *testing.T, silent *atomic.Bool, release chan struct{}) *testServer {
	t.Helper()
	var srv *server.Server
	return startServerHooked(t, freePort(t), "urn:nautilus:test", 12.5, func(s *server.Server) {
		srv = s
		s.RegisterHandler(id.WriteRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
			req := r.(*ua.WriteRequest)
			if silent.Load() {
				// A partition: the request is lost, never applied, and no
				// answer comes within the deadline.
				for silent.Load() {
					select {
					case <-release:
						return nil, ua.StatusBadTimeout
					case <-time.After(10 * time.Millisecond):
					}
				}
				return nil, ua.StatusBadTimeout
			}
			results := make([]ua.StatusCode, len(req.NodesToWrite))
			for i, w := range req.NodesToWrite {
				setValue(srv, w.NodeID, w.Value)
				results[i] = ua.StatusOK
			}
			return &ua.WriteResponse{
				ResponseHeader: &ua.ResponseHeader{Timestamp: time.Now(), RequestHandle: req.RequestHeader.RequestHandle, ServiceResult: ua.StatusOK,
					ServiceDiagnostics: &ua.DiagnosticInfo{}, StringTable: []string{}, AdditionalHeader: ua.NewExtensionObject(nil)},
				Results: results,
			}, nil
		})
	})
}
