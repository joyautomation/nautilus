//go:build !js

package wirebind

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/opcua"
	"github.com/joyautomation/nautilus/wiring"
)

// The keys an OPC UA source and an OPC UA member take. A source's endpoint
// makes the connection the instance's own; the session's security,
// credentials and scan rate are the drivers: section the source names (its
// template). A member's keys are the opcua manifest's columns.
var (
	opcuaSourceKeys = []string{"endpoint"}
	opcuaMemberKeys = []string{"node", "namespace", "type", "scan-class"}
)

// OPCUAConn is one OPC UA connection an instance uses, and the bindings its
// members add to it: a read binding per from: member and a writable,
// never-polled binding per to: member, each named by the member's key
// ("P101.Speed", "P101.Cmd.Start"). A to: member's read-back is its own
// from: member, so the command's node is never read.
type OPCUAConn struct {
	Instance string
	Conn     Conn
	// Endpoint is the instance's own server (a per-instance connection);
	// empty for a Shared one, which binds into its driver's session.
	Endpoint string
	Tags     []opcua.TagBinding
	// Warnings are what naut check says about the connection: a from:
	// node addressed by namespace index alone.
	Warnings []string
}

// opcuaWireTypes is the OPC UA built-in type an IEC member type is read and
// written as, when the member gives no type: of its own.
var opcuaWireTypes = map[string]string{
	"BOOL": "Boolean", "SINT": "SByte", "USINT": "Byte", "BYTE": "Byte",
	"INT": "Int16", "UINT": "UInt16", "WORD": "UInt16",
	"DINT": "Int32", "UDINT": "UInt32", "DWORD": "UInt32",
	"LINT": "Int64", "ULINT": "UInt64", "LWORD": "UInt64",
	"REAL": "Float", "LREAL": "Double", "STRING": "String",
}

// OPCUAConns are the set's OPC UA connections, in instance order, with
// every problem at once.
func (s *Set) OPCUAConns() ([]OPCUAConn, []wiring.Problem) {
	var out []OPCUAConn
	var probs []wiring.Problem
	add := func(pos wiring.Pos, format string, a ...any) {
		probs = append(probs, wiring.Problem{Pos: pos, Msg: fmt.Sprintf(format, a...)})
	}
	if s == nil {
		return nil, nil
	}
	for _, in := range s.Instances {
		for _, c := range in.Conns {
			if c.DriverType != "opcua-client" {
				continue
			}
			oc := OPCUAConn{Instance: in.Name, Conn: c}
			for _, k := range sortedKeys(c.Keys) {
				if !contains(opcuaSourceKeys, k) {
					add(c.Pos, "instance %s: source %s: %q is not an OPC UA source key: a source gives only endpoint: (its own server); security, security-mode, pki-dir, username, read-mode and scan-rate belong to the drivers: section it names", in.Name, c.Source, k)
				}
			}
			if !c.Shared {
				ep := fmt.Sprint(c.Keys["endpoint"])
				if c.Keys["endpoint"] == nil || ep == "" {
					add(c.Pos, "instance %s: source %s: endpoint is empty: give the server (opc.tcp://host:4840), usually a param, or leave endpoint: out to share the session of the drivers: entry the source names", in.Name, c.Source)
				}
				oc.Endpoint = ep
			}
			var commands []opcua.TagBinding // the commands so far, for one writer per node
			for _, mem := range in.Members {
				if mem.Source != c.Source || (mem.Kind != wiring.From && mem.Kind != wiring.To) {
					continue
				}
				b, warn, err := opcuaBinding(in.Key(mem), mem)
				if err != nil {
					add(mem.Pos, "instance %s: member %s: %v", in.Name, mem.Path, err)
					continue
				}
				if warn != "" {
					oc.Warnings = append(oc.Warnings, fmt.Sprintf("instance %s: member %s %s", in.Name, mem.Path, warn))
				}
				if b.Writable {
					clash := ""
					for _, o := range commands {
						if SameNode(o, b) {
							clash = strings.TrimPrefix(o.Name, in.Name+".")
						}
					}
					if clash != "" {
						add(mem.Pos, "instance %s: commands %s and %s both write node %s: two commands on one node fight, and the server takes whichever came last", in.Name, clash, mem.Path, b.NodeID)
						continue
					}
					commands = append(commands, b)
				}
				oc.Tags = append(oc.Tags, b)
			}
			out = append(out, oc)
		}
	}
	return out, probs
}

