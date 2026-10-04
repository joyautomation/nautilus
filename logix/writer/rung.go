package writer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ld"
)

// Rung lowering: the ladder graph's elements to Logix neutral text.

// rungCtx carries one source rung's state: the helper rungs it needs
// before and after it, and the edge-instance counter that keeps generated
// names stable (the same scheme lang/ld uses for its implicit R_TRIGs).
type rungCtx struct {
	lw   *lowered
	r    ld.Rung
	seen map[string]int
	pre  []rungOut // helper rungs that run before this one
	post []rungOut // helper rungs that run after it (RES)
	// segments collects the rung texts a TOF split produces; the first is
	// the rung itself, each later one is headed by XIC(t.DN).
	segments []string
}

func (lw *lowered) rung(r ld.Rung, notes string) {
	c := &rungCtx{lw: lw, r: r, seen: map[string]int{}}
	text := c.series(r.Elements, true)
	coils := c.coils(r.Coils)
	// A rung with no coils and a block as its last element is legal in
	// both worlds; a bare continuation left by a TOF with nothing after
	// it would not be, and series never produces one.
	text += coils
	c.segments = append(c.segments, text)

	comment := rungComment(notes, r.Comment)
	lw.rungs = append(lw.rungs, c.pre...)
	for i, seg := range c.segments {
		out := rungOut{Text: seg, Source: r.Name, Line: r.Line}
		if i == 0 {
			out.Comment = comment
		}
		lw.rungs = append(lw.rungs, out)
	}
	lw.rungs = append(lw.rungs, c.post...)
}

// series lowers a run of condition elements. top is true for the rung's
// own series (where an edge at the head may inline as ONS and a TOF may
// split the rung) and false inside a branch leg.
func (c *rungCtx) series(elems []ld.Element, top bool) string {
	var parts []string
	for i, e := range elems {
		last := i == len(elems)-1
		switch e.Kind {
		case "contact":
			parts = append(parts, c.contact(e.Ref, e.Neg))
		case "fn":
			if t, ok := c.compare(e); ok {
				parts = append(parts, t)
			}
		case "edge":
			parts = append(parts, c.edge(e, top && i == 0 && len(c.segments) == 0))
		case "branch":
			var legs []string
			for _, leg := range e.Legs {
				legs = append(legs, c.series(leg, false))
			}
			parts = append(parts, "["+strings.Join(legs, " ,")+" ]")
		case "fb":
			t, cont, ok := c.block(e, top, last && len(c.r.Coils) == 0)
			if !ok {
				continue
			}
			parts = append(parts, t)
			if cont == "" {
				continue
			}
			if splitsRung(e.Type) {
				// The block ends this rung; what follows is a new rung
				// headed by its done bit.
				c.segments = append(c.segments, join(parts, top))
				parts = []string{cont}
				continue
			}
			parts = append(parts, cont)
		case "coil":
			// Unreachable: lang/ld keeps coils in Rung.Coils.
		}
	}
	return join(parts, top)
}

// join spells a series the way the exporter does: concatenated on the
// rung itself, space-separated inside a branch leg.
func join(parts []string, top bool) string {
	if top {
		return strings.Join(parts, "")
	}
	return strings.Join(parts, " ")
}

func (c *rungCtx) coils(coils []ld.Element) string {
	var out []string
	for _, e := range coils {
		ref, ok := c.ref(e.Ref)
		if !ok {
			continue
		}
		switch e.Mode {
		case "":
			out = append(out, "OTE("+ref+")")
		case "S":
			out = append(out, "OTL("+ref+")")
		case "R":
			out = append(out, "OTU("+ref+")")
		case "P", "N":
			c.lw.diag(ruleCoilEdge, c.r.Line, c.r.Name, "( %s %s ): edge coils are not in the v1 subset; put the edge on the condition side as +Name / -Name, or latch with ( S %s ) and clear elsewhere", e.Mode, e.Ref, e.Ref)
		}
	}
	switch len(out) {
	case 0:
		return ""
	case 1:
		return out[0]
	default:
		return "[" + strings.Join(out, " ,") + " ]"
	}
}

