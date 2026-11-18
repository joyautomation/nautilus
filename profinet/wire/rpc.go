package wire

import (
	"encoding/binary"
	"fmt"
)

// PROFINET IO CM (Connection Management) UDP port.
const PNIOCMPort = 34964

// PROFINET RT UDP port reservation, advertised as responder_udp_rt_port in
// ARBlockRes (distinct from PNIOCMPort; matches PF_UDP_UNICAST_PORT in p-net).
const ProfinetRTUDPPort uint16 = 0x8892

// DCE/RPC connectionless version.
const RPCVersionCL = 4

// DCE/RPC packet types.
const (
	RPCPTypeRequest  uint8 = 0x00
	RPCPTypeResponse uint8 = 0x02
	RPCPTypeReject   uint8 = 0x06
	RPCPTypeNoCall   uint8 = 0x07
	RPCPTypeFack     uint8 = 0x09
)

// DCE/RPC flags1 bits (bit 0 is reserved for implementation).
const (
	RPCFlagLastFrag   uint8 = 0x02
	RPCFlagFrag       uint8 = 0x04
	RPCFlagNoFack     uint8 = 0x08
	RPCFlagMaybe      uint8 = 0x10
	RPCFlagIdempotent uint8 = 0x20
	RPCFlagBroadcast  uint8 = 0x40
)

// PNIO RPC operation numbers.
const (
	RPCOpConnect      uint16 = 0
	RPCOpRelease      uint16 = 1
	RPCOpRead         uint16 = 2
	RPCOpWrite        uint16 = 3
	RPCOpControl      uint16 = 4
	RPCOpReadImplicit uint16 = 5
)

// Well-known PROFINET interface UUIDs (canonical big-endian byte order).
var (
	// PNIO Interface UUID: DEA00001-6C97-11D1-8271-00A02442DF7D
	UUIDPNIOInterface = [16]byte{
		0xDE, 0xA0, 0x00, 0x01,
		0x6C, 0x97,
		0x11, 0xD1,
		0x82, 0x71,
		0x00, 0xA0, 0x24, 0x42, 0xDF, 0x7D,
	}

	// PNIO Controller Interface UUID: DEA00002-6C97-11D1-8271-00A02442DF7D
	// (used when a device sends a CControl RPC request to the controller).
	UUIDPNIOControllerInterface = [16]byte{
		0xDE, 0xA0, 0x00, 0x02,
		0x6C, 0x97,
		0x11, 0xD1,
		0x82, 0x71,
		0x00, 0xA0, 0x24, 0x42, 0xDF, 0x7D,
	}

	// EPM (Endpoint Mapper) UUID: E1AF8308-5D1F-11C9-91A4-08002B14A0FA
	UUIDEndpointMapper = [16]byte{
		0xE1, 0xAF, 0x83, 0x08,
		0x5D, 0x1F,
		0x11, 0xC9,
		0x91, 0xA4,
		0x08, 0x00, 0x2B, 0x14, 0xA0, 0xFA,
	}

	// NDR Transfer Syntax UUID: 8A885D04-1CEB-11C9-9FE8-08002B104860
	UUIDNDRTransferSyntax = [16]byte{
		0x8A, 0x88, 0x5D, 0x04,
		0x1C, 0xEB,
		0x11, 0xC9,
		0x9F, 0xE8,
		0x08, 0x00, 0x2B, 0x10, 0x48, 0x60,
	}
)

// RPCHeader is the 80-byte DCE/RPC connectionless header.
type RPCHeader struct {
	RPCVersion       uint8
	PacketType       uint8
	Flags1           uint8
	Flags2           uint8
	DataRep          [3]byte // [0]=byte order (0x10=LE, 0x00=BE), [1]=char, [2]=float
	SerialHigh       uint8
	ObjectUUID       [16]byte
	InterfaceUUID    [16]byte
	ActivityUUID     [16]byte
	ServerBootTime   uint32
	InterfaceVersion uint32
	SequenceNum      uint32
	OpNum            uint16
	InterfaceHint    uint16
	ActivityHint     uint16
	FragmentLen      uint16
	FragmentNum      uint16
	AuthProto        uint8
	SerialLow        uint8
}

// RPCHeaderSize is the fixed size of a DCE/RPC CL header.
const RPCHeaderSize = 80

