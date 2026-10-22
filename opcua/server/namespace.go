// namespace.go is the address space: one OPC UA namespace whose nodes ARE
// the tag store. Nothing is copied into it — a Browse lists what the store
// holds right now, a Read reads the store, a Write is refused — so there is
// no mirror to keep in step with the scan loop and no lock the scan loop
// could ever contend for beyond the store's own RWMutex.
//
// Node ids are string ids in the store's own address form, so a client
// that already knows nautilus (an HMI binding, a test manifest, a Sparkplug
// metric name) can write the node id down without browsing for it:
//
//	ns=2;s=Level              a tag
//	ns=2;s=P101.Drive.Speed   a member of a UDT tag
//	ns=2;s=Levels[2]          an element of an array of structs
//	ns=2;i=1                  the project folder under Objects
//
// A struct, an FB instance and an array of either are Objects with one
// child per member; a scalar and an array of scalars are Variables. Tag
// names are IEC identifiers, so a node id in another casing resolves to the
// same tag, exactly as the HTTP API's does.
package server

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gopcua/opcua/id"
	opcserver "github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// folderID is the numeric id, within this namespace, of the one folder
// node: the project, under ns=0's Objects.
const folderID = 1

type namespace struct {
	rt    *runtime.Runtime
	uri   string
	title string // the folder's browse/display name: the project
	sweep time.Duration
	id    uint16

	// stamps is the SourceTimestamp bookkeeping: the store records WHICH
	// write generation a tag last changed at, not when, so the namespace
	// notes the wall-clock time it first SAW each new generation — from a
	// client's read, or from the change sweep that feeds subscriptions,
	// whichever comes first. Keyed by root tag: a UDT changes whole.
	mu     sync.Mutex
	stamps map[string]stamp
}

type stamp struct {
	gen uint64
	at  time.Time
}

var _ opcserver.NameSpace = (*namespace)(nil)

func (ns *namespace) Name() string   { return ns.uri }
func (ns *namespace) ID() uint16     { return ns.id }
func (ns *namespace) SetID(i uint16) { ns.id = i }

// AddNode is part of the interface for namespaces that hold their own nodes;
// this one's nodes are the tag store, so there is nothing to add to.
func (ns *namespace) AddNode(n *opcserver.Node) *opcserver.Node { return n }

// Node returns a gopcua Node for the folder, which is the only node of ours
// that ns=0 refers to (so its Browse of Objects can render the folder's
// type definition). Tag nodes have no *Node: they are answered through
// Attribute and Browse directly.
func (ns *namespace) Node(nid *ua.NodeID) *opcserver.Node {
	if ns.isFolder(nid) {
		return ns.folderNode()
	}
	return nil
}

func (ns *namespace) Objects() *opcserver.Node { return ns.folderNode() }
func (ns *namespace) Root() *opcserver.Node    { return ns.folderNode() }

func (ns *namespace) folderNodeID() *ua.NodeID { return ua.NewNumericNodeID(ns.id, folderID) }

func (ns *namespace) isFolder(nid *ua.NodeID) bool {
	return nid != nil && nid.Namespace() == ns.id && nid.Type() != ua.NodeIDTypeString && nid.IntID() == folderID
}

func (ns *namespace) folderNode() *opcserver.Node {
	return opcserver.NewNode(
		ns.folderNodeID(),
		map[ua.AttributeID]*ua.DataValue{
			ua.AttributeIDNodeClass:     opcserver.DataValueFromValue(int32(ua.NodeClassObject)),
			ua.AttributeIDBrowseName:    opcserver.DataValueFromValue(ns.qualified(ns.title)),
			ua.AttributeIDDisplayName:   opcserver.DataValueFromValue(text(ns.title)),
			ua.AttributeIDDescription:   opcserver.DataValueFromValue(text("Nautilus controller " + ns.title + ": its tag store")),
			ua.AttributeIDDataType:      opcserver.DataValueFromValue(ua.NewNumericExpandedNodeID(0, id.FolderType)),
			ua.AttributeIDEventNotifier: opcserver.DataValueFromValue(uint8(0)),
		},
		nil, nil,
	)
}

func (ns *namespace) qualified(name string) *ua.QualifiedName {
	return &ua.QualifiedName{NamespaceIndex: ns.id, Name: name}
}

func text(s string) *ua.LocalizedText {
	return &ua.LocalizedText{EncodingMask: ua.LocalizedTextText, Text: s}
}

// resolve finds the member a node id addresses. ok is false for a node id
// in another namespace, of another id type, or naming no tag or member.
func (ns *namespace) resolve(nid *ua.NodeID) (member, bool) {
	if nid == nil || nid.Namespace() != ns.id || nid.Type() != ua.NodeIDTypeString {
		return member{}, false
	}
	root, segs := ir.SplitAddress(nid.StringID())
	tags := ns.rt.Tags()
	canon, ok := tags.Canonical(root)
	if !ok {
		return member{}, false
	}
	v, err := tags.ReadGlobal(canon)
	if err != nil {
		return member{}, false
	}
	return walk(canon, v, tags.TypeOf(canon), segs)
}

// nodeID is the node id a member is addressed by.
func (ns *namespace) nodeID(m member) *ua.NodeID { return ua.NewStringNodeID(ns.id, m.addr) }

// leafName is what a member is called under its parent: the last path
// segment, with an array element keeping its array's name ("Levels[2]")
// so a flat watch list still reads.
func leafName(addr string) string {
	if i := strings.LastIndex(addr, "."); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

// roots lists the store's top-level tags as members, in name order.
func (ns *namespace) roots() []member {
	tags := ns.rt.Tags()
	snap := tags.Snapshot()
	names := make([]string, 0, len(snap))
	for name := range snap {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]member, 0, len(names))
	for _, name := range names {
		out = append(out, member{addr: name, val: snap[name], typ: tags.TypeOf(name)})
	}
	return out
}

// ── Browse ──────────────────────────────────────────────────────────────

func (ns *namespace) Browse(bd *ua.BrowseDescription) *ua.BrowseResult {
	if bd == nil || bd.NodeID == nil {
		return &ua.BrowseResult{StatusCode: ua.StatusBadNodeIDInvalid}
	}
	var refs []*ua.ReferenceDescription
	switch {
	case ns.isFolder(bd.NodeID):
		refs = append(refs, ns.typeRef(id.FolderType))
		// The folder hangs off ns=0's Objects, and says so backwards.
		refs = append(refs, &ua.ReferenceDescription{
			ReferenceTypeID: ua.NewNumericNodeID(0, id.Organizes),
			IsForward:       false,
			NodeID:          ua.NewNumericExpandedNodeID(0, id.ObjectsFolder),
			BrowseName:      &ua.QualifiedName{Name: "Objects"},
			DisplayName:     text("Objects"),
			NodeClass:       ua.NodeClassObject,
			TypeDefinition:  ua.NewNumericExpandedNodeID(0, id.FolderType),
		})
		for _, m := range ns.roots() {
			refs = append(refs, ns.childRef(m, id.Organizes))
		}
	default:
		m, ok := ns.resolve(bd.NodeID)
		if !ok {
			return &ua.BrowseResult{StatusCode: ua.StatusBadNodeIDUnknown}
		}
		if isObject(m) {
			refs = append(refs, ns.typeRef(id.BaseObjectType))
		} else {
			refs = append(refs, ns.typeRef(id.BaseDataVariableType))
		}
		refs = append(refs, ns.parentRef(m))
		for _, c := range children(m) {
			refs = append(refs, ns.childRef(c, id.HasComponent))
		}
	}
	out := make([]*ua.ReferenceDescription, 0, len(refs))
	for _, r := range refs {
		if wantRef(bd, r) {
			out = append(out, r)
		}
	}
	return &ua.BrowseResult{StatusCode: ua.StatusGood, References: out}
}

// typeRef is a node's HasTypeDefinition reference to an ns=0 type.
func (ns *namespace) typeRef(typeID uint32) *ua.ReferenceDescription {
	name := "BaseDataVariableType"
	switch typeID {
	case id.FolderType:
		name = "FolderType"
	case id.BaseObjectType:
		name = "BaseObjectType"
	}
	return &ua.ReferenceDescription{
		ReferenceTypeID: ua.NewNumericNodeID(0, id.HasTypeDefinition),
		IsForward:       true,
		NodeID:          ua.NewNumericExpandedNodeID(0, typeID),
		BrowseName:      &ua.QualifiedName{Name: name},
		DisplayName:     text(name),
		NodeClass:       nodeClassOfType(typeID),
		TypeDefinition:  ua.NewTwoByteExpandedNodeID(0),
	}
}

func nodeClassOfType(typeID uint32) ua.NodeClass {
	if typeID == id.BaseDataVariableType {
		return ua.NodeClassVariableType
	}
	return ua.NodeClassObjectType
}

// childRef is the forward reference from a parent to one of its members.
func (ns *namespace) childRef(m member, refType uint32) *ua.ReferenceDescription {
	class, typeDef := ua.NodeClassVariable, uint32(id.BaseDataVariableType)
	if isObject(m) {
		class, typeDef = ua.NodeClassObject, id.BaseObjectType
	}
	name := leafName(m.addr)
	return &ua.ReferenceDescription{
		ReferenceTypeID: ua.NewNumericNodeID(0, refType),
		IsForward:       true,
		NodeID:          ua.NewExpandedNodeID(ns.nodeID(m), "", 0),
		BrowseName:      ns.qualified(name),
		DisplayName:     text(name),
		NodeClass:       class,
		TypeDefinition:  ua.NewNumericExpandedNodeID(0, typeDef),
	}
}

