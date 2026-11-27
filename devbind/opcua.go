//go:build !js

package devbind

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/devprofile"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/opcua"
)

// OPCUADevice is one OPC UA device as its own opcua-client driver (see
// OPCUARows), named after the device: its companion is <Device>__Online,
// as a Modbus device's source is.
type OPCUADevice struct {
	OPCUARow
	// Manifest has a read binding per selected point under its member
	// name, and a binding per selected writable point under its command
	// tag's name, written and never polled (scan class none: the
	// command is the program's, never read back over it).
	Manifest opcua.Manifest
	// IndexOnly are the namespace indices the manifest addresses by index
	// alone, the profile's URI for them being a placeholder.
	IndexOnly []int
}

// opcuaTypes is the OPC UA built-in type an IEC point type is written as.
var opcuaTypes = map[string]string{
	"BOOL": "Boolean", "BYTE": "Byte", "INT": "Int16", "UINT": "UInt16", "WORD": "UInt16",
	"DINT": "Int32", "UDINT": "UInt32", "DWORD": "UInt32", "LINT": "Int64", "ULINT": "UInt64",
	"REAL": "Float", "LREAL": "Double", "STRING": "String",
}

// OPCUADevices are the set's OPC UA devices, in declaration order.
func (s *Set) OPCUADevices() []OPCUADevice {
	var out []OPCUADevice
	rows := s.OPCUARows()
	for i, d := range s.protocol(devprofile.ProtoOPCUA) {
		p := d.prof
		od := OPCUADevice{OPCUARow: rows[i]}
		od.Manifest.Namespaces = append([]string{"http://opcfoundation.org/UA/"}, p.OPCUA.Namespaces...)
		indexOnly := map[int]bool{}
		for _, pt := range d.points {
			nid := p.Substitute(pt.NodeID, d.Params)
			b := opcua.TagBinding{Name: memberName(d.Name, pt.Name), NodeID: nid, Type: opcuaTypes[pt.Type], Desc: pt.Desc}
			if pt.Wire != "" {
				b.Type = pt.Wire // STRING hex over octets (profile validation allows only that)
			}
			if ns := devprofile.NodeNamespace(nid); ns > 0 {
				if uri := p.Namespace(ns); uri != "" && !devprofile.IsPlaceholderNamespace(uri) {
					b.Namespace = uri
				} else {
					indexOnly[ns] = true
				}
			}
			od.Manifest.Tags = append(od.Manifest.Tags, b)
			if pt.Writable {
				b.Name, b.Writable, b.ScanClass = CommandTag(d.Name, pt.Name), true, opcua.NoPoll
				od.Manifest.Tags = append(od.Manifest.Tags, b)
			}
		}
		for ns := 1; ns <= len(p.OPCUA.Namespaces); ns++ {
			if indexOnly[ns] {
				od.IndexOnly = append(od.IndexOnly, ns)
			}
		}
		out = append(out, od)
	}
	return out
}

// OPCUADriver is an OPC UA device's driver, its points arriving as the
// device's struct tag. It is the driver itself (health, the write gate,
// start and stop are still there), with the input side translated:
// members in, the struct out.
type OPCUADriver struct {
	*opcua.Driver
	st       structs
	warnings []string
	// ports are the master's port devices (#705); nil for a device with none.
	ports *ports
}

