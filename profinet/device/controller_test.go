package device

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/profinet/transport"
	"github.com/joyautomation/nautilus/profinet/wire"
)

// testController is the smallest IO controller that drives the device
// the way a PLC does: DCP, Connect with FrameOffsets from
// wire.LayoutIOCRs, PrmEnd, answering CControl(ApplicationReady), and
// cyclic output frames. It is test scaffolding, not profinet/controller.
type testController struct {
	t      *testing.T
	port   transport.Port
	cli    *net.UDPConn // requests to the device
	cm     *net.UDPConn // the device's CControl comes here
	devRPC *net.UDPAddr

	mu       sync.Mutex
	arUUID   [16]byte
	session  uint16
	seq      uint32
	in, out  wire.IOCRLayout
	inFrame  uint16
	outFrame uint16
	inLens   map[Addr]uint16
	outLens  map[Addr]uint16
	lastIn   []byte
	lastDS   uint8
	appReady bool
	outputs  map[Addr][]byte
	outIOPS  map[Addr]uint8
	outDS    uint8
	sending  bool
	dcp      chan *wire.DCPFrame
	props    uint32 // ARProperties; 0 = 0x11 (legacy start-up)
	activity uint16 // CMInitiatorActivityTimeout (100 ms units); 0 = 100
	noOutput bool   // leave the output IOCR out of the Connect

	sawARServer bool
	ctx         context.Context
	cancel      context.CancelFunc
}

func newController(t *testing.T, hub *transport.Hub, mac net.HardwareAddr, devRPC *net.UDPAddr) *testController {
	t.Helper()
	cli, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loIP})
	if err != nil {
		t.Fatal(err)
	}
	cm, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loIP})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &testController{
		t: t, cli: cli, cm: cm, devRPC: devRPC,
		port: hub.Port(mac, transport.Options{EtherTypes: []uint16{wire.EtherTypePNIO}}),
		dcp:  make(chan *wire.DCPFrame, 8), ctx: ctx, cancel: cancel,
		outputs: map[Addr][]byte{}, outIOPS: map[Addr]uint8{},
	}
	go c.recvLoop()
	go c.cmLoop()
	go c.sendLoop()
	return c
}

func (c *testController) close() {
	c.cancel()
	c.port.Close()
	c.cli.Close()
	c.cm.Close()
}

func (c *testController) recvLoop() {
	for {
		f, err := c.port.Recv(c.ctx)
		if err != nil {
			return
		}
		id := binary.BigEndian.Uint16(f.Payload)
		if id >= wire.FrameIDDCPHello && id <= wire.FrameIDDCPIdentResp {
			if d, err := wire.ParseDCPFrame(f.Payload); err == nil {
				c.dcp <- d
			}
			continue
		}
		c.mu.Lock()
		if id == c.inFrame && c.inFrame != 0 {
			if fr, err := wire.ParseCyclic(f.Payload, int(c.in.DataLength)); err == nil {
				c.lastIn = append(c.lastIn[:0], fr.CSDU...)
				c.lastDS = fr.DataStatus
			}
		}
		c.mu.Unlock()
	}
}

// cmLoop answers the device's CControl(ApplicationReady) with Done.
func (c *testController) cmLoop() {
	buf := make([]byte, 4096)
	for {
		n, from, err := c.cm.ReadFromUDP(buf)
		if err != nil {
			return
		}
		h, err := wire.ParseRPCHeader(buf[:n])
		if err != nil || h.InterfaceUUID != wire.UUIDPNIOControllerInterface {
			continue
		}
		argsMax, blocks, err := wire.ParsePNIORequestBody(buf[wire.RPCHeaderSize:n], h.IsLittleEndian())
		if err != nil || len(blocks) != 1 {
			continue
		}
		ctl, err := wire.ParseIODControl(blocks[0].Data)
		if err != nil {
			continue
		}
		c.mu.Lock()
		c.appReady = ctl.ARUUID == c.arUUID
		c.mu.Unlock()
		ctl.Command = wire.ControlCmdDone
		body := wire.MarshalPNIOResponseBody(wire.PNIOStatusOK, wire.MarshalIODControl(wire.BlockTypeAppRdyBlockRes, ctl), argsMax, false)
		r := *h
		r.PacketType, r.FragmentLen = wire.RPCPTypeResponse, uint16(len(body))
		c.cm.WriteToUDP(append(r.Marshal(), body...), from)
	}
}

