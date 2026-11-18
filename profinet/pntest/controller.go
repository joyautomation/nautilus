// Package pntest is a minimal PROFINET IO controller for tests. It drives
// a profinet/device the way a PLC does (DCP, Connect with FrameOffsets
// from wire.LayoutIOCRs, PrmEnd, CControl(ApplicationReady), cyclic output
// frames) over a transport.Hub and loopback UDP. It is test scaffolding,
// not profinet/controller.
package pntest

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/profinet/device"
	"github.com/joyautomation/nautilus/profinet/transport"
	"github.com/joyautomation/nautilus/profinet/wire"
)

// Loopback is where the test controller and device talk DCE/RPC.
var Loopback = net.IPv4(127, 0, 0, 1)

// Controller is the test controller.
type Controller struct {
	port   transport.Port
	devMAC net.HardwareAddr
	devRPC *net.UDPAddr
	cli    *net.UDPConn // requests to the device
	cm     *net.UDPConn // the device's CControl comes here

	// Props is the ARProperties of the next Connect (0: 0x11, legacy
	// start-up; set bit 30 for Advanced Startup).
	Props uint32
	// Activity is the CMInitiatorActivityTimeout of the next Connect in
	// 100 ms units (0: 100, ten seconds).
	Activity uint16
	// NoOutputIOCR leaves the output IOCR out of the next Connect (a
	// malformed request the device must refuse).
	NoOutputIOCR bool

	cmFrom      net.IP // source of the last CControl
	mu          sync.Mutex
	arUUID      [16]byte
	session     uint16
	seq         uint32
	in, out     wire.IOCRLayout
	inFrame     uint16
	outFrame    uint16
	inLens      map[device.Addr]uint16
	outLens     map[device.Addr]uint16
	lastIn      []byte
	lastDS      uint8
	appReady    bool
	sawARServer bool
	outputs     map[device.Addr][]byte
	outIOPS     map[device.Addr]uint8
	outDS       uint8
	sending     bool
	dcp         chan *wire.DCPFrame
	ctx         context.Context
	cancel      context.CancelFunc
}

// NewController attaches a controller with address mac to hub, for a
// device at devMAC whose DCE/RPC socket is devRPC.
func NewController(hub *transport.Hub, mac, devMAC net.HardwareAddr, devRPC *net.UDPAddr) (*Controller, error) {
	cli, err := net.ListenUDP("udp4", &net.UDPAddr{IP: Loopback})
	if err != nil {
		return nil, err
	}
	cm, err := net.ListenUDP("udp4", &net.UDPAddr{IP: Loopback})
	if err != nil {
		cli.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Controller{
		cli: cli, cm: cm, devRPC: devRPC, devMAC: devMAC,
		port: hub.Port(mac, transport.Options{EtherTypes: []uint16{wire.EtherTypePNIO}}),
		dcp:  make(chan *wire.DCPFrame, 8), ctx: ctx, cancel: cancel,
		outputs: map[device.Addr][]byte{}, outIOPS: map[device.Addr]uint8{},
	}
	go c.recvLoop()
	go c.cmLoop()
	go c.sendLoop()
	return c, nil
}

// CMPort is the UDP port the device must send CControl to.
func (c *Controller) CMPort() int { return c.cm.LocalAddr().(*net.UDPAddr).Port }

// MAC is the controller's address.
func (c *Controller) MAC() net.HardwareAddr { return c.port.MAC() }

// Close stops the controller.
func (c *Controller) Close() {
	c.cancel()
	c.port.Close()
	c.cli.Close()
	c.cm.Close()
}

func (c *Controller) recvLoop() {
	for {
		f, err := c.port.Recv(c.ctx)
		if err != nil {
			return
		}
		id := binary.BigEndian.Uint16(f.Payload)
		if id >= wire.FrameIDDCPHello && id <= wire.FrameIDDCPIdentResp {
			if d, err := wire.ParseDCPFrame(f.Payload); err == nil {
				select {
				case c.dcp <- d:
				default:
				}
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
func (c *Controller) cmLoop() {
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
		c.cmFrom = from.IP
		c.mu.Unlock()
		ctl.Command = wire.ControlCmdDone
		body := wire.MarshalPNIOResponseBody(wire.PNIOStatusOK, wire.MarshalIODControl(wire.BlockTypeAppRdyBlockRes, ctl), argsMax, false)
		r := *h
		r.PacketType, r.FragmentLen = wire.RPCPTypeResponse, uint16(len(body))
		c.cm.WriteToUDP(append(r.Marshal(), body...), from)
	}
}

// sendLoop sends the output frame every 2 ms while outputs are on.
func (c *Controller) sendLoop() {
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
			a := device.Addr{Slot: o.SlotNumber, Subslot: o.SubslotNumber}
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
		c.port.Send(c.devMAC, wire.EtherTypePNIO, frame)
	}
}

// StartOutputs sets the outputs and DataStatus and starts the cyclic
// output frames.
func (c *Controller) StartOutputs(out map[device.Addr][]byte, ds uint8) {
	c.SetOutputs(out, ds)
	c.mu.Lock()
	c.sending = true
	c.mu.Unlock()
}

// SetOutputs changes the outputs and the DataStatus of the frames.
func (c *Controller) SetOutputs(out map[device.Addr][]byte, ds uint8) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for a, b := range out {
		c.outputs[a] = b
	}
	c.outDS = ds
}

// SetOutputIOPS sets one subslot's provider status and the DataStatus.
func (c *Controller) SetOutputIOPS(a device.Addr, iops, ds uint8) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outIOPS[a] = iops
	c.outDS = ds
}

// StopOutputs stops the output frames (a pulled cable, a dead PLC).
func (c *Controller) StopOutputs() {
	c.mu.Lock()
	c.sending = false
	c.mu.Unlock()
}

// AppReadySeen reports whether the device sent CControl(ApplicationReady)
// for the current AR.
func (c *Controller) AppReadySeen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.appReady
}

