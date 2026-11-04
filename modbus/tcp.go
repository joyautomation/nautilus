// Package modbus is the Modbus TCP field driver core for nautilus. It talks
// to the polled devices on a typical skid (gas analysers, ADAM I/O modules,
// Anybus gateways fronting several controllers on one socket, VFDs, safety
// controllers, IO-Link masters' DO words) through the io.Driver seam, with
// validated manifests and coalesced block reads instead of one request per
// variable.
//
// This file is the wire layer: MBAP framing and the client side of function
// codes 1, 2, 3, 4, 5, 6, 15 and 16 over an abstract conn seam, so everything
// above it (plan execution, encoding, the driver loops) is testable without a
// socket — the same idea as sparkplug/host's handleMessage seam.
package modbus

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Modbus function codes the driver speaks. The names are the spec's.
const (
	FCReadCoils              byte = 0x01
	FCReadDiscreteInputs     byte = 0x02
	FCReadHoldingRegisters   byte = 0x03
	FCReadInputRegisters     byte = 0x04
	FCWriteSingleCoil        byte = 0x05
	FCWriteSingleRegister    byte = 0x06
	FCWriteMultipleCoils     byte = 0x0F
	FCWriteMultipleRegisters byte = 0x10
)

// Protocol limits from the Modbus application protocol spec v1.1b3. A PDU is
// at most 253 bytes (256-byte serial ADU minus address and CRC — TCP keeps
// the limit so gateways can bridge); the read/write count caps follow from
// what fits in one PDU.
const (
	maxPDU            = 253
	maxReadRegisters  = 125  // FC 3/4: 125 × 2 bytes + count byte ≤ 253
	maxReadBits       = 2000 // FC 1/2: 2000 / 8 = 250 data bytes
	maxWriteRegisters = 123  // FC 16: header eats 5 bytes of the PDU
	maxWriteBits      = 1968 // FC 15: 246 data bytes
)

// mbapLen is the fixed MBAP header: transaction id (2), protocol id (2),
// length (2), unit id (1).
const mbapLen = 7

// ExceptionError is a Modbus exception response: the device answered the
// request and refused it. Distinct from transport errors on purpose — an
// exception means the connection is fine and THIS request is wrong (illegal
// address, unsupported function), so the driver marks the block Bad and
// keeps polling instead of reconnecting.
type ExceptionError struct {
	Code byte
}

func (e ExceptionError) Error() string {
	name := "unknown"
	switch e.Code {
	case 0x01:
		name = "illegal function"
	case 0x02:
		name = "illegal data address"
	case 0x03:
		name = "illegal data value"
	case 0x04:
		name = "server device failure"
	case 0x05:
		name = "acknowledge"
	case 0x06:
		name = "server device busy"
	case 0x08:
		name = "memory parity error"
	case 0x0A:
		name = "gateway path unavailable"
	case 0x0B:
		name = "gateway target device failed to respond"
	}
	return fmt.Sprintf("modbus: exception 0x%02X (%s)", e.Code, name)
}

// conn is the request/response seam the FC helpers and the driver loops run
// over: one Modbus transaction — request PDU out, response PDU data back.
// The returned bytes are the response PDU minus its function code; an
// exception response comes back as ExceptionError. tcpConn is the real
// implementation; tests substitute a scripted fake.
type conn interface {
	Request(ctx context.Context, unit uint8, fc byte, pdu []byte) ([]byte, error)
}

// tcpConn is a Modbus TCP client connection: MBAP framing with transaction-id
// matching over one net.Conn, requests strictly sequential (the mutex).
// Sequential is deliberate — Modbus TCP allows pipelining by transaction id,
// but the gateways in the field (Anybus, ADAM) do not, so one outstanding
// request is the safe default.
type tcpConn struct {
	mu      sync.Mutex
	rw      io.ReadWriter
	nc      net.Conn // rw when it is a real socket; nil under test rigs
	timeout time.Duration
	txid    uint16
}

