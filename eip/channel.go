package eip

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/joyautomation/nautilus/eip/cip"
	"github.com/joyautomation/nautilus/eip/logix"
	"github.com/joyautomation/nautilus/lang/ir"
)

// Channel is a CIP communication channel to one Logix controller: the
// transport behind the READ and WRITE blocks (runtime.ChannelIO) when the
// manifest declares `type: cip`. It sends the services a Logix MSG of type
// CIP Data Table Read/Write sends: Read Tag and Write Tag on a connected
// session, opened on the first request and reopened after a failure.
//
// A variable is a Logix tag path: Tag, Tag.Member, Tag[3],
// Program:Main.Tag. Its layout always comes from the peer. A structure is
// read and written whole, as MSG moves it, by the template the controller
// reports for it; nautilus never computes a Logix layout, and a value whose
// members do not match the local variable's by name and type is refused
// with ir.CommTypeClash instead of moved in part.
type Channel struct {
	host string
	opts []logix.Option
	log  *slog.Logger

	mu        sync.Mutex
	ctrl      *logix.Controller
	symbols   map[string]logix.Symbol    // upper-cased name → symbol, per session
	templates map[uint16]*logix.Template // by template id, per session
	reg       *logix.Registry
	peer      map[string]peerType // per variable: what a write must send
}

// peerType is what the peer reported for a variable on its first read.
type peerType struct {
	code   uint16 // elementary CIP code, or cip.TypeStruct
	handle uint16
	tmpl   *logix.Template
}

// NewChannel returns a CIP channel to the controller at host; slot routes to
// a processor in that backplane slot (0 for the device at host itself).
// Nothing connects until the first request.
func NewChannel(host string, slot, port int, log *slog.Logger) *Channel {
	if log == nil {
		log = slog.Default()
	}
	opts := []logix.Option{logix.WithSlot(slot), logix.WithLogger(log)}
	if port > 0 {
		opts = append(opts, logix.WithPort(port))
	}
	return &Channel{host: host, opts: opts, log: log}
}

// commErr is a failure with the STATUS a READ or WRITE reports.
type commErr struct {
	status int
	err    error
}

func (e *commErr) Error() string   { return e.err.Error() }
func (e *commErr) Unwrap() error   { return e.err }
func (e *commErr) CommStatus() int { return e.status }

func clash(format string, a ...any) error {
	return &commErr{status: ir.CommTypeClash, err: fmt.Errorf(format, a...)}
}

func (c *Channel) conn(ctx context.Context) (*logix.Controller, error) {
	if c.ctrl != nil && !c.ctrl.Broken() {
		return c.ctrl, nil
	}
	if c.ctrl != nil {
		_ = c.ctrl.Close()
		c.ctrl = nil
	}
	ctrl, err := logix.Dial(ctx, c.host, c.opts...)
	if err != nil {
		return nil, &commErr{status: ir.CommUnreachable, err: err}
	}
	// A new session may be a re-downloaded controller: learn it again.
	c.ctrl, c.symbols, c.templates, c.peer = ctrl, nil, map[uint16]*logix.Template{}, map[string]peerType{}
	c.reg = logix.NewRegistry(c.templates)
	return ctrl, nil
}

// Close ends the session.
func (c *Channel) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctrl == nil {
		return nil
	}
	err := c.ctrl.Close()
	c.ctrl = nil
	return err
}

// Read reads variable from the peer and returns it in shape's type.
func (c *Channel) Read(ctx context.Context, variable string, shape ir.Value) (ir.Value, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctrl, err := c.conn(ctx)
	if err != nil {
		return ir.Value{}, err
	}
	raw, err := ctrl.ReadTag(ctx, variable, 1)
	if err != nil {
		return ir.Value{}, err
	}
	pv, err := c.decode(ctx, variable, raw)
	if err != nil {
		return ir.Value{}, err
	}
	return fit(pv, shape, nil)
}

