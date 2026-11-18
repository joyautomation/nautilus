package wire

import (
	"encoding/binary"
	"fmt"
)

// Request-side builders: what an IO controller sends in Connect, Write and
// Control, and the ModuleDiffBlock a device answers with. Each Marshal is
// the inverse of the matching Parse, which the tests hold them to.

// MarshalARBlockReq builds the ARBlockReq.
func MarshalARBlockReq(ar *ARBlockReq) []byte {
	p := make([]byte, 52, 52+len(ar.CMInitiatorStationName))
	binary.BigEndian.PutUint16(p[0:2], ar.ARType)
	copy(p[2:18], ar.ARUUID[:])
	binary.BigEndian.PutUint16(p[18:20], ar.SessionKey)
	copy(p[20:26], ar.CMInitiatorMAC)
	copy(p[26:42], ar.CMInitiatorObjUUID[:])
	binary.BigEndian.PutUint32(p[42:46], ar.ARProperties)
	binary.BigEndian.PutUint16(p[46:48], ar.CMInitiatorActivityTimeout)
	binary.BigEndian.PutUint16(p[48:50], ar.CMInitiatorUDPRTPort)
	binary.BigEndian.PutUint16(p[50:52], uint16(len(ar.CMInitiatorStationName)))
	p = append(p, ar.CMInitiatorStationName...)
	return MarshalPNIOBlock(BlockTypeARBlockReq, 1, 0, p)
}

// MarshalIOCRBlockReq builds an IOCRBlockReq with its APIs, IODataObjects
// and IOCS entries (NumberOfAPIs is taken from len(APIs)).
func MarshalIOCRBlockReq(r *IOCRBlockReq) []byte {
	p := make([]byte, 40)
	binary.BigEndian.PutUint16(p[0:2], r.IOCRType)
	binary.BigEndian.PutUint16(p[2:4], r.IOCRReference)
	binary.BigEndian.PutUint16(p[4:6], r.LT)
	binary.BigEndian.PutUint32(p[6:10], r.IOCRProperties)
	binary.BigEndian.PutUint16(p[10:12], r.DataLength)
	binary.BigEndian.PutUint16(p[12:14], r.FrameID)
	binary.BigEndian.PutUint16(p[14:16], r.SendClockFactor)
	binary.BigEndian.PutUint16(p[16:18], r.ReductionRatio)
	binary.BigEndian.PutUint16(p[18:20], r.Phase)
	binary.BigEndian.PutUint16(p[20:22], r.Sequence)
	binary.BigEndian.PutUint32(p[22:26], r.FrameSendOffset)
	binary.BigEndian.PutUint16(p[26:28], r.WatchdogFactor)
	binary.BigEndian.PutUint16(p[28:30], r.DataHoldFactor)
	binary.BigEndian.PutUint16(p[30:32], r.IOCRTagHeader)
	copy(p[32:38], r.IOCRMulticastMAC)
	binary.BigEndian.PutUint16(p[38:40], uint16(len(r.APIs)))
	for _, a := range r.APIs {
		p = binary.BigEndian.AppendUint32(p, a.API)
		p = binary.BigEndian.AppendUint16(p, uint16(len(a.IODataObjects)))
		for _, o := range a.IODataObjects {
			p = binary.BigEndian.AppendUint16(p, o.SlotNumber)
			p = binary.BigEndian.AppendUint16(p, o.SubslotNumber)
			p = binary.BigEndian.AppendUint16(p, o.FrameOffset)
		}
		p = binary.BigEndian.AppendUint16(p, uint16(len(a.IOCSs)))
		for _, o := range a.IOCSs {
			p = binary.BigEndian.AppendUint16(p, o.SlotNumber)
			p = binary.BigEndian.AppendUint16(p, o.SubslotNumber)
			p = binary.BigEndian.AppendUint16(p, o.FrameOffset)
		}
	}
	return MarshalPNIOBlock(BlockTypeIOCRBlockReq, 1, 0, p)
}