// newTCPConn wraps an established stream. timeout bounds each Request when
// the context carries no earlier deadline; 0 means no per-request timeout.
// Deadlines only apply when rw is a net.Conn — an in-memory test rig has no
// deadline to set, which is fine because it never blocks.
func newTCPConn(rw io.ReadWriter, timeout time.Duration) *tcpConn {
	c := &tcpConn{rw: rw, timeout: timeout}
	if nc, ok := rw.(net.Conn); ok {
		c.nc = nc
	}
	return c
}

// dialTCP opens a Modbus TCP connection. Split out so the driver's
// WithDialer option can substitute it wholesale in tests.
func dialTCP(ctx context.Context, addr string, timeout time.Duration) (*tcpConn, error) {
	d := net.Dialer{Timeout: timeout}
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return newTCPConn(nc, timeout), nil
}

// Close tears the socket down (no-op under a test rig without one).
func (c *tcpConn) Close() error {
	if c.nc != nil {
		return c.nc.Close()
	}
	return nil
}

// appendFrame renders one Modbus TCP ADU: MBAP header + function code + PDU
// data. Pure so the golden framing tests exercise exactly the bytes that hit
// the wire.
func appendFrame(dst []byte, txid uint16, unit uint8, fc byte, pdu []byte) []byte {
	var h [mbapLen + 1]byte
	binary.BigEndian.PutUint16(h[0:2], txid)
	binary.BigEndian.PutUint16(h[2:4], 0) // protocol id: always 0 for Modbus
	binary.BigEndian.PutUint16(h[4:6], uint16(2+len(pdu)))
	h[6] = unit
	h[7] = fc
	return append(append(dst, h[:]...), pdu...)
}

// Request performs one transaction: frame the PDU, send, read and validate
// the matching response. It returns the response data (PDU minus function
// code). Errors:
//
//   - ExceptionError: the device refused the request (per-block failure).
//   - anything else: a transport failure (short read, transaction-id
//     mismatch, timeout) — the connection can no longer be trusted and the
//     caller must reconnect, because a stray or truncated frame desyncs the
//     stream permanently on a protocol with no resync marker.
func (c *tcpConn) Request(ctx context.Context, unit uint8, fc byte, pdu []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(pdu) > maxPDU-1 {
		return nil, fmt.Errorf("modbus: request PDU too large (%d bytes)", len(pdu)+1)
	}

	c.txid++
	txid := c.txid
	if c.nc != nil {
		deadline := time.Time{}
		if c.timeout > 0 {
			deadline = time.Now().Add(c.timeout)
		}
		if d, ok := ctx.Deadline(); ok && (deadline.IsZero() || d.Before(deadline)) {
			deadline = d
		}
		if err := c.nc.SetDeadline(deadline); err != nil {
			return nil, fmt.Errorf("modbus: set deadline: %w", err)
		}
	}

	if _, err := c.rw.Write(appendFrame(nil, txid, unit, fc, pdu)); err != nil {
		return nil, fmt.Errorf("modbus: write: %w", err)
	}

	var hdr [mbapLen]byte
	if _, err := io.ReadFull(c.rw, hdr[:]); err != nil {
		return nil, fmt.Errorf("modbus: read header: %w", shortRead(err))
	}
	if proto := binary.BigEndian.Uint16(hdr[2:4]); proto != 0 {
		return nil, fmt.Errorf("modbus: response protocol id %d (want 0)", proto)
	}
	if rt := binary.BigEndian.Uint16(hdr[0:2]); rt != txid {
		return nil, fmt.Errorf("modbus: transaction id mismatch: got %d want %d", rt, txid)
	}
	length := int(binary.BigEndian.Uint16(hdr[4:6]))
	if length < 2 || length > maxPDU+1 {
		return nil, fmt.Errorf("modbus: response length %d out of range", length)
	}
	body := make([]byte, length-1) // unit id is hdr[6]; body = fc + data
	if _, err := io.ReadFull(c.rw, body); err != nil {
		return nil, fmt.Errorf("modbus: read body: %w", shortRead(err))
	}
	if hdr[6] != unit {
		return nil, fmt.Errorf("modbus: response unit id %d (want %d)", hdr[6], unit)
	}
	switch rfc := body[0]; {
	case rfc == fc|0x80:
		if len(body) < 2 {
			return nil, fmt.Errorf("modbus: truncated exception response")
		}
		return nil, ExceptionError{Code: body[1]}
	case rfc != fc:
		return nil, fmt.Errorf("modbus: response function 0x%02X (want 0x%02X)", rfc, fc)
	}
	return body[1:], nil
}

