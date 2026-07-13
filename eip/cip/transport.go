package cip

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// EIPPort is the well-known TCP/UDP port for EtherNet/IP encapsulation.
const EIPPort = 44818

// Server is a generic CIP / EtherNet/IP server. It owns the TCP listener,
// UDP listener (for ListIdentity broadcasts), and per-connection state. It
// delegates explicit message routing to a Dispatcher, and exposes hooks for
// callers (PF70 sim, Logix-tag server, etc.) to plug in ListIdentity payload
// generation and Forward_Open handling.
type Server struct {
	Addr       string // "0.0.0.0:44818" by default
	Log        *slog.Logger
	Dispatcher *Dispatcher

	// ListIdentityPayload produces the data portion of a ListIdentity reply.
	// The server calls it with the connection's local IP (so the reply
	// announces the correct interface). Callers should embed their device's
	// vendor/product/serial here.
	ListIdentityPayload func(localAddr net.IP, localPort uint16) []byte

	// HandleConnectedMessage is invoked when a SendUnitData arrives carrying
	// a connected explicit (or implicit, in some scanners') message. Optional;
	// nil means we ignore connected explicit messages.
	HandleConnectedMessage func(connID uint32, sequence uint16, mr MessageRouterRequest) MessageRouterResponse

	// ForwardOpen callback. Returns the connection-ID pair (O→T, T→O) and
	// per-direction RPI in µs. Return false to refuse the connection — server
	// will reply with a Forward_Open error to the originator. Optional; nil
	// means we refuse all Class 1 connections.
	HandleForwardOpen func(req ForwardOpenRequest) (resp ForwardOpenResponse, ok bool)

	// HandleForwardClose closes a previously opened connection.
	HandleForwardClose func(connSerial uint16, vendor uint16, origSerial uint32)

	mu          sync.Mutex
	sessions    map[uint32]*session
	nextSession uint32

	tcpLn  net.Listener
	udpLn  *net.UDPConn
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// session is per-TCP-connection state. SessionHandle is the value handed to
// the client during RegisterSession and required on subsequent requests.
type session struct {
	handle uint32
	conn   net.Conn
	addr   net.Addr
}

// NewServer builds a server bound to addr (e.g. ":44818"). The caller wires
// up the dispatcher and callbacks before invoking Run.
func NewServer(addr string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		Addr:        addr,
		Log:         log,
		Dispatcher:  NewDispatcher(),
		sessions:    make(map[uint32]*session),
		nextSession: 0x0E1AA001, // arbitrary nonzero seed
	}
}

// Run blocks serving until ctx is cancelled. It starts both the TCP listener
// (for explicit messaging) and the UDP listener on the same port (for
// broadcast ListIdentity from RSLinx / Studio 5000 discovery).
func (s *Server) Run(ctx context.Context) error {
	ctx, s.cancel = context.WithCancel(ctx)

	tcp, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	s.tcpLn = tcp

	udpAddr, err := net.ResolveUDPAddr("udp", s.Addr)
	if err != nil {
		_ = tcp.Close()
		return err
	}
	udp, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		_ = tcp.Close()
		return err
	}
	s.udpLn = udp

	s.Log.Info("cip server: listening", "addr", s.Addr)

	s.wg.Add(2)
	go s.serveTCP(ctx)
	go s.serveUDP(ctx)

	<-ctx.Done()
	_ = s.tcpLn.Close()
	_ = s.udpLn.Close()
	s.wg.Wait()
	return nil
}

// Stop tears the server down.
func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}

// serveTCP accepts encapsulation connections. Each connection runs in its
// own goroutine; we don't try to bound concurrency because real industrial
// installs see at most a handful of scanners simultaneously.
func (s *Server) serveTCP(ctx context.Context) {
	defer s.wg.Done()
	for {
		conn, err := s.tcpLn.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.Log.Warn("cip server: tcp accept", "error", err)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(ctx, conn)
		}()
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr()
	s.Log.Debug("cip server: client connected", "remote", remote)
	defer s.Log.Debug("cip server: client disconnected", "remote", remote)

	sess := &session{conn: conn, addr: remote}

	hdrBuf := make([]byte, EncapHeaderLen)
	for {
		if ctx.Err() != nil {
			return
		}
		// Each message: 24-byte header then header.Length bytes of payload.
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		if _, err := io.ReadFull(conn, hdrBuf); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				var ne net.Error
				if !errors.As(err, &ne) || !ne.Timeout() {
					s.Log.Debug("cip server: read header", "remote", remote, "error", err)
				}
			}
			return
		}
		var h EncapHeader
		_ = h.UnmarshalBinary(hdrBuf)

		var payload []byte
		if h.Length > 0 {
			payload = make([]byte, h.Length)
			if _, err := io.ReadFull(conn, payload); err != nil {
				s.Log.Debug("cip server: read payload", "remote", remote, "error", err)
				return
			}
		}

		reply, replyData, drop := s.dispatch(sess, h, payload)
		if drop {
			return
		}
		out := EncodeFrame(reply, replyData)
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(out); err != nil {
			s.Log.Debug("cip server: write", "remote", remote, "error", err)
			return
		}
	}
}