// opcuaBinding is one member's binding, and a warning for naut check.
func opcuaBinding(key string, mem Member) (opcua.TagBinding, string, error) {
	b := opcua.TagBinding{Name: key, Desc: mem.Desc}
	if mem.Type == nil || mem.Type.Kind == ir.TypeStruct || mem.Type.Kind == ir.TypeArray {
		return b, "", fmt.Errorf("is a %s: an OPC UA binding here is one value, so wire its members one by one", mem.Type)
	}
	for _, k := range sortedKeys(mem.Addr) {
		if !contains(opcuaMemberKeys, k) {
			return b, "", fmt.Errorf("%q is not an OPC UA address key (a member takes %s)", k, strings.Join(opcuaMemberKeys, ", "))
		}
	}
	node, _ := mem.Addr["node"].(string)
	if node == "" {
		return b, "", fmt.Errorf("needs node: (the node id, ns=2;s=Pump1.Speed)")
	}
	b.NodeID = node
	if v, ok := mem.Addr["namespace"]; ok {
		if v == nil || fmt.Sprint(v) == "" {
			return b, "", fmt.Errorf("namespace: is empty: give the namespace's URI (as naut opcua browse shows it), or leave the key out")
		}
		b.Namespace = fmt.Sprint(v)
	}
	iec := typeName(mem.Type)
	b.Type = opcuaWireTypes[iec]
	if v, ok := mem.Addr["type"]; ok {
		b.Type = fmt.Sprint(v)
	}
	if b.Type == "" {
		return b, "", fmt.Errorf("is %s, which has no OPC UA type of its own: give type: (the node's built-in type, Int32, Double, ...)", mem.Type)
	}
	if err := opcuaFits(mem.Type, b.Type); err != nil {
		return b, "", err
	}
	if v, ok := mem.Addr["scan-class"]; ok {
		b.ScanClass = fmt.Sprint(v)
	}
	if err := (opcua.Manifest{Tags: []opcua.TagBinding{b}}).Validate(); err != nil {
		return b, "", fmt.Errorf("%v", strings.TrimPrefix(err.Error(), "tag "+key+": "))
	}
	ns := nodeNamespace(node)
	var warn string
	if mem.Kind == wiring.To {
		if b.ScanClass != "" {
			return b, "", fmt.Errorf("is a command (to:), and its node is never read (its read-back is the from: member that shows it), so scan-class: does not apply")
		}
		if ns > 0 && b.Namespace == "" {
			// A server that renumbers its namespaces would send the command
			// to whatever node now holds that index and id.
			return b, "", fmt.Errorf("is a command (to:) addressed by namespace index %d alone: a server that renumbers its namespaces would take it at another node; add namespace: <uri> (the namespace's URI, as naut opcua browse shows it)", ns)
		}
		b.Writable, b.ScanClass, b.Event = true, opcua.NoPoll, mem.Event
	} else if ns > 0 && b.Namespace == "" {
		warn = fmt.Sprintf("is read by namespace index %d alone: a server that renumbers its namespaces would be misread; add namespace: <uri>", ns)
	}
	return b, warn, nil
}

// typeName is an IEC type's name as opcuaWireTypes keys it.
func typeName(t *ir.Type) string {
	if t.Enum != nil {
		return ""
	}
	if t.Kind == ir.TypeBool {
		return "BOOL"
	}
	return strings.ToUpper(t.Name)
}

