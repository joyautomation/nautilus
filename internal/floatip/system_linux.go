//go:build linux

package floatip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// System is the kernel's side on Linux: rtnetlink for the address and a raw
// packet socket for the gratuitous ARP. Both need CAP_NET_ADMIN / CAP_NET_RAW
// (naut deploy's unit grants them when redundancy.floating-ip is set).
type System struct{}

func (System) Add(c Config) error {
	return addr(unix.RTM_NEWADDR, unix.NLM_F_CREATE|unix.NLM_F_REPLACE, c)
}

func (System) Del(c Config) error {
	err := addr(unix.RTM_DELADDR, 0, c)
	if errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.ENODEV) {
		return nil // not there: what Del wants
	}
	return err
}

func align4(n int) int { return (n + 3) &^ 3 }

func addr(typ uint16, flags uint16, c Config) error {
	ifc, err := net.InterfaceByName(c.Interface)
	if err != nil {
		return fmt.Errorf("interface %s: %w", c.Interface, err)
	}
	ip := c.Address.Addr().As4()
	// nlmsghdr (16) + ifaddrmsg (8) + IFA_LOCAL (8) + IFA_ADDRESS (8)
	msg := make([]byte, 40)
	binary.NativeEndian.PutUint32(msg[0:], uint32(len(msg)))
	binary.NativeEndian.PutUint16(msg[4:], typ)
	binary.NativeEndian.PutUint16(msg[6:], flags|unix.NLM_F_REQUEST|unix.NLM_F_ACK)
	binary.NativeEndian.PutUint32(msg[8:], 1)
	msg[16] = unix.AF_INET
	msg[17] = byte(c.Address.Bits())
	msg[18] = 0
	msg[19] = unix.RT_SCOPE_UNIVERSE
	binary.NativeEndian.PutUint32(msg[20:], uint32(ifc.Index))
	for i, kind := range []uint16{unix.IFA_LOCAL, unix.IFA_ADDRESS} {
		o := 24 + 8*i
		binary.NativeEndian.PutUint16(msg[o:], 8)
		binary.NativeEndian.PutUint16(msg[o+2:], kind)
		copy(msg[o+4:], ip[:])
	}
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	tv := unix.Timeval{Sec: 3}
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	if err := unix.Sendto(fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	buf := make([]byte, 4096)
	n, _, err := unix.Recvfrom(fd, buf, 0)
	if err != nil {
		return err
	}
	for off := 0; off+16 <= n; {
		l := int(binary.NativeEndian.Uint32(buf[off:]))
		if l < 16 || off+l > n {
			break
		}
		if binary.NativeEndian.Uint16(buf[off+4:]) == unix.NLMSG_ERROR && l >= 20 {
			if code := int32(binary.NativeEndian.Uint32(buf[off+16:])); code != 0 {
				return syscall.Errno(-code)
			}
			return nil
		}
		off += align4(l)
	}
	return errors.New("no acknowledgement from the kernel")
}

// Announce sends a gratuitous ARP (a request for the address by the address,
// to the broadcast MAC) so every neighbour updates its cache.
func (System) Announce(c Config) error {
	ifc, err := net.InterfaceByName(c.Interface)
	if err != nil {
		return err
	}
	if len(ifc.HardwareAddr) != 6 {
		return fmt.Errorf("interface %s has no Ethernet address", c.Interface)
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_ARP)))
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ip := c.Address.Addr().As4()
	f := make([]byte, 0, 42)
	f = append(f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
	f = append(f, ifc.HardwareAddr...)
	f = append(f, 0x08, 0x06)       // ARP
	f = append(f, 0, 1, 8, 0, 6, 4) // Ethernet, IPv4, sizes
	f = append(f, 0, 1)             // request
	f = append(f, ifc.HardwareAddr...)
	f = append(f, ip[:]...)
	f = append(f, 0, 0, 0, 0, 0, 0)
	f = append(f, ip[:]...)
	sa := &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_ARP), Ifindex: ifc.Index, Halen: 6}
	copy(sa.Addr[:], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	return unix.Sendto(fd, f, 0, sa)
}

func htons(v uint16) uint16 {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	return *(*uint16)(unsafe.Pointer(&b[0]))
}
