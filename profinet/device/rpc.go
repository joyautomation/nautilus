package device

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/joyautomation/nautilus/profinet/wire"
)

// replyCache remembers the last responses by activity and sequence, so a
// request the controller retransmits (its response was lost) is answered
// again rather than executed twice — a second Connect would otherwise
// replace the AR the first one opened.
type replyCache struct {
	keys []replyKey
	pkts map[replyKey][]byte
}

type replyKey struct {
	activity [16]byte
	seq      uint32
	op       uint16
}

const replyCacheSize = 16

func (c *replyCache) get(k replyKey) []byte { return c.pkts[k] }

func (c *replyCache) put(k replyKey, pkt []byte) {
	if c.pkts == nil {
		c.pkts = map[replyKey][]byte{}
	}
	if _, ok := c.pkts[k]; !ok {
		c.keys = append(c.keys, k)
		if len(c.keys) > replyCacheSize {
			delete(c.pkts, c.keys[0])
			c.keys = c.keys[1:]
		}
	}
	c.pkts[k] = pkt
}

func (d *Device) rpcLoop(ctx context.Context) {
	var cache replyCache
	buf := make([]byte, 65536)
	for {
		n, from, err := d.cfg.RPC.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			d.log.Debug("profinet device: rpc read", "error", err)
			continue
		}
		udp, ok := from.(*net.UDPAddr)
		if !ok {
			continue
		}
		d.handleRPC(buf[:n], udp, &cache)
	}
}

// handleRPC serves one datagram. Requests are handled one at a time in
// arrival order: a controller's Connect, Write, Read and Control are
// sequential, and the order is what makes the AR's state right.
func (d *Device) handleRPC(pkt []byte, from *net.UDPAddr, cache *replyCache) {
	h, err := wire.ParseRPCHeader(pkt)
	if err != nil || h.RPCVersion != wire.RPCVersionCL || h.PacketType != wire.RPCPTypeRequest {
		return
	}
	body := pkt[wire.RPCHeaderSize:]
	if int(h.FragmentLen) < len(body) {
		body = body[:h.FragmentLen]
	}
	key := replyKey{h.ActivityUUID, h.SequenceNum, h.OpNum}
	if resp := cache.get(key); resp != nil {
		d.cfg.RPC.WriteTo(resp, from)
		return
	}
	var out []byte
	switch h.InterfaceUUID {
	case wire.UUIDPNIOInterface:
		var after func()
		out, after = d.pnio(h, body, from)
		if out != nil {
			cache.put(key, out)
			d.cfg.RPC.WriteTo(out, from)
		}
		if after != nil {
			go after()
		}
		return
	case wire.UUIDEndpointMapper:
		out = d.epm(h, body)
	}
	if out != nil {
		d.cfg.RPC.WriteTo(out, from)
	}
}

// pnio serves one PNIO request and returns the response datagram and an
// action to run once it is sent.
func (d *Device) pnio(h *wire.RPCHeader, body []byte, from *net.UDPAddr) ([]byte, func()) {
	le := h.IsLittleEndian()
	argsMax, args, err := wire.ParsePNIORequestArgs(body, le)
	if err != nil {
		return d.response(h, wire.PNIOStatus{ErrorCode: 0xDB, ErrorDecode: wire.ErrorDecodePNIO, ErrorCode1: 0x01}, nil, 0), nil
	}
	var (
		resp   []byte
		status wire.PNIOStatus
		after  func()
	)
	blocks := func() []wire.PNIOBlock { b, _ := wire.ParsePNIOBlocks(args); return b }
	d.mu.Lock()
	switch h.OpNum {
	case wire.RPCOpConnect:
		resp, status = d.connect(blocks(), from)
	case wire.RPCOpRelease:
		resp, status = d.release(blocks())
	case wire.RPCOpRead, wire.RPCOpReadImplicit:
		resp, status = d.read(blocks())
	case wire.RPCOpWrite:
		resp, status = d.write(args)
	case wire.RPCOpControl:
		resp, status, after = d.control(blocks())
	default:
		d.mu.Unlock()
		return nil, nil
	}
	d.mu.Unlock()
	return d.response(h, status, resp, argsMax), after
}

