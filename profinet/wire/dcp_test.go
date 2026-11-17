package wire

import (
	"encoding/binary"
	"net"
	"testing"
)

func TestParseDCPIdentifyAll(t *testing.T) {
	// Construct a minimal DCP Identify All request
	buf := make([]byte, 16)
	binary.BigEndian.PutUint16(buf[0:2], FrameIDDCPIdentReq)
	buf[2] = DCPServiceIdentify
	buf[3] = DCPServiceTypeRequest
	binary.BigEndian.PutUint32(buf[4:8], 0x12345678) // Xid
	binary.BigEndian.PutUint16(buf[8:10], 0x0001)    // ResponseDelay
	// One block: AllSelector (Option 0xFF, SubOption 0xFF)
	binary.BigEndian.PutUint16(buf[10:12], 4) // DCPDataLength
	buf[12] = DCPOptionAllSelector
	buf[13] = 0xFF
	binary.BigEndian.PutUint16(buf[14:16], 0) // BlockLength=0

	f, err := ParseDCPFrame(buf)
	if err != nil {
		t.Fatalf("ParseDCPFrame() error: %v", err)
	}

	if f.FrameID != FrameIDDCPIdentReq {
		t.Errorf("FrameID = 0x%04X, want 0x%04X", f.FrameID, FrameIDDCPIdentReq)
	}
	if f.Xid != 0x12345678 {
		t.Errorf("Xid = 0x%08X, want 0x12345678", f.Xid)
	}
	if !f.IsIdentifyRequest() {
		t.Error("expected IsIdentifyRequest() = true")
	}
	if len(f.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(f.Blocks))
	}
	if f.Blocks[0].Option != DCPOptionAllSelector {
		t.Errorf("block option = 0x%02X, want 0x%02X", f.Blocks[0].Option, DCPOptionAllSelector)
	}
}

func TestParseDCPIdentifyByName(t *testing.T) {
	name := "my-station"
	blockData := []byte(name)
	blockLen := len(blockData)

	// DCP header + 1 block
	buf := make([]byte, 12+4+blockLen)
	binary.BigEndian.PutUint16(buf[0:2], FrameIDDCPIdentReq)
	buf[2] = DCPServiceIdentify
	buf[3] = DCPServiceTypeRequest
	binary.BigEndian.PutUint32(buf[4:8], 0xAABBCCDD)
	binary.BigEndian.PutUint16(buf[10:12], uint16(4+blockLen)) // DCPDataLength
	buf[12] = DCPOptionDeviceProperties
	buf[13] = DCPSubOptionDevNameOfStation
	binary.BigEndian.PutUint16(buf[14:16], uint16(blockLen))
	copy(buf[16:], blockData)

	f, err := ParseDCPFrame(buf)
	if err != nil {
		t.Fatalf("ParseDCPFrame() error: %v", err)
	}

	if !f.MatchesFilter("my-station", 0, 0) {
		t.Error("expected filter match for correct station name")
	}
	if f.MatchesFilter("other-station", 0, 0) {
		t.Error("expected filter mismatch for wrong station name")
	}
}

func TestParseDCPSetIP(t *testing.T) {
	// Set IP request: Option=IP(0x01), SubOption=IPSuite(0x02), Data=IP+Mask+GW
	ip := net.IPv4(192, 168, 1, 100).To4()
	mask := net.IPv4(255, 255, 255, 0).To4()
	gw := net.IPv4(192, 168, 1, 1).To4()

	blockData := make([]byte, 12)
	copy(blockData[0:4], ip)
	copy(blockData[4:8], mask)
	copy(blockData[8:12], gw)

	buf := make([]byte, 12+4+12)
	binary.BigEndian.PutUint16(buf[0:2], FrameIDDCPGetSet)
	buf[2] = DCPServiceSet
	buf[3] = DCPServiceTypeRequest
	binary.BigEndian.PutUint32(buf[4:8], 0x11223344)
	binary.BigEndian.PutUint16(buf[10:12], uint16(4+12))
	buf[12] = DCPOptionIP
	buf[13] = DCPSubOptionIPSuite
	binary.BigEndian.PutUint16(buf[14:16], 12)
	copy(buf[16:], blockData)

	f, err := ParseDCPFrame(buf)
	if err != nil {
		t.Fatalf("ParseDCPFrame() error: %v", err)
	}
	if !f.IsSetRequest() {
		t.Error("expected IsSetRequest() = true")
	}

	block := f.Blocks[0]
	parsedIP, parsedMask, parsedGW, err := ParseIPSuiteBlock(block.Data)
	if err != nil {
		t.Fatalf("ParseIPSuiteBlock() error: %v", err)
	}
	if !parsedIP.Equal(ip) {
		t.Errorf("IP = %s, want %s", parsedIP, ip)
	}
	if !parsedMask.Equal(mask) {
		t.Errorf("Mask = %s, want %s", parsedMask, mask)
	}
	if !parsedGW.Equal(gw) {
		t.Errorf("Gateway = %s, want %s", parsedGW, gw)
	}
}