func (c *rungCtx) contact(ref string, neg bool) string {
	r, ok := c.ref(ref)
	if !ok {
		return ""
	}
	if neg {
		return "XIO(" + r + ")"
	}
	return "XIC(" + r + ")"
}

// memberRewrite maps IEC timer/counter members to the Logix structure's.
var memberRewrite = map[string]map[string]string{
	"TIMER":   {"Q": "DN", "ET": "ACC", "IN": "EN", "PT": "PRE"},
	"COUNTER": {"Q": "DN", "CV": "ACC", "CU": "CU", "PV": "PRE"},
}

// ref rewrites an operand reference for Logix: timer and counter members
// are renamed, array indexes pass through, and an accessor into anything
// else is refused (no UDTs in v1).
func (c *rungCtx) ref(ref string) (string, bool) {
	base, rest := splitRef(ref)
	v, declared := c.lw.vars[strings.ToLower(base)]
	if !declared {
		return ref, true // undeclared: the compiler's diagnostic, not ours
	}
	typ := strings.ToUpper(strings.TrimSpace(v.Type))
	if strings.EqualFold(typ, "TIME") {
		c.lw.diag(ruleTime, c.r.Line, c.r.Name, "%s: a TIME variable can only feed a timer preset in the Logix target", ref)
		return "", false
	}
	if rest == "" || strings.HasPrefix(rest, "[") && !strings.Contains(rest, ".") {
		return ref, true
	}
	if st := blockTypes[typ]; st != "" {
		member := strings.TrimPrefix(rest, ".")
		if to, ok := memberRewrite[st][strings.ToUpper(member)]; ok {
			return base + "." + to, true
		}
		c.lw.diag(ruleMember, c.r.Line, c.r.Name, "%s: %s has no %s member the Logix %s carries; the readable members are %s", ref, v.Type, member, st, membersOf(st))
		return "", false
	}
	c.lw.diag(ruleMember, c.r.Line, c.r.Name, "%s: member access into a %s has no Logix shape in v1 (no UDTs); declare the member as its own tag", ref, v.Type)
	return "", false
}

func membersOf(st string) string {
	switch st {
	case "TIMER":
		return "Q (DN), ET (ACC), IN (EN), PT (PRE)"
	default:
		return "Q (DN), CV (ACC), CU, PV (PRE)"
	}
}

// splitRef separates a reference's base identifier from its accessor
// chain: "t1.Q" → "t1", ".Q"; "Levels[2]" → "Levels", "[2]".
func splitRef(ref string) (string, string) {
	for i := 0; i < len(ref); i++ {
		if ref[i] == '.' || ref[i] == '[' {
			return ref[:i], ref[i:]
		}
	}
	return ref, ""
}

// compareComplement is the negated compare: /GT(a, b) is LE(a,b). NaN
// operands break the identity, which is a harness question (§5.5), not
// a writer one.
var compareComplement = map[string]string{"GT": "LE", "GE": "LT", "LT": "GE", "LE": "GT", "EQ": "NE", "NE": "EQ"}

func (c *rungCtx) compare(e ld.Element) (string, bool) {
	fn := strings.ToUpper(e.Fn)
	if _, ok := compareComplement[fn]; !ok {
		c.lw.diag(ruleFn, c.r.Line, c.r.Name, "%s(%s): only the compares GT GE LT LE EQ NE are function contacts in the Logix v1 subset", e.Fn, e.Args)
		return "", false
	}
	args := splitArgs(e.Args)
	if len(args) != 2 {
		c.lw.diag(ruleOperand, c.r.Line, c.r.Name, "%s(%s): a Logix compare takes exactly two operands", e.Fn, e.Args)
		return "", false
	}
	var ops []string
	for _, a := range args {
		op, ok := c.operand(a, e)
		if !ok {
			return "", false
		}
		ops = append(ops, op)
	}
	if e.Neg {
		fn = compareComplement[fn]
	}
	return fn + "(" + strings.Join(ops, ",") + ")", true
}

