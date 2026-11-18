// Package plcnexttest is an in-process PLCnext controller for tests: the
// firmware's gRPC data-access service (GDS) with BusConductor behind it,
// served on a Unix socket the plcnext driver dials as a manifest's
// `socket:`. Its port names are spelled out here, independently of
// package plcnext (which it must not import: plcnext's in-package tests
// would cycle), so a test is checked against the bench, not against the
// driver's own constants.
package plcnexttest

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"

	arptypes "github.com/joyautomation/nautilus/plcnext/gen/arptypes"
	gdspb "github.com/joyautomation/nautilus/plcnext/gen/plc/gds"
)

// The ports the fake serves: a bench AXC F 2152's, under a manifest that
// leaves inputs, outputs, run-port and bus-conductor at their defaults.
const (
	Inputs    = "Arp.Plc.Eclr/PND_S1_INPUTS"
	Outputs   = "Arp.Plc.Eclr/PND_S1_OUTPUTS"
	RunPort   = "Arp.Plc.Eclr/AXIO_DIAG_STATUS_REG_RUN"
	Component = "BcComponent1"
	// ImageSize is the length of each process image, in bytes.
	ImageSize = 512
	// Modules is what BusConductor's NUM_MODULES reports.
	Modules = 4

	bc           = Component + "/BusConductor."
	configReq    = bc + "CONFIG_REQ"
	configMatch  = bc + "CONFIG_MUST_MATCH"
	startReq     = bc + "START_IO_REQ"
	configured   = bc + "CONFIGURED"
	numModules   = bc + "NUM_MODULES"
	socketName   = "grpc.sock"
	socketPrefix = "plcnext-gds"
)

// GDS is a PLCnext firmware's IDataAccessService with BusConductor behind
// it, as measured on the bench (FW 2023.0): a rising CONFIG_REQ latches
// CONFIGURED; a rising START_IO_REQ on a configured bus raises the
// Axioline RUN bit; the 512-byte images answer DAE_NotSupported to a
// whole-array name and serve one byte per `Port[i]` element; unknown
// ports answer DAE_NotExists. Every method is safe for concurrent use.
type GDS struct {
	gdspb.UnimplementedIDataAccessServiceServer

	mu         sync.Mutex
	bools      map[string]bool
	in, out    []byte
	configured bool
	faulted    bool // SetRun(false): START_IO_REQ does not raise RUN
	writes     []string

	requests atomic.Int64

	lnMu  sync.Mutex
	down  bool
	conns map[net.Conn]bool
}

// New is a bench controller with its bus not yet configured, as after a
// runtime restart: the driver's BusConductor handshake configures and
// starts it.
func New() *GDS {
	return &GDS{
		bools: map[string]bool{
			configReq: false, configMatch: false, startReq: false, RunPort: false,
		},
		in:    make([]byte, ImageSize),
		out:   make([]byte, ImageSize),
		conns: map[net.Conn]bool{},
	}
}

// Listen serves the fake on a Unix socket in a fresh short temp dir (a
// socket path has a limit near 108 bytes that t.TempDir's test-named
// path can pass) and returns the socket's path. The server stops, and
// the dir goes, when the test ends.
func (g *GDS) Listen(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", socketPrefix)
	if err != nil {
		t.Fatalf("plcnexttest: %v", err)
	}
	path := filepath.Join(dir, socketName)
	ln, err := net.Listen("unix", path)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("plcnexttest: %v", err)
	}
	srv := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		g.requests.Add(1)
		return h(ctx, req)
	}))
	gdspb.RegisterIDataAccessServiceServer(srv, g)
	go srv.Serve(&listener{Listener: ln, g: g})
	t.Cleanup(func() {
		srv.Stop()
		os.RemoveAll(dir)
	})
	return path
}

// listener refuses connections while the fake is dropped (accepts and
// closes them, as a socket with no runtime behind it) and tracks the live
// ones so Drop can close them.
type listener struct {
	net.Listener
	g *GDS
}

