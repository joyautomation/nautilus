// enums.go serves the project's enumerated types as OPC UA enumeration
// DataTypes (nautilus#425), so a client sees the member names the HTTP API
// and the dashboard show — "Run", not only 10.
//
// Each IEC enumeration (TYPE Mode : (Idle, Run := 10, Fault)) is a DataType
// node in the project's namespace, a subtype of ns=0's Enumeration (i=29),
// so it is found where Part 3 puts it: Types / DataTypes / BaseDataType /
// Enumeration / Mode. It carries what Part 3 §5.8.3 asks of one:
//
//   - its DataTypeDefinition attribute, an EnumDefinition naming every
//     member and its value (mandatory since 1.04);
//   - one of the two properties, never both: EnumStrings (LocalizedText[])
//     when the values are 0, 1, …, n-1, so a value is its position, and
//     EnumValues (EnumValueType[]) otherwise, which IEC allows (Run := 10).
//
// A Variable whose declared type is an enumeration has that node as its
// DataType; its Value stays an Int32, the member's value, as Part 3 has an
// enumeration's value encoded. Writes are unchanged: the Write service
// converts by the encoding (dataType), and the front door refuses an
// integer that is no member's value.
//
// The set of enumerations is the runtime's as of Start, as every other type
// the address space shows is the store's as of New.
package server

import (
	"sort"
	"strconv"
	"strings"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/lang/ir"
	opcserver "github.com/joyautomation/nautilus/opcua/internal/uaserver"
	"github.com/joyautomation/nautilus/runtime"
)

// enumPrefix starts the string node id of an enumeration DataType
// ("ns=1;s=type:Mode") and of its property ("ns=1;s=type:Mode.EnumValues").
// A colon is in no IEC identifier, so no tag address can collide with one.
const enumPrefix = "type:"

// The two properties' browse names, standard ones in ns=0.
const (
	propEnumStrings = "EnumStrings"
	propEnumValues  = "EnumValues"
)

// enumTypes is the project's enumerations, by ir.NameKey of the name.
type enumTypes struct {
	byKey map[string]*ir.EnumDef
	keys  []string // sorted, for a stable order
}

// collectEnums gathers every enumeration the runtime declares (its TYPEs)
// or a tag holds anywhere beneath it (a struct field, an FB pin, an array
// element). The first declaration of a name wins.
func collectEnums(rt *runtime.Runtime) enumTypes {
	out := enumTypes{byKey: map[string]*ir.EnumDef{}}
	var walk func(t *ir.Type, depth int)
	walk = func(t *ir.Type, depth int) {
		if t == nil || depth > 32 {
			return
		}
		if t.Enum != nil {
			if t.Enum.Name == "" {
				return
			}
			k := ir.NameKey(t.Enum.Name)
			if _, dup := out.byKey[k]; !dup {
				out.byKey[k] = t.Enum
				out.keys = append(out.keys, k)
			}
			return
		}
		switch t.Kind {
		case ir.TypeStruct:
			if t.Struct != nil {
				for _, f := range t.Struct.Fields {
					walk(f.Type, depth+1)
				}
			}
		case ir.TypeFB:
			if t.FB != nil {
				for _, s := range t.FB.AllSlots() {
					walk(s.Type, depth+1)
				}
			}
		case ir.TypeArray:
			walk(t.Elem, depth+1)
		}
	}
	types := rt.Types()
	names := make([]string, 0, len(types))
	for n := range types {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		walk(types[n], 0)
	}
	tagTypes := rt.TagTypes()
	names = names[:0]
	for n := range tagTypes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		walk(tagTypes[n], 0)
	}
	sort.Strings(out.keys)
	return out
}

// dense reports whether d's values are 0, 1, …, n-1 in some order: the
// case EnumStrings can carry, a value being its position in the array.
func dense(d *ir.EnumDef) bool {
	seen := make([]bool, len(d.Members))
	for _, m := range d.Members {
		if m.Value < 0 || m.Value >= int64(len(d.Members)) || seen[m.Value] {
			return false
		}
		seen[m.Value] = true
	}
	return len(d.Members) > 0
}

// enumProperty is the one property an enumeration DataType carries.
func enumProperty(d *ir.EnumDef) string {
	if dense(d) {
		return propEnumStrings
	}
	return propEnumValues
}

func (ns *namespace) enumNodeID(d *ir.EnumDef) *ua.NodeID {
	return ua.NewStringNodeID(ns.id, enumPrefix+d.Name)
}

func (ns *namespace) enumPropID(d *ir.EnumDef) *ua.NodeID {
	return ua.NewStringNodeID(ns.id, enumPrefix+d.Name+"."+enumProperty(d))
}

