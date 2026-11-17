package device

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/profinet/wire"
)

// startupModeBit is ARProperties bit 30, Advanced Startup: the device
// adds an ARServerBlockRes to Connect.rsp and starts cyclic data at once
// rather than after PrmEnd.
const startupModeBit uint32 = 1 << 30

// ar is the one Application Relationship.
type ar struct {
	uuid        [16]byte
	arType      uint16
	sessionKey  uint16
	props       uint32
	ctrlMAC     net.HardwareAddr
	ctrlAddr    *net.UDPAddr
	ctrlObjUUID [16]byte
	ctrlStation string
	ctrlRTPort  uint16

	in, out *iocr // in: this device → controller; out: controller → this device
	alarm   *alarmCR

	// expected is the controller's view of each subslot's data lengths
	// (from ExpectedSubmoduleReq); wrong are the subslots whose ident or
	// lengths differ from the layout (reported in the ModuleDiff, IOPS Bad
	// both ways).
	expected map[Addr]expect
	wrong    map[Addr]bool

	appReady atomic.Bool // the controller acknowledged our CControl(AppReady)
	cancel   context.CancelFunc
	cyclic   bool
	lastOut  time.Time // last output frame (zero: none yet in this AR)
	dataAt   time.Time // when the AR entered DATA
	watchdog time.Duration
}

type iocr struct {
	typ, ref, frameID, dataLen uint16
	props                      uint32
	scf, rr, wdf, dhf          uint16
	objs                       []wire.IOCRDataObject
	iocs                       []wire.IOCRIOxS
}

func (c *iocr) period() time.Duration {
	return time.Duration(int64(c.scf)*int64(c.rr)*31250) * time.Nanosecond
}

type alarmCR struct {
	typ, remoteRef, localRef, maxLen uint16
	ackSeq                           uint16
}

type expect struct{ inLen, outLen uint16 }

// firstOutputGrace is how long after entering DATA the device waits for
// the controller's first output frame before it gives the AR up.
const firstOutputGrace = 10 * time.Second

func newIOCR(r *wire.IOCRBlockReq, frameID uint16) *iocr {
	c := &iocr{
		typ: r.IOCRType, ref: r.IOCRReference, frameID: frameID, dataLen: r.DataLength,
		props: r.IOCRProperties, scf: r.SendClockFactor, rr: r.ReductionRatio,
		wdf: r.WatchdogFactor, dhf: r.DataHoldFactor,
	}
	for _, a := range r.APIs {
		c.objs = append(c.objs, a.IODataObjects...)
		c.iocs = append(c.iocs, a.IOCSs...)
	}
	return c
}

