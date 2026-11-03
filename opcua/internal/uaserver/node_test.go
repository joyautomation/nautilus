package uaserver

import (
	"testing"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/opcua/internal/require"
)

// TestDescriptionIsLocalizedText checks that nodes built by NewVariableNode
// and NewFolderNode store a LocalizedText in the Description attribute.
// Previously they stored a uint32 NodeClass, so reading the attribute through
// Node.Description() panicked on the type assertion.
func TestDescriptionIsLocalizedText(t *testing.T) {
	v := NewVariableNode(ua.NewNumericNodeID(1, 100), "temperature", int32(7))
	require.Equal(t, "temperature", v.Description().Text)

	f := NewFolderNode(ua.NewNumericNodeID(1, 101), "sensors")
	require.Equal(t, "sensors", f.Description().Text)
}

// TestDataTypeSurvivesClientWrite checks that DataType() tolerates a value
// written by a client. The Write service stores client-supplied values
// without validation, and the DataType attribute is spec-typed NodeId, so a
// write stores a *ua.NodeID where DataType() previously asserted
// *ua.ExpandedNodeID — panicking the server on the next Browse that resolved
// a reference to the node.
func TestDataTypeSurvivesClientWrite(t *testing.T) {
	n := NewVariableNode(ua.NewNumericNodeID(1, 102), "count", int32(0))

	// what AttributeService.Write does with a client WriteValue for the
	// DataType attribute. The returned status is ignored: SetAttribute
	// stores the value regardless.
	_ = n.SetAttribute(ua.AttributeIDDataType, DataValueFromValue(ua.NewNumericNodeID(0, id.Int32)))

	dt := n.DataType()
	require.NotNil(t, dt)
	require.NotNil(t, dt.NodeID)
	require.Equal(t, uint32(id.Int32), dt.NodeID.IntID())
}

// AddRef to a Variable that has a DataType but no HasTypeDefinition gives
// the reference the null NodeId as its TypeDefinition, never the DataType
// (nautilus#485): a Variable's DataType is not its type definition, and
// passing it off as one is the bug #479's second review caught in Browse.
// A target with a HasTypeDefinition keeps it.
func TestAddRefNeverUsesTheDataTypeAsTypeDefinition(t *testing.T) {
	parent := NewFolderNode(ua.NewNumericNodeID(1, 200), "parent")
	v := NewVariableNode(ua.NewNumericNodeID(1, 201), "level", float64(1.5))
	if v.TypeDefinition() != nil {
		t.Fatalf("precondition: NewVariableNode now has a HasTypeDefinition %v", v.TypeDefinition())
	}
	if dt := v.DataType(); dt == nil || dt.NodeID.IntID() == 0 {
		t.Fatalf("precondition: the variable has no DataType (%v)", dt)
	}
	typed := NewNode(ua.NewNumericNodeID(1, 202), map[ua.AttributeID]*ua.DataValue{
		ua.AttributeIDNodeClass:  DataValueFromValue(uint32(ua.NodeClassObject)),
		ua.AttributeIDBrowseName: DataValueFromValue(&ua.QualifiedName{NamespaceIndex: 1, Name: "typed"}),
	}, References{{
		ReferenceTypeID: ua.NewNumericNodeID(0, id.HasTypeDefinition),
		IsForward:       true,
		NodeID:          ua.NewNumericExpandedNodeID(0, id.FolderType),
	}}, nil)

	before := len(parent.refs)
	parent.AddRef(v, RefTypeIDOrganizes, true)
	parent.AddRef(typed, RefTypeIDOrganizes, true)
	got := parent.refs[before:]
	if td := got[0].TypeDefinition; td == nil || td.NodeID.Namespace() != 0 || td.NodeID.IntID() != 0 {
		t.Errorf("reference to a variable with no type definition: TypeDefinition %v, want the null NodeId (its DataType is %v)", td, v.DataType())
	}
	if td := got[1].TypeDefinition; td == nil || td.NodeID.IntID() != id.FolderType {
		t.Errorf("reference to a typed object: TypeDefinition %v, want FolderType (i=%d)", td, id.FolderType)
	}
}