func (l *listener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		l.g.lnMu.Lock()
		if l.g.down {
			l.g.lnMu.Unlock()
			c.Close()
			continue
		}
		tc := &conn{Conn: c, g: l.g}
		l.g.conns[tc] = true
		l.g.lnMu.Unlock()
		return tc, nil
	}
}

type conn struct {
	net.Conn
	g *GDS
}

func (c *conn) Close() error {
	c.g.lnMu.Lock()
	delete(c.g.conns, c)
	c.g.lnMu.Unlock()
	return c.Conn.Close()
}

// Drop takes the controller away: every live connection closes and new
// ones are refused until Restore. The process image and the bus state are
// kept, as a firmware whose gRPC service went away and came back. A gRPC
// client redials a closed connection by itself, so a Restore before the
// driver's next call can go unnoticed: wait for the driver to see the drop
// (Health().Connected false) before restoring.
func (g *GDS) Drop() {
	g.lnMu.Lock()
	g.down = true
	cs := make([]net.Conn, 0, len(g.conns))
	for c := range g.conns {
		cs = append(cs, c)
	}
	g.lnMu.Unlock()
	for _, c := range cs {
		c.Close()
	}
}

// Restore accepts connections again after Drop.
func (g *GDS) Restore() {
	g.lnMu.Lock()
	g.down = false
	g.lnMu.Unlock()
}

// SetRun sets the Axioline RUN bit. false is a bus that stopped and will
// not start again (a module fault): a START_IO_REQ does not raise it until
// SetRun(true).
func (g *GDS) SetRun(v bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.bools[RunPort] = v
	g.faulted = !v
}

// Running reports the RUN bit.
func (g *GDS) Running() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.bools[RunPort]
}

// SetInput sets one byte of the input image.
func (g *GDS) SetInput(offset int, b byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.in[offset] = b
}

// Output is one byte of the output image.
func (g *GDS) Output(offset int) byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.out[offset]
}

// Writes is every write the fake took, in order, as "port=value" (a bool
// as true/false, a number in decimal).
func (g *GDS) Writes() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.writes...)
}

// Requests counts the gRPC calls served (reads and writes alike).
func (g *GDS) Requests() int64 { return g.requests.Load() }

// image is the byte image a port name is, or nil.
func (g *GDS) image(port string) []byte {
	switch port {
	case Inputs:
		return g.in
	case Outputs:
		return g.out
	}
	return nil
}

// element parses "Port[i]".
func element(name string) (string, int, bool) {
	if !strings.HasSuffix(name, "]") {
		return name, 0, false
	}
	i := strings.LastIndex(name, "[")
	if i < 0 {
		return name, 0, false
	}
	n, err := strconv.Atoi(name[i+1 : len(name)-1])
	if err != nil {
		return name, 0, false
	}
	return name[:i], n, true
}

func boolValue(b bool) *arptypes.ObjectType {
	return &arptypes.ObjectType{TypeCode: arptypes.CoreType_CT_Boolean, Value: &arptypes.ObjectType_BoolValue{BoolValue: b}}
}

func uint8Value(b byte) *arptypes.ObjectType {
	return &arptypes.ObjectType{TypeCode: arptypes.CoreType_CT_Uint8, Value: &arptypes.ObjectType_Uint8Value{Uint8Value: uint32(b)}}
}

// number is an integer ObjectType's value; the driver writes bytes as
// uint8.
func number(v *arptypes.ObjectType) (int64, bool) {
	switch x := v.GetValue().(type) {
	case *arptypes.ObjectType_Uint8Value:
		return int64(x.Uint8Value), true
	case *arptypes.ObjectType_Int8Value:
		return int64(x.Int8Value), true
	case *arptypes.ObjectType_Uint16Value:
		return int64(x.Uint16Value), true
	case *arptypes.ObjectType_Int16Value:
		return int64(x.Int16Value), true
	case *arptypes.ObjectType_Uint32Value:
		return int64(x.Uint32Value), true
	case *arptypes.ObjectType_Int32Value:
		return int64(x.Int32Value), true
	}
	return 0, false
}

