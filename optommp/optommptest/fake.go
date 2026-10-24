// Package optommptest is an in-process OptoMMP device for tests: a sparse
// memory map that answers block reads and writes over any net.Conn (a
// net.Pipe end, or a loopback listener for code that dials). Its addresses
// are spelled out here independently of package optommp, so a test is
// checked against the guide (form 1465) and the bench, not against the
// driver's own constants.
package optommptest

import (
	"encoding/binary"
	"io"
	"math"
	"net"
	"sync"
	"testing"
)

// The memory-map addresses the fake acts on (form 1465).
const (
	addrUnitDescription = 0xF0300080
	addrFirmware        = 0xF030001C
	addrChannelConfig   = 0xF0100000
	addrPackedValues    = 0xF1000300
	addrPackedState     = 0xF1000B00
	addrPackedPresent   = 0xF1000BC0
	addrDigitalPacked   = 0xF1001900
	addrDigitalMOMO     = 0xF1001A00
	addrAnalogWrite     = 0xF02A0000
	addrDigitalClear    = 0xF02E0000
	addrScratchBits     = 0xF0D80000
	addrScratchOn       = 0xF0D80400

	configModuleStride  = 0x3000
	configChannelStride = 0xC0
	digitalModuleStride = 0x10
	momoModuleStride    = 8
	valuesModuleStride  = 0x80
	pointModuleStride   = 0x1000
	pointChannelStride  = 0x40
	clearModuleStride   = 0x600
	clearChannelStride  = 0x18
	clearOnLatch        = 4
	clearOffLatch       = 8
	maxModules          = 16
	maxChannels         = 32

	tcodeWriteBlockRequest = 1
	tcodeWriteResponse     = 2
	tcodeReadBlockRequest  = 5
	tcodeReadBlockResponse = 7
)

// Device answers OptoMMP block reads and writes from a sparse memory
// map — the bench GRV-EPIC-PR1's chassis, as Scan saw it on 2026-10-23 —
// and acts out the side effects the driver relies on, as the bench unit
// did on 2026-10-24: must-on/must-off writes switch digital state and set
// latches, read-and-clear reads clear them, scratchpad masks set bits, an
// analog write lands in the packed values.
type Device struct {
	mu   sync.Mutex
	mem  map[uint32]byte
	reqs int // requests served
	// drop, when set, closes the connection on the next request.
	drop bool
}

// NewBench is the bench GRV-EPIC-PR1: slot 0 GRV-IACDCTTL-24 (DI 3 on),
// slot 1 GRV-ODCI-12 (DO 11 on), slot 2 GRV-IICTD-12 (ch 0 20.19 °C),
// slot 3 GRV-IV-24 at ±160 V (ch 0 8.4 V); every other value NaN.
func NewBench() *Device {
	d := &Device{mem: map[uint32]byte{}}
	d.put(addrUnitDescription, []byte("GRV-EPIC-PR1"))
	d.put(addrFirmware, []byte{3, 6, 1, 0})
	module := func(slot int, mtype uint32, chans int, ctype uint32) {
		for i := 0; i < chans; i++ {
			row := addrChannelConfig + uint32(slot)*configModuleStride + uint32(i)*configChannelStride
			d.putU32(row, mtype)
			d.putU32(row+4, ctype)
		}
	}
	module(0, 0x50000005, 24, 0x50000000) // GRV-IACDCTTL-24
	module(1, 0x90000015, 12, 0x90000000) // GRV-ODCI-12
	module(2, 0x6000001F, 12, 0x60000031) // GRV-IICTD-12, 12 of 24 rows configured
	module(3, 0x60000002, 24, 0x60000024) // GRV-IV-24 at ±160 V
	// Presence bits for the digital modules; state: DI 3 on, DO 11 on.
	setBit := func(base uint32, bit int) {
		w := base + uint32(bit/32)*4
		v := binary.BigEndian.Uint32([]byte{d.mem[w], d.mem[w+1], d.mem[w+2], d.mem[w+3]})
		d.putU32(w, v|1<<(uint(bit)%32))
	}
	for i := 0; i < 24; i++ {
		setBit(addrPackedPresent, i)
	}
	for i := 0; i < 12; i++ {
		setBit(addrPackedPresent, 32+i)
	}
	setBit(addrPackedState, 3)
	setBit(addrPackedState, 32+11)
	// The expanded digital packed data mirrors state; every slot without a
	// digital module reads quality FFFFFFFF, as on the bench.
	d.putU32(addrDigitalPacked+0*digitalModuleStride, 1<<3)
	d.putU32(addrDigitalPacked+1*digitalModuleStride, 1<<11)
	for slot := 2; slot < maxModules; slot++ {
		d.putU32(addrDigitalPacked+uint32(slot)*digitalModuleStride+4, 0xFFFFFFFF)
	}
	// Values: slot 2 ch 0 = 20.19 °C, slot 3 ch 0 = 8.4 V, the rest NaN.
	for bit := 0; bit < maxModules*maxChannels; bit++ {
		d.putU32(addrPackedValues+uint32(bit)*4, math.Float32bits(float32(math.NaN())))
	}
	d.putU32(addrPackedValues+(2*32+0)*4, math.Float32bits(20.19))
	d.putU32(addrPackedValues+(3*32+0)*4, math.Float32bits(8.4))
	return d
}

func (d *Device) put(addr uint32, b []byte) {
	for i, v := range b {
		d.mem[addr+uint32(i)] = v
	}
}

func (d *Device) putU32(addr uint32, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	d.put(addr, b[:])
}