// response builds the response datagram to request h.
func (d *Device) response(h *wire.RPCHeader, status wire.PNIOStatus, blocks []byte, argsMax uint32) []byte {
	body := wire.MarshalPNIOResponseBody(status, blocks, argsMax, h.IsLittleEndian())
	return d.reply(h, body)
}

func (d *Device) reply(h *wire.RPCHeader, body []byte) []byte {
	flags := wire.RPCFlagLastFrag | wire.RPCFlagNoFack
	if h.Flags1&wire.RPCFlagIdempotent != 0 {
		flags |= wire.RPCFlagIdempotent
	}
	r := &wire.RPCHeader{
		RPCVersion: wire.RPCVersionCL, PacketType: wire.RPCPTypeResponse, Flags1: flags,
		DataRep: h.DataRep, SerialHigh: h.SerialHigh, ObjectUUID: h.ObjectUUID,
		InterfaceUUID: h.InterfaceUUID, ActivityUUID: h.ActivityUUID, ServerBootTime: d.boot,
		InterfaceVersion: h.InterfaceVersion, SequenceNum: h.SequenceNum, OpNum: h.OpNum,
		InterfaceHint: 0xFFFF, ActivityHint: 0xFFFF, FragmentLen: uint16(len(body)), SerialLow: h.SerialLow,
	}
	return append(r.Marshal(), body...)
}

// sendAppReady sends CControl(ApplicationReady) to the controller from a
// fresh UDP socket, to the controller's CM port (not the port its Connect
// came from), and waits for the acknowledgement that turns on the Run
// bit in our frames.
func (d *Device) sendAppReady(a *ar) error {
	block := wire.MarshalIODControl(wire.BlockTypeAppRdyBlockReq, wire.IODControl{
		ARUUID: a.uuid, SessionKey: a.sessionKey, Command: wire.ControlCmdApplicationReady,
	})
	body := wire.MarshalPNIORequestBody(block, 16384, false)
	act := activityUUID(d.cfg.Port.MAC())
	hdr := &wire.RPCHeader{
		RPCVersion: wire.RPCVersionCL, PacketType: wire.RPCPTypeRequest,
		Flags1:     wire.RPCFlagLastFrag | wire.RPCFlagNoFack | wire.RPCFlagIdempotent,
		ObjectUUID: a.ctrlObjUUID, InterfaceUUID: wire.UUIDPNIOControllerInterface,
		ActivityUUID: act, InterfaceVersion: 1, OpNum: wire.RPCOpControl,
		InterfaceHint: 0xFFFF, ActivityHint: 0xFFFF, FragmentLen: uint16(len(body)),
	}
	pkt := append(hdr.Marshal(), body...)
	sock, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return err
	}
	defer sock.Close()
	dst := &net.UDPAddr{IP: a.ctrlAddr.IP, Port: d.cfg.ControllerCMPort}
	buf := make([]byte, 4096)
	// The datagram may be lost; resend a few times within the window.
	for try := 0; try < 3; try++ {
		if _, err := sock.WriteToUDP(pkt, dst); err != nil {
			return err
		}
		sock.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			n, _, err := sock.ReadFromUDP(buf)
			if err != nil {
				break // timeout: resend
			}
			rh, err := wire.ParseRPCHeader(buf[:n])
			if err != nil || rh.PacketType != wire.RPCPTypeResponse || rh.ActivityUUID != act {
				continue
			}
			st, blocks, err := wire.ParsePNIOResponseBody(buf[wire.RPCHeaderSize:n], rh.IsLittleEndian())
			if err != nil {
				return err
			}
			if !st.IsOK() {
				return fmt.Errorf("controller refused ApplicationReady: %x", st.Marshal())
			}
			for _, b := range blocks {
				c, err := wire.ParseIODControl(b.Data)
				if err == nil && c.ARUUID == a.uuid && c.Command&wire.ControlCmdDone != 0 {
					a.appReady.Store(true)
					return nil
				}
			}
			return errors.New("ApplicationReady response without Done")
		}
	}
	return errors.New("no answer to ApplicationReady")
}

func activityUUID(mac net.HardwareAddr) [16]byte {
	var u [16]byte
	now := uint64(time.Now().UnixNano() / 100)
	binary.BigEndian.PutUint32(u[0:4], uint32(now))
	binary.BigEndian.PutUint16(u[4:6], uint16(now>>32))
	binary.BigEndian.PutUint16(u[6:8], uint16(now>>48)&0x0FFF|0x1000)
	u[8] = 0x80
	u[9] = byte(now)
	copy(u[10:], mac)
	return u
}