// connect handles a Connect request (d.mu held).
func (d *Device) connect(blocks []wire.PNIOBlock, from *net.UDPAddr) ([]byte, wire.PNIOStatus) {
	refuse := func(code1, code2 uint8) ([]byte, wire.PNIOStatus) {
		return nil, wire.PNIOStatus{ErrorCode: wire.ErrorCodeConnect, ErrorDecode: wire.ErrorDecodePNIO, ErrorCode1: code1, ErrorCode2: code2}
	}
	var (
		arb   *wire.ARBlockReq
		iocrs []*wire.IOCRBlockReq
		alarm *wire.AlarmCRBlockReq
		exps  []*wire.ExpectedSubmoduleReq
	)
	for _, b := range blocks {
		var err error
		switch b.Type {
		case wire.BlockTypeARBlockReq:
			arb, err = wire.ParseARBlockReq(b.Data)
			if err != nil {
				return refuse(0x01, 0x00) // faulty ARBlockReq
			}
		case wire.BlockTypeIOCRBlockReq:
			r, err := wire.ParseIOCRBlockReq(b.Data)
			if err != nil {
				return refuse(0x02, 0x00)
			}
			iocrs = append(iocrs, r)
		case wire.BlockTypeAlarmCRBlockReq:
			alarm, err = wire.ParseAlarmCRBlockReq(b.Data)
			if err != nil {
				return refuse(0x04, 0x00)
			}
		case wire.BlockTypeExpectedSubmoduleReq:
			e, err := wire.ParseExpectedSubmoduleReq(b.Data)
			if err != nil {
				return refuse(0x03, 0x00)
			}
			exps = append(exps, e)
		}
	}
	if arb == nil {
		return refuse(0x01, 0x00)
	}

	// One AR. The same controller connecting again has lost the old one
	// (it restarted, or its watchdog fired before ours): replace it.
	// Another controller is refused until this AR ends.
	if old := d.ar; old != nil {
		if !macEqual(old.ctrlMAC, arb.CMInitiatorMAC) {
			d.counters.refusedARs++
			d.snapInfo.lastEvent = fmt.Sprintf("refused an AR from %s (%s): one is running with %s", arb.CMInitiatorStationName, arb.CMInitiatorMAC, old.ctrlStation)
			d.log.Warn("profinet device: " + d.snapInfo.lastEvent)
			return refuse(0x40, 0x04) // CMRPC: out of AR resources
		}
		d.endAR(old, "the controller opened a new AR")
	}

	a := &ar{
		uuid: arb.ARUUID, arType: arb.ARType, sessionKey: arb.SessionKey, props: arb.ARProperties,
		ctrlMAC: arb.CMInitiatorMAC, ctrlAddr: from, ctrlObjUUID: arb.CMInitiatorObjUUID,
		ctrlStation: arb.CMInitiatorStationName, ctrlRTPort: arb.CMInitiatorUDPRTPort,
		expected: map[Addr]expect{}, wrong: map[Addr]bool{},
	}
	var diff []wire.DiffModule
	for _, e := range exps {
		diff = append(diff, d.compare(e, a)...)
	}

	resp := wire.MarshalARBlockRes(arb.ARType, arb.ARUUID, arb.SessionKey, d.cfg.Port.MAC(), wire.ProfinetRTUDPPort)
	used := map[uint16]bool{}
	for _, r := range iocrs {
		id := r.FrameID
		if id == 0xFFFF {
			id = allocFrameID(r.IOCRProperties&0x0F, used)
		}
		used[id] = true
		switch r.IOCRType {
		case wire.IOCRTypeInput:
			a.in = newIOCR(r, id)
		case wire.IOCRTypeOutput:
			a.out = newIOCR(r, id)
		default:
			return refuse(0x02, 0x04) // multicast IOCRs are not supported
		}
		resp = append(resp, wire.MarshalIOCRBlockRes(r.IOCRType, r.IOCRReference, id)...)
	}
	if a.in == nil {
		return refuse(0x02, 0x00)
	}
	if alarm != nil {
		a.alarm = &alarmCR{typ: alarm.AlarmCRType, remoteRef: alarm.LocalAlarmRef, localRef: 1, maxLen: alarm.MaxAlarmDataLen, ackSeq: 0xFFFF}
		resp = append(resp, wire.MarshalAlarmCRBlockRes(alarm.AlarmCRType, a.alarm.localRef, alarm.MaxAlarmDataLen)...)
	}
	// A ModuleDiffBlock only when something differs: an empty one reads as
	// "this device has no modules" to some engineering tools.
	if len(diff) > 0 {
		resp = append(resp, wire.MarshalModuleDiff(diff)...)
	}
	if arb.ARProperties&startupModeBit != 0 {
		resp = append(resp, wire.MarshalARServerBlockRes(d.station)...)
	}

	a.watchdog = d.watchdogFor(a)
	d.ar = a
	d.counters.ars++
	d.snapInfo.controller, d.snapInfo.ctrlMAC = a.ctrlStation, a.ctrlMAC
	d.snapInfo.fresh, d.snapInfo.dataStatus = false, 0
	d.snapInfo.diff = diff
	event := fmt.Sprintf("AR from %s (%s)", a.ctrlStation, a.ctrlMAC)
	if len(diff) > 0 {
		event += fmt.Sprintf("; %d module(s) differ from the layout", len(diff))
	}
	d.setState(StateStartup, event)
	if arb.ARProperties&startupModeBit != 0 {
		d.startCyclic(a)
	}
	return resp, wire.PNIOStatusOK
}