// MarshalAlarmCRBlockReq builds the AlarmCRBlockReq.
func MarshalAlarmCRBlockReq(r *AlarmCRBlockReq) []byte {
	p := make([]byte, 20)
	binary.BigEndian.PutUint16(p[0:2], r.AlarmCRType)
	binary.BigEndian.PutUint16(p[2:4], r.LT)
	binary.BigEndian.PutUint32(p[4:8], r.AlarmCRProperties)
	binary.BigEndian.PutUint16(p[8:10], r.RTATimeoutFactor)
	binary.BigEndian.PutUint16(p[10:12], r.RTARetries)
	binary.BigEndian.PutUint16(p[12:14], r.LocalAlarmRef)
	binary.BigEndian.PutUint16(p[14:16], r.MaxAlarmDataLen)
	binary.BigEndian.PutUint16(p[16:18], r.AlarmCRTagHeaderH)
	binary.BigEndian.PutUint16(p[18:20], r.AlarmCRTagHeaderL)
	return MarshalPNIOBlock(BlockTypeAlarmCRBlockReq, 1, 0, p)
}

// Submodule properties: the type in bits 0–1.
const (
	SubmoduleTypeNoIO        uint16 = 0
	SubmoduleTypeInput       uint16 = 1
	SubmoduleTypeOutput      uint16 = 2
	SubmoduleTypeInputOutput uint16 = 3

	DataDirectionInput  uint16 = 1
	DataDirectionOutput uint16 = 2
)

// MarshalExpectedSubmoduleReq builds one ExpectedSubmoduleBlockReq. Each
// module is one API entry (the wire repeats the API per slot; see
// ParseExpectedSubmoduleReq). A submodule carries one DataDescription, or
// two (input then output) when its type is input-output.
func MarshalExpectedSubmoduleReq(r *ExpectedSubmoduleReq) []byte {
	var n uint16
	var p []byte
	for _, a := range r.APIs {
		for _, m := range a.Modules {
			n++
			p = binary.BigEndian.AppendUint32(p, a.API)
			p = binary.BigEndian.AppendUint16(p, m.SlotNumber)
			p = binary.BigEndian.AppendUint32(p, m.ModuleIdentNumber)
			p = binary.BigEndian.AppendUint16(p, m.ModuleProperties)
			p = binary.BigEndian.AppendUint16(p, uint16(len(m.Submodules)))
			for _, s := range m.Submodules {
				p = binary.BigEndian.AppendUint16(p, s.SubslotNumber)
				p = binary.BigEndian.AppendUint32(p, s.SubmoduleIdentNumber)
				p = binary.BigEndian.AppendUint16(p, s.SubmoduleProperties)
				for _, d := range s.DataDescription {
					p = binary.BigEndian.AppendUint16(p, d.DataDirection)
					p = binary.BigEndian.AppendUint16(p, d.DataLength)
					p = append(p, d.LengthIOCS, d.LengthIOPS)
				}
			}
		}
	}
	return MarshalPNIOBlock(BlockTypeExpectedSubmoduleReq, 1, 0, append(binary.BigEndian.AppendUint16(nil, n), p...))
}

// IODControl is the body of IODControlReq/Res and the CControl blocks.
type IODControl struct {
	ARUUID     [16]byte
	SessionKey uint16
	AlarmSeq   uint16
	Command    uint16
	BlockProps uint16
}

// MarshalIODControl builds a control block of blockType (IODControlReq for
// PrmEnd, AppRdyBlockReq, their responses, …).
func MarshalIODControl(blockType uint16, c IODControl) []byte {
	p := make([]byte, 26)
	copy(p[2:18], c.ARUUID[:])
	binary.BigEndian.PutUint16(p[18:20], c.SessionKey)
	binary.BigEndian.PutUint16(p[20:22], c.AlarmSeq)
	binary.BigEndian.PutUint16(p[22:24], c.Command)
	binary.BigEndian.PutUint16(p[24:26], c.BlockProps)
	return MarshalPNIOBlock(blockType, 1, 0, p)
}