// readOne is the firmware's resolution of one name; the caller holds mu.
func (g *GDS) readOne(name string) *gdspb.ReadItem {
	item := &gdspb.ReadItem{Error: gdspb.DataAccessError_DAE_None}
	switch name {
	case configured:
		item.Value = boolValue(g.configured)
		return item
	case numModules:
		item.Value = &arptypes.ObjectType{TypeCode: arptypes.CoreType_CT_Uint16, Value: &arptypes.ObjectType_Uint16Value{Uint16Value: Modules}}
		return item
	}
	if v, ok := g.bools[name]; ok {
		item.Value = boolValue(v)
		return item
	}
	if g.image(name) != nil {
		item.Error = gdspb.DataAccessError_DAE_NotSupported // whole arrays, as the real firmware
		return item
	}
	if port, i, ok := element(name); ok {
		if b := g.image(port); b != nil {
			if i < 0 || i >= len(b) {
				item.Error = gdspb.DataAccessError_DAE_IndexOutOfRange
			} else {
				item.Value = uint8Value(b[i])
			}
			return item
		}
	}
	item.Error = gdspb.DataAccessError_DAE_NotExists
	return item
}

// writeOne is the firmware's handling of one write; the caller holds mu.
func (g *GDS) writeOne(w *gdspb.WriteItem) gdspb.DataAccessError {
	port, v := w.GetPortName(), w.GetValue()
	if b, isBool := v.GetValue().(*arptypes.ObjectType_BoolValue); isBool {
		g.writes = append(g.writes, port+"="+strconv.FormatBool(b.BoolValue))
	} else {
		n, _ := number(v)
		g.writes = append(g.writes, port+"="+strconv.FormatInt(n, 10))
	}
	if was, ok := g.bools[port]; ok {
		now := v.GetBoolValue()
		g.bools[port] = now
		rising := now && !was
		switch {
		case port == configReq && rising:
			g.configured = true
		case port == startReq && rising && g.configured && !g.faulted:
			g.bools[RunPort] = true
		}
		return gdspb.DataAccessError_DAE_None
	}
	if g.image(port) != nil {
		return gdspb.DataAccessError_DAE_NotSupported
	}
	if p, i, ok := element(port); ok {
		if b := g.image(p); b != nil {
			if i < 0 || i >= len(b) {
				return gdspb.DataAccessError_DAE_IndexOutOfRange
			}
			n, ok := number(v)
			if !ok {
				return gdspb.DataAccessError_DAE_TypeMismatch
			}
			b[i] = byte(n)
			return gdspb.DataAccessError_DAE_None
		}
	}
	return gdspb.DataAccessError_DAE_NotExists
}

func (g *GDS) ReadSingle(_ context.Context, req *gdspb.IDataAccessServiceReadSingleRequest) (*gdspb.IDataAccessServiceReadSingleResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return &gdspb.IDataAccessServiceReadSingleResponse{XReturnValue: g.readOne(req.GetPortName())}, nil
}

func (g *GDS) Read(_ context.Context, req *gdspb.IDataAccessServiceReadRequest) (*gdspb.IDataAccessServiceReadResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*gdspb.ReadItem, len(req.GetPortNames()))
	for i, n := range req.GetPortNames() {
		out[i] = g.readOne(n)
	}
	return &gdspb.IDataAccessServiceReadResponse{XReturnValue: out}, nil
}

func (g *GDS) WriteSingle(_ context.Context, req *gdspb.IDataAccessServiceWriteSingleRequest) (*gdspb.IDataAccessServiceWriteSingleResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return &gdspb.IDataAccessServiceWriteSingleResponse{XReturnValue: g.writeOne(req.GetData())}, nil
}

func (g *GDS) Write(_ context.Context, req *gdspb.IDataAccessServiceWriteRequest) (*gdspb.IDataAccessServiceWriteResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]gdspb.DataAccessError, len(req.GetData()))
	for i, w := range req.GetData() {
		out[i] = g.writeOne(w)
	}
	return &gdspb.IDataAccessServiceWriteResponse{XReturnValue: out}, nil
}
