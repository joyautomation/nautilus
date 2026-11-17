// mapped.go is an address space laid over the tag store: nodes with the
// node ids, browse names and tree a particular device's server shows (an
// IO-Link master's "ns=1;s=IOLM/Port 1/SIO Input", another maker's
// "ns=3;i=8012"), each Variable reading and writing one tag. A virtual
// device is served this way (virtdev): the plant's runtime holds the
// values, and the client sees the real device's address space.
//
// Everything the server does for a tag's own node it does for a mapped
// Variable, because both resolve to the same member: Read, Write and its
// checks, AccessLevel, subscriptions and the change sweep. Only the names
// and the tree come from the map. The namespaces the map uses come first in
// the namespace array, at the indices the device uses, so a client that
// addresses a node by index (as CODESYS does) finds it where the hardware
// keeps it. The project folder is not hung under Objects when a map is
// served: the device's tree is what a client browses.
package server

import (
	"fmt"
	"sort"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	opcserver "github.com/joyautomation/nautilus/opcua/internal/uaserver"
)

// Mapped is an address space laid over the tag store.
type Mapped struct {
	// Namespaces is the namespace array from index 1 on: the URI of each
	// namespace the map's node ids use, in order. The tag namespace
	// follows them.
	Namespaces []string
	// Nodes is the tree. A node's Parent names an earlier node, or is
	// empty for one organized under Objects.
	Nodes []MappedNode
	// Product overrides the BuildInfo and server name the server reports,
	// so it introduces itself as the device it stands in for.
	Product *Product
}

// Product is what a server says it is (BuildInfo, ServerName).
type Product struct {
	Manufacturer string
	Name         string
	URI          string
	Version      string
}

// MappedNode is one node of a Mapped space.
type MappedNode struct {
	// NodeID in the standard string form, its index into Namespaces
	// ("ns=1;s=IOLM/Port 1/Status", "ns=3;i=8012").
	NodeID string
	// Parent is the parent's NodeID; empty means under Objects.
	Parent string
	// Name is the BrowseName (in the node's own namespace) and the
	// DisplayName.
	Name string
	// Desc is the Description.
	Desc string
	// Tag is the tag address a Variable reads and writes; empty makes the
	// node an Object that only organizes others.
	Tag string
}

// mnode is a MappedNode resolved.
type mnode struct {
	nid      *ua.NodeID
	parent   *mnode // nil: under Objects
	name     string
	desc     string
	tag      string
	children []*mnode
}

// mappedTree is the resolved map, keyed by node id.
type mappedTree struct {
	byID map[string]*mnode
	top  []*mnode
}

// newMappedTree checks and resolves a map against its namespace count.
func newMappedTree(m *Mapped) (*mappedTree, error) {
	t := &mappedTree{byID: map[string]*mnode{}}
	for _, n := range m.Nodes {
		nid, err := ua.ParseNodeID(n.NodeID)
		if err != nil {
			return nil, fmt.Errorf("mapped node %q: %w", n.NodeID, err)
		}
		if nid.Namespace() < 1 || int(nid.Namespace()) > len(m.Namespaces) {
			return nil, fmt.Errorf("mapped node %s: namespace %d is not one of the map's 1..%d", n.NodeID, nid.Namespace(), len(m.Namespaces))
		}
		k := nid.String()
		if _, dup := t.byID[k]; dup {
			return nil, fmt.Errorf("mapped node %s: declared twice", n.NodeID)
		}
		if n.Name == "" {
			return nil, fmt.Errorf("mapped node %s: no name", n.NodeID)
		}
		mn := &mnode{nid: nid, name: n.Name, desc: n.Desc, tag: n.Tag}
		if n.Parent != "" {
			pid, err := ua.ParseNodeID(n.Parent)
			if err != nil {
				return nil, fmt.Errorf("mapped node %s: parent %q: %w", n.NodeID, n.Parent, err)
			}
			p, ok := t.byID[pid.String()]
			if !ok {
				return nil, fmt.Errorf("mapped node %s: parent %s is not declared before it", n.NodeID, n.Parent)
			}
			if p.tag != "" {
				return nil, fmt.Errorf("mapped node %s: parent %s is a Variable", n.NodeID, n.Parent)
			}
			mn.parent = p
			p.children = append(p.children, mn)
		} else {
			t.top = append(t.top, mn)
		}
		t.byID[k] = mn
	}
	return t, nil
}

// node finds a mapped node; nil-safe on the tree.
func (t *mappedTree) node(nid *ua.NodeID) (*mnode, bool) {
	if t == nil || nid == nil {
		return nil, false
	}
	n, ok := t.byID[nid.String()]
	return n, ok
}

