package importer

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/l5x"
)

// MSG → READ / WRITE (docs/design/logix-authoring.md, "MSG as IEC 61131-5
// READ/WRITE on a channel").
//
// A MESSAGE tag of type CIP Data Table Read or Write becomes a READ or
// WRITE instance of the same name; the MSG instruction on it becomes the
// block in the rung, power on REQ; the peer the ConnectionPath names
// becomes a channels: entry the block's ID names. Status members map to
// the block's pins (DN → NDR/DONE, ER → ERROR, ERR → STATUS), and the
// common copy of the status word, MOVE(M.Flags, D), becomes D built from
// the done and error bits (DN is bit 5, ER bit 4), marked
// (* @logix MSG.Flags *) so the writer folds it back.
//
// What a Logix MSG holds that the block and channel do not (ConnectedFlag
// and CacheConnections, a ConnectionPath spelled otherwise) goes to
// target.logix.messages, for the write-back. Refused, by reason: a CIP
// Generic message (socket and other services), a path through more than
// one hop, more than one element, and the members with no pin (EN, EW,
// ST, TO, ...).

// msgInfo is what the import makes of one MESSAGE tag.
type msgInfo struct {
	block   string // READ or WRITE
	remote  string // RemoteElement: VAR_1
	local   string // LocalElement: RD_1 / SD_1
	channel string // the channels: entry ID names
	refused *refusal
}

// msgChannel is one channels: entry the import writes.
type msgChannel struct {
	name, host string
	slot       int // -1: none (the device at host)
}

// msgExtra is one target.logix.messages entry: what only Logix needs.
type msgExtra struct {
	connected, cache, path string
}

