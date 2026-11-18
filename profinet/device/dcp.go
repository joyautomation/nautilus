package device

import (
	"fmt"
	"math/rand/v2"
	"net"
	"time"

	"github.com/joyautomation/nautilus/profinet/transport"
	"github.com/joyautomation/nautilus/profinet/wire"
)

// DCP block errors in a Set response.
const (
	dcpOK            uint8 = 0x00
	dcpNotSupported  uint8 = 0x02
	dcpNotSet        uint8 = 0x03 // suboption not set: the value was refused
	dcpResourceError uint8 = 0x04
)

func (d *Device) handleDCP(f transport.Frame) {
	req, err := wire.ParseDCPFrame(f.Payload)
	if err != nil || req.ServiceType != wire.DCPServiceTypeRequest {
		return
	}
	switch {
	case req.IsIdentifyRequest():
		d.dcpIdentify(req, f.Src)
	case req.IsSetRequest():
		d.dcpSet(req, f.Src)
	case req.IsGetRequest():
		d.dcpGet(req, f.Src)
	}
}

func (d *Device) dcpIdentify(req *wire.DCPFrame, to net.HardwareAddr) {
	d.mu.Lock()
	match := req.MatchesFilter(d.station, d.cfg.VendorID, d.cfg.DeviceID)
	blocks := d.identityBlocks()
	d.mu.Unlock()
	if !match {
		return
	}
	resp := &wire.DCPFrame{
		FrameID: wire.FrameIDDCPIdentResp, ServiceID: wire.DCPServiceIdentify,
		ServiceType: wire.DCPServiceTypeResponse, Xid: req.Xid,
		Blocks: append(blocks, wire.DCPBlockDeviceOptions(blocks)),
	}
	// A multicast Identify asks every station to answer within
	// ResponseDelay × 10 ms, spread so the answers do not collide.
	send := func() { d.cfg.Port.Send(to, wire.EtherTypePNIO, wire.MarshalDCPFrame(resp)) }
	if req.ResponseDelay > 1 {
		time.AfterFunc(time.Duration(rand.IntN(int(req.ResponseDelay)*10))*time.Millisecond, send)
		return
	}
	send()
}

// identityBlocks are the device's DCP properties (d.mu held). Until an
// IP suite has been set over DCP the device reports none: a Siemens PLC
// assigns the address it was configured with and will not open the AR to
// a device that reports one on first contact.
func (d *Device) identityBlocks() []wire.DCPBlock {
	ip, mask, gw, info := net.IPv4zero.To4(), net.IPv4zero.To4(), net.IPv4zero.To4(), wire.DCPBlockInfoIPNotSet
	if s := d.suite; s != nil && d.holds(s.ip) {
		ip, mask, gw, info = s.ip, s.mask, s.gw, wire.DCPBlockInfoIPSet
	}
	return []wire.DCPBlock{
		wire.DCPBlockNameOfStation(d.station),
		wire.DCPBlockVendor(d.cfg.VendorName),
		wire.DCPBlockDeviceID(d.cfg.VendorID, d.cfg.DeviceID),
		wire.DCPBlockDeviceRole(wire.DeviceRoleIODevice),
		wire.DCPBlockDeviceInstance(0x00, 0x01),
		wire.DCPBlockIPSuite(ip, mask, gw, info),
		wire.DCPBlockDHCPClientID(d.cfg.Port.MAC()),
	}
}