// opcuaFits refuses a member whose type cannot hold what its node holds:
// a BOOL that is no Boolean, an integer read from a REAL, a STRING that is
// no String. A REAL holds any number.
func opcuaFits(t *ir.Type, wire string) error {
	isBool := t.Kind == ir.TypeBool
	numeric := map[string]bool{"SByte": true, "Byte": true, "Int16": true, "UInt16": true, "Int32": true, "UInt32": true, "Int64": true, "UInt64": true, "Float": true, "Double": true}
	floating := wire == "Float" || wire == "Double"
	switch {
	case isBool != (wire == "Boolean"):
		return fmt.Errorf("is %s, and its node is %s: make the member BOOL for a Boolean node, or give type: the node's own type", t, wire)
	case isBool:
		return nil
	case t.Kind == ir.TypeString || wire == "String":
		if t.Kind != ir.TypeString || wire != "String" {
			return fmt.Errorf("is %s, and its node is %s: a STRING member reads a String node", t, wire)
		}
		return nil
	case !numeric[wire]:
		return fmt.Errorf("is %s, and type %s is not a number OPC UA delivers to it", t, wire)
	case t.Kind == ir.TypeInt && floating:
		return fmt.Errorf("is %s, and its node is %s: a reading would not fit; make the member REAL or LREAL", t, wire)
	case t.Kind != ir.TypeInt && t.Kind != ir.TypeReal:
		return fmt.Errorf("is %s, which an OPC UA number does not hold", t)
	}
	return nil
}

// nodeNamespace is a node id's namespace index ("ns=3;s=x" is 3), 0 when
// it names none.
func nodeNamespace(node string) int {
	rest, ok := strings.CutPrefix(node, "ns=")
	if !ok {
		return 0
	}
	n, _ := strconv.Atoi(rest[:strings.IndexByte(rest+";", ';')])
	return n
}

// SameNode says whether two bindings may address one node of a server.
// The driver re-targets a binding's namespace index from its URI when it
// connects, so a node is its URI and its identifier, whatever index its id
// was written with. A binding with no URI keeps its index, which may now
// be any namespace's: it is taken as the same node as any binding with its
// identifier, so a doubtful pair is refused rather than written twice.
func SameNode(a, b opcua.TagBinding) bool {
	ida, ia := nodeIdent(a.NodeID)
	idb, ib := nodeIdent(b.NodeID)
	if ida != idb {
		return false
	}
	switch {
	case a.Namespace != "" && b.Namespace != "":
		return a.Namespace == b.Namespace
	case a.Namespace == "" && b.Namespace == "" && (ia == 0) != (ib == 0):
		return false // the standard namespace is never renumbered
	}
	return true
}

// nodeIdent is a node id without its namespace index ("ns=3;s=x" is
// "s=x", 3).
func nodeIdent(node string) (string, int) {
	rest, ok := strings.CutPrefix(node, "ns=")
	if !ok {
		return node, 0
	}
	i := strings.IndexByte(rest, ';')
	if i < 0 {
		return node, 0
	}
	n, _ := strconv.Atoi(rest[:i])
	return rest[i+1:], n
}

// MergeOPCUA adds a shared connection's bindings to the manifest of the
// driver it names. A binding name both declare, or a node a command of each
// may write (SameNode), is an error naming it.
func MergeOPCUA(own opcua.Manifest, add []opcua.TagBinding) (opcua.Manifest, error) {
	names := map[string]bool{}
	var writes []opcua.TagBinding
	for _, t := range own.Tags {
		names[ir.NameKey(t.Name)] = true
		if t.Writable {
			writes = append(writes, t)
		}
	}
	for _, t := range add {
		if names[ir.NameKey(t.Name)] {
			return own, fmt.Errorf("the OPC UA driver already binds %s (its manifest, or another instance): rename one", t.Name)
		}
		names[ir.NameKey(t.Name)] = true
		if !t.Writable {
			continue
		}
		for _, w := range writes {
			if SameNode(w, t) {
				return own, fmt.Errorf("%s and %s both write node %s on one server: two commands on one node fight, and the server takes whichever came last", w.Name, t.Name, t.NodeID)
			}
		}
		writes = append(writes, t)
	}
	out := own
	out.Tags = append(append([]opcua.TagBinding(nil), own.Tags...), add...)
	return out, nil
}
