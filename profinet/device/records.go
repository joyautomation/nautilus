package device

import (
	"encoding/binary"
	"sort"

	"github.com/joyautomation/nautilus/profinet/wire"
)

// Record indices the device answers in a Read.
const (
	indexIM0             uint16 = 0xAFF0
	indexRealIdentAPI    uint16 = 0xF000
	indexARData          uint16 = 0xF820
	indexAPIData         uint16 = 0xF821
	indexIM0Filter       uint16 = 0xF840
	indexPDRealData      uint16 = 0xF841
	indexPDExpData       uint16 = 0xF842
	firstSystemIndex     uint16 = 0x8000
	readNotSupportedCode uint8  = 0xA9 // PNIORW application: feature not supported
)

// read answers one Read request (d.mu held). Unknown system indices
// (≥ 0x8000) answer empty: an error there makes a controller abort the AR
// right after start-up (it reads diagnosis and PD records it can do
// without). Unknown user indices are an error.
func (d *Device) read(blocks []wire.PNIOBlock) ([]byte, wire.PNIOStatus) {
	var resp []byte
	for _, b := range blocks {
		if b.Type != wire.BlockTypeIODReadReqHeader {
			continue
		}
		h, err := wire.ParseRecordHeader(b.Data)
		if err != nil {
			continue
		}
		var data []byte
		switch {
		case h.Index == indexIM0:
			data = d.im0()
		case h.Index == indexRealIdentAPI:
			data = d.realIdentification()
		case h.Index == indexIM0Filter:
			data = d.im0Filter()
		case h.Index == indexAPIData:
			data = d.apiData()
		case h.Index == indexARData:
			data = d.arData()
		case h.Index >= firstSystemIndex: // incl. PDRealData, PDExpData
		default:
			resp = append(resp, wire.MarshalReadRes(h, 0)...)
			return resp, wire.PNIOStatus{ErrorCode: wire.ErrorCodeRead, ErrorDecode: wire.ErrorDecodePNIORW, ErrorCode1: readNotSupportedCode}
		}
		resp = append(resp, wire.MarshalReadRes(h, len(data))...)
		resp = append(resp, data...)
	}
	return resp, wire.PNIOStatusOK
}

func (d *Device) im0() []byte {
	im := &wire.IM0Data{
		VendorID:         d.cfg.VendorID,
		HWRevision:       d.cfg.HWRevision,
		SWRevisionPrefix: 'V',
		SWRevision:       d.cfg.SWRevision,
		ProfileSpecType:  0x0003, // generic device
		IMVersion:        0x0101,
		IMSupported:      0x001E,
	}
	// OrderID and serial are space-padded visible strings.
	pad(im.OrderID[:], d.cfg.OrderNumber)
	pad(im.IMSerialNumber[:], d.cfg.Serial)
	return wire.MarshalIM0Block(im)
}

func pad(dst []byte, s string) {
	n := copy(dst, s)
	for i := n; i < len(dst); i++ {
		dst[i] = ' '
	}
}

// slots returns the layout grouped by slot, in slot and subslot order,
// the DAP first.
func (d *Device) slots() [][]*Subslot {
	by := map[uint16][]*Subslot{}
	for _, s := range d.subs {
		by[s.Slot] = append(by[s.Slot], s)
	}
	keys := make([]int, 0, len(by))
	for k := range by {
		keys = append(keys, int(k))
	}
	sort.Ints(keys)
	out := make([][]*Subslot, 0, len(keys))
	for _, k := range keys {
		ss := by[uint16(k)]
		sort.Slice(ss, func(i, j int) bool { return ss[i].Subslot < ss[j].Subslot })
		out = append(out, ss)
	}
	return out
}

// realIdentification is RealIdentificationData for API 0: every plugged
// module and submodule.
func (d *Device) realIdentification() []byte {
	slots := d.slots()
	p := binary.BigEndian.AppendUint16(nil, 1) // one API
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint16(p, uint16(len(slots)))
	for _, ss := range slots {
		p = binary.BigEndian.AppendUint16(p, ss[0].Slot)
		p = binary.BigEndian.AppendUint32(p, ss[0].ModuleIdent)
		p = binary.BigEndian.AppendUint16(p, uint16(len(ss)))
		for _, s := range ss {
			p = binary.BigEndian.AppendUint16(p, s.Subslot)
			p = binary.BigEndian.AppendUint32(p, s.SubmoduleIdent)
		}
	}
	return wire.MarshalPNIOBlock(wire.BlockTypeRealIdentData, 1, 0, p)
}