// dcpSet answers a Set. DCP is unauthenticated, so nothing in it reaches
// the host: the name is taken in memory, and an IP suite is accepted only
// when it is the address the interface already has.
func (d *Device) dcpSet(req *wire.DCPFrame, to net.HardwareAddr) {
	d.mu.Lock()
	var resp []wire.DCPBlock
	for _, b := range req.Blocks {
		code := dcpOK
		switch {
		case b.Option == wire.DCPOptionIP && b.SubOption == wire.DCPSubOptionIPSuite:
			code = d.setIP(b.Data)
		case b.Option == wire.DCPOptionDeviceProperties && b.SubOption == wire.DCPSubOptionDevNameOfStation:
			name := ""
			if len(b.Data) >= 2 {
				name = string(b.Data[2:])
			}
			// An empty name clears it (a reset); any other must be a
			// valid NameOfStation, which also keeps it short enough for
			// LLDP's 9-bit TLV length.
			if name != "" {
				if err := ValidStationName(name); err != nil {
					code = dcpNotSet
					d.snapInfo.lastEvent = fmt.Sprintf("station name from %s refused: %v", to, err)
					d.log.Warn("profinet device: " + d.snapInfo.lastEvent)
					break
				}
			}
			if name != d.station {
				d.counters.nameSets++
				d.snapInfo.lastEvent = fmt.Sprintf("station name set over DCP by %s: %q (manifest: %q, not saved)", to, name, d.cfg.Station)
				d.log.Warn("profinet device: " + d.snapInfo.lastEvent)
				d.station = name
			}
		case b.Option == wire.DCPOptionControl:
			// Start/Stop/Signal/Response: acknowledge; Signal (flash the
			// LED) is logged, the only indicator a host has.
			if b.SubOption == wire.DCPSubOptionControlSignal {
				d.log.Info("profinet device: DCP Signal from " + to.String())
			}
		default:
			code = dcpNotSupported
		}
		resp = append(resp, wire.DCPBlockControlResponse(b.Option, b.SubOption, code))
	}
	d.mu.Unlock()
	d.cfg.Port.Send(to, wire.EtherTypePNIO, wire.MarshalDCPFrame(&wire.DCPFrame{
		FrameID: wire.FrameIDDCPGetSet, ServiceID: wire.DCPServiceSet,
		ServiceType: wire.DCPServiceTypeResponse, Xid: req.Xid, Blocks: resp,
	}))
}

// setIP checks a Set IP suite against the interface (d.mu held).
func (d *Device) setIP(data []byte) uint8 {
	if len(data) < 14 {
		return dcpNotSet
	}
	ip, mask, gw, err := wire.ParseIPSuiteBlock(data[2:])
	if err != nil {
		return dcpNotSet
	}
	if ip.Equal(net.IPv4zero) {
		d.suite = nil // "reset to no address": nothing to apply either
		return dcpOK
	}
	if d.holds(ip) {
		d.suite = &ipSuite{ip: ip.To4(), mask: mask.To4(), gw: gw.To4()}
		return dcpOK
	}
	d.counters.setIPRefused++
	var have []net.IP
	if d.cfg.InterfaceAddrs != nil {
		have = d.cfg.InterfaceAddrs()
	}
	d.snapInfo.lastEvent = fmt.Sprintf("refused DCP Set IP %s: the interface holds %v; give the device one of those addresses in the controller's project", ip, have)
	d.log.Warn("profinet device: " + d.snapInfo.lastEvent)
	return dcpNotSet
}

func (d *Device) dcpGet(req *wire.DCPFrame, to net.HardwareAddr) {
	d.mu.Lock()
	all := d.identityBlocks()
	d.mu.Unlock()
	var resp []wire.DCPBlock
	for _, want := range req.Blocks {
		for _, b := range all {
			if want.Option == wire.DCPOptionAllSelector || (want.Option == b.Option && want.SubOption == b.SubOption) {
				resp = append(resp, b)
			}
		}
	}
	d.cfg.Port.Send(to, wire.EtherTypePNIO, wire.MarshalDCPFrame(&wire.DCPFrame{
		FrameID: wire.FrameIDDCPGetSet, ServiceID: wire.DCPServiceGet,
		ServiceType: wire.DCPServiceTypeResponse, Xid: req.Xid, Blocks: resp,
	}))
}

// handleAlarm acknowledges the controller's alarm notifications (the
// controller aborts an AR whose alarms go unanswered) and counts them.
func (d *Device) handleAlarm(f transport.Frame) {
	p, err := wire.ParseRTA(f.Payload)
	if err != nil {
		return
	}
	d.mu.Lock()
	a := d.ar
	if a == nil || a.alarm == nil || !macEqual(f.Src, a.ctrlMAC) || p.DstRef != a.alarm.localRef {
		d.mu.Unlock()
		return
	}
	switch p.PDUType {
	case wire.RTAPDUTypeData:
		d.counters.alarms++
		d.mu.Unlock()
		d.cfg.Port.Send(a.ctrlMAC, wire.EtherTypePNIO, wire.BuildAlarmAck(p.FrameID, p.SrcRef, p.DstRef, p.SendSeq))
		return
	case wire.RTAPDUTypeERR:
		d.counters.aborts++
		d.endAR(a, "the controller aborted the AR (RTA ERR)")
	}
	d.mu.Unlock()
}

// holds reports whether the interface has address ip.
func (d *Device) holds(ip net.IP) bool {
	if d.cfg.InterfaceAddrs == nil {
		return false
	}
	for _, a := range d.cfg.InterfaceAddrs() {
		if a.Equal(ip) {
			return true
		}
	}
	return false
}
