// Package slave is an in-process Modbus TCP server: the eip/logixserver
// precedent for the modbus driver. It serves FC 1–6, 15 and 16 from a
// register/coil store per unit-id — several units on one listener, the way
// an Anybus gateway fronts several Omrons — with configurable exception
// injection and artificial latency, so driver tests exercise every failure
// path on 127.0.0.1 with no build tags, and `naut modbus serve` can
// stand in for a skid of devices on a bench.
package slave

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Server listens for Modbus TCP clients and answers from its units' stores.
// Requests on one connection are handled sequentially (real gateways do not
// pipeline); connections are served concurrently.
type Server struct {
	addr string
	log  *slog.Logger

	mu      sync.Mutex
	units   map[uint8]*Unit
	latency time.Duration
	ln      net.Listener
	conns   map[net.Conn]struct{}
	closed  bool

	wg sync.WaitGroup
}

// NewServer binds nothing yet — call Start. addr "" means "127.0.0.1:0"
// (an ephemeral port for tests; Addr reports what was picked).
func NewServer(addr string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	return &Server{
		addr:  addr,
		log:   log,
		units: map[uint8]*Unit{},
		conns: map[net.Conn]struct{}{},
	}
}

// Unit returns the store for one unit-id, creating it on first use. A
// request addressed to a unit that was never created answers exception 0x0B
// (gateway target device failed to respond) — the same face a gateway shows
// for an absent drop.
func (s *Server) Unit(id uint8) *Unit {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.units[id]
	if !ok {
		u = newUnit()
		s.units[id] = u
	}
	return u
}

// SetLatency delays every response by d — how tests simulate a slow Anybus
// hop or force a client timeout.
func (s *Server) SetLatency(d time.Duration) {
	s.mu.Lock()
	s.latency = d
	s.mu.Unlock()
}

// Start listens and begins serving. It returns once the listener is bound,
// so Addr is immediately valid — the shape a test on :0 needs.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("slave: listen %s: %w", s.addr, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	s.wg.Add(1)
	go s.accept(ln)
	return nil
}

// Addr is the bound listen address ("127.0.0.1:41927"), valid after Start.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return s.addr
	}
	return s.ln.Addr().String()
}

// Stop closes the listener and every live connection, then waits for the
// handlers to drain.
func (s *Server) Stop() {
	s.mu.Lock()
	s.closed = true
	if s.ln != nil {
		s.ln.Close()
	}
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Server) accept(ln net.Listener) {
	defer s.wg.Done()
	for {
		c, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			c.Close()
			return
		}
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go s.serve(c)
	}
}

// serve handles one client connection: MBAP frame in, response out, until
// the peer hangs up or sends something unframeable (there is no way to
// resync a broken byte stream, so we drop the connection — what a real
// device does too).
func (s *Server) serve(c net.Conn) {
	defer s.wg.Done()
	defer func() {
		c.Close()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
	for {
		var hdr [7]byte
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			if err != io.EOF && !errors.Is(err, net.ErrClosed) {
				s.log.Debug("slave: read", "err", err)
			}
			return
		}
		if binary.BigEndian.Uint16(hdr[2:4]) != 0 {
			s.log.Debug("slave: dropping connection: bad protocol id")
			return
		}
		length := int(binary.BigEndian.Uint16(hdr[4:6]))
		if length < 2 || length > 254 {
			s.log.Debug("slave: dropping connection: bad length", "length", length)
			return
		}
		body := make([]byte, length-1)
		if _, err := io.ReadFull(c, body); err != nil {
			return
		}
		txid := binary.BigEndian.Uint16(hdr[0:2])
		unit, fc, data := hdr[6], body[0], body[1:]

		s.mu.Lock()
		latency := s.latency
		u := s.units[unit]
		s.mu.Unlock()
		if latency > 0 {
			time.Sleep(latency)
		}

		var resp []byte
		if u == nil {
			resp = exception(txid, unit, fc, 0x0B) // gateway target failed to respond
		} else if code, ok := u.injected(fc); ok {
			resp = exception(txid, unit, fc, code)
		} else if out, exc := s.dispatch(u, fc, data); exc != 0 {
			resp = exception(txid, unit, fc, exc)
		} else {
			resp = frame(txid, unit, fc, out)
		}
		if _, err := c.Write(resp); err != nil {
			return
		}
	}
}

