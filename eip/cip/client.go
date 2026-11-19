package cip

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Client is an EtherNet/IP originator: it speaks TCP to a target device
// and runs RegisterSession + Forward_Open.
//
// One Client per target IP. Concurrent Forward_Open calls on the same
// session are not supported — wrap with a mutex if you need them.
type Client struct {
	Host string
	Port int // default 44818
	Log  *slog.Logger

	mu       sync.Mutex
	tcp      net.Conn
	sessHdl  uint32
	ctxToken uint64 // monotonic SenderContext seed
}

// NewClient returns an idle client. Call Connect to open the TCP session.
func NewClient(host string, port int, log *slog.Logger) *Client {
	if port == 0 {
		port = EIPPort
	}
	if log == nil {
		log = slog.Default()
	}
	return &Client{Host: host, Port: port, Log: log}
}

// Connect opens TCP and registers a CIP session. Safe to call once; reuse
// the client for many Forward_Open calls afterwards.
func (c *Client) Connect(ctx context.Context) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(c.Host, fmt.Sprint(c.Port)))
	if err != nil {
		return fmt.Errorf("cip client: dial: %w", err)
	}
	c.mu.Lock()
	c.tcp = conn
	c.mu.Unlock()
	if err := c.registerSession(ctx); err != nil {
		_ = conn.Close()
		c.mu.Lock()
		c.tcp = nil
		c.mu.Unlock()
		return fmt.Errorf("cip client: register session: %w", err)
	}
	return nil
}

// Close tears down the TCP session.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tcp == nil {
		return nil
	}
	// Best-effort UnRegisterSession; ignore errors.
	hdr := EncapHeader{Command: CmdUnRegisterSession, SessionHandle: c.sessHdl}
	_, _ = c.tcp.Write(EncodeFrame(hdr, nil))
	err := c.tcp.Close()
	c.tcp = nil
	return err
}

// SessionHandle returns the negotiated session handle (0 if not connected).
func (c *Client) SessionHandle() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessHdl
}

func (c *Client) registerSession(ctx context.Context) error {
	body := make([]byte, 4)
	binary.LittleEndian.PutUint16(body[0:2], 1) // protocol version
	binary.LittleEndian.PutUint16(body[2:4], 0) // options
	hdr := EncapHeader{Command: CmdRegisterSession}
	defer arm(ctx, c.tcp)()
	if _, err := c.tcp.Write(EncodeFrame(hdr, body)); err != nil {
		return cancelled(ctx, err)
	}
	rhdr, _, err := c.readFrame()
	if err != nil {
		return cancelled(ctx, err)
	}
	if rhdr.Status != EncapStatusSuccess {
		return fmt.Errorf("register session: status 0x%x", rhdr.Status)
	}
	c.sessHdl = rhdr.SessionHandle
	return nil
}

// SendRRData sends an unconnected message (UCMM) and returns the inner
// MessageRouterResponse. The path/service/data are wrapped in an
// UnconnectedData CPF item with a NullAddress.
func (c *Client) SendRRData(ctx context.Context, service uint8, path []byte, data []byte) (MessageRouterResponse, error) {
	mr := encodeMRRequest(service, path, data)
	items := []CPFItem{
		{TypeID: ItemNullAddress, Data: nil},
		{TypeID: ItemUnconnectedData, Data: mr},
	}
	body := EncodeSendRRData(SendRRDataHeader{Timeout: 5}, items)
	return c.sendCommand(ctx, CmdSendRRData, body)
}

// sendCommand transmits a single encap command and parses the reply as a
// CPF-wrapped MessageRouterResponse from the second item.
func (c *Client) sendCommand(ctx context.Context, cmd uint16, data []byte) (MessageRouterResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tcp == nil {
		return MessageRouterResponse{}, errors.New("cip client: not connected")
	}
	atomic.AddUint64(&c.ctxToken, 1)
	hdr := EncapHeader{
		Command:       cmd,
		SessionHandle: c.sessHdl,
	}
	binary.LittleEndian.PutUint64(hdr.SenderContext[:], c.ctxToken)
	defer arm(ctx, c.tcp)()
	if _, err := c.tcp.Write(EncodeFrame(hdr, data)); err != nil {
		return MessageRouterResponse{}, cancelled(ctx, err)
	}
	rhdr, payload, err := c.readFrame()
	if err != nil {
		return MessageRouterResponse{}, cancelled(ctx, err)
	}
	if rhdr.Status != EncapStatusSuccess {
		return MessageRouterResponse{}, fmt.Errorf("encap status 0x%x", rhdr.Status)
	}
	_, items, err := DecodeSendRRData(payload)
	if err != nil {
		return MessageRouterResponse{}, err
	}
	if len(items) < 2 || items[1].TypeID != ItemUnconnectedData {
		return MessageRouterResponse{}, fmt.Errorf("unexpected reply CPF: %d items", len(items))
	}
	return decodeMRResponse(items[1].Data)
}

// requestTimeout bounds one request when its context has no sooner
// deadline.
const requestTimeout = 10 * time.Second

