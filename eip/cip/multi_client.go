package cip

import (
	"encoding/binary"
	"fmt"
)

// EncodedMRRequest is a pre-encoded Message Router request for embedding in a
// Multiple Service Packet: service + path + data, without transport framing.
type EncodedMRRequest struct {
	Service uint8
	Path    []byte
	Data    []byte
}

// EncodeMultipleServiceRequest assembles a Multiple Service Packet (0x0A)
// request body from sub-requests. Inverse of DecodeMultipleServiceRequest:
//
//	[u16 count][count×u16 offset][sub-request bytes...]
//
// Offsets are relative to the start of the body (the count word).
func EncodeMultipleServiceRequest(reqs []EncodedMRRequest) []byte {
	count := len(reqs)
	headerLen := 2 + count*2
	encoded := make([][]byte, count)
	bodyLen := 0
	for i, r := range reqs {
		encoded[i] = encodeMRRequest(r.Service, r.Path, r.Data)
		bodyLen += len(encoded[i])
	}
	out := make([]byte, headerLen+bodyLen)
	binary.LittleEndian.PutUint16(out[:2], uint16(count))
	off := headerLen
	for i := range encoded {
		binary.LittleEndian.PutUint16(out[2+i*2:], uint16(off))
		copy(out[off:], encoded[i])
		off += len(encoded[i])
	}
	return out
}

// DecodeMultipleServiceResponse splits a Multiple Service Packet reply body
// into the per-sub-request responses, in order. Inverse of
// EncodeMultipleServiceResponse.
func DecodeMultipleServiceResponse(body []byte) ([]MessageRouterResponse, error) {
	if len(body) < 2 {
		return nil, fmt.Errorf("multi-service response: body too short")
	}
	count := int(binary.LittleEndian.Uint16(body[:2]))
	if count == 0 {
		return nil, nil
	}
	if len(body) < 2+count*2 {
		return nil, fmt.Errorf("multi-service response: offset table truncated")
	}
	offsets := make([]int, count)
	for i := 0; i < count; i++ {
		offsets[i] = int(binary.LittleEndian.Uint16(body[2+i*2:]))
	}
	resps := make([]MessageRouterResponse, count)
	for i := 0; i < count; i++ {
		start := offsets[i]
		end := len(body)
		if i+1 < count {
			end = offsets[i+1]
		}
		if start < 0 || end > len(body) || start > end {
			return nil, fmt.Errorf("multi-service response: sub-response %d out of bounds", i)
		}
		r, err := decodeMRResponse(body[start:end])
		if err != nil {
			return nil, fmt.Errorf("multi-service response: sub-response %d: %w", i, err)
		}
		resps[i] = r
	}
	return resps, nil
}