func (c *testController) sendLoop() {
	t := time.NewTicker(2 * time.Millisecond)
	defer t.Stop()
	var cycle uint16
	var frame []byte
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		c.mu.Lock()
		if !c.sending {
			c.mu.Unlock()
			continue
		}
		csdu := make([]byte, c.out.DataLength)
		for _, o := range c.out.API.IODataObjects {
			a := Addr{o.SlotNumber, o.SubslotNumber}
			n := int(c.outLens[a])
			copy(csdu[o.FrameOffset:int(o.FrameOffset)+n], c.outputs[a])
			iops, ok := c.outIOPS[a]
			if !ok {
				iops = wire.IOxSGood
			}
			csdu[int(o.FrameOffset)+n] = iops
		}
		for _, s := range c.out.API.IOCSs {
			csdu[s.FrameOffset] = wire.IOxSGood
		}
		cycle += 64
		frame = wire.AppendCyclic(frame[:0], c.outFrame, csdu, cycle, c.outDS)
		c.mu.Unlock()
		c.port.Send(devMAC, wire.EtherTypePNIO, frame)
	}
}

func (c *testController) startOutputs(out map[Addr][]byte, ds uint8) {
	c.setOutputs(out, ds)
	c.mu.Lock()
	c.sending = true
	c.mu.Unlock()
}

func (c *testController) setOutputs(out map[Addr][]byte, ds uint8) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for a, b := range out {
		c.outputs[a] = b
	}
	c.outDS = ds
}

func (c *testController) setOutputIOPS(a Addr, iops, ds uint8) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outIOPS[a] = iops
	c.outDS = ds
}

func (c *testController) stopOutputs() {
	c.mu.Lock()
	c.sending = false
	c.mu.Unlock()
}

func (c *testController) appReadySeen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.appReady
}

// lastInput returns subslot a's data and IOPS from the last input frame.
func (c *testController) lastInput(a Addr) ([]byte, uint8, uint8, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastIn == nil {
		return nil, 0, 0, false
	}
	for _, o := range c.in.API.IODataObjects {
		if (Addr{o.SlotNumber, o.SubslotNumber}) == a {
			n := int(c.inLens[a])
			off := int(o.FrameOffset)
			return append([]byte(nil), c.lastIn[off:off+n]...), c.lastIn[off+n], c.lastDS, true
		}
	}
	return nil, 0, 0, false
}

