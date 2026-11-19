//go:build !js

package devbind

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/iolink"
	"github.com/joyautomation/nautilus/opcua"
)

// fakeCaller answers ISDU calls and records them.
type fakeCaller struct {
	calls   []fakeCall
	answer  func(method string, args []*ua.Variant) ([]*ua.Variant, error)
	respond []*ua.Variant
}

type fakeCall struct {
	object, method opcua.NodeRef
	args           []any
}

func (f *fakeCaller) Call(_ context.Context, object, method opcua.NodeRef, args ...*ua.Variant) ([]*ua.Variant, error) {
	c := fakeCall{object: object, method: method}
	for _, a := range args {
		c.args = append(c.args, a.Value())
	}
	f.calls = append(f.calls, c)
	return f.answer(method.NodeID, args)
}

func variants(t *testing.T, vs ...any) []*ua.Variant {
	t.Helper()
	var out []*ua.Variant
	for _, v := range vs {
		x, err := ua.NewVariant(v)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, x)
	}
	return out
}

func isduPorts(t *testing.T, profile string, params map[string]string) *ports {
	t.Helper()
	fsys := portFS(t, profile)
	s, err := Resolve(fsys, []Device{{Name: "M", Profile: profile, Endpoint: "h", Params: params,
		Ports: map[int]Port{1: {Name: "LEVEL1", IODD: levelFile}}}})
	if err != nil {
		t.Fatal(err)
	}
	return newPorts(s.devices[0].ports)
}

// sickVals are the master's members for the SIG300 profile's port 1.
func sickVals(vendor, device int64) nio.Values {
	return nio.Values{"M.Port1_PdiBytes": levelImage, "M.Port1_VendorID": vendor, "M.Port1_DeviceID": device}
}

func readOK(t *testing.T, data any) func(string, []*ua.Variant) ([]*ua.Variant, error) {
	return func(string, []*ua.Variant) ([]*ua.Variant, error) {
		return variants(t, data, uint16(0), int32(0)), nil
	}
}

func TestISDUReadThroughOPC30120(t *testing.T) {
	ps := isduPorts(t, "sick-sig300", nil)
	ps.take(sickVals(levelVendor, levelDevice))
	c := &fakeCaller{answer: func(string, []*ua.Variant) ([]*ua.Variant, error) {
		return variants(t, ua.ByteArray{0x03, 0xE8}, uint16(0), int32(0)), nil
	}}
	res, err := ps.isduRead(context.Background(), c, "level1", "V_Setpoint")
	if err != nil {
		t.Fatal(err)
	}
	if res.Value != uint64(1000) || res.Index != 64 || res.Subindex != 0 || res.Identity != iolink.Ok || !res.Checked {
		t.Errorf("result %+v", res)
	}
	call := c.calls[0]
	if call.object.NodeID != "ns=3;s=SIG300/Port 1/Device" || call.method.NodeID != "ns=3;s=SIG300/Port 1/Device/ReadISDU" || call.method.Namespace != "" || call.object.Namespace != "" {
		t.Errorf("called %+v on %+v (ns=3 is the profile's placeholder namespace: bound by index)", call.method, call.object)
	}
	if len(call.args) != 2 || call.args[0] != uint16(64) || call.args[1] != uint8(0) {
		t.Errorf("arguments %v, want index 64, subindex 0", call.args)
	}
	// A record item is read at its subindex (what the device answers is
	// the item's width, the IODD's; the request is what is checked here).
	c2 := &fakeCaller{answer: readOK(t, ua.ByteArray{0, 5})}
	_, _ = ps.isduRead(context.Background(), c2, "LEVEL1", "Window.2")
	if len(c2.calls) != 1 || c2.calls[0].args[0] != uint16(65) || c2.calls[0].args[1] != uint8(2) {
		t.Errorf("a record item was read with %v, want index 65 subindex 2", c2.calls)
	}
}