// ParseIODControl decodes a control block's data.
func ParseIODControl(data []byte) (IODControl, error) {
	if len(data) < 26 {
		return IODControl{}, fmt.Errorf("IODControl block too short: %d bytes", len(data))
	}
	var c IODControl
	copy(c.ARUUID[:], data[2:18])
	c.SessionKey = binary.BigEndian.Uint16(data[18:20])
	c.AlarmSeq = binary.BigEndian.Uint16(data[20:22])
	c.Command = binary.BigEndian.Uint16(data[22:24])
	c.BlockProps = binary.BigEndian.Uint16(data[24:26])
	return c, nil
}

// RecordHeader is an IODReadReqHeader/IODWriteReqHeader (and the fields a
// response header shares with it).
type RecordHeader struct {
	Seq        uint16
	ARUUID     [16]byte
	API        uint32
	Slot       uint16
	Subslot    uint16
	Index      uint16
	DataLength uint32
}

// recordHeaderLen is the data length of a read/write request or response
// header block (after the block header and version).
const recordHeaderLen = 58

// MarshalRecordHeader builds a read or write request header block.
func MarshalRecordHeader(blockType uint16, h RecordHeader) []byte {
	p := make([]byte, recordHeaderLen)
	binary.BigEndian.PutUint16(p[0:2], h.Seq)
	copy(p[2:18], h.ARUUID[:])
	binary.BigEndian.PutUint32(p[18:22], h.API)
	binary.BigEndian.PutUint16(p[22:24], h.Slot)
	binary.BigEndian.PutUint16(p[24:26], h.Subslot)
	binary.BigEndian.PutUint16(p[28:30], h.Index)
	binary.BigEndian.PutUint32(p[30:34], h.DataLength)
	return MarshalPNIOBlock(blockType, 1, 0, p)
}

// ParseRecordHeader decodes a read/write request or response header.
func ParseRecordHeader(data []byte) (RecordHeader, error) {
	if len(data) < 34 {
		return RecordHeader{}, fmt.Errorf("record header too short: %d bytes", len(data))
	}
	var h RecordHeader
	h.Seq = binary.BigEndian.Uint16(data[0:2])
	copy(h.ARUUID[:], data[2:18])
	h.API = binary.BigEndian.Uint32(data[18:22])
	h.Slot = binary.BigEndian.Uint16(data[22:24])
	h.Subslot = binary.BigEndian.Uint16(data[24:26])
	h.Index = binary.BigEndian.Uint16(data[28:30])
	h.DataLength = binary.BigEndian.Uint32(data[30:34])
	return h, nil
}

// MarshalPNIORequestBody is the NDR request body: ArgsMaximum, ArgsLength,
// and the conformant varying array (MaxCount, Offset, ActualCount) of
// blocks, in the RPC header's byte order.
func MarshalPNIORequestBody(blocks []byte, argsMax uint32, le bool) []byte {
	var order binary.ByteOrder = binary.BigEndian
	if le {
		order = binary.LittleEndian
	}
	b := make([]byte, 20, 20+len(blocks))
	order.PutUint32(b[0:4], argsMax)
	order.PutUint32(b[4:8], uint32(len(blocks)))
	order.PutUint32(b[8:12], argsMax)
	order.PutUint32(b[12:16], 0)
	order.PutUint32(b[16:20], uint32(len(blocks)))
	return append(b, blocks...)
}

