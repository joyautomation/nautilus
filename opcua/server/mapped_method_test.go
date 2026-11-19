package server

import (
	"context"
	"testing"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/runtime"
)

// A mapped Method is a Method of its parent Object: browsable, executable,
// and a Call of it on that object runs its handler with the input arguments
// and answers its outputs or its status; the same method on another object,
// or a node that is not a method, is not a call (#705: ISDU is a pair of
// methods on a port device's object).
func TestMappedMethodIsCalled(t *testing.T) {
	var gotArgs []any
	_, c := bytesFixture(t, []runtime.TagDef{runtime.State("T", "00")},
		[]MappedNode{
			{NodeID: "ns=1;s=Dev", Name: "Dev"},
			{NodeID: "ns=1;s=Other", Name: "Other"},
			{NodeID: "ns=1;s=Dev/Echo", Parent: "ns=1;s=Dev", Name: "Echo",
				Method: func(args []*ua.Variant) ([]*ua.Variant, ua.StatusCode) {
					gotArgs = nil
					for _, a := range args {
						gotArgs = append(gotArgs, a.Value())
					}
					if len(args) != 2 {
						return nil, ua.StatusBadArgumentsMissing
					}
					out, _ := ua.NewVariant(ua.ByteArray{0xAB, 0xCD})
					return []*ua.Variant{out}, ua.StatusOK
				}},
			{NodeID: "ns=1;s=T", Parent: "ns=1;s=Dev", Name: "T", Tag: "T"},
		})
	call := func(obj, meth string, args ...*ua.Variant) *ua.CallMethodResult {
		t.Helper()
		resp, err := c.Call(context.Background(), &ua.CallMethodRequest{ObjectID: ua.MustParseNodeID(obj), MethodID: ua.MustParseNodeID(meth), InputArguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	idx, _ := ua.NewVariant(uint16(64))
	sub, _ := ua.NewVariant(uint8(1))

	r := call("ns=1;s=Dev", "ns=1;s=Dev/Echo", idx, sub)
	if r.StatusCode != ua.StatusOK || len(r.OutputArguments) != 1 {
		t.Fatalf("call: %v %v", r.StatusCode, r.OutputArguments)
	}
	var out []byte
	switch b := r.OutputArguments[0].Value().(type) {
	case []byte:
		out = b
	case ua.ByteArray:
		out = b
	}
	if string(out) != "\xab\xcd" || !r.OutputArguments[0].Has(ua.VariantArrayValues) {
		t.Errorf("output %#v, want the Byte[] ab cd", r.OutputArguments[0].Value())
	}
	if len(gotArgs) != 2 || gotArgs[0] != uint16(64) || gotArgs[1] != uint8(1) {
		t.Errorf("the handler saw %v", gotArgs)
	}
	if r := call("ns=1;s=Dev", "ns=1;s=Dev/Echo", idx); r.StatusCode != ua.StatusBadArgumentsMissing {
		t.Errorf("a bad call: %v, want the handler's status", r.StatusCode)
	}
	if r := call("ns=1;s=Other", "ns=1;s=Dev/Echo", idx, sub); r.StatusCode == ua.StatusOK {
		t.Errorf("the method called on an object that is not its parent: %v", r.StatusCode)
	}
	if r := call("ns=1;s=Dev", "ns=1;s=T", idx, sub); r.StatusCode == ua.StatusOK {
		t.Errorf("a Variable called as a method: %v", r.StatusCode)
	}

	// It is a Method node: class, executable, and a child of the object.
	if got := readNode(t, c, "ns=1;s=Dev/Echo", ua.AttributeIDNodeClass).Value.Value(); got != int32(ua.NodeClassMethod) {
		t.Errorf("NodeClass %v, want Method", got)
	}
	if got := readNode(t, c, "ns=1;s=Dev/Echo", ua.AttributeIDExecutable).Value.Value(); got != true {
		t.Errorf("Executable %v", got)
	}
	found := false
	for name, class := range browseClasses(t, c, "ns=1;s=Dev") {
		if name == "Echo" && class == ua.NodeClassMethod {
			found = true
		}
	}
	if !found {
		t.Error("the object does not list the Method")
	}
	if _, err := newMappedTree(&Mapped{Namespaces: []string{"u"}, Nodes: []MappedNode{
		{NodeID: "ns=1;s=M", Name: "M", Method: func([]*ua.Variant) ([]*ua.Variant, ua.StatusCode) { return nil, ua.StatusOK }},
	}}); err == nil {
		t.Error("a Method with no parent was accepted")
	}
	_ = id.HasComponent
}

func browseClasses(t *testing.T, c interface {
	Browse(context.Context, *ua.BrowseRequest) (*ua.BrowseResponse, error)
}, nid string) map[string]ua.NodeClass {
	t.Helper()
	resp, err := c.Browse(context.Background(), &ua.BrowseRequest{NodesToBrowse: []*ua.BrowseDescription{{
		NodeID: ua.MustParseNodeID(nid), BrowseDirection: ua.BrowseDirectionForward,
		ReferenceTypeID: ua.NewNumericNodeID(0, id.HierarchicalReferences), IncludeSubtypes: true, ResultMask: uint32(ua.BrowseResultMaskAll),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ua.NodeClass{}
	for _, r := range resp.Results[0].References {
		out[r.BrowseName.Name] = r.NodeClass
	}
	return out
}
