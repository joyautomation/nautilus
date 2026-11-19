package opcua

import (
	"strings"
	"testing"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
)

// addBytes adds a node holding v ([]byte is a ByteString, ua.ByteArray a
// Byte[]) to the test server's Plant folder.
func addBytes(t *testing.T, ts *testServer, name string, v any, writable bool) {
	t.Helper()
	n := ts.ns.AddNewVariableStringNode(name, v)
	dt, level := uint32(id.ByteString), uint8(ua.AccessLevelTypeCurrentRead)
	if _, ok := v.(ua.ByteArray); ok {
		dt = id.Byte
	}
	if writable {
		level |= uint8(ua.AccessLevelTypeCurrentWrite)
	}
	_ = n.SetAttribute(ua.AttributeIDDataType, server.DataValueFromValue(ua.NewNumericNodeID(0, dt)))
	_ = n.SetAttribute(ua.AttributeIDAccessLevel, server.DataValueFromValue(level))
	ts.srv.Node(ua.NewStringNodeID(ts.ns.ID(), "Plant")).AddRef(n, id.HasComponent, true)
	ts.vars[name] = n
}

func byteManifest(tags ...TagBinding) Manifest {
	m := Manifest{Namespaces: []string{"http://opcfoundation.org/UA/", "urn:nautilus:test"}}
	for _, tg := range tags {
		tg.Namespace = "urn:nautilus:test"
		m.Tags = append(m.Tags, tg)
	}
	return m
}

// Octets arrive as lowercase hex whichever shape the server holds them in
// (ByteString, or Byte[] as OPC 30120 types process data), under either
// declared byte type.
func TestByteTypesReadAsHex(t *testing.T) {
	ts := startServer(t, freePort(t))
	addBytes(t, ts, "PdStr", []byte{0x02, 0x86, 0xAB}, false)
	addBytes(t, ts, "PdArr", ua.ByteArray{0x02, 0x86, 0xAB}, false)
	addBytes(t, ts, "Empty", []byte{}, false)
	d := startDriver(t, ts.url, byteManifest(
		TagBinding{Name: "StrAsStr", NodeID: "ns=2;s=PdStr", Type: ByteString},
		TagBinding{Name: "ArrAsStr", NodeID: "ns=2;s=PdArr", Type: ByteString},
		TagBinding{Name: "StrAsArr", NodeID: "ns=2;s=PdStr", Type: ByteArray},
		TagBinding{Name: "ArrAsArr", NodeID: "ns=2;s=PdArr", Type: ByteArray},
		TagBinding{Name: "Empty", NodeID: "ns=2;s=Empty", Type: ByteString},
	))
	waitFor(t, "values", func() bool { return read(t, d)["ArrAsArr"] != nil })
	v := read(t, d)
	for _, n := range []string{"StrAsStr", "ArrAsStr", "StrAsArr", "ArrAsArr"} {
		if v[n] != "0286ab" {
			t.Errorf("%s = %#v, want \"0286ab\"", n, v[n])
		}
	}
	if v["Empty"] != "" {
		t.Errorf("Empty = %#v, want \"\"", v["Empty"])
	}
}

// A value longer than a byte type holds is refused: the tag is Bad and the
// error names it, never a truncated value.
func TestByteTypeTooLongIsBadNotTruncated(t *testing.T) {
	ts := startServer(t, freePort(t))
	addBytes(t, ts, "Max", make([]byte, MaxBytes), false)
	addBytes(t, ts, "Over", make([]byte, MaxBytes+1), false)
	d := startDriver(t, ts.url, byteManifest(
		TagBinding{Name: "Max", NodeID: "ns=2;s=Max", Type: ByteString},
		TagBinding{Name: "Over", NodeID: "ns=2;s=Over", Type: ByteString},
	))
	waitFor(t, "Over bad", func() bool { return d.Quality()["Over"] == nio.Bad })
	v := read(t, d)
	if s, _ := v["Max"].(string); len(s) != 2*MaxBytes {
		t.Errorf("a value at the limit read %d hex digits, want %d", len(s), 2*MaxBytes)
	}
	if _, has := v["Over"]; has {
		t.Errorf("Over delivered %v", v["Over"])
	}
	if e := d.Health().LastError; !strings.Contains(e, "Over") || !strings.Contains(e, "do not fit") {
		t.Errorf("lastError %q does not name Over and why", e)
	}
}

// A byte-typed binding on a node that is not octets is Bad, and a scalar
// binding on an octet node is Bad: the declared type decides.
func TestByteTypeMismatchIsBad(t *testing.T) {
	ts := startServer(t, freePort(t))
	addBytes(t, ts, "Pd", []byte{1, 2}, false)
	d := startDriver(t, ts.url, byteManifest(
		TagBinding{Name: "CountAsBytes", NodeID: "ns=2;s=Count", Type: ByteString},
		TagBinding{Name: "PdAsInt", NodeID: "ns=2;s=Pd", Type: "Int32"},
	))
	waitFor(t, "both bad", func() bool {
		q := d.Quality()
		return q["CountAsBytes"] == nio.Bad && q["PdAsInt"] == nio.Bad
	})
}

// Writes: hex text in either case goes out as the declared shape, a
// bad one is refused before it reaches the wire.
func TestByteTypeWrite(t *testing.T) {
	for _, c := range []struct {
		typ  string
		in   string
		want any
	}{
		{ByteString, "0286Ab", []byte{0x02, 0x86, 0xAB}},
		{ByteArray, "0286ab", ua.ByteArray{0x02, 0x86, 0xAB}},
		{ByteString, "", []byte{}},
	} {
		v, err := toVariant(c.typ, c.in)
		if err != nil {
			t.Fatalf("%s %q: %v", c.typ, c.in, err)
		}
		if c.typ == ByteArray && v.Type() != ua.TypeIDByte {
			t.Errorf("ByteArray encoded as %v, want an array of Byte", v.Type())
		}
		if c.typ == ByteString && v.Type() != ua.TypeIDByteString {
			t.Errorf("ByteString encoded as %v", v.Type())
		}
		if s, _ := bytesToHex(v); s != strings.ToLower(c.in) {
			t.Errorf("%s %q round-trips as %q", c.typ, c.in, s)
		}
	}
	for _, bad := range []string{"028", "zz", "0x0286", "02 86", strings.Repeat("00", MaxBytes+1)} {
		if _, err := toVariant(ByteString, bad); err == nil {
			t.Errorf("%.20q written without error", bad)
		}
	}
	if _, err := toVariant(ByteString, int64(5)); err == nil {
		t.Error("an integer written as a byte value")
	}
}

// A byte value written through the driver reaches the server as octets.
func TestByteTypeWriteReachesTheServer(t *testing.T) {
	ts := startServer(t, freePort(t))
	addBytes(t, ts, "Out", []byte{0, 0}, true)
	m := byteManifest(TagBinding{Name: "Out", NodeID: "ns=2;s=Out", Type: ByteString, Writable: true})
	d := startDriver(t, ts.url, m)
	waitFor(t, "online", d.Online)
	_ = d.WriteOutputs(nio.Values{"Out": "0000"}) // baseline, as the other write tests
	_ = d.WriteOutputs(nio.Values{"Out": "BEEF"})
	waitFor(t, "write", func() bool {
		b, _ := ts.get(t, "Out").([]byte)
		return len(b) == 2 && b[0] == 0xBE && b[1] == 0xEF
	})
}
