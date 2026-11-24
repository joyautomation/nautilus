package opcua

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"

	"github.com/joyautomation/nautilus/internal/fence"
)

// #710 d1c: a method call that writes to a device (an IO-Link parameter's
// WriteISDU) goes only while the write gate is open, so a standby's never
// reaches the master, and one under way is counted for a switchover's
// drain while the server has it. isdu_write_unfenced turns it red.
func TestCallFencedGoesOnlyThroughAnOpenGate(t *testing.T) {
	var mu sync.Mutex
	var during []int
	ts := startServerHooked(t, freePort(t), "urn:nautilus:test", 12.5, func(s *server.Server) {
		s.RegisterHandler(id.CallRequest_Encoding_DefaultBinary,
			func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
				req := r.(*ua.CallRequest)
				mu.Lock()
				during = append(during, fence.InFlight())
				mu.Unlock()
				return &ua.CallResponse{
					ResponseHeader: &ua.ResponseHeader{Timestamp: time.Now(), RequestHandle: req.RequestHeader.RequestHandle, ServiceResult: ua.StatusOK,
						ServiceDiagnostics: &ua.DiagnosticInfo{}, StringTable: []string{}, AdditionalHeader: ua.NewExtensionObject(nil)},
					Results: []*ua.CallMethodResult{{StatusCode: ua.StatusOK}},
				}, nil
			})
	})
	d, err := New(ts.url, manifest(2), fastOpts()...)
	if err != nil {
		t.Fatal(err)
	}
	var open sync.Mutex
	gate := false
	d.SetSendGate(func() (bool, time.Duration, uint64) {
		open.Lock()
		defer open.Unlock()
		return gate, time.Second, 1
	})
	startHooked(t, d)
	waitFor(t, "online", d.Online)
	obj, meth := NodeRef{NodeID: "ns=0;s=Port1", Namespace: "urn:nautilus:test"}, NodeRef{NodeID: "ns=0;s=WriteISDU", Namespace: "urn:nautilus:test"}

	if _, err := d.CallFenced(context.Background(), obj, meth); !errors.Is(err, fence.ErrGateClosed) {
		t.Fatalf("a call with the gate shut (a standby): %v, want ErrGateClosed", err)
	}
	mu.Lock()
	if len(during) != 0 {
		t.Fatalf("a standby's call reached the master (%d calls)", len(during))
	}
	mu.Unlock()

	open.Lock()
	gate = true
	open.Unlock()
	if _, err := d.CallFenced(context.Background(), obj, meth); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(during) != 1 || during[0] < 1 {
		t.Fatalf("the leader's call reached the master with %v in flight: a drain would not wait for it", during)
	}
	mu.Unlock()
	if n := fence.InFlight(); n != 0 {
		t.Fatalf("%d sends still counted after the call returned", n)
	}
}
