package cip

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// A request ends as soon as its context is cancelled, not when its 10 s
// socket deadline runs out: a driver's Stop cancels the context, and a
// device that stopped answering must not hold the stop (#932).

// stubDevice is a TCP peer that answers the first answer requests
// (RegisterSession, then SendRRData) after delay, and reads but never
// answers the rest: a device that hung.
func stubDevice(t *testing.T, answer int, delay time.Duration) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		for n := 0; ; n++ {
			hdr := make([]byte, EncapHeaderLen)
			if _, err := io.ReadFull(c, hdr); err != nil {
				return
			}
			var h EncapHeader
			if err := h.UnmarshalBinary(hdr); err != nil {
				return
			}
			if _, err := io.CopyN(io.Discard, c, int64(h.Length)); err != nil {
				return
			}
			if n >= answer {
				continue // hung: read, never answer
			}
			time.Sleep(delay)
			reply := EncapHeader{Command: h.Command, SessionHandle: 0x1234, Status: EncapStatusSuccess}
			var body []byte
			if h.Command == CmdSendRRData {
				body = EncodeSendRRData(SendRRDataHeader{}, []CPFItem{
					{TypeID: ItemNullAddress},
					{TypeID: ItemUnconnectedData, Data: MessageRouterResponse{Service: 0x01}.Encode()},
				})
			}
			if _, err := c.Write(EncodeFrame(reply, body)); err != nil {
				return
			}
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

// endsOnCancel runs req with a context cancelled after 50 ms and wants it
// to fail with context.Canceled well inside the 10 s request deadline.
func endsOnCancel(t *testing.T, what string, req func(ctx context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	// On its own goroutine, so a request that never ends fails the test
	// rather than hanging it (RegisterSession had no deadline at all).
	done := make(chan error, 1)
	go func() { done <- req(ctx) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s still running 2 s after its context was cancelled at 50 ms", what)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%s: err = %v, want context.Canceled", what, err)
	}
}

func TestRegisterSessionEndsOnCancel(t *testing.T) {
	host, port := stubDevice(t, 0, 0)
	c := NewClient(host, port, nil)
	t.Cleanup(func() { c.Close() })
	endsOnCancel(t, "Connect", c.Connect)
}

func TestSendRRDataEndsOnCancel(t *testing.T) {
	host, port := stubDevice(t, 1, 0)
	c := NewClient(host, port, nil)
	t.Cleanup(func() { c.Close() })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	endsOnCancel(t, "SendRRData", func(ctx context.Context) error {
		_, err := c.SendRRData(ctx, 0x01, []byte{0x20, 0x01, 0x24, 0x01}, nil)
		return err
	})
}

func TestSendUnitDataEndsOnCancel(t *testing.T) {
	host, port := stubDevice(t, 1, 0)
	c := NewClient(host, port, nil)
	t.Cleanup(func() { c.Close() })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	endsOnCancel(t, "SendUnitData", func(ctx context.Context) error {
		_, err := c.SendUnitData(ctx, 1, 1, 0x01, []byte{0x20, 0x01, 0x24, 0x01}, nil)
		return err
	})
}

// A request that finished is disarmed: cancelling its context later does
// not cut short the next request on the same connection.
func TestFinishedRequestIsDisarmed(t *testing.T) {
	host, port := stubDevice(t, 2, 200*time.Millisecond)
	c := NewClient(host, port, nil)
	t.Cleanup(func() { c.Close() })
	first, cancelFirst := context.WithCancel(context.Background())
	if err := c.Connect(first); err != nil {
		t.Fatal(err)
	}
	// The next request is under way when the first one's context is
	// cancelled; the device answers it 200 ms in.
	time.AfterFunc(50*time.Millisecond, cancelFirst)
	if _, err := c.SendRRData(context.Background(), 0x01, []byte{0x20, 0x01, 0x24, 0x01}, nil); err != nil {
		t.Fatalf("a request on a fresh context failed when an earlier one's context was cancelled: %v", err)
	}
}