// owns reports whether nid is one of this namespace's nodes: a tag's, the
// folder, an enumeration's, or a mapped node.
func (ns *namespace) owns(nid *ua.NodeID) bool {
	if nid == nil {
		return false
	}
	if nid.Namespace() == ns.id {
		return true
	}
	_, ok := ns.mapped.node(nid)
	return ok
}

// addrOf is the address a node of this namespace stands for: the tag
// address of a mapped Variable, the string id of a tag's own node.
func (ns *namespace) addrOf(nid *ua.NodeID) string {
	if n, ok := ns.mapped.node(nid); ok {
		return n.tag
	}
	if nid.Type() == ua.NodeIDTypeString {
		return nid.StringID()
	}
	return ""
}

// mappedAttribute answers the attributes a mapped node owns: its id, names
// and description, and every attribute of an Object. handled is false for
// the rest of a Variable's, which are its tag member's.
func (ns *namespace) mappedAttribute(n *mnode, attr ua.AttributeID) (dv *ua.DataValue, handled bool) {
	switch attr {
	case ua.AttributeIDNodeID:
		return value(n.nid), true
	case ua.AttributeIDBrowseName:
		return value(&ua.QualifiedName{NamespaceIndex: n.nid.Namespace(), Name: n.name}), true
	case ua.AttributeIDDisplayName:
		return value(text(n.name)), true
	case ua.AttributeIDDescription:
		if n.desc != "" || n.tag == "" {
			return value(text(n.desc)), true
		}
		return nil, false
	}
	if n.tag != "" {
		return nil, false
	}
	switch attr {
	case ua.AttributeIDNodeClass:
		return value(int32(ua.NodeClassObject)), true
	case ua.AttributeIDWriteMask, ua.AttributeIDUserWriteMask:
		return value(uint32(0)), true
	case ua.AttributeIDEventNotifier:
		return value(uint8(0)), true
	}
	return status(ua.StatusBadAttributeIDInvalid), true
}

// mappedVariable resolves a mapped Variable to its tag member; ok is false
// for an Object or a tag the store does not hold.
func (ns *namespace) mappedVariable(n *mnode) (member, bool) {
	if n.tag == "" {
		return member{}, false
	}
	return ns.resolveAddr(n.tag)
}

// mappedRef is the reference to a mapped node from its parent (or from
// Objects), forward, or back to the parent, inverse.
func (ns *namespace) mappedRef(n *mnode, refType uint32, forward bool) *ua.ReferenceDescription {
	class, typeDef := ua.NodeClassObject, uint32(id.FolderType)
	if n.tag != "" {
		class, typeDef = ua.NodeClassVariable, id.BaseDataVariableType
	}
	return &ua.ReferenceDescription{
		ReferenceTypeID: ua.NewNumericNodeID(0, refType),
		IsForward:       forward,
		NodeID:          ua.NewExpandedNodeID(n.nid, "", 0),
		BrowseName:      &ua.QualifiedName{NamespaceIndex: n.nid.Namespace(), Name: n.name},
		DisplayName:     text(n.name),
		NodeClass:       class,
		TypeDefinition:  ua.NewNumericExpandedNodeID(0, typeDef),
	}
}

// mappedBrowse lists a mapped node's references: its type, its parent
// (inverse) and its children.
func (ns *namespace) mappedBrowse(n *mnode, bd *ua.BrowseDescription) *ua.BrowseResult {
	var refs []*ua.ReferenceDescription
	if n.tag != "" {
		refs = append(refs, ns.typeRef(id.BaseDataVariableType))
	} else {
		refs = append(refs, ns.typeRef(id.FolderType))
	}
	if n.parent == nil {
		refs = append(refs, &ua.ReferenceDescription{
			ReferenceTypeID: ua.NewNumericNodeID(0, id.Organizes),
			IsForward:       false,
			NodeID:          ua.NewNumericExpandedNodeID(0, id.ObjectsFolder),
			BrowseName:      &ua.QualifiedName{Name: "Objects"},
			DisplayName:     text("Objects"),
			NodeClass:       ua.NodeClassObject,
			TypeDefinition:  ua.NewNumericExpandedNodeID(0, id.FolderType),
		})
	} else {
		refs = append(refs, ns.mappedRef(n.parent, childRefType(n), false))
	}
	for _, c := range n.children {
		refs = append(refs, ns.mappedRef(c, childRefType(c), true))
	}
	out := make([]*ua.ReferenceDescription, 0, len(refs))
	for _, r := range refs {
		if wantRef(bd, r) {
			out = append(out, r)
		}
	}
	return &ua.BrowseResult{StatusCode: ua.StatusGood, References: out}
}