// SawARServer reports whether the last Connect response had an
// ARServerBlockRes.
func (c *Controller) SawARServer() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sawARServer
}

// LastInput returns subslot a's data and IOPS and the DataStatus from the
// last input frame.
func (c *Controller) LastInput(a device.Addr) (data []byte, iops, ds uint8, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastIn == nil {
		return nil, 0, 0, false
	}
	for _, o := range c.in.API.IODataObjects {
		if (device.Addr{Slot: o.SlotNumber, Subslot: o.SubslotNumber}) == a {
			n := int(c.inLens[a])
			off := int(o.FrameOffset)
			return append([]byte(nil), c.lastIn[off:off+n]...), c.lastIn[off+n], c.lastDS, true
		}
	}
	return nil, 0, 0, false
}

func (c *Controller) dcpRequest(f *wire.DCPFrame, dst net.HardwareAddr) *wire.DCPFrame {
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

// Identify asks for name and returns what the answering device reports
// ("" when none answers).
func (c *Controller) Identify(name string) (string, net.IP) {
	r := c.dcpRequest(&wire.DCPFrame{
		FrameID: wire.FrameIDDCPIdentReq, ServiceID: wire.DCPServiceIdentify, ServiceType: wire.DCPServiceTypeRequest,
		Xid: 1, ResponseDelay: 1,
		Blocks: []wire.DCPBlock{{Option: wire.DCPOptionDeviceProperties, SubOption: wire.DCPSubOptionDevNameOfStation, Data: []byte(name)}},
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

func (c *Controller) dcpSet(b wire.DCPBlock) (uint8, error) {
	r := c.dcpRequest(&wire.DCPFrame{
		FrameID: wire.FrameIDDCPGetSet, ServiceID: wire.DCPServiceSet, ServiceType: wire.DCPServiceTypeRequest,
		Xid: 2, Blocks: []wire.DCPBlock{b},
	}, c.devMAC)
	if r == nil || len(r.Blocks) != 1 || len(r.Blocks[0].Data) < 3 {
		return 0, errors.New("no DCP Set response")
	}
	return r.Blocks[0].Data[2], nil // option, suboption, error
}

// SetIP sends a DCP Set IP suite and returns the device's block error.
func (c *Controller) SetIP(ip net.IP) (uint8, error) {
	return c.dcpSet(wire.DCPBlockIPSuite(ip, net.IPv4(255, 0, 0, 0), net.IPv4zero, 0x0001))
}

// SetName sends a DCP Set NameOfStation.
func (c *Controller) SetName(name string) (uint8, error) {
	return c.dcpSet(wire.DCPBlock{Option: wire.DCPOptionDeviceProperties, SubOption: wire.DCPSubOptionDevNameOfStation, Data: append([]byte{0, 1}, name...)})
}

// call sends one PNIO request and returns the response status and blocks.
// twice sends the datagram twice (a retransmission) and checks that both
// answers are the same.
func (c *Controller) call(op uint16, args []byte, twice bool) (wire.PNIOStatus, []wire.PNIOBlock, error) {
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
		return wire.PNIOStatus{}, nil, fmt.Errorf("op %d: %w", op, err)
	}
	if twice {
		m := make([]byte, 65536)
		c.cli.SetReadDeadline(time.Now().Add(2 * time.Second))
		k, _, err := c.cli.ReadFromUDP(m)
		if err != nil || string(m[:k]) != string(buf[:n]) {
			return wire.PNIOStatus{}, nil, fmt.Errorf("retransmission answered differently (%v)", err)
		}
	}
	rh, err := wire.ParseRPCHeader(buf[:n])
	if err != nil || rh.ActivityUUID != h.ActivityUUID {
		return wire.PNIOStatus{}, nil, fmt.Errorf("response header: %v", err)
	}
	return parseResponse(buf[wire.RPCHeaderSize:n], rh.IsLittleEndian())
}

func parseResponse(b []byte, le bool) (wire.PNIOStatus, []wire.PNIOBlock, error) {
	st, blocks, err := wire.ParsePNIOResponseBody(b, le)
	return st, blocks, err
}

func randUUID() [16]byte {
	var u [16]byte
	rand.Read(u[:])
	return u
}

// DAPSubslots are the access point's subslots as a controller expects
// them.
func DAPSubslots() []device.Subslot {
	return []device.Subslot{
		{Addr: device.Addr{Slot: 0, Subslot: 1}, ModuleIdent: device.DAPModuleIdent, SubmoduleIdent: device.DAPSubmoduleIdent},
		{Addr: device.Addr{Slot: 0, Subslot: device.InterfaceSubslot}, ModuleIdent: device.DAPModuleIdent, SubmoduleIdent: device.InterfaceSubmodIdent},
		{Addr: device.Addr{Slot: 0, Subslot: device.PortSubslot}, ModuleIdent: device.DAPModuleIdent, SubmoduleIdent: device.PortSubmodIdent},
	}
}

func (c *Controller) connectArgs(subs []device.Subslot) []byte {
	all := append(DAPSubslots(), subs...)
	var io []wire.IOSubmodule
	for _, s := range all {
		io = append(io, wire.IOSubmodule{Slot: s.Slot, Subslot: s.Subslot, InputLen: s.InputLen, OutputLen: s.OutputLen})
	}
	in, out := wire.LayoutIOCRs(io)
	props := c.Props
	if props == 0 {
		props = 0x11
	}
	c.mu.Lock()
	c.arUUID, c.session = randUUID(), c.session+1
	c.in, c.out, c.inFrame, c.lastIn, c.appReady, c.sawARServer = in, out, 0xC001, nil, false, false
	c.inLens, c.outLens = map[device.Addr]uint16{}, map[device.Addr]uint16{}
	for _, s := range all {
		c.inLens[s.Addr], c.outLens[s.Addr] = s.InputLen, s.OutputLen
	}
	args := wire.MarshalARBlockReq(&wire.ARBlockReq{
		ARType: 1, ARUUID: c.arUUID, SessionKey: c.session, CMInitiatorMAC: c.port.MAC(),
		CMInitiatorObjUUID: randUUID(), ARProperties: props, CMInitiatorActivityTimeout: c.activity(),
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
	if !c.NoOutputIOCR {
		args = append(args, iocr(wire.IOCRTypeOutput, 2, 0xFFFF, out)...)
	}
	args = append(args, wire.MarshalAlarmCRBlockReq(&wire.AlarmCRBlockReq{AlarmCRType: 1, LT: wire.EtherTypePNIO, RTATimeoutFactor: 1, RTARetries: 3, LocalAlarmRef: 7, MaxAlarmDataLen: 200})...)
	api := wire.ExpectedAPI{API: 0}
	for _, s := range all {
		if n := len(api.Modules); n == 0 || api.Modules[n-1].SlotNumber != s.Slot {
			api.Modules = append(api.Modules, wire.ExpectedModule{SlotNumber: s.Slot, ModuleIdentNumber: s.ModuleIdent})
		}
		sm := wire.ExpectedSubmodule{SubslotNumber: s.Subslot, SubmoduleIdentNumber: s.SubmoduleIdent}
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
		m := &api.Modules[len(api.Modules)-1]
		m.Submodules = append(m.Submodules, sm)
	}
	return append(args, wire.MarshalExpectedSubmoduleReq(&wire.ExpectedSubmoduleReq{APIs: []wire.ExpectedAPI{api}})...)
}

func dd(dir, n uint16) wire.SubmoduleDataDesc {
	return wire.SubmoduleDataDesc{DataDirection: dir, DataLength: n, LengthIOCS: 1, LengthIOPS: 1}
}

// Connect opens an AR for the layout subs (sorted by slot, without the
// DAP) and returns the device's status and ModuleDiff. twice sends the
// request twice, as a retransmission.
func (c *Controller) Connect(subs []device.Subslot, twice bool) (wire.PNIOStatus, []wire.DiffModule, error) {
	st, blocks, err := c.call(wire.RPCOpConnect, c.connectArgs(subs), twice)
	if err != nil || !st.IsOK() {
		return st, nil, err
	}
	var diff []wire.DiffModule
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range blocks {
		switch b.Type {
		case wire.BlockTypeIOCRBlockRes:
			if binary.BigEndian.Uint16(b.Data[0:2]) == wire.IOCRTypeOutput {
				c.outFrame = binary.BigEndian.Uint16(b.Data[4:6])
			}
		case wire.BlockTypeARServerBlockRes:
			c.sawARServer = true
		case wire.BlockTypeModuleDiffBlock:
			if diff, err = wire.ParseModuleDiff(b.Data); err != nil {
				return st, nil, err
			}
		}
	}
	if c.outFrame < 0xC000 || c.outFrame > 0xF7FF {
		return st, diff, fmt.Errorf("output FrameID %#04x is not RT_CLASS_1", c.outFrame)
	}
	return st, diff, nil
}

func (c *Controller) control(op, cmd uint16) error {
	c.mu.Lock()
	ctl := wire.IODControl{ARUUID: c.arUUID, SessionKey: c.session, Command: cmd}
	c.mu.Unlock()
	st, _, err := c.call(op, wire.MarshalIODControl(wire.BlockTypeIODControlReq, ctl), false)
	if err == nil && !st.IsOK() {
		err = fmt.Errorf("status %x", st.Marshal())
	}
	return err
}

// PrmEnd ends parameterisation.
func (c *Controller) PrmEnd() error { return c.control(wire.RPCOpControl, wire.ControlCmdPrmEnd) }

// Release ends the AR with an IODReleaseBlockReq (0x0114) and checks the
// reply is an IODReleaseBlockRes (0x8114).
func (c *Controller) Release() error {
	c.mu.Lock()
	ctl := wire.IODControl{ARUUID: c.arUUID, SessionKey: c.session, Command: wire.ControlCmdRelease}
	c.mu.Unlock()
	st, blocks, err := c.call(wire.RPCOpRelease, wire.MarshalIODControl(wire.BlockTypeIODReleaseReq, ctl), false)
	switch {
	case err != nil:
		return err
	case !st.IsOK():
		return fmt.Errorf("status %x", st.Marshal())
	case len(blocks) != 1 || blocks[0].Type != wire.BlockTypeIODReleaseRes:
		return fmt.Errorf("release reply blocks %+v, want one IODReleaseBlockRes (0x8114)", blocks)
	}
	return nil
}

// CControlFrom is the source address of the device's last CControl
// (ApplicationReady): the station address the controller talks to.
func (c *Controller) CControlFrom() net.IP {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cmFrom
}

func (c *Controller) activity() uint16 {
	if c.Activity == 0 {
		return 100
	}
	return c.Activity
}

// Read reads a record and returns its data (after the response header)
// and the PNIO status.
func (c *Controller) Read(slot, subslot, index uint16) ([]byte, wire.PNIOStatus, error) {
	c.mu.Lock()
	h := wire.RecordHeader{ARUUID: c.arUUID, Slot: slot, Subslot: subslot, Index: index, DataLength: 4096}
	c.mu.Unlock()
	body := wire.MarshalPNIORequestBody(wire.MarshalRecordHeader(wire.BlockTypeIODReadReqHeader, h), 4096, false)
	rh := &wire.RPCHeader{
		RPCVersion: wire.RPCVersionCL, PacketType: wire.RPCPTypeRequest, Flags1: wire.RPCFlagLastFrag,
		InterfaceUUID: wire.UUIDPNIOInterface, ActivityUUID: randUUID(), InterfaceVersion: 1,
		OpNum: wire.RPCOpRead, FragmentLen: uint16(len(body)),
	}
	c.cli.WriteToUDP(append(rh.Marshal(), body...), c.devRPC)
	buf := make([]byte, 65536)
	c.cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := c.cli.ReadFromUDP(buf)
	if err != nil {
		return nil, wire.PNIOStatus{}, err
	}
	b := buf[wire.RPCHeaderSize:n]
	st := wire.PNIOStatus{ErrorCode: b[0], ErrorDecode: b[1], ErrorCode1: b[2], ErrorCode2: b[3]}
	args := b[20:]
	if len(args) < 4 {
		return nil, st, nil
	}
	return args[4+int(binary.BigEndian.Uint16(args[2:4])):], st, nil
}

// Write writes records and returns the PNIO status.
func (c *Controller) Write(ws []wire.Write) (wire.PNIOStatus, error) {
	var args []byte
	c.mu.Lock()
	ar := c.arUUID
	c.mu.Unlock()
	for _, w := range ws {
		w.Header.ARUUID, w.Header.DataLength = ar, uint32(len(w.Data))
		args = append(args, wire.MarshalRecordHeader(wire.BlockTypeIODWriteReqHeader, w.Header)...)
		args = append(args, w.Data...)
	}
	st, _, err := c.call(wire.RPCOpWrite, args, false)
	return st, err
}
