package iodd

import (
	"bytes"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func load(t *testing.T, name string) *IODD {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse %s: %v", name, err)
	}
	return d
}

const (
	level = "acme-level100-20261118-IODD1.1.xml"
	press = "acme-press200-20261118-IODD1.0.1.xml"
	flow  = "acme-flow300-20261118-IODD1.1.xml"
)

func TestParseIdentityAndVersion(t *testing.T) {
	d := load(t, level)
	if d.Version != "1.1" {
		t.Errorf("version %q, want 1.1", d.Version)
	}
	id := d.Identity
	if id.VendorID != 65261 || id.DeviceID != 1048577 || id.VendorName != "Acme" || id.DeviceName != "LV-100 level sensor" {
		t.Errorf("identity %+v", id)
	}
	want := []Variant{{"LV-100-A", "LV-100-A", "Level sensor, 1 m probe"}, {"LV-100-B", "LV-100-B", "Level sensor, 2 m probe"}}
	if !reflect.DeepEqual(id.Variants, want) {
		t.Errorf("variants %+v", id.Variants)
	}
	for _, c := range []struct {
		vendor, device uint32
		ok             bool
	}{
		{65261, 1048577, true},
		{65261, 1048578, true}, // additionalDeviceIds
		{65261, 1048579, false},
		{65260, 1048577, false}, // right device id, wrong vendor
	} {
		if got := id.Matches(c.vendor, c.device); got != c.ok {
			t.Errorf("Matches(%d, %d) = %v, want %v", c.vendor, c.device, got, c.ok)
		}
	}
	if v := load(t, press).Version; v != "1.0.1" {
		t.Errorf("press version %q, want 1.0.1", v)
	}
}