// enumAt resolves a node id to an enumeration DataType (prop "") or to its
// property (prop its browse name). ok is false for any other node id.
func (ns *namespace) enumAt(nid *ua.NodeID) (d *ir.EnumDef, prop string, ok bool) {
	if nid == nil || nid.Namespace() != ns.id || nid.Type() != ua.NodeIDTypeString {
		return nil, "", false
	}
	rest, isType := strings.CutPrefix(nid.StringID(), enumPrefix)
	if !isType {
		return nil, "", false
	}
	name, p, hasProp := strings.Cut(rest, ".")
	d, ok = ns.enums.byKey[ir.NameKey(name)]
	if !ok {
		return nil, "", false
	}
	if hasProp {
		if !strings.EqualFold(p, enumProperty(d)) {
			return nil, "", false
		}
		return d, enumProperty(d), true
	}
	return d, "", true
}

// enumOf is the enumeration a Variable member holds: its own type's, or its
// element type's for an array of an enumeration. nil for anything else.
func enumOf(m member) *ir.EnumDef {
	t := m.typ
	if t != nil && t.Kind == ir.TypeArray {
		t = t.Elem
	}
	if t == nil || t.Enum == nil {
		return nil
	}
	return t.Enum
}

// dataTypeID is a Variable member's DataType attribute: the enumeration's
// own DataType node for an enumerated member, encoded as Int32 (dataType),
// else the ns=0 type dataType names.
func (ns *namespace) dataTypeID(m member) *ua.NodeID {
	dt := dataType(m)
	if dt == id.Int32 {
		if d := enumOf(m); d != nil {
			if known, ok := ns.enums.byKey[ir.NameKey(d.Name)]; ok {
				return ns.enumNodeID(known)
			}
		}
	}
	return ua.NewNumericNodeID(0, dt)
}

// enumTypeNode is the *Node ns=0's Enumeration refers to with HasSubtype:
// what its Browse renders for the reference.
func (ns *namespace) enumTypeNode(d *ir.EnumDef) *opcserver.Node {
	return opcserver.NewNode(
		ns.enumNodeID(d),
		map[ua.AttributeID]*ua.DataValue{
			ua.AttributeIDNodeClass:   opcserver.DataValueFromValue(int32(ua.NodeClassDataType)),
			ua.AttributeIDBrowseName:  opcserver.DataValueFromValue(ns.qualified(d.Name)),
			ua.AttributeIDDisplayName: opcserver.DataValueFromValue(text(d.Name)),
		},
		nil, nil,
	)
}

// enumDefinition is the DataTypeDefinition attribute: every member in
// declaration order.
func enumDefinition(d *ir.EnumDef) *ua.EnumDefinition {
	fields := make([]*ua.EnumField, len(d.Members))
	for i, m := range d.Members {
		fields[i] = &ua.EnumField{Value: m.Value, DisplayName: text(m.Name), Description: text(""), Name: m.Name}
	}
	return &ua.EnumDefinition{Fields: fields}
}

// enumPropValue is the property's Value: EnumStrings indexed by value, or
// EnumValues in declaration order.
func enumPropValue(d *ir.EnumDef) any {
	if enumProperty(d) == propEnumStrings {
		out := make([]*ua.LocalizedText, len(d.Members))
		for _, m := range d.Members {
			out[m.Value] = text(m.Name)
		}
		return out
	}
	out := make([]*ua.ExtensionObject, len(d.Members))
	for i, m := range d.Members {
		out[i] = ua.NewExtensionObject(&ua.EnumValueType{Value: m.Value, DisplayName: text(m.Name), Description: text("")})
	}
	return out
}

func describeEnum(d *ir.EnumDef) string {
	parts := make([]string, len(d.Members))
	for i, m := range d.Members {
		parts[i] = m.Name + " = " + strconv.FormatInt(m.Value, 10)
	}
	return "Enumeration " + d.Name + ": " + strings.Join(parts, ", ")
}

