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
}

// WrapOPCUA wraps the driver built for device name from OPCUADevices;
// security is the policy it was built with (the row's, or its driver:
// section's), which naut check names when it is None.
func (s *Set) WrapOPCUA(name string, d *opcua.Driver, security string) (*OPCUADriver, error) {
	for _, dev := range s.protocol(devprofile.ProtoOPCUA) {
		if dev.Name != name {
			continue
		}
		w := &OPCUADriver{Driver: d, st: newStructs([]*device{dev}, d.InputNames())}
		for _, od := range s.OPCUADevices() {
			if od.Name != name {
				continue
			}
			switch {
			case od.Driver == "":
				w.warnings = append(w.warnings, fmt.Sprintf(
					"device %s connects with security None, anonymous: its row names no driver: section with its own security, so the session is neither signed nor encrypted even if the device offers more",
					name))
			case strings.EqualFold(security, "None"):
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
func (w *OPCUADriver) NewAssembly() func(nio.Values) { return w.st.newAssembly() }

// Unwrap is the OPC UA driver underneath.
func (w *OPCUADriver) Unwrap() nio.Driver { return w.Driver }

// ReadInputs delivers the device as its struct.
func (w *OPCUADriver) ReadInputs() (nio.Values, error) {
	vals, err := w.Driver.ReadInputs()
	if err != nil {
		return nil, err
	}
	w.st.assemble(vals)
	return vals, nil
}

// ReadInputsInto is ReadInputs into the runtime's map (io.BatchReader).
func (w *OPCUADriver) ReadInputsInto(dst nio.Values) error {
	w.st.clear(dst)
	if err := w.Driver.ReadInputsInto(dst); err != nil {
		return err
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
	return w.Driver.InputType(name)
}

// Quality reports the device by its members (rollup).
func (w *OPCUADriver) Quality() map[string]nio.Quality { return w.st.quality(w.Driver.Quality()) }

// Warnings are the driver's own, plus each namespace the device is bound
// to by index alone.
func (w *OPCUADriver) Warnings() []string {
	return append(w.Driver.Warnings(), w.warnings...)
}
