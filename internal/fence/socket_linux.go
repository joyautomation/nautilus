//go:build linux

package fence

import (
	"syscall"
	"unsafe"
)

// linger0 sets SO_LINGER 0 on fd: its close sends a reset.
func linger0(fd uintptr) error {
	return syscall.SetsockoptLinger(int(fd), syscall.SOL_SOCKET, syscall.SO_LINGER, &syscall.Linger{Onoff: 1, Linger: 0})
}

// abortFD disconnects the TCP socket fd in place: connect with AF_UNSPEC
// makes Linux abort the connection (tcp_disconnect), sending a reset and
// dropping the send queue, while the descriptor stays open for its owner
// to close.
func abortFD(fd uintptr) bool {
	sa := syscall.RawSockaddr{Family: syscall.AF_UNSPEC}
	_, _, errno := syscall.Syscall(syscall.SYS_CONNECT, fd, uintptr(unsafe.Pointer(&sa)), unsafe.Sizeof(sa))
	return errno == 0
}
