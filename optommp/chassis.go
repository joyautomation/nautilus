package optommp

// chassis.go reads what the processor knows about its own chassis: the
// module and channel type in every slot, the channel-present mask, and the
// current values — one Scan, a few block reads. Addresses are form 1465's:
//
//	F0300080  unit description (part number string, 32 bytes)
//	F030001C  firmware version (four bytes a.b.c.d)
//	F0100000  (expanded) channel configuration: 64 channels × 0xC0 bytes
//	          per module, 64 modules; +0 module type, +4 channel type,
//	          +8 feature — "Analog & Digital Channel Configuration"
//	F1000300  packed channel values, 16 modules × 32 channels × 4 bytes:
//	          analog in engineering units (float), digital feature data
//	F1000B00  packed digital state, 512 bits
//	F1000BC0  packed channel-present mask, 512 bits
//
// A groov chassis holds at most 16 modules of at most 32 channels (the
// 64-channel GRV-IVAPM-3 aside), which is the packed areas' shape; the scan
// stays inside it.

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

const (
	addrUnitDescription = 0xF0300080
	addrFirmware        = 0xF030001C
	addrChannelConfig   = 0xF0100000
	addrPackedValues    = 0xF1000300
	addrPackedState     = 0xF1000B00
	addrPackedPresent   = 0xF1000BC0

	configModuleStride  = 0x3000 // 64 channels × 0xC0
	configChannelStride = 0xC0

	// MaxModules and MaxChannels bound the packed areas (and so the scan).
	MaxModules  = 16
	MaxChannels = 32
)

// Chassis is one processor and the modules it reports.
type Chassis struct {
	Part     string // "GRV-EPIC-PR1"
	Firmware string // "3.6.1.0" shaped; what the status area says
	Modules  []Module
}

// Module is one occupied slot.
type Module struct {
	Slot     int
	Type     uint32 // module type code
	Part     string // part number, or "module 0x…" when unknown
	Channels []Channel
}

// Channel is one configured channel on a module. Unconfigured channels
// (type 0) are not listed — the processor reports no value for them.
type Channel struct {
	Index   int
	Type    uint32 // channel type code
	Kind    Kind
	Desc    string // range / probe type from the table
	Unit    string
	Lo, Hi  float64 // engineering range: the configured scale, else the table's
	Feature uint32  // digital feature (counter, totalizer …), 0 when none
	Name    string  // channel name stored in the processor's map, often empty
	Present bool    // the channel-present bit; digital modules set it
	State   bool    // digital state at scan time
	Value   float64 // analog engineering value at scan time (NaN when none)
}

// Offsets inside one channel-configuration row (0xC0 bytes).
const (
	cfgModuleType  = 0x00
	cfgChannelType = 0x04
	cfgFeature     = 0x08
	cfgHighScale   = 0x14
	cfgLowScale    = 0x18
	cfgName        = 0x30 // 0x33 bytes, zero-terminated
	cfgUnits       = 0x78 // 0x10 bytes, zero-terminated
)

