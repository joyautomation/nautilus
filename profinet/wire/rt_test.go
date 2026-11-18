package wire

import (
	"bytes"
	"encoding/binary"
	"math"
	"net"
	"testing"
)

func TestCyclicRoundTripPadsShortCSDU(t *testing.T) {
	csdu := []byte{0x01, 0x02, 0x03, 0x04, IOxSGood}
	p := AppendCyclic(nil, 0xC001, csdu, 42, DataStatusRunNormal)
	if want := 2 + MinCSDU + 4; len(p) != want {
		t.Fatalf("len = %d, want %d (C-SDU padded to %d)", len(p), want, MinCSDU)
	}
	// The sender pads the C-SDU to MinCSDU, so the IOCR's DataLength is
	// at least MinCSDU and the receiver finds the trailer right after it.
	f, err := ParseCyclic(p, MinCSDU)
	if err != nil {
		t.Fatal(err)
	}
	if f.FrameID != 0xC001 || f.CycleCounter != 42 || f.DataStatus != DataStatusRunNormal || f.TransferStatus != 0 {
		t.Fatalf("got %+v", f)
	}
	if !bytes.Equal(f.CSDU[:len(csdu)], csdu) {
		t.Fatalf("C-SDU = % x, want prefix % x", f.CSDU, csdu)
	}
}

func TestParseCyclicWithEthernetPadding(t *testing.T) {
	// A 44-byte C-SDU frame followed by 6 bytes an Ethernet layer might
	// leave: the receiver passes DataLength and finds the trailer there.
	csdu := make([]byte, 44)
	csdu[0] = 0xAA
	p := AppendCyclic(nil, 0x8001, csdu, 7, DataStatusNormal)
	p = append(p, 0, 0, 0, 0, 0, 0)
	f, err := ParseCyclic(p, 44)
	if err != nil {
		t.Fatal(err)
	}
	if f.CycleCounter != 7 || f.DataStatus != DataStatusNormal || len(f.CSDU) != 44 || f.CSDU[0] != 0xAA {
		t.Fatalf("got %+v", f)
	}
	if _, err := ParseCyclic(p[:20], 44); err == nil {
		t.Fatal("short frame parsed")
	}
}

func TestMeaningOf(t *testing.T) {
	for _, tc := range []struct {
		ds   uint8
		want DataStatusMeaning
	}{
		{DataStatusRunNormal, StatusRun},
		{DataStatusRunNormal &^ DataStatusNoProblem, StatusRun}, // a station problem is diagnosis, not invalid data
		{DataStatusNormal, StatusStop},                          // valid, primary, provider in stop
		{DataStatusRunNormal &^ DataStatusValid, StatusInvalid},
		{DataStatusRunNormal &^ DataStatusPrimary, StatusInvalid}, // backup
		{DataStatusRunNormal | DataStatusIgnore, StatusInvalid},
		{0x00, StatusInvalid},
	} {
		if got := MeaningOf(tc.ds); got != tc.want {
			t.Errorf("MeaningOf(%#02x) = %d, want %d", tc.ds, got, tc.want)
		}
	}
	if !IOxSGoodState(IOxSGood) || IOxSGoodState(IOxSBad) || !IOxSGoodState(0xA0) {
		t.Fatal("IOxS DataState is bit 7")
	}
}

