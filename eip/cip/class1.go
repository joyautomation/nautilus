package cip

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Class1Port is the UDP port for CIP Class 1 I/O messaging. Both originator
// and target use this port; addresses are negotiated via Forward_Open.
const Class1Port = 2222

// Class1Producer sends I/O frames from device → controller at the negotiated
// RPI. The producer holds one set of these per active connection.
//
// Wire layout of a Class 1 I/O frame (Vol 2 §3-2.1):
//
//	[u16 itemCount=2]
//	[CPF: SequencedAddress (8 bytes: connID + sequence)]
//	[CPF: ConnectedData (header + data)]
//
// The ConnectedData payload is:
//
//	[u16 cipSequenceCount]      // increments per frame
//	[data bytes]                // the assembly data
//	[u16 modifier=0]            // run/idle header — bit 0 = Run mode
//
// Real-T2O frames may include 32-bit "real-time header" before the data
// (transport class 1, fixed/variable types — see Vol 1 §3-4.4). For PF70
// basic speed control we use the simpler header-less form: scanners accept
// it as long as we keep the sequence counter monotonic.
type Class1Producer struct {
	connID    uint32 // T→O connection ID assigned during Forward_Open
	rpiMicros uint32
	getData   func() []byte
	dst       *net.UDPAddr
	conn      *net.UDPConn

	sequence uint32
	cipSeq   uint16
	log      *slog.Logger

	stop chan struct{}
	done chan struct{}
}

// NewClass1Producer creates a producer. The caller must hold a UDPConn bound
// to 0.0.0.0:2222 — typically the same one used by the consumer half. The
// producer doesn't own the socket, just sends through it.
func NewClass1Producer(connID uint32, rpiMicros uint32, dst *net.UDPAddr, conn *net.UDPConn, getData func() []byte, log *slog.Logger) *Class1Producer {
	return &Class1Producer{
		connID:    connID,
		rpiMicros: rpiMicros,
		dst:       dst,
		conn:      conn,
		getData:   getData,
		log:       log,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Run sends frames at the RPI cadence until Stop is called or ctx cancels.
// Blocks; call in its own goroutine.
func (p *Class1Producer) Run(ctx context.Context) {
	defer close(p.done)
	if p.rpiMicros == 0 {
		p.rpiMicros = 10000 // 10ms safety default
	}
	period := time.Duration(p.rpiMicros) * time.Microsecond
	t := time.NewTicker(period)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stop:
			return
		case <-t.C:
			p.sendOne()
		}
	}
}

// Stop ends the producer. Idempotent.
func (p *Class1Producer) Stop() {
	select {
	case <-p.stop:
		// already stopped
	default:
		close(p.stop)
	}
	<-p.done
}

func (p *Class1Producer) sendOne() {
	data := p.getData()

	// CPF item 1: SequencedAddress — connID + 32-bit sequence count (Vol 2
	// §3-2.4). Some implementations use ConnectedAddress (just connID), but
	// SequencedAddress with a monotonic counter is what real Logix uses and
	// what scanners expect.
	addrItem := make([]byte, 8)
	binary.LittleEndian.PutUint32(addrItem[0:4], p.connID)
	atomic.AddUint32(&p.sequence, 1)
	binary.LittleEndian.PutUint32(addrItem[4:8], atomic.LoadUint32(&p.sequence))

	// CPF item 2: ConnectedData — CIP sequence count + assembly data +
	// run/idle modifier word.
	dataItem := make([]byte, 2+len(data)+2)
	p.cipSeq++
	binary.LittleEndian.PutUint16(dataItem[0:2], p.cipSeq)
	copy(dataItem[2:2+len(data)], data)
	// Modifier: bit 0 = Run/Idle. We always assert Run; idle is for
	// scanners that haven't loaded their AOI yet.
	binary.LittleEndian.PutUint16(dataItem[2+len(data):], 0x0001)

	items := []CPFItem{
		{TypeID: ItemSequencedAddress, Data: addrItem},
		{TypeID: ItemConnectedData, Data: dataItem},
	}
	// Class 1 frames have no encapsulation header — just the CPF list
	// directly on UDP (Vol 2 §3-2.1).
	frame := EncodeCPF(items)
	_ = p.conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := p.conn.WriteToUDP(frame, p.dst); err != nil {
		if p.log != nil {
			p.log.Debug("class1: send failed", "dst", p.dst, "error", err)
		}
	}
}