// operand accepts a tag reference or a numeric literal. Logix compares do
// take expressions (CMP does), but a nautilus expression here would need
// the ST expression grammar lowered to Logix's, which is Phase D's ST
// routine work, not v1.
func (c *rungCtx) operand(a string, e ld.Element) (string, bool) {
	a = strings.TrimSpace(a)
	if numRe.MatchString(a) {
		return a, true
	}
	if i := strings.Index(a, "#"); i > 0 {
		// INT#5, 16#FF: spell as decimal
		if n, ok := parseInt(a); ok && !strings.ContainsAny(a[:i], ".") && strings.ToUpper(a[:i]) != "T" && strings.ToUpper(a[:i]) != "TIME" {
			return strconv.FormatInt(n, 10), true
		}
	}
	if isRef(a) {
		return c.ref(a)
	}
	c.lw.diag(ruleOperand, c.r.Line, c.r.Name, "%s(%s): operand %q is an expression; Logix v1 compares take a tag or a numeric literal — compute it into a tag first", e.Fn, e.Args, a)
	return "", false
}

func isRef(s string) bool {
	if s == "" || !(s[0] == '_' || s[0] >= 'A' && s[0] <= 'Z' || s[0] >= 'a' && s[0] <= 'z') {
		return false
	}
	depth := 0
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '[':
			depth++
		case ch == ']':
			depth--
		case ch == '.' || ch == '_' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z':
		case ch == ' ' && depth > 0:
		default:
			return false
		}
	}
	return depth == 0
}

// splitArgs splits an argument list on top-level commas.
func splitArgs(args string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(args[start:i]))
				start = i + 1
			}
		}
	}
	if s := strings.TrimSpace(args[start:]); s != "" || len(out) > 0 {
		out = append(out, s)
	}
	return out
}

// ── edges ───────────────────────────────────────────────────────────────────

// edgeName is lang/ld's implicit-instance name for an edge: kind, rung,
// reference, with a counter for repeats of the same triple in one rung.
func (c *rungCtx) edgeName(kind, ref string) string {
	base := kind + "_" + sanitizeIdent(c.r.Name) + "_" + sanitizeIdent(ref)
	n := c.seen[base]
	c.seen[base] = n + 1
	if n == 0 {
		return base
	}
	return fmt.Sprintf("%s_%d", base, n+1)
}

func sanitizeIdent(s string) string {
	var b strings.Builder
	prevUnderscore := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		ok := ch == '_' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
		if !ok {
			ch = '_'
		}
		if ch == '_' {
			if prevUnderscore {
				continue
			}
			prevUnderscore = true
		} else {
			prevUnderscore = false
		}
		b.WriteByte(ch)
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "x"
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	return out
}

// edge lowers +Name / -Name. At the head of the rung a rising edge is the
// Logix idiom XIC(Name)ONS(storage): ONS acts on the rung-in condition,
// which there is Name alone. Anywhere else the rung-in condition includes
// whatever precedes the edge, so the one-shot moves to its own rung —
// XIC(Name)OSR(storage,out) — and the rung reads XIC(out). OSF's storage
// bit holds the previous rung-in, initially 0, so a falling edge does not
// fire on the first scan, exactly as nautilus's F_TRIG does not.
func (c *rungCtx) edge(e ld.Element, head bool) string {
	ref, ok := c.ref(e.Ref)
	if !ok {
		return ""
	}
	if e.Mode == "P" && head {
		st := c.edgeName("rt", e.Ref)
		c.lw.genTag(st, c.r.Line, c.r.Name)
		return "XIC(" + ref + ")ONS(" + st + ")"
	}
	kind, instr := "rt", "OSR"
	if e.Mode == "N" {
		kind, instr = "ft", "OSF"
	}
	st := c.edgeName(kind, e.Ref)
	out := st + "_Q"
	c.lw.genTag(st, c.r.Line, c.r.Name)
	c.lw.genTag(out, c.r.Line, c.r.Name)
	c.pre = append(c.pre, rungOut{Text: "XIC(" + ref + ")" + instr + "(" + st + "," + out + ")", Source: c.r.Name, Line: c.r.Line})
	return "XIC(" + out + ")"
}

// ── blocks ──────────────────────────────────────────────────────────────────

