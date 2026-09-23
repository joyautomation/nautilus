// client.go is the small exported client surface the commissioning tools
// ride on: `naut modbus browse` reads a live register range, and
// `naut modbus serve` seeds its slave through Encode. The driver itself
// stays on the package-private seams (conn, decodeRegisters) — this file
// only re-exposes what a tool outside the package legitimately needs, so
// the CLI never grows a second Modbus implementation.
package modbus

import (
	"context"
	"fmt"
	"time"
)

// Client is one Modbus TCP connection for tooling: strictly sequential
// requests over a single socket, like the driver's per-source connection.
type Client struct {
	c *tcpConn
}

// Dial opens a Modbus TCP connection. timeout bounds the dial and each
// later request; 0 means no per-request timeout.
func Dial(ctx context.Context, addr string, timeout time.Duration) (*Client, error) {
	c, err := dialTCP(ctx, addr, timeout)
	if err != nil {
		return nil, err
	}
	return &Client{c: c}, nil
}

// Close tears the connection down.
func (cl *Client) Close() error { return cl.c.Close() }

// ReadRegisters reads count registers from addr on a register table
// ("holding" → FC 3, "input" → FC 4). count is capped at 125 by the
// protocol; a larger range takes several calls.
func (cl *Client) ReadRegisters(ctx context.Context, unit uint8, table string, addr, count uint16) ([]uint16, error) {
	var fc byte
	switch table {
	case TableHolding:
		fc = FCReadHoldingRegisters
	case TableInput:
		fc = FCReadInputRegisters
	default:
		return nil, fmt.Errorf("modbus: ReadRegisters on table %q (want holding or input)", table)
	}
	return readRegisters(ctx, cl.c, unit, fc, addr, count)
}

// ReadBits reads count bits from addr on a bit table ("coil" → FC 1,
// "discrete" → FC 2). count is capped at 2000 by the protocol.
func (cl *Client) ReadBits(ctx context.Context, unit uint8, table string, addr, count uint16) ([]bool, error) {
	var fc byte
	switch table {
	case TableCoil:
		fc = FCReadCoils
	case TableDiscrete:
		fc = FCReadDiscreteInputs
	default:
		return nil, fmt.Errorf("modbus: ReadBits on table %q (want coil or discrete)", table)
	}
	return readBits(ctx, cl.c, unit, fc, addr, count)
}

// Decode turns the raw registers behind one binding into its engineering
// value: int64 for unscaled integer kinds, float64 for float kinds or
// anything scaled, bool for bool/bit:N. len(regs) must be exactly
// f.Words(). Scaling is engineering = raw*scale + offset, scale 0 meaning 1.
func Decode(f Format, regs []uint16, wordOrder, byteOrder string, scale, offset float64) (any, error) {
	return decodeRegisters(f, regs, wordOrder, byteOrder, scale, offset)
}

// Encode is the inverse: an engineering value to the raw registers for one
// binding, inverting the scaling and applying word/byte order. For Kind
// "bit" the result carries only that bit — merging into the register's
// other bits is the caller's job.
func Encode(f Format, v any, wordOrder, byteOrder string, scale, offset float64) ([]uint16, error) {
	return encodeRegisters(f, v, wordOrder, byteOrder, scale, offset)
}
