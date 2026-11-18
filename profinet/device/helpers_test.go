package device_test

import (
	"net"
	"testing"

	. "github.com/joyautomation/nautilus/profinet/device"
	"github.com/joyautomation/nautilus/profinet/pntest"
	"github.com/joyautomation/nautilus/profinet/wire"
)

// tctl wraps the test controller so a failed exchange fails the test.
type tctl struct {
	*pntest.Controller
	t *testing.T
}

func (c tctl) identify(name string) (string, net.IP) { return c.Identify(name) }

func (c tctl) connect(t *testing.T, subs []Subslot, twice bool) []wire.DiffModule {
	t.Helper()
	st, diff, err := c.Connect(subs, twice)
	if err != nil || !st.IsOK() {
		t.Fatalf("connect: %x %v", st.Marshal(), err)
	}
	return diff
}

func (c tctl) connectStatus(t *testing.T, subs []Subslot) wire.PNIOStatus {
	st, _, _ := c.Connect(subs, false)
	return st
}

func (c tctl) prmEnd(t *testing.T) {
	t.Helper()
	if err := c.PrmEnd(); err != nil {
		t.Fatalf("PrmEnd: %v", err)
	}
}

func (c tctl) release(t *testing.T) {
	t.Helper()
	if err := c.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func (c tctl) appReadySeen() bool                            { return c.AppReadySeen() }
func (c tctl) startOutputs(o map[Addr][]byte, ds uint8)      { c.StartOutputs(o, ds) }
func (c tctl) setOutputs(o map[Addr][]byte, ds uint8)        { c.SetOutputs(o, ds) }
func (c tctl) setOutputIOPS(a Addr, iops, ds uint8)          { c.SetOutputIOPS(a, iops, ds) }
func (c tctl) stopOutputs()                                  { c.StopOutputs() }
func (c tctl) lastInput(a Addr) ([]byte, uint8, uint8, bool) { return c.LastInput(a) }

func (c tctl) setIP(ip net.IP) uint8 {
	code, err := c.SetIP(ip)
	if err != nil {
		c.t.Fatal(err)
	}
	return code
}

func (c tctl) setName(n string) uint8 {
	code, err := c.SetName(n)
	if err != nil {
		c.t.Fatal(err)
	}
	return code
}

func (c tctl) read(t *testing.T, slot, subslot, index uint16) []byte {
	t.Helper()
	b, st, err := c.Read(slot, subslot, index)
	if err != nil || !st.IsOK() {
		t.Fatalf("read %#04x: %x %v", index, st.Marshal(), err)
	}
	return b
}

func (c tctl) readStatus(t *testing.T, slot, subslot, index uint16) wire.PNIOStatus {
	_, st, err := c.Read(slot, subslot, index)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (c tctl) write(t *testing.T, ws []wire.Write) wire.PNIOStatus {
	st, err := c.Write(ws)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
