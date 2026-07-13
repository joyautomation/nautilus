package cip

import (
	"encoding/binary"
	"fmt"
)

// ForwardOpenRequest is the parsed Connection Manager Forward_Open service
// payload. It is the negotiation that turns an UDP/TCP pair into a CIP Class
// 1 (cyclic I/O) or Class 3 (connected explicit) connection.
//
// Layout (Vol 1 §3-5.5.2, abridged):
//
//	[u8 priorityTimeTick]
//	[u8 timeoutTicks]
//	[u32 oToTNetworkConnID]
//	[u32 tToONetworkConnID]
//	[u16 connectionSerial]
//	[u16 originatorVendorID]
//	[u32 originatorSerialNum]
//	[u8 connectionTimeoutMultiplier]
//	[3 bytes reserved]
//	[u32 oToTRPI]            // µs
//	[u16 oToTNetworkParams]  // size, type, priority, redundant owner
//	[u32 tToORPI]            // µs
//	[u16 tToONetworkParams]
//	[u8 transportTrigger]    // class/trigger/dir
//	[u8 connectionPathSize]  // in 16-bit words
//	[connectionPathSize*2 bytes connectionPath]
//
// Large Forward_Open (service 0x5B) widens network params to 32-bit; we
// transparently decode both.
type ForwardOpenRequest struct {
	PriorityTimeTick      uint8
	TimeoutTicks          uint8
	OToTNetworkConnID     uint32
	TToONetworkConnID     uint32
	ConnectionSerial      uint16
	OriginatorVendorID    uint16
	OriginatorSerialNum   uint32
	ConnectionTimeoutMult uint8
	OToTRPIMicros         uint32
	OToTNetworkParams     uint32
	TToORPIMicros         uint32
	TToONetworkParams     uint32
	TransportClassTrigger uint8
	ConnectionPath        []byte
	IsLargeOpen           bool
}

// ForwardOpenResponse is the success reply payload (Vol 1 §3-5.5.3):
//
//	[u32 oToTNetworkConnID]  // server-assigned (or echoed)
//	[u32 tToONetworkConnID]  // server-assigned
//	[u16 connectionSerial]   // echo
//	[u16 originatorVendorID] // echo
//	[u32 originatorSerialNum]// echo
//	[u32 oToTAPI]            // µs, actual API the server will run
//	[u32 tToOAPI]
//	[u8 applicationReplySize]
//	[u8 reserved=0]
//	[appReplySize*2 bytes appReply]
type ForwardOpenResponse struct {
	OToTNetworkConnID   uint32
	TToONetworkConnID   uint32
	ConnectionSerial    uint16
	OriginatorVendorID  uint16
	OriginatorSerialNum uint32
	OToTAPIMicros       uint32
	TToOAPIMicros       uint32
	AppReply            []byte
}

// DecodeForwardOpen parses a Forward_Open or Large Forward_Open service body.
// Caller already verified the service code; pass the raw body bytes.
func DecodeForwardOpen(b []byte, large bool) (ForwardOpenRequest, error) {
	var r ForwardOpenRequest
	r.IsLargeOpen = large
	minLen := 36
	if large {
		minLen = 40 // 32-bit network params instead of 16-bit
	}
	if len(b) < minLen {
		return r, fmt.Errorf("forward_open: short request (have %d, need %d)", len(b), minLen)
	}
	r.PriorityTimeTick = b[0]
	r.TimeoutTicks = b[1]
	r.OToTNetworkConnID = binary.LittleEndian.Uint32(b[2:6])
	r.TToONetworkConnID = binary.LittleEndian.Uint32(b[6:10])
	r.ConnectionSerial = binary.LittleEndian.Uint16(b[10:12])
	r.OriginatorVendorID = binary.LittleEndian.Uint16(b[12:14])
	r.OriginatorSerialNum = binary.LittleEndian.Uint32(b[14:18])
	r.ConnectionTimeoutMult = b[18]
	// 3 reserved bytes at 19..22
	r.OToTRPIMicros = binary.LittleEndian.Uint32(b[22:26])
	off := 26
	if large {
		r.OToTNetworkParams = binary.LittleEndian.Uint32(b[off : off+4])
		off += 4
	} else {
		r.OToTNetworkParams = uint32(binary.LittleEndian.Uint16(b[off : off+2]))
		off += 2
	}
	r.TToORPIMicros = binary.LittleEndian.Uint32(b[off : off+4])
	off += 4
	if large {
		r.TToONetworkParams = binary.LittleEndian.Uint32(b[off : off+4])
		off += 4
	} else {
		r.TToONetworkParams = uint32(binary.LittleEndian.Uint16(b[off : off+2]))
		off += 2
	}
	r.TransportClassTrigger = b[off]
	off++
	pathWords := int(b[off])
	off++
	if off+pathWords*2 > len(b) {
		return r, fmt.Errorf("forward_open: truncated path")
	}
	r.ConnectionPath = make([]byte, pathWords*2)
	copy(r.ConnectionPath, b[off:off+pathWords*2])
	return r, nil
}

