package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/internal/gopcuatest"
	nio "github.com/joyautomation/nautilus/io"
	opcuaclient "github.com/joyautomation/nautilus/opcua"
	"github.com/joyautomation/nautilus/runtime"
)

// A STRING tag mapped with Wire: ByteString holds octets as hex and is
// served as a ByteString (#705): its DataType says so, a read returns the
// octets, and a ByteString written to it is stored as lowercase hex.
func TestMappedByteStringWire(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM Main\nEND_PROGRAM\n",
		Driver:  nio.NewMemory(),
		Scan:    20 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.State("B_Pdi", "0286ab"),
			runtime.Setpoint("B_Pdo", "0000"),
			runtime.State("B_Bad", "zz"),
			runtime.State("B_Text", "plain"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &Mapped{
		Namespaces: []string{"urn:test:bytes"},
		Nodes: []MappedNode{
			{NodeID: "ns=1;s=Pdi", Name: "Pdi", Tag: "B_Pdi", Wire: "ByteString"},
			{NodeID: "ns=1;s=Pdo", Name: "Pdo", Tag: "B_Pdo", Wire: "ByteString"},
			{NodeID: "ns=1;s=Bad", Name: "Bad", Tag: "B_Bad", Wire: "ByteString"},
			{NodeID: "ns=1;s=Text", Name: "Text", Tag: "B_Text"},
		},
	}
	s, err := New(rt, Options{Endpoint: "localhost:0", Name: "virtual", Sweep: 20 * time.Millisecond,
		Mapped: m, Writable: []string{"B_Pdo"}, AnonymousWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	go rt.Run(ctx)
	c, err := opcua.NewClient(s.URL(), opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := gopcuatest.Connect(ctx, c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		c.Close(cctx)
	})

	dv := readNode(t, c, "ns=1;s=Pdi", ua.AttributeIDValue)
	if b, ok := dv.Value.Value().([]byte); !ok || string(b) != "\x02\x86\xab" || dv.Value.Type() != ua.TypeIDByteString {
		t.Fatalf("Pdi = %#v (%v), want the ByteString 02 86 ab", dv.Value.Value(), dv.Value.Type())
	}
	if got := readNode(t, c, "ns=1;s=Pdi", ua.AttributeIDDataType).Value.Value(); got.(*ua.NodeID).IntID() != id.ByteString {
		t.Errorf("Pdi DataType = %v, want ByteString", got)
	}
	if got := readNode(t, c, "ns=1;s=Text", ua.AttributeIDDataType).Value.Value(); got.(*ua.NodeID).IntID() != id.String {
		t.Errorf("a node without Wire has DataType %v, want String", got)
	}
	// A tag that is not hex has no octets to serve: not a value.
	if bad := readNode(t, c, "ns=1;s=Bad", ua.AttributeIDValue); bad.Status == ua.StatusOK && bad.Value != nil {
		t.Errorf("Bad (not hex) read as %v with status %v", bad.Value.Value(), bad.Status)
	}

	write := func(nid string, v any) ua.StatusCode {
		variant, err := ua.NewVariant(v)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Write(ctx, &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{
			NodeID: ua.MustParseNodeID(nid), AttributeID: ua.AttributeIDValue,
			Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: variant},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Results[0]
	}
	if st := write("ns=1;s=Pdo", []byte{0xBE, 0xEF}); st != ua.StatusOK {
		t.Fatalf("write ByteString: %v", st)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		v, _ := rt.Tags().ReadGlobal("B_Pdo")
		if v.S == "beef" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("B_Pdo = %q, want \"beef\" (lowercase hex)", v.S)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := write("ns=1;s=Pdo", "beef"); st != ua.StatusBadTypeMismatch {
		t.Errorf("a String written to a ByteString node: %v, want Bad_TypeMismatch", st)
	}
	if _, err := newMappedTree(&Mapped{Namespaces: []string{"u"}, Nodes: []MappedNode{{NodeID: "ns=1;s=O", Name: "O", Wire: "ByteString"}}}); err == nil {
		t.Error("an Object with a Wire was accepted")
	}
}

func bytesFixture(t *testing.T, tags []runtime.TagDef, nodes []MappedNode, writable ...string) (*runtime.Runtime, *opcua.Client) {
	t.Helper()
	return bytesFixtureWith(t, true, tags, nodes, writable...)
}

// bytesFixtureWith is bytesFixture with the anonymous session a writer or not
// (opcua.anonymous-writes).
func bytesFixtureWith(t *testing.T, anonymousWrites bool, tags []runtime.TagDef, nodes []MappedNode, writable ...string) (*runtime.Runtime, *opcua.Client) {
	t.Helper()
	rt, err := runtime.New(runtime.Options{Program: "PROGRAM Main\nEND_PROGRAM\n", Driver: nio.NewMemory(), Scan: 20 * time.Millisecond, Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(rt, Options{Endpoint: "localhost:0", Name: "virtual", Sweep: 20 * time.Millisecond,
		Mapped: &Mapped{Namespaces: []string{"urn:test:bytes"}, Nodes: nodes}, Writable: writable, AnonymousWrites: anonymousWrites})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	go rt.Run(ctx)
	c, err := opcua.NewClient(s.URL(), opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := gopcuatest.Connect(ctx, c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		c.Close(cctx)
	})
	return rt, c
}

func writeNode(t *testing.T, c *opcua.Client, nid string, v *ua.Variant) ua.StatusCode {
	t.Helper()
	resp, err := c.Write(context.Background(), &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{
		NodeID: ua.MustParseNodeID(nid), AttributeID: ua.AttributeIDValue,
		Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: v},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Results[0]
}

// A byte node carries at most 232 octets, in both directions, whatever its
// wire shape: an over-long tag value reads Bad, an over-long write is
// refused Bad_OutOfRange and stores nothing. 232 itself is carried.
func TestMappedByteNodesAreCapped(t *testing.T) {
	if maxBytes != opcuaclient.MaxBytes {
		t.Fatalf("the server's cap %d and the client's %d differ", maxBytes, opcuaclient.MaxBytes)
	}
	fit, over := strings.Repeat("ab", maxBytes), strings.Repeat("ab", maxBytes+1)
	rt, c := bytesFixture(t,
		[]runtime.TagDef{runtime.State("Fit", fit), runtime.State("Over", over), runtime.State("OverArr", over),
			runtime.Setpoint("Out", "00"), runtime.Setpoint("OutArr", "00")},
		[]MappedNode{
			{NodeID: "ns=1;s=Fit", Name: "Fit", Tag: "Fit", Wire: "ByteString"},
			{NodeID: "ns=1;s=Over", Name: "Over", Tag: "Over", Wire: "ByteString"},
			{NodeID: "ns=1;s=OverArr", Name: "OverArr", Tag: "OverArr", Wire: "ByteArray"},
			{NodeID: "ns=1;s=Out", Name: "Out", Tag: "Out", Wire: "ByteString"},
			{NodeID: "ns=1;s=OutArr", Name: "OutArr", Tag: "OutArr", Wire: "ByteArray"},
		}, "Out", "OutArr")
	if dv := readNode(t, c, "ns=1;s=Fit", ua.AttributeIDValue); dv.Status != ua.StatusOK || len(dv.Value.Value().([]byte)) != maxBytes {
		t.Errorf("a value of %d octets: status %v", maxBytes, dv.Status)
	}
	for _, n := range []string{"Over", "OverArr"} {
		if dv := readNode(t, c, "ns=1;s="+n, ua.AttributeIDValue); dv.Status == ua.StatusOK && dv.Value != nil {
			t.Errorf("%s: %d octets read as a good value", n, maxBytes+1)
		}
	}
	long := make([]byte, maxBytes+1)
	vs, _ := ua.NewVariant(long)
	va, _ := ua.NewVariant(ua.ByteArray(long))
	if st := writeNode(t, c, "ns=1;s=Out", vs); st != ua.StatusBadOutOfRange {
		t.Errorf("a ByteString of %d octets written: %v, want Bad_OutOfRange", len(long), st)
	}
	if st := writeNode(t, c, "ns=1;s=OutArr", va); st != ua.StatusBadOutOfRange {
		t.Errorf("a Byte[] of %d octets written: %v, want Bad_OutOfRange", len(long), st)
	}
	for _, n := range []string{"Out", "OutArr"} {
		if v, _ := rt.Tags().ReadGlobal(n); v.S != "00" {
			t.Errorf("%s holds %q after a refused write", n, v.S)
		}
	}
	short, _ := ua.NewVariant(make([]byte, maxBytes))
	if st := writeNode(t, c, "ns=1;s=Out", short); st != ua.StatusOK {
		t.Errorf("a ByteString of %d octets written: %v", maxBytes, st)
	}
}

// A Byte[] node: DataType Byte, ValueRank 1, read as an array of Byte, and a
// Byte[] written to it is stored as hex. A ByteString written to it, and a
// Byte[] written to a ByteString node, are type mismatches.
func TestMappedByteArrayNode(t *testing.T) {
	rt, c := bytesFixture(t,
		[]runtime.TagDef{runtime.State("Pdi", "0286ab"), runtime.Setpoint("Pdo", "00"), runtime.Setpoint("Str", "00")},
		[]MappedNode{
			{NodeID: "ns=1;s=Pdi", Name: "Pdi", Tag: "Pdi", Wire: "ByteArray"},
			{NodeID: "ns=1;s=Pdo", Name: "Pdo", Tag: "Pdo", Wire: "ByteArray"},
			{NodeID: "ns=1;s=Str", Name: "Str", Tag: "Str", Wire: "ByteString"},
		}, "Pdo", "Str")
	dv := readNode(t, c, "ns=1;s=Pdi", ua.AttributeIDValue)
	if !dv.Value.Has(ua.VariantArrayValues) || dv.Value.Type() != ua.TypeIDByte {
		t.Fatalf("Pdi read as %v array=%v, want a Byte array", dv.Value.Type(), dv.Value.Has(ua.VariantArrayValues))
	}
	if got := readNode(t, c, "ns=1;s=Pdi", ua.AttributeIDDataType).Value.Value().(*ua.NodeID).IntID(); got != id.Byte {
		t.Errorf("DataType %d, want Byte", got)
	}
	if got := readNode(t, c, "ns=1;s=Pdi", ua.AttributeIDValueRank).Value.Value(); got != int32(1) {
		t.Errorf("ValueRank %v, want 1", got)
	}
	arr, _ := ua.NewVariant(ua.ByteArray{0xBE, 0xEF})
	if st := writeNode(t, c, "ns=1;s=Pdo", arr); st != ua.StatusOK {
		t.Fatalf("a Byte[] written to a Byte[] node: %v", st)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if v, _ := rt.Tags().ReadGlobal("Pdo"); v.S == "beef" {
			break
		}
		if time.Now().After(deadline) {
			v, _ := rt.Tags().ReadGlobal("Pdo")
			t.Fatalf("Pdo = %q, want \"beef\"", v.S)
		}
		time.Sleep(10 * time.Millisecond)
	}
	str, _ := ua.NewVariant([]byte{1})
	if st := writeNode(t, c, "ns=1;s=Pdo", str); st != ua.StatusBadTypeMismatch {
		t.Errorf("a ByteString written to a Byte[] node: %v, want Bad_TypeMismatch", st)
	}
	if st := writeNode(t, c, "ns=1;s=Str", arr); st != ua.StatusBadWriteNotSupported {
		t.Errorf("a Byte[] written to a ByteString node: %v, want Bad_WriteNotSupported", st)
	}
}
