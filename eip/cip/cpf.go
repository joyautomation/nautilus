package cip

import (
	"encoding/binary"
	"fmt"
)

// Common Packet Format (CPF) item type IDs (Vol 2, §2-6.3).
// CPF wraps the data payload inside SendRRData / SendUnitData. It is a list
// of typed items: an address item that says "who/what is this for" followed
// by a data item carrying the actual CIP message router request/response.
const (
	ItemNullAddress      uint16 = 0x0000 // Unconnected message (UCMM)
	ItemListIdentity     uint16 = 0x000C // ListIdentity reply payload
	ItemConnectedAddress uint16 = 0x00A1 // Connected — contains connection ID
	ItemConnectedData    uint16 = 0x00B1 // Connected data
	ItemUnconnectedData  uint16 = 0x00B2 // Unconnected message data
	ItemListServices     uint16 = 0x0100
	ItemSockAddrO2T      uint16 = 0x8000 // Originator→Target sock info
	ItemSockAddrT2O      uint16 = 0x8001 // Target→Originator sock info
	ItemSequencedAddress uint16 = 0x8002 // Connected + 32-bit sequence count
)

// CPFItem is a single typed item in a CPF list.
type CPFItem struct {
	TypeID uint16
	Data   []byte
}

// EncodeCPF builds the CPF wrapper that goes inside SendRRData's data field.
// SendRRData's data field has 6 leading bytes (Interface Handle + Timeout)
// followed by the CPF list. This function emits only the CPF list portion;
// the caller prepends the 6-byte SendRRData header.
//
// Layout:
//
//	[u16 itemCount]
//	for each item:
//	  [u16 typeID] [u16 length] [length bytes of data]
func EncodeCPF(items []CPFItem) []byte {
	size := 2
	for _, it := range items {
		size += 4 + len(it.Data)
	}
	out := make([]byte, size)
	binary.LittleEndian.PutUint16(out[0:2], uint16(len(items)))
	off := 2
	for _, it := range items {
		binary.LittleEndian.PutUint16(out[off:off+2], it.TypeID)
		binary.LittleEndian.PutUint16(out[off+2:off+4], uint16(len(it.Data)))
		off += 4
		copy(out[off:], it.Data)
		off += len(it.Data)
	}
	return out
}

// DecodeCPF parses a CPF item list. Returns the items in order. The caller
// supplies the buffer pointing at the item count.
func DecodeCPF(b []byte) ([]CPFItem, error) {
	if len(b) < 2 {
		return nil, fmt.Errorf("cpf: short item count")
	}
	count := int(binary.LittleEndian.Uint16(b[0:2]))
	off := 2
	items := make([]CPFItem, 0, count)
	for i := 0; i < count; i++ {
		if off+4 > len(b) {
			return nil, fmt.Errorf("cpf: truncated item header %d/%d", i, count)
		}
		typeID := binary.LittleEndian.Uint16(b[off : off+2])
		length := int(binary.LittleEndian.Uint16(b[off+2 : off+4]))
		off += 4
		if off+length > len(b) {
			return nil, fmt.Errorf("cpf: item %d data truncated (need %d, have %d)", i, length, len(b)-off)
		}
		data := make([]byte, length)
		copy(data, b[off:off+length])
		off += length
		items = append(items, CPFItem{TypeID: typeID, Data: data})
	}
	return items, nil
}

// SendRRDataHeader is the 6-byte prefix that precedes the CPF item list in
// a SendRRData / SendUnitData command. Interface handle is always 0 for CIP.
type SendRRDataHeader struct {
	InterfaceHandle uint32 // 0 for CIP-over-EtherNet/IP
	Timeout         uint16 // in seconds; 0 = encap-layer timeout (Vol 2 §2-4.7)
}

// EncodeSendRRData composes the full SendRRData data payload: 6-byte header
// followed by the CPF item list.
func EncodeSendRRData(h SendRRDataHeader, items []CPFItem) []byte {
	cpf := EncodeCPF(items)
	out := make([]byte, 6+len(cpf))
	binary.LittleEndian.PutUint32(out[0:4], h.InterfaceHandle)
	binary.LittleEndian.PutUint16(out[4:6], h.Timeout)
	copy(out[6:], cpf)
	return out
}

// DecodeSendRRData splits a SendRRData data payload into its header and items.
func DecodeSendRRData(b []byte) (SendRRDataHeader, []CPFItem, error) {
	var h SendRRDataHeader
	if len(b) < 6 {
		return h, nil, fmt.Errorf("sendrrdata: short header")
	}
	h.InterfaceHandle = binary.LittleEndian.Uint32(b[0:4])
	h.Timeout = binary.LittleEndian.Uint16(b[4:6])
	items, err := DecodeCPF(b[6:])
	return h, items, err
}