var ipv4 = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}$`)

// msgDone is the pin a MESSAGE's DN becomes.
func msgDone(block string) string {
	if block == "WRITE" {
		return "DONE"
	}
	return "NDR"
}

// message resolves a MESSAGE tag once.
func (im *importer) message(t *l5x.Tag) *msgInfo {
	if m, ok := im.msgs[t]; ok {
		return m
	}
	m := im.newMessage(t)
	im.msgs[t] = m
	return m
}

func (im *importer) newMessage(t *l5x.Tag) *msgInfo {
	m := &msgInfo{}
	if t.Message == nil {
		m.refused = refuse("MSG", "%s carries no message parameters", t.Name)
		return m
	}
	if t.Dimensions != "" {
		m.refused = refuse("MSG", "%s: an array of MESSAGE", t.Name)
		return m
	}
	switch typ := t.MessageParam("MessageType"); typ {
	case "CIP Data Table Read":
		m.block = "READ"
	case "CIP Data Table Write":
		m.block = "WRITE"
	default:
		m.refused = refuse("MSG", "%s is a %s message; only CIP Data Table Read/Write have a nautilus form (READ/WRITE)", t.Name, typ)
		return m
	}
	if n := t.MessageParam("RequestedLength"); n != "1" {
		m.refused = refuse("MSG", "%s moves %s elements; READ and WRITE move one", t.Name, n)
		return m
	}
	if li := t.MessageParam("LocalIndex"); li != "" && li != "0" {
		m.refused = refuse("MSG", "%s: a local index %s", t.Name, li)
		return m
	}
	m.remote, m.local = t.MessageParam("RemoteElement"), t.MessageParam("LocalElement")
	if m.remote == "" || m.local == "" {
		m.refused = refuse("MSG", "%s names no remote or no local element", t.Name)
		return m
	}
	path := t.MessageParam("ConnectionPath")
	host, slot, ok := msgPath(path)
	if !ok {
		m.refused = refuse("MSG", "%s: connection path %q is not out the Ethernet port to one controller (2, address[, 1, slot])", t.Name, path)
		return m
	}
	m.channel = im.msgChannel(host, slot)
	x := msgExtra{}
	if c := t.MessageParam("ConnectedFlag"); c != "2" {
		x.connected = c
	}
	x.cache = t.MessageParam("CacheConnections")
	if path != canonicalPath(host, slot) {
		x.path = path
	}
	if x != (msgExtra{}) {
		im.msgExtras[l5x.Ident(t.Name)] = x
	}
	return m
}

// msgPath reads a ConnectionPath of the two shapes a channel carries: out
// the local Ethernet port (2) to an address, optionally on through that
// device's backplane (1) to a slot.
func msgPath(p string) (host string, slot int, ok bool) {
	parts := strings.Split(p, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if len(parts) != 2 && len(parts) != 4 {
		return "", 0, false
	}
	if parts[0] != "2" || (!ipv4.MatchString(parts[1]) && net.ParseIP(parts[1]) == nil) {
		return "", 0, false
	}
	if len(parts) == 2 {
		return parts[1], -1, true
	}
	n, err := strconv.Atoi(parts[3])
	if parts[2] != "1" || err != nil || n < 0 {
		return "", 0, false
	}
	return parts[1], n, true
}

// canonicalPath is how the writer spells a channel's path back.
func canonicalPath(host string, slot int) string {
	if slot < 0 {
		return "2, " + host
	}
	return fmt.Sprintf("2, %s, 1, %d", host, slot)
}

// msgChannel names the channel to host (and slot), adding it once.
func (im *importer) msgChannel(host string, slot int) string {
	key := fmt.Sprintf("%s/%d", host, slot)
	if n, ok := im.chanByKey[key]; ok {
		return n
	}
	name := "cip_" + strings.NewReplacer(".", "_", ":", "_").Replace(host)
	if slot >= 0 {
		name += fmt.Sprintf("_s%d", slot)
	}
	im.chanByKey[key] = name
	im.channels = append(im.channels, msgChannel{name: name, host: host, slot: slot})
	return name
}

// msgTag finds the MESSAGE tag an operand names, and what the import makes
// of it.
func (rt *routine) msgTag(operand string) (*found, *msgInfo, *refusal) {
	f := rt.sc.lookup(baseOf(operand))
	if f == nil {
		return nil, nil, refuse("undefined", "tag %s is not in scope", baseOf(operand))
	}
	if !strings.EqualFold(f.dtype, "MESSAGE") || f.tag == nil {
		return nil, nil, refuse("MSG", "%s is a %s, not a MESSAGE", f.name, f.dtype)
	}
	m := rt.im.message(f.tag)
	if m.refused != nil {
		return nil, nil, m.refused
	}
	return f, m, nil
}

// msgBlock lowers MSG(M) to the block call: M:READ(ID := 'channel',
// VAR_1 := 'Remote', RD_1 := Local). Power is REQ and passes through.
func (rt *routine) msgBlock(in *l5x.Instr) (string, *refusal) {
	if len(in.Args) != 1 {
		return "", refuse("MSG", "%d operands", len(in.Args))
	}
	f, m, err := rt.msgTag(in.Args[0])
	if err != nil {
		return "", err
	}
	if baseOf(in.Args[0]) != in.Args[0] {
		return "", refuse("MSG", "%s: a member as the message", in.Args[0])
	}
	name, err := rt.ref(in.Args[0], m.block)
	if err != nil {
		return "", err
	}
	local, err := rt.ref(m.local, "")
	if err != nil {
		return "", refuse("MSG", "%s's local element: %s", f.name, err.text)
	}
	pin := "RD_1"
	if m.block == "WRITE" {
		pin = "SD_1"
	} else {
		rt.markWritten(m.local)
	}
	return fmt.Sprintf("%s:%s(ID := %s, VAR_1 := %s, %s := %s)", name, m.block, stQuote(m.channel), stQuote(m.remote), pin, local), nil
}

// msgMember maps a MESSAGE member to the block's pin.
func msgMember(m *msgInfo, member string) (string, *refusal) {
	switch strings.ToUpper(member) {
	case "DN":
		return msgDone(m.block), nil
	case "ER":
		return "ERROR", nil
	case "ERR":
		return "STATUS", nil
	case "EN":
		return "", refuse("MSG", ".EN is TRUE while the rung is true or the message is in flight; no pin says that (BUSY is only in flight)")
	}
	return "", refuse("MSG", ".%s has no READ/WRITE pin", member)
}

// msgFlags lowers MOVE(M.Flags, D): D gets the status word's two bits the
// logic reads, DN (bit 5) and ER (bit 4), from the block's pins; the other
// bits read 0. The marker lets the writer give the MOVE back.
func (rt *routine) msgFlags(in *l5x.Instr) (string, bool, *refusal) {
	if len(in.Args) != 2 || !strings.EqualFold(strings.TrimPrefix(in.Args[0][len(baseOf(in.Args[0])):], "."), "Flags") {
		return "", false, nil
	}
	f := rt.sc.lookup(baseOf(in.Args[0]))
	if f == nil || !strings.EqualFold(f.dtype, "MESSAGE") {
		return "", false, nil
	}
	_, m, err := rt.msgTag(in.Args[0])
	if err != nil {
		return "", true, err
	}
	name, err := rt.ref(baseOf(in.Args[0]), m.block)
	if err != nil {
		return "", true, err
	}
	dst, err := rt.ref(in.Args[1], "")
	if err != nil {
		return "", true, err
	}
	rt.markWritten(in.Args[1])
	return fmt.Sprintf("{ %s := SEL(%s.%s, 0, 32) + SEL(%s.ERROR, 0, 16) (* @logix MSG.Flags *) }",
		dst, name, msgDone(m.block), name), true, nil
}

// stQuote is an IEC string literal.
func stQuote(s string) string {
	return "'" + strings.NewReplacer("$", "$$", "'", "$'").Replace(s) + "'"
}

// msgRef rewrites an operand on a MESSAGE tag: the instance itself (an
// MSG's operand, blockType READ or WRITE) or a status member as the
// block's pin.
func (rt *routine) msgRef(f *found, rest, blockType string) (string, *refusal) {
	if f.tag == nil {
		return "", refuse("MSG", "%s: a MESSAGE parameter of an Add-On Instruction", f.name)
	}
	if f.dims != "" {
		return "", refuse("MSG", "%s: an array of MESSAGE", f.name)
	}
	m := rt.im.message(f.tag)
	if m.refused != nil {
		return "", m.refused
	}
	if owner, ok := rt.sc.blockOwner[strings.ToLower(f.name)]; ok && rt.sc.ctrl != nil {
		if !strings.EqualFold(rt.sc.owner, rt.ownerProgram()+"/"+owner) {
			return "", refuse("shared-block", "%s is sent by routine %s; a block instance belongs to one nautilus program", f.name, owner)
		}
	}
	if !rt.im.declareFound(rt.sc, rt.p, f, blockType) {
		return "", refuse("block-type", "%s is sent by two different kinds of message", f.name)
	}
	if rest == "" {
		if blockType == "" {
			return "", refuse("MSG", "%s: a whole MESSAGE as an operand", f.name)
		}
		return f.name, nil
	}
	if blockType != "" || !strings.HasPrefix(rest, ".") || strings.ContainsAny(rest[1:], ".[") {
		return "", refuse("MSG", "%s%s: not a status member", f.name, rest)
	}
	pin, err := msgMember(m, rest[1:])
	if err != nil {
		return "", err
	}
	return f.name + "." + pin, nil
}
