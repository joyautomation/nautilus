//go:build !js

package devbind

import (
	"fmt"
	"sort"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
)

// ModbusManifest is what the Modbus devices add to the project's Modbus
// driver: a source per device, named as the device (so its companions are
// <Device>__Online and <Device>__Outages), a read binding per selected
// point under its member name, and a write-only binding per selected
// writable point under its command tag's name.
func (s *Set) ModbusManifest() modbus.Manifest {
	var m modbus.Manifest
	for _, d := range s.devices {
		host, port, _ := d.hostPort() // Resolve checked it
		src := modbus.Source{ID: d.Name, Host: host, Port: port, UnitID: d.unitID()}
		if mb := d.prof.Modbus; mb != nil {
			src.WordOrder, src.ByteOrder = mb.WordOrder, mb.ByteOrder
		}
		m.Sources = append(m.Sources, src)
		for _, pt := range d.points {
			b := modbus.TagBinding{
				Name: memberName(d.Name, pt.Name), Source: d.Name, Table: pt.Table, Address: pt.Address,
				Format: pt.Format, Scale: pt.Scale, Offset: pt.Offset,
			}
			m.Tags = append(m.Tags, b)
			if pt.Writable {
				b.Name, b.Writable, b.WriteOnly = CommandTag(d.Name, pt.Name), true, true
				m.Tags = append(m.Tags, b)
			}
		}
	}
	return m
}

// MergeModbus adds the devices' sources and bindings to a project's own
// Modbus manifest. A source id or binding name both declare is an error.
func (s *Set) MergeModbus(own modbus.Manifest) (modbus.Manifest, error) {
	add := s.ModbusManifest()
	ids := map[string]bool{}
	for _, src := range own.Sources {
		ids[src.ID] = true
	}
	names := map[string]bool{}
	for _, t := range own.Tags {
		names[t.Name] = true
	}
	for _, src := range add.Sources {
		if ids[src.ID] {
			return own, fmt.Errorf("device %s: the Modbus manifest already has a source %s", src.ID, src.ID)
		}
	}
	for _, t := range add.Tags {
		if names[t.Name] {
			return own, fmt.Errorf("device %s: the Modbus manifest already binds %s", t.Source, t.Name)
		}
	}
	out := modbus.Manifest{
		Sources: append(append([]modbus.Source(nil), own.Sources...), add.Sources...),
		Tags:    append(append([]modbus.TagBinding(nil), own.Tags...), add.Tags...),
	}
	return out, nil
}

// ModbusDriver is a Modbus driver whose devices arrive as struct tags. It
// is the driver itself (every method it has is still there: health,
// connections, the write gate, start and stop), with the input side
// translated: members in, structs out.
type ModbusDriver struct {
	*modbus.Driver
	asm    []*assembler
	member map[string]int // internal member name -> index into asm
	inputs []string
}

// WrapModbus wraps the project's Modbus driver, built from MergeModbus's
// manifest.
func (s *Set) WrapModbus(d *modbus.Driver) *ModbusDriver {
	w := &ModbusDriver{Driver: d, member: map[string]int{}}
	for _, dev := range s.devices {
		a := newAssembler(dev)
		for _, m := range a.members {
			w.member[m] = len(w.asm)
		}
		w.asm = append(w.asm, a)
	}
	seen := map[string]bool{}
	for _, n := range d.InputNames() {
		if i, ok := w.member[n]; ok {
			n = w.asm[i].dev
		}
		if !seen[n] {
			seen[n] = true
			w.inputs = append(w.inputs, n)
		}
	}
	return w
}

// Unwrap is the Modbus driver underneath.
func (w *ModbusDriver) Unwrap() nio.Driver { return w.Driver }

// ReadInputs delivers the devices as structs.
func (w *ModbusDriver) ReadInputs() (nio.Values, error) {
	vals, err := w.Driver.ReadInputs()
	if err != nil {
		return nil, err
	}
	w.assemble(vals)
	return vals, nil
}

// ReadInputsInto is ReadInputs into the runtime's map (io.BatchReader).
func (w *ModbusDriver) ReadInputsInto(dst nio.Values) error {
	if err := w.Driver.ReadInputsInto(dst); err != nil {
		return err
	}
	w.assemble(dst)
	return nil
}

func (w *ModbusDriver) assemble(vals nio.Values) {
	for _, a := range w.asm {
		a.take(vals)
	}
}

// InputNames lists the device tags in place of their members.
func (w *ModbusDriver) InputNames() []string { return append([]string(nil), w.inputs...) }

// InputType is a device's struct type, else the driver's own answer.
func (w *ModbusDriver) InputType(name string) string {
	for _, a := range w.asm {
		if a.dev == name {
			return a.def.Name
		}
	}
	return w.Driver.InputType(name)
}

// Quality reports a device by its members (rollup); other tags pass
// through.
func (w *ModbusDriver) Quality() map[string]nio.Quality {
	return rollup(w.asm, w.member, w.Driver.Quality())
}

// Connections names the device tags in place of their members.
func (w *ModbusDriver) Connections() []nio.Connection {
	cs := w.Driver.Connections()
	for i := range cs {
		seen := map[string]bool{}
		var tags []string
		for _, t := range cs[i].Inputs {
			if j, ok := w.member[t]; ok {
				t = w.asm[j].dev
			}
			if !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
		sort.Strings(tags)
		cs[i].Inputs = tags
	}
	return cs
}
