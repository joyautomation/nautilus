// value.go is the decoder: one varbind off the wire → the hw.Raw every
// binding is applied to. The mapping is the brief's (§3.1) and deliberately
// small — the meaning lives in the manifest's map/eq/scale, not here:
//
//	INTEGER                          → RawInt
//	Counter32 Gauge32 TimeTicks
//	Counter64 (Unsigned32)           → RawUint (so rate: sees a counter)
//	OCTET STRING, printable          → RawString (trailing NULs dropped)
//	OCTET STRING, binary             → RawString "00:1A:2B:…" (a MAC reads as one)
//	OBJECT IDENTIFIER                → RawString "1.3.6.1.4.1.9" (no leading dot,
//	                                   the manifest's own OID spelling)
//	IpAddress                        → RawString "192.0.2.1"
//
// noSuchObject / noSuchInstance / endOfMibView are not values: the caller
// marks the tag Bad.
package snmp

import (
	"bytes"
	"fmt"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/snmp/walk"
)

// RawOf decodes one varbind. ok is false for the exception types and Null,
// which carry no value.
func RawOf(v walk.Varbind) (raw hw.Raw, ok bool, err error) {
	switch v.Type {
	case walk.Integer:
		return hw.RawIntVal(v.Int), true, nil
	case walk.Counter32, walk.Gauge32, walk.TimeTicks, walk.Counter64:
		return hw.RawUintVal(v.Uint), true, nil
	case walk.OctetString, walk.Opaque:
		if walk.Printable(v.Bytes) {
			return hw.RawStringVal(string(bytes.TrimRight(v.Bytes, "\x00"))), true, nil
		}
		return hw.RawStringVal(walk.HexString(v.Bytes)), true, nil
	case walk.ObjectID, walk.IPAddress:
		return hw.RawStringVal(v.Str), true, nil
	case walk.Null, walk.NoSuchObject, walk.NoSuchInstance, walk.EndOfMibView:
		return hw.Raw{}, false, nil
	}
	return hw.Raw{}, false, fmt.Errorf("%s: unsupported type %s", v.OID, v.Type)
}

// PortListRaw is a varbind bound with ports: (a BRIDGE-MIB PortList), read
// as the colon hex of ALL its octets whatever they look like. RawOf would
// hand a PortList whose bytes happen to be printable over as text with its
// NULs trimmed ("12" for 0x31 0x32 00…), which reads back as other ports.
func PortListRaw(v walk.Varbind) (hw.Raw, bool) {
	if v.Type != walk.OctetString && v.Type != walk.Opaque {
		return hw.Raw{}, false
	}
	return hw.RawStringVal(walk.HexString(v.Bytes)), true
}
