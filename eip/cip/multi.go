package cip

import (
	"encoding/binary"
	"fmt"
)

// DecodeMultipleServiceRequest splits a Multiple Service Packet (0x0A) request
// body into its embedded Message Router sub-requests. Layout (Vol 1 §2-4.10):
//
//	[u16 count][count×u16 offset][sub-request bytes...]
//
// Each offset is relative to the start of the body (the count word). The last
// sub-request runs to the end of the buffer.
func DecodeMultipleServiceRequest(body []byte) ([]MessageRouterRequest, error) {
	if len(body) < 2 {
		return nil, fmt.Errorf("multi-service: body too short")
	}
	count := int(binary.LittleEndian.Uint16(body[:2]))
	if count == 0 {
		return nil, nil
	}
	if len(body) < 2+count*2 {
		return nil, fmt.Errorf("multi-service: offset table truncated")
	}
	offsets := make([]int, count)
	for i := 0; i < count; i++ {
		offsets[i] = int(binary.LittleEndian.Uint16(body[2+i*2:]))
	}
	reqs := make([]MessageRouterRequest, count)
	for i := 0; i < count; i++ {
		start := offsets[i]
		end := len(body)
		if i+1 < count {
			end = offsets[i+1]
		}
		if start < 0 || end > len(body) || start > end {
			return nil, fmt.Errorf("multi-service: sub-request %d out of bounds", i)
		}
		r, err := DecodeMRRequest(body[start:end])
		if err != nil {
			return nil, fmt.Errorf("multi-service: sub-request %d: %w", i, err)
		}
		reqs[i] = r
	}
	return reqs, nil
}

// EncodeMultipleServiceResponse assembles a Multiple Service Packet reply body
// from per-sub-request responses, in order. Layout mirrors the request:
//
//	[u16 count][count×u16 offset][response bytes...]
//
// Each offset is relative to the start of the body (the count word).
func EncodeMultipleServiceResponse(resps []MessageRouterResponse) []byte {
	count := len(resps)
	headerLen := 2 + count*2
	encoded := make([][]byte, count)
	bodyLen := 0
	for i, r := range resps {
		encoded[i] = r.Encode()
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