// ParsePNIOResponseBody is MarshalPNIOResponseBody's inverse.
func ParsePNIOResponseBody(data []byte, le bool) (PNIOStatus, []PNIOBlock, error) {
	if len(data) < 20 {
		return PNIOStatus{}, nil, fmt.Errorf("PNIO response body too short: %d", len(data))
	}
	var order binary.ByteOrder = binary.BigEndian
	if le {
		order = binary.LittleEndian
	}
	st := PNIOStatus{data[0], data[1], data[2], data[3]}
	n := order.Uint32(data[16:20])
	body := data[20:]
	if int(n) < len(body) {
		body = body[:n]
	}
	if len(body) == 0 {
		return st, nil, nil
	}
	blocks, err := ParsePNIOBlocks(body)
	return st, blocks, err
}

// Module states in a ModuleDiffBlock.
const (
	ModuleStateNoModule     uint16 = 0
	ModuleStateWrongModule  uint16 = 1
	ModuleStateProperModule uint16 = 2
	ModuleStateSubstitute   uint16 = 3
)

// SubmoduleState values (detailed format, bit 15 set). IdentInfo sits in
// bits 11–14; ARInfo in bits 7–10.
const (
	SubmoduleStateOK          uint16 = 0x8000
	SubmoduleStateSubstitute  uint16 = 0x8000 | 1<<11
	SubmoduleStateWrong       uint16 = 0x8000 | 2<<11
	SubmoduleStateNoSubmodule uint16 = 0x8000 | 3<<11
)

// DiffModule is one module entry of a ModuleDiffBlock.
type DiffModule struct {
	API         uint32
	Slot        uint16
	ModuleIdent uint32 // the device's actual module (0 when none)
	State       uint16
	Submodules  []DiffSubmodule
}

// DiffSubmodule is one submodule entry of a ModuleDiffBlock.
type DiffSubmodule struct {
	Subslot        uint16
	SubmoduleIdent uint32
	State          uint16
}

// MarshalModuleDiff builds a ModuleDiffBlock listing the given modules,
// grouped by API in the order they first appear.
func MarshalModuleDiff(mods []DiffModule) []byte {
	var apis []uint32
	by := map[uint32][]DiffModule{}
	for _, m := range mods {
		if _, ok := by[m.API]; !ok {
			apis = append(apis, m.API)
		}
		by[m.API] = append(by[m.API], m)
	}
	p := binary.BigEndian.AppendUint16(nil, uint16(len(apis)))
	for _, api := range apis {
		p = binary.BigEndian.AppendUint32(p, api)
		p = binary.BigEndian.AppendUint16(p, uint16(len(by[api])))
		for _, m := range by[api] {
			p = binary.BigEndian.AppendUint16(p, m.Slot)
			p = binary.BigEndian.AppendUint32(p, m.ModuleIdent)
			p = binary.BigEndian.AppendUint16(p, m.State)
			p = binary.BigEndian.AppendUint16(p, uint16(len(m.Submodules)))
			for _, s := range m.Submodules {
				p = binary.BigEndian.AppendUint16(p, s.Subslot)
				p = binary.BigEndian.AppendUint32(p, s.SubmoduleIdent)
				p = binary.BigEndian.AppendUint16(p, s.State)
			}
		}
	}
	return MarshalPNIOBlock(BlockTypeModuleDiffBlock, 1, 0, p)
}

