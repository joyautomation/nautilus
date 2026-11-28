//go:build !js

package wirebind

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/wiring"
)

// The keys a Modbus source and a Modbus member take, as the devices: rows
// and a modbus manifest spell them.
var (
	modbusSourceKeys = []string{"endpoint", "unit-id", "word-order", "byte-order", "timeout", "max-block"}
	modbusMemberKeys = []string{"table", "address", "format", "scale", "offset", "scan-class", "rewrite"}
)

// ModbusManifest is what the Modbus instances add to the Modbus driver
// named driver (a drivers: entry's name, or "" for the project's one Modbus
// driver): a source per connection, named by its ID (so the driver's
// companions are <Instance>__Online and <Instance>__Outages), a read
// binding per from: member and a write-only binding per to: member, each
// named by the member's key ("P101.Speed", "P101.Cmd.Start"). The to:
// member's read-back is its own from: member, so the command binding is
// never polled. owns says which Modbus driver a source's driver: names.
func (s *Set) ModbusManifest(owns func(driver string) bool) (modbus.Manifest, []wiring.Problem) {
	var m modbus.Manifest
	var out []wiring.Problem
	add := func(pos wiring.Pos, format string, a ...any) {
		out = append(out, wiring.Problem{Pos: pos, Msg: fmt.Sprintf(format, a...)})
	}
	if s == nil {
		return m, nil
	}
	devices := map[string]string{} // Endpoint -> the instance on it
	for _, in := range s.Instances {
		for _, c := range in.Conns {
			if c.DriverType != "modbus" || !owns(c.Driver) {
				continue
			}
			src, err := modbusSource(c)
			if err != nil {
				add(c.Pos, "instance %s: source %s: %v", in.Name, c.Source, err)
				continue
			}
			if prev, dup := devices[Endpoint(src)]; dup {
				add(in.Pos, "instance %s and instance %s are both on %s: one device, two connections. Wire both as one instance of a type that holds them both, or give them different unit-ids if a gateway fronts two devices", prev, in.Name, Endpoint(src))
				continue
			}
			devices[Endpoint(src)] = in.Name
			m.Sources = append(m.Sources, src)
			var written []writeSpan
			for _, mem := range in.Members {
				if mem.Source != c.Source || (mem.Kind != wiring.From && mem.Kind != wiring.To) {
					continue
				}
				b, err := modbusBinding(in.Key(mem), src.ID, mem)
				if err != nil {
					add(mem.Pos, "instance %s: member %s: %v", in.Name, mem.Path, err)
					continue
				}
				if b.Writable {
					w := spanOf(b, mem.Path)
					for _, o := range written {
						if o.overlaps(w) {
							add(mem.Pos, "instance %s: commands %s and %s both write %s %d: two commands on one register fight, and the device takes whichever came last", in.Name, o.member, w.member, b.Table, b.Address)
						}
					}
					written = append(written, w)
				}
				m.Tags = append(m.Tags, b)
			}
		}
	}
	return m, out
}

// Endpoint is a Modbus source's device, normalised: host (lower case),
// port (502 when none) and unit id. Two sources on one Endpoint are two
// connections to one device.
func Endpoint(src modbus.Source) string {
	port := src.Port
	if port == 0 {
		port = 502
	}
	return fmt.Sprintf("%s:%d unit %d", strings.ToLower(src.Host), port, src.UnitID)
}

// writeSpan is the registers (or coil) one command writes, and the bits
// of a single register a bit:N command writes.
type writeSpan struct {
	member      string
	table       string
	first, last int
	bit         int // -1: the whole span
}

func spanOf(b modbus.TagBinding, member string) writeSpan {
	w := writeSpan{member: member, table: b.Table, first: int(b.Address), last: int(b.Address), bit: -1}
	if f, err := modbus.ParseFormat(b.Format); err == nil {
		w.last = w.first + f.Words() - 1
		if f.Kind == "bit" {
			w.bit = f.Bit
		}
	}
	return w
}

func (a writeSpan) overlaps(b writeSpan) bool {
	if a.table != b.table || a.last < b.first || b.last < a.first {
		return false
	}
	return a.bit < 0 || b.bit < 0 || a.bit == b.bit
}

