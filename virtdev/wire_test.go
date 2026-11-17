package virtdev

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/modbus"
)

// writeTestProject lays a project's devices.yaml (and anything beside it)
// into a temp dir.
func writeTestProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// rawWrite sends one FC 5 or FC 6 request and returns the response's
// function code (high bit set on an exception).
func rawWrite(t *testing.T, c net.Conn, unit, fc byte, addr, value uint16) byte {
	t.Helper()
	req := make([]byte, 12)
	binary.BigEndian.PutUint16(req[0:2], 1)
	binary.BigEndian.PutUint16(req[4:6], 6)
	req[6], req[7] = unit, fc
	binary.BigEndian.PutUint16(req[8:10], addr)
	binary.BigEndian.PutUint16(req[10:12], value)
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}
	var hdr [7]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, binary.BigEndian.Uint16(hdr[4:6])-1)
	if _, err := io.ReadFull(c, body); err != nil {
		t.Fatal(err)
	}
	return body[0]
}

// eventually polls f until it returns nil or the deadline passes.
func eventually(t *testing.T, what string, f func() error) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if err = f(); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: %v", what, err)
}

const counterProfile = `
profile: test-counter
title: a counter that counts while commanded
maker: test
version: 1
protocol: modbus
modbus: { unit-id: 3 }
docs:
  - { id: here, title: this test }
points:
  - { name: Cmd,   type: WORD, table: holding, address: 0, writable: true, doc: here }
  - { name: Count, type: UINT, table: holding, address: 1, doc: here }
  - { name: Temp,  type: REAL, table: input, address: 10, format: int16, scale: 0.1, doc: here }
simulator: { block: COUNTER_SIM, source: counter.st }
`

const counterST = `
FUNCTION_BLOCK COUNTER_SIM
VAR_INPUT
  Cmd : WORD;
  Heat : REAL;
END_VAR
VAR_OUTPUT
  Count : UINT;
  Temp : REAL;
  Busy : BOOL;
END_VAR
Busy := Cmd = 1;
IF Busy THEN
  Count := Count + 1;
END_IF;
Temp := 20.0 + Heat;
END_FUNCTION_BLOCK
`

func TestWireFace(t *testing.T) {
	dir := writeTestProject(t, map[string]string{
		"devices.yaml": `
devices:
  - name: RB1
    profile: relay-board-16
    endpoint: 192.168.10.21
  - name: Ctr
    profile: profiles/counter.yaml
    endpoint: 192.168.10.21     # behind the same gateway address, unit 3
    init: { Heat: 1.5 }
model:
  program: plant.st
`,
		"profiles/counter.yaml": counterProfile,
		"profiles/counter.st":   counterST,
		"plant.st": `
PROGRAM Plant
(* Relay 1 is wired back to input 1; input 2 follows the counter. *)
RB1_Input1 := RB1_Relay1;
RB1_Input2 := Ctr_Busy;
END_PROGRAM
`,
	})
	set, err := LoadSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	run, err := Start(context.Background(), set, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(run.Stop)

	addr, ok := run.Redirect("192.168.10.21:502")
	if !ok {
		t.Fatalf("no redirect for the shared endpoint; endpoints %+v", run.Endpoints)
	}
	if len(run.Endpoints) != 2 || run.Endpoints[0].Listen != run.Endpoints[1].Listen {
		t.Fatalf("two devices behind one endpoint should share a listener: %+v", run.Endpoints)
	}

	ctx := context.Background()
	cl, err := modbus.Dial(ctx, addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	// Relay 1 on, through the board, through the model, back on input 1.
	if fc := rawWrite(t, raw, 1, 0x05, 0, 0xFF00); fc != 0x05 {
		t.Fatalf("write coil: fc %02X", fc)
	}
	eventually(t, "input 1 follows relay 1", func() error {
		bits, err := cl.ReadBits(ctx, 1, "discrete", 0, 16)
		if err != nil {
			return err
		}
		if !bits[0] {
			return errors.New("input 1 still off")
		}
		return nil
	})

	// The counter's simulator counts while commanded, and its hook drives
	// the relay board's input 2 through the model.
	if fc := rawWrite(t, raw, 3, 0x06, 0, 1); fc != 0x06 {
		t.Fatalf("write Cmd: fc %02X", fc)
	}
	eventually(t, "count rises and input 2 follows Busy", func() error {
		regs, err := cl.ReadRegisters(ctx, 3, "holding", 1, 1)
		if err != nil {
			return err
		}
		bits, err := cl.ReadBits(ctx, 1, "discrete", 1, 1)
		if err != nil {
			return err
		}
		if regs[0] < 3 || !bits[0] {
			return errors.New("not yet")
		}
		return nil
	})
	// Scaled input register: 21.5 °C at 0.1/count is 215.
	regs, err := cl.ReadRegisters(ctx, 3, "input", 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if regs[0] != 215 {
		t.Errorf("Temp register = %d, want 215", regs[0])
	}

	// The device refuses what the real one would: a read-only register,
	// an address it does not implement.
	if fc := rawWrite(t, raw, 3, 0x06, 1, 9); fc != 0x86 {
		t.Errorf("write to read-only Count: fc %02X, want 86", fc)
	}
	if _, err := cl.ReadRegisters(ctx, 3, "holding", 5, 1); err == nil {
		t.Error("read of an unimplemented register succeeded")
	}
	if errs := run.Plant.Errors(); len(errs) > 0 {
		t.Errorf("plant store errors: %v", errs)
	}
}
