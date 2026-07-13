package cip

import (
	"encoding/binary"
	"fmt"
)

// MessageRouterRequest is the unconnected CIP message body that lives inside
// an UnconnectedData CPF item. Layout (Vol 1 §2-4.1):
//
//	[u8 service]
//	[u8 pathSize]        // size of path in 16-bit words
//	[pathSize*2 bytes]   // EPATH
//	[remaining bytes]    // service-specific data
type MessageRouterRequest struct {
	Service uint8
	Path    []byte // raw EPATH bytes
	Data    []byte // service-specific request data
}

// DecodeMRRequest parses a Message Router request from raw bytes.
func DecodeMRRequest(b []byte) (MessageRouterRequest, error) {
	var r MessageRouterRequest
	if len(b) < 2 {
		return r, fmt.Errorf("mr request: too short")
	}
	r.Service = b[0]
	pathWords := int(b[1])
	pathEnd := 2 + pathWords*2
	if pathEnd > len(b) {
		return r, fmt.Errorf("mr request: path length %d exceeds buffer", pathWords*2)
	}
	r.Path = b[2:pathEnd]
	r.Data = b[pathEnd:]
	return r, nil
}

// MessageRouterResponse is the reply form (Vol 1 §2-4.2):
//
//	[u8 service|0x80]
//	[u8 reserved=0]
//	[u8 generalStatus]
//	[u8 extStatusSize]      // size of ext status in 16-bit words
//	[extStatusSize*2 bytes]
//	[remaining bytes]       // service-specific reply data
type MessageRouterResponse struct {
	Service       uint8 // original request service (reply bit applied on encode)
	GeneralStatus uint8
	ExtStatus     []uint16 // optional extended status words
	Data          []byte
}

// Encode serializes a Message Router response into its wire form.
func (r MessageRouterResponse) Encode() []byte {
	size := 4 + 2*len(r.ExtStatus) + len(r.Data)
	out := make([]byte, size)
	out[0] = r.Service | ReplyBit
	out[1] = 0x00
	out[2] = r.GeneralStatus
	out[3] = uint8(len(r.ExtStatus))
	off := 4
	for _, w := range r.ExtStatus {
		binary.LittleEndian.PutUint16(out[off:off+2], w)
		off += 2
	}
	copy(out[off:], r.Data)
	return out
}

// MRError is a convenience for building error responses.
func MRError(service, status uint8) MessageRouterResponse {
	return MessageRouterResponse{Service: service, GeneralStatus: status}
}

// MROK is a convenience for a success reply with no data.
func MROK(service uint8, data []byte) MessageRouterResponse {
	return MessageRouterResponse{Service: service, GeneralStatus: StatusSuccess, Data: data}
}

// CIPObject is the per-class handler interface. Server-side objects implement
// it to participate in dispatch. Receivers handle a request scoped to a
// particular instance (instance 0 means the class itself).
type CIPObject interface {
	// HandleService dispatches a service to this object. The path has already
	// been parsed and the instance located. Implementations should never panic
	// — return an MRError response for unsupported services/attributes.
	HandleService(service uint8, instance uint32, attribute uint32, data []byte) MessageRouterResponse
}

// Dispatcher routes Message Router requests to registered CIPObjects keyed by
// class ID. It is concurrency-safe to register classes during startup; once
// serving begins, registration must be quiescent (objects own their own
// internal locking).
type Dispatcher struct {
	classes map[uint16]CIPObject
}

// NewDispatcher returns an empty dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{classes: make(map[uint16]CIPObject)}
}

// Register attaches an object to a class ID. Re-registering replaces the
// existing entry (useful in tests).
func (d *Dispatcher) Register(class uint16, obj CIPObject) {
	d.classes[class] = obj
}

// Dispatch parses the request's path, looks up the class, and invokes the
// object's handler. Returns the response (always a valid MR response, even
// on error — the caller serializes and ships it back).
func (d *Dispatcher) Dispatch(req MessageRouterRequest) MessageRouterResponse {
	path, _, err := ParsePath(req.Path)
	if err != nil {
		return MRError(req.Service, StatusPathSegmentError)
	}
	if !path.HasClass {
		return MRError(req.Service, StatusPathDestUnknown)
	}
	obj, ok := d.classes[uint16(path.Class)]
	if !ok {
		return MRError(req.Service, StatusPathDestUnknown)
	}
	return obj.HandleService(req.Service, path.Instance, path.Attribute, req.Data)
}
