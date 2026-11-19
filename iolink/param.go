package iolink

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/iolink/iodd"
)

// A parameter is a device variable reached by ISDU (docs/design/iolink.md
// §7): an index and a subindex on the wire, a typed value in the program.
// A simple variable is read and written whole (subindex 0); a record
// variable is read and written by item, at the item's subindex, each with
// its own type. Values are encoded and decoded by the same codec as process
// data, an ISDU value being the type's octets, right-aligned and rounded up
// to whole octets.

// Param is one addressable parameter of an IODD.
type Param struct {
	// Variable is the IODD variable; Item the record item when Subindex > 0.
	Variable iodd.Variable
	Item     *iodd.Item
	// Subindex is what goes on the wire: 0 for a whole simple variable.
	Subindex int
	typ      iodd.Type
	img      *iodd.Image
}

// FindParam resolves a reference to a parameter: a variable by id
// (V_SerialNumber) or name ("Serial Number"), any case, or by index
// (21); for a record, the item after a dot, by subindex or name
// (Window.2, Window.Upper, 65.2). A record named without an item is
// refused naming its items.
func FindParam(d *iodd.IODD, ref string) (*Param, error) {
	name, item, hasItem := strings.Cut(strings.TrimSpace(ref), ".")
	var v *iodd.Variable
	if n, err := strconv.Atoi(name); err == nil {
		for i := range d.Variables {
			if d.Variables[i].Index == n {
				v = &d.Variables[i]
				break
			}
		}
	} else {
		for i := range d.Variables {
			if strings.EqualFold(d.Variables[i].ID, name) || strings.EqualFold(d.Variables[i].Name, name) {
				v = &d.Variables[i]
				break
			}
		}
	}
	if v == nil {
		return nil, fmt.Errorf("the IODD has no variable %q", name)
	}
	p := &Param{Variable: *v}
	if len(v.Items) == 0 {
		if v.Type.Kind == 0 || v.Type.Kind == iodd.Unsupported {
			return nil, fmt.Errorf("variable %s (%s) has a type the codec does not carry", v.ID, v.Type.Name)
		}
		if hasItem {
			return nil, fmt.Errorf("variable %s is not a record: it has no item %q", v.ID, item)
		}
		p.typ = v.Type
	} else {
		if !hasItem {
			var names []string
			for _, it := range v.Items {
				names = append(names, fmt.Sprintf("%d %s", it.Subindex, it.Name))
			}
			return nil, fmt.Errorf("variable %s is a record: name an item (%s)", v.ID, strings.Join(names, ", "))
		}
		for i := range v.Items {
			it := &v.Items[i]
			if n, err := strconv.Atoi(item); (err == nil && it.Subindex == n) || (err != nil && strings.EqualFold(it.Name, item)) {
				p.Item, p.Subindex, p.typ = it, it.Subindex, it.Type
				break
			}
		}
		if p.Item == nil {
			return nil, fmt.Errorf("variable %s has no item %q", v.ID, item)
		}
		if p.typ.Kind == iodd.Unsupported {
			return nil, fmt.Errorf("item %s.%s (%s) has a type the codec does not carry", v.ID, item, p.typ.Name)
		}
	}
	octets := (p.typ.BitLength + 7) / 8
	if p.typ.Kind == iodd.String || p.typ.Kind == iodd.OctetString {
		octets = p.typ.FixedLength
	}
	if octets < 1 {
		return nil, fmt.Errorf("variable %s: the type %s has no length", v.ID, p.typ.Name)
	}
	p.img = &iodd.Image{BitLength: 8 * octets, Items: []iodd.Item{{Subindex: p.Subindex, Name: v.Name, Type: p.typ}}}
	return p, nil
}

// Type is the parameter's IODD type (the item's, for a record item).
func (p *Param) Type() iodd.Type { return p.typ }

// Index is the ISDU index.
func (p *Param) Index() int { return p.Variable.Index }