// A read may run in any state: it helps find out which device is there, so
// the answer says which.
func TestISDUReadInAnyStateSaysTheIdentity(t *testing.T) {
	for state, vals := range map[iolink.State]nio.Values{
		iolink.WrongDevice: sickVals(levelVendor, levelDevice+100),
		iolink.NoDevice:    sickVals(0, 0),
	} {
		ps := isduPorts(t, "sick-sig300", nil)
		ps.take(vals)
		c := &fakeCaller{answer: readOK(t, ua.ByteArray{0x03, 0xE8})}
		res, err := ps.isduRead(context.Background(), c, "LEVEL1", "V_Setpoint")
		if err != nil || res.Identity != state || !res.Checked || res.Value != uint64(1000) {
			t.Errorf("%v: %+v, %v", state, res, err)
		}
	}
	// Before the master has delivered its ids: read, not checked.
	ps := isduPorts(t, "sick-sig300", nil)
	res, err := ps.isduRead(context.Background(), &fakeCaller{answer: readOK(t, ua.ByteArray{0x03, 0xE8})}, "LEVEL1", "V_Setpoint")
	if err != nil || res.Checked {
		t.Errorf("before any check: %+v, %v", res, err)
	}
}

func TestISDUWriteIsRefusedUnlessTheIdentityIsOk(t *testing.T) {
	for name, tc := range map[string]struct {
		vals nio.Values
		want string
	}{
		"wrong device": {sickVals(levelVendor, levelDevice+100), "WrongDevice"},
		"no device":    {sickVals(0, 0), "NoDevice"},
		"not checked":  {nil, "has not been checked"},
	} {
		ps := isduPorts(t, "sick-sig300", nil)
		if tc.vals != nil {
			ps.take(tc.vals)
		}
		c := &fakeCaller{answer: func(string, []*ua.Variant) ([]*ua.Variant, error) {
			return variants(t, uint16(0), int32(0)), nil
		}}
		_, err := ps.isduWrite(context.Background(), c, "LEVEL1", "V_Setpoint", uint64(500))
		if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want a refusal naming %q", name, err, tc.want)
		}
		if len(c.calls) != 0 {
			t.Errorf("%s: %d calls reached the master", name, len(c.calls))
		}
	}
}

func TestISDUWriteThroughOPC30120(t *testing.T) {
	ps := isduPorts(t, "sick-sig300", nil)
	ps.take(sickVals(levelVendor, levelDevice))
	c := &fakeCaller{answer: func(string, []*ua.Variant) ([]*ua.Variant, error) {
		return variants(t, uint16(0), int32(0)), nil
	}}
	res, err := ps.isduWrite(context.Background(), c, "LEVEL1", "V_Setpoint", uint64(500))
	if err != nil {
		t.Fatal(err)
	}
	call := c.calls[0]
	if call.method.NodeID != "ns=3;s=SIG300/Port 1/Device/WriteISDU" || len(call.args) != 3 {
		t.Fatalf("call %+v", call)
	}
	if b, _ := call.args[2].(ua.ByteArray); string(b) != "\x01\xf4" {
		t.Errorf("data %#v, want the Byte[] 01 F4", call.args[2])
	}
	if res.Value != uint64(500) || res.Identity != iolink.Ok {
		t.Errorf("result %+v", res)
	}
	// Refused before anything is sent: outside the range, read-only,
	// unknown, a record with no item.
	for ref, v := range map[string]any{"V_Setpoint": uint64(1001), "V_SerialNumber": "x", "V_Nothing": uint64(1), "V_Window": uint64(1)} {
		c := &fakeCaller{answer: func(string, []*ua.Variant) ([]*ua.Variant, error) { return variants(t, uint16(0), int32(0)), nil }}
		if _, err := ps.isduWrite(context.Background(), c, "LEVEL1", ref, v); err == nil {
			t.Errorf("%s: accepted", ref)
		}
		if len(c.calls) != 0 {
			t.Errorf("%s: a refused write reached the master", ref)
		}
	}
}