func (d *Device) u32(addr uint32) uint32 {
	return binary.BigEndian.Uint32([]byte{d.mem[addr], d.mem[addr+1], d.mem[addr+2], d.mem[addr+3]})
}

// U32 reads a quadlet of the device's memory.
func (d *Device) U32(addr uint32) uint32 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.u32(addr)
}

// PutU32 writes a quadlet of the device's memory, as the processor would.
func (d *Device) PutU32(addr uint32, v uint32) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.putU32(addr, v)
}

// setDigital switches one channel the way the module would: state in both
// packed areas, the matching latch on a transition.
func (d *Device) setDigital(slot, ch int, on bool) {
	row := addrDigitalPacked + uint32(slot)*digitalModuleStride
	state := d.u32(row)
	bit := uint32(1) << uint(ch)
	was := state&bit != 0
	if on {
		state |= bit
	} else {
		state &^= bit
	}
	d.putU32(row, state)
	d.putU32(addrPackedState+uint32(slot)*4, state)
	switch {
	case on && !was:
		d.putU32(row+8, d.u32(row+8)|bit)
	case !on && was:
		d.putU32(row+12, d.u32(row+12)|bit)
	}
}

// effects acts out a write's side effects.
func (d *Device) effects(addr uint32, data []byte) {
	switch {
	case addr >= addrDigitalMOMO && addr < addrDigitalMOMO+0x80:
		for off := 0; off+8 <= len(data); off += 8 {
			slot := int(addr-addrDigitalMOMO)/momoModuleStride + off/momoModuleStride
			on := binary.BigEndian.Uint32(data[off:])
			offm := binary.BigEndian.Uint32(data[off+4:])
			for ch := 0; ch < maxChannels; ch++ {
				bit := uint32(1) << uint(ch)
				switch {
				case on&bit != 0 && offm&bit == 0:
					d.setDigital(slot, ch, true)
				case on&bit != 0 || offm&bit != 0:
					d.setDigital(slot, ch, false) // both set: off, as the guide says
				}
			}
		}
	case addr == addrScratchOn && len(data) == 16:
		bits := binary.BigEndian.Uint64([]byte{d.mem[addrScratchBits], d.mem[addrScratchBits+1], d.mem[addrScratchBits+2],
			d.mem[addrScratchBits+3], d.mem[addrScratchBits+4], d.mem[addrScratchBits+5], d.mem[addrScratchBits+6], d.mem[addrScratchBits+7]})
		bits |= binary.BigEndian.Uint64(data)
		bits &^= binary.BigEndian.Uint64(data[8:])
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], bits)
		d.put(addrScratchBits, b[:])
	case addr >= addrAnalogWrite && addr < addrAnalogWrite+0x40000 && (addr-addrAnalogWrite)%pointChannelStride == 0:
		slot := (addr - addrAnalogWrite) / pointModuleStride
		ch := (addr - addrAnalogWrite) % pointModuleStride / pointChannelStride
		d.put(addrPackedValues+slot*valuesModuleStride+ch*4, data[:4])
	}
}

// readEffects clears what a read-and-clear read returned.
func (d *Device) readEffects(addr uint32, n int) {
	if addr < addrDigitalClear || addr >= addrDigitalClear+0x18000 {
		return
	}
	for a := addr; a < addr+uint32(n); a += 4 {
		off := a - addrDigitalClear
		slot, ch, field := off/clearModuleStride, off%clearModuleStride/clearChannelStride, off%clearChannelStride
		row := addrDigitalPacked + slot*digitalModuleStride
		switch field {
		case clearOnLatch:
			d.putU32(row+8, d.u32(row+8)&^(1<<ch))
		case clearOffLatch:
			d.putU32(row+12, d.u32(row+12)&^(1<<ch))
		}
	}
}

// Serve answers one connection until it closes.
func (d *Device) Serve(conn net.Conn) {
	defer conn.Close()
	for {
		var hdr [16]byte
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return
		}
		addr := binary.BigEndian.Uint32(hdr[8:12])
		n := int(binary.BigEndian.Uint16(hdr[12:14]))
		d.mu.Lock()
		d.reqs++
		if d.drop {
			d.drop = false
			d.mu.Unlock()
			return
		}
		switch hdr[3] >> 4 {
		case tcodeReadBlockRequest:
			resp := make([]byte, 16+n)
			resp[3] = tcodeReadBlockResponse << 4
			binary.BigEndian.PutUint16(resp[12:14], uint16(n))
			for i := 0; i < n; i++ {
				resp[16+i] = d.mem[addr+uint32(i)]
			}
			d.readEffects(addr, n)
			d.mu.Unlock()
			conn.Write(resp)
		case tcodeWriteBlockRequest:
			d.mu.Unlock()
			data := make([]byte, n)
			io.ReadFull(conn, data)
			d.mu.Lock()
			d.put(addr, data)
			d.effects(addr, data)
			d.mu.Unlock()
			resp := make([]byte, 12)
			resp[3] = tcodeWriteResponse << 4
			conn.Write(resp)
		default:
			d.mu.Unlock()
		}
	}
}

// SetDigital switches one channel as the module would (state, latches).
func (d *Device) SetDigital(slot, ch int, on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.setDigital(slot, ch, on)
}

// Drop makes the device close the connection on its next request.
func (d *Device) Drop() {
	d.mu.Lock()
	d.drop = true
	d.mu.Unlock()
}

// Requests is how many requests the device has served.
func (d *Device) Requests() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reqs
}

// Listen serves the device on a loopback TCP port until the test ends and
// returns the port.
func (d *Device) Listen(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go d.Serve(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}
