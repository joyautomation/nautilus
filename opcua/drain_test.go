package opcua

import (
	"sync"
	"testing"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"

	"github.com/joyautomation/nautilus/internal/fence"
	nio "github.com/joyautomation/nautilus/io"
)

// #710 d1b: on a pair each OPC UA write chunk is counted for a planned
// switchover's drain while the server has it (the server sees a send in
// flight), and the count is back to nothing once the write has returned,
// so a drain neither misses a chunk nor waits forever. A done called
// before the write (or never) turns it red.
func TestOPCUAEachChunkIsCountedForTheDrain(t *testing.T) {
	var mu sync.Mutex
	var during []int
	ts := startServerHooked(t, freePort(t), "urn:nautilus:test", 12.5, func(s *server.Server) {
		s.RegisterHandler(id.WriteRequest_Encoding_DefaultBinary,
			func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
				req := r.(*ua.WriteRequest)
				mu.Lock()
				during = append(during, fence.InFlight())
				mu.Unlock()
				res := make([]ua.StatusCode, len(req.NodesToWrite))
				return &ua.WriteResponse{
					ResponseHeader: &ua.ResponseHeader{Timestamp: time.Now(), RequestHandle: req.RequestHeader.RequestHandle, ServiceResult: ua.StatusOK,
						ServiceDiagnostics: &ua.DiagnosticInfo{}, StringTable: []string{}, AdditionalHeader: ua.NewExtensionObject(nil)},
					Results: res,
				}, nil
			})
	})
	d := startPaired(t, ts.url, manifest(2), func() (bool, time.Duration, uint64) { return true, time.Second, 1 })
	waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Speed"]; return ok })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 5.0})
	waitFor(t, "the write", func() bool { mu.Lock(); defer mu.Unlock(); return len(during) > 0 })
	mu.Lock()
	for i, n := range during {
		if n < 1 {
			t.Errorf("write %d reached the server with nothing in flight: a drain would not wait for it", i)
		}
	}
	mu.Unlock()
	waitFor(t, "the count back to nothing", func() bool { return fence.InFlight() == 0 })
}