// dispatch executes one PDU against a unit. It returns the response data
// (after the echoed function code) or a nonzero exception code: 1 illegal
// function, 2 illegal data address, 3 illegal data value — the same codes a
// real device would pick.
func (s *Server) dispatch(u *Unit, fc byte, data []byte) (out []byte, exc byte) {
	switch fc {
	case 0x01, 0x02: // read coils / discrete inputs
		addr, count, ok := addrCount(data)
		if !ok || count == 0 || count > 2000 {
			return nil, 0x03
		}
		if int(addr)+int(count) > 0x10000 {
			return nil, 0x02
		}
		bits := make([]bool, count)
		u.mu.Lock()
		table := u.coils
		if fc == 0x02 {
			table = u.discrete
		}
		for i := range bits {
			bits[i] = table[addr+uint16(i)]
		}
		u.mu.Unlock()
		packed := packBits(bits)
		return append([]byte{byte(len(packed))}, packed...), 0

	case 0x03, 0x04: // read holding / input registers
		addr, count, ok := addrCount(data)
		if !ok || count == 0 || count > 125 {
			return nil, 0x03
		}
		if int(addr)+int(count) > 0x10000 {
			return nil, 0x02
		}
		out = make([]byte, 1+2*int(count))
		out[0] = byte(2 * count)
		u.mu.Lock()
		table := u.holding
		if fc == 0x04 {
			table = u.input
		}
		for i := range int(count) {
			binary.BigEndian.PutUint16(out[1+2*i:], table[addr+uint16(i)])
		}
		u.mu.Unlock()
		return out, 0

	case 0x05: // write single coil
		addr, value, ok := addrCount(data)
		if !ok {
			return nil, 0x03
		}
		switch value {
		case 0x0000:
			u.SetCoil(addr, false)
		case 0xFF00:
			u.SetCoil(addr, true)
		default:
			return nil, 0x03
		}
		return data[:4], 0 // echo

	case 0x06: // write single register
		addr, value, ok := addrCount(data)
		if !ok {
			return nil, 0x03
		}
		u.SetHolding(addr, value)
		return data[:4], 0 // echo

	case 0x0F: // write multiple coils
		addr, count, ok := addrCount(data)
		if !ok || count == 0 || count > 1968 {
			return nil, 0x03
		}
		want := (int(count) + 7) / 8
		if len(data) != 5+want || int(data[4]) != want {
			return nil, 0x03
		}
		if int(addr)+int(count) > 0x10000 {
			return nil, 0x02
		}
		u.mu.Lock()
		for i := range int(count) {
			u.coils[addr+uint16(i)] = data[5+i/8]&(1<<(i%8)) != 0
		}
		u.mu.Unlock()
		return data[:4], 0

	case 0x10: // write multiple registers
		addr, count, ok := addrCount(data)
		if !ok || count == 0 || count > 123 {
			return nil, 0x03
		}
		if len(data) != 5+2*int(count) || int(data[4]) != 2*int(count) {
			return nil, 0x03
		}
		if int(addr)+int(count) > 0x10000 {
			return nil, 0x02
		}
		u.mu.Lock()
		for i := range int(count) {
			u.holding[addr+uint16(i)] = binary.BigEndian.Uint16(data[5+2*i:])
		}
		u.mu.Unlock()
		return data[:4], 0
	}
	return nil, 0x01 // illegal function
}

// addrCount pulls the (address, count-or-value) prefix every PDU here
// starts with.
func addrCount(data []byte) (addr, count uint16, ok bool) {
	if len(data) < 4 {
		return 0, 0, false
	}
	return binary.BigEndian.Uint16(data[0:2]), binary.BigEndian.Uint16(data[2:4]), true
}

// frame renders a normal response ADU.
func frame(txid uint16, unit, fc byte, data []byte) []byte {
	out := make([]byte, 8+len(data))
	binary.BigEndian.PutUint16(out[0:2], txid)
	binary.BigEndian.PutUint16(out[4:6], uint16(2+len(data)))
	out[6] = unit
	out[7] = fc
	copy(out[8:], data)
	return out
}

// exception renders an exception response: function code with the high bit
// set, then the code.
func exception(txid uint16, unit, fc, code byte) []byte {
	return frame(txid, unit, fc|0x80, []byte{code})
}

// packBits packs LSB-first per byte, the Modbus bit order.
func packBits(values []bool) []byte {
	out := make([]byte, (len(values)+7)/8)
	for i, v := range values {
		if v {
			out[i/8] |= 1 << (i % 8)
		}
	}
	return out
}
