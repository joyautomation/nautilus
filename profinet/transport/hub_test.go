package transport

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

var (
	macA  = net.HardwareAddr{0x02, 0, 0, 0, 0, 0x0A}
	macB  = net.HardwareAddr{0x02, 0, 0, 0, 0, 0x0B}
	macC  = net.HardwareAddr{0x02, 0, 0, 0, 0, 0x0C}
	group = net.HardwareAddr{0x01, 0x0E, 0xCF, 0x00, 0x00, 0x00}
)

const pnio = 0x8892

func recvWithin(t *testing.T, p Port, d time.Duration) (Frame, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	f, err := p.Recv(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return Frame{}, false
	}
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	return f, true
}

func TestHubDelivery(t *testing.T) {
	h := NewHub()
	a := h.Port(macA, Options{EtherTypes: []uint16{pnio}})
	b := h.Port(macB, Options{EtherTypes: []uint16{pnio}, Multicast: []net.HardwareAddr{group}})
	c := h.Port(macC, Options{EtherTypes: []uint16{0x88CC}})

	// Unicast reaches only its destination, padded to the Ethernet minimum.
	if err := a.Send(macB, pnio, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	f, ok := recvWithin(t, b, time.Second)
	if !ok || !bytes.Equal(f.Src, macA) || !bytes.Equal(f.Dst, macB) || f.EtherType != pnio ||
		len(f.Payload) != minPayload || !bytes.Equal(f.Payload[:3], []byte{1, 2, 3}) {
		t.Fatalf("got %+v ok=%v", f, ok)
	}
	if _, ok := recvWithin(t, a, 20*time.Millisecond); ok {
		t.Fatal("sender received its own frame")
	}

	// A group reaches members only; broadcast reaches every port that
	// takes the EtherType.
	a.Send(group, pnio, nil)
	if _, ok := recvWithin(t, b, time.Second); !ok {
		t.Fatal("member missed the group frame")
	}
	a.Send(broadcast, pnio, nil)
	if _, ok := recvWithin(t, b, time.Second); !ok {
		t.Fatal("broadcast missed")
	}
	if _, ok := recvWithin(t, c, 20*time.Millisecond); ok {
		t.Fatal("frame of another EtherType delivered")
	}
}

func TestHubCutTapClose(t *testing.T) {
	h := NewHub()
	a := h.Port(macA, Options{EtherTypes: []uint16{pnio}})
	b := h.Port(macB, Options{EtherTypes: []uint16{pnio}})
	var seen int
	h.Tap(func(Frame) { seen++ })

	h.Cut(macB, true)
	a.Send(macB, pnio, nil)
	if _, ok := recvWithin(t, b, 20*time.Millisecond); ok {
		t.Fatal("frame reached a cut port")
	}
	b.Send(macA, pnio, nil)
	if _, ok := recvWithin(t, a, 20*time.Millisecond); ok {
		t.Fatal("frame left a cut port")
	}
	h.Cut(macB, false)
	a.Send(macB, pnio, nil)
	if _, ok := recvWithin(t, b, time.Second); !ok {
		t.Fatal("reconnected port missed the frame")
	}
	if seen != 3 {
		t.Fatalf("tap saw %d frames, want 3", seen)
	}

	done := make(chan error, 1)
	go func() { _, err := b.Recv(context.Background()); done <- err }()
	b.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Recv after Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wake Recv")
	}
	if err := b.Send(macA, pnio, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Send after Close: %v", err)
	}
}

func TestPermissionErrorSaysHow(t *testing.T) {
	err := error(&PermissionError{Interface: "eth1", Err: errors.New("operation not permitted")})
	for _, want := range []string{"eth1", "CAP_NET_RAW", "setcap", "AmbientCapabilities"} {
		if !bytes.Contains([]byte(err.Error()), []byte(want)) {
			t.Errorf("%q lacks %q", err, want)
		}
	}
}
