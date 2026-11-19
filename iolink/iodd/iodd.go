// Package iodd reads IO-Link Device Descriptions (IODD 1.0.1 and 1.1) and
// decodes and encodes the process data they describe
// (docs/design/iolink.md §4, §5).
//
// It is a leaf: it imports no other nautilus package, so the device
// library, the IO-Link adapter and the CLI can all read it. It keeps only
// what nautilus uses from an IODD (identity, process data, scaling, value
// names, variables and texts) and ignores the rest (menus, user roles,
// images, communication characteristics).
package iodd

import (
	"fmt"
	"slices"
	"strings"
)

// Schema namespaces, by IODD version: the root IODevice element's
// namespace tells them apart.
const (
	NS101 = "http://www.io-link.com/IODD/2009/11"
	NS11  = "http://www.io-link.com/IODD/2010/10"
)

// IODD is one parsed device description.
type IODD struct {
	// Version is "1.0.1" or "1.1", from the root namespace.
	Version  string
	Identity Identity
	// ProcessData are the device's process data layouts. A device whose
	// layout depends on a parameter has several, each with a Condition.
	ProcessData []ProcessData
	// Variables are the device's parameters, standard ones included, in
	// IODD order.
	Variables []Variable
}

// Identity is what a master reports for the device on a port, and what
// a person reads.
type Identity struct {
	// VendorID is the IO-Link vendor id (16 bits).
	VendorID uint32
	// DeviceID is the IO-Link device id (24 bits).
	DeviceID uint32
	// AdditionalDeviceIDs are other device ids the IODD says it covers.
	AdditionalDeviceIDs []uint32
	VendorName          string
	DeviceName          string
	// Variants are the products the IODD covers (one device id, several
	// order numbers).
	Variants []Variant
}

// Variant is one product of a DeviceVariantCollection.
type Variant struct {
	ProductID   string
	Name        string
	Description string
}

// Matches reports whether a master's (vendor id, device id) is this
// device.
func (id Identity) Matches(vendor, device uint32) bool {
	if vendor != id.VendorID {
		return false
	}
	if device == id.DeviceID {
		return true
	}
	return slices.Contains(id.AdditionalDeviceIDs, device)
}

// Condition says which value of a variable selects a process data layout.
type Condition struct {
	VariableID string
	Subindex   int
	Value      int64
}

// ProcessData is one layout of a device's process data.
type ProcessData struct {
	ID string
	// Condition is nil for a device with one layout.
	Condition *Condition
	In        *Image
	Out       *Image
}

// Image is process data in one direction.
type Image struct {
	ID   string
	Name string
	// BitLength is the image's length in bits; the image on the wire is
	// BitLength/8 octets, rounded up.
	BitLength int
	// Items are the record's items, or one item at offset 0 when the
	// process data is a single value (its Subindex is then 0).
	Items []Item
}

// Octets is the image's length on the wire.
func (im *Image) Octets() int { return (im.BitLength + 7) / 8 }

// Kind is an IODD simple type.
type Kind int

const (
	Boolean Kind = iota + 1
	UInteger
	Integer
	Float32
	String
	OctetString
	// Unsupported marks a type the codec does not decode (TimeT,
	// TimeSpanT, ArrayT, RecordT inside a record); Type.Name says which.
	Unsupported
)

// Type is a simple datatype.
type Type struct {
	Kind Kind
	// Name is the IODD's spelling (UIntegerT).
	Name string
	// BitLength is the type's width in bits: 1 for a boolean, 32 for a
	// float, 8 × FixedLength for a string or octet string.
	BitLength int
	// FixedLength is a string's or octet string's length in octets.
	FixedLength int
	// Encoding is a string's: "US-ASCII" or "UTF-8".
	Encoding string
	// Values are the type's named values (SingleValue).
	Values []NamedValue
	// Ranges are the type's value ranges (ValueRange).
	Ranges []Range
}

