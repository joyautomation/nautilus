package server

import (
	"context"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/internal/gopcuatest"
	nio "github.com/joyautomation/nautilus/io"
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
