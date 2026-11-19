//go:build !js

package devbind

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/iolink"
	"github.com/joyautomation/nautilus/opcua"
)

// ISDU (#705 PR 5a, docs/design/iolink.md §7): a parameter of the device on
// a port is read and written by two OPC UA methods on the port device's
// object, ReadISDU(Index, Subindex) and WriteISDU(Index, Subindex, Data),
// through the master's own session. The IODD says what the parameter is (its
// index, type, range and access rights), the profile's isdu: block where the
// methods are and how a value travels.

// Caller is what ISDU needs of a driver: the method call (*opcua.Driver).
type Caller interface {
	Call(ctx context.Context, object, method opcua.NodeRef, args ...*ua.Variant) ([]*ua.Variant, error)
}

// ISDUResult is a parameter as read, and what is known of the port at the
// time.
type ISDUResult struct {
	// Variable is the IODD variable's id; Index and Subindex are the wire's.
	Variable string
	Index    int
	Subindex int
	// Value is decoded by the IODD's type; Raw is what the device sent.
	Value any
	Raw   []byte
	// Identity is the port's identity check when the parameter was read,
	// Checked false when the master's ids have not arrived. A read may run
	// in any state, which is why the answer says which device is there.
	Identity iolink.State
	Checked  bool
}

func (ps *ports) find(name string) (*portIn, error) {
	for _, p := range ps.in {
		if strings.EqualFold(p.Name, name) {
			return p, nil
		}
	}
	var names []string
	for _, p := range ps.in {
		names = append(names, p.Name)
	}
	return nil, fmt.Errorf("no port device %q on this master (have %s)", name, strings.Join(names, ", "))
}

// identity is the port's identity check: the state (the check's, not
// AwaitingRearm, which is about outputs) and whether any check has run.
func (ps *ports) identity(p *portIn) (iolink.State, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return p.state, p.checked
}

// ISDURead reads a parameter of the device on a port. ref is a variable id,
// name or index, with the record item after a dot where it is a record
// (iolink.FindParam). It works in any identity state and says which.
func (w *OPCUADriver) ISDURead(ctx context.Context, port, ref string) (ISDUResult, error) {
	if w.ports == nil {
		return ISDUResult{}, errors.New("this device has no port devices")
	}
	return w.ports.isduRead(ctx, w.Driver, port, ref)
}

// ISDUWrite writes a parameter of the device on a port, and returns what
// was written as the device then holds it. It is refused while the port's
// identity is not Ok (or Unchecked): a parameter must never reach a device
// that is not the one the IODD describes. The refusal names the state.
func (w *OPCUADriver) ISDUWrite(ctx context.Context, port, ref string, v any) (ISDUResult, error) {
	if w.ports == nil {
		return ISDUResult{}, errors.New("this device has no port devices")
	}
	return w.ports.isduWrite(ctx, w.Driver, port, ref, v)
}

func (ps *ports) isduParam(port, ref string) (*portIn, *iolink.Param, error) {
	p, err := ps.find(port)
	if err != nil {
		return nil, nil, err
	}
	if p.isdu == nil {
		return nil, nil, fmt.Errorf("port device %s: master %s offers no ISDU (its profile has no isdu:)", p.Name, p.Master)
	}
	prm, err := iolink.FindParam(p.dev.IODD, ref)
	if err != nil {
		return nil, nil, fmt.Errorf("port device %s: %w", p.Name, err)
	}
	if lim := p.isdu.spec.Octets(); prm.Octets() > lim {
		return nil, nil, fmt.Errorf("port device %s: %s is %d octets and master %s carries at most %d in one ISDU call", p.Name, prm.Variable.ID, prm.Octets(), p.Master, lim)
	}
	return p, prm, nil
}

func (ps *ports) isduRead(ctx context.Context, c Caller, port, ref string) (ISDUResult, error) {
	p, prm, err := ps.isduParam(port, ref)
	if err != nil {
		return ISDUResult{}, err
	}
	if !prm.CanRead() {
		return ISDUResult{}, fmt.Errorf("port device %s: %s is write-only", p.Name, prm.Variable.ID)
	}
	st, checked := ps.identity(p)
	res := ISDUResult{Variable: prm.Variable.ID, Index: prm.Index(), Subindex: prm.Subindex, Identity: st, Checked: checked}
	raw, err := callISDU(ctx, c, p.isdu, p.isdu.read, prm, nil)
	if err != nil {
		return res, fmt.Errorf("port device %s: read %s: %w", p.Name, prm.Variable.ID, err)
	}
	res.Raw = raw
	if res.Value, err = prm.Decode(raw); err != nil {
		return res, fmt.Errorf("port device %s: %w", p.Name, err)
	}
	return res, nil
}

