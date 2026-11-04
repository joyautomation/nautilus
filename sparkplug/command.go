package sparkplug

import (
	"errors"
	"fmt"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// handleCommand processes NCMD/DCMD. A "Node Control/Rebirth" = true triggers
// a rebirth (NBIRTH+DBIRTH, bdSeq unchanged, seq reset). Other command metrics
// are writes into the tag store (a SCADA host writing a setpoint), which the
// program then acts on and a driver may propagate to the field — but only
// through the write gate (writegate.go): the tag must be opted in with
// WithWritable, and it must belong to the node or device the command was
// addressed to. Everything else is refused and logged.
//
// A Template metric is a UDT (struct) tag, and Sparkplug allows a PARTIAL
// template update — a template carrying only the members that changed — in
// NCMD/DCMD just as it does in NDATA. So a template command MERGES into the
// tag's current value member by member rather than replacing it: a host
// commanding Motor1.START must not zero the Speed the edge is holding.
// Scalars are unchanged: they replace, as they always did. The gate applies
// per member: a template carrying a writable member and a read-only one
// writes the first and refuses the second.
func (n *Node) handleCommand(_ mqtt.Client, msg mqtt.Message) {
	payload, err := DecodePayload(msg.Payload())
	if err != nil {
		n.log.Warn("sparkplug: bad command payload", "topic", msg.Topic(), "error", err)
		return
	}
	n.applyCommand(commandDevice(msg.Topic()), payload)
}

// applyCommand is handleCommand minus the decode — the seam command_test.go
// drives, so the write semantics are testable without an MQTT message.
// device is the DCMD's device id, "" for an NCMD.
func (n *Node) applyCommand(device string, payload Payload) {
	for _, m := range payload.Metrics {
		if m.Name == "Node Control/Rebirth" {
			if b, ok := m.Value.(bool); ok && b {
				n.requestRebirth()
				return
			}
		}
	}
	// Remaining metrics are writes into the tag store. A metric named after
	// a TEMPLATE MEMBER ("P101/Speed" flattened to "P101.Speed" by the host)
	// is written to that member of the UDT tag; a name that resolves to
	// nothing is refused rather than stored, so a host's typo cannot create
	// a junk top-level tag that the next NBIRTH would publish back at it.
	for _, m := range payload.Metrics {
		if m.Name == "" || m.IsNull || m.Name == "Node Control/Rebirth" {
			continue
		}
		if n.flatten {
			// Flattened, a member is published as "P101/Speed" and a host
			// writes it back under that name: the store's path for it is
			// "P101.Speed" (see flatten.go). Translated first, so the device
			// check and the write gate see the tag and member path.
			m.Name = memberPath(m.Name)
		}
		if owner, _ := n.ownerOf(rootTag(m.Name)); owner != device {
			n.refuseCommand("addressed to the wrong device", m.Name,
				"addressedTo", deviceLabel(device), "publishedOn", deviceLabel(owner))
			continue
		}
		// An input is the field's value: its driver replaces it before every
		// scan, so a command would be discarded unread. Refused, as HTTP and
		// OPC UA refuse it, whatever sparkplug.writable opens. A device
		// carries only its connection's inputs, so this refuses every DCMD
		// until a write reaches the device through its driver.
		if n.isInput(rootTag(m.Name)) {
			n.refuseCommand(inputWriteRefusal, m.Name)
			continue
		}
		if isArrayType(m.Datatype) {
			n.refuseCommand(arrayWriteRefusal, m.Name, "datatype", m.Datatype.String())
			continue
		}
		// An indexed name ("Levels[1]", "Drives[0].Speed") is an element
		// write. The node never births one — a scalar array is one array
		// metric, an array of structs is not published — so it is refused
		// like a whole-array command, whatever sparkplug.writable opens.
		if strings.ContainsRune(m.Name, '[') {
			n.refuseCommand(arrayWriteRefusal, m.Name)
			continue
		}
		if m.Datatype == spb.DataType_Template {
			pruned, arrays := WithoutArrays(m.Name, m)
			for _, r := range arrays {
				n.refuseCommand(arrayWriteRefusal, r)
			}
			pruned, refused := n.writableTemplate(m.Name, pruned)
			for _, r := range refused {
				n.refuseCommand("not writable", r)
			}
			if t, _ := pruned.Value.(*Template); t != nil && len(t.Metrics) > 0 {
				n.commandTemplate(pruned)
			}
			continue
		}
		if !n.writeAllowed(m.Name) {
			n.refuseCommand("not writable", m.Name)
			continue
		}
		if v, ok := commandValue(m); ok {
			// The runtime's one write front door, as HTTP and OPC UA use:
			// a value the tag's declared type cannot hold is refused, never
			// stored as a new type, and a forced tag keeps its force.
			if err := n.writeCommand(m.Name, v); err != nil {
				n.refuseCommand(writeRefusal(err), m.Name, "datatype", m.Datatype.String(), "error", err)
				continue
			}
			n.log.Debug("sparkplug: command write", "tag", m.Name, "value", m.Value)
		}
	}
}

// writeCommand writes one scalar command through the runtime's write front
// door. A declared output the store does not hold yet (no scan has written
// it) is created under its declared name, on a zero of its declared type by
// the same strict rule, as an HTTP write is, so it cannot be born retyped.
// Any other name the store does not hold is refused: the node never births
// such a tag, and a global only a program declares, created under the
// host's spelling, was a tag the write gate had judged by that spelling
// ("gain" past "g*" for the program's Gain, #619).
func (n *Node) writeCommand(name string, v any) error {
	tags := n.rt.Tags()
	if s, ok := v.(string); ok && !n.takesString(name) {
		// As OPC UA refuses a String Variant for a non-String node: the node
		// births a STRING tag as a String metric and nothing else as one, so
		// a String command for anything else is the wrong type, never text
		// to parse (a TIME literal, a number).
		return fmt.Errorf("tag %s: a String metric (%q) for a tag that is not a STRING: %w", name, s, ir.ErrTypeMismatch)
	}
	err := tags.Write([]runtime.WriteOp{{Path: name, Value: v}})[0]
	if !errors.Is(err, runtime.ErrUndefined) || strings.ContainsRune(name, '.') {
		return err
	}
	decl, isOut := tags.DeclaredOutput(name)
	if !isOut {
		return err
	}
	t := tags.TypeOf(decl)
	if t == nil {
		return err
	}
	nv, serr := ir.SetFieldTypedStrict(ir.Zero(t), t, nil, v, "tag "+decl)
	if serr != nil {
		return serr
	}
	tags.Set(decl, nv)
	return nil
}

// takesString reports whether name is a STRING tag or member, or an
// enumeration (which takes a member's name). It is judged by the value the
// store holds, else by the declared type of a whole tag not yet stored.
func (n *Node) takesString(name string) bool {
	tags := n.rt.Tags()
	if v, ok := tags.ReadPath(name); ok {
		_, isStr := v.(string) // a STRING, or an enumeration read as its member name
		return isStr
	}
	if t := tags.TypeOf(name); t != nil {
		return t.Kind == ir.TypeString || t.Enum != nil
	}
	return false
}

// inputWriteRefusal is the reason a command to an input is refused.
const inputWriteRefusal = "an input: its driver replaces it before every scan (force it to hold a value, or write the setpoint the logic reads)"

// isInput reports whether tag is one of the runtime's inputs, in any case.
// It folds with ir.NameKey, as the store does: under strings.EqualFold the
// dotless ı of "Lımit" missed the input Limit that the write then reached
// (#619).
func (n *Node) isInput(tag string) bool {
	k := ir.NameKey(tag)
	for _, in := range n.rt.Inputs() {
		if ir.NameKey(in) == k {
			return true
		}
	}
	return false
}

// writeRefusal is the logged reason for a command the runtime's write
// front door refused.
func writeRefusal(err error) string {
	switch {
	case errors.Is(err, runtime.ErrForced):
		return "the tag is forced"
	case errors.Is(err, runtime.ErrUndefined):
		return "no such tag"
	case errors.Is(err, ir.ErrTypeMismatch):
		return "the value does not fit the tag's type"
	case errors.Is(err, ir.ErrOutOfRange):
		return "the value is out of the tag's range"
	}
	return "the write failed"
}

// arrayWriteRefusal is the reason an array write is refused. Sparkplug can
// carry one (an Int64Array in an NCMD), but the edge node does not apply
// array writes yet, so a command carrying one is refused — counted and
// logged like the write gate's refusals — rather than dropped silently or
// half-applied.
const arrayWriteRefusal = "array writes are not supported"

// WithoutArrays strips the array members out of a template metric, at any
// depth, and returns their dotted paths (under p) so each can be refused by
// name — the edge for a command it receives, the host for one it sends. The
// template's other members are kept; a non-template metric is returned as is.
func WithoutArrays(p string, m Metric) (Metric, []string) {
	t, ok := m.Value.(*Template)
	if !ok || t == nil {
		return m, nil
	}
	var kept []Metric
	var arrays []string
	for _, member := range t.Metrics {
		mp := p + "." + member.Name
		if isArrayType(member.Datatype) {
			arrays = append(arrays, mp)
			continue
		}
		if _, nested := member.Value.(*Template); nested {
			pruned, a := WithoutArrays(mp, member)
			arrays = append(arrays, a...)
			member = pruned
		}
		kept = append(kept, member)
	}
	if len(arrays) == 0 {
		return m, nil
	}
	out := *t
	out.Metrics = kept
	m.Value = &out
	return m, arrays
}

// deviceLabel names a command destination for a log line: the device id, or
// "node" for the edge node itself.
func deviceLabel(device string) string {
	if device == "" {
		return "node"
	}
	return device
}

// commandTemplate writes a Template command metric into the struct tag it
// names, one front-door write per leaf member it carries ("Motor1.Speed",
// "Motor1.Drive.Torque"), all in one Tags.Write: they land under one lock,
// so a scan sees them together, and each is judged on its own, exactly as
// the same member sent by its member path would be. A member the type
// cannot hold, a forced member and a member the type does not have are
// refused one by one; the template's other members still apply. Members
// the template does not carry keep the value the edge holds.
func (n *Node) commandTemplate(m Metric) {
	tags := n.rt.Tags()
	prev, err := tags.ReadGlobal(m.Name)
	if err != nil {
		n.refuseCommand("a template for a tag this node does not have", m.Name)
		return
	}
	if prev.Kind != ir.TypeStruct || prev.Struct == nil {
		n.refuseCommand("a template for a tag that is not a UDT", m.Name, "kind", prev.Kind)
		return
	}
	var ops []runtime.WriteOp
	var leaves []Metric
	var walk func(path string, t *Template)
	walk = func(path string, t *Template) {
		for _, mm := range t.Metrics {
			if mm.Name == "" || mm.IsNull {
				continue
			}
			p := path + "." + mm.Name
			// A member name is a name: an index ("Levels[1]") would be an
			// element write past the array refusal, and a dot a path the
			// node never births.
			if strings.ContainsRune(mm.Name, '[') {
				n.refuseCommand(arrayWriteRefusal, p)
				continue
			}
			if strings.ContainsRune(mm.Name, '.') {
				n.refuseCommand("a member name with a dot", p)
				continue
			}
			if sub, ok := mm.Value.(*Template); ok {
				if sub != nil {
					walk(p, sub)
				}
				continue
			}
			v, ok := commandValue(mm)
			if _, isStr := v.(string); ok && isStr && !n.takesString(p) {
				ok = false // a String metric only for a STRING member, as for a tag
			}
			if !ok {
				n.refuseCommand("the value does not fit the tag's type", p, "datatype", mm.Datatype.String())
				continue
			}
			ops = append(ops, runtime.WriteOp{Path: p, Value: v})
			leaves = append(leaves, mm)
		}
	}
	if t, _ := m.Value.(*Template); t != nil {
		walk(m.Name, t)
	}
	written := 0
	for i, err := range tags.Write(ops) {
		if err != nil {
			reason := writeRefusal(err)
			if strings.Contains(err.Error(), "unknown member") { // ir's error for it is untyped
				reason = "a member the type does not have"
			}
			n.refuseCommand(reason, ops[i].Path, "member", leaves[i].Name, "type", prev.Struct.Name,
				"datatype", leaves[i].Datatype.String(), "error", err)
			continue
		}
		written++
	}
	n.log.Debug("sparkplug: template command write", "tag", m.Name, "members", written)
}

// warnCommandOnce logs a command diagnostic the first time its reason is seen,
// so a host retrying a bad member every scan does not flood the log.
func (n *Node) warnCommandOnce(reason, msg string, args ...any) {
	n.mu.Lock()
	if n.cmdWarned == nil {
		n.cmdWarned = map[string]bool{}
	}
	seen := n.cmdWarned[reason]
	n.cmdWarned[reason] = true
	n.mu.Unlock()
	if !seen {
		n.log.Warn(msg, args...)
	}
}

// requestRebirth answers a "Node Control/Rebirth" command. The spec says an
// edge node that receives one MUST stop sending DATA immediately and resume
// only after its new NBIRTH. So the gate closes HERE, synchronously, before
// the handler returns — rebirthPending stops every DATA not yet issued — and
// only the birth itself runs on its own goroutine: an MQTT message handler
// must not block on a publish (paho with OrderMatters), and the birth waits
// on pubMu behind any tick in progress. See the pubMu invariant in node.go.
//
// "Not yet issued" is exact: publishRecords checks the gate under n.mu per
// record, so the one record that can still go out after this returns is one
// that passed its check before the gate closed — the same, on the wire, as a
// record sent just before the command arrived.
func (n *Node) requestRebirth() {
	n.mu.Lock()
	n.rebirthPending = true
	n.mu.Unlock()
	go n.Rebirth()
}

// Rebirth republishes the birth certificates without a new MQTT session:
// bdSeq stays the same, seq restarts at 0. Runs on its own goroutine (see
// requestRebirth and the rebirth-debounce timer below), so it registers
// itself as in-flight the same way birth() does — no-op once Stop has begun,
// so Stop can wait for it before mutating bdSeq.
//
// It does not clear born: the rebirthPending gate stops data instead, which
// leaves born meaning what Stop reads it as — "an NBIRTH is out, so an
// NDEATH is owed" — even when Stop cancels the rebirth.
func (n *Node) Rebirth() {
	if !n.beginInflight() {
		return // Stop already in progress; no-op
	}
	defer n.inflight.Done()

	n.mu.Lock()
	n.rebirthPending = true
	n.mu.Unlock()
	if err := n.birth(); err != nil {
		n.log.Error("sparkplug: rebirth failed", "error", err)
	}
}

// scheduleRebirthLocked debounces a rebirth (500ms) after a new metric
// appears, so bursts of new tags coalesce into one rebirth. Caller holds n.mu.
func (n *Node) scheduleRebirthLocked() {
	if n.rebirthTimer != nil {
		return
	}
	n.rebirthTimer = time.AfterFunc(500*time.Millisecond, func() {
		n.mu.Lock()
		n.rebirthTimer = nil
		n.mu.Unlock()
		n.Rebirth()
	})
}

// commandValue converts a decoded command metric to a tag value the store
// accepts (bool/int64/float64/string).
func commandValue(m Metric) (any, bool) {
	switch v := m.Value.(type) {
	case bool, int64, float64, string:
		return v, true
	}
	return nil, false
}