// ParseModuleDiff decodes a ModuleDiffBlock's data.
func ParseModuleDiff(data []byte) ([]DiffModule, error) {
	short := fmt.Errorf("ModuleDiffBlock truncated")
	if len(data) < 2 {
		return nil, short
	}
	var out []DiffModule
	nAPI := int(binary.BigEndian.Uint16(data))
	o := 2
	for range nAPI {
		if o+6 > len(data) {
			return nil, short
		}
		api := binary.BigEndian.Uint32(data[o:])
		nMod := int(binary.BigEndian.Uint16(data[o+4:]))
		o += 6
		for range nMod {
			if o+10 > len(data) {
				return nil, short
			}
			m := DiffModule{
				API:         api,
				Slot:        binary.BigEndian.Uint16(data[o:]),
				ModuleIdent: binary.BigEndian.Uint32(data[o+2:]),
				State:       binary.BigEndian.Uint16(data[o+6:]),
			}
			nSub := int(binary.BigEndian.Uint16(data[o+8:]))
			o += 10
			for range nSub {
				if o+8 > len(data) {
					return nil, short
				}
				m.Submodules = append(m.Submodules, DiffSubmodule{
					Subslot:        binary.BigEndian.Uint16(data[o:]),
					SubmoduleIdent: binary.BigEndian.Uint32(data[o+2:]),
					State:          binary.BigEndian.Uint16(data[o+6:]),
				})
				o += 8
			}
			out = append(out, m)
		}
	}
	return out, nil
}

// ParsePNIORequestArgs returns a request body's ArgsMaximum and its raw
// argument bytes (the conformant array's contents). A Write's arguments
// are a header block followed by record data, which is not a block and
// must not go through ParsePNIOBlocks.
func ParsePNIORequestArgs(data []byte, le bool) (argsMax uint32, args []byte, err error) {
	if len(data) < 20 {
		return 0, nil, fmt.Errorf("PNIO request body too short: %d", len(data))
	}
	var order binary.ByteOrder = binary.BigEndian
	if le {
		order = binary.LittleEndian
	}
	argsMax = order.Uint32(data[0:4])
	n := order.Uint32(data[16:20])
	args = data[20:]
	if int(n) < len(args) {
		args = args[:n]
	}
	return argsMax, args, nil
}

// Write is one record of a Write request.
type Write struct {
	Header RecordHeader
	Data   []byte
}

// IndexMultipleWrite is the index of a write that carries several records.
const IndexMultipleWrite uint16 = 0xE040

// ParseWriteArgs splits a Write request's arguments into its records:
// one IODWriteReqHeader and its data, or for IndexMultipleWrite, the
// header followed by records each padded to four bytes. The outer header
// of a multiple write comes back first, with no data.
func ParseWriteArgs(args []byte) ([]Write, error) {
	h, data, _, err := nextWrite(args)
	if err != nil {
		return nil, err
	}
	if h.Index != IndexMultipleWrite {
		return []Write{{Header: h, Data: data}}, nil
	}
	out := []Write{{Header: h}}
	for len(data) > 0 {
		ih, idata, irest, err := nextWrite(data)
		if err != nil {
			return nil, err
		}
		out = append(out, Write{Header: ih, Data: idata})
		used := len(data) - len(irest)
		if pad := (4 - used%4) % 4; pad <= len(irest) {
			irest = irest[pad:]
		}
		data = irest
	}
	return out, nil
}

func nextWrite(b []byte) (RecordHeader, []byte, []byte, error) {
	if len(b) < 6 || binary.BigEndian.Uint16(b) != BlockTypeIODWriteReqHeader {
		return RecordHeader{}, nil, nil, fmt.Errorf("write: no IODWriteReqHeader")
	}
	n := int(binary.BigEndian.Uint16(b[2:4])) + 4
	if n < 6 || n > len(b) {
		return RecordHeader{}, nil, nil, fmt.Errorf("write: header length %d", n)
	}
	h, err := ParseRecordHeader(b[6:n])
	if err != nil {
		return RecordHeader{}, nil, nil, err
	}
	b = b[n:]
	if int(h.DataLength) > len(b) {
		return RecordHeader{}, nil, nil, fmt.Errorf("write: record %#04x claims %d bytes, %d follow", h.Index, h.DataLength, len(b))
	}
	return h, b[:h.DataLength], b[h.DataLength:], nil
}