func TestParseProcessDataLayout(t *testing.T) {
	pd, err := load(t, level).Layout("")
	if err != nil {
		t.Fatal(err)
	}
	in := pd.In
	if in.BitLength != 32 || in.Octets() != 4 || in.Name != "Process data in" {
		t.Fatalf("in: %d bits, %d octets, %q", in.BitLength, in.Octets(), in.Name)
	}
	type row struct {
		sub, off int
		name     string
		kind     Kind
		bits     int
		iec      string
		scaled   bool
		unit     int
	}
	var got []row
	for _, it := range in.Items {
		got = append(got, row{it.Subindex, it.BitOffset, it.Name, it.Type.Kind, it.Type.BitLength, it.IECType(), it.Scaled, it.UnitCode})
	}
	want := []row{
		{1, 18, "Level", Integer, 14, "REAL", true, 1013},
		{2, 4, "Temperature", Integer, 12, "INT", false, 1001},
		{3, 16, "Ready", Boolean, 1, "BOOL", false, 0},
		{4, 0, "Switch point 1", Boolean, 1, "BOOL", false, 0},
		{5, 1, "Switch point 2", Boolean, 1, "BOOL", false, 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("items\n got %+v\nwant %+v", got, want)
	}
	if lv := in.Items[0]; lv.Gradient != 0.1 || lv.Offset != 0 {
		t.Errorf("level scaling %g, %g", lv.Gradient, lv.Offset)
	}
	if vals := in.Items[3].Type.Values; !reflect.DeepEqual(vals, []NamedValue{{"false", "Off"}, {"true", "On"}}) {
		t.Errorf("SP1 value names %+v", vals)
	}
	if pd.Out == nil || pd.Out.BitLength != 8 || len(pd.Out.Items) != 3 || pd.Out.Items[1].IECType() != "USINT" {
		t.Errorf("out %+v", pd.Out)
	}
}

func TestParseVariables(t *testing.T) {
	d := load(t, level)
	byID := map[string]Variable{}
	for _, v := range d.Variables {
		byID[v.ID] = v
	}
	// Standard variables resolve to their spec index without the
	// Community's definitions file.
	for id, idx := range map[string]int{"V_DirectParameters_1": 0, "V_SystemCommand": 2, "V_VendorName": 16, "V_SerialNumber": 21, "V_ApplicationSpecificTag": 24, "V_DeviceStatus": 36} {
		v, ok := byID[id]
		if !ok || v.Index != idx || !v.Standard {
			t.Errorf("%s: %+v, want standard index %d", id, v, idx)
		}
	}
	if v := byID["V_VendorName"]; v.Default != "Acme" || v.Access != "ro" || v.Type.Kind != String {
		t.Errorf("V_VendorName %+v", v)
	}
	sp := byID["V_Setpoint"]
	if sp.Index != 64 || sp.Access != "rw" || sp.Default != "100" || sp.Name != "Setpoint" || sp.Type.Kind != UInteger || sp.Type.BitLength != 16 ||
		!reflect.DeepEqual(sp.Type.Ranges, []Range{{"0", "1000", ""}}) {
		t.Errorf("V_Setpoint %+v", sp)
	}
	w := byID["V_Window"] // a record through DatatypeRef
	if w.Index != 65 || len(w.Items) != 2 || w.Items[0].Name != "Window low" || w.Items[0].BitOffset != 16 {
		t.Errorf("V_Window %+v", w)
	}
	// IODD 1.0.1's older standard ids.
	p := load(t, press)
	for _, v := range p.Variables {
		if v.ID == "V_ApplicationSpecificName" && v.Index != 24 || v.ID == "V_ProcessDataIn" && v.Index != 40 {
			t.Errorf("1.0.1 %s index %d", v.ID, v.Index)
		}
	}
}

// The decode vectors are worked out by hand from the IO-Link bit rule
// (bit 0 is the LSB of the last octet), not from the codec.
func TestDecodeHandVectors(t *testing.T) {
	in := mustLayout(t, load(t, level), "").In
	for _, c := range []struct {
		name string
		raw  []byte
		want []any // Level (scaled), Temperature, Ready, SP1, SP2
	}{
		{
			// Level -125 → 14-bit 0x3F83 at bits 31..18; Ready at 16;
			// Temperature -3 → 12-bit 0xFFD at bits 15..4; SP1 at 0.
			// 1111 1110 0000 1101 1111 1111 1101 0001 = FE 0D FF D1.
			name: "negative values",
			raw:  []byte{0xFE, 0x0D, 0xFF, 0xD1},
			want: []any{-12.5, int64(-3), true, true, false},
		},
		{
			// Level +8191 → 0x1FFF; Temperature -2048 → 0x800; SP2 at 1.
			// 0111 1111 1111 1100 1000 0000 0000 0010 = 7F FC 80 02.
			name: "extremes",
			raw:  []byte{0x7F, 0xFC, 0x80, 0x02},
			want: []any{819.1, int64(-2048), false, false, true},
		},
		{
			// Temperature +1 alone: bit 4 is bit 4 of the LAST octet.
			name: "temperature one",
			raw:  []byte{0x00, 0x00, 0x00, 0x10},
			want: []any{0.0, int64(1), false, false, false},
		},
		{
			// Temperature 0x7FF (+2047): bits 15..4 = 0111 1111 1111,
			// straddling octet 2 (0x7F) and the top of octet 3 (0xF0).
			name: "straddle",
			raw:  []byte{0x00, 0x00, 0x7F, 0xF0},
			want: []any{0.0, int64(2047), false, false, false},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := in.Decode(c.raw)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("%d values", len(got))
			}
			for i := range got {
				if f, ok := c.want[i].(float64); ok {
					g, ok := got[i].(float64)
					if !ok || math.Abs(g-f) > 1e-9 {
						t.Errorf("item %d = %v (%T), want %v", i, got[i], got[i], f)
					}
					continue
				}
				if got[i] != c.want[i] {
					t.Errorf("item %d = %v (%T), want %v (%T)", i, got[i], got[i], c.want[i], c.want[i])
				}
			}
		})
	}
}

func TestDecodeFloatStatusAlarm(t *testing.T) {
	in := mustLayout(t, load(t, press), "").In
	// 2.5 is 0x40200000 in IEEE 754 single; status 0x81; alarm bit 0.
	got, err := in.Decode([]byte{0x40, 0x20, 0x00, 0x00, 0x81, 0x01})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []any{float32(2.5), uint64(129), true}) {
		t.Errorf("got %#v", got)
	}
}

