package wire

import (
	"encoding/binary"
	"net"
)

// LLDP constants. A PROFINET device announces itself with LLDP so an
// engineering tool's topology check can match its port to the partner
// port it was configured with.
const (
	EtherTypeLLDP uint16 = 0x88CC

	LLDPTLVEnd             uint8 = 0
	LLDPTLVChassisID       uint8 = 1
	LLDPTLVPortID          uint8 = 2
	LLDPTLVTTL             uint8 = 3
	LLDPTLVPortDesc        uint8 = 4
	LLDPTLVSysName         uint8 = 5
	LLDPTLVSysDesc         uint8 = 6
	LLDPTLVSysCapabilities uint8 = 7
	LLDPTLVMgmtAddr        uint8 = 8
	LLDPTLVOrgSpecific     uint8 = 127

	// PNIOSubtypePortStatus and PNIOSubtypeChassisMAC are the PROFINET
	// organisation-specific subtypes (OUI 00:0E:CF).
	PNIOSubtypePortStatus uint8 = 0x02
	PNIOSubtypeChassisMAC uint8 = 0x05

	// LLDPPortID is the port's name. PROFINET names a device's ports
	// "port-001", "port-002", …; a Siemens topology check reports "wrong
	// partner port" for anything derived from the host interface name.
	LLDPPortID = "port-001"
)

// LLDPMulticastMAC is the nearest-bridge LLDP destination.
var LLDPMulticastMAC = net.HardwareAddr{0x01, 0x80, 0xC2, 0x00, 0x00, 0x0E}

var pnioOUI = [3]byte{0x00, 0x0E, 0xCF}

// LLDPInfo is what one LLDP announcement says about the station.
type LLDPInfo struct {
	ChassisMAC  net.HardwareAddr
	StationName string
	SysDesc     string
	IP          net.IP // management address; nil or 0.0.0.0 omits it
	TTL         uint16 // seconds; 0 = 20
}

// BuildLLDP returns the LLDPDU (the Ethernet payload) for info.
func BuildLLDP(info LLDPInfo) []byte {
	var b []byte
	b = appendTLV(b, LLDPTLVChassisID, append([]byte{0x04}, info.ChassisMAC...)) // subtype 4: MAC
	b = appendTLV(b, LLDPTLVPortID, append([]byte{0x07}, LLDPPortID...))         // subtype 7: local
	ttl := info.TTL
	if ttl == 0 {
		ttl = 20
	}
	b = appendTLV(b, LLDPTLVTTL, binary.BigEndian.AppendUint16(nil, ttl))
	b = appendTLV(b, LLDPTLVPortDesc, []byte(LLDPPortID))
	b = appendTLV(b, LLDPTLVSysName, []byte(info.StationName))
	b = appendTLV(b, LLDPTLVSysDesc, []byte(info.SysDesc))
	// System capabilities: station only, enabled.
	b = appendTLV(b, LLDPTLVSysCapabilities, []byte{0x00, 0x80, 0x00, 0x80})
	if ip4 := info.IP.To4(); ip4 != nil && !ip4.Equal(net.IPv4zero) {
		mgmt := []byte{5, 1} // address string length (subtype + 4), subtype IPv4
		mgmt = append(mgmt, ip4...)
		mgmt = append(mgmt, 2, 0, 0, 0, 1, 0) // ifIndex numbering, interface 1, no OID
		b = appendTLV(b, LLDPTLVMgmtAddr, mgmt)
	}
	// PROFINET port status: RTClass2 and RTClass3 port status, both 0
	// (not supported). Eight bytes: OUI, subtype, two Unsigned16.
	b = appendTLV(b, LLDPTLVOrgSpecific, []byte{pnioOUI[0], pnioOUI[1], pnioOUI[2], PNIOSubtypePortStatus, 0, 0, 0, 0})
	chassis := []byte{pnioOUI[0], pnioOUI[1], pnioOUI[2], PNIOSubtypeChassisMAC}
	b = appendTLV(b, LLDPTLVOrgSpecific, append(chassis, info.ChassisMAC...))
	return append(b, 0x00, 0x00) // End
}

// LLDPTLV is one decoded TLV.
type LLDPTLV struct {
	Type  uint8
	Value []byte
}

// ParseLLDP splits an LLDPDU into its TLVs, up to and excluding End. The
// values alias data.
func ParseLLDP(data []byte) ([]LLDPTLV, error) {
	var out []LLDPTLV
	for len(data) >= 2 {
		h := binary.BigEndian.Uint16(data)
		typ, n := uint8(h>>9), int(h&0x01FF)
		if typ == LLDPTLVEnd {
			return out, nil
		}
		if len(data) < 2+n {
			return nil, errShort("LLDP TLV", 2+n, len(data))
		}
		out = append(out, LLDPTLV{Type: typ, Value: data[2 : 2+n]})
		data = data[2+n:]
	}
	return nil, errShort("LLDP End TLV", 2, len(data))
}

func appendTLV(b []byte, typ uint8, value []byte) []byte {
	b = binary.BigEndian.AppendUint16(b, uint16(typ)<<9|uint16(len(value)&0x01FF))
	return append(b, value...)
}
