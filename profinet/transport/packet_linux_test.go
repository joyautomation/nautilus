package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestPacketVeth runs the real AF_PACKET port across a veth pair. Raw
// sockets need CAP_NET_RAW, so the test re-runs itself as root of a new
// user and network namespace: no privilege on the host, no host
// interface touched. Where the kernel refuses unprivileged user
// namespaces (some distributions, some CI runners) it skips;
// NAUTILUS_PROFINET_VETH=1 turns the skip into a failure.
func TestPacketVeth(t *testing.T) {
	if os.Getenv("NAUTILUS_PROFINET_IN_NETNS") == "1" {
		vethInNamespace(t)
		return
	}
	if testing.Short() {
		t.Skip("short")
	}
	skip := func(why string, err error) {
		t.Helper()
		if os.Getenv("NAUTILUS_PROFINET_VETH") == "1" {
			t.Fatalf("%s: %v", why, err)
		}
		t.Skipf("%s: %v", why, err)
	}
	if _, err := exec.LookPath("ip"); err != nil {
		skip("no ip(8)", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPacketVeth$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), "NAUTILUS_PROFINET_IN_NETNS=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		skip("no unprivileged user namespace", err)
	}
	t.Logf("in namespace:\n%s", out)
	if bytes.Contains(out, []byte("NETNS-SETUP-FAILED")) {
		skip("veth setup refused in the namespace", err)
	}
	if err != nil {
		t.Fatalf("namespace run failed: %v", err)
	}
}

func vethInNamespace(t *testing.T) {
	for _, args := range [][]string{
		{"link", "add", "pn0", "type", "veth", "peer", "name", "pn1"},
		{"link", "set", "pn0", "up"},
		{"link", "set", "pn1", "up"},
	} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("NETNS-SETUP-FAILED ip %v: %v %s", args, err, out)
		}
	}
	dcp := net.HardwareAddr{0x01, 0x0E, 0xCF, 0x00, 0x00, 0x00}
	other := net.HardwareAddr{0x01, 0x0E, 0xCF, 0x00, 0x00, 0x01}
	a, err := Open("pn0", Options{EtherTypes: []uint16{pnio}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open("pn1", Options{EtherTypes: []uint16{pnio, 0x88CC}, Multicast: []net.HardwareAddr{dcp}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	expect := func(what string, et uint16, payload []byte) {
		t.Helper()
		f, ok := recvWithin(t, b, 2*time.Second)
		if !ok {
			t.Fatalf("%s: nothing received", what)
		}
		if !bytes.Equal(f.Src, a.MAC()) || f.EtherType != et || !bytes.HasPrefix(f.Payload, payload) {
			t.Fatalf("%s: got %+v", what, f)
		}
	}
	nothing := func(what string) {
		t.Helper()
		if f, ok := recvWithin(t, b, 100*time.Millisecond); ok {
			t.Fatalf("%s: received %+v", what, f)
		}
	}

	// The filter drops other EtherTypes in the kernel; the port drops
	// frames for other stations and groups it did not join.
	a.Send(b.MAC(), 0x0800, []byte{0xEE})
	a.Send(net.HardwareAddr{0x02, 1, 2, 3, 4, 5}, pnio, []byte{0xEE})
	a.Send(other, pnio, []byte{0xEE})
	nothing("filtered frames")

	a.Send(b.MAC(), pnio, []byte{0xFE, 0xFE, 1})
	expect("unicast", pnio, []byte{0xFE, 0xFE, 1})
	a.Send(dcp, pnio, []byte{0xFE, 0xFE, 2})
	expect("joined group", pnio, []byte{0xFE, 0xFE, 2})
	a.Send(broadcast, 0x88CC, []byte{3})
	expect("broadcast LLDP", 0x88CC, []byte{3})

	// A priority-tagged frame (VLAN 0, priority 6) arrives untagged.
	tag := binary.BigEndian.AppendUint16(nil, 6<<13)
	tag = binary.BigEndian.AppendUint16(tag, pnio)
	a.Send(b.MAC(), 0x8100, append(tag, 0xC0, 0x01))
	expect("802.1Q priority tag", pnio, []byte{0xC0, 0x01})

	// The sender does not receive its own frames even when it takes the
	// EtherType it sends.
	if f, ok := recvWithin(t, a, 100*time.Millisecond); ok {
		t.Fatalf("sender received %+v", f)
	}

	// Cancelling and closing wake a blocked Recv.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := b.Recv(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv after cancel: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := b.Recv(context.Background()); done <- err }()
	time.Sleep(20 * time.Millisecond)
	b.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Recv after Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wake Recv")
	}
}

func TestOpenWithoutCapability(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	ifs, _ := net.Interfaces()
	name := ""
	for _, ifi := range ifs {
		if len(ifi.HardwareAddr) == 6 {
			name = ifi.Name
			break
		}
	}
	if name == "" {
		t.Skip("no Ethernet interface")
	}
	_, err := Open(name, Options{EtherTypes: []uint16{pnio}})
	var pe *PermissionError
	if err == nil {
		t.Skip("this process holds CAP_NET_RAW")
	}
	if !errors.As(err, &pe) {
		t.Fatalf("want *PermissionError, got %T %v", err, err)
	}
}

func TestEtherTypeFilterShape(t *testing.T) {
	p := etherTypeFilter([]uint16{0x8892, 0x88CC})
	// ld, 2×jeq, jeq 8100, ld, 2×jeq, drop, accept
	if len(p) != 9 {
		t.Fatalf("%d instructions", len(p))
	}
	// Every accepting jump lands on the last instruction.
	for _, i := range []int{1, 2, 5, 6} {
		if i+1+int(p[i].Jt) != len(p)-1 {
			t.Errorf("instruction %d jumps to %d", i, i+1+int(p[i].Jt))
		}
	}
	if 3+1+int(p[3].Jf) != len(p)-2 {
		t.Errorf("non-VLAN frames jump to %d, want drop", 3+1+int(p[3].Jf))
	}
	if p[len(p)-2].K != 0 || p[len(p)-1].K == 0 {
		t.Error("drop/accept order")
	}
}