// compare checks one ExpectedSubmoduleReq against the layout, records the
// controller's lengths and the wrong subslots in a, and returns the
// ModuleDiff entries for what differs.
func (d *Device) compare(e *wire.ExpectedSubmoduleReq, a *ar) []wire.DiffModule {
	var diff []wire.DiffModule
	for _, api := range e.APIs {
		for _, m := range api.Modules {
			var have *Subslot
			for _, s := range d.subs {
				if s.Slot == m.SlotNumber {
					have = s
					break
				}
			}
			dm := wire.DiffModule{API: api.API, Slot: m.SlotNumber, State: wire.ModuleStateProperModule}
			switch {
			case api.API != 0 || have == nil:
				dm.State = wire.ModuleStateNoModule
			case have.ModuleIdent != m.ModuleIdentNumber:
				dm.State, dm.ModuleIdent = wire.ModuleStateWrongModule, have.ModuleIdent
			default:
				dm.ModuleIdent = have.ModuleIdent
			}
			for _, sm := range m.Submodules {
				addr := Addr{m.SlotNumber, sm.SubslotNumber}
				var x expect
				for _, dd := range sm.DataDescription {
					switch dd.DataDirection {
					case wire.DataDirectionInput:
						x.inLen = dd.DataLength
					case wire.DataDirectionOutput:
						x.outLen = dd.DataLength
					}
				}
				a.expected[addr] = x
				s, ok := d.subs[addr]
				state := wire.SubmoduleStateOK
				var ident uint32
				switch {
				case dm.State == wire.ModuleStateNoModule || !ok:
					state = wire.SubmoduleStateNoSubmodule
				case s.SubmoduleIdent != sm.SubmoduleIdentNumber || s.InputLen != x.inLen || s.OutputLen != x.outLen:
					state, ident = wire.SubmoduleStateWrong, s.SubmoduleIdent
				default:
					ident = s.SubmoduleIdent
				}
				if dm.State == wire.ModuleStateWrongModule && state == wire.SubmoduleStateOK {
					state = wire.SubmoduleStateWrong
				}
				if state != wire.SubmoduleStateOK {
					a.wrong[addr] = true
					if dm.State != wire.ModuleStateNoModule {
						dm.Submodules = append(dm.Submodules, wire.DiffSubmodule{Subslot: sm.SubslotNumber, SubmoduleIdent: ident, State: state})
					}
				}
			}
			if dm.State != wire.ModuleStateProperModule || len(dm.Submodules) > 0 {
				diff = append(diff, dm)
			}
		}
	}
	return diff
}

// watchdogFor is the output watchdog: the controller's cycle times the
// larger of its watchdog and data-hold factors, but not below
// MinWatchdog.
func (d *Device) watchdogFor(a *ar) time.Duration {
	if a.out == nil {
		return 0
	}
	f := max(a.out.wdf, a.out.dhf, 3)
	return max(a.out.period()*time.Duration(f), d.cfg.MinWatchdog)
}

// allocFrameID picks a FrameID for an IOCR the controller left to the
// device (0xFFFF), in the range of its RT class.
func allocFrameID(rtClass uint32, used map[uint16]bool) uint16 {
	lo, hi := uint16(0xC000), uint16(0xF7FF) // RT_CLASS_1 and UDP
	switch rtClass {
	case wire.IOCRPropertyRTClass2:
		lo, hi = 0x8000, 0xBBFF
	case wire.IOCRPropertyRTClass3:
		lo, hi = 0x0100, 0x7FFF
	}
	for id := lo; id <= hi; id++ {
		if !used[id] {
			return id
		}
	}
	return 0xFFFF
}

