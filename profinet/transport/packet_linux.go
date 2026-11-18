package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// Open binds a port to the named interface: an AF_PACKET socket that
// receives the given EtherTypes (a classic BPF filter, so the kernel
// drops everything else) and joins the given multicast groups. It needs
// CAP_NET_RAW; without it the error is a *PermissionError.
//
// The socket is non-blocking under Go's poller, so Recv honours its
// context and Close wakes it.
func Open(ifname string, opt Options) (Port, error) {
	if len(opt.EtherTypes) == 0 {
		return nil, errors.New("profinet transport: no EtherTypes to receive")
	}
	ifi, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, fmt.Errorf("profinet transport: %w", err)
	}
	if len(ifi.HardwareAddr) != 6 {
		return nil, fmt.Errorf("profinet transport: %s has no Ethernet address", ifname)
	}
	// Protocol 0 receives nothing until bind, so no frame slips in
	// before the filter is attached.
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			return nil, &PermissionError{Interface: ifname, Err: err}
		}
		return nil, fmt.Errorf("profinet transport: socket: %w", err)
	}
	fail := func(what string, err error) (Port, error) {
		syscall.Close(fd)
		return nil, fmt.Errorf("profinet transport: %s on %s: %w", what, ifname, err)
	}
	if err := syscall.AttachLsf(fd, etherTypeFilter(opt.EtherTypes)); err != nil {
		return fail("attach filter", err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_ALL), Ifindex: ifi.Index}); err != nil {
		return fail("bind", err)
	}
	for _, g := range opt.Multicast {
		if err := joinGroup(fd, ifi.Index, g); err != nil {
			return fail("join "+g.String(), err)
		}
	}
	p := &packetPort{
		f:   os.NewFile(uintptr(fd), "profinet:"+ifname),
		mac: append(net.HardwareAddr(nil), ifi.HardwareAddr...),
	}
	for _, g := range opt.Multicast {
		p.groups = append(p.groups, append(net.HardwareAddr(nil), g...))
	}
	return p, nil
}

type packetPort struct {
	f      *os.File
	mac    net.HardwareAddr
	groups []net.HardwareAddr

	rmu  sync.Mutex // one reader at a time; rbuf is the reader's
	rbuf [1522]byte
	wmu  sync.Mutex
	wbuf []byte
}

func (p *packetPort) MAC() net.HardwareAddr { return p.mac }

func (p *packetPort) Recv(ctx context.Context) (Frame, error) {
	p.rmu.Lock()
	defer p.rmu.Unlock()
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}
	p.f.SetReadDeadline(time.Time{})
	stop := context.AfterFunc(ctx, func() { p.f.SetReadDeadline(time.Now()) })
	defer stop()
	for {
		n, err := p.f.Read(p.rbuf[:])
		if err != nil {
			if ctx.Err() != nil {
				return Frame{}, ctx.Err()
			}
			if errors.Is(err, os.ErrClosed) {
				return Frame{}, ErrClosed
			}
			return Frame{}, fmt.Errorf("profinet transport: read: %w", err)
		}
		f, ok := parseFrame(p.rbuf[:n])
		// A packet socket bound to ETH_P_ALL also sees what this host
		// sends, and frames for other stations whenever the NIC passes
		// them (a veth, or promiscuous mode another socket asked for).
		// Only what is addressed to this station is delivered.
		if !ok || bytes.Equal(f.Src, p.mac) || !addressedTo(f.Dst, p.mac, p.groups) {
			continue
		}
		return f, nil
	}
}

func (p *packetPort) Send(dst net.HardwareAddr, etherType uint16, payload []byte) error {
	if len(dst) != 6 {
		return fmt.Errorf("profinet transport: destination %v is not an Ethernet address", dst)
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	b := append(p.wbuf[:0], dst...)
	b = append(b, p.mac...)
	b = binary.BigEndian.AppendUint16(b, etherType)
	b = append(b, padded(payload)...)
	p.wbuf = b
	_, err := p.f.Write(b)
	if errors.Is(err, os.ErrClosed) {
		return ErrClosed
	}
	return err
}

func (p *packetPort) Close() error { return p.f.Close() }

// parseFrame splits a received frame, copying it out of the read buffer
// and removing one 802.1Q tag (PROFINET RT frames often carry a
// priority tag, VLAN 0 priority 6, when the NIC does not strip it).
func parseFrame(b []byte) (Frame, bool) {
	if len(b) < 14 {
		return Frame{}, false
	}
	et := binary.BigEndian.Uint16(b[12:14])
	body := b[14:]
	if et == 0x8100 {
		if len(body) < 4 {
			return Frame{}, false
		}
		et = binary.BigEndian.Uint16(body[2:4])
		body = body[4:]
	}
	return Frame{
		Dst:       append(net.HardwareAddr(nil), b[0:6]...),
		Src:       append(net.HardwareAddr(nil), b[6:12]...),
		EtherType: et,
		Payload:   append([]byte(nil), body...),
	}, true
}

// etherTypeFilter accepts a frame whose EtherType, or whose 802.1Q
// inner EtherType, is one of types; it drops everything else in the
// kernel.
func etherTypeFilter(types []uint16) []syscall.SockFilter {
	const (
		ldh  = syscall.BPF_LD | syscall.BPF_H | syscall.BPF_ABS
		jeq  = syscall.BPF_JMP | syscall.BPF_JEQ | syscall.BPF_K
		ret  = syscall.BPF_RET | syscall.BPF_K
		keep = 0x40000
	)
	n := len(types)
	// Layout:
	//   0       ldh [12]
	//   1..n    jeq type_i → ACCEPT
	//   n+1     jeq 0x8100 → next, else DROP
	//   n+2     ldh [16]
	//   n+3..   jeq type_i → ACCEPT
	//   2n+3    ret 0      (DROP)
	//   2n+4    ret keep   (ACCEPT)
	drop := 2*n + 3
	accept := drop + 1
	var p []syscall.SockFilter
	add := func(code uint16, jt, jf int, k uint32) {
		p = append(p, syscall.SockFilter{Code: code, Jt: uint8(jt), Jf: uint8(jf), K: k})
	}
	at := func() int { return len(p) }
	add(ldh, 0, 0, 12)
	for _, t := range types {
		add(jeq, accept-at()-1, 0, uint32(t))
	}
	add(jeq, 0, drop-at()-1, 0x8100)
	add(ldh, 0, 0, 16)
	for _, t := range types {
		add(jeq, accept-at()-1, 0, uint32(t))
	}
	add(ret, 0, 0, 0)
	add(ret, 0, 0, keep)
	return p
}

// joinGroup adds a multicast membership (struct packet_mreq).
func joinGroup(fd, ifindex int, mac net.HardwareAddr) error {
	var mreq [16]byte
	binary.NativeEndian.PutUint32(mreq[0:4], uint32(ifindex))
	binary.NativeEndian.PutUint16(mreq[4:6], syscall.PACKET_MR_MULTICAST)
	binary.NativeEndian.PutUint16(mreq[6:8], uint16(len(mac)))
	copy(mreq[8:], mac)
	return syscall.SetsockoptString(fd, syscall.SOL_PACKET, syscall.PACKET_ADD_MEMBERSHIP, string(mreq[:]))
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }
