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
	"github.com/joyautomation/nautilus/lang/ir"
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
	// Wire is "ByteString" or "ByteArray" (Byte[], ValueRank 1) for a STRING
	// tag that holds octets as lowercase hex (opcua/bytes.go): the node is
	// served as that, and a value written to it is stored as hex. At most
	// MaxBytes octets either way. Only a STRING tag takes it.
	Wire string
	// Method makes the node a Method of its Parent Object (the parent is
	// required): a Call of it runs this with the input arguments and
	// answers its output arguments, or a bad status. A Method has no tag,
	// is executable by any session the Call service admits, and is a leaf.
	Method func(args []*ua.Variant) (out []*ua.Variant, status ua.StatusCode)
	// Writes says the method changes the device it belongs to (WriteISDU):
	// a Call of it needs a session a variable write would be allowed from (a
	// SignAndEncrypt channel from a trusted certificate, or, in a lab,
	// opcua.anonymous-writes), else it is Bad_UserAccessDenied. A method
	// that only reads (ReadISDU) is open to every session, as a read is.
	Writes bool
}

// mnode is a MappedNode resolved.
type mnode struct {
	nid      *ua.NodeID
	parent   *mnode // nil: under Objects
	name     string
	desc     string
	tag      string
	wire     string
	method   func(args []*ua.Variant) ([]*ua.Variant, ua.StatusCode)
	writes   bool
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
		if n.Wire != "" && (n.Wire != "ByteString" && n.Wire != "ByteArray" || n.Tag == "") {
			return nil, fmt.Errorf("mapped node %s: wire %q: only a Variable can be a ByteString or a ByteArray", n.NodeID, n.Wire)
		}
		if n.Method != nil && (n.Tag != "" || n.Parent == "" || n.Wire != "") {
			return nil, fmt.Errorf("mapped node %s: a Method has a Parent Object and no tag or wire", n.NodeID)
		}
		if n.Writes && n.Method == nil {
			return nil, fmt.Errorf("mapped node %s: Writes is for a Method", n.NodeID)
		}
		mn := &mnode{nid: nid, name: n.Name, desc: n.Desc, tag: n.Tag, wire: n.Wire, method: n.Method, writes: n.Writes}
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
			if p.method != nil {
				return nil, fmt.Errorf("mapped node %s: parent %s is a Method", n.NodeID, n.Parent)
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
	if n.method != nil {
		switch attr {
		case ua.AttributeIDNodeClass:
			return value(int32(ua.NodeClassMethod)), true
		case ua.AttributeIDWriteMask, ua.AttributeIDUserWriteMask:
			return value(uint32(0)), true
		case ua.AttributeIDExecutable, ua.AttributeIDUserExecutable:
			return value(true), true
		}
		return status(ua.StatusBadAttributeIDInvalid), true
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

// callMapped runs a mapped Method: ok is false when methodID is not one.
// A Method called on an object that is not its parent is Bad_MethodInvalid;
// callOne's hasMethod has refused that already, so the check here is the
// second lock, not what a test can see alone.
func (ns *namespace) callMapped(objectID, methodID *ua.NodeID, args []*ua.Variant, writer func() bool) (*ua.CallMethodResult, bool) {
	n, ok := ns.mapped.node(methodID)
	if !ok || n.method == nil {
		return nil, false
	}
	if n.parent == nil || !n.parent.nid.Equal(objectID) {
		return callResult(ua.StatusBadMethodInvalid), true
	}
	if n.writes && !writer() {
		return callResult(ua.StatusBadUserAccessDenied), true
	}
	// Under an auth: block a method that changes a device has no permission
	// or journal entry yet (it writes no tag an area could scope, and which
	// permission it needs, config or write, is not decided), so it is
	// refused, whoever calls it, rather than run unjournalled. Only virtual
	// devices register one today, with no Identity (#708 PR 8a review).
	if n.writes && ns.w.id != nil {
		return callResult(ua.StatusBadUserAccessDenied), true
	}
	out, st := n.method(args)
	r := callResult(st)
	if out != nil {
		r.OutputArguments = out
	}
	return r, true
}

// mappedVariable resolves a mapped Variable to its tag member; ok is false
// for an Object or a tag the store does not hold.
func (ns *namespace) mappedVariable(n *mnode) (member, bool) {
	if n.tag == "" {
		return member{}, false
	}
	m, ok := ns.resolveAddr(n.tag)
	if ok && n.wire != "" && m.val.Kind == ir.TypeString {
		m.wire = n.wire
	}
	return m, ok
}

// mappedRef is the reference to a mapped node from its parent (or from
// Objects), forward, or back to the parent, inverse.
func (ns *namespace) mappedRef(n *mnode, refType uint32, forward bool) *ua.ReferenceDescription {
	class, typeDef := ua.NodeClassObject, uint32(id.FolderType)
	if n.tag != "" {
		class, typeDef = ua.NodeClassVariable, id.BaseDataVariableType
	}
	if n.method != nil {
		class, typeDef = ua.NodeClassMethod, 0 // a Method has no type definition
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
	switch {
	case n.method != nil: // no type definition
	case n.tag != "":
		refs = append(refs, ns.typeRef(id.BaseDataVariableType))
	default:
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
	if n.tag == "" && n.method == nil {
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
