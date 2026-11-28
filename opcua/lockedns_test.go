package opcua

import (
	"sync"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/server/attrs"
	"github.com/gopcua/opcua/ua"
)

// lockedNS is the fixture's address space: gopcua's NodeNameSpace with the
// reads and writes of a node's value behind one lock.
//
// Why not NodeNameSpace as it comes: gopcua's Node is unsynchronized, and
// its server reads a node's attributes on goroutines of its own (a
// subscription's first notification, a monitored item's change check)
// while a client's Write replaces the same field on the service goroutine.
// The detector sees no ordering between them (only the TCP stream orders
// them), so `go test -race` reports a data race in the library that fails
// the race nightly (#1405). v0.9.4, the latest, has the same code. Every
// way the server reaches an attribute goes through the namespace, so one
// RWMutex here removes the race without hiding any of ours.
//
// The namespace must hold the index the first custom namespace has had so
// far (ns=2: tests spell node ids that way), so it registers FIRST, and the
// NodeNameSpace it wraps registers after it, under a throwaway URI that no
// node id names.
type lockedNS struct {
	*server.NodeNameSpace
	srv  *server.Server
	mu   sync.RWMutex
	id   uint16
	name string
}

func newLockedNS(srv *server.Server, uri string) *lockedNS {
	l := &lockedNS{srv: srv, name: uri}
	srv.AddNamespace(l) // takes the index, and calls SetID
	l.NodeNameSpace = server.NewNodeNameSpace(srv, uri+"#unrouted")
	l.NodeNameSpace.SetID(l.id) // the nodes it builds carry our index
	// NewNodeNameSpace made this namespace's Objects node under the index it
	// registered with, not ours; the server's Objects folder points at ours.
	typedef := ua.NewNumericExpandedNodeID(0, id.ObjectsFolder)
	l.NodeNameSpace.AddNode(server.NewNode(ua.NewNumericNodeID(l.id, id.ObjectsFolder),
		map[ua.AttributeID]*ua.DataValue{
			ua.AttributeIDNodeClass:     server.DataValueFromValue(uint32(ua.NodeClassObject)),
			ua.AttributeIDBrowseName:    server.DataValueFromValue(attrs.BrowseName(uri)),
			ua.AttributeIDDisplayName:   server.DataValueFromValue(attrs.DisplayName(uri, uri)),
			ua.AttributeIDDescription:   server.DataValueFromValue(uint32(ua.NodeClassObject)),
			ua.AttributeIDDataType:      server.DataValueFromValue(typedef),
			ua.AttributeIDEventNotifier: server.DataValueFromValue(int16(0)),
		}, []*ua.ReferenceDescription{}, nil))
	return l
}

func (l *lockedNS) Name() string { return l.name }
func (l *lockedNS) ID() uint16   { return l.id }
func (l *lockedNS) SetID(id uint16) {
	l.id = id
	if l.NodeNameSpace != nil {
		l.NodeNameSpace.SetID(id)
	}
}

func (l *lockedNS) Attribute(id *ua.NodeID, attr ua.AttributeID) *ua.DataValue {
	if attr == ua.AttributeIDNodeClass {
		// The library rewrites this stored value in place on every read.
		l.mu.Lock()
		defer l.mu.Unlock()
	} else {
		l.mu.RLock()
		defer l.mu.RUnlock()
	}
	return l.NodeNameSpace.Attribute(id, attr)
}

// setValue replaces a node's value under the lock and tells nobody: for a
// test service handler that answers a Write itself (fence_test.go) and would
// otherwise reach the node around the lock.
func setValue(srv *server.Server, id *ua.NodeID, v *ua.DataValue) {
	ns, err := srv.Namespace(int(id.Namespace()))
	if err != nil {
		return
	}
	l, ok := ns.(*lockedNS)
	if !ok {
		return
	}
	n := l.NodeNameSpace.Node(id)
	if n == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = n.SetAttribute(ua.AttributeIDValue, v)
}

// SetAttribute is NodeNameSpace's, with only the node's own write under the
// lock: the server's change notification reads attributes back through this
// namespace (Attribute takes the read lock), so notifying while holding the
// write lock would deadlock the service goroutine on itself.
func (l *lockedNS) SetAttribute(id *ua.NodeID, attr ua.AttributeID, val *ua.DataValue) ua.StatusCode {
	n := l.NodeNameSpace.Node(id)
	if n == nil {
		return ua.StatusBadNodeIDUnknown
	}
	l.mu.Lock()
	if !n.Access(ua.AccessLevelTypeCurrentWrite) {
		l.mu.Unlock()
		return ua.StatusBadUserAccessDenied
	}
	err := n.SetAttribute(attr, val)
	l.mu.Unlock()
	if err != nil {
		return ua.StatusBadAttributeIDInvalid
	}
	l.srv.ChangeNotification(id)
	select {
	case l.NodeNameSpace.ExternalNotification <- id:
	default:
	}
	return ua.StatusOK
}
