package walk

import (
	"os"
	"strings"
	"testing"
)

func TestParseOID(t *testing.T) {
	for _, tc := range []struct {
		in, want, err string
	}{
		{in: ".1.3.6.1.2.1.1.5.0", want: "1.3.6.1.2.1.1.5.0"},
		{in: "1.3.6.1.4.1.3808.1.1.3.3.3.1.1.4.3", want: "1.3.6.1.4.1.3808.1.1.3.3.3.1.1.4.3"},
		{in: "1", err: "two arcs"},
		{in: "1..3", err: "empty arc"},
		{in: "1.3.6.1.2.1.ifName.3", err: "not a number"},
		{in: "3.6.1", err: "first arc"},
		{in: "1.3.06", err: "leading zero"},
		{in: "1.3.4294967296", err: "not a number"},
		{in: "", err: "empty"},
	} {
		got, err := ParseOID(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("ParseOID(%q) err = %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseOID(%q) = %q, %v", tc.in, got, err)
		}
	}
}

func TestCompareIsNumeric(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.3.6.1.2.1.2.2.1.9", "1.3.6.1.2.1.2.2.1.10", -1},
		{"1.3.6.1.2.1.2.2.1.10.1", "1.3.6.1.2.1.2.2.1.10", 1},
		{"1.3.6.1", "1.3.6.1", 0},
		{"1.3.6.1.4.1.9", "1.3.6.1.2.1.1", 1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if !HasPrefix("1.3.6.1.2.1.2.2.1.8.3", "1.3.6.1.2.1.2.2.1.8") || HasPrefix("1.3.6.1.2.1.2.2.1.80", "1.3.6.1.2.1.2.2.1.8") {
		t.Error("HasPrefix must respect arc boundaries")
	}
}

// Everything net-snmp prints for the types we meet, including the shapes
// only a real recording has: enum labels, units, typeless empty strings,
// multi-line strings, wrapped Hex-STRINGs, the end-of-walk lines.
func TestParseNetSnmpShapes(t *testing.T) {
	in := `.1.3.6.1.2.1.1.1.0 = STRING: "FSOS Software, Version 2.2.0F
Copyright (c) \"FS\" 2009-2024"
.1.3.6.1.2.1.1.2.0 = OID: .1.3.6.1.4.1.52642.2.1.45.101
.1.3.6.1.2.1.1.3.0 = Timeticks: (123456789) 14 days, 6:56:07.89
.1.3.6.1.2.1.2.2.1.6.1 = Hex-STRING: 00 00 5E 00 53 01 00 00 5E 00 53 01 00 00 5E 00
00 00 5E 00
.1.3.6.1.2.1.2.2.1.7.1 = INTEGER: up(1)
.1.3.6.1.2.1.2.2.1.8.1 = INTEGER: 2
.1.3.6.1.2.1.31.1.1.1.18.2 = ""
.1.3.6.1.2.1.31.1.1.1.6.1 = Counter64: 18446744073709551615
.1.3.6.1.2.1.31.1.1.1.15.1 = Gauge32: 1000
.1.3.6.1.2.1.2.2.1.10.1 = Counter32: 4294967295
.1.3.6.1.2.1.4.20.1.1.192.0.2.1 = IpAddress: 192.0.2.1
.1.3.6.1.2.1.33.1.2.7.0 = INTEGER: -5
.1.3.6.1.2.1.2.2.1.4.1 = Wrong Type (should be INTEGER): Gauge32: 1500
.1.3.6.1.2.1.99.1 = No Such Object available on this agent at this OID
.1.3.6.1.2.1.99.2 = No more variables left in this MIB View (It is past the end of the MIB tree)
`
	w, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	get := func(oid string) Varbind {
		t.Helper()
		v, ok := w.Get(oid)
		if !ok {
			t.Fatalf("missing %s", oid)
		}
		return v
	}
	if v := get("1.3.6.1.2.1.1.1.0"); string(v.Bytes) != "FSOS Software, Version 2.2.0F\nCopyright (c) \"FS\" 2009-2024" {
		t.Errorf("multi-line string = %q", v.Bytes)
	}
	if v := get("1.3.6.1.2.1.1.2.0"); v.Type != ObjectID || v.Str != "1.3.6.1.4.1.52642.2.1.45.101" {
		t.Errorf("OID = %+v", v)
	}
	if v := get("1.3.6.1.2.1.1.3.0"); v.Type != TimeTicks || v.Uint != 123456789 {
		t.Errorf("ticks = %+v", v)
	}
	if v := get("1.3.6.1.2.1.2.2.1.6.1"); len(v.Bytes) != 20 || v.Bytes[19] != 0 || v.Bytes[18] != 0x5E {
		t.Errorf("wrapped hex = % X", v.Bytes)
	}
	if v := get("1.3.6.1.2.1.2.2.1.7.1"); v.Int != 1 {
		t.Errorf("enum label = %+v", v)
	}
	if v := get("1.3.6.1.2.1.31.1.1.1.18.2"); v.Type != OctetString || len(v.Bytes) != 0 {
		t.Errorf("empty string = %+v", v)
	}
	if v := get("1.3.6.1.2.1.31.1.1.1.6.1"); v.Uint != 1<<64-1 {
		t.Errorf("Counter64 max = %d", v.Uint)
	}
	if v := get("1.3.6.1.2.1.4.20.1.1.192.0.2.1"); v.Type != IPAddress || v.Str != "192.0.2.1" {
		t.Errorf("ip = %+v", v)
	}
	if v := get("1.3.6.1.2.1.33.1.2.7.0"); v.Int != -5 {
		t.Errorf("negative = %+v", v)
	}
	if v := get("1.3.6.1.2.1.2.2.1.4.1"); v.Type != Gauge32 || v.Uint != 1500 {
		t.Errorf("wrong-type prefix = %+v", v)
	}
	if _, ok := w.Get("1.3.6.1.2.1.99.1"); ok {
		t.Error("No Such Object line must be skipped")
	}
	// Sorted numerically: .10.1 after .8.1.
	for i := 1; i < len(w); i++ {
		if Compare(w[i-1].OID, w[i].OID) >= 0 {
			t.Fatalf("not sorted at %s", w[i].OID)
		}
	}
}

func TestParseErrorsNameTheLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{".1.3.6.1.2.1.1.5.0 = STRING: \"x\"\n.1.3.6.1.2.1.1.5.0 = STRING: \"y\"\n", "appears twice"},
		{".1.3.6.1.2.1.1.5.0 = Float: 1.5\n", "line 1"},
		{"garbage\n", "line 1"},
		{".1.3.6.1.2.1.1.5.0 = STRING: \"open\n", "unterminated"},
		{".1.3.6.1.2.1.1.3.0 = Counter32: -1\n", "unsigned"},
	} {
		_, err := Parse(strings.NewReader(tc.in))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Parse(%q) err = %v, want %q", tc.in, err, tc.want)
		}
	}
}