func TestDecodeVariantsStringsAndSingleValue(t *testing.T) {
	d := load(t, flow)
	single := mustLayout(t, d, "0").In
	if len(single.Items) != 1 || single.Items[0].Name != "Flow" || single.Items[0].Subindex != 0 || single.Items[0].IECType() != "REAL" {
		t.Fatalf("single %+v", single.Items)
	}
	// 0x01F4 = 500; 500 × 0.5 − 10 = 240.
	got, err := single.Decode([]byte{0x01, 0xF4})
	if err != nil || got[0] != 240.0 {
		t.Errorf("flow = %v, %v; want 240", got, err)
	}
	rec := mustLayout(t, d, "1").In
	// "AB" padded with NULs in octets 0..3, then CA FE.
	got, err = rec.Decode([]byte{'A', 'B', 0, 0, 0xCA, 0xFE})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "AB" || !bytes.Equal(got[1].([]byte), []byte{0xCA, 0xFE}) {
		t.Errorf("record %#v", got)
	}
	if rec.Items[0].IECType() != "STRING" || rec.Items[1].IECType() != "ARRAY [0..1] OF BYTE" {
		t.Errorf("types %s, %s", rec.Items[0].IECType(), rec.Items[1].IECType())
	}
}

func TestDecodeRefusesWrongLength(t *testing.T) {
	in := mustLayout(t, load(t, level), "").In
	for _, raw := range [][]byte{{0xFE, 0x0D, 0xFF}, {0xFE, 0x0D, 0xFF, 0xD1, 0x00}, nil} {
		if _, err := in.Decode(raw); err == nil {
			t.Errorf("Decode(% X) accepted a %d-octet image for a 4-octet one", raw, len(raw))
		}
	}
}

func TestLayoutChoosesVariant(t *testing.T) {
	d := load(t, flow)
	for _, c := range []struct{ variant, want, err string }{
		{"0", "PD_Flow", ""},
		{"1", "PD_Tagged", ""},
		{"", "", "say which with pd-variant: one of 0, 1"},
		{"7", "", "pd-variant 7 is not a value of V_PdSelect"},
	} {
		pd, err := d.Layout(c.variant)
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("Layout(%q) error %v, want %q", c.variant, err, c.err)
		case c.err == "" && (err != nil || pd.ID != c.want):
			t.Errorf("Layout(%q) = %v, %v; want %s", c.variant, pd, err, c.want)
		}
	}
	if _, err := load(t, level).Layout("1"); err == nil {
		t.Error("a pd-variant on a device with one layout was accepted")
	}
}

// The encode vectors are by hand too: Lock bit 7, Mode bits 3..1, Teach
// bit 0.
func TestEncodeHandVectors(t *testing.T) {
	out := mustLayout(t, load(t, level), "").Out
	for _, c := range []struct {
		vals []any
		want byte
	}{
		{[]any{true, uint64(5), nil}, 0x0B},   // 0000 1011
		{[]any{false, uint64(1), true}, 0x82}, // 1000 0010
		{[]any{nil, nil, nil}, 0x00},
	} {
		got, err := out.Encode(c.vals)
		if err != nil || len(got) != 1 || got[0] != c.want {
			t.Errorf("Encode(%v) = % X, %v; want %02X", c.vals, got, err, c.want)
		}
	}
}

