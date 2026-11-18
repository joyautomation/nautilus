package virtdev

import (
	"fmt"

	"github.com/joyautomation/nautilus/devprofile"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/slave"
)

// store is one device's data as its protocol holds it: a Modbus unit's
// register and coil tables, an OPC UA server's node values. The plant
// reads what the controller wrote from it and writes what the device shows
// into it; the wire face serves it.
type store interface {
	get(pt devprofile.Point) (any, error)
	put(pt devprofile.Point, v any) error
}

// modbusStore is a device's Modbus unit, read and written through the
// same codec the modbus driver uses, so a value lands in exactly the
// registers, word order and scaling the driver will read it back from.
type modbusStore struct {
	unit                 *slave.Unit
	wordOrder, byteOrder string
}

func newModbusStore(p *devprofile.Profile) *modbusStore {
	u := slave.NewUnit()
	spec := p.Modbus
	if !spec.Open {
		for _, pt := range p.Points {
			u.Define(pt.Table, pt.Address, uint16(pt.Words()), pt.Writable)
		}
	}
	return &modbusStore{unit: u, wordOrder: spec.WordOrder, byteOrder: spec.ByteOrder}
}

func (s *modbusStore) get(pt devprofile.Point) (any, error) {
	return GetBinding(s.unit, s.binding(pt), s.wordOrder, s.byteOrder)
}

func (s *modbusStore) put(pt devprofile.Point, v any) error {
	return PutBinding(s.unit, s.binding(pt), s.wordOrder, s.byteOrder, v)
}

func (s *modbusStore) binding(pt devprofile.Point) modbus.TagBinding {
	return modbus.TagBinding{
		Name: pt.Name, Table: pt.Table, Address: pt.Address,
		Format: pt.Format, Scale: pt.Scale, Offset: pt.Offset, Writable: pt.Writable,
	}
}

// PutBinding writes one engineering value into a unit through a binding:
// the encode the modbus driver applies to a write, and the one
// `naut modbus serve` seeds with. A bit:N value merges into the register's
// other bits.
func PutBinding(u *slave.Unit, t modbus.TagBinding, wordOrder, byteOrder string, v any) error {
	switch t.Table {
	case modbus.TableCoil:
		u.SetCoil(t.Address, truthy(v))
		return nil
	case modbus.TableDiscrete:
		u.SetDiscrete(t.Address, truthy(v))
		return nil
	case modbus.TableHolding, modbus.TableInput:
	default:
		return fmt.Errorf("table %q", t.Table)
	}
	format := t.Format
	if format == "" {
		format = "uint16"
	}
	f, err := modbus.ParseFormat(format)
	if err != nil {
		return err
	}
	get, set := u.Holding, u.SetHolding
	if t.Table == modbus.TableInput {
		get, set = u.Input, u.SetInput
	}
	if f.Kind == "bit" {
		// Encode(true) carries exactly the mask for this bit in this byte
		// order; merge it so the register's other bits survive.
		mask, err := modbus.Encode(f, true, wordOrder, byteOrder, 0, 0)
		if err != nil {
			return err
		}
		cur := get(t.Address)
		if truthy(v) {
			cur |= mask[0]
		} else {
			cur &^= mask[0]
		}
		set(t.Address, cur)
		return nil
	}
	regs, err := modbus.Encode(f, v, wordOrder, byteOrder, t.Scale, t.Offset)
	if err != nil {
		return err
	}
	for i, r := range regs {
		set(t.Address+uint16(i), r)
	}
	return nil
}

// GetBinding reads one engineering value out of a unit through a binding:
// the decode the modbus driver applies to a read.
func GetBinding(u *slave.Unit, t modbus.TagBinding, wordOrder, byteOrder string) (any, error) {
	switch t.Table {
	case modbus.TableCoil:
		return u.Coil(t.Address), nil
	case modbus.TableDiscrete:
		return u.Discrete(t.Address), nil
	case modbus.TableHolding, modbus.TableInput:
	default:
		return nil, fmt.Errorf("table %q", t.Table)
	}
	format := t.Format
	if format == "" {
		format = "uint16"
	}
	f, err := modbus.ParseFormat(format)
	if err != nil {
		return nil, err
	}
	get := u.Holding
	if t.Table == modbus.TableInput {
		get = u.Input
	}
	regs := make([]uint16, f.Words())
	for i := range regs {
		regs[i] = get(t.Address + uint16(i))
	}
	return modbus.Decode(f, regs, wordOrder, byteOrder, t.Scale, t.Offset)
}

// truthy is the JSON-value face of a bool: true, or any nonzero number.
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case int64:
		return x != 0
	case int:
		return x != 0
	}
	return false
}
