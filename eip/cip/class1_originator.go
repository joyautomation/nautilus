package cip

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Class1OriginatorConfig describes one Class 1 (cyclic I/O) connection the
// scanner wants to open against a target device.
type Class1OriginatorConfig struct {
	Host string
	Port int // default 44818

	// Connection points on the target — these come from the device's EDS.
	// CfgInst may be 0 to omit the configuration assembly.
	CfgInst  uint16
	OToTInst uint16 // output assembly (controller → device)
	TToOInst uint16 // input assembly  (device → controller)

	// Data sizes in bytes.
	OToTSize uint16
	TToOSize uint16

	// RPI (Requested Packet Interval) in microseconds for each direction.
	OToTRPIMicros uint32
	TToORPIMicros uint32

	// Vendor + serial identifying this originator. Use any unique value.
	OriginatorVendor uint16
	OriginatorSerial uint32

	// Transport class/trigger. 0x01 = class 1 cyclic, server is server.
	// Defaults to 0x01 if zero.
	TransportClassTrigger uint8

	// IsLargeOpen forces Large Forward_Open (service 0x5B). Required when
	// either size > 511 bytes.
	IsLargeOpen bool
}

// Class1Originator owns one CIP session + one Class 1 connection. It writes
// O→T frames at the negotiated API and invokes the caller's handler on each
// T→O frame received.
type Class1Originator struct {
	cfg    Class1OriginatorConfig
	log    *slog.Logger
	client *Client
	mux    *Class1Mux

	// Negotiated state.
	mu             sync.RWMutex
	oToTConnID     uint32
	tToOConnID     uint32
	oToTAPI        uint32 // actual packet interval the target chose
	tToOAPI        uint32
	connSerial     uint16
	targetUDPAddr  *net.UDPAddr
	producer       *Class1Producer
	producerCancel context.CancelFunc
	oToTPayload    []byte // last data we will send on O→T frames
}

// NewClass1Originator wires the components but does not connect.
func NewClass1Originator(cfg Class1OriginatorConfig, mux *Class1Mux, log *slog.Logger) *Class1Originator {
	if cfg.Port == 0 {
		cfg.Port = EIPPort
	}
	if cfg.TransportClassTrigger == 0 {
		cfg.TransportClassTrigger = 0x01
	}
	if cfg.OriginatorVendor == 0 {
		cfg.OriginatorVendor = 0x1337 // arbitrary
	}
	if cfg.OriginatorSerial == 0 {
		var b [4]byte
		_, _ = rand.Read(b[:])
		cfg.OriginatorSerial = binary.LittleEndian.Uint32(b[:])
	}
	if log == nil {
		log = slog.Default()
	}
	return &Class1Originator{
		cfg:    cfg,
		log:    log,
		client: NewClient(cfg.Host, cfg.Port, log),
		mux:    mux,
	}
}

// Open registers the session, sends Forward_Open, and begins the O→T
// producer + T→O consumer. onInputFrame is invoked from the mux goroutine on
// every T→O frame; keep it fast (publish/copy and return). getOutputData
// supplies the current O→T payload on each producer tick.
func (o *Class1Originator) Open(
	ctx context.Context,
	getOutputData func() []byte,
	onInputFrame func(data []byte),
) error {
	if err := o.client.Connect(ctx); err != nil {
		return err
	}

	// Build connection serial (16-bit; just use low bits of serial).
	o.connSerial = uint16(o.cfg.OriginatorSerial & 0xFFFF)

	req := ForwardOpenRequest{
		PriorityTimeTick:      0x0A, // ~1s tick scale (10 ticks of ~32ms)
		TimeoutTicks:          0x05,
		OToTNetworkConnID:     0, // target assigns
		TToONetworkConnID:     0, // target assigns
		ConnectionSerial:      o.connSerial,
		OriginatorVendorID:    o.cfg.OriginatorVendor,
		OriginatorSerialNum:   o.cfg.OriginatorSerial,
		ConnectionTimeoutMult: 0x02, // 4× the RPI before considering connection dead
		OToTRPIMicros:         o.cfg.OToTRPIMicros,
		OToTNetworkParams:     ForwardOpenNetworkParams(o.cfg.OToTSize, true, o.cfg.IsLargeOpen),
		TToORPIMicros:         o.cfg.TToORPIMicros,
		TToONetworkParams:     ForwardOpenNetworkParams(o.cfg.TToOSize, true, o.cfg.IsLargeOpen),
		TransportClassTrigger: o.cfg.TransportClassTrigger,
		IsLargeOpen:           o.cfg.IsLargeOpen,
	}

	path := BuildConnectionPath(o.cfg.CfgInst, o.cfg.OToTInst, o.cfg.TToOInst)
	resp, err := o.client.ForwardOpenOriginator(ctx, req, path)
	if err != nil {
		_ = o.client.Close()
		return err
	}

	targetUDP, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", o.cfg.Host, Class1Port))
	if err != nil {
		_ = o.client.Close()
		return err
	}

	o.mu.Lock()
	o.oToTConnID = resp.OToTNetworkConnID
	o.tToOConnID = resp.TToONetworkConnID
	o.oToTAPI = resp.OToTAPIMicros
	o.tToOAPI = resp.TToOAPIMicros
	o.targetUDPAddr = targetUDP
	o.mu.Unlock()

	// Register T→O consumer. The connID we listen for is the one the target
	// assigned for its T→O frames.
	o.mux.Register(resp.TToONetworkConnID, func(src *net.UDPAddr, data []byte) {
		onInputFrame(data)
	})

	// Start the O→T producer. Use a long-lived context tied to Close(), not
	// the caller's ctx (which often carries the Forward_Open timeout).
	prodCtx, prodCancel := context.WithCancel(context.Background())
	o.mu.Lock()
	o.producer = NewClass1Producer(resp.OToTNetworkConnID, resp.OToTAPIMicros, targetUDP, o.mux.Conn(), getOutputData, o.log)
	o.producerCancel = prodCancel
	o.mu.Unlock()
	go o.producer.Run(prodCtx)

	o.log.Info("class1 originator: opened",
		"target", o.cfg.Host,
		"oToT_id", resp.OToTNetworkConnID,
		"tToO_id", resp.TToONetworkConnID,
		"oToT_api_us", resp.OToTAPIMicros,
		"tToO_api_us", resp.TToOAPIMicros,
	)
	return nil
}

// Close releases the connection (Forward_Close + UnRegisterSession) and stops
// the producer/consumer. Safe to call once.
func (o *Class1Originator) Close(ctx context.Context) error {
	o.mu.RLock()
	tToO := o.tToOConnID
	prod := o.producer
	prodCancel := o.producerCancel
	connSerial := o.connSerial
	o.mu.RUnlock()

	if prodCancel != nil {
		prodCancel()
	}
	if prod != nil {
		prod.Stop()
	}
	if tToO != 0 {
		o.mux.Unregister(tToO)
	}
	if connSerial != 0 {
		// Send Forward_Close with the same path.
		path := BuildConnectionPath(o.cfg.CfgInst, o.cfg.OToTInst, o.cfg.TToOInst)
		fcCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_ = o.client.ForwardCloseOriginator(fcCtx, connSerial, o.cfg.OriginatorVendor, o.cfg.OriginatorSerial, path)
		cancel()
	}
	return o.client.Close()
}

// TToOConnID returns the target-assigned T→O connection ID (0 if not open).
func (o *Class1Originator) TToOConnID() uint32 {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.tToOConnID
}
