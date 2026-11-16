package writer

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/ld"
)

// READ / WRITE → MSG (docs/design/logix-authoring.md, "MSG as IEC 61131-5
// READ/WRITE on a channel"). A READ or WRITE instance is a MESSAGE tag, its
// call on a rung the MSG instruction, which passes its rung-in on as the
// block passes power. The tag's MessageParameters come from the call (VAR_1
// the remote element, RD_1 / SD_1 the local one), the channel ID names (the
// connection path), and target.logix.messages (what only Logix says).

// Channel is a manifest channels: entry, as the writer needs it.
type Channel struct {
	Name, Type, Host string
	Slot             *int
	Port             int // 0 or 44818: an MSG path cannot name another
}

// Message is a target.logix.messages entry: MSG parameters the import
// found that the block and channel do not say; "" keeps the default.
type Message struct {
	Name                                            string
	ConnectedFlag, CacheConnections, ConnectionPath string
}

// msgParams is a MESSAGE tag's MessageParameters, written in the order
// Studio 5000 exports them.
type msgParams struct {
	Type, Remote, Length, Connected, Path, Local, Cache string
}

// message lowers a READ / WRITE call to MSG(inst) and records the tag's
// parameters.
func (c *rungCtx) message(e ld.Element, typ string) (string, bool) {
	bad := func(format string, a ...any) (string, bool) {
		c.lw.diag(ruleMessage, c.r.Line, c.r.Name, "%s:%s: "+format, append([]any{e.Inst, e.Type}, a...)...)
		return "", false
	}
	if _, idx := splitRef(e.Inst); idx != "" {
		return bad("an array of messages has no MSG form; declare one instance per message")
	}
	if c.lw.st {
		return bad("a READ or WRITE is written as an MSG on a ladder rung; call it from a .ld program")
	}
	var id, remote, local string
	dataPin := "RD_1"
	p := msgParams{Type: "CIP Data Table Read", Connected: "2", Length: "1"}
	if typ == "WRITE" {
		dataPin, p.Type = "SD_1", "CIP Data Table Write"
	}
	for _, a := range splitArgs(e.Args) {
		pin, val, isOut, ok := splitBinding(a)
		if !ok {
			return bad("cannot read the binding %q", a)
		}
		if isOut {
			return bad("%s => …: read the block's outputs as %s.%s instead (the MESSAGE tag's bits)", pin, e.Inst, pin)
		}
		switch strings.ToUpper(pin) {
		case "ID":
			s, ok := iecString(val)
			if !ok {
				return bad("ID := %s: the MSG's path is fixed in its tag; name the channel with a string literal", val)
			}
			id = s
		case "VAR_1":
			s, ok := iecString(val)
			if !ok {
				return bad("VAR_1 := %s: the remote element is fixed in the MESSAGE tag; write it as a string literal", val)
			}
			remote = s
		case dataPin:
			if !isRef(val) {
				return bad("%s := %s: the local element is a tag, or a member or element of one", pin, val)
			}
			r, ok := c.ref(val)
			if !ok {
				return "", false
			}
			local = r
			// A whole array moves whole, as the block moves it: that many
			// elements from the first.
			if base, rest := splitRef(val); rest == "" {
				if v, ok := c.lw.vars[strings.ToLower(base)]; ok {
					if n := arrayLen(v.Type); n > 0 {
						p.Length = fmt.Sprint(n)
						local = base + "[0]"
						if lo := arrayLo(v.Type); lo != 0 {
							return bad("%s := %s: an array from index %d; Logix arrays start at 0", pin, val, lo)
						}
					}
				}
			}
		case "REQ":
			return bad("the rung's power drives REQ; don't bind it")
		default:
			return bad("pin %s has no MSG parameter", pin)
		}
	}
	if id == "" || remote == "" || local == "" {
		return bad("an MSG needs ID, VAR_1 and %s", dataPin)
	}
	var ch *Channel
	for i := range c.lw.opts.Channels {
		if ir.NameKey(c.lw.opts.Channels[i].Name) == ir.NameKey(id) {
			ch = &c.lw.opts.Channels[i]
		}
	}
	switch {
	case ch == nil && c.lw.opts.check:
		ch = &Channel{Name: id, Type: "cip", Host: "0.0.0.0"} // the manifest check names it
	case ch == nil:
		return bad("ID %q names no channel in channels:", id)
	case !strings.EqualFold(ch.Type, "cip"):
		return bad("channel %s is %s; an MSG reaches a Logix controller over a cip channel", id, ch.Type)
	case ch.Port != 0 && ch.Port != 44818:
		return bad("channel %s uses port %d; an MSG path reaches the EtherNet/IP port only", id, ch.Port)
	}
	p.Path = "2, " + ch.Host
	if ch.Slot != nil {
		p.Path += fmt.Sprintf(", 1, %d", *ch.Slot)
	}
	p.Remote, p.Local = remote, local
	for _, m := range c.lw.opts.Messages {
		if ir.NameKey(m.Name) != ir.NameKey(e.Inst) {
			continue
		}
		if m.ConnectedFlag != "" {
			p.Connected = m.ConnectedFlag
		}
		if m.ConnectionPath != "" {
			p.Path = m.ConnectionPath
		}
		p.Cache = m.CacheConnections
	}
	tag := c.lw.findTag(e.Inst)
	if tag == nil {
		return "", false
	}
	if tag.Message != nil {
		// One MSG per tag: two would share its EN/DN/ER, as two calls of
		// one block would share its REQ edge.
		return bad("%s is called on another rung too; a READ or WRITE instance has one call", e.Inst)
	}
	tag.Message = &p
	return "MSG(" + e.Inst + ")", true
}