func (c *testController) dcpRequest(f *wire.DCPFrame, dst net.HardwareAddr) *wire.DCPFrame {
	for len(c.dcp) > 0 {
		<-c.dcp
	}
	c.port.Send(dst, wire.EtherTypePNIO, wire.MarshalDCPFrame(f))
	select {
	case r := <-c.dcp:
		return r
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}

// identify returns the name and IP a device answering for name reports.
func (c *testController) identify(name string) (string, net.IP) {
	r := c.dcpRequest(&wire.DCPFrame{
		FrameID: wire.FrameIDDCPIdentReq, ServiceID: wire.DCPServiceIdentify, ServiceType: wire.DCPServiceTypeRequest,
		Xid: 1, ResponseDelay: 1, Blocks: []wire.DCPBlock{{Option: wire.DCPOptionDeviceProperties, SubOption: wire.DCPSubOptionDevNameOfStation, Data: []byte(name)}},
	}, wire.DCPMulticastAddr)
	if r == nil {
		return "", nil
	}
	var gotName string
	var ip net.IP
	for _, b := range r.Blocks {
		switch {
		case b.Option == wire.DCPOptionDeviceProperties && b.SubOption == wire.DCPSubOptionDevNameOfStation:
			gotName = string(b.Data[2:])
		case b.Option == wire.DCPOptionIP && b.SubOption == wire.DCPSubOptionIPSuite:
			ip, _, _, _ = wire.ParseIPSuiteBlock(b.Data[2:])
		}
	}
	return gotName, ip
}

func (c *testController) dcpSet(b wire.DCPBlock) uint8 {
	r := c.dcpRequest(&wire.DCPFrame{
		FrameID: wire.FrameIDDCPGetSet, ServiceID: wire.DCPServiceSet, ServiceType: wire.DCPServiceTypeRequest,
		Xid: 2, Blocks: []wire.DCPBlock{b},
	}, devMAC)
	if r == nil || len(r.Blocks) != 1 || len(r.Blocks[0].Data) < 3 {
		c.t.Fatal("no Set response")
	}
	return r.Blocks[0].Data[2] // option, suboption, error
}

func (c *testController) setIP(ip net.IP) uint8 {
	b := wire.DCPBlockIPSuite(ip, net.IPv4(255, 0, 0, 0), net.IPv4zero, 0x0001) // qualifier: permanent
	return c.dcpSet(b)
}

func (c *testController) setName(name string) uint8 {
	return c.dcpSet(wire.DCPBlock{Option: wire.DCPOptionDeviceProperties, SubOption: wire.DCPSubOptionDevNameOfStation, Data: append([]byte{0, 1}, name...)})
}

// call sends one PNIO request and returns the response status and blocks.
func (c *testController) call(t *testing.T, op uint16, args []byte, twice bool) (wire.PNIOStatus, []wire.PNIOBlock) {
	t.Helper()
	body := wire.MarshalPNIORequestBody(args, 4096, false)
	c.mu.Lock()
	c.seq++
	h := &wire.RPCHeader{
		RPCVersion: wire.RPCVersionCL, PacketType: wire.RPCPTypeRequest, Flags1: wire.RPCFlagLastFrag | wire.RPCFlagIdempotent,
		ObjectUUID: wire.UUIDPNIOObjectBase, InterfaceUUID: wire.UUIDPNIOInterface, ActivityUUID: randUUID(),
		InterfaceVersion: 1, SequenceNum: c.seq, OpNum: op, InterfaceHint: 0xFFFF, ActivityHint: 0xFFFF,
		FragmentLen: uint16(len(body)),
	}
	c.mu.Unlock()
	pkt := append(h.Marshal(), body...)
	c.cli.WriteToUDP(pkt, c.devRPC)
	if twice {
		c.cli.WriteToUDP(pkt, c.devRPC)
	}
	buf := make([]byte, 65536)
	c.cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := c.cli.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("op %d: %v", op, err)
	}
	if twice { // the retransmission's answer
		c.cli.SetReadDeadline(time.Now().Add(2 * time.Second))
		m := make([]byte, 65536)
		k, _, err := c.cli.ReadFromUDP(m)
		if err != nil || string(m[:k]) != string(buf[:n]) {
			t.Fatalf("retransmission answered differently: %v", err)
		}
	}
	rh, err := wire.ParseRPCHeader(buf[:n])
	if err != nil || rh.ActivityUUID != h.ActivityUUID {
		t.Fatalf("response header %v", err)
	}
	st, blocks, err := wire.ParsePNIOResponseBody(buf[wire.RPCHeaderSize:n], rh.IsLittleEndian())
	if err != nil {
		t.Fatal(err)
	}
	return st, blocks
}

func randUUID() [16]byte {
	var u [16]byte
	rand.Read(u[:])
	return u
}