// dispatch routes a single TCP-framed encap request. Returns (replyHeader,
// replyData, drop). When drop is true, the connection should close.
func (s *Server) dispatch(sess *session, h EncapHeader, data []byte) (EncapHeader, []byte, bool) {
	reply := EncapHeader{
		Command:       h.Command,
		SessionHandle: h.SessionHandle,
		SenderContext: h.SenderContext,
		Options:       h.Options,
	}
	switch h.Command {
	case CmdNOP:
		return reply, data, false // echo back
	case CmdRegisterSession:
		return s.handleRegisterSession(sess, reply, data)
	case CmdUnRegisterSession:
		s.dropSession(sess.handle)
		return reply, nil, true
	case CmdListIdentity:
		return s.handleListIdentity(reply, sess.conn.LocalAddr())
	case CmdListServices:
		return s.handleListServices(reply)
	case CmdSendRRData:
		return s.handleSendRRData(reply, data)
	case CmdSendUnitData:
		return s.handleSendUnitData(reply, data)
	default:
		reply.Status = EncapStatusInvalidCommand
		return reply, nil, false
	}
}

func (s *Server) handleRegisterSession(sess *session, reply EncapHeader, data []byte) (EncapHeader, []byte, bool) {
	// Payload: [u16 protocolVersion][u16 optionsFlags]; both must be valid.
	if len(data) < 4 {
		reply.Status = EncapStatusInvalidLength
		return reply, nil, true
	}
	proto := binary.LittleEndian.Uint16(data[0:2])
	if proto != 1 {
		reply.Status = EncapStatusUnsupportedProto
		return reply, nil, true
	}
	s.mu.Lock()
	s.nextSession++
	handle := s.nextSession
	sess.handle = handle
	s.sessions[handle] = sess
	s.mu.Unlock()
	reply.SessionHandle = handle
	return reply, data[:4], false // echo protocol/options back
}

func (s *Server) dropSession(handle uint32) {
	s.mu.Lock()
	delete(s.sessions, handle)
	s.mu.Unlock()
}

func (s *Server) handleListIdentity(reply EncapHeader, local net.Addr) (EncapHeader, []byte, bool) {
	if s.ListIdentityPayload == nil {
		reply.Status = EncapStatusInvalidCommand
		return reply, nil, false
	}
	ip, port := splitIPPort(local)
	body := s.ListIdentityPayload(ip, port)
	items := []CPFItem{{TypeID: ItemListIdentity, Data: body}}
	return reply, EncodeCPF(items), false
}

func (s *Server) handleListServices(reply EncapHeader) (EncapHeader, []byte, bool) {
	// Single item describing CIP-over-TCP capability (Vol 2 §2-4.5).
	data := make([]byte, 4+16)
	binary.LittleEndian.PutUint16(data[0:2], 1)    // protocol version
	binary.LittleEndian.PutUint16(data[2:4], 1<<5) // capability: supports CIP encap over TCP
	copy(data[4:], padString("Communications", 16))
	items := []CPFItem{{TypeID: ItemListServices, Data: data}}
	return reply, EncodeCPF(items), false
}

func (s *Server) handleSendRRData(reply EncapHeader, data []byte) (EncapHeader, []byte, bool) {
	_, items, err := DecodeSendRRData(data)
	if err != nil || len(items) < 2 {
		reply.Status = EncapStatusIncorrectData
		return reply, nil, false
	}
	// Unconnected: items[0] = NullAddress, items[1] = UnconnectedData.
	if items[0].TypeID != ItemNullAddress || items[1].TypeID != ItemUnconnectedData {
		reply.Status = EncapStatusIncorrectData
		return reply, nil, false
	}
	req, err := DecodeMRRequest(items[1].Data)
	if err != nil {
		reply.Status = EncapStatusIncorrectData
		return reply, nil, false
	}

	// Some unconnected messages wrap a real request — e.g. Connection
	// Manager Forward_Open. Handle CM separately so we can run the
	// ForwardOpen callback.
	mrResp := s.routeMR(req)
	respItems := []CPFItem{
		{TypeID: ItemNullAddress, Data: nil},
		{TypeID: ItemUnconnectedData, Data: mrResp.Encode()},
	}
	return reply, EncodeSendRRData(SendRRDataHeader{Timeout: 5}, respItems), false
}

// routeMR handles a Message Router request: either a normal class request or
// a Connection Manager service (Forward_Open, Forward_Close, UnconnectedSend).
// UnconnectedSend wraps a nested request that we re-dispatch.
func (s *Server) routeMR(req MessageRouterRequest) MessageRouterResponse {
	// Connection Manager (class 0x06, instance 1) — Forward_Open / Close
	// and UnconnectedSend live here.
	path, _, err := ParsePath(req.Path)
	if err == nil && path.HasClass && uint16(path.Class) == ClassConnectionManager {
		switch req.Service {
		case ServiceForwardOpen, ServiceLargeForwardOpen:
			return s.handleForwardOpen(req)
		case ServiceForwardClose:
			return s.handleForwardClose(req)
		case ServiceUnconnectedSend: // 0x52
			return s.handleUnconnectedSend(req)
		}
	}
	return s.Dispatcher.Dispatch(req)
}

