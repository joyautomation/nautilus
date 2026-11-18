package plcnexttest_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	arptypes "github.com/joyautomation/nautilus/plcnext/gen/arptypes"
	gdspb "github.com/joyautomation/nautilus/plcnext/gen/plc/gds"
	"github.com/joyautomation/nautilus/plcnext/plcnexttest"
)

// The fake's own answers, pinned at the wire, so a change to it shows here
// and not only as a driver test that went odd.

func client(t *testing.T, g *plcnexttest.GDS) gdspb.IDataAccessServiceClient {
	t.Helper()
	conn, err := grpc.NewClient("unix://"+g.Listen(t), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return gdspb.NewIDataAccessServiceClient(conn)
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func read(t *testing.T, c gdspb.IDataAccessServiceClient, names ...string) []*gdspb.ReadItem {
	t.Helper()
	r, err := c.Read(ctx(t), &gdspb.IDataAccessServiceReadRequest{PortNames: names})
	if err != nil {
		t.Fatal(err)
	}
	return r.GetXReturnValue()
}

func boolean(v bool) *arptypes.ObjectType {
	return &arptypes.ObjectType{TypeCode: arptypes.CoreType_CT_Boolean, Value: &arptypes.ObjectType_BoolValue{BoolValue: v}}
}

func u8(v byte) *arptypes.ObjectType {
	return &arptypes.ObjectType{TypeCode: arptypes.CoreType_CT_Uint8, Value: &arptypes.ObjectType_Uint8Value{Uint8Value: uint32(v)}}
}

func write(t *testing.T, c gdspb.IDataAccessServiceClient, port string, v *arptypes.ObjectType) gdspb.DataAccessError {
	t.Helper()
	r, err := c.Write(ctx(t), &gdspb.IDataAccessServiceWriteRequest{Data: []*gdspb.WriteItem{{PortName: port, Value: v}}})
	if err != nil {
		t.Fatal(err)
	}
	return r.GetXReturnValue()[0]
}

func TestTheFakesAnswers(t *testing.T) {
	g := plcnexttest.New()
	c := client(t, g)
	const (
		in  = plcnexttest.Inputs
		out = plcnexttest.Outputs
		bc  = plcnexttest.Component + "/BusConductor."
	)
	g.SetInput(3, 0xA5)

	for _, tc := range []struct {
		name string
		want gdspb.DataAccessError
	}{
		{in, gdspb.DataAccessError_DAE_NotSupported}, // a whole array, as the firmware
		{in + "[3]", gdspb.DataAccessError_DAE_None},
		{in + "[511]", gdspb.DataAccessError_DAE_None},
		{in + "[512]", gdspb.DataAccessError_DAE_IndexOutOfRange},
		{in + "[-1]", gdspb.DataAccessError_DAE_IndexOutOfRange},
		{"Arp.Plc.Eclr/NO_SUCH_PORT", gdspb.DataAccessError_DAE_NotExists},
		{plcnexttest.RunPort, gdspb.DataAccessError_DAE_None},
	} {
		if got := read(t, c, tc.name)[0].GetError(); got != tc.want {
			t.Errorf("read %s: %s, want %s", tc.name, got, tc.want)
		}
	}
	if v := read(t, c, in+"[3]")[0].GetValue().GetUint8Value(); v != 0xA5 {
		t.Errorf("%s[3] = %#x, want 0xa5", in, v)
	}
	if n := read(t, c, bc+"NUM_MODULES")[0].GetValue().GetUint16Value(); n != plcnexttest.Modules {
		t.Errorf("NUM_MODULES = %d, want %d", n, plcnexttest.Modules)
	}
	g.SetModules(5)
	if n := read(t, c, bc+"NUM_MODULES")[0].GetValue().GetUint16Value(); n != 5 {
		t.Errorf("NUM_MODULES after SetModules(5) = %d", n)
	}

	for _, tc := range []struct {
		port string
		v    *arptypes.ObjectType
		want gdspb.DataAccessError
	}{
		{out + "[1]", u8(7), gdspb.DataAccessError_DAE_None},
		{out + "[1]", boolean(true), gdspb.DataAccessError_DAE_TypeMismatch},
		{out, u8(1), gdspb.DataAccessError_DAE_NotSupported},
		{out + "[512]", u8(1), gdspb.DataAccessError_DAE_IndexOutOfRange},
		{"Arp.Plc.Eclr/NO_SUCH_PORT", u8(1), gdspb.DataAccessError_DAE_NotExists},
	} {
		if got := write(t, c, tc.port, tc.v); got != tc.want {
			t.Errorf("write %s: %s, want %s", tc.port, got, tc.want)
		}
	}
	if b := g.Output(1); b != 7 {
		t.Errorf("Output(1) = %d, want 7", b)
	}
	want := []string{out + "[1]=7", out + "[1]=true", out + "=1", out + "[512]=1", "Arp.Plc.Eclr/NO_SUCH_PORT=1"}
	if got := g.Writes(); !slices.Equal(got, want) {
		t.Errorf("Writes() = %q, want %q", got, want)
	}

	// SetPortError: the port and its elements, until cleared.
	g.SetPortError(in, gdspb.DataAccessError_DAE_NotExists)
	if got := read(t, c, in+"[3]")[0].GetError(); got != gdspb.DataAccessError_DAE_NotExists {
		t.Errorf("read %s[3] with the port's error set: %s", in, got)
	}
	g.SetPortError(out, gdspb.DataAccessError_DAE_NotSupported)
	if got := write(t, c, out+"[0]", u8(1)); got != gdspb.DataAccessError_DAE_NotSupported {
		t.Errorf("write %s[0] with the port's error set: %s", out, got)
	}
	g.SetPortError(in, gdspb.DataAccessError_DAE_None)
	if got := read(t, c, in+"[3]")[0].GetError(); got != gdspb.DataAccessError_DAE_None {
		t.Errorf("read %s[3] after clearing: %s", in, got)
	}
}

// BusConductor: rising edges, not levels; Restart puts it back.
func TestTheFakesBusConductor(t *testing.T) {
	g := plcnexttest.New()
	c := client(t, g)
	bc := plcnexttest.Component + "/BusConductor."
	flag := func(port string) bool { return read(t, c, port)[0].GetValue().GetBoolValue() }

	write(t, c, bc+"START_IO_REQ", boolean(true))
	if g.Running() {
		t.Fatal("START_IO_REQ started an unconfigured bus")
	}
	write(t, c, bc+"CONFIG_REQ", boolean(true))
	if !flag(bc + "CONFIGURED") {
		t.Fatal("a rising CONFIG_REQ did not configure the bus")
	}
	write(t, c, bc+"START_IO_REQ", boolean(true)) // still high: no edge
	if g.Running() {
		t.Fatal("START_IO_REQ held high started the bus: only a rising edge does")
	}
	write(t, c, bc+"START_IO_REQ", boolean(false))
	write(t, c, bc+"START_IO_REQ", boolean(true))
	if !g.Running() || !flag(plcnexttest.RunPort) {
		t.Fatal("a rising START_IO_REQ on a configured bus did not start it")
	}

	g.Restart()
	if g.Running() || flag(bc+"CONFIGURED") || flag(bc+"START_IO_REQ") {
		t.Fatal("after Restart the bus should be unconfigured, stopped, its flags low")
	}
	g.SetConfigured(true)
	if !flag(bc + "CONFIGURED") {
		t.Fatal("SetConfigured(true) not read back")
	}
}

// errs records Errorf instead of failing; everything else is the real test.
type errs struct {
	testing.TB
	mu  sync.Mutex
	got []string
}

func (e *errs) Errorf(format string, args ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, fmt.Sprintf(format, args...))
}

// An offset outside an image fails the test that listened, not with a
// panic; before Listen there is no test to fail, so it panics.
func TestAnOffsetOutsideTheImageFailsTheTest(t *testing.T) {
	g := plcnexttest.New()
	rec := &errs{TB: t}
	g.Listen(rec)
	g.SetInput(plcnexttest.ImageSize, 1)
	if b := g.Output(-1); b != 0 {
		t.Errorf("Output(-1) = %d, want 0", b)
	}
	if len(rec.got) != 2 {
		t.Fatalf("reported %q, want two misuse errors", rec.got)
	}

	defer func() {
		if recover() == nil {
			t.Error("SetInput out of range before Listen did not panic")
		}
	}()
	plcnexttest.New().SetInput(-1, 0)
}