// Octets is the length of the value on the wire.
func (p *Param) Octets() int { return p.img.Octets() }

// CanRead and CanWrite say what the IODD's access rights allow. A standard
// variable the IODD did not restate has the rights of the standard.
func (p *Param) CanRead() bool  { return p.Variable.Access != "wo" }
func (p *Param) CanWrite() bool { return p.Variable.Access != "ro" }

// Decode decodes a read's octets: a bool, uint64, int64, float32 or string
// by the type, or []byte for an octet string.
func (p *Param) Decode(raw []byte) (any, error) {
	if !p.CanRead() {
		return nil, fmt.Errorf("%s is write-only", p.Variable.ID)
	}
	if len(raw) != p.Octets() {
		return nil, fmt.Errorf("%s: the device answered %d octets, and the parameter is %d", p.Variable.ID, len(raw), p.Octets())
	}
	vals, err := p.img.Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Variable.ID, err)
	}
	return vals[0], nil
}

// Encode builds a write's octets. A value outside the IODD's ranges, or
// that does not fit the type, is refused naming the variable, never clipped;
// so is a write to a read-only variable.
func (p *Param) Encode(v any) ([]byte, error) {
	if !p.CanWrite() {
		return nil, fmt.Errorf("%s is read-only", p.Variable.ID)
	}
	if err := p.checkRanges(v); err != nil {
		return nil, fmt.Errorf("%s: %w", p.Variable.ID, err)
	}
	raw, err := p.img.Encode([]any{v})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Variable.ID, err)
	}
	return raw, nil
}

func (p *Param) checkRanges(v any) error {
	if len(p.typ.Ranges) == 0 {
		return nil
	}
	switch p.typ.Kind {
	case iodd.UInteger, iodd.Integer:
		return p.checkIntRanges(v)
	case iodd.Float32:
		var f float64
		switch x := v.(type) {
		case float64:
			f = x
		case float32:
			f = float64(x)
		case uint64:
			f = float64(x)
		case int64:
			f = float64(x)
		case int:
			f = float64(x)
		default:
			return nil // the codec refuses what is not a number
		}
		for _, r := range p.typ.Ranges {
			lo, err1 := strconv.ParseFloat(r.Lower, 64)
			hi, err2 := strconv.ParseFloat(r.Upper, 64)
			if err1 == nil && err2 == nil && f >= lo && f <= hi {
				return nil
			}
		}
		return p.outsideRange(v)
	}
	return nil
}

// checkIntRanges compares an integer with the IODD's bounds as integers: a
// float64 loses the low bits of anything near 2^64, which is where a 64-bit
// unsigned range ends.
func (p *Param) checkIntRanges(v any) error {
	var n *big.Int
	switch x := v.(type) {
	case uint64:
		n = new(big.Int).SetUint64(x)
	case int64:
		n = big.NewInt(x)
	case int:
		n = big.NewInt(int64(x))
	case float64:
		if x != math.Trunc(x) || math.IsInf(x, 0) || math.IsNaN(x) {
			return nil // the codec refuses what is not an integer
		}
		n, _ = new(big.Float).SetFloat64(x).Int(nil)
	default:
		return nil // the codec refuses what is not a number
	}
	for _, r := range p.typ.Ranges {
		lo, ok1 := new(big.Int).SetString(r.Lower, 10)
		hi, ok2 := new(big.Int).SetString(r.Upper, 10)
		if ok1 && ok2 && n.Cmp(lo) >= 0 && n.Cmp(hi) <= 0 {
			return nil
		}
	}
	return p.outsideRange(v)
}

func (p *Param) outsideRange(v any) error {
	var parts []string
	for _, r := range p.typ.Ranges {
		parts = append(parts, r.Lower+".."+r.Upper)
	}
	return fmt.Errorf("%v is outside the IODD's range (%s)", v, strings.Join(parts, ", "))
}