// release ends the AR the request names (d.mu held).
func (d *Device) release(blocks []wire.PNIOBlock) ([]byte, wire.PNIOStatus) {
	for _, b := range blocks {
		if b.Type != wire.BlockTypeIODControlReq && b.Type != wire.BlockTypeARRPCBlockReq {
			continue
		}
		c, err := wire.ParseIODControl(b.Data)
		if err != nil || d.ar == nil || c.ARUUID != d.ar.uuid {
			break
		}
		d.endAR(d.ar, "released by the controller")
		return wire.MarshalIODControl(wire.BlockTypeIODControlRes, wire.IODControl{ARUUID: c.ARUUID, SessionKey: c.SessionKey, Command: wire.ControlCmdDone}), wire.PNIOStatusOK
	}
	return nil, wire.PNIOStatus{ErrorCode: wire.ErrorCodeRelease, ErrorDecode: wire.ErrorDecodePNIO, ErrorCode1: 0x40, ErrorCode2: 0x05} // unknown AR
}

// control handles PrmEnd, ApplicationReady and Release (d.mu held). It
// returns a func to run after the response is sent, or nil.
func (d *Device) control(blocks []wire.PNIOBlock) ([]byte, wire.PNIOStatus, func()) {
	var resp []byte
	var after func()
	for _, b := range blocks {
		if b.Type != wire.BlockTypeIODControlReq && b.Type != wire.BlockTypeARRPCBlockReq {
			continue
		}
		c, err := wire.ParseIODControl(b.Data)
		if err != nil || d.ar == nil || c.ARUUID != d.ar.uuid {
			return nil, wire.PNIOStatus{ErrorCode: wire.ErrorCodeControl, ErrorDecode: wire.ErrorDecodePNIO, ErrorCode1: 0x40, ErrorCode2: 0x05}, nil
		}
		a := d.ar
		done := wire.MarshalIODControl(wire.BlockTypeIODControlRes, wire.IODControl{ARUUID: c.ARUUID, SessionKey: c.SessionKey, Command: wire.ControlCmdDone})
		switch {
		case c.Command&wire.ControlCmdPrmEnd != 0:
			resp = append(resp, done...)
			after = func() { d.applicationReady(a) }
		case c.Command&wire.ControlCmdApplicationReady != 0:
			resp = append(resp, done...)
		case c.Command&wire.ControlCmdRelease != 0:
			d.endAR(a, "released by the controller")
			resp = append(resp, done...)
		default:
			resp = append(resp, done...)
		}
	}
	return resp, wire.PNIOStatusOK, after
}

// applicationReady moves the AR to DATA after PrmEnd and tells the
// controller with CControl(ApplicationReady). The Run bit stays off in
// our frames until the controller acknowledges it.
func (d *Device) applicationReady(a *ar) {
	d.mu.Lock()
	if d.ar != a {
		d.mu.Unlock()
		return
	}
	a.dataAt = time.Now()
	d.startCyclic(a)
	d.setState(StateData, "cyclic data with "+a.ctrlStation)
	d.mu.Unlock()
	if err := d.sendAppReady(a); err != nil {
		d.log.Warn("profinet device: CControl(ApplicationReady)", "error", err)
	}
}

// endAR stops the AR's cyclic exchange and forgets it (d.mu held). The
// last outputs stay in the image: the driver marks them Stale.
func (d *Device) endAR(a *ar, why string) {
	if a.cancel != nil {
		a.cancel()
	}
	if d.ar == a {
		d.ar = nil
	}
	d.snapInfo.fresh = false
	d.setState(StateIdle, "AR ended: "+why)
}

func macEqual(a, b net.HardwareAddr) bool { return string(a) == string(b) }