// MergeModbus adds the instances' sources and bindings to a Modbus
// manifest. A source id or binding name both declare is an error naming it.
func MergeModbus(own, add modbus.Manifest) (modbus.Manifest, error) {
	ids := map[string]bool{}
	for _, src := range own.Sources {
		ids[strings.ToLower(src.ID)] = true
	}
	names := map[string]bool{}
	for _, t := range own.Tags {
		names[ir.NameKey(t.Name)] = true
	}
	devices := map[string]string{}
	for _, src := range own.Sources {
		devices[Endpoint(src)] = src.ID
	}
	for _, src := range add.Sources {
		if ids[strings.ToLower(src.ID)] {
			return own, fmt.Errorf("instance %s: the Modbus driver already has a source %s (a devices: row, or its manifest): rename one", src.ID, src.ID)
		}
		if prev, dup := devices[Endpoint(src)]; dup {
			return own, fmt.Errorf("instance %s is on %s, which the Modbus driver already polls as %s (a devices: row, or its manifest): one device, two connections; wire it in one place", src.ID, Endpoint(src), prev)
		}
	}
	for _, t := range add.Tags {
		if names[ir.NameKey(t.Name)] {
			return own, fmt.Errorf("instance %s: the Modbus driver already binds %s: rename one", t.Source, t.Name)
		}
	}
	return modbus.Manifest{
		Sources: append(append([]modbus.Source(nil), own.Sources...), add.Sources...),
		Tags:    append(append([]modbus.TagBinding(nil), own.Tags...), add.Tags...),
	}, nil
}

func modbusSource(c Conn) (modbus.Source, error) {
	src := modbus.Source{ID: c.ID}
	for _, k := range sortedKeys(c.Keys) {
		if !contains(modbusSourceKeys, k) {
			return src, fmt.Errorf("%q is not a Modbus source key (it takes %s)", k, strings.Join(modbusSourceKeys, ", "))
		}
	}
	ep, ok := c.Keys["endpoint"]
	if !ok || ep == nil || fmt.Sprint(ep) == "" {
		return src, fmt.Errorf("a Modbus source needs endpoint: (host or host:port), usually a param: endpoint: \"{endpoint}\"")
	}
	host, port, err := modbus.SplitHost(fmt.Sprint(ep))
	if err != nil || host == "" {
		return src, fmt.Errorf("endpoint %q is not host or host:port", fmt.Sprint(ep))
	}
	src.Host, src.Port = host, port
	if v, ok := c.Keys["unit-id"]; ok {
		n, err := intOf(v)
		if err != nil || n < 0 || n > 255 {
			return src, fmt.Errorf("unit-id %v is not 0..255", v)
		}
		src.UnitID = uint8(n)
	}
	for _, k := range []string{"word-order", "byte-order"} {
		v, ok := c.Keys[k]
		if !ok {
			continue
		}
		o := fmt.Sprint(v)
		if o != modbus.OrderBig && o != modbus.OrderLittle {
			return src, fmt.Errorf("%s %q is big or little", k, o)
		}
		if k == "word-order" {
			src.WordOrder = o
		} else {
			src.ByteOrder = o
		}
	}
	if v, ok := c.Keys["timeout"]; ok {
		d, err := time.ParseDuration(fmt.Sprint(v))
		if err != nil || d <= 0 {
			return src, fmt.Errorf("timeout %v is not a duration (3s, 500ms)", v)
		}
		src.Timeout = d
	}
	if v, ok := c.Keys["max-block"]; ok {
		n, err := intOf(v)
		if err != nil || n <= 0 {
			return src, fmt.Errorf("max-block %v is not a positive count", v)
		}
		src.MaxBlock = n
	}
	return src, nil
}

func modbusBinding(key, source string, mem Member) (modbus.TagBinding, error) {
	b := modbus.TagBinding{Name: key, Source: source}
	if mem.Type == nil || mem.Type.Kind == ir.TypeStruct || mem.Type.Kind == ir.TypeArray {
		return b, fmt.Errorf("is a %s: a Modbus binding is one value, so wire its members one by one", mem.Type)
	}
	for _, k := range sortedKeys(mem.Addr) {
		if !contains(modbusMemberKeys, k) {
			return b, fmt.Errorf("%q is not a Modbus address key (a member takes %s)", k, strings.Join(modbusMemberKeys, ", "))
		}
	}
	if v, ok := mem.Addr["table"]; ok {
		b.Table = fmt.Sprint(v)
	}
	switch b.Table {
	case modbus.TableHolding, modbus.TableInput, modbus.TableCoil, modbus.TableDiscrete:
	case "":
		return b, fmt.Errorf("needs table: (holding, input, coil or discrete)")
	default:
		return b, fmt.Errorf("table %q is holding, input, coil or discrete", b.Table)
	}
	v, ok := mem.Addr["address"]
	if !ok {
		return b, fmt.Errorf("needs address: (the 0-based register or coil number)")
	}
	n, err := intOf(v)
	if err != nil || n < 0 || n > math.MaxUint16 {
		return b, fmt.Errorf("address %v is not 0..65535", v)
	}
	b.Address = uint16(n)
	if v, ok := mem.Addr["format"]; ok {
		b.Format = fmt.Sprint(v)
	}
	for _, k := range []string{"scale", "offset"} {
		v, ok := mem.Addr[k]
		if !ok {
			continue
		}
		f, err := floatOf(v)
		if err != nil {
			return b, fmt.Errorf("%s %v is not a number", k, v)
		}
		if k == "scale" {
			b.Scale = f
		} else {
			b.Offset = f
		}
	}
	if v, ok := mem.Addr["scan-class"]; ok {
		b.ScanClass = fmt.Sprint(v)
	}
	if v, ok := mem.Addr["rewrite"]; ok {
		if mem.Kind != wiring.To {
			return b, fmt.Errorf("rewrite: re-asserts a command, and this member is %s", mem.Kind)
		}
		if mem.Event {
			return b, fmt.Errorf("is event: (never sent twice), and rewrite: re-sends it every period: give it one or the other")
		}
		d, err := time.ParseDuration(fmt.Sprint(v))
		if err != nil || d <= 0 {
			return b, fmt.Errorf("rewrite %v is not a duration (5s)", v)
		}
		b.Rewrite = d
	}
	if mem.Kind == wiring.To {
		if b.Table != modbus.TableHolding && b.Table != modbus.TableCoil {
			return b, fmt.Errorf("is a command (to:), and table %s is read-only: commands go to holding or coil", b.Table)
		}
		b.Writable, b.WriteOnly = true, true
	}
	if err := fits(b, mem); err != nil {
		return b, err
	}
	return b, nil
}