// arm bounds one request on conn: its context's deadline or requestTimeout,
// whichever comes first, and it ends the request at once when the context
// is cancelled (a driver's Stop), rather than leaving a read on a device
// that stopped answering to run out its deadline (#932). The returned
// func disarms the cancel; call it when the request is done.
func arm(ctx context.Context, conn net.Conn) (disarm func()) {
	deadline := time.Now().Add(requestTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	fired := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(fired)
		if testHookCancelFired != nil {
			testHookCancelFired()
		}
		_ = conn.SetDeadline(time.Now())
	})
	return func() {
		if !stop() {
			// The cancel came as the request finished: let its
			// SetDeadline(now) land before returning, or it could land on
			// the next request on this connection and fail it.
			<-fired
		}
	}
}

// testHookCancelFired, when set (by tests), runs in arm's cancel callback
// before it sets the deadline.
var testHookCancelFired func()

// cancelled says why a request failed when its context ended it: the
// cancel, not the deadline it was given.
func cancelled(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("%w (%v)", cerr, err)
	}
	return err
}

func (c *Client) readFrame() (EncapHeader, []byte, error) {
	hdrBuf := make([]byte, EncapHeaderLen)
	if _, err := io.ReadFull(c.tcp, hdrBuf); err != nil {
		return EncapHeader{}, nil, err
	}
	var h EncapHeader
	if err := h.UnmarshalBinary(hdrBuf); err != nil {
		return h, nil, err
	}
	var body []byte
	if h.Length > 0 {
		body = make([]byte, h.Length)
		if _, err := io.ReadFull(c.tcp, body); err != nil {
			return h, nil, err
		}
	}
	return h, body, nil
}

// encodeMRRequest builds the on-wire MR request: service / pathWords / path /
// data. Inverse of DecodeMRRequest.
func encodeMRRequest(service uint8, path []byte, data []byte) []byte {
	pathWords := len(path) / 2
	out := make([]byte, 2+len(path)+len(data))
	out[0] = service
	out[1] = uint8(pathWords)
	copy(out[2:2+len(path)], path)
	copy(out[2+len(path):], data)
	return out
}

func decodeMRResponse(b []byte) (MessageRouterResponse, error) {
	var r MessageRouterResponse
	if len(b) < 4 {
		return r, fmt.Errorf("mr response: too short (%d)", len(b))
	}
	r.Service = b[0] &^ ReplyBit
	r.GeneralStatus = b[2]
	extWords := int(b[3])
	off := 4
	if off+extWords*2 > len(b) {
		return r, fmt.Errorf("mr response: truncated ext status")
	}
	r.ExtStatus = make([]uint16, extWords)
	for i := 0; i < extWords; i++ {
		r.ExtStatus[i] = binary.LittleEndian.Uint16(b[off : off+2])
		off += 2
	}
	r.Data = b[off:]
	return r, nil
}

// ForwardOpenOriginator runs the Connection-Manager Forward_Open service to
// establish a Class 1 (cyclic I/O) connection to the target. The originator
// proposes connection IDs (typically derived from the connection serial); the
// target replaces them with its own assigned values in the response.
//
// The path is the routing/destination EPATH. For non-routed connections to
// an end device the typical shape is:
//
//	0x20 0x06   logical class    = Connection Manager
//	0x24 0x01   logical instance = 1
//	0x2C cfg    logical conn pt  = configuration assembly
//	0x2C oToT   logical conn pt  = O→T (output) assembly
//	0x2C tToO   logical conn pt  = T→O (input) assembly
func (c *Client) ForwardOpenOriginator(ctx context.Context, req ForwardOpenRequest, path []byte) (ForwardOpenResponse, error) {
	// Connection-Manager request: wrap the Forward_Open service body and
	// address it at class 6 / instance 1.
	cmPath := BuildPath(uint32(ClassConnectionManager), 1, 0)
	req.ConnectionPath = path
	body := EncodeForwardOpenRequest(req)
	svc := ServiceForwardOpen
	if req.IsLargeOpen {
		svc = ServiceLargeForwardOpen
	}
	mr, err := c.SendRRData(ctx, svc, cmPath, body)
	if err != nil {
		return ForwardOpenResponse{}, err
	}
	if mr.GeneralStatus != StatusSuccess {
		return ForwardOpenResponse{}, fmt.Errorf("forward_open refused: status 0x%02x ext=%v", mr.GeneralStatus, mr.ExtStatus)
	}
	return DecodeForwardOpenResponse(mr.Data)
}

// ForwardCloseOriginator releases a previously opened connection. Best-effort —
// scanners typically log and move on if Close fails (the target may have
// already torn down via timeout).
func (c *Client) ForwardCloseOriginator(ctx context.Context, connSerial, vendor uint16, origSerial uint32, path []byte) error {
	cmPath := BuildPath(uint32(ClassConnectionManager), 1, 0)
	body := make([]byte, 0, 12+len(path))
	body = append(body, 0x07, 0xFA) // priority/tick + timeout ticks (~2s)
	tmp := make([]byte, 8)
	binary.LittleEndian.PutUint16(tmp[0:2], connSerial)
	binary.LittleEndian.PutUint16(tmp[2:4], vendor)
	binary.LittleEndian.PutUint32(tmp[4:8], origSerial)
	body = append(body, tmp...)
	body = append(body, uint8(len(path)/2), 0x00)
	body = append(body, path...)
	mr, err := c.SendRRData(ctx, ServiceForwardClose, cmPath, body)
	if err != nil {
		return err
	}
	if mr.GeneralStatus != StatusSuccess {
		return fmt.Errorf("forward_close: status 0x%02x", mr.GeneralStatus)
	}
	return nil
}