// Bytes → Parse → Bytes is a fixed point, for every type the format
// carries: what `browse --record` writes is what `import --walk` reads.
func TestRoundTrip(t *testing.T) {
	w := Walk{
		{OID: "1.3.6.1.2.1.1.1.0", Type: OctetString, Bytes: []byte("line one\nline \"two\" \\ end")},
		{OID: "1.3.6.1.2.1.1.2.0", Type: ObjectID, Str: "1.3.6.1.4.1.32473.1"},
		{OID: "1.3.6.1.2.1.1.3.0", Type: TimeTicks, Uint: 8640000},
		{OID: "1.3.6.1.2.1.2.2.1.5.1", Type: Gauge32, Uint: 4294967295},
		{OID: "1.3.6.1.2.1.2.2.1.6.1", Type: OctetString, Bytes: []byte{0, 0x1a, 0x2b, 0xff}},
		{OID: "1.3.6.1.2.1.2.2.1.7.1", Type: Integer, Int: -2147483648},
		{OID: "1.3.6.1.2.1.2.2.1.10.1", Type: Counter32, Uint: 7},
		{OID: "1.3.6.1.2.1.4.20.1.1.10.0.0.1", Type: IPAddress, Str: "10.0.0.1"},
		{OID: "1.3.6.1.2.1.31.1.1.1.6.1", Type: Counter64, Uint: 1 << 63},
		{OID: "1.3.6.1.2.1.31.1.1.1.18.1", Type: OctetString, Bytes: []byte{}},
		{OID: "1.3.6.1.2.1.31.1.1.1.18.2", Type: OctetString, Bytes: []byte("trailing nul\x00")},
	}
	first := w.Bytes()
	back, err := ParseBytes(first)
	if err != nil {
		t.Fatalf("%v\n%s", err, first)
	}
	if got := back.Bytes(); string(got) != string(first) {
		t.Fatalf("not a fixed point:\n%s\n---\n%s", first, got)
	}
	if string(back[len(back)-1].Bytes) != "trailing nul\x00" {
		t.Errorf("trailing NUL lost: %q", back[len(back)-1].Bytes)
	}
}

