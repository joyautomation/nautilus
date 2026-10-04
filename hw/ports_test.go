package hw

import (
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// A PortList (Q-BRIDGE VLAN membership) reads into the front-panel ports
// whose bits are set, and serves back as the bitmap that reads the same.
func TestPortsBinding(t *testing.T) {
	f := Field{Name: "Ports", Kind: ir.TypeString}
	// Bridge ports 165..168 are front-panel ports 1..4: byte 20 (0-based),
	// bits 0x08 0x04 0x02 0x01.
	b := Binding{Ports: map[string]int{"165": 1, "166": 2, "167": 3, "168": 4}}
	if err := b.Validate(f); err != nil {
		t.Fatal(err)
	}
	wire := strings.Repeat("00:", 20) + "0D:00:00" // 165, 166, 168
	v, ok, err := b.Apply(f, RawStringVal(wire), nil, time.Now())
	if err != nil || !ok || v.S != "1,2,4" {
		t.Fatalf("apply = %q %v %v", v.S, ok, err)
	}
	// Bits with no port (a CPU or LAG bridge port) are dropped; "" (an
	// all-zero list, its NULs trimmed by the decoder) reads empty.
	if v, _, _ := b.Apply(f, RawStringVal("80:"+strings.Repeat("00:", 19)+"08"), nil, time.Now()); v.S != "1" {
		t.Fatalf("unmapped bit: %q", v.S)
	}
	if v, _, _ := b.Apply(f, RawStringVal(""), nil, time.Now()); v.S != "" {
		t.Fatalf("empty: %q", v.S)
	}
	if got := PortBits(RawStringVal("A")); len(got) != 2 || got[0] != 2 || got[1] != 8 {
		t.Fatalf("printable list: %v", got)
	}

	// Backwards: the recording's length is kept, and Apply reads it back.
	for _, want := range []string{"3", "1,2,3,4", ""} {
		raw, err := b.Invert(f, ir.StringVal(want), RawStringVal(wire))
		if err != nil {
			t.Fatal(err)
		}
		if len(raw.S) != len(wire) {
			t.Fatalf("%q: served %d chars, recorded %d", want, len(raw.S), len(wire))
		}
		if v, _, _ := b.Apply(f, raw, nil, time.Now()); v.S != want {
			t.Fatalf("round trip %q → %q → %q", want, raw.S, v.S)
		}
	}
	// From an empty recording, the list is as long as its highest bit needs.
	if raw, _ := b.Invert(f, ir.StringVal("4"), RawStringVal("")); raw.S != strings.Repeat("00:", 20)+"01" {
		t.Fatalf("from empty: %q", raw.S)
	}
	if _, err := b.Invert(f, ir.StringVal("9"), RawStringVal(wire)); err == nil {
		t.Fatal("a port with no bit must be an error")
	}
	if err := (Binding{Ports: map[string]int{"1": 1}}).Validate(Field{Name: "Pvid", Kind: ir.TypeInt}); err == nil {
		t.Fatal("ports: on an INT must be refused")
	}
	if err := (Binding{Ports: map[string]int{"1": 1}, Scale: 2}).Validate(f); err == nil {
		t.Fatal("ports: with scale must be refused")
	}
}
