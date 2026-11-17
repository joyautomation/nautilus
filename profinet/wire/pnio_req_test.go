package wire

import (
	"net"
	"reflect"
	"testing"
)

func oneBlock(t *testing.T, b []byte, typ uint16) PNIOBlock {
	t.Helper()
	blocks, err := ParsePNIOBlocks(b)
	if err != nil || len(blocks) != 1 || blocks[0].Type != typ {
		t.Fatalf("blocks %v %v", blocks, err)
	}
	return blocks[0]
}

func TestARBlockReqRoundTrip(t *testing.T) {
	in := &ARBlockReq{
		ARType: 1, ARUUID: [16]byte{1, 2, 3}, SessionKey: 7,
		CMInitiatorMAC: net.HardwareAddr{2, 0, 0, 0, 0, 1}, CMInitiatorObjUUID: [16]byte{9},
		ARProperties: 0x40000011, CMInitiatorActivityTimeout: 100, CMInitiatorUDPRTPort: 0x8892,
		CMInitiatorStationName: "plc-1",
	}
	got, err := ParseARBlockReq(oneBlock(t, MarshalARBlockReq(in), BlockTypeARBlockReq).Data)
	if err != nil || !reflect.DeepEqual(got, in) {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestIOCRBlockReqRoundTrip(t *testing.T) {
	in := &IOCRBlockReq{
		IOCRType: IOCRTypeOutput, IOCRReference: 2, LT: EtherTypePNIO, IOCRProperties: IOCRPropertyRTClass1,
		DataLength: 40, FrameID: 0xFFFF, SendClockFactor: 32, ReductionRatio: 8, Phase: 1,
		WatchdogFactor: 3, DataHoldFactor: 3, IOCRMulticastMAC: net.HardwareAddr{0, 0, 0, 0, 0, 0},
		NumberOfAPIs: 1,
		APIs: []IOCRAPIEntry{{
			API:           0,
			IODataObjects: []IOCRDataObject{{1, 1, 0}, {2, 1, 5}},
			IOCSs:         []IOCRIOxS{{0, 1, 9}, {1, 2, 10}},
		}},
	}
	got, err := ParseIOCRBlockReq(oneBlock(t, MarshalIOCRBlockReq(in), BlockTypeIOCRBlockReq).Data)
	if err != nil || !reflect.DeepEqual(got, in) {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestAlarmCRBlockReqRoundTrip(t *testing.T) {
	in := &AlarmCRBlockReq{AlarmCRType: 1, LT: EtherTypePNIO, RTATimeoutFactor: 1, RTARetries: 3, LocalAlarmRef: 5, MaxAlarmDataLen: 200}
	got, err := ParseAlarmCRBlockReq(oneBlock(t, MarshalAlarmCRBlockReq(in), BlockTypeAlarmCRBlockReq).Data)
	if err != nil || !reflect.DeepEqual(got, in) {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestExpectedSubmoduleReqRoundTrip(t *testing.T) {
	in := &ExpectedSubmoduleReq{APIs: []ExpectedAPI{{API: 0, Modules: []ExpectedModule{
		{SlotNumber: 0, ModuleIdentNumber: 0x10000, Submodules: []ExpectedSubmodule{
			{SubslotNumber: 1, SubmoduleIdentNumber: 1, DataDescription: []SubmoduleDataDesc{{DataDirectionInput, 0, 1, 1}}},
		}},
		{SlotNumber: 1, ModuleIdentNumber: 0x101, Submodules: []ExpectedSubmodule{
			{SubslotNumber: 1, SubmoduleIdentNumber: 1, SubmoduleProperties: SubmoduleTypeInputOutput, DataDescription: []SubmoduleDataDesc{
				{DataDirectionInput, 8, 1, 1}, {DataDirectionOutput, 4, 1, 1},
			}},
		}},
	}}}}
	got, err := ParseExpectedSubmoduleReq(oneBlock(t, MarshalExpectedSubmoduleReq(in), BlockTypeExpectedSubmoduleReq).Data)
	if err != nil || !reflect.DeepEqual(got, in) {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestControlRecordAndBodyRoundTrip(t *testing.T) {
	c := IODControl{ARUUID: [16]byte{4}, SessionKey: 3, Command: ControlCmdPrmEnd}
	got, err := ParseIODControl(oneBlock(t, MarshalIODControl(BlockTypeIODControlReq, c), BlockTypeIODControlReq).Data)
	if err != nil || got != c {
		t.Fatalf("control %+v %v", got, err)
	}
	h := RecordHeader{Seq: 2, ARUUID: [16]byte{5}, Slot: 1, Subslot: 1, Index: 0xAFF0, DataLength: 64}
	rh, err := ParseRecordHeader(oneBlock(t, MarshalRecordHeader(BlockTypeIODReadReqHeader, h), BlockTypeIODReadReqHeader).Data)
	if err != nil || rh != h {
		t.Fatalf("record header %+v %v", rh, err)
	}
	for _, le := range []bool{false, true} {
		blocks := MarshalIODControl(BlockTypeIODControlRes, c)
		argsMax, parsed, err := ParsePNIORequestBody(MarshalPNIORequestBody(blocks, 4096, le), le)
		if err != nil || argsMax != 4096 || len(parsed) != 1 {
			t.Fatalf("request body le=%v: %d %v %v", le, argsMax, parsed, err)
		}
		st, parsed, err := ParsePNIOResponseBody(MarshalPNIOResponseBody(PNIOStatusOK, blocks, 4096, le), le)
		if err != nil || !st.IsOK() || len(parsed) != 1 {
			t.Fatalf("response body le=%v: %v %v %v", le, st, parsed, err)
		}
	}
}

func TestModuleDiffRoundTrip(t *testing.T) {
	in := []DiffModule{
		{API: 0, Slot: 1, ModuleIdent: 0x101, State: ModuleStateProperModule, Submodules: []DiffSubmodule{{1, 1, SubmoduleStateWrong}}},
		{API: 0, Slot: 3, ModuleIdent: 0, State: ModuleStateNoModule},
	}
	got, err := ParseModuleDiff(oneBlock(t, MarshalModuleDiff(in), BlockTypeModuleDiffBlock).Data)
	if err != nil || !reflect.DeepEqual(got, in) {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestLayoutIOCRs(t *testing.T) {
	in, out := LayoutIOCRs([]IOSubmodule{
		{0, 1, 0, 0}, // DAP: no IO
		{1, 1, 4, 2}, // input and output
		{2, 1, 0, 3}, // output only
	})
	wantInObjs := []IOCRDataObject{{0, 1, 0}, {1, 1, 1}}
	wantInIOCS := []IOCRIOxS{{1, 1, 6}, {2, 1, 7}}
	wantOutObjs := []IOCRDataObject{{1, 1, 0}, {2, 1, 3}}
	wantOutIOCS := []IOCRIOxS{{0, 1, 7}, {1, 1, 8}}
	if !reflect.DeepEqual(in.API.IODataObjects, wantInObjs) || !reflect.DeepEqual(in.API.IOCSs, wantInIOCS) ||
		!reflect.DeepEqual(out.API.IODataObjects, wantOutObjs) || !reflect.DeepEqual(out.API.IOCSs, wantOutIOCS) {
		t.Fatalf("in %+v\nout %+v", in, out)
	}
	if in.DataLength != MinCSDU || out.DataLength != MinCSDU {
		t.Fatalf("lengths %d %d", in.DataLength, out.DataLength)
	}
	big, _ := LayoutIOCRs([]IOSubmodule{{1, 1, 100, 0}})
	if big.DataLength != 101 {
		t.Fatalf("big %d", big.DataLength)
	}
}

func TestParseWriteArgsMultiple(t *testing.T) {
	inner := func(idx uint16, data []byte) []byte {
		b := append(MarshalRecordHeader(BlockTypeIODWriteReqHeader, RecordHeader{Slot: 1, Subslot: 1, Index: idx, DataLength: uint32(len(data))}), data...)
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
		return b
	}
	nested := append(inner(1, []byte{1, 2, 3}), inner(2, []byte{4})...)
	args := append(MarshalRecordHeader(BlockTypeIODWriteReqHeader, RecordHeader{Index: IndexMultipleWrite, DataLength: uint32(len(nested))}), nested...)
	ws, err := ParseWriteArgs(args)
	if err != nil || len(ws) != 3 || ws[1].Header.Index != 1 || !reflect.DeepEqual(ws[1].Data, []byte{1, 2, 3}) ||
		ws[2].Header.Index != 2 || !reflect.DeepEqual(ws[2].Data, []byte{4}) {
		t.Fatalf("%+v %v", ws, err)
	}
	if _, err := ParseWriteArgs([]byte{0, 1, 2}); err == nil {
		t.Fatal("garbage parsed")
	}
}
