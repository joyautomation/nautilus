// Package optommp speaks OptoMMP, the memory-mapped protocol every Opto 22
// groov EPIC, groov RIO and SNAP PAC answers on TCP 2001: a 16-byte
// IEEE 1394-style header, then a block of bytes read from or written to an
// address in the device's memory map. It is both the commissioning seam
// for a groov chassis — `naut optommp browse` lists the modules and
// channels the processor sees, `naut optommp import` turns that chassis
// into an optommp_manifest.yaml — and the driver that polls it (Driver,
// `driver: {type: optommp}`): every channel's state or value with
// per-channel quality, outputs, counters, latches and the scratchpad. The
// older projection onto the processor's Modbus/TCP map (modbusmap.go,
// `import --modbus`) stays for projects that want the modbus driver.
// Opto 22's spellings (module part numbers, channel type codes,
// memory-map addresses) stop at this package.
//
// Pure stdlib. Addresses and layouts are from the OptoMMP Protocol Guide
// (Opto 22 form 1465) and the Modbus/TCP Protocol Guide (form 1678); the
// chassis tables in modules.go are the groov I/O rows of form 1465's
// "groov I/O Module Types and Channel Types".
package optommp

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// DefaultPort is the OptoMMP TCP port.
const DefaultPort = 2001

// MaxBlock is the largest block one request should move. The protocol
// allows 2034 bytes, but a GRV-EPIC-PR1 on the bench stopped answering a
// 0x800-byte read and answered 0x600 in under a millisecond, so reads are
// chunked at this size (form 1465 recommends "about 1400 bytes or less").
const MaxBlock = 0x600

// Transaction codes (form 1465, "OptoMMP packet format").
const (
	tcodeWriteBlockRequest = 1
	tcodeWriteResponse     = 2
	tcodeReadBlockRequest  = 5
	tcodeReadBlockResponse = 7
)

// Error is a non-zero response code from the device: the request was
// understood and refused (bad address, bad length, access denied).
type Error struct {
	Addr  uint32
	RCode uint8
}

func (e Error) Error() string {
	return fmt.Sprintf("optommp: device refused access to 0x%08X (rcode %d)", e.Addr, e.RCode)
}

// Client is one TCP connection to a device. Requests are sequential; the
// protocol has a transaction label but devices answer in order.
//
// It also keeps the bound the driver needs for the communication
// watchdog. The device sees request n arrive somewhere between its send
// and its answer, so the time from one request's send to the next one's
// answer is the longest the device can have gone without a request
// between them. A process stall, a network drop that TCP rides out and a
// slow scan all show up in it; quiet reports the largest.
type Client struct {
	conn    net.Conn
	timeout time.Duration
	hdr     [16]byte
	sent    time.Time     // when the last request went out (zero: none yet)
	gap     time.Duration // the longest send-to-next-answer span since quiet
}

// Dial connects to host:port (port 0 means 2001).
func Dial(ctx context.Context, host string, port int, timeout time.Duration) (*Client, error) {
	if port == 0 {
		port = DefaultPort
	}
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return nil, fmt.Errorf("optommp: %w", err)
	}
	return NewClient(conn, timeout), nil
}