func (c *testController) connectArgs(subs []Subslot) []byte {
	io := expectedFor(subs)
	in, out := wire.LayoutIOCRs(io)
	c.mu.Lock()
	c.arUUID, c.session = randUUID(), c.session+1
	c.in, c.out, c.inFrame, c.lastIn, c.appReady = in, out, 0xC001, nil, false
	c.inLens, c.outLens = map[Addr]uint16{}, map[Addr]uint16{}
	for _, s := range io {
		c.inLens[Addr{s.Slot, s.Subslot}] = s.InputLen
		c.outLens[Addr{s.Slot, s.Subslot}] = s.OutputLen
	}
	args := wire.MarshalARBlockReq(&wire.ARBlockReq{
		ARType: 1, ARUUID: c.arUUID, SessionKey: c.session, CMInitiatorMAC: c.port.MAC(),
		CMInitiatorObjUUID: randUUID(), ARProperties: c.arProps(), CMInitiatorActivityTimeout: c.activityTimeout(),
		CMInitiatorUDPRTPort: wire.ProfinetRTUDPPort, CMInitiatorStationName: "ctrl",
	})
	c.mu.Unlock()
	iocr := func(typ, ref, frame uint16, l wire.IOCRLayout) []byte {
		return wire.MarshalIOCRBlockReq(&wire.IOCRBlockReq{
			IOCRType: typ, IOCRReference: ref, LT: wire.EtherTypePNIO, IOCRProperties: wire.IOCRPropertyRTClass1,
			DataLength: l.DataLength, FrameID: frame, SendClockFactor: 32, ReductionRatio: 2,
			WatchdogFactor: 3, DataHoldFactor: 3, IOCRMulticastMAC: make(net.HardwareAddr, 6), APIs: []wire.IOCRAPIEntry{l.API},
		})
	}
	args = append(args, iocr(wire.IOCRTypeInput, 1, 0xC001, in)...)
	if !c.noOutput {
		args = append(args, iocr(wire.IOCRTypeOutput, 2, 0xFFFF, out)...)
	}
	args = append(args, wire.MarshalAlarmCRBlockReq(&wire.AlarmCRBlockReq{AlarmCRType: 1, LT: wire.EtherTypePNIO, RTATimeoutFactor: 1, RTARetries: 3, LocalAlarmRef: 7, MaxAlarmDataLen: 200})...)
	exp := &wire.ExpectedSubmoduleReq{}
	mods := map[uint16]*wire.ExpectedModule{}
	var order []uint16
	ident := func(a Addr) (uint32, uint32) {
		if a.Slot == 0 {
			for _, s := range dapSubslots() {
				if s.Addr == a {
					return s.ModuleIdent, s.SubmoduleIdent
				}
			}
		}
		for _, s := range subs {
			if s.Addr == a {
				return s.ModuleIdent, s.SubmoduleIdent
			}
		}
		return 0, 0
	}
	for _, s := range io {
		mi, si := ident(Addr{s.Slot, s.Subslot})
		m, ok := mods[s.Slot]
		if !ok {
			m = &wire.ExpectedModule{SlotNumber: s.Slot, ModuleIdentNumber: mi}
			mods[s.Slot] = m
			order = append(order, s.Slot)
		}
		sm := wire.ExpectedSubmodule{SubslotNumber: s.Subslot, SubmoduleIdentNumber: si}
		switch {
		case s.InputLen > 0 && s.OutputLen > 0:
			sm.SubmoduleProperties = wire.SubmoduleTypeInputOutput
			sm.DataDescription = []wire.SubmoduleDataDesc{dd(wire.DataDirectionInput, s.InputLen), dd(wire.DataDirectionOutput, s.OutputLen)}
		case s.OutputLen > 0:
			sm.SubmoduleProperties = wire.SubmoduleTypeOutput
			sm.DataDescription = []wire.SubmoduleDataDesc{dd(wire.DataDirectionOutput, s.OutputLen)}
		default:
			sm.SubmoduleProperties = wire.SubmoduleTypeInput
			if s.InputLen == 0 {
				sm.SubmoduleProperties = wire.SubmoduleTypeNoIO
			}
			sm.DataDescription = []wire.SubmoduleDataDesc{dd(wire.DataDirectionInput, s.InputLen)}
		}
		m.Submodules = append(m.Submodules, sm)
	}
	api := wire.ExpectedAPI{API: 0}
	for _, slot := range order {
		api.Modules = append(api.Modules, *mods[slot])
	}
	exp.APIs = []wire.ExpectedAPI{api}
	return append(args, wire.MarshalExpectedSubmoduleReq(exp)...)
}

// connect opens an AR for subs and returns the device's ModuleDiff.
func (c *testController) connect(t *testing.T, subs []Subslot, twice bool) []wire.DiffModule {
	t.Helper()
	st, blocks := c.call(t, wire.RPCOpConnect, c.connectArgs(subs), twice)
	if !st.IsOK() {
		t.Fatalf("connect: %+v", st)
	}
	var diff []wire.DiffModule
	for _, b := range blocks {
		switch b.Type {
		case wire.BlockTypeIOCRBlockRes:
			if binary.BigEndian.Uint16(b.Data[0:2]) == wire.IOCRTypeOutput {
				c.mu.Lock()
				c.outFrame = binary.BigEndian.Uint16(b.Data[4:6])
				c.mu.Unlock()
			}
		case wire.BlockTypeARServerBlockRes:
			c.sawARServer = true
		case wire.BlockTypeModuleDiffBlock:
			d, err := wire.ParseModuleDiff(b.Data)
			if err != nil {
				t.Fatal(err)
			}
			diff = d
		}
	}
	if c.outFrame < 0xC000 || c.outFrame > 0xF7FF {
		t.Fatalf("output FrameID %#04x is not RT_CLASS_1", c.outFrame)
	}
	return diff
}

