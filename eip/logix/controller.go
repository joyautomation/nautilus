package logix

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"

	"github.com/joyautomation/nautilus/eip/cip"
)

// connectionBytes is the Class 3 connection size we negotiate — the standard
// (non-large) Forward_Open maximum. Requests and replies, including Multiple
// Service Packet batches, must fit within it.
const connectionBytes = 500

// maxPayload is the conservative per-message budget for MR request/response
// bodies after connected-messaging overhead (sequence word, MR header).
const maxPayload = connectionBytes - 20

// Controller is one connected Logix controller: a registered EtherNet/IP
// session plus a Class 3 (connected explicit messaging) connection routed to
// the processor slot. Methods are safe for concurrent use; requests are
// serialized on the underlying TCP session.
//
// A Controller does not auto-reconnect: any transport error leaves it broken
// and every subsequent call fails. Callers (the eip driver) detect this and
// Dial a fresh Controller.
type Controller struct {
	host string
	port int
	slot int
	log  *slog.Logger

	mu         sync.Mutex
	cli        *cip.Client
	connID     uint32 // O→T network connection ID (target-assigned)
	seq        uint16
	connSerial uint16
	origSerial uint32
	broken     bool
}

// Option configures Dial.
type Option func(*Controller)

// WithPort overrides the EtherNet/IP TCP port (default 44818).
func WithPort(p int) Option { return func(c *Controller) { c.port = p } }

// WithSlot sets the processor's backplane slot (default 0 — correct for
// CompactLogix and single-slot ControlLogix layouts).
func WithSlot(s int) Option { return func(c *Controller) { c.slot = s } }

// WithLogger sets the structured logger (default slog.Default()).
func WithLogger(l *slog.Logger) Option { return func(c *Controller) { c.log = l } }

// Dial opens a session to the controller at host and establishes the Class 3
// connection used for all tag services.
func Dial(ctx context.Context, host string, opts ...Option) (*Controller, error) {
	c := &Controller{host: host, port: cip.EIPPort, slot: 0, log: slog.Default()}
	for _, o := range opts {
		o(c)
	}
	c.cli = cip.NewClient(c.host, c.port, c.log)
	if err := c.cli.Connect(ctx); err != nil {
		return nil, err
	}
	if err := c.forwardOpen(ctx); err != nil {
		_ = c.cli.Close()
		return nil, err
	}
	return c, nil
}

// forwardOpen negotiates the Class 3 connection: routing path is backplane
// port 1 → processor slot, endpoint is the Message Router (class 0x02).
func (c *Controller) forwardOpen(ctx context.Context) error {
	c.connSerial = uint16(rand.Uint32())
	c.origSerial = rand.Uint32()

	// Routing: [port 1, link <slot>] then Message Router class/instance.
	path := append([]byte{0x01, byte(c.slot)}, cip.BuildPath(uint32(cip.ClassMessageRouter), 1, 0)...)

	// Network params 0x43F4: point-to-point, low priority, variable size,
	// 500 bytes — the value pycomm3 uses for connected explicit messaging.
	req := cip.ForwardOpenRequest{
		PriorityTimeTick:      0x0A,
		TimeoutTicks:          0x05,
		TToONetworkConnID:     rand.Uint32(),
		ConnectionSerial:      c.connSerial,
		OriginatorVendorID:    0x004E, // 'N' — nautilus (not a registered ODVA vendor)
		OriginatorSerialNum:   c.origSerial,
		ConnectionTimeoutMult: 3, // ×16 → 32 s at 2 s RPI
		OToTRPIMicros:         2_000_000,
		OToTNetworkParams:     0x43F4,
		TToORPIMicros:         2_000_000,
		TToONetworkParams:     0x43F4,
		TransportClassTrigger: 0xA3, // class 3, application trigger, server
	}
	resp, err := c.cli.ForwardOpenOriginator(ctx, req, path)
	if err != nil {
		return fmt.Errorf("logix: forward_open to %s slot %d: %w", c.host, c.slot, err)
	}
	c.connID = resp.OToTNetworkConnID
	c.seq = 0
	c.log.Debug("logix: connected", "host", c.host, "slot", c.slot, "connID", c.connID)
	return nil
}

// Close releases the Class 3 connection and the session. Best-effort.
func (c *Controller) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cli == nil {
		return nil
	}
	if !c.broken {
		path := append([]byte{0x01, byte(c.slot)}, cip.BuildPath(uint32(cip.ClassMessageRouter), 1, 0)...)
		_ = c.cli.ForwardCloseOriginator(context.Background(), c.connSerial, 0x004E, c.origSerial, path)
	}
	err := c.cli.Close()
	c.cli = nil
	return err
}

// Broken reports whether the connection has seen a transport error and needs
// to be re-dialed.
func (c *Controller) Broken() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.broken
}

