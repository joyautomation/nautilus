package redfish

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
)

// #1040 review (3): a Redfish command on a redundant pair (a fenced POST,
// the ComputerSystem.Reset ForceOff) that the BMC never answers has its
// socket RESET at the send deadline, never closed with a FIN that would
// leave the kernel retransmitting the POST past the lease during a
// partition. The BMC side reads ECONNRESET. fence_fin turns it red, and
// so does a client that leaves the close to net/http (a FIN).
func TestFencedPostResetsAtItsDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	peerErr := make(chan error, 1)
	go func() {
		pc, err := ln.Accept()
		if err != nil {
			peerErr <- err
			return
		}
		defer pc.Close()
		_ = pc.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, err = io.Copy(io.Discard, pc) // takes the POST, never answers
		peerErr <- err
	}()
	c, err := NewClient(Source{ID: "bmc1", Host: "http://" + ln.Addr().String(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := fence.Context(context.Background(), time.Now().Add(200*time.Millisecond))
	defer cancel()
	start := time.Now()
	if _, err := c.Post(ctx, "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", map[string]string{"ResetType": "ForceOff"}); err == nil {
		t.Fatal("an unanswered POST succeeded")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the unanswered POST took %s; want it ended at its 200 ms deadline", took)
	}
	select {
	case err := <-peerErr:
		if !errors.Is(err, syscall.ECONNRESET) {
			t.Fatalf("the BMC side read to %v; want ECONNRESET (a reset, not a FIN)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was never closed")
	}
}