func TestRTARoundTrip(t *testing.T) {
	p := BuildRTA(FrameIDAlarmLow, 3, 4, RTAPDUTypeData, 9, 8, []byte{0xDE, 0xAD})
	r, err := ParseRTA(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.FrameID != FrameIDAlarmLow || r.DstRef != 3 || r.SrcRef != 4 || r.PDUType != RTAPDUTypeData ||
		r.SendSeq != 9 || r.AckSeq != 8 || !bytes.Equal(r.Payload, []byte{0xDE, 0xAD}) {
		t.Fatalf("got %+v", r)
	}
	if _, err := ParseRTA(p[:len(p)-1]); err == nil {
		t.Fatal("truncated var part parsed")
	}
}

func TestBuildLLDP(t *testing.T) {
	mac := net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	tlvs, err := ParseLLDP(BuildLLDP(LLDPInfo{ChassisMAC: mac, StationName: "dev-1", SysDesc: "nautilus", IP: net.IPv4(192, 0, 2, 7)}))
	if err != nil {
		t.Fatal(err)
	}
	by := map[uint8][][]byte{}
	for _, tlv := range tlvs {
		by[tlv.Type] = append(by[tlv.Type], tlv.Value)
	}
	if v := by[LLDPTLVPortID]; len(v) != 1 || string(v[0][1:]) != "port-001" || v[0][0] != 0x07 {
		t.Fatalf("port id %q", v)
	}
	if v := by[LLDPTLVSysName]; len(v) != 1 || string(v[0]) != "dev-1" {
		t.Fatalf("sys name %q", v)
	}
	if v := by[LLDPTLVTTL]; len(v) != 1 || binary.BigEndian.Uint16(v[0]) != 20 {
		t.Fatalf("ttl % x", v)
	}
	if v := by[LLDPTLVMgmtAddr]; len(v) != 1 || !net.IP(v[0][2:6]).Equal(net.IPv4(192, 0, 2, 7)) {
		t.Fatalf("mgmt % x", v)
	}
	org := by[LLDPTLVOrgSpecific]
	if len(org) != 2 {
		t.Fatalf("org TLVs: %d", len(org))
	}
	// PROFINET port status: OUI(3) subtype(1) RTClass2(2) RTClass3(2).
	if !bytes.Equal(org[0], []byte{0x00, 0x0E, 0xCF, PNIOSubtypePortStatus, 0, 0, 0, 0}) {
		t.Fatalf("port status % x", org[0])
	}
	if !bytes.Equal(org[1][4:], mac) || org[1][3] != PNIOSubtypeChassisMAC {
		t.Fatalf("chassis mac % x", org[1])
	}
	noIP, _ := ParseLLDP(BuildLLDP(LLDPInfo{ChassisMAC: mac, StationName: "x"}))
	for _, tlv := range noIP {
		if tlv.Type == LLDPTLVMgmtAddr {
			t.Fatal("management address without an IP")
		}
	}
}

func TestValueRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		f    Format
		in   any
		want any
		raw  []byte
	}{
		{FormatInt8, int64(-2), int64(-2), []byte{0xFE}},
		{FormatUint8, int64(200), int64(200), []byte{0xC8}},
		{FormatInt16, int64(-2), int64(-2), []byte{0xFF, 0xFE}},
		{FormatUint16, int64(0xBEEF), int64(0xBEEF), []byte{0xBE, 0xEF}},
		{FormatInt32, int64(-1), int64(-1), []byte{0xFF, 0xFF, 0xFF, 0xFF}},
		{FormatUint32, int64(0xDEADBEEF), int64(0xDEADBEEF), []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		{FormatInt64, int64(math.MinInt64), int64(math.MinInt64), []byte{0x80, 0, 0, 0, 0, 0, 0, 0}},
		{FormatUint64, int64(math.MaxInt64), int64(math.MaxInt64), []byte{0x7F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
		{FormatFloat32, 1.5, 1.5, []byte{0x3F, 0xC0, 0, 0}},
		{FormatFloat64, -2.0, -2.0, []byte{0xC0, 0, 0, 0, 0, 0, 0, 0}},
		{FormatInt16, 2.6, int64(3), []byte{0x00, 0x03}}, // rounds to nearest
		{FormatUint8, true, int64(1), []byte{0x01}},
	} {
		buf := make([]byte, 1+tc.f.Size())
		if err := Encode(buf, tc.f, 1, 0, tc.in); err != nil {
			t.Fatalf("%s %v: %v", tc.f, tc.in, err)
		}
		if !bytes.Equal(buf[1:], tc.raw) || buf[0] != 0 {
			t.Fatalf("%s %v: % x, want 00 % x", tc.f, tc.in, buf, tc.raw)
		}
		got, err := Decode(buf, tc.f, 1, 0)
		if err != nil || got != tc.want {
			t.Fatalf("%s: decode %v (%T), %v; want %v", tc.f, got, got, err, tc.want)
		}
	}
}

func TestValueBoolKeepsOtherBits(t *testing.T) {
	buf := []byte{0b1010_0000}
	if err := Encode(buf, FormatBool, 0, 1, true); err != nil {
		t.Fatal(err)
	}
	if err := Encode(buf, FormatBool, 0, 7, false); err != nil {
		t.Fatal(err)
	}
	if buf[0] != 0b0010_0010 {
		t.Fatalf("%08b", buf[0])
	}
	for bit, want := range map[uint8]bool{1: true, 5: true, 7: false, 0: false} {
		if v, _ := Decode(buf, FormatBool, 0, bit); v != want {
			t.Fatalf("bit %d = %v", bit, v)
		}
	}
	if err := Encode(buf, FormatBool, 0, 8, true); err == nil {
		t.Fatal("bit 8 accepted")
	}
}

func TestValueRefusesWhatDoesNotFit(t *testing.T) {
	buf := make([]byte, 8)
	for _, tc := range []struct {
		f Format
		v any
	}{
		{FormatUint8, int64(256)},
		{FormatInt8, int64(-129)},
		{FormatUint16, int64(-1)},
		{FormatInt32, 3e9},
		{FormatInt64, math.NaN()},
		{FormatUint64, int64(-1)},
		{FormatInt16, "12"},
	} {
		before := append([]byte(nil), buf...)
		if err := Encode(buf, tc.f, 0, 0, tc.v); err == nil {
			t.Errorf("%s %v accepted", tc.f, tc.v)
		}
		if !bytes.Equal(buf, before) {
			t.Errorf("%s %v: buffer changed on error", tc.f, tc.v)
		}
	}
	if _, err := Decode([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, FormatUint64, 0, 0); err == nil {
		t.Error("uint64 above MaxInt64 decoded into a signed tag")
	}
	if _, err := Decode([]byte{0, 0, 0}, FormatInt32, 0, 0); err == nil {
		t.Error("read past the IO data")
	}
	if err := Encode(buf, FormatFloat64, 4, 0, 1.0); err == nil {
		t.Error("write past the IO data")
	}
}

func TestParseFormat(t *testing.T) {
	for f := FormatBool; f <= FormatFloat64; f++ {
		got, err := ParseFormat(f.String())
		if err != nil || got != f || f.Size() == 0 || f.IECType() == "" {
			t.Fatalf("%s: %v %v", f, got, err)
		}
	}
	if _, err := ParseFormat("string"); err == nil {
		t.Fatal("string accepted")
	}
}
