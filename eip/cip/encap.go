// Package cip implements EtherNet/IP encapsulation and CIP (Common Industrial
// Protocol) primitives shared by client and server implementations. It is
// intentionally library-style: no I/O, no concurrency — just framing,
// service/class/status codes, and path encoding. Higher layers (server,
// client, sim) own the transport and the message router.
//
// References: ODVA Volume 1 (CIP) and Volume 2 (EtherNet/IP Adaptation).
// libplctag and gologix are used as cross-references; this code is
// independent and not a port.
package cip

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Encapsulation command codes (Vol 2, §2-3.2).
const (
	CmdNOP               uint16 = 0x0000
	CmdListServices      uint16 = 0x0004
	CmdListIdentity      uint16 = 0x0063
	CmdListInterfaces    uint16 = 0x0064
	CmdRegisterSession   uint16 = 0x0065
	CmdUnRegisterSession uint16 = 0x0066
	CmdSendRRData        uint16 = 0x006F // Unconnected explicit messaging
	CmdSendUnitData      uint16 = 0x0070 // Connected explicit messaging
	CmdIndicateStatus    uint16 = 0x0072
	CmdCancel            uint16 = 0x0073
)

// Encapsulation status codes (Vol 2, §2-3.3).
const (
	EncapStatusSuccess          uint32 = 0x0000
	EncapStatusInvalidCommand   uint32 = 0x0001
	EncapStatusInsufficientMem  uint32 = 0x0002
	EncapStatusIncorrectData    uint32 = 0x0003
	EncapStatusInvalidSession   uint32 = 0x0064
	EncapStatusInvalidLength    uint32 = 0x0065
	EncapStatusUnsupportedProto uint32 = 0x0069
)

// EncapHeaderLen is the fixed 24-byte EtherNet/IP encapsulation header.
const EncapHeaderLen = 24

// EncapHeader is the EtherNet/IP encapsulation header that prefixes every
// command. It is identical on request and reply; the receiver echoes
// SenderContext so clients can correlate replies with outstanding requests.
type EncapHeader struct {
	Command       uint16
	Length        uint16 // length of data following the header
	SessionHandle uint32 // assigned by target during RegisterSession
	Status        uint32 // encap-layer status (not CIP status)
	SenderContext [8]byte
	Options       uint32
}

// MarshalBinary serializes the header in little-endian wire order.
func (h *EncapHeader) MarshalBinary() ([]byte, error) {
	b := make([]byte, EncapHeaderLen)
	binary.LittleEndian.PutUint16(b[0:2], h.Command)
	binary.LittleEndian.PutUint16(b[2:4], h.Length)
	binary.LittleEndian.PutUint32(b[4:8], h.SessionHandle)
	binary.LittleEndian.PutUint32(b[8:12], h.Status)
	copy(b[12:20], h.SenderContext[:])
	binary.LittleEndian.PutUint32(b[20:24], h.Options)
	return b, nil
}

// UnmarshalBinary parses a header from the first 24 bytes of b.
func (h *EncapHeader) UnmarshalBinary(b []byte) error {
	if len(b) < EncapHeaderLen {
		return fmt.Errorf("encap header: need %d bytes, got %d", EncapHeaderLen, len(b))
	}
	h.Command = binary.LittleEndian.Uint16(b[0:2])
	h.Length = binary.LittleEndian.Uint16(b[2:4])
	h.SessionHandle = binary.LittleEndian.Uint32(b[4:8])
	h.Status = binary.LittleEndian.Uint32(b[8:12])
	copy(h.SenderContext[:], b[12:20])
	h.Options = binary.LittleEndian.Uint32(b[20:24])
	return nil
}

// ErrShortFrame is returned when an encapsulation frame is truncated.
var ErrShortFrame = errors.New("cip: short encapsulation frame")

// DecodeFrame splits a buffer that starts with an encap header into the
// header and its data payload. It does not consume bytes beyond the framed
// message — the caller is responsible for buffer management.
func DecodeFrame(buf []byte) (EncapHeader, []byte, error) {
	var h EncapHeader
	if err := h.UnmarshalBinary(buf); err != nil {
		return h, nil, err
	}
	end := EncapHeaderLen + int(h.Length)
	if len(buf) < end {
		return h, nil, ErrShortFrame
	}
	return h, buf[EncapHeaderLen:end], nil
}

// EncodeFrame builds a complete encapsulation message from the header and
// its data payload. The header's Length field is set automatically.
func EncodeFrame(h EncapHeader, data []byte) []byte {
	h.Length = uint16(len(data))
	hdr, _ := h.MarshalBinary()
	out := make([]byte, 0, EncapHeaderLen+len(data))
	out = append(out, hdr...)
	out = append(out, data...)
	return out
}