// A bad status from the master is an error that says so, with OPC 30120's
// meaning and the device's IO-Link ErrorType.
func TestISDUStatusIsAnError(t *testing.T) {
	ps := isduPorts(t, "sick-sig300", nil)
	ps.take(sickVals(levelVendor, levelDevice))
	c := &fakeCaller{answer: func(string, []*ua.Variant) ([]*ua.Variant, error) {
		return variants(t, ua.ByteArray(nil), uint16(0x8011), int32(-3)), nil
	}}
	_, err := ps.isduRead(context.Background(), c, "LEVEL1", "V_Setpoint")
	if err == nil || !strings.Contains(err.Error(), "0x8011") || !strings.Contains(err.Error(), "device refused") {
		t.Errorf("a device's refusal: %v", err)
	}
	c.answer = func(string, []*ua.Variant) ([]*ua.Variant, error) { return variants(t, uint16(0)), nil }
	if _, err := ps.isduRead(context.Background(), c, "LEVEL1", "V_Setpoint"); err == nil || !strings.Contains(err.Error(), "output arguments") {
		t.Errorf("a short answer: %v", err)
	}
}

// The ifm master carries a value as hex text, at most 32 octets.
func TestISDUThroughTheIfmMethods(t *testing.T) {
	ps := isduPorts(t, "ifm-al1590", map[string]string{"location": "X"})
	ps.take(nio.Values{"M.Port1_Pdin": "FE0DFFD1", "M.Port1_VendorID": int64(levelVendor), "M.Port1_DeviceID": int64(levelDevice)})
	c := &fakeCaller{answer: func(string, []*ua.Variant) ([]*ua.Variant, error) {
		return variants(t, "0x03e8", uint16(0), uint32(0)), nil
	}}
	res, err := ps.isduRead(context.Background(), c, "LEVEL1", "V_Setpoint")
	if err != nil || res.Value != uint64(1000) {
		t.Fatalf("read: %+v, %v", res, err)
	}
	if got := c.calls[0].method.NodeID; got != "ns=5;s=X/iolinkmaster/port[1]/iolinkdevice/MethodSet/ReadISDU" {
		t.Errorf("method %s", got)
	}
	c.answer = func(string, []*ua.Variant) ([]*ua.Variant, error) { return variants(t, uint16(0), uint32(0)), nil }
	if _, err := ps.isduWrite(context.Background(), c, "LEVEL1", "V_Setpoint", uint64(500)); err != nil {
		t.Fatal(err)
	}
	if got := c.calls[len(c.calls)-1].args[2]; got != "0x01f4" {
		t.Errorf("data %#v, want the hex text 0x01f4", got)
	}
	// 64 octets do not fit 64 hex digits: refused before any call, naming
	// the limit, never cut.
	n := len(c.calls)
	_, err = ps.isduRead(context.Background(), c, "LEVEL1", "V_VendorName")
	if err == nil || !strings.Contains(err.Error(), "at most 32") || len(c.calls) != n {
		t.Errorf("a 64-octet parameter through the ifm: %v (calls %d)", err, len(c.calls)-n)
	}
}

func TestISDUWithoutMethodsOrPort(t *testing.T) {
	ps := portsOf(t, pfMaster(t, nil)) // the P+F's isdu: is none
	if _, err := ps.isduRead(context.Background(), &fakeCaller{}, "LEVEL1", "V_Setpoint"); err == nil || !strings.Contains(err.Error(), "offers no ISDU") {
		t.Errorf("a master with no isdu:: %v", err)
	}
	ps = isduPorts(t, "sick-sig300", nil)
	if _, err := ps.isduRead(context.Background(), &fakeCaller{}, "NOPE", "V_Setpoint"); err == nil || !strings.Contains(err.Error(), `no port device "NOPE"`) {
		t.Errorf("an unknown port device: %v", err)
	}
}