// Write writes data to variable on the peer, whole.
func (c *Channel) Write(ctx context.Context, variable string, data ir.Value) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctrl, err := c.conn(ctx)
	if err != nil {
		return err
	}
	pt, ok := c.peer[strings.ToUpper(variable)]
	if !ok {
		// The first write learns the variable's type and, for a structure,
		// its handle and template, from one read.
		raw, err := ctrl.ReadTag(ctx, variable, 1)
		if err != nil {
			return err
		}
		pt = peerType{code: raw.Type, handle: raw.Handle}
		if raw.Type == cip.TypeStruct {
			if pt.tmpl, err = c.templateFor(ctx, variable, raw.Handle); err != nil {
				return err
			}
		}
		c.peer[strings.ToUpper(variable)] = pt
	}
	if pt.code == cip.TypeStruct {
		if pt.tmpl.IsString() {
			return clash("%s: a Logix STRING is not written by WRITE yet", variable)
		}
		buf := make([]byte, pt.tmpl.StructSize)
		if err := c.encodeStruct(buf, pt.tmpl, data, nil); err != nil {
			return err
		}
		return ctrl.WriteTagStruct(ctx, variable, pt.handle, 1, buf)
	}
	ti, ok := logix.TypeByCode(pt.code)
	if !ok {
		return clash("%s: peer type 0x%04x", variable, pt.code)
	}
	b, err := encodeScalar(ti, data, nil)
	if err != nil {
		return err
	}
	return ctrl.WriteTag(ctx, variable, pt.code, 1, b)
}

func (c *Channel) decode(ctx context.Context, variable string, raw logix.RawTag) (logix.Value, error) {
	if raw.Type != cip.TypeStruct {
		return c.reg.Decode(raw)
	}
	t, err := c.templateFor(ctx, variable, raw.Handle)
	if err != nil {
		return logix.Value{}, err
	}
	return c.reg.DecodeStructData(t, raw.Data)
}