// childRefType is how a parent refers to a mapped child: Organizes for an
// Object (a folder of things), HasComponent for a Variable.
func childRefType(n *mnode) uint32 {
	if n.tag == "" {
		return id.Organizes
	}
	return id.HasComponent
}

// MappedTag is a mapped Variable's node id and tag, for a caller listing
// what the map serves (the endpoint table virtdev prints).
type MappedTag struct {
	NodeID string
	Tag    string
}

// MappedTags lists every mapped Variable's node id and tag, sorted by node
// id.
func (s *Server) MappedTags() []MappedTag {
	t, err := s.mappedTree()
	if err != nil || t == nil {
		return nil
	}
	var out []MappedTag
	for k, n := range t.byID {
		if n.tag != "" {
			out = append(out, MappedTag{NodeID: k, Tag: n.tag})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func (s *Server) mappedTree() (*mappedTree, error) {
	if s.opts.Mapped == nil {
		return nil, nil
	}
	return newMappedTree(s.opts.Mapped)
}

// mappedSpace is one of the map's namespaces as gopcua sees it: the index
// the Read, Browse and Monitor paths route a node id to. Every question is
// answered by the tag namespace, which owns the map.
type mappedSpace struct {
	uri string
	id  uint16
	ns  *namespace
}

var _ opcserver.NameSpace = (*mappedSpace)(nil)

func (m *mappedSpace) Name() string                              { return m.uri }
func (m *mappedSpace) ID() uint16                                { return m.id }
func (m *mappedSpace) SetID(i uint16)                            { m.id = i }
func (m *mappedSpace) AddNode(n *opcserver.Node) *opcserver.Node { return n }
func (m *mappedSpace) Objects() *opcserver.Node                  { return nil }
func (m *mappedSpace) Root() *opcserver.Node                     { return nil }
func (m *mappedSpace) Browse(bd *ua.BrowseDescription) *ua.BrowseResult {
	if bd == nil || bd.NodeID == nil {
		return &ua.BrowseResult{StatusCode: ua.StatusBadNodeIDInvalid}
	}
	n, ok := m.ns.mapped.node(bd.NodeID)
	if !ok {
		return &ua.BrowseResult{StatusCode: ua.StatusBadNodeIDUnknown}
	}
	return m.ns.mappedBrowse(n, bd)
}

func (m *mappedSpace) Attribute(nid *ua.NodeID, attr ua.AttributeID) *ua.DataValue {
	return m.ns.Attribute(nid, attr)
}

func (m *mappedSpace) SetAttribute(*ua.NodeID, ua.AttributeID, *ua.DataValue) ua.StatusCode {
	// Writes come through the Write service (write.go), which knows the
	// session; nothing writes an attribute behind its back.
	return ua.StatusBadNotWritable
}

// Node renders a mapped node for the callers that want a *Node: the Objects
// folder's reference to a top node, and the existence checks the monitor
// path makes.
func (m *mappedSpace) Node(nid *ua.NodeID) *opcserver.Node {
	n, ok := m.ns.mapped.node(nid)
	if !ok {
		return nil
	}
	class := ua.NodeClassObject
	typeDef := uint32(id.FolderType)
	if n.tag != "" {
		class, typeDef = ua.NodeClassVariable, id.BaseDataVariableType
	}
	return opcserver.NewNode(n.nid,
		map[ua.AttributeID]*ua.DataValue{
			ua.AttributeIDNodeClass:   opcserver.DataValueFromValue(int32(class)),
			ua.AttributeIDBrowseName:  opcserver.DataValueFromValue(&ua.QualifiedName{NamespaceIndex: n.nid.Namespace(), Name: n.name}),
			ua.AttributeIDDisplayName: opcserver.DataValueFromValue(text(n.name)),
		},
		opcserver.References{ns0TypeRef(typeDef)}, nil)
}

// ns0TypeRef is a HasTypeDefinition reference to an ns=0 type, for a
// rendered node.
func ns0TypeRef(typeID uint32) *ua.ReferenceDescription {
	return &ua.ReferenceDescription{
		ReferenceTypeID: ua.NewNumericNodeID(0, id.HasTypeDefinition),
		IsForward:       true,
		NodeID:          ua.NewNumericExpandedNodeID(0, typeID),
		NodeClass:       nodeClassOfType(typeID),
		TypeDefinition:  ua.NewTwoByteExpandedNodeID(0),
	}
}