func (c *testController) connectStatus(t *testing.T, subs []Subslot) wire.PNIOStatus {
	st, _ := c.call(t, wire.RPCOpConnect, c.connectArgs(subs), false)
	return st
}

func (c *testController) control(t *testing.T, cmd uint16) wire.PNIOStatus {
	c.mu.Lock()
	ctl := wire.IODControl{ARUUID: c.arUUID, SessionKey: c.session, Command: cmd}
	c.mu.Unlock()
	st, _ := c.call(t, wire.RPCOpControl, wire.MarshalIODControl(wire.BlockTypeIODControlReq, ctl), false)
	return st
}

func (c *testController) prmEnd(t *testing.T) {
	if st := c.control(t, wire.ControlCmdPrmEnd); !st.IsOK() {
		t.Fatalf("PrmEnd %+v", st)
	}
}

func (c *testController) release(t *testing.T) {
	c.mu.Lock()
	ctl := wire.IODControl{ARUUID: c.arUUID, SessionKey: c.session, Command: wire.ControlCmdRelease}
	c.mu.Unlock()
	st, blocks := c.call(t, wire.RPCOpRelease, wire.MarshalIODControl(wire.BlockTypeIODReleaseReq, ctl), false)
	if !st.IsOK() {
		t.Fatalf("release %+v", st)
	}
	if len(blocks) != 1 || blocks[0].Type != wire.BlockTypeIODReleaseRes {
		t.Fatalf("release reply blocks %+v, want one IODReleaseBlockRes (0x8114)", blocks)
	}
}

func (c *testController) readStatus(t *testing.T, slot, subslot, index uint16) wire.PNIOStatus {
	st, _ := c.call(t, wire.RPCOpRead, wire.MarshalRecordHeader(wire.BlockTypeIODReadReqHeader, wire.RecordHeader{ARUUID: c.arUUID, Slot: slot, Subslot: subslot, Index: index, DataLength: 4096}), false)
	return st
}

// read returns the record data after the read response header.
func (c *testController) read(t *testing.T, slot, subslot, index uint16) []byte {
	t.Helper()
	body := wire.MarshalPNIORequestBody(wire.MarshalRecordHeader(wire.BlockTypeIODReadReqHeader, wire.RecordHeader{ARUUID: c.arUUID, Slot: slot, Subslot: subslot, Index: index, DataLength: 4096}), 4096, false)
	h := &wire.RPCHeader{
		RPCVersion: wire.RPCVersionCL, PacketType: wire.RPCPTypeRequest, Flags1: wire.RPCFlagLastFrag,
		InterfaceUUID: wire.UUIDPNIOInterface, ActivityUUID: randUUID(), InterfaceVersion: 1,
		OpNum: wire.RPCOpRead, FragmentLen: uint16(len(body)),
	}
	c.cli.WriteToUDP(append(h.Marshal(), body...), c.devRPC)
	buf := make([]byte, 65536)
	c.cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := c.cli.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	b := buf[wire.RPCHeaderSize:n]
	if st := (wire.PNIOStatus{ErrorCode: b[0], ErrorDecode: b[1], ErrorCode1: b[2], ErrorCode2: b[3]}); !st.IsOK() {
		t.Fatalf("read %#04x: %+v", index, st)
	}
	args := b[20:]
	hdrLen := 4 + int(binary.BigEndian.Uint16(args[2:4]))
	return args[hdrLen:]
}

func (c *testController) write(t *testing.T, ws []wire.Write) wire.PNIOStatus {
	var args []byte
	for _, w := range ws {
		w.Header.ARUUID, w.Header.DataLength = c.arUUID, uint32(len(w.Data))
		args = append(args, wire.MarshalRecordHeader(wire.BlockTypeIODWriteReqHeader, w.Header)...)
		args = append(args, w.Data...)
	}
	st, _ := c.call(t, wire.RPCOpWrite, args, false)
	return st
}

func dd(dir, n uint16) wire.SubmoduleDataDesc {
	return wire.SubmoduleDataDesc{DataDirection: dir, DataLength: n, LengthIOCS: 1, LengthIOPS: 1}
}

func (c *testController) activityTimeout() uint16 {
	if c.activity == 0 {
		return 100
	}
	return c.activity
}

func (c *testController) arProps() uint32 {
	if c.props == 0 {
		return 0x11
	}
	return c.props
}
