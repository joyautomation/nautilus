package logixserver

import (
	"encoding/binary"
	"net"

	"github.com/joyautomation/nautilus/eip/cip"
)

// Logix identity constants. These make the server present as an Allen-Bradley
// ControlLogix 5580 (1756-L83E) so Logix clients treat it as a real
// controller. Vendor 0x0001 is Rockwell/Allen-Bradley; device type 0x000E is
// "Programmable Logic Controller".
const (
	identityVendorAB     uint16 = 0x0001 // Rockwell Automation / Allen-Bradley
	identityDeviceType   uint16 = 0x000E // Programmable Logic Controller
	identityProductCode  uint16 = 0x00D6 // representative 1756-L8x (5580) product code
	identityRevisionMaj  uint8  = 32
	identityRevisionMin  uint8  = 11
	identityStatus       uint16 = 0x0000
	identitySerialNumber uint32 = 0x1337C0DE
	identityProductName  string = "1756-L83E/B"

	// DefaultControllerName is the project/controller name returned by the
	// Program Name object (class 0x64) when none is configured. pycomm3 reads
	// this during LogixDriver init via get_plc_name(); init fails if the class
	// is unsupported.
	DefaultControllerName string = "Nautilus_Logix"
)

// ProgramNameObject implements CIP class 0x64. pycomm3's get_plc_name() issues
// GetAttributesAll on instance 1 and decodes the reply as a CIP STRING
// (2-byte length + 1-byte-per-char), so we reply with exactly that.
type ProgramNameObject struct {
	name string
}

// NewProgramNameObject returns the Program Name handler reporting name. An empty
// name falls back to DefaultControllerName.
func NewProgramNameObject(name string) *ProgramNameObject {
	if name == "" {
		name = DefaultControllerName
	}
	return &ProgramNameObject{name: name}
}

// HandleService dispatches CIP services on the Program Name instance.
func (o *ProgramNameObject) HandleService(service uint8, instance uint32, _ uint32, _ []byte) cip.MessageRouterResponse {
	if instance != 1 {
		return cip.MRError(service, cip.StatusObjDoesNotExist)
	}
	switch service {
	case cip.ServiceGetAttributeSingle, cip.ServiceGetAttributesAll:
		return cip.MROK(service, cipString(o.name))
	default:
		return cip.MRError(service, cip.StatusServiceNotSupported)
	}
}

// cipString encodes a CIP STRING (2-byte little-endian length prefix, 1 byte
// per char).
func cipString(s string) []byte {
	out := make([]byte, 0, 2+len(s))
	out = append(out, u16(uint16(len(s)))...)
	out = append(out, []byte(s)...)
	return out
}

// IdentityObject implements CIP class 0x01 for the Logix server.
type IdentityObject struct{}

// NewIdentityObject returns the Identity handler.
func NewIdentityObject() *IdentityObject { return &IdentityObject{} }

// HandleService dispatches CIP services on the Identity instance.
func (o *IdentityObject) HandleService(service uint8, instance uint32, attribute uint32, _ []byte) cip.MessageRouterResponse {
	if instance != 1 {
		return cip.MRError(service, cip.StatusObjDoesNotExist)
	}
	switch service {
	case cip.ServiceGetAttributeSingle:
		return o.getAttr(service, uint16(attribute))
	case cip.ServiceGetAttributesAll:
		return cip.MROK(service, identityAttributesAll())
	default:
		return cip.MRError(service, cip.StatusServiceNotSupported)
	}
}

func (o *IdentityObject) getAttr(service uint8, attr uint16) cip.MessageRouterResponse {
	switch attr {
	case 1:
		return cip.MROK(service, u16(identityVendorAB))
	case 2:
		return cip.MROK(service, u16(identityDeviceType))
	case 3:
		return cip.MROK(service, u16(identityProductCode))
	case 4:
		return cip.MROK(service, []byte{identityRevisionMaj, identityRevisionMin})
	case 5:
		return cip.MROK(service, u16(identityStatus))
	case 6:
		return cip.MROK(service, u32(identitySerialNumber))
	case 7:
		return cip.MROK(service, shortString(identityProductName))
	default:
		return cip.MRError(service, cip.StatusAttrNotSupported)
	}
}

// identityAttributesAll concatenates the standard Identity attribute set
// (Vendor, DeviceType, ProductCode, Revision, Status, Serial, ProductName) —
// the layout pycomm3 parses from a GetAttributesAll reply.
func identityAttributesAll() []byte {
	out := make([]byte, 0, 32)
	out = append(out, u16(identityVendorAB)...)
	out = append(out, u16(identityDeviceType)...)
	out = append(out, u16(identityProductCode)...)
	out = append(out, identityRevisionMaj, identityRevisionMin)
	out = append(out, u16(identityStatus)...)
	out = append(out, u32(identitySerialNumber)...)
	out = append(out, shortString(identityProductName)...)
	return out
}

// ListIdentityPayload builds the data portion of a ListIdentity reply, wrapped
// in CPF item 0x000C by the encapsulation layer. Layout per Vol 2 §2-4.4.2.
func ListIdentityPayload(addr net.IP, port uint16) []byte {
	sockaddr := make([]byte, 16)
	binary.BigEndian.PutUint16(sockaddr[0:2], 2) // AF_INET (network byte order)
	binary.BigEndian.PutUint16(sockaddr[2:4], port)
	ip4 := addr.To4()
	if ip4 == nil {
		ip4 = []byte{0, 0, 0, 0}
	}
	copy(sockaddr[4:8], ip4)

	out := make([]byte, 0, 64)
	out = append(out, u16(1)...) // encapsulation protocol version
	out = append(out, sockaddr...)
	out = append(out, u16(identityVendorAB)...)
	out = append(out, u16(identityDeviceType)...)
	out = append(out, u16(identityProductCode)...)
	out = append(out, identityRevisionMaj, identityRevisionMin)
	out = append(out, u16(identityStatus)...)
	out = append(out, u32(identitySerialNumber)...)
	out = append(out, byte(len(identityProductName)))
	out = append(out, []byte(identityProductName)...)
	out = append(out, 0x03) // device state = Operational
	return out
}

// shortString encodes a CIP SHORT_STRING (1-byte length prefix).
func shortString(s string) []byte {
	out := make([]byte, 1+len(s))
	out[0] = byte(len(s))
	copy(out[1:], s)
	return out
}