func TestMarshalDCPRoundTrip(t *testing.T) {
	original := &DCPFrame{
		FrameID:       FrameIDDCPIdentResp,
		ServiceID:     DCPServiceIdentify,
		ServiceType:   DCPServiceTypeResponse,
		Xid:           0xDEADBEEF,
		ResponseDelay: 0,
		Blocks: []DCPBlock{
			DCPBlockNameOfStation("test-device"),
			DCPBlockDeviceID(0x1234, 0x5678),
			DCPBlockDeviceRole(DeviceRoleIODevice),
			DCPBlockIPSuite(
				net.IPv4(10, 0, 0, 1).To4(),
				net.IPv4(255, 255, 255, 0).To4(),
				net.IPv4(10, 0, 0, 254).To4(),
				DCPBlockInfoIPSet,
			),
		},
	}

	data := MarshalDCPFrame(original)
	parsed, err := ParseDCPFrame(data)
	if err != nil {
		t.Fatalf("round-trip parse error: %v", err)
	}

	if parsed.FrameID != original.FrameID {
		t.Errorf("FrameID = 0x%04X, want 0x%04X", parsed.FrameID, original.FrameID)
	}
	if parsed.Xid != original.Xid {
		t.Errorf("Xid = 0x%08X, want 0x%08X", parsed.Xid, original.Xid)
	}
	if parsed.ServiceType != DCPServiceTypeResponse {
		t.Error("expected response service type")
	}
	if len(parsed.Blocks) != len(original.Blocks) {
		t.Fatalf("block count = %d, want %d", len(parsed.Blocks), len(original.Blocks))
	}

	// Verify name block
	nameBlock := parsed.Blocks[0]
	if nameBlock.Option != DCPOptionDeviceProperties || nameBlock.SubOption != DCPSubOptionDevNameOfStation {
		t.Error("first block should be NameOfStation")
	}
	// Data starts with 2-byte BlockInfo, then the name
	if string(nameBlock.Data[2:]) != "test-device" {
		t.Errorf("station name = %q, want %q", string(nameBlock.Data[2:]), "test-device")
	}

	// Verify device ID block
	idBlock := parsed.Blocks[1]
	if len(idBlock.Data) < 6 {
		t.Fatalf("device ID block too short: %d", len(idBlock.Data))
	}
	vid := binary.BigEndian.Uint16(idBlock.Data[2:4])
	did := binary.BigEndian.Uint16(idBlock.Data[4:6])
	if vid != 0x1234 || did != 0x5678 {
		t.Errorf("VendorID:DeviceID = %04X:%04X, want 1234:5678", vid, did)
	}
}

func TestMatchesFilterAllSelector(t *testing.T) {
	f := &DCPFrame{
		FrameID:     FrameIDDCPIdentReq,
		ServiceID:   DCPServiceIdentify,
		ServiceType: DCPServiceTypeRequest,
		Blocks: []DCPBlock{
			{Option: DCPOptionAllSelector, SubOption: 0xFF},
		},
	}

	if !f.MatchesFilter("anything", 0x1234, 0x5678) {
		t.Error("AllSelector should match any device")
	}
}

func TestMatchesFilterByDeviceID(t *testing.T) {
	filterData := make([]byte, 4)
	binary.BigEndian.PutUint16(filterData[0:2], 0x1234)
	binary.BigEndian.PutUint16(filterData[2:4], 0x0001)

	f := &DCPFrame{
		FrameID:     FrameIDDCPIdentReq,
		ServiceID:   DCPServiceIdentify,
		ServiceType: DCPServiceTypeRequest,
		Blocks: []DCPBlock{
			{Option: DCPOptionDeviceProperties, SubOption: DCPSubOptionDevID, Data: filterData},
		},
	}

	if !f.MatchesFilter("any", 0x1234, 0x0001) {
		t.Error("should match correct VendorID:DeviceID")
	}
	if f.MatchesFilter("any", 0x1234, 0x0002) {
		t.Error("should not match wrong DeviceID")
	}
}

func TestMarshalOddLengthBlockPadding(t *testing.T) {
	// Station name with odd length should be padded
	f := &DCPFrame{
		FrameID:     FrameIDDCPIdentResp,
		ServiceID:   DCPServiceIdentify,
		ServiceType: DCPServiceTypeResponse,
		Blocks: []DCPBlock{
			DCPBlockNameOfStation("abc"), // 2 (BlockInfo) + 3 (name) = 5 bytes (odd)
			DCPBlockDeviceRole(DeviceRoleIODevice),
		},
	}

	data := MarshalDCPFrame(f)
	parsed, err := ParseDCPFrame(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(parsed.Blocks) != 2 {
		t.Fatalf("expected 2 blocks after padding, got %d", len(parsed.Blocks))
	}
}