// WrapOPCUA wraps the driver built for device name from OPCUADevices;
// policy and mode are the security it was built with (the row's, or its
// driver: section's), which naut check names when either is None: a mode
// of None is an unsigned session whatever the policy.
func (s *Set) WrapOPCUA(name string, d *opcua.Driver, policy, mode string) (*OPCUADriver, error) {
	for _, dev := range s.protocol(devprofile.ProtoOPCUA) {
		if dev.Name != name {
			continue
		}
		w := &OPCUADriver{Driver: d, st: newStructs([]*device{dev}, d.InputNames())}
		if len(dev.ports) > 0 {
			w.ports = newPorts(dev.ports)
			w.st.inputs = append(w.st.inputs, w.ports.names()...)
			w.warnings = append(w.warnings, portWarnings(dev.ports)...)
		}
		for _, od := range s.OPCUADevices() {
			if od.Name != name {
				continue
			}
			switch {
			case od.Driver == "":
				w.warnings = append(w.warnings, fmt.Sprintf(
					"device %s connects with security None, anonymous: its row names no driver: section with its own security, so the session is neither signed nor encrypted even if the device offers more",
					name))
			case strings.EqualFold(policy, "None") || strings.EqualFold(mode, "None"):
				w.warnings = append(w.warnings, fmt.Sprintf("device %s connects with security None, as its driver: section %s sets", name, od.Driver))
			}
			for _, ns := range od.IndexOnly {
				w.warnings = append(w.warnings, fmt.Sprintf(
					"device %s binds by namespace index %d: profile %s's URI for it is a placeholder (%s), so a server that renumbers its namespaces would be misread",
					name, ns, dev.prof.ID, dev.prof.Namespace(ns)))
			}
		}
		return w, nil
	}
	return nil, fmt.Errorf("device %s is not an OPC UA device of the set", name)
}

// NewAssembly is a fresh copy of the adapter's input side (see
// ModbusDriver.NewAssembly).
func (w *OPCUADriver) NewAssembly() func(nio.Values) {
	f := w.st.newAssembly()
	if w.ports == nil {
		return f
	}
	g := w.ports.newAssembly()
	return func(vals nio.Values) { g(vals); f(vals) }
}

// Unwrap is the OPC UA driver underneath.
func (w *OPCUADriver) Unwrap() nio.Driver { return w.Driver }

// ReadInputs delivers the device as its struct.
func (w *OPCUADriver) ReadInputs() (nio.Values, error) {
	vals, err := w.Driver.ReadInputs()
	if err != nil {
		return nil, err
	}
	if w.ports != nil {
		w.ports.take(vals) // before the master's own struct takes its members
	}
	w.st.assemble(vals)
	return vals, nil
}

// ReadInputsInto is ReadInputs into the runtime's map (io.BatchReader).
func (w *OPCUADriver) ReadInputsInto(dst nio.Values) error {
	w.st.clear(dst)
	if w.ports != nil {
		w.ports.clear(dst)
	}
	if err := w.Driver.ReadInputsInto(dst); err != nil {
		return err
	}
	if w.ports != nil {
		w.ports.take(dst)
	}
	w.st.assemble(dst)
	return nil
}

// InputNames lists the device tag in place of its members.
func (w *OPCUADriver) InputNames() []string { return append([]string(nil), w.st.inputs...) }

// InputType is the device's struct type, else the driver's own answer.
func (w *OPCUADriver) InputType(name string) string {
	if t, ok := w.st.inputType(name); ok {
		return t
	}
	if w.ports != nil {
		if t, ok := w.ports.inputType(name); ok {
			return t
		}
	}
	return w.Driver.InputType(name)
}

// Quality reports the device by its members (rollup).
func (w *OPCUADriver) Quality() map[string]nio.Quality {
	raw := w.Driver.Quality()
	out := w.st.quality(raw)
	if w.ports != nil {
		out = w.ports.quality(raw, out)
	}
	return out
}

// PortWrites is what the driver is handed for a push, as WriteOutputs
// makes it: virtdev's in-process face (naut test) answers the master's
// command bindings itself, and must get the ports' gated output through
// the same gate, not a silent drop of the ports' command tags. A master
// with no ports gets the push as it is.
func (w *OPCUADriver) PortWrites(vals nio.Values) (nio.Values, error) {
	if w.ports == nil {
		return vals, nil
	}
	return w.ports.transform(vals)
}

// WriteOutputs hands the driver what the runtime pushed. A master with
// port devices takes its ports' command tags out and puts each port's
// gated process data out in their place (port.go).
func (w *OPCUADriver) WriteOutputs(vals nio.Values) error {
	if w.ports == nil {
		return w.Driver.WriteOutputs(vals)
	}
	return w.ports.write(w.Driver, vals)
}

// Warnings are the driver's own, plus each namespace the device is bound
// to by index alone.
func (w *OPCUADriver) Warnings() []string {
	return append(w.Driver.Warnings(), w.warnings...)
}

// The wrapper embeds the driver, so its InputsReady (#786) is the driver's.
var _ nio.InputsReadier = (*OPCUADriver)(nil)