// Encode builds the success-reply body.
func (r ForwardOpenResponse) Encode() []byte {
	out := make([]byte, 26+len(r.AppReply))
	binary.LittleEndian.PutUint32(out[0:4], r.OToTNetworkConnID)
	binary.LittleEndian.PutUint32(out[4:8], r.TToONetworkConnID)
	binary.LittleEndian.PutUint16(out[8:10], r.ConnectionSerial)
	binary.LittleEndian.PutUint16(out[10:12], r.OriginatorVendorID)
	binary.LittleEndian.PutUint32(out[12:16], r.OriginatorSerialNum)
	binary.LittleEndian.PutUint32(out[16:20], r.OToTAPIMicros)
	binary.LittleEndian.PutUint32(out[20:24], r.TToOAPIMicros)
	out[24] = uint8(len(r.AppReply) / 2)
	out[25] = 0
	copy(out[26:], r.AppReply)
	return out
}

// ForwardOpenSize extracts the connection-data size in bytes from the
// network params word. Bits 0-8 hold the size (Vol 1 §3-5.5.2 table 3-5.31).
// For Large Forward_Open, size is bits 0-15.
func ForwardOpenSize(networkParams uint32, large bool) uint16 {
	if large {
		return uint16(networkParams & 0xFFFF)
	}
	return uint16(networkParams & 0x01FF)
}

// EncodeForwardOpenRequest serializes a ForwardOpenRequest for transmission
// by the originator (scanner). Inverse of DecodeForwardOpen.
func EncodeForwardOpenRequest(r ForwardOpenRequest) []byte {
	pathWords := len(r.ConnectionPath) / 2
	size := 36 + len(r.ConnectionPath)
	if r.IsLargeOpen {
		size = 40 + len(r.ConnectionPath)
	}
	out := make([]byte, size)
	out[0] = r.PriorityTimeTick
	out[1] = r.TimeoutTicks
	binary.LittleEndian.PutUint32(out[2:6], r.OToTNetworkConnID)
	binary.LittleEndian.PutUint32(out[6:10], r.TToONetworkConnID)
	binary.LittleEndian.PutUint16(out[10:12], r.ConnectionSerial)
	binary.LittleEndian.PutUint16(out[12:14], r.OriginatorVendorID)
	binary.LittleEndian.PutUint32(out[14:18], r.OriginatorSerialNum)
	out[18] = r.ConnectionTimeoutMult
	// 3 reserved bytes at 19..21
	binary.LittleEndian.PutUint32(out[22:26], r.OToTRPIMicros)
	off := 26
	if r.IsLargeOpen {
		binary.LittleEndian.PutUint32(out[off:off+4], r.OToTNetworkParams)
		off += 4
	} else {
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(r.OToTNetworkParams))
		off += 2
	}
	binary.LittleEndian.PutUint32(out[off:off+4], r.TToORPIMicros)
	off += 4
	if r.IsLargeOpen {
		binary.LittleEndian.PutUint32(out[off:off+4], r.TToONetworkParams)
		off += 4
	} else {
		binary.LittleEndian.PutUint16(out[off:off+2], uint16(r.TToONetworkParams))
		off += 2
	}
	out[off] = r.TransportClassTrigger
	off++
	out[off] = uint8(pathWords)
	off++
	copy(out[off:], r.ConnectionPath)
	return out
}

// DecodeForwardOpenResponse parses the success-reply body returned by the
// target. Inverse of ForwardOpenResponse.Encode.
func DecodeForwardOpenResponse(b []byte) (ForwardOpenResponse, error) {
	var r ForwardOpenResponse
	if len(b) < 26 {
		return r, fmt.Errorf("forward_open response: short (%d < 26)", len(b))
	}
	r.OToTNetworkConnID = binary.LittleEndian.Uint32(b[0:4])
	r.TToONetworkConnID = binary.LittleEndian.Uint32(b[4:8])
	r.ConnectionSerial = binary.LittleEndian.Uint16(b[8:10])
	r.OriginatorVendorID = binary.LittleEndian.Uint16(b[10:12])
	r.OriginatorSerialNum = binary.LittleEndian.Uint32(b[12:16])
	r.OToTAPIMicros = binary.LittleEndian.Uint32(b[16:20])
	r.TToOAPIMicros = binary.LittleEndian.Uint32(b[20:24])
	appReplyWords := int(b[24])
	off := 26
	if off+appReplyWords*2 > len(b) {
		return r, fmt.Errorf("forward_open response: truncated app reply")
	}
	r.AppReply = make([]byte, appReplyWords*2)
	copy(r.AppReply, b[off:off+appReplyWords*2])
	return r, nil
}