func (s *Server) handleSendUnitData(reply EncapHeader, data []byte) (EncapHeader, []byte, bool) {
	_, items, err := DecodeSendRRData(data)
	if err != nil || len(items) < 2 {
		reply.Status = EncapStatusIncorrectData
		return reply, nil, false
	}
	// Connected: items[0] = ConnectedAddress (4-byte conn ID), items[1] =
	// ConnectedData (starts with u16 sequence count then the MR request).
	if items[0].TypeID != ItemConnectedAddress || len(items[0].Data) < 4 {
		reply.Status = EncapStatusIncorrectData
		return reply, nil, false
	}
	if items[1].TypeID != ItemConnectedData || len(items[1].Data) < 2 {
		reply.Status = EncapStatusIncorrectData
		return reply, nil, false
	}
	connID := binary.LittleEndian.Uint32(items[0].Data[:4])
	sequence := binary.LittleEndian.Uint16(items[1].Data[:2])
	req, err := DecodeMRRequest(items[1].Data[2:])
	if err != nil {
		reply.Status = EncapStatusIncorrectData
		return reply, nil, false
	}

	var mrResp MessageRouterResponse
	if s.HandleConnectedMessage != nil {
		mrResp = s.HandleConnectedMessage(connID, sequence, req)
	} else {
		mrResp = s.Dispatcher.Dispatch(req)
	}

	// Reply mirrors the connected-data structure: ConnectedAddress (the
	// returning connection ID, typically the T→O peer of the originator's
	// O→T) + ConnectedData (seq+1, then MR response).
	addrData := make([]byte, 4)
	binary.LittleEndian.PutUint32(addrData, connID)
	respData := make([]byte, 2+len(mrResp.Encode()))
	binary.LittleEndian.PutUint16(respData[:2], sequence)
	copy(respData[2:], mrResp.Encode())
	respItems := []CPFItem{
		{TypeID: ItemConnectedAddress, Data: addrData},
		{TypeID: ItemConnectedData, Data: respData},
	}
	return reply, EncodeSendRRData(SendRRDataHeader{Timeout: 5}, respItems), false
}

// serveUDP handles broadcast ListIdentity from discovery tools (RSLinx
// Classic, Studio 5000 "Add EtherNet/IP node" wizard, etc.). The client
// blasts a ListIdentity command to 255.255.255.255:44818 and expects every
// device to reply unicast.
func (s *Server) serveUDP(ctx context.Context) {
	defer s.wg.Done()
	buf := make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return
		}
		_ = s.udpLn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, src, err := s.udpLn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			s.Log.Debug("cip server: udp read", "error", err)
			continue
		}
		if n < EncapHeaderLen {
			continue
		}
		var h EncapHeader
		if err := h.UnmarshalBinary(buf[:EncapHeaderLen]); err != nil {
			continue
		}
		if h.Command != CmdListIdentity {
			continue
		}
		ip := localUDPAddr(s.udpLn)
		reply := EncapHeader{
			Command:       CmdListIdentity,
			SessionHandle: h.SessionHandle,
			SenderContext: h.SenderContext,
			Options:       h.Options,
		}
		body := s.ListIdentityPayload(ip, EIPPort)
		items := []CPFItem{{TypeID: ItemListIdentity, Data: body}}
		out := EncodeFrame(reply, EncodeCPF(items))
		_ = s.udpLn.SetWriteDeadline(time.Now().Add(1 * time.Second))
		if _, err := s.udpLn.WriteToUDP(out, src); err != nil {
			s.Log.Debug("cip server: udp write", "src", src, "error", err)
		}
	}
}

// SessionCount is exposed for tests / observability.
func (s *Server) SessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// Stats keeps light counters useful for the REST surface.
type Stats struct {
	OpenSessions int32
	OpenConns    int32
}

// statsView is updated by the server. Exposed via atomics so reads from the
// HTTP surface don't synchronize with hot paths.
var _ = atomic.Int32{}

// ── helpers ────────────────────────────────────────────────────────────

func splitIPPort(addr net.Addr) (net.IP, uint16) {
	switch a := addr.(type) {
	case *net.TCPAddr:
		return a.IP, uint16(a.Port)
	case *net.UDPAddr:
		return a.IP, uint16(a.Port)
	}
	return net.IPv4zero, 0
}

func localUDPAddr(c *net.UDPConn) net.IP {
	if la, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return la.IP
	}
	return net.IPv4zero
}

func padString(s string, n int) []byte {
	out := make([]byte, n)
	copy(out, []byte(s))
	return out
}