// splitsRung reports whether a block's done bit can be true while its
// rung-in is false. A Logix timer or counter passes its rung-in through,
// so power that nautilus takes from the block's Q has to be read as
// XIC(inst.DN). For a TON that can stay inline — DN is only ever true
// while the rung-in is — but a TOF's DN is true during the off delay and
// a CTU's DN stays true at the preset with no pulse present, so for those
// the rest of the rung moves to a continuation rung headed by the bit.
func splitsRung(typ string) bool {
	switch strings.ToUpper(typ) {
	case "TOF", "CTU":
		return true
	}
	return false
}

// block lowers a timer or counter. It returns the instruction text and the
// contact that carries power onward (XIC(inst.DN)), empty when the block
// is the rung's last element and nothing follows it.
func (c *rungCtx) block(e ld.Element, top, isLast bool) (text, cont string, ok bool) {
	typ := strings.ToUpper(e.Type)
	st := blockTypes[typ]
	if st == "" {
		alt := "the v1 blocks are TON, TOF and CTU"
		switch typ {
		case "TP", "CTD", "CTUD":
			alt = "its IEC semantics have no equivalent Logix instruction; " + alt
		case "R_TRIG", "F_TRIG":
			alt = "write the edge as +Name / -Name"
		case "SR", "RS":
			alt = "write the latch as ( S X ) and ( R X ) coils"
		default:
			if _, user := c.lw.userBlock(e.Type); user {
				alt = "user blocks become Add-On Instructions in a later phase; inline its rungs here"
			}
		}
		c.lw.diag(ruleFB, c.r.Line, c.r.Name, "%s:%s: not in the Logix v1 subset; %s", e.Inst, e.Type, alt)
		return "", "", false
	}
	if v, declared := c.lw.vars[strings.ToLower(e.Inst)]; !declared || !strings.EqualFold(strings.TrimSpace(v.Type), typ) {
		// The compiler reports an undeclared or mistyped instance; the
		// writer only needs to know it cannot type the tag.
		return "", "", false
	}
	if splitsRung(typ) && !top {
		c.lw.diag(ruleTOFPosition, c.r.Line, c.r.Name, "%s:%s inside a branch: a Logix %s passes its rung-in through, and its done bit can be true while the rung-in is false; put it on its own rung and use %s.Q as a contact", e.Inst, typ, typ, e.Inst)
		return "", "", false
	}
	var preset, reset string
	for _, a := range splitArgs(e.Args) {
		pin, val, isOut, ok := splitBinding(a)
		if !ok {
			c.lw.diag(ruleFBPin, c.r.Line, c.r.Name, "%s:%s(%s): cannot read the binding %q", e.Inst, e.Type, e.Args, a)
			return "", "", false
		}
		up := strings.ToUpper(pin)
		switch {
		case isOut:
			c.lw.diag(ruleFBPin, c.r.Line, c.r.Name, "%s:%s: output capture %s => %s has no Logix form; read %s.%s as a contact or operand where it is needed", e.Inst, e.Type, pin, val, e.Inst, pin)
			return "", "", false
		case up == "PT" && st == "TIMER", up == "PV" && st == "COUNTER":
			preset = val
		case up == "R" && st == "COUNTER":
			reset = val
		case up == "IN" || up == "CU":
			c.lw.diag(ruleFBPin, c.r.Line, c.r.Name, "%s:%s: the rung's power drives %s; don't bind it", e.Inst, e.Type, pin)
			return "", "", false
		default:
			c.lw.diag(ruleFBPin, c.r.Line, c.r.Name, "%s:%s: pin %s has no Logix mapping in v1 (timers take PT; counters take PV and R)", e.Inst, e.Type, pin)
			return "", "", false
		}
	}
	if preset == "" {
		pin := "PT"
		if st == "COUNTER" {
			pin = "PV"
		}
		c.lw.diag(rulePreset, c.r.Line, c.r.Name, "%s:%s: Logix needs the preset in the %s tag; bind %s := a literal or a variable", e.Inst, e.Type, st, pin)
		return "", "", false
	}
	if !c.preset(e, st, preset) {
		return "", "", false
	}
	if reset != "" {
		if !isRef(reset) || strings.ContainsAny(reset, ".[") {
			c.lw.diag(ruleReset, c.r.Line, c.r.Name, "%s:CTU: R := %s must be a plain BOOL tag; compute the condition into a tag first", e.Inst, reset)
			return "", "", false
		}
		// After the count, so a reset in the same scan as a count edge
		// wins — R is dominant in the IEC block.
		c.post = append(c.post, rungOut{Text: "XIC(" + reset + ")RES(" + e.Inst + ")", Source: c.r.Name, Line: c.r.Line})
	}
	text = typ + "(" + e.Inst + ",?,?)"
	if !isLast {
		cont = "XIC(" + e.Inst + ".DN)"
	}
	return text, cont, true
}

