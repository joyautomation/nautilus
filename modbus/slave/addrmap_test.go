package slave

import "testing"

// A unit with no map answers every address (the driver tests rely on it);
// one with a map answers exception 02 outside it and for a write to a
// read-only register, like the hardware a virtual device stands in for.
func TestAddressMap(t *testing.T) {
	srv, c := startRaw(t)
	open := srv.Unit(1)
	open.SetHolding(900, 7)

	strict := NewUnit()
	strict.Define(TableHolding, 0, 2, true)   // control word, reference
	strict.Define(TableHolding, 50, 2, false) // status word, actual
	strict.Define(TableCoil, 0, 16, true)
	strict.SetHolding(50, 0x0237)
	srv.SetUnit(2, strict)

	cases := []struct {
		name   string
		unit   byte
		fc     byte
		data   []byte
		wantFC byte
	}{
		{"open unit reads anything", 1, 0x03, []byte{0x03, 0x84, 0, 1}, 0x03},
		{"mapped read", 2, 0x03, []byte{0, 50, 0, 2}, 0x03},
		{"read runs past the map", 2, 0x03, []byte{0, 50, 0, 3}, 0x83},
		{"unmapped read", 2, 0x03, []byte{0, 10, 0, 1}, 0x83},
		{"unmapped table", 2, 0x04, []byte{0, 0, 0, 1}, 0x84},
		{"write a writable register", 2, 0x06, []byte{0, 0, 0x04, 0x7F}, 0x06},
		{"write a read-only register", 2, 0x06, []byte{0, 50, 0, 1}, 0x86},
		{"write multiple across read-only", 2, 0x10, []byte{0, 49, 0, 2, 4, 0, 1, 0, 2}, 0x90},
		{"write a mapped coil", 2, 0x05, []byte{0, 3, 0xFF, 0}, 0x05},
		{"write past the coils", 2, 0x0F, []byte{0, 15, 0, 2, 1, 3}, 0x8F},
	}
	for i, tc := range cases {
		fc, data := rawRequest(t, c, uint16(i+1), tc.unit, tc.fc, tc.data)
		if fc != tc.wantFC {
			t.Errorf("%s: fc %02X, want %02X (data % X)", tc.name, fc, tc.wantFC, data)
			continue
		}
		if fc&0x80 != 0 && (len(data) != 1 || data[0] != 0x02) {
			t.Errorf("%s: exception % X, want 02", tc.name, data)
		}
	}
	if got := strict.Holding(0); got != 0x047F {
		t.Errorf("control word = %04X after the write, want 047F", got)
	}
	if got := strict.Holding(50); got != 0x0237 {
		t.Errorf("status word = %04X, the refused write must not land", got)
	}
	if !strict.Coil(3) {
		t.Error("coil 3 not set")
	}
}
