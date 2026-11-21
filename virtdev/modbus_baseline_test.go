package virtdev

import (
	"encoding/binary"
	"testing"

	"github.com/joyautomation/nautilus/acceptance"
	nio "github.com/joyautomation/nautilus/io"
)

// The in-process Modbus face decides its start-up push as the modbus driver
// does (#1123): the first push is computed from the device's live read, so an
// output that differs from what the device holds is one write, and one that
// equals it is recorded and not sent. After it only a change is a write, so a
// value the plant sets later survives an output the program never moves.
func TestInProcessModbusStartUpDecidesAgainstTheDevice(t *testing.T) {
	set, err := LoadSet(binFill)
	if err != nil {
		t.Fatal(err)
	}
	imb, _ := fixtureDrivers(t, nil, "opc.tcp://10.77.1.103:4840")
	ip, err := NewInProcess(set, acceptance.NewClock(), []nio.NamedDriver{{Name: "modbus", Driver: imb}})
	if err != nil {
		t.Fatal(err)
	}
	unit := ip.mb[0].sources["RB1"].unit
	device := func() uint16 {
		t.Helper()
		resp, exc := unit.Handle(0x03, []byte{0, 199, 0, 1})
		if exc != 0 {
			t.Fatalf("read holding 199: exception %02X", exc)
		}
		return binary.BigEndian.Uint16(resp[1:])
	}
	setDevice := func(v uint16) {
		t.Helper()
		if _, exc := unit.Handle(0x06, []byte{0, 199, byte(v >> 8), byte(v)}); exc != 0 {
			t.Fatalf("set holding 199: exception %02X", exc)
		}
	}
	push := func(v int64) {
		t.Helper()
		if err := ip.WriteOutputs(nio.Values{"RB_Out": v}); err != nil {
			t.Fatal(err)
		}
		if err := ip.Err(); err != nil {
			t.Fatal(err)
		}
	}
	setDevice(7)
	push(3) // the program's first value differs from the device's: one write
	if got := device(); got != 3 {
		t.Fatalf("the first push (3) did not reach a device holding 7: %d", got)
	}
	setDevice(9) // the plant moves it
	push(3)
	if got := device(); got != 9 {
		t.Errorf("an unchanged output overwrote the plant's change: %d, want 9", got)
	}
	push(4)
	if got := device(); got != 4 {
		t.Errorf("a changed output did not reach the device: %d, want 4", got)
	}
}