// idlePeek is how long checkIdle waits for the socket to say something.
// Any pending EOF or socket error comes back from the read syscall at
// once; the wait only bounds the quiet case, so it can be tiny.
const idlePeek = time.Millisecond

// checkIdle asks the socket, between requests, whether the link is still
// there, without sending anything: a short read that times out on a
// healthy, quiet link and fails on one the peer closed (EOF), reset, or
// the kernel gave up on (a TCP keepalive that went unanswered). No request
// is outstanding, so the read cannot take bytes of a response. Bytes that
// arrive anyway are unsolicited and would desync the next transaction's
// framing, so they break the connection too. Under a test rig with no
// socket it reports nothing.
func (c *tcpConn) checkIdle() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nc == nil {
		return nil
	}
	if err := c.nc.SetReadDeadline(time.Now().Add(idlePeek)); err != nil {
		return fmt.Errorf("modbus: idle check: %w", err)
	}
	var b [1]byte
	n, err := c.nc.Read(b[:])
	switch {
	case n > 0:
		return fmt.Errorf("modbus: unsolicited data from the device while idle")
	case err == nil:
		return nil
	case errors.Is(err, os.ErrDeadlineExceeded):
		return nil // quiet and healthy
	case err == io.EOF:
		return fmt.Errorf("modbus: connection closed by the device")
	}
	return fmt.Errorf("modbus: idle check: %w", err)
}