// parentRef is a member's inverse reference to whatever holds it: the
// folder for a tag, the enclosing struct/array/FB for a member.
func (ns *namespace) parentRef(m member) *ua.ReferenceDescription {
	root, segs := ir.SplitAddress(m.addr)
	if len(segs) == 0 {
		return &ua.ReferenceDescription{
			ReferenceTypeID: ua.NewNumericNodeID(0, id.Organizes),
			IsForward:       false,
			NodeID:          ua.NewExpandedNodeID(ns.folderNodeID(), "", 0),
			BrowseName:      ns.qualified(ns.title),
			DisplayName:     text(ns.title),
			NodeClass:       ua.NodeClassObject,
			TypeDefinition:  ua.NewNumericExpandedNodeID(0, id.FolderType),
		}
	}
	parent := ir.JoinPath(root, segs[:len(segs)-1])
	name := leafName(parent)
	return &ua.ReferenceDescription{
		ReferenceTypeID: ua.NewNumericNodeID(0, id.HasComponent),
		IsForward:       false,
		NodeID:          ua.NewExpandedNodeID(ua.NewStringNodeID(ns.id, parent), "", 0),
		BrowseName:      ns.qualified(name),
		DisplayName:     text(name),
		NodeClass:       ua.NodeClassObject,
		TypeDefinition:  ua.NewNumericExpandedNodeID(0, id.BaseObjectType),
	}
}

// wantRef applies the BrowseDescription's filters — direction, reference
// type (with the subtype relations among the few reference types this
// namespace emits), node class — to one reference.
func wantRef(bd *ua.BrowseDescription, r *ua.ReferenceDescription) bool {
	switch bd.BrowseDirection {
	case ua.BrowseDirectionForward:
		if !r.IsForward {
			return false
		}
	case ua.BrowseDirectionInverse:
		if r.IsForward {
			return false
		}
	}
	if bd.NodeClassMask != 0 && bd.NodeClassMask&uint32(r.NodeClass) == 0 {
		return false
	}
	want := bd.ReferenceTypeID
	if want == nil || (want.Namespace() == 0 && want.IntID() == 0) {
		return true
	}
	if want.Namespace() != 0 {
		return false
	}
	have := r.ReferenceTypeID.IntID()
	if want.IntID() == have {
		return true
	}
	if !bd.IncludeSubtypes {
		return false
	}
	switch want.IntID() {
	case id.References:
		return true
	case id.HierarchicalReferences:
		return have == id.HasComponent || have == id.Organizes
	case id.HasChild, id.Aggregates:
		return have == id.HasComponent
	case id.NonHierarchicalReferences:
		return have == id.HasTypeDefinition
	}
	return false
}

// ── Attributes ──────────────────────────────────────────────────────────

func (ns *namespace) Attribute(nid *ua.NodeID, attr ua.AttributeID) *ua.DataValue {
	if ns.isFolder(nid) {
		return ns.folderAttribute(attr)
	}
	m, ok := ns.resolve(nid)
	if !ok {
		return status(ua.StatusBadNodeIDUnknown)
	}
	object := isObject(m)
	switch attr {
	case ua.AttributeIDNodeID:
		return value(nid)
	case ua.AttributeIDNodeClass:
		if object {
			return value(int32(ua.NodeClassObject))
		}
		return value(int32(ua.NodeClassVariable))
	case ua.AttributeIDBrowseName:
		return value(ns.qualified(leafName(m.addr)))
	case ua.AttributeIDDisplayName:
		return value(text(leafName(m.addr)))
	case ua.AttributeIDDescription:
		return value(text(ns.describe(m)))
	case ua.AttributeIDWriteMask, ua.AttributeIDUserWriteMask:
		return value(uint32(0))
	case ua.AttributeIDRolePermissions, ua.AttributeIDUserRolePermissions, ua.AttributeIDAccessRestrictions:
		return status(ua.StatusBadAttributeIDInvalid)
	}
	if object {
		if attr == ua.AttributeIDEventNotifier {
			return value(uint8(0))
		}
		return status(ua.StatusBadAttributeIDInvalid)
	}
	switch attr {
	case ua.AttributeIDValue:
		return ns.readValue(m)
	case ua.AttributeIDDataType:
		return value(ua.NewNumericNodeID(0, dataType(m)))
	case ua.AttributeIDValueRank:
		if m.val.Kind == ir.TypeArray {
			return value(int32(1))
		}
		return value(int32(-1))
	case ua.AttributeIDArrayDimensions:
		if m.val.Kind == ir.TypeArray {
			return value([]uint32{uint32(len(m.val.Arr))})
		}
		return value([]uint32{})
	case ua.AttributeIDAccessLevel, ua.AttributeIDUserAccessLevel:
		return value(uint8(ua.AccessLevelTypeCurrentRead))
	case ua.AttributeIDAccessLevelEx:
		return value(uint32(ua.AccessLevelExTypeCurrentRead))
	case ua.AttributeIDMinimumSamplingInterval:
		return value(float64(ns.sweep / time.Millisecond))
	case ua.AttributeIDHistorizing:
		return value(false)
	}
	return status(ua.StatusBadAttributeIDInvalid)
}