// preset records a block's preset: a literal lands in the tag's PRE; a
// variable gets a MOVE helper rung before this one, so the structure's
// PRE always holds the variable's current value.
func (c *rungCtx) preset(e ld.Element, st, val string) bool {
	tag := c.lw.findTag(e.Inst)
	if tag == nil {
		return false
	}
	if st == "TIMER" {
		if ms, ok := parseTime(val); ok {
			tag.Preset = ms
			return true
		}
	} else if n, ok := parseInt(val); ok {
		tag.Preset = n
		return true
	}
	if isRef(val) && !strings.ContainsAny(val, ".[") {
		if v, ok := c.lw.vars[strings.ToLower(val)]; ok {
			typ := strings.ToUpper(strings.TrimSpace(v.Type))
			if st == "TIMER" && typ == "TIME" || st == "COUNTER" && (typ == "SINT" || typ == "INT" || typ == "DINT") {
				c.pre = append(c.pre, rungOut{Text: "MOVE(" + val + "," + e.Inst + ".PRE)", Source: c.r.Name, Line: c.r.Line})
				return true
			}
		}
	}
	want := "a TIME literal (T#10S) or a TIME variable"
	if st == "COUNTER" {
		want = "an integer literal or a SINT/INT/DINT variable"
	}
	c.lw.diag(rulePreset, c.r.Line, c.r.Name, "%s:%s: preset %q must be %s", e.Inst, e.Type, val, want)
	return false
}

// scanPresets finds the variables that feed a preset, before declarations
// are typed: a TIME variable is legal only in that role.
func (lw *lowered) scanPresets(elems []ld.Element) {
	for _, e := range elems {
		switch e.Kind {
		case "branch":
			for _, leg := range e.Legs {
				lw.scanPresets(leg)
			}
		case "fb":
			for _, a := range splitArgs(e.Args) {
				pin, val, isOut, ok := splitBinding(a)
				if !ok || isOut {
					continue
				}
				if up := strings.ToUpper(pin); up == "PT" || up == "PV" {
					if isRef(val) {
						lw.presetVars[strings.ToLower(val)] = true
					}
				}
			}
		}
	}
}

func (lw *lowered) findTag(name string) *tagDef {
	for i := range lw.ctrlTags {
		if strings.EqualFold(lw.ctrlTags[i].Name, name) {
			return &lw.ctrlTags[i]
		}
	}
	for i := range lw.progTags {
		if strings.EqualFold(lw.progTags[i].Name, name) {
			return &lw.progTags[i]
		}
	}
	return nil
}

// userBlock reports whether a type is a user FUNCTION_BLOCK the model's
// catalog knows.
func (lw *lowered) userBlock(typ string) (ld.FBType, bool) {
	for _, t := range lw.model.FBTypes {
		if t.User && strings.EqualFold(t.Name, typ) {
			return t, true
		}
	}
	return ld.FBType{}, false
}

// splitBinding reads `PIN := value` or `PIN => target`.
func splitBinding(a string) (pin, val string, isOut, ok bool) {
	for _, sep := range []string{":=", "=>"} {
		if i := strings.Index(a, sep); i > 0 {
			pin = strings.TrimSpace(a[:i])
			val = strings.TrimSpace(a[i+len(sep):])
			return pin, val, sep == "=>", pin != "" && val != ""
		}
	}
	return "", "", false, false
}
