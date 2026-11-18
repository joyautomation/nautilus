package wire

import (
	"encoding/binary"
	"fmt"
	"net"
)

// PROFINET DCP (Discovery and Configuration Protocol) implementation.
// DCP runs on raw Ethernet (EtherType 0x8892) and handles device discovery,
// IP assignment, and name assignment.

// PROFINET EtherTypes and multicast addresses.
var (
	EtherTypePNIO    uint16           = 0x8892
	DCPMulticastAddr net.HardwareAddr = net.HardwareAddr{0x01, 0x0E, 0xCF, 0x00, 0x00, 0x00}
)

// DCP FrameIDs.
const (
	FrameIDDCPHello     uint16 = 0xFEFC
	FrameIDDCPGetSet    uint16 = 0xFEFD
	FrameIDDCPIdentReq  uint16 = 0xFEFE
	FrameIDDCPIdentResp uint16 = 0xFEFF
)

// DCP ServiceIDs.
const (
	DCPServiceGet      uint8 = 0x03
	DCPServiceSet      uint8 = 0x04
	DCPServiceIdentify uint8 = 0x05
	DCPServiceHello    uint8 = 0x06
)

// DCP ServiceTypes.
const (
	DCPServiceTypeRequest  uint8 = 0x00
	DCPServiceTypeResponse uint8 = 0x01
)

// DCP Block Options.
const (
	DCPOptionIP               uint8 = 0x01
	DCPOptionDeviceProperties uint8 = 0x02
	DCPOptionDHCP             uint8 = 0x03
	DCPOptionControl          uint8 = 0x05
	DCPOptionAllSelector      uint8 = 0xFF
)

// DCP IP SubOptions.
const (
	DCPSubOptionIPMAC   uint8 = 0x01
	DCPSubOptionIPSuite uint8 = 0x02
	DCPSubOptionIPFull  uint8 = 0x03
)

// DCP Device Properties SubOptions.
const (
	DCPSubOptionDevVendor        uint8 = 0x01 // Vendor name (type of station)
	DCPSubOptionDevNameOfStation uint8 = 0x02 // Station name
	DCPSubOptionDevID            uint8 = 0x03 // VendorID + DeviceID
	DCPSubOptionDevRole          uint8 = 0x04 // Device role
	DCPSubOptionDevOptions       uint8 = 0x05 // Supported options
	DCPSubOptionDevAlias         uint8 = 0x06 // Alias name
	DCPSubOptionDevInstance      uint8 = 0x07 // Device instance
	DCPSubOptionDevOEMID         uint8 = 0x08 // OEM device ID
)

// DCP Control SubOptions.
const (
	DCPSubOptionControlStart    uint8 = 0x01
	DCPSubOptionControlStop     uint8 = 0x02
	DCPSubOptionControlSignal   uint8 = 0x03
	DCPSubOptionControlResponse uint8 = 0x04
	DCPSubOptionControlFReset   uint8 = 0x05
	DCPSubOptionControlReset    uint8 = 0x06
)

// DCP BlockInfo values for IP suite.
const (
	DCPBlockInfoIPNotSet   uint16 = 0x0000
	DCPBlockInfoIPSet      uint16 = 0x0001
	DCPBlockInfoIPSetDHCP  uint16 = 0x0002
	DCPBlockInfoIPConflict uint16 = 0x0080
)

// Device roles.
const (
	DeviceRoleIODevice     uint8 = 0x01
	DeviceRoleIOController uint8 = 0x02
	DeviceRoleIOMulti      uint8 = 0x03
	DeviceRoleIOSupervisor uint8 = 0x04
)

// DCPFrame represents a parsed DCP frame (after the Ethernet header and FrameID).
type DCPFrame struct {
	FrameID       uint16
	ServiceID     uint8
	ServiceType   uint8
	Xid           uint32
	ResponseDelay uint16
	Blocks        []DCPBlock
}

// DCPBlock represents a single DCP data block.
type DCPBlock struct {
	Option    uint8
	SubOption uint8
	Data      []byte // raw block data (includes BlockInfo for responses)
}