// UUIDFromWire converts a UUID from wire format (respecting drep byte order) to canonical BE.
func UUIDFromWire(data []byte, le bool) [16]byte {
	var uuid [16]byte
	copy(uuid[:], data[:16])
	if le {
		// Byte-swap first 3 groups from LE to BE (clock_seq and node are always BE)
		uuid[0], uuid[1], uuid[2], uuid[3] = data[3], data[2], data[1], data[0]
		uuid[4], uuid[5] = data[5], data[4]
		uuid[6], uuid[7] = data[7], data[6]
	}
	return uuid
}

// UUIDToWire converts a UUID from canonical BE to wire format (respecting drep byte order).
func UUIDToWire(uuid [16]byte, le bool) []byte {
	buf := make([]byte, 16)
	copy(buf, uuid[:])
	if le {
		buf[0], buf[1], buf[2], buf[3] = uuid[3], uuid[2], uuid[1], uuid[0]
		buf[4], buf[5] = uuid[5], uuid[4]
		buf[6], buf[7] = uuid[7], uuid[6]
	}
	return buf
}

// ParseRPCHeader parses a DCE/RPC connectionless header from raw bytes.
// UUIDs are converted to canonical big-endian format.
func ParseRPCHeader(data []byte) (*RPCHeader, error) {
	if len(data) < RPCHeaderSize {
		return nil, fmt.Errorf("RPC header too short: %d bytes", len(data))
	}

	h := &RPCHeader{
		RPCVersion: data[0],
		PacketType: data[1],
		Flags1:     data[2],
		Flags2:     data[3],
		SerialHigh: data[7],
		AuthProto:  data[78],
		SerialLow:  data[79],
	}
	copy(h.DataRep[:], data[4:7])

	le := h.IsLittleEndian()

	// Parse UUIDs, converting from drep to canonical BE
	h.ObjectUUID = UUIDFromWire(data[8:24], le)
	h.InterfaceUUID = UUIDFromWire(data[24:40], le)
	h.ActivityUUID = UUIDFromWire(data[40:56], le)

	if le {
		h.ServerBootTime = binary.LittleEndian.Uint32(data[56:60])
		h.InterfaceVersion = binary.LittleEndian.Uint32(data[60:64])
		h.SequenceNum = binary.LittleEndian.Uint32(data[64:68])
		h.OpNum = binary.LittleEndian.Uint16(data[68:70])
		h.InterfaceHint = binary.LittleEndian.Uint16(data[70:72])
		h.ActivityHint = binary.LittleEndian.Uint16(data[72:74])
		h.FragmentLen = binary.LittleEndian.Uint16(data[74:76])
		h.FragmentNum = binary.LittleEndian.Uint16(data[76:78])
	} else {
		h.ServerBootTime = binary.BigEndian.Uint32(data[56:60])
		h.InterfaceVersion = binary.BigEndian.Uint32(data[60:64])
		h.SequenceNum = binary.BigEndian.Uint32(data[64:68])
		h.OpNum = binary.BigEndian.Uint16(data[68:70])
		h.InterfaceHint = binary.BigEndian.Uint16(data[70:72])
		h.ActivityHint = binary.BigEndian.Uint16(data[72:74])
		h.FragmentLen = binary.BigEndian.Uint16(data[74:76])
		h.FragmentNum = binary.BigEndian.Uint16(data[76:78])
	}

	return h, nil
}