// Class1Consumer accepts incoming O→T frames on the shared UDP socket and
// dispatches the payload to a handler. There is one consumer per connection
// (or rather: the manager routes incoming frames to the right consumer based
// on the connection ID in the SequencedAddress item).
//
// The handler receives the source address of the first frame so the T→O
// producer knows where to send replies. Subsequent calls may pass the same
// address; the handler is expected to capture it once.
type Class1Consumer struct {
	connID  uint32
	handler func(src *net.UDPAddr, data []byte)
}

// Class1Mux owns the UDP socket on port 2222 and demultiplexes incoming
// O→T frames to registered consumers. It also serves as the originator-side
// destination address provider for producers.
type Class1Mux struct {
	conn *net.UDPConn
	log  *slog.Logger

	mu        sync.RWMutex
	consumers map[uint32]*Class1Consumer

	cancel context.CancelFunc
	done   chan struct{}
}

// NewClass1Mux binds the UDP socket and returns the mux.
func NewClass1Mux(bindAddr string, log *slog.Logger) (*Class1Mux, error) {
	if bindAddr == "" {
		bindAddr = ":2222"
	}
	if log == nil {
		log = slog.Default()
	}
	udpAddr, err := net.ResolveUDPAddr("udp", bindAddr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}
	return &Class1Mux{
		conn:      conn,
		log:       log,
		consumers: make(map[uint32]*Class1Consumer),
		done:      make(chan struct{}),
	}, nil
}

// Run loops reading frames and dispatching. Call in a goroutine; cancel
// the context to stop.
func (m *Class1Mux) Run(ctx context.Context) {
	defer close(m.done)
	ctx, m.cancel = context.WithCancel(ctx)

	buf := make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return
		}
		_ = m.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, src, err := m.conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			m.log.Debug("class1mux: read", "error", err)
			continue
		}
		m.dispatch(src, buf[:n])
	}
}

func (m *Class1Mux) dispatch(src *net.UDPAddr, frame []byte) {
	items, err := DecodeCPF(frame)
	if err != nil || len(items) < 2 {
		return
	}
	// Item 0 is SequencedAddress (with seq) or ConnectedAddress (without).
	var connID uint32
	switch items[0].TypeID {
	case ItemSequencedAddress:
		if len(items[0].Data) < 4 {
			return
		}
		connID = binary.LittleEndian.Uint32(items[0].Data[0:4])
	case ItemConnectedAddress:
		if len(items[0].Data) < 4 {
			return
		}
		connID = binary.LittleEndian.Uint32(items[0].Data[0:4])
	default:
		return
	}
	if items[1].TypeID != ItemConnectedData {
		return
	}
	// Strip the 2-byte CIP sequence count prefix and the 4-byte run/idle
	// header that scanners *sometimes* include. Heuristic: if the data
	// length doesn't match what we expect for our O→T size, skip header
	// bytes. For simplicity right now, strip the 2-byte sequence; the
	// caller is responsible for tolerating the rest.
	payload := items[1].Data
	if len(payload) < 2 {
		return
	}
	data := payload[2:]
	// Trim trailing run/idle modifier if present (last 2 bytes).
	// Real scanners usually include it; we don't actually need it.

	m.mu.RLock()
	c, ok := m.consumers[connID]
	m.mu.RUnlock()
	if !ok {
		return
	}
	c.handler(src, data)
}

// Register attaches a consumer for a given connection ID. The handler is
// invoked on every O→T frame for that connection. The src address is the
// originator's UDP endpoint — the target should send T→O frames there.
func (m *Class1Mux) Register(connID uint32, handler func(src *net.UDPAddr, data []byte)) {
	m.mu.Lock()
	m.consumers[connID] = &Class1Consumer{connID: connID, handler: handler}
	m.mu.Unlock()
}

// Unregister removes a consumer.
func (m *Class1Mux) Unregister(connID uint32) {
	m.mu.Lock()
	delete(m.consumers, connID)
	m.mu.Unlock()
}

// Conn returns the underlying UDP connection so producers can share it for
// outbound sends.
func (m *Class1Mux) Conn() *net.UDPConn { return m.conn }

// Stop closes the mux.
func (m *Class1Mux) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	_ = m.conn.Close()
	<-m.done
}