// ForwardOpenNetworkParams packs the network-parameters word for a connection
// direction. fixedSize=true for fixed-length data (typical for assemblies);
// type 2 = point-to-point (the only kind for Ethernet/IP); priority 0 = Low.
//
// For non-Large Forward_Open the word is 16 bits:
//
//	bit  15:    redundant owner (0 = exclusive)
//	bits 14-13: connection type (2 = point-to-point)
//	bits 12-11: priority (0 = low)
//	bit  10:    variable/fixed (0 = fixed, 1 = variable)
//	bits 9-0:   connection size in bytes
//
// For Large Forward_Open the word widens to 32 bits and size occupies bits 15-0.
func ForwardOpenNetworkParams(sizeBytes uint16, fixedSize bool, large bool) uint32 {
	var word uint32
	if large {
		word |= uint32(sizeBytes) & 0xFFFF
		word |= 2 << 29 // connection type = point-to-point (bits 30-29)
	} else {
		word |= uint32(sizeBytes) & 0x01FF
		word |= 2 << 13 // connection type (bits 14-13)
		if !fixedSize {
			word |= 1 << 10
		}
	}
	return word
}

// handleForwardOpen runs the Forward_Open service through the server's
// callback. The server's HandleForwardOpen owns connection ID allocation,
// RPI selection, and produces the AppReply (typically empty for assembly-
// based connections).
func (s *Server) handleForwardOpen(req MessageRouterRequest) MessageRouterResponse {
	large := req.Service == ServiceLargeForwardOpen
	parsed, err := DecodeForwardOpen(req.Data, large)
	if err != nil {
		return MRError(req.Service, StatusInvalidParameter)
	}
	if s.HandleForwardOpen == nil {
		// Refuse with an extended status indicating "connection refused —
		// resource unavailable". A real refuse would also include the
		// originator's serial/vendor/connSerial; libplctag tolerates the
		// short form.
		return MRError(req.Service, StatusConnectionFailure)
	}
	resp, ok := s.HandleForwardOpen(parsed)
	if !ok {
		return MRError(req.Service, StatusConnectionFailure)
	}
	return MROK(req.Service, resp.Encode())
}

// handleForwardClose runs the Forward_Close service. The body shape mirrors
// the relevant fields of Forward_Open:
//
//	[u8 priorityTimeTick]
//	[u8 timeoutTicks]
//	[u16 connectionSerial]
//	[u16 originatorVendorID]
//	[u32 originatorSerialNum]
//	[u8 pathSize]
//	[u8 reserved=0]
//	[pathSize*2 bytes path]
func (s *Server) handleForwardClose(req MessageRouterRequest) MessageRouterResponse {
	if len(req.Data) < 10 {
		return MRError(req.Service, StatusInvalidParameter)
	}
	connSerial := binary.LittleEndian.Uint16(req.Data[2:4])
	vendor := binary.LittleEndian.Uint16(req.Data[4:6])
	origSerial := binary.LittleEndian.Uint32(req.Data[6:10])

	if s.HandleForwardClose != nil {
		s.HandleForwardClose(connSerial, vendor, origSerial)
	}

	// Reply shape: connSerial, vendor, origSerial, appReplySize, reserved.
	resp := make([]byte, 10)
	binary.LittleEndian.PutUint16(resp[0:2], connSerial)
	binary.LittleEndian.PutUint16(resp[2:4], vendor)
	binary.LittleEndian.PutUint32(resp[4:8], origSerial)
	// resp[8] = 0 (app reply size); resp[9] = reserved.
	return MROK(req.Service, resp)
}

// handleUnconnectedSend unwraps a nested CIP message routed through Connection
// Manager (service 0x52). The outer payload carries the inner request plus
// a routing path used to forward through gateways. We ignore routing — the
// sim is the endpoint — and dispatch the inner request normally.
//
// Layout:
//
//	[u8 priorityTimeTick]
//	[u8 timeoutTicks]
//	[u16 messageRequestSize]
//	[messageRequestSize bytes  inner MR request]
//	[pad byte if messageRequestSize odd]
//	[u8 routePathSize] [u8 reserved] [routePathSize*2 bytes routePath]
func (s *Server) handleUnconnectedSend(req MessageRouterRequest) MessageRouterResponse {
	if len(req.Data) < 4 {
		return MRError(req.Service, StatusInvalidParameter)
	}
	innerSize := int(binary.LittleEndian.Uint16(req.Data[2:4]))
	if 4+innerSize > len(req.Data) {
		return MRError(req.Service, StatusInvalidParameter)
	}
	inner, err := DecodeMRRequest(req.Data[4 : 4+innerSize])
	if err != nil {
		return MRError(req.Service, StatusInvalidParameter)
	}
	// Dispatch via the normal path; this gives nested service requests the
	// same access to the object dispatcher that direct requests get.
	return s.Dispatcher.Dispatch(inner)
}
