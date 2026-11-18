package wire

import (
	"encoding/binary"
	"testing"
)

// One fuzz target per parser of untrusted bytes: anything on the wire or
// in a UDP datagram reaches one of these, so none may panic. The seeds
// run as ordinary tests; CI's profinet-fuzz job runs each target for a
// while (go test -fuzz). Review finding on #727: a station-name length of
// 0xFFFF wrapped in uint16 and sliced past the end.

func seedBytes(f *testing.F, seeds ...[]byte) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add(make([]byte, 64))
	for _, s := range seeds {
		f.Add(s)
	}
}

// arBlockWrap is the AR block body with the station-name length the
// review reproduced the panic with.
func arBlockWrap() []byte {
	b := make([]byte, 60)
	binary.BigEndian.PutUint16(b[50:52], 0xFFFF)
	return b
}

func FuzzParseARBlockReq(f *testing.F) {
	seedBytes(f, arBlockWrap())
	f.Fuzz(func(t *testing.T, b []byte) { ParseARBlockReq(b) })
}

func FuzzParseIOCRBlockReq(f *testing.F) {
	seedBytes(f)
	f.Fuzz(func(t *testing.T, b []byte) { ParseIOCRBlockReq(b) })
}

func FuzzParseAlarmCRBlockReq(f *testing.F) {
	seedBytes(f)
	f.Fuzz(func(t *testing.T, b []byte) { ParseAlarmCRBlockReq(b) })
}

func FuzzParseExpectedSubmoduleReq(f *testing.F) {
	seedBytes(f)
	f.Fuzz(func(t *testing.T, b []byte) { ParseExpectedSubmoduleReq(b) })
}

func FuzzParsePNIOBlocks(f *testing.F) {
	seedBytes(f, MarshalPNIOBlock(BlockTypeARBlockReq, 1, 0, arBlockWrap()))
	f.Fuzz(func(t *testing.T, b []byte) { ParsePNIOBlocks(b) })
}

func FuzzParsePNIORequestBody(f *testing.F) {
	f.Add([]byte{}, false)
	f.Add(make([]byte, 20), true)
	f.Add(make([]byte, 64), false)
	f.Fuzz(func(t *testing.T, b []byte, le bool) { ParsePNIORequestBody(b, le) })
}

func FuzzParseRPCHeader(f *testing.F) {
	seedBytes(f, make([]byte, RPCHeaderSize))
	f.Fuzz(func(t *testing.T, b []byte) { ParseRPCHeader(b) })
}

func FuzzParseDCPFrame(f *testing.F) {
	seedBytes(f, MarshalDCPFrame(&DCPFrame{
		FrameID: FrameIDDCPIdentReq, ServiceID: DCPServiceIdentify, ServiceType: DCPServiceTypeRequest,
		Blocks: []DCPBlock{{Option: DCPOptionDeviceProperties, SubOption: DCPSubOptionDevNameOfStation, Data: []byte("naut-1")}},
	}))
	f.Fuzz(func(t *testing.T, b []byte) { ParseDCPFrame(b) })
}

func FuzzParseIPSuiteBlock(f *testing.F) {
	seedBytes(f, make([]byte, 12))
	f.Fuzz(func(t *testing.T, b []byte) { ParseIPSuiteBlock(b) })
}

func FuzzParseLLDP(f *testing.F) {
	seedBytes(f)
	f.Fuzz(func(t *testing.T, b []byte) { ParseLLDP(b) })
}

func FuzzParseCyclic(f *testing.F) {
	f.Add([]byte{}, 40)
	f.Add(make([]byte, 46), 40)
	f.Add(make([]byte, 46), -1)
	f.Add(make([]byte, 46), 1<<20)
	f.Fuzz(func(t *testing.T, b []byte, n int) { ParseCyclic(b, n) })
}

func FuzzParseRTA(f *testing.F) {
	seedBytes(f)
	f.Fuzz(func(t *testing.T, b []byte) { ParseRTA(b) })
}

func FuzzParseFormat(f *testing.F) {
	f.Add("float32")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) { ParseFormat(s) })
}

// An IOCR block that declares more data objects than it carries is an
// error, not a silently shorter layout (review nit on #727).
func TestParseIOCRBlockReqTruncated(t *testing.T) {
	b := make([]byte, 40+6+6)
	binary.BigEndian.PutUint16(b[38:40], 1) // one API
	binary.BigEndian.PutUint16(b[44:46], 3) // claims 3 data objects, carries 1
	if _, err := ParseIOCRBlockReq(b); err == nil {
		t.Fatal("truncated IOCR block accepted")
	}
	ok := make([]byte, 40+6+2)
	binary.BigEndian.PutUint16(ok[38:40], 1) // one API, no objects, no IOCS
	if _, err := ParseIOCRBlockReq(ok); err != nil {
		t.Fatalf("complete empty API refused: %v", err)
	}
}