func (ps *ports) isduWrite(ctx context.Context, c Caller, port, ref string, v any) (ISDUResult, error) {
	p, prm, err := ps.isduParam(port, ref)
	if err != nil {
		return ISDUResult{}, err
	}
	st, checked := ps.identity(p)
	res := ISDUResult{Variable: prm.Variable.ID, Index: prm.Index(), Subindex: prm.Subindex, Identity: st, Checked: checked}
	switch {
	case !checked:
		return res, fmt.Errorf("port device %s: refused: the identity of the device on %s port %d has not been checked yet (the master has not delivered its ids)", p.Name, p.Master, p.Num)
	case !st.Drives():
		return res, fmt.Errorf("port device %s: refused: the device on %s port %d is not the one expected (%s): a parameter would reach the wrong device", p.Name, p.Master, p.Num, st)
	case p.isdu.spec.Write == "":
		return res, fmt.Errorf("port device %s: master %s cannot write parameters (its isdu: has no write method)", p.Name, p.Master)
	}
	raw, err := prm.Encode(v)
	if err != nil {
		return res, fmt.Errorf("port device %s: %w", p.Name, err)
	}
	if _, err := callISDU(ctx, c, p.isdu, p.isdu.write, prm, raw); err != nil {
		return res, fmt.Errorf("port device %s: write %s: %w", p.Name, prm.Variable.ID, err)
	}
	res.Raw = raw
	if res.Value, err = prm.Decode(raw); err != nil && !prm.CanRead() {
		res.Value = v // write-only: nothing to read back
	}
	return res, nil
}

// callISDU makes one ReadISDU (data nil) or WriteISDU call and returns the
// octets a read answered. An answer with a bad Status is an error that
// carries the IO-Link ErrorType, so a device's own refusal is not mistaken
// for the master's.
func callISDU(ctx context.Context, c Caller, r *isduRefs, method nodeRef, prm *iolink.Param, data []byte) ([]byte, error) {
	idx, _ := ua.NewVariant(uint16(prm.Index()))
	sub, _ := ua.NewVariant(uint8(prm.Subindex))
	args := []*ua.Variant{idx, sub}
	if data != nil {
		var d *ua.Variant
		var err error
		if r.spec.Data == devprofile.ISDUDataHex {
			d, err = ua.NewVariant("0x" + hex.EncodeToString(data))
		} else {
			d, err = ua.NewVariant(ua.ByteArray(data))
		}
		if err != nil {
			return nil, err
		}
		args = append(args, d)
	}
	out, err := c.Call(ctx, opcua.NodeRef{NodeID: r.object.ID, Namespace: r.object.URI}, opcua.NodeRef{NodeID: method.ID, Namespace: method.URI}, args...)
	if err != nil {
		return nil, err
	}
	// ReadISDU answers (value, ErrorType, Status); WriteISDU (ErrorType, Status).
	want := 2
	if data == nil {
		want = 3
	}
	if len(out) != want {
		return nil, fmt.Errorf("the master answered %d output arguments, want %d", len(out), want)
	}
	errType, status := out[want-2], out[want-1]
	if code, ok := intOf(status.Value()); !ok || code != 0 {
		et, _ := intOf(errType.Value())
		return nil, fmt.Errorf("the master's status %v (IO-Link ErrorType 0x%04X)%s", status.Value(), et, isduStatusNote(r.spec.Mode, code))
	}
	if data != nil {
		return nil, nil
	}
	return octetsOf(out[0])
}

// isduStatusNote names OPC 30120's status codes (the master's, not the
// device's: a device's refusal is -3 with its ErrorType).
func isduStatusNote(mode string, code int64) string {
	if mode != devprofile.ISDUOPC30120 {
		return ""
	}
	switch code {
	case -1:
		return ": another ISDU request is running on the port"
	case -2:
		return ": the device on the port is not active"
	case -3:
		return ": the device refused it"
	}
	return ""
}

func intOf(v any) (int64, bool) {
	switch x := v.(type) {
	case int32:
		return int64(x), true
	case uint32:
		return int64(x), true
	case int64:
		return x, true
	case uint16:
		return int64(x), true
	}
	return 0, false
}

// octetsOf is a read's value as octets: a Byte[] or ByteString, or hex
// text ("0x" first or not, strict hex).
func octetsOf(v *ua.Variant) ([]byte, error) {
	switch x := v.Value().(type) {
	case []byte:
		return x, nil
	case ua.ByteArray:
		return x, nil
	case string:
		b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(x, "0x"), "0X"))
		if err != nil {
			return nil, fmt.Errorf("the master answered %q, which is not hex text", x)
		}
		return b, nil
	}
	return nil, fmt.Errorf("the master answered a %T, not octets", v.Value())
}