// iecString unquotes a one-quoted IEC string literal.
func iecString(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return "", false
	}
	return strings.NewReplacer("$'", "'", "$$", "$").Replace(s[1 : len(s)-1]), true
}

// flagsRe is the import's spelling of a message's status word: the done
// bit (5) and the error bit (4).
var flagsRe = regexp.MustCompile(`(?i)^SEL\(([A-Za-z_][A-Za-z0-9_]*)\.(NDR|DONE),0,32\)\+SEL\(([A-Za-z_][A-Za-z0-9_]*)\.ERROR,0,16\)$`)

// msgFlags folds `{ D := SEL(M.NDR, 0, 32) + SEL(M.ERROR, 0, 16)
// (* @logix MSG.Flags *) }` back into MOVE(M.Flags,D).
func (c *rungCtx) msgFlags(a ld.Assignment) (string, bool) {
	expr := strings.ReplaceAll(a.Value.IEC(), " ", "")
	m := flagsRe.FindStringSubmatch(expr)
	if m == nil || !strings.EqualFold(m[1], m[3]) {
		c.lw.diag(ruleMessage, c.r.Line, c.r.Name, "{ %s := %s }: the @logix MSG.Flags pragma marks a message's status word, SEL(M.NDR, 0, 32) + SEL(M.ERROR, 0, 16); fix the expression or delete the pragma", a.Target, a.Value.IEC())
		return "", false
	}
	v, ok := c.lw.vars[strings.ToLower(m[1])]
	if !ok || blockTypes[strings.ToUpper(strings.TrimSpace(v.Type))] != "MESSAGE" {
		c.lw.diag(ruleMessage, c.r.Line, c.r.Name, "{ %s := … }: %s is not a READ or WRITE instance", a.Target, m[1])
		return "", false
	}
	target, ok := c.ref(a.Target)
	if !ok {
		return "", false
	}
	return "MOVE(" + m[1] + ".Flags," + target + ")", true
}

// checkMessages reports a READ or WRITE instance no rung calls: its MESSAGE
// tag would have no parameters to carry.
func (lw *lowered) checkMessages() {
	for _, tags := range [][]tagDef{lw.ctrlTags, lw.progTags} {
		for _, t := range tags {
			if t.DataType == "MESSAGE" && t.Message == nil {
				lw.diag(ruleMessage, t.Line, "", "%s: no rung calls this READ or WRITE, so its MESSAGE tag has no parameters; call it or delete it", t.Name)
			}
		}
	}
}

// arrayLen and arrayLo read `ARRAY [lo..hi] OF T`: its element count (0
// when t is not a one-dimensional array) and its lower bound.
func arrayLen(t string) int {
	lo, hi, ok := arrayBounds(t)
	if !ok {
		return 0
	}
	return int(hi - lo + 1)
}

func arrayLo(t string) int64 {
	lo, _, _ := arrayBounds(t)
	return lo
}

func arrayBounds(t string) (lo, hi int64, ok bool) {
	m := arrayRe.FindStringSubmatch(strings.TrimSpace(t))
	if m == nil || m[3] != "" { // one dimension only
		return 0, 0, false
	}
	lo, _ = parseInt(m[1])
	hi, _ = parseInt(m[2])
	return lo, hi, hi >= lo
}
