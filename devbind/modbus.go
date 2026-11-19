//go:build !js

package devbind

import (
	"fmt"

	"github.com/joyautomation/nautilus/devprofile"
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
	for _, d := range s.protocol(devprofile.ProtoModbus) {
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
	st structs
}

// WrapModbus wraps the project's Modbus driver, built from MergeModbus's
// manifest.
func (s *Set) WrapModbus(d *modbus.Driver) *ModbusDriver {
	return &ModbusDriver{Driver: d, st: newStructs(s.protocol(devprofile.ProtoModbus), d.InputNames())}
}

// NewAssembly is a fresh copy of the adapter's input side: a func that
// moves the devices' members out of a delivery and puts each device's
// struct in under its name, as ReadInputs does. It shares no state with
// the driver's own, so the in-process face of virtual devices (one per
// test) delivers through the same code without one test's values leaking
// into the next.
func (w *ModbusDriver) NewAssembly() func(nio.Values) { return w.st.newAssembly() }

// Unwrap is the Modbus driver underneath.
func (w *ModbusDriver) Unwrap() nio.Driver { return w.Driver }

// ReadInputs delivers the devices as structs.
func (w *ModbusDriver) ReadInputs() (nio.Values, error) {
	vals, err := w.Driver.ReadInputs()
	if err != nil {
		return nil, err
	}
	w.st.assemble(vals)
	return vals, nil
}

// ReadInputsInto is ReadInputs into the runtime's map (io.BatchReader).
func (w *ModbusDriver) ReadInputsInto(dst nio.Values) error {
	w.st.clear(dst)
	if err := w.Driver.ReadInputsInto(dst); err != nil {
		return err
	}
	w.st.assemble(dst)
	return nil
}

// InputNames lists the device tags in place of their members.
func (w *ModbusDriver) InputNames() []string { return append([]string(nil), w.st.inputs...) }

// InputType is a device's struct type, else the driver's own answer.
func (w *ModbusDriver) InputType(name string) string {
	if t, ok := w.st.inputType(name); ok {
		return t
	}
	return w.Driver.InputType(name)
}

// Quality reports a device by its members (rollup); other tags pass
// through.
func (w *ModbusDriver) Quality() map[string]nio.Quality { return w.st.quality(w.Driver.Quality()) }

// Connections names the device tags in place of their members.
func (w *ModbusDriver) Connections() []nio.Connection {
	return w.st.connections(w.Driver.Connections())
}