// The committed fixtures parse and survive the round trip, so a fixture
// edited by hand cannot drift from what the format writes.
func TestFixturesParse(t *testing.T) {
	for _, f := range []string{"switch.snmpwalk", "ups.snmpwalk", "pdu.snmpwalk"} {
		raw, err := os.ReadFile("../testdata/" + f)
		if err != nil {
			t.Fatal(err)
		}
		w, err := ParseBytes(raw)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		again, err := ParseBytes(w.Bytes())
		if err != nil || string(again.Bytes()) != string(w.Bytes()) {
			t.Fatalf("%s: round trip: %v", f, err)
		}
	}
}

func TestLookup(t *testing.T) {
	w := Walk{
		{OID: "1.3.6.1.2.1.2.2.1.8.1", Type: Integer, Int: 1},
		{OID: "1.3.6.1.2.1.2.2.1.8.2", Type: Integer, Int: 2},
		{OID: "1.3.6.1.2.1.2.2.1.10.1", Type: Counter32, Uint: 5},
	}
	w.Sort()
	if v, ok := w.Next("1.3.6.1.2.1.2.2.1.8"); !ok || v.OID != "1.3.6.1.2.1.2.2.1.8.1" {
		t.Errorf("Next(column) = %+v", v)
	}
	if v, ok := w.Next("1.3.6.1.2.1.2.2.1.8.2"); !ok || v.OID != "1.3.6.1.2.1.2.2.1.10.1" {
		t.Errorf("Next across columns = %+v", v)
	}
	if _, ok := w.Next("1.3.6.1.2.1.2.2.1.10.1"); ok {
		t.Error("Next past the end")
	}
	if got := len(w.Subtree("1.3.6.1.2.1.2.2.1.8")); got != 2 {
		t.Errorf("Subtree = %d", got)
	}
}

func TestSnmprec(t *testing.T) {
	w := Walk{
		{OID: "1.3.6.1.2.1.1.5.0", Type: OctetString, Bytes: []byte("sw1")},
		{OID: "1.3.6.1.2.1.2.2.1.6.1", Type: OctetString, Bytes: []byte{0, 0x1a}},
		{OID: "1.3.6.1.2.1.2.2.1.7.1", Type: Integer, Int: 1},
		{OID: "1.3.6.1.2.1.31.1.1.1.6.1", Type: Counter64, Uint: 99},
		{OID: "1.3.6.1.2.1.31.1.1.1.18.1", Type: OctetString, Bytes: []byte{}},
	}
	got := string(w.Snmprec(map[string]string{"1.3.6.1.2.1.31.1.1.1.6.1": "70:numeric|rate=10,initial=99"}))
	want := "1.3.6.1.2.1.1.5.0|4|sw1\n" +
		"1.3.6.1.2.1.2.2.1.6.1|4x|001a\n" +
		"1.3.6.1.2.1.2.2.1.7.1|2|1\n" +
		"1.3.6.1.2.1.31.1.1.1.6.1|70:numeric|rate=10,initial=99\n" +
		"1.3.6.1.2.1.31.1.1.1.18.1|4x|\n"
	if got != want {
		t.Errorf("snmprec:\n%s\nwant:\n%s", got, want)
	}
}