func (ns *namespace) folderAttribute(attr ua.AttributeID) *ua.DataValue {
	switch attr {
	case ua.AttributeIDNodeID:
		return value(ns.folderNodeID())
	case ua.AttributeIDNodeClass:
		return value(int32(ua.NodeClassObject))
	case ua.AttributeIDBrowseName:
		return value(ns.qualified(ns.title))
	case ua.AttributeIDDisplayName:
		return value(text(ns.title))
	case ua.AttributeIDDescription:
		return value(text("Nautilus controller " + ns.title + ": its tag store"))
	case ua.AttributeIDWriteMask, ua.AttributeIDUserWriteMask:
		return value(uint32(0))
	case ua.AttributeIDEventNotifier:
		return value(uint8(0))
	}
	return status(ua.StatusBadAttributeIDInvalid)
}

// describe is a member's Description: the tag model's desc, with its unit
// in brackets — what the HMI shows beside the value, served as data.
func (ns *namespace) describe(m member) string {
	meta := ns.rt.Meta()[m.addr]
	switch {
	case meta.Desc != "" && meta.Unit != "":
		return meta.Desc + " [" + meta.Unit + "]"
	case meta.Unit != "":
		return "[" + meta.Unit + "]"
	}
	return meta.Desc
}

// readValue is the Value attribute: the member's Variant, the root tag's
// quality as the status, and the time its current generation was first
// seen as the SourceTimestamp.
func (ns *namespace) readValue(m member) *ua.DataValue {
	v, err := variant(m)
	if err != nil {
		return status(ua.StatusBadAttributeIDInvalid)
	}
	root, _ := ir.SplitAddress(m.addr)
	now := time.Now()
	dv := &ua.DataValue{
		EncodingMask:    ua.DataValueValue | ua.DataValueStatusCode | ua.DataValueSourceTimestamp | ua.DataValueServerTimestamp,
		Value:           v,
		Status:          statusOf(ns.rt.TagQuality(root)),
		ServerTimestamp: now,
		SourceTimestamp: now,
	}
	if gen, ok := ns.rt.Tags().TagGeneration(root); ok {
		dv.SourceTimestamp = ns.stampAt(root, gen, now)
	}
	return dv
}

// stampAt returns the time generation gen of root was first seen, recording
// now as that time if this is the first sighting.
func (ns *namespace) stampAt(root string, gen uint64, now time.Time) time.Time {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	if s, ok := ns.stamps[root]; ok && s.gen == gen {
		return s.at
	}
	ns.stamps[root] = stamp{gen: gen, at: now}
	return now
}

// SetAttribute is the Write service's landing point. This namespace is
// read-only: a write to a Value is refused as an access-denied — an
// anonymous session has no business writing a controller, and until
// writes ride a secure channel from a trusted client (a follow-up) every
// session is anonymous — and a write to any other attribute as not
// writable. Either way the client is told; nothing is silently dropped.
func (ns *namespace) SetAttribute(nid *ua.NodeID, attr ua.AttributeID, _ *ua.DataValue) ua.StatusCode {
	if ns.isFolder(nid) {
		return ua.StatusBadNotWritable
	}
	m, ok := ns.resolve(nid)
	if !ok {
		return ua.StatusBadNodeIDUnknown
	}
	if attr == ua.AttributeIDValue && !isObject(m) {
		return ua.StatusBadUserAccessDenied
	}
	return ua.StatusBadNotWritable
}

// value wraps a Good attribute value.
func value(v any) *ua.DataValue {
	return &ua.DataValue{
		EncodingMask:    ua.DataValueValue | ua.DataValueServerTimestamp,
		Value:           ua.MustVariant(v),
		ServerTimestamp: time.Now(),
	}
}

// status is an attribute read that failed with code.
func status(code ua.StatusCode) *ua.DataValue {
	return &ua.DataValue{
		EncodingMask:    ua.DataValueStatusCode | ua.DataValueServerTimestamp,
		Status:          code,
		ServerTimestamp: time.Now(),
	}
}