// shortRead normalizes io.EOF mid-frame to io.ErrUnexpectedEOF, so a peer
// that hangs up halfway through a response reads as the short read it is
// rather than a clean end of stream.
func shortRead(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// ── FC helpers ───────────────────────────────────────────────────────────
//
// One function per function code, all over the conn seam. Each builds the
// request PDU, validates the response shape, and returns decoded raw wire
// values ([]uint16 registers, []bool bits) — encode.go turns those into
// engineering values.

// readRegisters is FC 3 (holding) / FC 4 (input): count registers from addr.
func readRegisters(ctx context.Context, c conn, unit uint8, fc byte, addr, count uint16) ([]uint16, error) {
	if count == 0 || count > maxReadRegisters {
		return nil, fmt.Errorf("modbus: read count %d out of range 1..%d", count, maxReadRegisters)
	}
	data, err := c.Request(ctx, unit, fc, u16pair(addr, count))
	if err != nil {
		return nil, err
	}
	if len(data) != 1+2*int(count) || int(data[0]) != 2*int(count) {
		return nil, fmt.Errorf("modbus: FC%d response: got %d bytes for %d registers", fc, len(data), count)
	}
	regs := make([]uint16, count)
	for i := range regs {
		regs[i] = binary.BigEndian.Uint16(data[1+2*i:])
	}
	return regs, nil
}

// readBits is FC 1 (coils) / FC 2 (discrete inputs): count bits from addr.
func readBits(ctx context.Context, c conn, unit uint8, fc byte, addr, count uint16) ([]bool, error) {
	if count == 0 || count > maxReadBits {
		return nil, fmt.Errorf("modbus: read count %d out of range 1..%d", count, maxReadBits)
	}
	data, err := c.Request(ctx, unit, fc, u16pair(addr, count))
	if err != nil {
		return nil, err
	}
	want := (int(count) + 7) / 8
	if len(data) != 1+want || int(data[0]) != want {
		return nil, fmt.Errorf("modbus: FC%d response: got %d bytes for %d bits", fc, len(data), count)
	}
	return unpackBits(data[1:], int(count)), nil
}

// writeSingleCoil is FC 5. The wire value for "on" is the spec's 0xFF00.
func writeSingleCoil(ctx context.Context, c conn, unit uint8, addr uint16, on bool) error {
	var v uint16
	if on {
		v = 0xFF00
	}
	pdu := u16pair(addr, v)
	data, err := c.Request(ctx, unit, FCWriteSingleCoil, pdu)
	if err != nil {
		return err
	}
	return checkEcho(FCWriteSingleCoil, data, pdu)
}

// writeSingleRegister is FC 6.
func writeSingleRegister(ctx context.Context, c conn, unit uint8, addr, value uint16) error {
	pdu := u16pair(addr, value)
	data, err := c.Request(ctx, unit, FCWriteSingleRegister, pdu)
	if err != nil {
		return err
	}
	return checkEcho(FCWriteSingleRegister, data, pdu)
}

// writeMultipleRegisters is FC 16: values to consecutive registers from addr.
func writeMultipleRegisters(ctx context.Context, c conn, unit uint8, addr uint16, values []uint16) error {
	if len(values) == 0 || len(values) > maxWriteRegisters {
		return fmt.Errorf("modbus: write count %d out of range 1..%d", len(values), maxWriteRegisters)
	}
	pdu := u16pair(addr, uint16(len(values)))
	pdu = append(pdu, byte(2*len(values)))
	for _, v := range values {
		pdu = binary.BigEndian.AppendUint16(pdu, v)
	}
	data, err := c.Request(ctx, unit, FCWriteMultipleRegisters, pdu)
	if err != nil {
		return err
	}
	return checkEcho(FCWriteMultipleRegisters, data, u16pair(addr, uint16(len(values))))
}

// writeMultipleCoils is FC 15: values to consecutive coils from addr.
func writeMultipleCoils(ctx context.Context, c conn, unit uint8, addr uint16, values []bool) error {
	if len(values) == 0 || len(values) > maxWriteBits {
		return fmt.Errorf("modbus: write count %d out of range 1..%d", len(values), maxWriteBits)
	}
	packed := packBits(values)
	pdu := u16pair(addr, uint16(len(values)))
	pdu = append(pdu, byte(len(packed)))
	pdu = append(pdu, packed...)
	data, err := c.Request(ctx, unit, FCWriteMultipleCoils, pdu)
	if err != nil {
		return err
	}
	return checkEcho(FCWriteMultipleCoils, data, u16pair(addr, uint16(len(values))))
}

// checkEcho validates a write response: every write FC echoes address and
// count/value. A device that answers something else is mis-speaking the
// protocol and its write cannot be trusted as delivered.
func checkEcho(fc byte, got, want []byte) error {
	if len(got) != len(want) {
		return fmt.Errorf("modbus: FC%d response: %d bytes (want %d)", fc, len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			return fmt.Errorf("modbus: FC%d response echo mismatch", fc)
		}
	}
	return nil
}

// u16pair renders two big-endian uint16s — the (address, count) or
// (address, value) prefix every PDU here starts with.
func u16pair(a, b uint16) []byte {
	return []byte{byte(a >> 8), byte(a), byte(b >> 8), byte(b)}
}

// packBits packs coil values LSB-first per byte, the Modbus bit order:
// values[0] is bit 0 of byte 0.
func packBits(values []bool) []byte {
	out := make([]byte, (len(values)+7)/8)
	for i, v := range values {
		if v {
			out[i/8] |= 1 << (i % 8)
		}
	}
	return out
}

// unpackBits is the inverse: count bits, LSB-first per byte.
func unpackBits(data []byte, count int) []bool {
	out := make([]bool, count)
	for i := range out {
		out[i] = data[i/8]&(1<<(i%8)) != 0
	}
	return out
}