// ParseDCPFrame parses a DCP frame from raw bytes after the Ethernet header.
// The input should start at the FrameID field.
func ParseDCPFrame(data []byte) (*DCPFrame, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("DCP frame too short: %d bytes", len(data))
	}

	f := &DCPFrame{
		FrameID:       binary.BigEndian.Uint16(data[0:2]),
		ServiceID:     data[2],
		ServiceType:   data[3],
		Xid:           binary.BigEndian.Uint32(data[4:8]),
		ResponseDelay: binary.BigEndian.Uint16(data[8:10]),
	}
	dcpDataLen := binary.BigEndian.Uint16(data[10:12])

	// Parse blocks
	offset := 12
	end := 12 + int(dcpDataLen)
	if end > len(data) {
		end = len(data)
	}

	for offset+4 <= end {
		block := DCPBlock{
			Option:    data[offset],
			SubOption: data[offset+1],
		}
		blockLen := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		offset += 4

		if offset+blockLen > end {
			break
		}
		block.Data = make([]byte, blockLen)
		copy(block.Data, data[offset:offset+blockLen])
		offset += blockLen

		// Pad to even length
		if blockLen%2 != 0 {
			offset++
		}

		f.Blocks = append(f.Blocks, block)
	}

	return f, nil
}

// MarshalDCPFrame serializes a DCP frame to bytes (FrameID through end, no Ethernet header).
func MarshalDCPFrame(f *DCPFrame) []byte {
	// Calculate total block data length
	var blockData []byte
	for _, block := range f.Blocks {
		blockData = append(blockData, block.Option, block.SubOption)
		blockData = binary.BigEndian.AppendUint16(blockData, uint16(len(block.Data)))
		blockData = append(blockData, block.Data...)
		// Pad to even length
		if len(block.Data)%2 != 0 {
			blockData = append(blockData, 0x00)
		}
	}

	buf := make([]byte, 12+len(blockData))
	binary.BigEndian.PutUint16(buf[0:2], f.FrameID)
	buf[2] = f.ServiceID
	buf[3] = f.ServiceType
	binary.BigEndian.PutUint32(buf[4:8], f.Xid)
	binary.BigEndian.PutUint16(buf[8:10], f.ResponseDelay)
	binary.BigEndian.PutUint16(buf[10:12], uint16(len(blockData)))
	copy(buf[12:], blockData)

	return buf
}

// DCP block builder helpers for constructing response blocks.

// DCPBlockNameOfStation creates a NameOfStation response block.
func DCPBlockNameOfStation(name string) DCPBlock {
	// BlockInfo (2 bytes) + name
	data := make([]byte, 2+len(name))
	binary.BigEndian.PutUint16(data[0:2], 0x0000) // BlockInfo: reserved
	copy(data[2:], name)
	return DCPBlock{Option: DCPOptionDeviceProperties, SubOption: DCPSubOptionDevNameOfStation, Data: data}
}

// DCPBlockVendor creates a Vendor (type of station) response block.
func DCPBlockVendor(vendor string) DCPBlock {
	data := make([]byte, 2+len(vendor))
	binary.BigEndian.PutUint16(data[0:2], 0x0000)
	copy(data[2:], vendor)
	return DCPBlock{Option: DCPOptionDeviceProperties, SubOption: DCPSubOptionDevVendor, Data: data}
}

// DCPBlockDeviceID creates a DeviceID response block (VendorID + DeviceID).
func DCPBlockDeviceID(vendorID, deviceID uint16) DCPBlock {
	data := make([]byte, 6) // BlockInfo(2) + VendorID(2) + DeviceID(2)
	binary.BigEndian.PutUint16(data[0:2], 0x0000)
	binary.BigEndian.PutUint16(data[2:4], vendorID)
	binary.BigEndian.PutUint16(data[4:6], deviceID)
	return DCPBlock{Option: DCPOptionDeviceProperties, SubOption: DCPSubOptionDevID, Data: data}
}

// DCPBlockDeviceRole creates a DeviceRole response block.
func DCPBlockDeviceRole(role uint8) DCPBlock {
	data := []byte{0x00, 0x00, role, 0x00} // BlockInfo(2) + Role(1) + Padding(1)
	return DCPBlock{Option: DCPOptionDeviceProperties, SubOption: DCPSubOptionDevRole, Data: data}
}

// DCPBlockIPSuite creates an IP Suite response block.
func DCPBlockIPSuite(ip, mask, gateway net.IP, blockInfo uint16) DCPBlock {
	data := make([]byte, 14) // BlockInfo(2) + IP(4) + Mask(4) + Gateway(4)
	binary.BigEndian.PutUint16(data[0:2], blockInfo)
	copy(data[2:6], ip.To4())
	copy(data[6:10], mask.To4())
	copy(data[10:14], gateway.To4())
	return DCPBlock{Option: DCPOptionIP, SubOption: DCPSubOptionIPSuite, Data: data}
}

// DCPBlockDeviceInstance creates a DeviceInstance response block.
func DCPBlockDeviceInstance(high, low uint8) DCPBlock {
	data := []byte{0x00, 0x00, high, low} // BlockInfo(2) + InstanceHigh(1) + InstanceLow(1)
	return DCPBlock{Option: DCPOptionDeviceProperties, SubOption: DCPSubOptionDevInstance, Data: data}
}