// epm answers an endpoint-mapper lookup (ept_lookup, opnum 2): the PNIO
// interface's tower on the first call, "no more entries" after. An
// engineering tool uses it to find the device's RPC endpoint.
func (d *Device) epm(h *wire.RPCHeader, body []byte) []byte {
	if h.OpNum != 2 {
		return nil
	}
	le := h.IsLittleEndian()
	var order binary.AppendByteOrder = binary.BigEndian
	if le {
		order = binary.LittleEndian
	}
	u32 := func(b []byte, v uint32) []byte { return order.AppendUint32(b, v) }
	first := true
	if len(body) >= 72 {
		for _, c := range body[52:72] {
			if c != 0 {
				first = false
				break
			}
		}
	}
	if !first {
		r := make([]byte, 20) // zero context handle
		r = u32(r, 0)         // num_entry
		r = u32(r, 0)
		r = u32(r, 0)
		r = u32(r, 0)
		r = u32(r, 0x16c9a0d6) // EPT_S_NOT_REGISTERED
		return d.reply(h, r)
	}
	var ip net.IP
	if d.cfg.InterfaceIP != nil {
		ip, _ = d.cfg.InterfaceIP()
	}
	tower := buildTower(ip)
	obj := wire.UUIDPNIOObjectBase
	obj[10], obj[11] = 0x00, 0x01
	obj[12], obj[13] = byte(d.cfg.DeviceID>>8), byte(d.cfg.DeviceID)
	obj[14], obj[15] = byte(d.cfg.VendorID>>8), byte(d.cfg.VendorID)

	var r []byte
	handle := make([]byte, 20)
	copy(handle[4:], act16(d.cfg.Port.MAC()))
	r = append(r, handle...)
	r = u32(r, 1) // num_entry
	r = u32(r, 1) // max_count
	r = u32(r, 0) // offset
	r = u32(r, 1) // actual_count
	r = append(r, wire.UUIDToWire(obj, le)...)
	r = u32(r, 3) // tower referent
	ann := fmt.Sprintf("%-25.25s %-20s %5d V  0  0  1", d.cfg.VendorName, "", 1)
	annLen := uint32(len(ann) + 1)
	r = u32(r, 0)
	r = u32(r, annLen)
	r = append(r, ann...)
	r = append(r, 0)
	for len(r)%4 != 0 {
		r = append(r, 0)
	}
	r = u32(r, uint32(len(tower)))
	r = u32(r, uint32(len(tower)))
	r = append(r, tower...)
	for len(r)%4 != 0 {
		r = append(r, 0)
	}
	r = u32(r, 0) // status
	return d.reply(h, r)
}

func act16(mac net.HardwareAddr) []byte {
	u := activityUUID(mac)
	return u[:]
}

// buildTower is the DCE/RPC protocol tower for the PNIO interface over
// UDP/IP at ip:34964 (floor fields are little-endian by definition).
func buildTower(ip net.IP) []byte {
	const cmPort uint16 = wire.PNIOCMPort
	ip4 := ip.To4()
	if ip4 == nil {
		ip4 = net.IPv4zero.To4()
	}
	t := []byte{0x05, 0x00} // five floors
	t = append(t, 0x13, 0x00, 0x0D)
	t = append(t, wire.UUIDToWire(wire.UUIDPNIOInterface, true)...)
	t = append(t, 0x01, 0x00, 0x02, 0x00, 0x00, 0x00) // version 1.0
	t = append(t, 0x13, 0x00, 0x0D)
	t = append(t, wire.UUIDToWire(wire.UUIDNDRTransferSyntax, true)...)
	t = append(t, 0x02, 0x00, 0x02, 0x00, 0x00, 0x00)                               // NDR 2.0
	t = append(t, 0x01, 0x00, 0x0A, 0x02, 0x00, 0x00, 0x00)                         // connectionless RPC
	t = append(t, 0x01, 0x00, 0x08, 0x02, 0x00, byte(cmPort>>8), byte(cmPort&0xFF)) // UDP port
	t = append(t, 0x01, 0x00, 0x09, 0x04, 0x00)                                     // IP
	return append(t, ip4...)
}