// MarshalWriteRes builds an IODWriteResHeader acknowledging h with status.
func MarshalWriteRes(h RecordHeader, status PNIOStatus) []byte {
	p := make([]byte, recordHeaderLen)
	binary.BigEndian.PutUint16(p[0:2], h.Seq)
	copy(p[2:18], h.ARUUID[:])
	binary.BigEndian.PutUint32(p[18:22], h.API)
	binary.BigEndian.PutUint16(p[22:24], h.Slot)
	binary.BigEndian.PutUint16(p[24:26], h.Subslot)
	binary.BigEndian.PutUint16(p[28:30], h.Index)
	binary.BigEndian.PutUint32(p[30:34], h.DataLength)
	copy(p[38:42], status.Marshal())
	return MarshalPNIOBlock(BlockTypeIODWriteResHeader, 1, 0, p)
}

// MarshalReadRes builds an IODReadResHeader for h with n bytes of record
// data following it.
func MarshalReadRes(h RecordHeader, n int) []byte {
	p := make([]byte, recordHeaderLen)
	binary.BigEndian.PutUint16(p[0:2], h.Seq)
	copy(p[2:18], h.ARUUID[:])
	binary.BigEndian.PutUint32(p[18:22], h.API)
	binary.BigEndian.PutUint16(p[22:24], h.Slot)
	binary.BigEndian.PutUint16(p[24:26], h.Subslot)
	binary.BigEndian.PutUint16(p[28:30], h.Index)
	binary.BigEndian.PutUint32(p[30:34], uint32(n))
	return MarshalPNIOBlock(BlockTypeIODReadResHeader, 1, 0, p)
}

// Per-service PNIO error codes (ErrorCode) and the decodes used here.
const (
	ErrorCodeConnect uint8 = 0xDB
	ErrorCodeRelease uint8 = 0xDC
	ErrorCodeControl uint8 = 0xDD
	ErrorCodeRead    uint8 = 0xDE
	ErrorCodeWrite   uint8 = 0xDF

	ErrorDecodePNIORW uint8 = 0x80
	ErrorDecodePNIO   uint8 = 0x81
)

// IOSubmodule is one submodule's IO data lengths as the controller plans
// them: InputLen is data the device provides, OutputLen data the
// controller sends.
type IOSubmodule struct {
	Slot, Subslot       uint16
	InputLen, OutputLen uint16
}

// IOCRLayout is the C-SDU plan of one IOCR.
type IOCRLayout struct {
	DataLength uint16
	API        IOCRAPIEntry // API 0
}

// LayoutIOCRs assigns FrameOffsets the way an IO controller does in
// Connect. The input C-SDU (device → controller) carries, for every
// submodule with input data or none at all, its data and IOPS (a
// submodule with no IO data is an input of length 0), then an IOCS for
// every submodule with output data. The output C-SDU carries, for every
// submodule with output data, its data and IOPS, then an IOCS for every
// submodule in the input C-SDU. Each is at least MinCSDU long.
func LayoutIOCRs(subs []IOSubmodule) (in, out IOCRLayout) {
	var io, oo uint16
	for _, s := range subs {
		if s.InputLen > 0 || s.OutputLen == 0 {
			in.API.IODataObjects = append(in.API.IODataObjects, IOCRDataObject{s.Slot, s.Subslot, io})
			io += s.InputLen + 1
		}
		if s.OutputLen > 0 {
			out.API.IODataObjects = append(out.API.IODataObjects, IOCRDataObject{s.Slot, s.Subslot, oo})
			oo += s.OutputLen + 1
		}
	}
	for _, s := range subs {
		if s.OutputLen > 0 {
			in.API.IOCSs = append(in.API.IOCSs, IOCRIOxS{s.Slot, s.Subslot, io})
			io++
		}
		if s.InputLen > 0 || s.OutputLen == 0 {
			out.API.IOCSs = append(out.API.IOCSs, IOCRIOxS{s.Slot, s.Subslot, oo})
			oo++
		}
	}
	in.DataLength, out.DataLength = max(io, MinCSDU), max(oo, MinCSDU)
	return in, out
}