// DCPBlockDeviceOptions creates a DeviceOptions response block listing supported options.
func DCPBlockDeviceOptions(options []DCPBlock) DCPBlock {
	// Each option is 2 bytes: Option + SubOption
	data := make([]byte, 2+len(options)*2)
	binary.BigEndian.PutUint16(data[0:2], 0x0000) // BlockInfo
	for i, opt := range options {
		data[2+i*2] = opt.Option
		data[2+i*2+1] = opt.SubOption
	}
	return DCPBlock{Option: DCPOptionDeviceProperties, SubOption: DCPSubOptionDevOptions, Data: data}
}

// DCPBlockDHCPClientID creates a DHCP Client Identifier block (Option 61 / SubOption 0x3D).
// Including this in the Identify response signals to tools like PRONETA that the device supports DHCP.
func DCPBlockDHCPClientID(mac net.HardwareAddr) DCPBlock {
	// All DCP response blocks include BlockInfo(2) prefix, then payload.
	// DHCP option 61 payload: type(1) = 0x01 (Ethernet) + MAC(6)
	data := make([]byte, 2+1+len(mac))
	binary.BigEndian.PutUint16(data[0:2], 0x0000) // BlockInfo: reserved
	data[2] = 0x01                                // hardware type: Ethernet
	copy(data[3:], mac)
	return DCPBlock{Option: DCPOptionDHCP, SubOption: 0x3D, Data: data}
}

// DCPBlockControlResponse creates a Control response block (for Set responses).
func DCPBlockControlResponse(option, subOption, errorCode uint8) DCPBlock {
	data := []byte{option, subOption, errorCode}
	return DCPBlock{Option: DCPOptionControl, SubOption: DCPSubOptionControlResponse, Data: data}
}

// ParseIPSuiteBlock extracts IP, mask, and gateway from an IP Suite block's data.
func ParseIPSuiteBlock(data []byte) (ip, mask, gateway net.IP, err error) {
	if len(data) < 12 {
		return nil, nil, nil, fmt.Errorf("IP suite block too short: %d bytes", len(data))
	}
	ip = net.IP(make([]byte, 4))
	mask = net.IP(make([]byte, 4))
	gateway = net.IP(make([]byte, 4))
	copy(ip, data[0:4])
	copy(mask, data[4:8])
	copy(gateway, data[8:12])
	return ip, mask, gateway, nil
}

// IsIdentifyRequest checks if the frame is a DCP Identify multicast request.
func (f *DCPFrame) IsIdentifyRequest() bool {
	return f.FrameID == FrameIDDCPIdentReq && f.ServiceID == DCPServiceIdentify && f.ServiceType == DCPServiceTypeRequest
}

// IsSetRequest checks if the frame is a DCP Set request.
func (f *DCPFrame) IsSetRequest() bool {
	return f.FrameID == FrameIDDCPGetSet && f.ServiceID == DCPServiceSet && f.ServiceType == DCPServiceTypeRequest
}

// IsGetRequest checks if the frame is a DCP Get request.
func (f *DCPFrame) IsGetRequest() bool {
	return f.FrameID == FrameIDDCPGetSet && f.ServiceID == DCPServiceGet && f.ServiceType == DCPServiceTypeRequest
}

// MatchesFilter checks if an Identify request's filter blocks match the device.
// Returns true if ALL filter criteria are satisfied (AND logic).
func (f *DCPFrame) MatchesFilter(stationName string, vendorID, deviceID uint16) bool {
	if !f.IsIdentifyRequest() {
		return false
	}

	// If no filter blocks, it's an "identify all" request
	if len(f.Blocks) == 0 {
		return true
	}

	for _, block := range f.Blocks {
		switch {
		case block.Option == DCPOptionAllSelector && block.SubOption == 0xFF:
			// "All" selector — matches everything
			continue

		case block.Option == DCPOptionDeviceProperties && block.SubOption == DCPSubOptionDevNameOfStation:
			// Filter by station name
			if string(block.Data) != stationName {
				return false
			}

		case block.Option == DCPOptionDeviceProperties && block.SubOption == DCPSubOptionDevID:
			// Filter by VendorID + DeviceID
			if len(block.Data) < 4 {
				return false
			}
			reqVendor := binary.BigEndian.Uint16(block.Data[0:2])
			reqDevice := binary.BigEndian.Uint16(block.Data[2:4])
			if reqVendor != vendorID || reqDevice != deviceID {
				return false
			}

		case block.Option == DCPOptionDeviceProperties && block.SubOption == DCPSubOptionDevAlias:
			// Alias name filter — we don't support aliases yet
			return false
		}
	}

	return true
}