// enumAttribute answers an attribute of an enumeration DataType or of its
// property.
func (ns *namespace) enumAttribute(d *ir.EnumDef, prop string, attr ua.AttributeID) *ua.DataValue {
	if prop == "" {
		switch attr {
		case ua.AttributeIDNodeID:
			return value(ns.enumNodeID(d))
		case ua.AttributeIDNodeClass:
			return value(int32(ua.NodeClassDataType))
		case ua.AttributeIDBrowseName:
			return value(ns.qualified(d.Name))
		case ua.AttributeIDDisplayName:
			return value(text(d.Name))
		case ua.AttributeIDDescription:
			return value(text(describeEnum(d)))
		case ua.AttributeIDWriteMask, ua.AttributeIDUserWriteMask:
			return value(uint32(0))
		case ua.AttributeIDIsAbstract:
			return value(false)
		case ua.AttributeIDDataTypeDefinition:
			return value(ua.NewExtensionObject(enumDefinition(d)))
		}
		return status(ua.StatusBadAttributeIDInvalid)
	}
	switch attr {
	case ua.AttributeIDNodeID:
		return value(ns.enumPropID(d))
	case ua.AttributeIDNodeClass:
		return value(int32(ua.NodeClassVariable))
	case ua.AttributeIDBrowseName:
		return value(&ua.QualifiedName{Name: prop})
	case ua.AttributeIDDisplayName:
		return value(text(prop))
	case ua.AttributeIDDescription:
		return value(text("The members of " + d.Name))
	case ua.AttributeIDWriteMask, ua.AttributeIDUserWriteMask:
		return value(uint32(0))
	case ua.AttributeIDValue:
		return value(enumPropValue(d))
	case ua.AttributeIDDataType:
		if prop == propEnumStrings {
			return value(ua.NewNumericNodeID(0, id.LocalizedText))
		}
		return value(ua.NewNumericNodeID(0, id.EnumValueType))
	case ua.AttributeIDValueRank:
		return value(int32(1))
	case ua.AttributeIDArrayDimensions:
		return value([]uint32{uint32(len(d.Members))})
	case ua.AttributeIDAccessLevel, ua.AttributeIDUserAccessLevel:
		return value(uint8(ua.AccessLevelTypeCurrentRead))
	case ua.AttributeIDAccessLevelEx:
		return value(uint32(ua.AccessLevelTypeCurrentRead))
	case ua.AttributeIDMinimumSamplingInterval:
		return value(float64(0))
	case ua.AttributeIDHistorizing:
		return value(false)
	}
	return status(ua.StatusBadAttributeIDInvalid)
}

// enumBrowse lists an enumeration DataType's references (its supertype and
// its property) or its property's (its type definition and its owner).
func (ns *namespace) enumBrowse(d *ir.EnumDef, prop string) []*ua.ReferenceDescription {
	owner := &ua.ReferenceDescription{
		ReferenceTypeID: ua.NewNumericNodeID(0, id.HasProperty),
		NodeID:          ua.NewExpandedNodeID(ns.enumNodeID(d), "", 0),
		BrowseName:      ns.qualified(d.Name),
		DisplayName:     text(d.Name),
		NodeClass:       ua.NodeClassDataType,
		TypeDefinition:  ua.NewTwoByteExpandedNodeID(0),
	}
	if prop != "" {
		return []*ua.ReferenceDescription{
			{
				ReferenceTypeID: ua.NewNumericNodeID(0, id.HasTypeDefinition),
				IsForward:       true,
				NodeID:          ua.NewNumericExpandedNodeID(0, id.PropertyType),
				BrowseName:      &ua.QualifiedName{Name: "PropertyType"},
				DisplayName:     text("PropertyType"),
				NodeClass:       ua.NodeClassVariableType,
				TypeDefinition:  ua.NewTwoByteExpandedNodeID(0),
			},
			owner, // inverse: the property's owner
		}
	}
	p := enumProperty(d)
	return []*ua.ReferenceDescription{
		{
			ReferenceTypeID: ua.NewNumericNodeID(0, id.HasSubtype),
			IsForward:       false,
			NodeID:          ua.NewNumericExpandedNodeID(0, id.Enumeration),
			BrowseName:      &ua.QualifiedName{Name: "Enumeration"},
			DisplayName:     text("Enumeration"),
			NodeClass:       ua.NodeClassDataType,
			TypeDefinition:  ua.NewTwoByteExpandedNodeID(0),
		},
		{
			ReferenceTypeID: ua.NewNumericNodeID(0, id.HasProperty),
			IsForward:       true,
			NodeID:          ua.NewExpandedNodeID(ns.enumPropID(d), "", 0),
			BrowseName:      &ua.QualifiedName{Name: p},
			DisplayName:     text(p),
			NodeClass:       ua.NodeClassVariable,
			TypeDefinition:  ua.NewNumericExpandedNodeID(0, id.PropertyType),
		},
	}
}

// hangEnums adds each enumeration DataType under ns=0's Enumeration, the
// forward HasSubtype a client browses down to find it.
func (ns *namespace) hangEnums(enumeration *opcserver.Node) {
	for _, k := range ns.enums.keys {
		enumeration.AddRef(ns.enumTypeNode(ns.enums.byKey[k]), opcserver.RefType(id.HasSubtype), true)
	}
}
