package wire

import (
	"encoding/binary"
	"log/slog"
	"net"
	"testing"
	"time"
)

// --- RPC Header tests ---

func TestRPCHeaderRoundTripBE(t *testing.T) {
	orig := &RPCHeader{
		RPCVersion:       RPCVersionCL,
		PacketType:       RPCPTypeRequest,
		Flags1:           RPCFlagLastFrag | RPCFlagIdempotent,
		Flags2:           0,
		DataRep:          [3]byte{0x00, 0x00, 0x00}, // big-endian
		SerialHigh:       0x01,
		ObjectUUID:       [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		InterfaceUUID:    UUIDPNIOInterface,
		ActivityUUID:     [16]byte{0xAA, 0xBB, 0xCC, 0xDD},
		ServerBootTime:   12345,
		InterfaceVersion: 1,
		SequenceNum:      42,
		OpNum:            RPCOpConnect,
		InterfaceHint:    0xFFFF,
		ActivityHint:     0xFFFF,
		FragmentLen:      256,
		FragmentNum:      0,
		AuthProto:        0,
		SerialLow:        0x02,
	}

	data := orig.Marshal()
	if len(data) != RPCHeaderSize {
		t.Fatalf("expected %d bytes, got %d", RPCHeaderSize, len(data))
	}

	parsed, err := ParseRPCHeader(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if parsed.RPCVersion != orig.RPCVersion {
		t.Errorf("RPCVersion: got %d, want %d", parsed.RPCVersion, orig.RPCVersion)
	}
	if parsed.PacketType != orig.PacketType {
		t.Errorf("PacketType: got %d, want %d", parsed.PacketType, orig.PacketType)
	}
	if parsed.ObjectUUID != orig.ObjectUUID {
		t.Errorf("ObjectUUID mismatch")
	}
	if parsed.InterfaceUUID != orig.InterfaceUUID {
		t.Errorf("InterfaceUUID mismatch: got %x, want %x", parsed.InterfaceUUID, orig.InterfaceUUID)
	}
	if parsed.OpNum != orig.OpNum {
		t.Errorf("OpNum: got %d, want %d", parsed.OpNum, orig.OpNum)
	}
	if parsed.SequenceNum != orig.SequenceNum {
		t.Errorf("SequenceNum: got %d, want %d", parsed.SequenceNum, orig.SequenceNum)
	}
	if parsed.FragmentLen != orig.FragmentLen {
		t.Errorf("FragmentLen: got %d, want %d", parsed.FragmentLen, orig.FragmentLen)
	}
	if parsed.SerialHigh != orig.SerialHigh || parsed.SerialLow != orig.SerialLow {
		t.Errorf("Serial: got %d/%d, want %d/%d", parsed.SerialHigh, parsed.SerialLow, orig.SerialHigh, orig.SerialLow)
	}
}

func TestRPCHeaderRoundTripLE(t *testing.T) {
	orig := &RPCHeader{
		RPCVersion:       RPCVersionCL,
		PacketType:       RPCPTypeRequest,
		Flags1:           RPCFlagLastFrag,
		DataRep:          [3]byte{0x10, 0x00, 0x00}, // little-endian
		ObjectUUID:       [16]byte{0xDE, 0xAD, 0xBE, 0xEF},
		InterfaceUUID:    UUIDPNIOInterface,
		ActivityUUID:     [16]byte{0x11, 0x22, 0x33, 0x44},
		ServerBootTime:   99999,
		InterfaceVersion: 1,
		SequenceNum:      7,
		OpNum:            RPCOpControl,
		InterfaceHint:    0xFFFF,
		ActivityHint:     0xFFFF,
		FragmentLen:      100,
		FragmentNum:      0,
	}

	data := orig.Marshal()
	parsed, err := ParseRPCHeader(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if parsed.OpNum != RPCOpControl {
		t.Errorf("OpNum: got %d, want %d", parsed.OpNum, RPCOpControl)
	}
	if parsed.SequenceNum != 7 {
		t.Errorf("SequenceNum: got %d, want 7", parsed.SequenceNum)
	}
	if parsed.InterfaceUUID != UUIDPNIOInterface {
		t.Errorf("InterfaceUUID mismatch after LE round-trip")
	}
	if parsed.FragmentLen != 100 {
		t.Errorf("FragmentLen: got %d, want 100", parsed.FragmentLen)
	}
}

func TestRPCHeaderTooShort(t *testing.T) {
	_, err := ParseRPCHeader(make([]byte, 40))
	if err == nil {
		t.Error("expected error for short header")
	}
}

// --- UUID wire conversion tests ---

func TestUUIDWireConversionBE(t *testing.T) {
	uuid := [16]byte{0xDE, 0xA0, 0x00, 0x01, 0x6C, 0x97, 0x11, 0xD1, 0x82, 0x71, 0x00, 0xA0, 0x24, 0x42, 0xDF, 0x7D}
	wire := UUIDToWire(uuid, false) // BE — no swap
	result := UUIDFromWire(wire, false)
	if result != uuid {
		t.Errorf("BE round-trip failed: got %x, want %x", result, uuid)
	}
}

func TestUUIDWireConversionLE(t *testing.T) {
	uuid := UUIDPNIOInterface
	wire := UUIDToWire(uuid, true) // LE — swap first 3 groups

	// Verify first 4 bytes are reversed
	if wire[0] != uuid[3] || wire[1] != uuid[2] || wire[2] != uuid[1] || wire[3] != uuid[0] {
		t.Errorf("LE wire first group not swapped: got %x", wire[:4])
	}

	// Round-trip
	result := UUIDFromWire(wire, true)
	if result != uuid {
		t.Errorf("LE round-trip failed: got %x, want %x", result, uuid)
	}
}

// --- PNIO request/response body tests ---

func TestPNIORequestBodyRoundTrip(t *testing.T) {
	// Create a simple block
	block := MarshalPNIOBlock(BlockTypeARBlockReq, 1, 0, []byte{0x01, 0x02, 0x03, 0x04})

	// Build request body (BE)
	argsLen := uint32(len(block))
	body := make([]byte, 20+len(block))
	binary.BigEndian.PutUint32(body[0:4], 1024) // ArgsMaximum
	binary.BigEndian.PutUint32(body[4:8], argsLen)
	binary.BigEndian.PutUint32(body[8:12], argsLen)  // MaxCount
	binary.BigEndian.PutUint32(body[12:16], 0)       // Offset
	binary.BigEndian.PutUint32(body[16:20], argsLen) // ActualCount
	copy(body[20:], block)

	argsMax, blocks, err := ParsePNIORequestBody(body, false)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if argsMax != 1024 {
		t.Errorf("ArgsMaximum: got %d, want 1024", argsMax)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Type != BlockTypeARBlockReq {
		t.Errorf("block type: got 0x%04X, want 0x%04X", blocks[0].Type, BlockTypeARBlockReq)
	}
}

func TestPNIOResponseBody(t *testing.T) {
	blocks := MarshalPNIOBlock(BlockTypeARBlockRes, 1, 0, []byte{0xAA, 0xBB})
	const reqArgsMax uint32 = 386
	body := MarshalPNIOResponseBody(PNIOStatusOK, blocks, reqArgsMax, false)

	// Response layout (per p-net pf_cmrpc_rm_connect_rsp):
	//   PNIOStatus(4) + ArgsLength(4) + MaxCount(4) + Offset(4) + ActualCount(4) + blocks
	// ArgsLength/ActualCount cover blocks only (not the status).
	// MaxCount is the conformant array bound preserved from the request.
	wantLen := uint32(len(blocks))

	// Status is at the START of the body
	if body[0] != 0 || body[1] != 0 || body[2] != 0 || body[3] != 0 {
		t.Errorf("expected OK status at start, got %x", body[0:4])
	}

	argsLen := binary.BigEndian.Uint32(body[4:8])
	maxCount := binary.BigEndian.Uint32(body[8:12])
	offset := binary.BigEndian.Uint32(body[12:16])
	actualCount := binary.BigEndian.Uint32(body[16:20])
	if argsLen != wantLen {
		t.Errorf("ArgsLength: got %d, want %d", argsLen, wantLen)
	}
	if maxCount != reqArgsMax {
		t.Errorf("MaxCount: got %d, want %d (request ArgsMaximum)", maxCount, reqArgsMax)
	}
	if offset != 0 {
		t.Errorf("Offset: got %d, want 0", offset)
	}
	if actualCount != wantLen {
		t.Errorf("ActualCount: got %d, want %d", actualCount, wantLen)
	}
}

// --- PNIO Block tests ---

func TestPNIOBlockRoundTrip(t *testing.T) {
	payload := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	data := MarshalPNIOBlock(BlockTypeARBlockReq, 1, 0, payload)

	blocks, err := ParsePNIOBlocks(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}

	b := blocks[0]
	if b.Type != BlockTypeARBlockReq {
		t.Errorf("type: got 0x%04X, want 0x%04X", b.Type, BlockTypeARBlockReq)
	}
	if b.VersionHigh != 1 || b.VersionLow != 0 {
		t.Errorf("version: got %d.%d, want 1.0", b.VersionHigh, b.VersionLow)
	}
	if len(b.Data) != len(payload) {
		t.Errorf("data length: got %d, want %d", len(b.Data), len(payload))
	}
	for i, v := range payload {
		if b.Data[i] != v {
			t.Errorf("data[%d]: got 0x%02X, want 0x%02X", i, b.Data[i], v)
		}
	}
}

func TestPNIOMultipleBlocks(t *testing.T) {
	var data []byte
	data = append(data, MarshalPNIOBlock(BlockTypeARBlockReq, 1, 0, []byte{0x01, 0x02})...)
	// Pad to 4-byte alignment
	for len(data)%4 != 0 {
		data = append(data, 0)
	}
	data = append(data, MarshalPNIOBlock(BlockTypeIOCRBlockReq, 1, 0, []byte{0x03, 0x04, 0x05, 0x06})...)

	blocks, err := ParsePNIOBlocks(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].Type != BlockTypeARBlockReq {
		t.Errorf("block 0 type: got 0x%04X", blocks[0].Type)
	}
	if blocks[1].Type != BlockTypeIOCRBlockReq {
		t.Errorf("block 1 type: got 0x%04X", blocks[1].Type)
	}
}

// --- Alarm tests ---

func TestAlarmACKMarshal(t *testing.T) {
	ack := BuildAlarmAck(FrameIDAlarmHigh, 0x0001, 0x0002, 5)
	if len(ack) != 14 {
		t.Fatalf("expected 14 bytes, got %d", len(ack))
	}

	frameID := binary.BigEndian.Uint16(ack[0:2])
	if frameID != FrameIDAlarmHigh {
		t.Errorf("FrameID: got 0x%04X, want 0x%04X", frameID, FrameIDAlarmHigh)
	}

	dstRef := binary.BigEndian.Uint16(ack[2:4])
	if dstRef != 0x0001 {
		t.Errorf("dst_ref: got 0x%04X, want 0x0001", dstRef)
	}
	srcRef := binary.BigEndian.Uint16(ack[4:6])
	if srcRef != 0x0002 {
		t.Errorf("src_ref: got 0x%04X, want 0x0002", srcRef)
	}

	// pdu_type byte: low nibble = type (3 = ACK), high nibble = version (1)
	pduTypeByte := ack[6]
	if (pduTypeByte & 0x0F) != RTAPDUTypeACK {
		t.Errorf("PDU type: got 0x%02X, want 0x%02X", pduTypeByte&0x0F, RTAPDUTypeACK)
	}
	if (pduTypeByte>>4)&0x0F != 0x01 {
		t.Errorf("PDU version: got 0x%02X, want 0x01", (pduTypeByte>>4)&0x0F)
	}

	ackSeq := binary.BigEndian.Uint16(ack[10:12])
	if ackSeq != 5 {
		t.Errorf("AckSeqNum: got %d, want 5", ackSeq)
	}

	varPartLen := binary.BigEndian.Uint16(ack[12:14])
	if varPartLen != 0 {
		t.Errorf("var_part_len: got %d, want 0", varPartLen)
	}
}

func TestIsRTCyclicFrame(t *testing.T) {
	tests := []struct {
		frameID uint16
		want    bool
	}{
		{0x0100, true},
		{0xC001, true},
		{0xF7FF, true},
		{0xF800, false}, // acyclic
		{0xFC01, false}, // alarm high
		{0xFEFC, false}, // DCP
		{0x00FF, false}, // below range
	}
	for _, tt := range tests {
		if got := IsRTCyclicFrame(tt.frameID); got != tt.want {
			t.Errorf("IsRTCyclicFrame(0x%04X) = %v, want %v", tt.frameID, got, tt.want)
		}
	}
}

func TestIsAlarmFrame(t *testing.T) {
	if !IsAlarmFrame(FrameIDAlarmHigh) {
		t.Error("expected FrameIDAlarmHigh to be alarm frame")
	}
	if !IsAlarmFrame(FrameIDAlarmLow) {
		t.Error("expected FrameIDAlarmLow to be alarm frame")
	}
	if IsAlarmFrame(0xC001) {
		t.Error("expected 0xC001 to not be alarm frame")
	}
}

// --- PNIOStatus tests ---

func TestPNIOStatusOK(t *testing.T) {
	if !PNIOStatusOK.IsOK() {
		t.Error("PNIOStatusOK should be OK")
	}

	data := PNIOStatusOK.Marshal()
	if len(data) != 4 {
		t.Fatalf("expected 4 bytes, got %d", len(data))
	}
	for i, v := range data {
		if v != 0 {
			t.Errorf("byte %d: got 0x%02X, want 0x00", i, v)
		}
	}
}

func TestPNIOStatusError(t *testing.T) {
	status := PNIOStatus{0xDE, 0x81, 0x01, 0x00}
	if status.IsOK() {
		t.Error("error status should not be OK")
	}
}

// --- I&M0 tests ---

func TestIM0BlockMarshal(t *testing.T) {
	im0 := &IM0Data{
		VendorID:         0x1234,
		HWRevision:       1,
		SWRevisionPrefix: 'V',
		SWRevision:       [3]byte{1, 2, 3},
		IMVersion:        0x0101,
		IMSupported:      0x001E,
	}
	copy(im0.OrderID[:], []byte("TestDevice"))
	copy(im0.IMSerialNumber[:], []byte("SN0001"))

	block := MarshalIM0Block(im0)
	if len(block) < 6 { // At minimum, header
		t.Fatalf("block too short: %d bytes", len(block))
	}

	// Parse as PNIO block
	blocks, err := ParsePNIOBlocks(block)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Type != BlockTypeIM0 {
		t.Errorf("block type: got 0x%04X, want 0x%04X", blocks[0].Type, BlockTypeIM0)
	}
}

// --- IODControlRes test ---

func TestMarshalIODControlRes(t *testing.T) {
	arUUID := [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10}
	block := MarshalIODControlRes(arUUID, 0x0001, ControlCmdDone)

	blocks, err := ParsePNIOBlocks(block)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Type != BlockTypeIODControlRes {
		t.Errorf("type: got 0x%04X, want 0x%04X", blocks[0].Type, BlockTypeIODControlRes)
	}
}

// --- Helpers ---

func buildTestARBlockData(arUUID [16]byte, arType, sessionKey uint16, mac net.HardwareAddr, stationName string) []byte {
	nameLen := len(stationName)
	data := make([]byte, 52+nameLen)
	binary.BigEndian.PutUint16(data[0:2], arType)
	copy(data[2:18], arUUID[:])
	binary.BigEndian.PutUint16(data[18:20], sessionKey)
	copy(data[20:26], mac)
	// CMInitiatorObjUUID at 26:42 (zeros ok)
	binary.BigEndian.PutUint32(data[42:46], 0x00000001) // ARProperties
	binary.BigEndian.PutUint16(data[46:48], 100)        // ActivityTimeout
	binary.BigEndian.PutUint16(data[48:50], 34964)      // UDPRTPort
	binary.BigEndian.PutUint16(data[50:52], uint16(nameLen))
	copy(data[52:], []byte(stationName))
	return data
}

func buildTestIOCRBlockData(iocrType, iocrRef uint16, dataLen uint16, frameID uint16) []byte {
	data := make([]byte, 40) // minimum IOCR block
	binary.BigEndian.PutUint16(data[0:2], iocrType)
	binary.BigEndian.PutUint16(data[2:4], iocrRef)
	binary.BigEndian.PutUint16(data[4:6], 0x8892)      // LT = PROFINET
	binary.BigEndian.PutUint32(data[6:10], 0x00000001) // Properties = RT Class 1
	binary.BigEndian.PutUint16(data[10:12], dataLen)
	binary.BigEndian.PutUint16(data[12:14], frameID)
	binary.BigEndian.PutUint16(data[14:16], 32)         // SendClockFactor
	binary.BigEndian.PutUint16(data[16:18], 1)          // ReductionRatio
	binary.BigEndian.PutUint16(data[18:20], 1)          // Phase
	binary.BigEndian.PutUint16(data[20:22], 0)          // Sequence
	binary.BigEndian.PutUint32(data[22:26], 0xFFFFFFFF) // FrameSendOffset
	binary.BigEndian.PutUint16(data[26:28], 10)         // WatchdogFactor
	binary.BigEndian.PutUint16(data[28:30], 3)          // DataHoldFactor
	binary.BigEndian.PutUint16(data[30:32], 0)          // IOCRTagHeader
	// MulticastMAC at 32:38 (zeros)
	binary.BigEndian.PutUint16(data[38:40], 0) // NumberOfAPIs
	return data
}

func buildTestAlarmCRData() []byte {
	data := make([]byte, 18)
	binary.BigEndian.PutUint16(data[0:2], 0x0001)   // AlarmCRType
	binary.BigEndian.PutUint16(data[2:4], 0x8892)   // LT
	binary.BigEndian.PutUint32(data[4:8], 0)        // Properties
	binary.BigEndian.PutUint16(data[8:10], 10)      // RTATimeoutFactor
	binary.BigEndian.PutUint16(data[10:12], 3)      // RTARetries
	binary.BigEndian.PutUint16(data[12:14], 0x0001) // LocalAlarmRef
	binary.BigEndian.PutUint16(data[14:16], 200)    // MaxAlarmDataLen
	binary.BigEndian.PutUint16(data[16:18], 0)      // TagHeaderH
	return data
}

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(devNull{}, nil))
}

type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }

func makeTimeout(seconds int) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		<-makeTimer(seconds)
		close(ch)
	}()
	return ch
}

func makeTimer(seconds int) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		t := time.NewTimer(time.Duration(seconds) * time.Second)
		<-t.C
		close(ch)
	}()
	return ch
}
