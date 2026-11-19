package opcua

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"
)

// A method call goes out on the driver's own session, with the node ids
// resolved against the live namespace table (a URI follows a renumbering),
// and the output arguments come back; a bad status of the call or of an
// argument, and a call with no session, are errors that say so.
func TestDriverCall(t *testing.T) {
	var sawObject, sawMethod string
	var sawArgs []any
	status := ua.StatusOK
	var argStatus []ua.StatusCode
	ts := startServerHooked(t, freePort(t), "urn:nautilus:test", 12.5, func(s *server.Server) {
		s.RegisterHandler(id.CallRequest_Encoding_DefaultBinary,
			func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
				req := r.(*ua.CallRequest)
				c := req.MethodsToCall[0]
				sawObject, sawMethod = c.ObjectID.String(), c.MethodID.String()
				sawArgs = nil
				for _, a := range c.InputArguments {
					sawArgs = append(sawArgs, a.Value())
				}
				out, _ := ua.NewVariant([]byte{0x01, 0x02})
				return &ua.CallResponse{
					ResponseHeader: &ua.ResponseHeader{Timestamp: time.Now(), RequestHandle: req.RequestHeader.RequestHandle, ServiceResult: ua.StatusOK,
						ServiceDiagnostics: &ua.DiagnosticInfo{}, StringTable: []string{}, AdditionalHeader: ua.NewExtensionObject(nil)},
					Results: []*ua.CallMethodResult{{StatusCode: status, InputArgumentResults: argStatus, OutputArguments: []*ua.Variant{out}}},
				}, nil
			})
	})
	d, err := New(ts.url, manifest(2), fastOpts()...)
	if err != nil {
		t.Fatal(err)
	}
	obj, meth := NodeRef{NodeID: "ns=0;s=Plant", Namespace: "urn:nautilus:test"}, NodeRef{NodeID: "ns=9;s=Read", Namespace: "urn:nautilus:test"}
	idx, _ := ua.NewVariant(uint16(64))
	sub, _ := ua.NewVariant(uint8(0))

	if _, err := d.Call(context.Background(), obj, meth, idx, sub); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("a call before any session: %v, want not connected", err)
	}
	startHooked(t, d)
	waitFor(t, "online", d.Online)

	out, err := d.Call(context.Background(), obj, meth, idx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || string(out[0].Value().([]byte)) != "\x01\x02" {
		t.Errorf("outputs %v", out)
	}
	// The URI decided the index: the server's urn:nautilus:test is index 1
	// whatever the node ids wrote (0 and 9).
	if sawObject != "ns=1;s=Plant" || sawMethod != "ns=1;s=Read" {
		t.Errorf("the server saw %s on %s, want both in ns=1", sawMethod, sawObject)
	}
	if len(sawArgs) != 2 || sawArgs[0] != uint16(64) || sawArgs[1] != uint8(0) {
		t.Errorf("the server saw arguments %v", sawArgs)
	}
	if _, err := d.Call(context.Background(), NodeRef{NodeID: "ns=0;s=Plant", Namespace: "urn:not:there"}, meth); err == nil || !strings.Contains(err.Error(), "urn:not:there") {
		t.Errorf("a namespace the server lacks: %v", err)
	}
	status = ua.StatusBadNotExecutable
	if _, err := d.Call(context.Background(), obj, meth, idx, sub); err == nil || !strings.Contains(err.Error(), "NotExecutable") {
		t.Errorf("a refused call: %v", err)
	}
	status, argStatus = ua.StatusOK, []ua.StatusCode{ua.StatusOK, ua.StatusBadTypeMismatch}
	if _, err := d.Call(context.Background(), obj, meth, idx, sub); err == nil || !strings.Contains(err.Error(), "argument 2") {
		t.Errorf("a bad argument: %v", err)
	}
}

// A call that was sent and got no answer is of a kind of its own: the server
// may have run it. One never sent (no session), and one the server answered
// with a failure, are certain.
func TestDriverCallErrorKinds(t *testing.T) {
	answer := make(chan struct{})
	var mode string
	ts := startServerHooked(t, freePort(t), "urn:nautilus:test", 12.5, func(s *server.Server) {
		s.RegisterHandler(id.CallRequest_Encoding_DefaultBinary,
			func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
				req := r.(*ua.CallRequest)
				switch mode {
				case "silent":
					<-answer // never answers within the call's timeout
				case "fault":
					return nil, ua.StatusBadServiceUnsupported
				}
				return &ua.CallResponse{
					ResponseHeader: &ua.ResponseHeader{Timestamp: time.Now(), RequestHandle: req.RequestHeader.RequestHandle, ServiceResult: ua.StatusOK,
						ServiceDiagnostics: &ua.DiagnosticInfo{}, StringTable: []string{}, AdditionalHeader: ua.NewExtensionObject(nil)},
					Results: []*ua.CallMethodResult{{StatusCode: ua.StatusOK}},
				}, nil
			})
	})
	t.Cleanup(func() { close(answer) })
	d, err := New(ts.url, manifest(2), fastOpts(WithTimeout(500*time.Millisecond))...)
	if err != nil {
		t.Fatal(err)
	}
	obj, meth := NodeRef{NodeID: "ns=1;s=Plant"}, NodeRef{NodeID: "ns=1;s=Read"}
	if _, err := d.Call(context.Background(), obj, meth); err == nil || errors.Is(err, ErrCallUnknown) {
		t.Errorf("a call with no session: %v, want a certain failure", err)
	}
	startHooked(t, d)
	waitFor(t, "online", d.Online)
	if _, err := d.Call(context.Background(), NodeRef{NodeID: "ns=1;s=Plant", Namespace: "urn:not:there"}, meth); err == nil || errors.Is(err, ErrCallUnknown) {
		t.Errorf("an id that does not resolve: %v, want a certain failure", err)
	}
	mode = "fault"
	if _, err := d.Call(context.Background(), obj, meth); err == nil || errors.Is(err, ErrCallUnknown) {
		t.Errorf("a service fault: %v, want the server's own answer, certain", err)
	}
	mode = "silent"
	if _, err := d.Call(context.Background(), obj, meth); !errors.Is(err, ErrCallUnknown) {
		t.Errorf("a call that was sent and not answered: %v, want ErrCallUnknown", err)
	}
}