// sendConnected issues one connected explicit request and returns the MR
// response. CIP-level errors (non-zero general status) are returned to the
// caller embedded in the response, not as Go errors — several services use
// status 0x06 (partial transfer) as a continuation signal.
func (c *Controller) sendConnected(ctx context.Context, service uint8, path, data []byte) (cip.MessageRouterResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cli == nil || c.broken {
		return cip.MessageRouterResponse{}, fmt.Errorf("logix: connection to %s is down", c.host)
	}
	c.seq++
	resp, err := c.cli.SendUnitData(ctx, c.connID, c.seq, service, path, data)
	if err != nil {
		c.broken = true
		return cip.MessageRouterResponse{}, fmt.Errorf("logix: %s: %w", c.host, err)
	}
	return resp, nil
}

// RawTag is an undecoded tag read: the leading CIP type code, the structure
// handle when Type is cip.TypeStruct (0x02A0), and the raw value bytes.
type RawTag struct {
	Type   uint16
	Handle uint16
	Data   []byte
}

// splitReadReply peels the type prefix off a Read Tag reply body.
func splitReadReply(b []byte) (RawTag, error) {
	if len(b) < 2 {
		return RawTag{}, fmt.Errorf("logix: read reply too short (%d bytes)", len(b))
	}
	r := RawTag{Type: binary.LittleEndian.Uint16(b[:2])}
	rest := b[2:]
	if r.Type == cip.TypeStruct {
		if len(rest) < 2 {
			return RawTag{}, fmt.Errorf("logix: struct read reply missing handle")
		}
		r.Handle = binary.LittleEndian.Uint16(rest[:2])
		rest = rest[2:]
	}
	r.Data = rest
	return r, nil
}

// ReadTag reads count elements of a tag (count 1 for scalars and whole
// structs). Falls back to Read Tag Fragmented transparently when the value
// exceeds the connection size.
func (c *Controller) ReadTag(ctx context.Context, tag string, count uint16) (RawTag, error) {
	path, err := EncodeTagPath(tag)
	if err != nil {
		return RawTag{}, err
	}
	data := make([]byte, 2)
	binary.LittleEndian.PutUint16(data, count)
	resp, err := c.sendConnected(ctx, cip.ServiceReadTag, path, data)
	if err != nil {
		return RawTag{}, err
	}
	switch resp.GeneralStatus {
	case cip.StatusSuccess:
		return splitReadReply(resp.Data)
	case cip.StatusPartialTransfer, cip.StatusReplyTooLarge:
		return c.readTagFragmented(ctx, path, tag, count)
	default:
		return RawTag{}, statusError("read", tag, resp)
	}
}

// readTagFragmented reassembles a large value with Read Tag Fragmented:
// request data is [u16 count][u32 byte offset]; each reply repeats the type
// (and struct handle) prefix, which is kept once and stripped from
// continuation chunks.
func (c *Controller) readTagFragmented(ctx context.Context, path []byte, tag string, count uint16) (RawTag, error) {
	var out RawTag
	offset := uint32(0)
	for chunk := 0; ; chunk++ {
		data := make([]byte, 6)
		binary.LittleEndian.PutUint16(data[0:2], count)
		binary.LittleEndian.PutUint32(data[2:6], offset)
		resp, err := c.sendConnected(ctx, cip.ServiceReadTagFragmented, path, data)
		if err != nil {
			return RawTag{}, err
		}
		if resp.GeneralStatus != cip.StatusSuccess && resp.GeneralStatus != cip.StatusPartialTransfer {
			return RawTag{}, statusError("read fragmented", tag, resp)
		}
		part, err := splitReadReply(resp.Data)
		if err != nil {
			return RawTag{}, err
		}
		if chunk == 0 {
			out.Type, out.Handle = part.Type, part.Handle
		}
		out.Data = append(out.Data, part.Data...)
		offset += uint32(len(part.Data))
		if resp.GeneralStatus == cip.StatusSuccess {
			return out, nil
		}
		if len(part.Data) == 0 {
			return RawTag{}, fmt.Errorf("logix: read fragmented %q: empty partial chunk", tag)
		}
	}
}

// WriteTag writes count elements of an elementary-typed tag. typeCode is the
// CIP elementary code (0xC1 BOOL … 0xCB LREAL); data is the little-endian
// value bytes. Struct-typed leaf members are written by addressing the leaf
// symbolically (e.g. "Motor.Cmd.Speed") with its elementary type.
func (c *Controller) WriteTag(ctx context.Context, tag string, typeCode uint16, count uint16, data []byte) error {
	path, err := EncodeTagPath(tag)
	if err != nil {
		return err
	}
	body := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint16(body[0:2], typeCode)
	binary.LittleEndian.PutUint16(body[2:4], count)
	copy(body[4:], data)
	resp, err := c.sendConnected(ctx, cip.ServiceWriteTag, path, body)
	if err != nil {
		return err
	}
	if resp.GeneralStatus != cip.StatusSuccess {
		return statusError("write", tag, resp)
	}
	return nil
}