// Marshal serializes a DCE/RPC CL header using the byte order specified in DataRep.
// UUIDs are converted from canonical BE to wire format.
func (h *RPCHeader) Marshal() []byte {
	buf := make([]byte, RPCHeaderSize)
	buf[0] = h.RPCVersion
	buf[1] = h.PacketType
	buf[2] = h.Flags1
	buf[3] = h.Flags2
	copy(buf[4:7], h.DataRep[:])
	buf[7] = h.SerialHigh

	le := h.IsLittleEndian()
	copy(buf[8:24], UUIDToWire(h.ObjectUUID, le))
	copy(buf[24:40], UUIDToWire(h.InterfaceUUID, le))
	copy(buf[40:56], UUIDToWire(h.ActivityUUID, le))

	if le {
		binary.LittleEndian.PutUint32(buf[56:60], h.ServerBootTime)
		binary.LittleEndian.PutUint32(buf[60:64], h.InterfaceVersion)
		binary.LittleEndian.PutUint32(buf[64:68], h.SequenceNum)
		binary.LittleEndian.PutUint16(buf[68:70], h.OpNum)
		binary.LittleEndian.PutUint16(buf[70:72], h.InterfaceHint)
		binary.LittleEndian.PutUint16(buf[72:74], h.ActivityHint)
		binary.LittleEndian.PutUint16(buf[74:76], h.FragmentLen)
		binary.LittleEndian.PutUint16(buf[76:78], h.FragmentNum)
	} else {
		binary.BigEndian.PutUint32(buf[56:60], h.ServerBootTime)
		binary.BigEndian.PutUint32(buf[60:64], h.InterfaceVersion)
		binary.BigEndian.PutUint32(buf[64:68], h.SequenceNum)
		binary.BigEndian.PutUint16(buf[68:70], h.OpNum)
		binary.BigEndian.PutUint16(buf[70:72], h.InterfaceHint)
		binary.BigEndian.PutUint16(buf[72:74], h.ActivityHint)
		binary.BigEndian.PutUint16(buf[74:76], h.FragmentLen)
		binary.BigEndian.PutUint16(buf[76:78], h.FragmentNum)
	}

	buf[78] = h.AuthProto
	buf[79] = h.SerialLow
	return buf
}

// IsLittleEndian returns true if the header uses LE byte order.
func (h *RPCHeader) IsLittleEndian() bool {
	return h.DataRep[0] == 0x10
}

// ParsePNIORequestBody extracts PNIO blocks from a Connect/Write/Control request body.
// NDR layout: ArgsMaximum(4) + ArgsLength(4) + MaxCount(4) + Offset(4) + ActualCount(4) + blocks
func ParsePNIORequestBody(data []byte, le bool) (argsMax uint32, blocks []PNIOBlock, err error) {
	if len(data) < 20 {
		return 0, nil, fmt.Errorf("PNIO request body too short: %d", len(data))
	}

	var get32 func([]byte) uint32
	if le {
		get32 = func(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
	} else {
		get32 = func(b []byte) uint32 { return binary.BigEndian.Uint32(b) }
	}

	argsMax = get32(data[0:4])
	// argsLen := get32(data[4:8])
	// maxCount := get32(data[8:12])
	// offset := get32(data[12:16])
	actualCount := get32(data[16:20])

	blockData := data[20:]
	if int(actualCount) < len(blockData) {
		blockData = blockData[:actualCount]
	}

	blocks, err = ParsePNIOBlocks(blockData)
	return argsMax, blocks, err
}

// MarshalPNIOResponseBody creates an NDR-encoded PNIO response body.
// Layout (per p-net pf_cmrpc_rm_connect_rsp):
//
//	PNIOStatus(4) + ArgsLength(4) + MaxCount(4) + Offset(4) + ActualCount(4) + blocks
//
// PNIOStatus comes FIRST (it's the [out] status parameter that precedes
// the in/out byte buffer). ArgsLength/ActualCount measure ONLY the blocks
// length, not including the status. MaxCount is the conformant array bound
// preserved from the request's ArgsMaximum (the controller's buffer size).
func MarshalPNIOResponseBody(status PNIOStatus, responseBlocks []byte, argsMax uint32, le bool) []byte {
	argsLen := uint32(len(responseBlocks))
	buf := make([]byte, 4+16+len(responseBlocks))

	var put32 func([]byte, uint32)
	if le {
		put32 = func(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }
	} else {
		put32 = func(b []byte, v uint32) { binary.BigEndian.PutUint32(b, v) }
	}

	copy(buf[0:4], status.Marshal()) // PNIOStatus
	put32(buf[4:8], argsLen)         // ArgsLength
	put32(buf[8:12], argsMax)        // MaxCount (preserved from request)
	put32(buf[12:16], 0)             // Offset
	put32(buf[16:20], argsLen)       // ActualCount
	copy(buf[20:], responseBlocks)

	return buf
}

// UUIDPNIOObjectBase is the PROFINET IO object instance UUID base:: DEA00000-6C97-11D1-8271-00xx-xxxx-xxxx
// The node bytes encode: interface_index(2) + DeviceID(2) + VendorID(2)
var UUIDPNIOObjectBase = [16]byte{
	0xDE, 0xA0, 0x00, 0x00,
	0x6C, 0x97,
	0x11, 0xD1,
	0x82, 0x71,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
}