// Scan reads the chassis. One read per module for the configuration (so an
// unknown module type still lands in the listing) plus three packed reads
// for values, state and presence.
func Scan(c *Client) (Chassis, error) {
	var ch Chassis
	if b, err := c.Read(addrUnitDescription, 32); err == nil {
		ch.Part = strings.TrimRight(string(b), "\x00")
	} else {
		return ch, err
	}
	if b, err := c.Read(addrFirmware, 4); err == nil {
		ch.Firmware = fmt.Sprintf("%d.%d.%d.%d", b[0], b[1], b[2], b[3])
	}
	values, err := c.Read(addrPackedValues, MaxModules*MaxChannels*4)
	if err != nil {
		return ch, err
	}
	state, err := c.Read(addrPackedState, MaxModules*MaxChannels/8)
	if err != nil {
		return ch, err
	}
	present, err := c.Read(addrPackedPresent, MaxModules*MaxChannels/8)
	if err != nil {
		return ch, err
	}

	for slot := 0; slot < MaxModules; slot++ {
		// The first 32 channels' config rows in one read: 32 × 0xC0 = 0x1800.
		cfg, err := c.Read(addrChannelConfig+uint32(slot)*configModuleStride, MaxChannels*configChannelStride)
		if err != nil {
			return ch, fmt.Errorf("slot %d: %w", slot, err)
		}
		mtype := binary.BigEndian.Uint32(cfg[cfgModuleType:])
		m := Module{Slot: slot, Type: mtype, Part: ModuleName(mtype)}
		for i := 0; i < MaxChannels; i++ {
			row := cfg[i*configChannelStride:]
			ctype := binary.BigEndian.Uint32(row[cfgChannelType:])
			if ctype == 0 {
				continue
			}
			bit := slot*MaxChannels + i
			chn := Channel{
				Index:   i,
				Type:    ctype,
				Kind:    KindOf(ctype),
				Feature: binary.BigEndian.Uint32(row[cfgFeature:]),
				Name:    cString(row[cfgName : cfgName+0x33]),
				Present: packedBit(present, bit),
				State:   packedBit(state, bit),
				Value:   math.NaN(),
			}
			if info, ok := LookupChannel(ctype); ok {
				chn.Desc, chn.Unit, chn.Lo, chn.Hi = info.Desc, info.Unit, info.Lo, info.Hi
			} else {
				chn.Desc = fmt.Sprintf("channel type 0x%08X", ctype)
			}
			if chn.Kind == KindAI || chn.Kind == KindAO {
				// The processor's own scale and units win over the table:
				// groov Manage can rescale a channel (a 4-20 mA loop read as
				// 0-100 %), and the map then reports the rescaled values.
				hi := float64(math.Float32frombits(binary.BigEndian.Uint32(row[cfgHighScale:])))
				lo := float64(math.Float32frombits(binary.BigEndian.Uint32(row[cfgLowScale:])))
				if hi != lo && !math.IsNaN(hi) && !math.IsNaN(lo) {
					chn.Lo, chn.Hi = lo, hi
				}
				if u := cString(row[cfgUnits : cfgUnits+0x10]); u != "" {
					chn.Unit = u
				}
			}
			if chn.Kind == KindAI || chn.Kind == KindAO {
				chn.Value = float64(math.Float32frombits(binary.BigEndian.Uint32(values[bit*4:])))
			}
			m.Channels = append(m.Channels, chn)
		}
		if mtype == 0 && len(m.Channels) == 0 {
			continue // empty slot
		}
		ch.Modules = append(ch.Modules, m)
	}
	return ch, nil
}

// packedBit reads bit i of a packed mask: the areas are big-endian
// quadlets, bit 0 of each quadlet the least significant.
func packedBit(mask []byte, i int) bool {
	w := binary.BigEndian.Uint32(mask[(i/32)*4:])
	return w>>(uint(i)%32)&1 == 1
}

// cString is a zero-terminated (or zero-padded) string field, trimmed.
func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			b = b[:i]
			break
		}
	}
	return strings.TrimSpace(string(b))
}

// String renders the chassis as the browse listing.
func (ch Chassis) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s firmware %s — %d module(s)\n", ch.Part, ch.Firmware, len(ch.Modules))
	for _, m := range ch.Modules {
		fmt.Fprintf(&b, "slot %2d  %-16s type 0x%08X  %d channel(s)\n", m.Slot, m.Part, m.Type, len(m.Channels))
		for _, c := range m.Channels {
			val := ""
			switch c.Kind {
			case KindDI, KindDO:
				val = fmt.Sprintf("state %t", c.State)
			case KindAI, KindAO:
				if math.IsNaN(c.Value) {
					val = "value NaN (open / unwired)"
				} else {
					val = fmt.Sprintf("value %g %s", c.Value, c.Unit)
				}
			}
			fmt.Fprintf(&b, "   ch %2d  %-4s %-36s %s\n", c.Index, c.Kind, c.Desc, val)
		}
	}
	return b.String()
}