// Each refusal is of a kind the controller's API answers as it should:
// 404 for what does not exist, 422 for what the IODD forbids, 409 for a port
// that cannot take it now.
func TestISDURefusalKinds(t *testing.T) {
	ps := isduPorts(t, "sick-sig300", nil)
	ps.take(sickVals(levelVendor, levelDevice+100)) // the wrong device
	ctx := context.Background()
	c := &fakeCaller{answer: readOK(t, ua.ByteArray{0x03, 0xE8})}
	check := func(name string, err error, want error) {
		t.Helper()
		if !errors.Is(err, want) {
			t.Errorf("%s: %v, want a %v", name, err, want)
		}
	}
	_, err := ps.isduRead(ctx, c, "NOPE", "V_Setpoint")
	check("an unknown port device", err, ErrISDUUnknown)
	_, err = ps.isduRead(ctx, c, "LEVEL1", "V_Nothing")
	check("an unknown variable", err, ErrISDUUnknown)
	_, err = ps.isduRead(ctx, c, "LEVEL1", "V_SystemCommand")
	check("a read of a write-only variable", err, ErrISDUInvalid)
	_, err = ps.isduWrite(ctx, c, "LEVEL1", "V_Setpoint", uint64(500))
	check("a write to the wrong device", err, ErrISDURefused)
	ps.take(sickVals(levelVendor, levelDevice)) // the right one
	_, err = ps.isduWrite(ctx, c, "LEVEL1", "V_Setpoint", uint64(2000))
	check("a value outside the range", err, ErrISDUInvalid)
	_, err = ps.isduWrite(ctx, c, "LEVEL1", "V_SerialNumber", "x")
	check("a write to a read-only variable", err, ErrISDUInvalid)
	_, err = portsOf(t, pfMaster(t, nil)).isduRead(ctx, c, "LEVEL1", "V_Setpoint")
	check("a master with no isdu:", err, ErrISDURefused)
}

// A port device's parameters are listed from its IODD with no traffic: the
// ids, indexes, access, types, ranges and defaults, a record by its items,
// and whether the master can carry each in one call.
func TestISDUParamsListing(t *testing.T) {
	fsys := portFS(t, "ifm-al1590")
	s, err := Resolve(fsys, []Device{{Name: "M", Profile: "ifm-al1590", Endpoint: "h", Params: map[string]string{"location": "X"},
		Ports: map[int]Port{1: {Name: "LEVEL1", IODD: levelFile}}}})
	if err != nil {
		t.Fatal(err)
	}
	w := &OPCUADriver{ports: newPorts(s.devices[0].ports)}
	list, master, ok := w.ISDUParams("level1")
	if !ok || master != "M" {
		t.Fatalf("listing: %v %q", ok, master)
	}
	by := map[string]ISDUParamInfo{}
	for _, p := range list {
		by[p.Ref] = p
	}
	sp := by["64"]
	if sp.ID != "V_Setpoint" || sp.Access != "rw" || sp.Octets != 2 || len(sp.Range) != 1 || sp.Range[0] != "0..1000" || sp.Default != "100" || !sp.Carried {
		t.Errorf("setpoint %+v", sp)
	}
	if vn := by["16"]; vn.Octets != 64 || vn.Carried {
		t.Errorf("vendor name %+v: 64 octets do not fit the ifm's 32", vn)
	}
	if _, whole := by["65"]; whole {
		t.Error("a record is listed whole")
	}
	if it := by["65.2"]; it.ID != "V_Window.Upper" && !strings.HasPrefix(it.ID, "V_Window.") {
		t.Errorf("a record item %+v", it)
	}
	if _, _, ok := w.ISDUParams("NOPE"); ok {
		t.Error("a port device the master does not have was listed")
	}
	if _, _, ok := (&OPCUADriver{}).ISDUParams("LEVEL1"); ok {
		t.Error("a device with no ports listed parameters")
	}
}