// templateFor finds the template of a structure-typed variable: its root
// symbol's template, then each member's along the path, and loads every
// template nested in it. handle is what the read reported, and must agree.
func (c *Channel) templateFor(ctx context.Context, variable string, handle uint16) (*logix.Template, error) {
	if c.symbols == nil {
		syms, _, err := c.ctrl.ListSymbols(ctx)
		if err != nil {
			return nil, err
		}
		c.symbols = make(map[string]logix.Symbol, len(syms))
		for _, s := range syms {
			c.symbols[strings.ToUpper(s.Name)] = s
		}
	}
	root, members := splitVariable(variable)
	sym, ok := c.symbols[strings.ToUpper(root)]
	if !ok || !sym.IsStruct() {
		return nil, clash("%s: %s is not a structure on the peer", variable, root)
	}
	id := sym.TemplateID()
	for _, name := range members {
		t, err := c.template(ctx, id)
		if err != nil {
			return nil, err
		}
		var found bool
		for _, m := range t.Members {
			if strings.EqualFold(m.Name, name) && m.IsStruct() {
				id, found = m.NestedID(), true
				break
			}
		}
		if !found {
			return nil, clash("%s: no structure member %s in %s", variable, name, t.Name)
		}
	}
	t, err := c.template(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Handle != handle {
		return nil, clash("%s: the peer's handle 0x%04x is not %s's", variable, handle, t.Name)
	}
	return t, nil
}

// template reads one template and every template nested in it, once per
// session.
func (c *Channel) template(ctx context.Context, id uint16) (*logix.Template, error) {
	if t, ok := c.templates[id]; ok {
		return t, nil
	}
	t, err := c.ctrl.ReadTemplate(ctx, id)
	if err != nil {
		return nil, err
	}
	c.templates[id] = t
	for _, m := range t.Members {
		if m.IsStruct() {
			if _, err := c.template(ctx, m.NestedID()); err != nil {
				return nil, err
			}
		}
	}
	c.reg = logix.NewRegistry(c.templates)
	return t, nil
}

// splitVariable splits a tag path into its root symbol and the member
// names after it; array subscripts drop out, since an element has its
// array's type. "Program:Main.Line[2].Pump" → "Program:Main.Line", [Pump].
func splitVariable(v string) (string, []string) {
	var prefix string
	if strings.HasPrefix(strings.ToUpper(v), "PROGRAM:") {
		if i := strings.IndexByte(v, '.'); i > 0 {
			prefix, v = v[:i+1], v[i+1:]
		}
	}
	parts := strings.Split(v, ".")
	for i, p := range parts {
		if j := strings.IndexByte(p, '['); j >= 0 {
			parts[i] = p[:j]
		}
	}
	return prefix + parts[0], parts[1:]
}

// fit converts a value the peer sent into shape's type. Members match by
// name, case-insensitively, and every one of the local variable's must be
// there with nothing left over; elementary types must agree (an INT is not
// a DINT), REAL and LREAL aside.
func fit(pv logix.Value, shape ir.Value, typ *ir.Type) (ir.Value, error) {
	switch shape.Kind {
	case ir.TypeStruct:
		if shape.Struct == nil || pv.Fields == nil {
			return ir.Value{}, clash("peer %s is not a structure like %s", pv.Type, structName(shape))
		}
		if len(pv.Fields) != len(shape.Struct.Fields) {
			return ir.Value{}, clash("peer %s has %d members, %s has %d", pv.Type, len(pv.Fields), structName(shape), len(shape.Struct.Fields))
		}
		out := ir.CopyValue(shape)
		for i, f := range shape.Struct.Fields {
			var hit *logix.Value
			for j := range pv.Fields {
				if strings.EqualFold(pv.Fields[j].Name, f.Name) {
					hit = &pv.Fields[j].Value
					break
				}
			}
			if hit == nil {
				return ir.Value{}, clash("peer %s has no member %s", pv.Type, f.Name)
			}
			v, err := fit(*hit, shape.Fld[i], f.Type)
			if err != nil {
				return ir.Value{}, fmt.Errorf("%s: %w", f.Name, err)
			}
			out.Fld[i] = v
		}
		return out, nil
	case ir.TypeArray:
		if pv.Elems == nil || len(pv.Elems) != len(shape.Arr) {
			return ir.Value{}, clash("peer %s has %d elements, want %d", pv.Type, len(pv.Elems), len(shape.Arr))
		}
		var et *ir.Type
		if typ != nil {
			et = typ.Elem
		}
		out := ir.CopyValue(shape)
		for i := range shape.Arr {
			v, err := fit(pv.Elems[i], shape.Arr[i], et)
			if err != nil {
				return ir.Value{}, fmt.Errorf("[%d]: %w", i, err)
			}
			out.Arr[i] = v
		}
		return out, nil
	case ir.TypeBool:
		if b, ok := pv.Scalar.(bool); ok {
			return ir.BoolVal(b), nil
		}
	case ir.TypeInt:
		if n, ok := pv.Scalar.(int64); ok && (typ == nil || typ.Enum == nil) && sameName(typ, pv.Type) {
			return ir.IntVal(n), nil
		}
	case ir.TypeReal:
		if f, ok := pv.Scalar.(float64); ok {
			return ir.RealVal(f), nil
		}
	case ir.TypeString:
		if s, ok := pv.Scalar.(string); ok {
			return ir.StringVal(s), nil
		}
	}
	return ir.Value{}, clash("peer %s does not fit a local %s", pv.Type, kindName(shape, typ))
}

// encodeStruct lays v out in buf by template t's member offsets.
func (c *Channel) encodeStruct(buf []byte, t *logix.Template, v ir.Value, typ *ir.Type) error {
	if v.Kind != ir.TypeStruct || v.Struct == nil {
		return clash("a %s is written from a structure, not a %s", t.Name, kindName(v, typ))
	}
	vis := t.VisibleMembers()
	if len(vis) != len(v.Struct.Fields) {
		return clash("peer %s has %d members, %s has %d", t.Name, len(vis), structName(v), len(v.Struct.Fields))
	}
	for _, m := range vis {
		idx, ok := -1, false
		for i, f := range v.Struct.Fields {
			if strings.EqualFold(f.Name, m.Name) {
				idx, ok = i, true
				break
			}
		}
		if !ok {
			return clash("%s has no member %s", structName(v), m.Name)
		}
		if err := c.encodeMember(buf, m, v.Fld[idx], v.Struct.Fields[idx].Type); err != nil {
			return fmt.Errorf("%s: %w", m.Name, err)
		}
	}
	return nil
}

func (c *Channel) encodeMember(buf []byte, m logix.Member, v ir.Value, typ *ir.Type) error {
	count := 1
	if m.IsArray() {
		count = int(m.Info)
		if v.Kind != ir.TypeArray || len(v.Arr) != count {
			return clash("peer has %d elements, local %s", count, kindName(v, typ))
		}
	}
	elem := func(i int) (ir.Value, *ir.Type) {
		if !m.IsArray() {
			return v, typ
		}
		var et *ir.Type
		if typ != nil {
			et = typ.Elem
		}
		return v.Arr[i], et
	}
	if m.IsStruct() {
		nested, ok := c.templates[m.NestedID()]
		if !ok || nested.IsString() {
			return clash("member type 0x%03x is not written by WRITE", m.NestedID())
		}
		stride := int(nested.StructSize)
		for i := 0; i < count; i++ {
			off := int(m.Offset) + i*stride
			if off+stride > len(buf) {
				return clash("member out of range")
			}
			ev, et := elem(i)
			if err := c.encodeStruct(buf[off:off+stride], nested, ev, et); err != nil {
				return err
			}
		}
		return nil
	}
	ti, ok := logix.TypeByCode(m.ElementaryCode())
	if !ok {
		return clash("member type 0x%04x", m.Type)
	}
	if ti.Code == cip.TypeBOOL {
		for i := 0; i < count; i++ {
			ev, _ := elem(i)
			if ev.Kind != ir.TypeBool {
				return clash("peer BOOL, local %s", kindName(ev, nil))
			}
			off, bit := int(m.Offset), uint(m.Info%8)
			if m.IsArray() {
				off, bit = int(m.Offset)+i/8, uint(i%8)
			}
			if off >= len(buf) {
				return clash("member out of range")
			}
			if ev.B {
				buf[off] |= 1 << bit
			} else {
				buf[off] &^= 1 << bit
			}
		}
		return nil
	}
	for i := 0; i < count; i++ {
		ev, et := elem(i)
		b, err := encodeScalar(ti, ev, et)
		if err != nil {
			return err
		}
		off := int(m.Offset) + i*ti.Size
		if off+len(b) > len(buf) {
			return clash("member out of range")
		}
		copy(buf[off:], b)
	}
	return nil
}

// encodeScalar renders an elementary value as the peer's type ti; the local
// type must agree with it, as fit requires on the way in.
func encodeScalar(ti logix.TypeInfo, v ir.Value, typ *ir.Type) ([]byte, error) {
	var x any
	switch {
	case v.Kind == ir.TypeBool && ti.Code == cip.TypeBOOL:
		x = v.B
	case v.Kind == ir.TypeReal && (ti.Name == "REAL" || ti.Name == "LREAL"):
		x = v.F
	case v.Kind == ir.TypeInt && ti.Code != cip.TypeBOOL && ti.Name != "REAL" && ti.Name != "LREAL" &&
		(typ == nil || typ.Enum == nil) && sameName(typ, ti.Name):
		x = v.I
	default:
		return nil, clash("peer %s, local %s", ti.Name, kindName(v, typ))
	}
	return logix.EncodeScalar(ti.Code, x)
}

// sameName reports whether a local integer type matches the peer's type
// name. A local integer of undeclared width matches any.
func sameName(typ *ir.Type, peer string) bool {
	return typ == nil || typ.Name == "" || strings.EqualFold(typ.Name, peer)
}

func structName(v ir.Value) string {
	if v.Struct != nil && v.Struct.Name != "" {
		return v.Struct.Name
	}
	return "the structure"
}

func kindName(v ir.Value, typ *ir.Type) string {
	if typ != nil {
		return typ.String()
	}
	if v.Kind == ir.TypeStruct {
		return structName(v)
	}
	return v.Kind.String()
}