// ReadResult pairs one tag of a batch read with its outcome.
type ReadResult struct {
	Tag string
	RawTag
	Err error
}

// ReadTags reads a set of tags, batching them into Multiple Service Packets
// sized to the connection. Tags whose expected reply size (estimate via
// expectedSize, 0 = unknown) would overflow a batch are read individually.
// The result slice is parallel to tags.
func (c *Controller) ReadTags(ctx context.Context, tags []string, expectedSize func(tag string) int) []ReadResult {
	results := make([]ReadResult, len(tags))
	mrPath := cip.BuildPath(uint32(cip.ClassMessageRouter), 1, 0)

	type entry struct {
		i    int
		path []byte
		size int // estimated reply bytes incl. type prefix + MSP overhead
	}
	var batchable []entry
	for i, t := range tags {
		results[i].Tag = t
		path, err := EncodeTagPath(t)
		if err != nil {
			results[i].Err = err
			continue
		}
		est := 0
		if expectedSize != nil {
			est = expectedSize(t)
		}
		if est <= 0 || est+8 > maxPayload/2 {
			// Unknown or large: read individually (fragmented fallback applies).
			raw, err := c.ReadTag(ctx, t, 1)
			results[i].RawTag, results[i].Err = raw, err
			continue
		}
		batchable = append(batchable, entry{i: i, path: path, size: est + 8})
	}

	var batch []entry
	reqBytes, respBytes := 2, 2 // MSP count words
	flush := func() {
		if len(batch) == 0 {
			return
		}
		reqs := make([]cip.EncodedMRRequest, len(batch))
		for j, e := range batch {
			data := []byte{1, 0} // element count 1
			reqs[j] = cip.EncodedMRRequest{Service: cip.ServiceReadTag, Path: e.path, Data: data}
		}
		resp, err := c.sendConnected(ctx, cip.ServiceMultipleServicePkt, mrPath, cip.EncodeMultipleServiceRequest(reqs))
		if err != nil {
			for _, e := range batch {
				results[e.i].Err = err
			}
			batch, reqBytes, respBytes = nil, 2, 2
			return
		}
		// Outer status 0x1E means "some sub-service failed" — sub-responses
		// still decode individually.
		subs, derr := cip.DecodeMultipleServiceResponse(resp.Data)
		if derr != nil || len(subs) != len(batch) {
			if derr == nil {
				derr = fmt.Errorf("logix: multi-service reply count %d != %d", len(subs), len(batch))
			}
			for _, e := range batch {
				results[e.i].Err = derr
			}
			batch, reqBytes, respBytes = nil, 2, 2
			return
		}
		for j, e := range batch {
			sub := subs[j]
			switch sub.GeneralStatus {
			case cip.StatusSuccess:
				results[e.i].RawTag, results[e.i].Err = mustSplit(sub.Data)
			case cip.StatusPartialTransfer, cip.StatusReplyTooLarge:
				// Element too big for the batch after all — reread solo.
				raw, rerr := c.ReadTag(ctx, results[e.i].Tag, 1)
				results[e.i].RawTag, results[e.i].Err = raw, rerr
			default:
				results[e.i].Err = statusError("read", results[e.i].Tag, sub)
			}
		}
		batch, reqBytes, respBytes = nil, 2, 2
	}

	for _, e := range batchable {
		req := 2 + 2 + len(e.path) + 2 + 2 // offset word + MR hdr + path + data
		if reqBytes+req > maxPayload || respBytes+e.size > maxPayload {
			flush()
		}
		batch = append(batch, e)
		reqBytes += req
		respBytes += e.size
	}
	flush()
	return results
}

func mustSplit(b []byte) (RawTag, error) {
	return splitReadReply(b)
}

// CIPError is a request the controller answered with a non-success general
// status. Callers can inspect Status to distinguish permanent conditions
// (bad path, access denied) from transient ones.
type CIPError struct {
	Op     string
	Tag    string
	Status uint8
	Ext    []uint16
}

func (e *CIPError) Error() string {
	if len(e.Ext) > 0 {
		return fmt.Sprintf("logix: %s %q: CIP status 0x%02x ext %04x", e.Op, e.Tag, e.Status, e.Ext)
	}
	return fmt.Sprintf("logix: %s %q: CIP status 0x%02x", e.Op, e.Tag, e.Status)
}

// Permanent reports whether retrying the same request can't succeed while
// the controller program stays unchanged: the path doesn't resolve (0x04,
// 0x05) or access is denied (0x0F).
func (e *CIPError) Permanent() bool {
	switch e.Status {
	case cip.StatusPathSegmentError, cip.StatusPathDestUnknown, cip.StatusPrivilegeViolation:
		return true
	}
	return false
}

// statusError wraps a CIP error status with its extended words.
func statusError(op, tag string, r cip.MessageRouterResponse) error {
	return &CIPError{Op: op, Tag: tag, Status: r.GeneralStatus, Ext: r.ExtStatus}
}