// fits refuses a member whose type cannot hold what its binding delivers
// (a from:) or a command its register cannot take (a to:): at load, rather
// than a delivery the runtime counts as dropped on every poll.
func fits(b modbus.TagBinding, mem Member) error {
	iec := b.IECType()
	if iec == "" {
		_, err := modbus.ParseFormat(b.Format)
		if err == nil {
			err = fmt.Errorf("format %q does not fit table %s", b.Format, b.Table)
		}
		return err
	}
	t := mem.Type
	isBool := t.Kind == ir.TypeBool
	switch {
	case iec == "BOOL" && !isBool:
		return fmt.Errorf("is %s, but %s %d (format %s) delivers BOOL: make the member BOOL, or give the register a numeric format", t, b.Table, b.Address, formatOf(b))
	case iec != "BOOL" && isBool:
		return fmt.Errorf("is BOOL, but %s %d (format %s) delivers %s: give the member that type, or read one bit with format bit:N", b.Table, b.Address, formatOf(b), iec)
	case isBool:
		return nil
	case t.Kind != ir.TypeInt && t.Kind != ir.TypeReal:
		return fmt.Errorf("is %s, which a Modbus register does not hold: make it a number or a BOOL", t)
	}
	if mem.Kind == wiring.To || t.Kind == ir.TypeReal {
		return nil // a command is encoded into its register (range checked as it is sent); a REAL holds any reading
	}
	if iec == "REAL" || iec == "LREAL" {
		return fmt.Errorf("is %s, but %s %d delivers %s (format %s, scale %g, offset %g): make the member REAL, or read the register unscaled", t, b.Table, b.Address, iec, formatOf(b), scaleOf(b), b.Offset)
	}
	if t.Enum != nil {
		return nil // a value that is no member is refused as it arrives, and counted
	}
	if !holds(t, iec) {
		return fmt.Errorf("is %s, but %s %d delivers %s: a reading would not fit; make the member %s, or a wider integer of that sign", t, b.Table, b.Address, iec, iec)
	}
	return nil
}

// holds says whether an integer member of type t holds every value of the
// binding's integer type iec (INT, UINT, DINT, UDINT).
func holds(t *ir.Type, iec string) bool {
	w := t.BitWidth()
	unsignedMember := false
	switch t.Name {
	case "USINT", "UINT", "UDINT", "ULINT", "BYTE", "WORD", "DWORD", "LWORD":
		unsignedMember = true
	}
	need, unsignedReg := 0, false
	switch iec {
	case "INT":
		need = 16
	case "UINT":
		need, unsignedReg = 16, true
	case "DINT":
		need = 32
	case "UDINT":
		need, unsignedReg = 32, true
	default:
		return true
	}
	switch {
	case !unsignedReg && unsignedMember:
		return false // a negative reading
	case unsignedReg && !unsignedMember:
		return w > need
	}
	return w >= need
}

func formatOf(b modbus.TagBinding) string {
	if b.Format == "" {
		return "bool"
	}
	return b.Format
}

func scaleOf(b modbus.TagBinding) float64 {
	if b.Scale == 0 {
		return 1
	}
	return b.Scale
}

func intOf(v any) (int, error) {
	switch x := v.(type) {
	case int:
		return x, nil
	case int64:
		return int(x), nil
	case uint64:
		return int(x), nil
	case float64:
		if x == math.Trunc(x) {
			return int(x), nil
		}
	case string:
		return strconv.Atoi(strings.TrimSpace(x))
	}
	return 0, fmt.Errorf("not a whole number")
}

func floatOf(v any) (float64, error) {
	switch x := v.(type) {
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case uint64:
		return float64(x), nil
	case float64:
		return x, nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(x), 64)
	}
	return 0, fmt.Errorf("not a number")
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