// NamedValue is a SingleValue: a value with a name.
type NamedValue struct {
	Value string
	Name  string
}

// Range is a ValueRange.
type Range struct {
	Lower, Upper string
	Name         string
}

// Item is one record item, or the whole image when it is a single value.
type Item struct {
	Subindex  int
	BitOffset int
	Name      string
	Type      Type
	// Gradient and Offset scale an integer (displayed = raw × Gradient +
	// Offset); Scaled says the IODD gave either. From the IODD 1.1 user
	// interface's ProcessDataRefCollection.
	Gradient, Offset float64
	Scaled           bool
	// UnitCode is the IO-Link unit code (0: none); Unit its symbol when
	// the code is one this package knows.
	UnitCode int
	Unit     string
}

// Variable is a device parameter, reached by ISDU at Index (and the
// subindexes of its record items).
type Variable struct {
	ID    string
	Index int
	// Access is "ro", "wo" or "rw".
	Access  string
	Default string
	Name    string
	// Standard says the variable came from a StdVariableRef.
	Standard bool
	// Type is the variable's type when it is a simple one; Items are its
	// record items when it is a record.
	Type  Type
	Items []Item
}

// IECType is the IEC 61131-3 type a decoded item is stored as
// (docs/design/iolink.md §4.1), or "" when the codec does not decode it.
func (it Item) IECType() string {
	if it.Scaled && (it.Type.Kind == UInteger || it.Type.Kind == Integer) {
		return "REAL"
	}
	t := it.Type
	switch t.Kind {
	case Boolean:
		return "BOOL"
	case UInteger:
		return widthType(t.BitLength, "USINT", "UINT", "UDINT", "ULINT")
	case Integer:
		return widthType(t.BitLength, "SINT", "INT", "DINT", "LINT")
	case Float32:
		return "REAL"
	case String:
		return "STRING"
	case OctetString:
		return fmt.Sprintf("ARRAY [0..%d] OF BYTE", t.FixedLength-1)
	}
	return ""
}

func widthType(bits int, t8, t16, t32, t64 string) string {
	switch {
	case bits <= 8:
		return t8
	case bits <= 16:
		return t16
	case bits <= 32:
		return t32
	}
	return t64
}

// Layout returns the process data layout a device uses: the only one,
// or, for a device with conditional layouts, the one whose condition
// value is variant. variant is ignored (and must be "") for a device with
// one layout.
func (d *IODD) Layout(variant string) (*ProcessData, error) {
	if len(d.ProcessData) == 0 {
		return nil, fmt.Errorf("the IODD declares no process data")
	}
	conditional := false
	for _, pd := range d.ProcessData {
		if pd.Condition != nil {
			conditional = true
		}
	}
	if !conditional {
		if variant != "" {
			return nil, fmt.Errorf("pd-variant %s: the IODD has one process data layout, so it takes no pd-variant", variant)
		}
		if len(d.ProcessData) > 1 {
			return nil, fmt.Errorf("the IODD declares %d process data layouts with no condition to choose between them", len(d.ProcessData))
		}
		return &d.ProcessData[0], nil
	}
	var values []string
	for i := range d.ProcessData {
		pd := &d.ProcessData[i]
		if pd.Condition == nil {
			continue
		}
		v := fmt.Sprint(pd.Condition.Value)
		values = append(values, v)
		if v == strings.TrimSpace(variant) {
			return pd, nil
		}
	}
	cond := d.ProcessData[0].Condition
	if cond == nil {
		cond = d.ProcessData[len(d.ProcessData)-1].Condition
	}
	if variant == "" {
		return nil, fmt.Errorf("the process data layout depends on %s; say which with pd-variant: one of %s", cond.VariableID, strings.Join(values, ", "))
	}
	return nil, fmt.Errorf("pd-variant %s is not a value of %s the IODD has a layout for (%s)", variant, cond.VariableID, strings.Join(values, ", "))
}