// NewClient wraps an open connection (tests hand it a net.Pipe end).
func NewClient(conn net.Conn, timeout time.Duration) *Client {
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	return &Client{conn: conn, timeout: timeout}
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// LocalAddr / RemoteAddr are the connection's ends: the address a stream
// is sent to, and the only one it is accepted from.
func (c *Client) LocalAddr() net.Addr  { return c.conn.LocalAddr() }
func (c *Client) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

// Read reads n bytes at addr, chunking at MaxBlock.
func (c *Client) Read(addr uint32, n int) ([]byte, error) {
	out := make([]byte, 0, n)
	for len(out) < n {
		chunk := min(n-len(out), MaxBlock)
		b, err := c.readBlock(addr+uint32(len(out)), chunk)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out, nil
}

// ReadUint32 reads one big-endian quadlet.
func (c *Client) ReadUint32(addr uint32) (uint32, error) {
	b, err := c.readBlock(addr, 4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

// Write writes data at addr in one block (callers keep data ≤ MaxBlock).
func (c *Client) Write(addr uint32, data []byte) error {
	if len(data) == 0 || len(data) > MaxBlock {
		return fmt.Errorf("optommp: write of %d bytes (want 1..%d)", len(data), MaxBlock)
	}
	pkt := make([]byte, 16+len(data))
	header(pkt[:16], tcodeWriteBlockRequest, addr, len(data))
	copy(pkt[16:], data)
	prev := c.send()
	if _, err := c.conn.Write(pkt); err != nil {
		return fmt.Errorf("optommp: %w", err)
	}
	// Write response: 12 bytes, rcode in the upper nibble of byte 6.
	resp := make([]byte, 12)
	if _, err := io.ReadFull(c.conn, resp); err != nil {
		return fmt.Errorf("optommp: %w", err)
	}
	c.answered(prev)
	if tc := resp[3] >> 4; tc != tcodeWriteResponse {
		return fmt.Errorf("optommp: unexpected tcode %d in write response", tc)
	}
	if rc := resp[6] >> 4; rc != 0 {
		return Error{Addr: addr, RCode: rc}
	}
	return nil
}

func (c *Client) readBlock(addr uint32, n int) ([]byte, error) {
	header(c.hdr[:], tcodeReadBlockRequest, addr, n)
	prev := c.send()
	if _, err := c.conn.Write(c.hdr[:]); err != nil {
		return nil, fmt.Errorf("optommp: %w", err)
	}
	var resp [16]byte
	if _, err := io.ReadFull(c.conn, resp[:]); err != nil {
		return nil, fmt.Errorf("optommp: %w", err)
	}
	c.answered(prev)
	if tc := resp[3] >> 4; tc != tcodeReadBlockResponse {
		return nil, fmt.Errorf("optommp: unexpected tcode %d in read response", tc)
	}
	if rc := resp[6] >> 4; rc != 0 {
		return nil, Error{Addr: addr, RCode: rc}
	}
	got := int(binary.BigEndian.Uint16(resp[12:14]))
	if got != n {
		return nil, fmt.Errorf("optommp: asked %d bytes at 0x%08X, device answers %d", n, addr, got)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(c.conn, data); err != nil {
		return nil, fmt.Errorf("optommp: %w", err)
	}
	return data, nil
}

// send arms the deadline for a request about to go out and records when
// it did; it returns when the request before it went out.
func (c *Client) send() (prev time.Time) {
	prev, c.sent = c.sent, time.Now()
	c.conn.SetDeadline(c.sent.Add(c.timeout))
	return prev
}

// answered records the span from the previous request's send (prev) to
// this one's answer, now.
func (c *Client) answered(prev time.Time) {
	if prev.IsZero() {
		return
	}
	if g := time.Since(prev); g > c.gap {
		c.gap = g
	}
}

// quiet is the longest the device may have gone without a request since
// the last call: the largest send-to-next-answer span, or the time since
// the last send when that is longer — a request about to go out arrives
// no sooner than now. It starts over at each call.
func (c *Client) quiet() time.Duration {
	g := c.gap
	c.gap = 0
	if !c.sent.IsZero() {
		g = max(g, time.Since(c.sent))
	}
	return g
}

// header fills a 16-byte request header: destination id 0, transaction
// label 0, tcode, source id 0, the 48-bit destination offset (0xFFFF then
// the 32-bit map address), data length, extended tcode 0.
func header(b []byte, tcode uint8, addr uint32, n int) {
	clear(b[:16])
	b[3] = tcode << 4
	b[6], b[7] = 0xFF, 0xFF
	binary.BigEndian.PutUint32(b[8:12], addr)
	binary.BigEndian.PutUint16(b[12:14], uint16(n))
}
