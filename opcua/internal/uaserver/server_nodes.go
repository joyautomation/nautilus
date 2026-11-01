package uaserver

import (
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/opcua/internal/uaserver/attrs"
)

func RootNode() *Node {
	return NewNode(
		ua.NewNumericNodeID(0, id.RootFolder),
		map[ua.AttributeID]*ua.DataValue{
			ua.AttributeIDNodeClass:  DataValueFromValue(attrs.NodeClass(ua.NodeClassObject)),
			ua.AttributeIDBrowseName: DataValueFromValue(attrs.BrowseName("Root")),
			ua.AttributeIDDataType:   DataValueFromValue(ua.NewNumericExpandedNodeID(0, id.DataTypesFolder)),
		},
		nil,
		nil,
	)
}

// setServerObjectValues gives the Server object's variables their values.
// The nodes are the standard node set's own (ImportNodeSet), so their
// BrowseNames, DataTypes, ValueRanks and references are the standard's:
// BuildInfo's six fields hang under BuildInfo, BuildInfo under
// ServerStatus, and so on. gopcua's original replaced them with nodes of
// its own, which got names wrong (ServerStatus was "Status", State was
// "ServerStatus", ProductUri "ProductURI", ManufacturerName
// "ProductName", NamespaceArray an Object called "Namespaces"), lost
// every reference but a flat list under ServerStatus, and had no DataType
// or ValueRank (nautilus#424).
//
// The Mandatory variables of ServerType (Part 5 §6.3.1) that gopcua left
// valueless, so that a Read of their Value answered Bad_AttributeIdInvalid,
// have values: ServerArray, whose only entry is this server's
// ApplicationUri; ServiceLevel, 255 (healthy) while the server runs and 0
// (out of service) otherwise; and Auditing, false, as this server writes
// no audit events.
func setServerObjectValues(s *Server) {
	set := func(nid uint32, f func() any) {
		if n := s.namespaces[0].Node(ua.NewNumericNodeID(0, nid)); n != nil {
			n.val = func() *ua.DataValue { return DataValueFromValue(f()) }
		}
	}
	build := func() *ua.BuildInfo { return s.Status().BuildInfo }

	set(id.Server_ServerArray, func() any { return []string{s.cfg.applicationURI} })
	set(id.Server_NamespaceArray, func() any {
		n := s.Namespaces()
		ns := make([]string, len(n))
		for i := range ns {
			ns[i] = n[i].Name()
		}
		return ns
	})
	set(id.Server_ServiceLevel, func() any {
		if s.Status().State == ua.ServerStateRunning {
			return byte(255)
		}
		return byte(0)
	})
	set(id.Server_Auditing, func() any { return false })

	set(id.Server_ServerStatus, func() any { return ua.NewExtensionObject(s.Status()) })
	set(id.Server_ServerStatus_StartTime, func() any { return s.Status().StartTime })
	set(id.Server_ServerStatus_CurrentTime, func() any { return time.Now() })
	set(id.Server_ServerStatus_State, func() any { return int32(s.Status().State) })
	set(id.Server_ServerStatus_SecondsTillShutdown, func() any { return s.Status().SecondsTillShutdown })
	set(id.Server_ServerStatus_ShutdownReason, func() any {
		if r := s.Status().ShutdownReason; r != nil {
			return r
		}
		return &ua.LocalizedText{}
	})

	set(id.Server_ServerStatus_BuildInfo, func() any { return ua.NewExtensionObject(build()) })
	set(id.Server_ServerStatus_BuildInfo_ProductURI, func() any { return build().ProductURI })
	set(id.Server_ServerStatus_BuildInfo_ManufacturerName, func() any { return build().ManufacturerName })
	set(id.Server_ServerStatus_BuildInfo_ProductName, func() any { return build().ProductName })
	set(id.Server_ServerStatus_BuildInfo_SoftwareVersion, func() any { return build().SoftwareVersion })
	set(id.Server_ServerStatus_BuildInfo_BuildNumber, func() any { return build().BuildNumber })
	set(id.Server_ServerStatus_BuildInfo_BuildDate, func() any { return build().BuildDate })

	set(id.Server_ServerCapabilities_OperationLimits_MaxNodesPerRead, func() any {
		return s.cfg.cap.OperationalLimits.MaxNodesPerRead
	})
}