// apiData is APIData: the slots and subslots of API 0.
func (d *Device) apiData() []byte {
	slots := d.slots()
	p := binary.BigEndian.AppendUint16(nil, 1)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint16(p, uint16(len(slots)))
	for _, ss := range slots {
		p = binary.BigEndian.AppendUint16(p, ss[0].Slot)
		p = binary.BigEndian.AppendUint16(p, uint16(len(ss)))
		for _, s := range ss {
			p = binary.BigEndian.AppendUint16(p, s.Subslot)
		}
	}
	return wire.MarshalPNIOBlock(wire.BlockTypeAPIData, 1, 0, p)
}

// im0Filter says that I&M0 is carried by the DAP (slot 0, subslot 1) at
// submodule, module and device level.
func (d *Device) im0Filter() []byte {
	p := []byte{0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 1, 0, 1} // 1 API (0), 1 slot (0), 1 subslot (1)
	var b []byte
	for _, t := range []uint16{wire.BlockTypeIM0FilterDataSubmodule, wire.BlockTypeIM0FilterDataModule, wire.BlockTypeIM0FilterDataDevice} {
		b = append(b, wire.MarshalPNIOBlock(t, 1, 0, p)...)
	}
	return b
}

// arData is ARData (0xF820): the running AR as the controller negotiated
// it. A controller reads it after ApplicationReady and aborts an AR whose
// record does not match.
func (d *Device) arData() []byte {
	a := d.ar
	if a == nil {
		return wire.MarshalPNIOBlock(wire.BlockTypeARData, 1, 1, nil)
	}
	p := binary.BigEndian.AppendUint16(nil, 1)
	p = append(p, a.uuid[:]...)
	p = append(p, a.ctrlObjUUID[:]...)
	p = append(p, make([]byte, 16)...) // parameter server object UUID
	p = binary.BigEndian.AppendUint32(p, a.props)
	p = binary.BigEndian.AppendUint16(p, a.arType)
	var alarmType, remote, local uint16
	if a.alarm != nil {
		alarmType, remote, local = a.alarm.typ, a.alarm.remoteRef, a.alarm.localRef
	}
	p = binary.BigEndian.AppendUint16(p, alarmType)
	p = binary.BigEndian.AppendUint16(p, remote)
	p = binary.BigEndian.AppendUint16(p, local)
	p = binary.BigEndian.AppendUint16(p, a.ctrlRTPort)
	p = binary.BigEndian.AppendUint16(p, wire.ProfinetRTUDPPort)
	p = binary.BigEndian.AppendUint16(p, uint16(len(a.ctrlStation)))
	p = append(p, a.ctrlStation...)
	p = align4(p)
	p = binary.BigEndian.AppendUint16(p, 0) // parameter server station name length
	p = align4(p)
	var crs []*iocr
	for _, c := range []*iocr{a.in, a.out} {
		if c != nil {
			crs = append(crs, c)
		}
	}
	p = binary.BigEndian.AppendUint16(p, uint16(len(crs)))
	p = align4(p)
	for _, c := range crs {
		p = binary.BigEndian.AppendUint32(p, c.props)
		p = binary.BigEndian.AppendUint16(p, c.typ)
		p = binary.BigEndian.AppendUint16(p, c.frameID)
		p = append(p, 0, 0, 0, 0) // APDU_Status
	}
	p = binary.BigEndian.AppendUint16(p, 1) // APIs: just 0
	p = align4(p)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint16(p, 0) // ARDataInfo count
	p = align4(p)
	return wire.MarshalPNIOBlock(wire.BlockTypeARData, 1, 1, p)
}

// align4 pads to a multiple of four counted from the start of the block's
// payload, as the source did (its ARData passed a TIA Portal start-up).
// Whether the standard counts from the payload or the frame is to be
// confirmed against the bench capture (docs/design/profinet.md §5).
func align4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// write accepts parameter records (d.mu held). The layout's subslots are
// generic IO with no parameters, so the records are acknowledged and
// counted, not applied.
func (d *Device) write(args []byte) ([]byte, wire.PNIOStatus) {
	ws, err := wire.ParseWriteArgs(args)
	if err != nil {
		return nil, wire.PNIOStatus{ErrorCode: wire.ErrorCodeWrite, ErrorDecode: wire.ErrorDecodePNIORW, ErrorCode1: 0xB0} // invalid index/header
	}
	var resp []byte
	for i, w := range ws {
		r := wire.MarshalWriteRes(w.Header, wire.PNIOStatusOK)
		if i > 0 && ws[0].Header.Index == wire.IndexMultipleWrite {
			r = align4(r)
		}
		resp = append(resp, r...)
	}
	return resp, wire.PNIOStatusOK
}
