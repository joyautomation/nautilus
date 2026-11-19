package iolink

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestFindParam(t *testing.T) {
	d := levelIODD(t)
	for ref, want := range map[string]struct {
		index, sub, octets int
	}{
		"V_Setpoint": {64, 0, 2}, "setpoint": {64, 0, 2}, "64": {64, 0, 2},
		"V_SerialNumber": {21, 0, 16}, "Serial Number": {21, 0, 16},
		"V_SystemCommand": {2, 0, 1},
		"Window.1":        {65, 1, 0}, "65.2": {65, 2, 0},
	} {
		p, err := FindParam(d, ref)
		if err != nil {
			t.Errorf("%q: %v", ref, err)
			continue
		}
		if p.Index() != want.index || p.Subindex != want.sub || (want.octets != 0 && p.Octets() != want.octets) {
			t.Errorf("%q: index %d sub %d octets %d, want %+v", ref, p.Index(), p.Subindex, p.Octets(), want)
		}
	}
	for ref, want := range map[string]string{
		"V_Nothing":    "has no variable",
		"999":          "has no variable",
		"V_Window":     "is a record: name an item",
		"Window.9":     "has no item",
		"V_Setpoint.1": "is not a record",
	} {
		if _, err := FindParam(d, ref); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", ref, err, want)
		}
	}
}

// An ISDU value is the type's octets right-aligned: a 16-bit unsigned is two
// octets big-endian, a string its fixed length with trailing NULs trimmed.
func TestParamDecodeEncode(t *testing.T) {
	d := levelIODD(t)
	sp, _ := FindParam(d, "V_Setpoint")
	got, err := sp.Decode([]byte{0x03, 0xE8})
	if err != nil || got != uint64(1000) {
		t.Errorf("Setpoint decode = %v, %v; want 1000", got, err)
	}
	raw, err := sp.Encode(uint64(500))
	if err != nil || !bytes.Equal(raw, []byte{0x01, 0xF4}) {
		t.Errorf("Setpoint encode = % X, %v; want 01 F4", raw, err)
	}
	tag, _ := FindParam(d, "V_ApplicationSpecificTag")
	s, err := tag.Decode(append([]byte("Tank 3"), make([]byte, 26)...))
	if err != nil || s != "Tank 3" {
		t.Errorf("tag decode = %q, %v", s, err)
	}
	if raw, err := tag.Encode("Pump"); err != nil || len(raw) != 32 || string(bytes.TrimRight(raw, "\x00")) != "Pump" {
		t.Errorf("tag encode = %q, %v; want 32 octets", raw, err)
	}
	if _, err := sp.Decode([]byte{0x03}); err == nil {
		t.Error("a short value was decoded")
	}
}

// A write is checked before it is sent: the IODD's range, the type's width,
// the access rights; each refusal names the variable, nothing is clipped.
func TestParamEncodeRefuses(t *testing.T) {
	d := levelIODD(t)
	sp, _ := FindParam(d, "V_Setpoint")
	for name, v := range map[string]any{"above the range": uint64(1001), "negative": int64(-1), "not a number": "high"} {
		if raw, err := sp.Encode(v); err == nil {
			t.Errorf("%s: encoded % X", name, raw)
		} else if !strings.Contains(err.Error(), "V_Setpoint") {
			t.Errorf("%s: %v does not name the variable", name, err)
		}
	}
	if _, err := sp.Encode(uint64(1001)); err == nil || !strings.Contains(err.Error(), "0..1000") {
		t.Errorf("the refusal does not say the range: %v", err)
	}
	if _, err := sp.Encode(uint64(1000)); err != nil {
		t.Errorf("the top of the range: %v", err)
	}
	serial, _ := FindParam(d, "V_SerialNumber")
	if _, err := serial.Encode("X"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("a write to a read-only variable: %v", err)
	}
	cmd, _ := FindParam(d, "V_SystemCommand")
	if _, err := cmd.Decode([]byte{0}); err == nil || !strings.Contains(err.Error(), "write-only") {
		t.Errorf("a read of a write-only variable: %v", err)
	}
	if !reflect.DeepEqual([]bool{sp.CanRead(), sp.CanWrite(), serial.CanWrite(), cmd.CanRead()}, []bool{true, true, false, false}) {
		t.Error("access rights")
	}
}