func TestEncodeRefusesWhatDoesNotFit(t *testing.T) {
	out := mustLayout(t, load(t, level), "").Out
	for _, vals := range [][]any{
		{nil, uint64(8), nil}, // 3 bits hold 0..7
		{nil, int64(-1), nil}, // unsigned
		{"yes", nil, nil},     // not a bool
		{nil, 2.0, nil},       // not an integer (unscaled)
		{nil, nil},            // too few values
	} {
		if got, err := out.Encode(vals); err == nil {
			t.Errorf("Encode(%v) = % X, accepted", vals, got)
		}
	}
	// A signed item's range, and a string's length.
	im := &Image{BitLength: 40, Items: []Item{
		{Subindex: 1, BitOffset: 32, Type: Type{Kind: Integer, Name: "IntegerT", BitLength: 8}},
		{Subindex: 2, BitOffset: 0, Type: Type{Kind: String, Name: "StringT", FixedLength: 4, BitLength: 32, Encoding: "US-ASCII"}},
	}}
	for _, vals := range [][]any{{int64(128), nil}, {int64(-129), nil}, {nil, "ABCDE"}, {nil, "Ä"}} {
		if got, err := im.Encode(vals); err == nil {
			t.Errorf("Encode(%v) = % X, accepted", vals, got)
		}
	}
	got, err := im.Encode([]any{int64(-128), "AB"})
	if want := []byte{0x80, 'A', 'B', 0, 0}; err != nil || !bytes.Equal(got, want) {
		t.Errorf("Encode(-128, AB) = % X, %v; want % X", got, err, want)
	}
}

func TestEncodeScaled(t *testing.T) {
	// Out with a scaled 16-bit item: 240 → (240 + 10) / 0.5 = 500 = 01 F4.
	im := &Image{BitLength: 16, Items: []Item{{Type: Type{Kind: UInteger, Name: "UIntegerT", BitLength: 16}, Scaled: true, Gradient: 0.5, Offset: -10}}}
	got, err := im.Encode([]any{240.0})
	if err != nil || !bytes.Equal(got, []byte{0x01, 0xF4}) {
		t.Errorf("Encode(240) = % X, %v; want 01 F4", got, err)
	}
	// -20 would be raw -20: not an unsigned value.
	if got, err := im.Encode([]any{-20.0}); err == nil {
		t.Errorf("Encode(-20) = % X, accepted", got)
	}
}

// Encode then decode round-trips every kind at random in-range values;
// the hand vectors above pin the bit order, so the two cannot be wrong
// the same way.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	im := &Image{BitLength: 120, Items: []Item{
		{Subindex: 1, BitOffset: 117, Type: Type{Kind: Boolean, Name: "BooleanT", BitLength: 1}},
		{Subindex: 2, BitOffset: 104, Type: Type{Kind: UInteger, Name: "UIntegerT", BitLength: 13}},
		{Subindex: 3, BitOffset: 93, Type: Type{Kind: Integer, Name: "IntegerT", BitLength: 11}},
		{Subindex: 4, BitOffset: 56, Type: Type{Kind: Float32, Name: "Float32T", BitLength: 32}},
		{Subindex: 5, BitOffset: 24, Type: Type{Kind: String, Name: "StringT", FixedLength: 4, BitLength: 32, Encoding: "UTF-8"}},
		{Subindex: 6, BitOffset: 8, Type: Type{Kind: OctetString, Name: "OctetStringT", FixedLength: 2, BitLength: 16}},
		{Subindex: 7, BitOffset: 1, Type: Type{Kind: Integer, Name: "IntegerT", BitLength: 7}},
	}}
	if err := im.checkLayout(); err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewSource(705))
	letters := "abcdefgh"
	for range 2000 {
		s := make([]byte, r.Intn(5))
		for i := range s {
			s[i] = letters[r.Intn(len(letters))]
		}
		vals := []any{
			r.Intn(2) == 1,
			uint64(r.Intn(1 << 13)),
			int64(r.Intn(1<<11) - 1<<10),
			float32(r.NormFloat64() * 1e3),
			string(s),
			[]byte{byte(r.Intn(256)), byte(r.Intn(256))},
			int64(r.Intn(1<<7) - 1<<6),
		}
		raw, err := im.Encode(vals)
		if err != nil {
			t.Fatalf("Encode(%v): %v", vals, err)
		}
		got, err := im.Decode(raw)
		if err != nil {
			t.Fatalf("Decode(% X): %v", raw, err)
		}
		if !reflect.DeepEqual(got, vals) {
			t.Fatalf("round trip\n got %#v\nwant %#v", got, vals)
		}
	}
}

func mustLayout(t *testing.T, d *IODD, variant string) *ProcessData {
	t.Helper()
	pd, err := d.Layout(variant)
	if err != nil {
		t.Fatal(err)
	}
	return pd
}
