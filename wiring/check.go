package wiring

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Env is what the format cannot know by itself. Both hooks are optional: a
// nil one skips the checks that need it, which is how a manifest read
// before the project's ST is compiled still checks everything else.
type Env struct {
	// Driver resolves a source's `driver:` (a `drivers:` entry's name) to
	// its driver type. A name it cannot resolve is tried as a type.
	Driver func(name string) (typ string, ok bool)
	// Type returns every member path of a TYPE, structs included, without
	// array indexes.
	Type func(name string) (paths []string, ok bool)
}

// Problem is one thing wrong, with where.
type Problem struct {
	Pos Pos
	Msg string
}

func (p Problem) Error() string {
	switch {
	case p.Pos.File != "" && p.Pos.Line > 0:
		return fmt.Sprintf("%s:%d: %s", p.Pos.File, p.Pos.Line, p.Msg)
	case p.Pos.File != "":
		return p.Pos.File + ": " + p.Msg
	}
	return p.Msg
}

// Err joins problems into one error, or nil.
func Err(ps []Problem) error {
	if len(ps) == 0 {
		return nil
	}
	errs := make([]error, len(ps))
	for i, p := range ps {
		errs[i] = p
	}
	return errors.Join(errs...)
}

var (
	identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	paramRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	pathRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*|\[[0-9]+\])*$`)
	indexRE = regexp.MustCompile(`\[[0-9]+\]`)
	refRE   = regexp.MustCompile(`\{([^{}]*)\}`)
)

// builtinParams are always in scope in a substitution (design §4).
var builtinParams = []string{"name", "desc"}

// Check validates every wiring and instance of files together: they share
// one namespace across files. It reports every problem, in a stable order.
func Check(files []*File, env Env) []Problem {
	c := &checker{env: env, wirings: map[string]*Wiring{}}
	var ws []*Wiring
	var is []*Instance
	for _, f := range files {
		for i := range f.Wirings {
			ws = append(ws, &f.Wirings[i])
		}
		for i := range f.Instances {
			is = append(is, &f.Instances[i])
		}
	}
	for _, w := range ws {
		c.wiring(w)
	}
	c.sameKinds(ws)
	for _, in := range is {
		c.instance(in)
	}
	c.instanceNames(is)
	sort.SliceStable(c.out, func(i, j int) bool {
		a, b := c.out[i].Pos, c.out[j].Pos
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return c.out
}

type checker struct {
	env     Env
	wirings map[string]*Wiring // by ir.NameKey of the wiring's name
	byType  map[string][]*Wiring
	out     []Problem
}

func (c *checker) add(pos Pos, format string, a ...any) {
	c.out = append(c.out, Problem{pos, fmt.Sprintf(format, a...)})
}

// at is a member's position, falling back to its wiring's.
func at(w *Wiring, m *Member) Pos {
	if m != nil && m.Line > 0 {
		return Pos{w.Pos.File, m.Line}
	}
	return w.Pos
}

func (c *checker) wiring(w *Wiring) {
	if c.byType == nil {
		c.byType = map[string][]*Wiring{}
	}
	if w.Type == "" {
		c.add(w.Pos, "a wiring needs type: (the TYPE it wires)")
		return
	}
	if !identRE.MatchString(w.Type) {
		c.add(w.Pos, "wiring type %q is not an IEC identifier", w.Type)
	}
	name := w.WiringName()
	if !identRE.MatchString(name) {
		c.add(w.Pos, "wiring name %q is not an IEC identifier", name)
	}
	if prev, dup := c.wirings[ir.NameKey(name)]; dup {
		c.add(w.Pos, "wiring %q is declared again (first at %s:%d): a wiring's name is unique across wiring files; give one a name:", name, prev.Pos.File, prev.Pos.Line)
		return
	}
	c.wirings[ir.NameKey(name)] = w
	c.byType[ir.NameKey(w.Type)] = append(c.byType[ir.NameKey(w.Type)], w)

	var paths map[string]bool
	if c.env.Type != nil {
		ps, ok := c.env.Type(w.Type)
		if !ok {
			c.add(w.Pos, "wiring %q is for type %q, which the project does not declare", name, w.Type)
		} else {
			paths = map[string]bool{}
			for _, p := range ps {
				paths[p] = true
			}
		}
	}

	params := map[string]bool{}
	for _, b := range builtinParams {
		params[b] = true
	}
	for _, p := range sortedKeys(w.Params) {
		switch {
		case !paramRE.MatchString(p):
			c.add(w.Pos, "param %q: a param name is letters, digits, _ and -", p)
		case p == "name" || p == "desc":
			c.add(w.Pos, "param %q is built in (the instance's own %s): choose another name", p, p)
		}
		params[p] = true
	}
	refs := func(pos Pos, where, s string) { c.refs(pos, where, s, params) }

	for _, s := range sortedKeys(w.Sources) {
		src := w.Sources[s]
		if !identRE.MatchString(s) {
			c.add(w.Pos, "source %q is not an identifier", s)
		}
		if src.Driver == "" {
			c.add(w.Pos, "source %q needs driver: (a drivers: entry's name, or a driver type)", s)
		} else if c.env.Driver != nil {
			if _, ok := c.env.Driver(src.Driver); !ok {
				c.add(w.Pos, "source %q: driver %q is not a drivers: entry or a driver type", s, src.Driver)
			}
		}
		for _, k := range sortedKeys(src.Keys) {
			if v, ok := src.Keys[k].(string); ok {
				refs(w.Pos, "source "+s+" "+k, v)
			}
		}
	}
	if f := w.Fill; f != nil {
		if _, ok := w.Sources[f.From]; !ok {
			c.add(w.Pos, "fill: from %q is not a source of this wiring%s", f.From, didYouMean(f.From, sortedKeys(w.Sources)))
		}
		if f.Member != "" && !pathRE.MatchString(f.Member) {
			c.add(w.Pos, "fill: member %q is not a member path", f.Member)
		}
		for _, k := range sortedKeys(f.Keys) {
			if v, ok := f.Keys[k].(string); ok {
				refs(w.Pos, "fill "+k, v)
			}
		}
	}

	paths2 := sortedKeys(w.Members)
	for _, p := range paths2 {
		m := w.Members[p]
		pos := at(w, &m)
		if !pathRE.MatchString(p) {
			c.add(pos, "member %q is not a member path (Name, Group.Name, Array[1])", p)
			continue
		}
		if paths != nil && !paths[indexRE.ReplaceAllString(p, "")] {
			c.add(pos, "wiring %q: type %s has no member %q%s", name, w.Type, p, didYouMean(p, keysOf(paths)))
		}
		c.member(w, p, &m, pos, refs)
		// A name that is a prefix of another entry is a struct: only a
		// whole-struct `memory:` may name one.
		if m.Kind != Memory && m.Kind != "" {
			for _, q := range paths2 {
				if strings.HasPrefix(q, p+".") || strings.HasPrefix(q, p+"[") {
					c.add(pos, "member %q is a whole struct (%q is under it) but is wired %s: only memory: may name a whole struct; wire its members instead", p, q, m.Kind)
					break
				}
			}
		}
	}

	seenAlarm := map[string]bool{}
	for _, a := range w.Alarms {
		switch {
		case a.Member == "":
			c.add(w.Pos, "an alarm needs member:")
			continue
		case seenAlarm[a.Member]:
			c.add(w.Pos, "member %q has two alarms: a member has one", a.Member)
			continue
		}
		seenAlarm[a.Member] = true
		c.inType(w, paths, a.Member, "an alarm", w.Pos)
		c.explicit(w, a.Member, "an alarm", w.Pos)
		refs(w.Pos, "alarm "+a.Member+" name", a.Name)
		refs(w.Pos, "alarm "+a.Member+" notes", a.Notes)
	}
}

// member checks one entry's own rules.
func (c *checker) member(w *Wiring, path string, m *Member, pos Pos, refs func(Pos, string, string)) {
	switch m.Kind {
	case "":
		c.add(pos, "member %q has no kind: give it one of from:, to:, both:, memory: or expr: (a member left out is retained memory)", path)
		return
	case From, To, Both:
		if m.Source == "" {
			c.add(pos, "member %q: %s: needs a source name (one of %s)", path, m.Kind, strings.Join(sortedKeys(w.Sources), ", "))
		} else if _, ok := w.Sources[m.Source]; !ok {
			c.add(pos, "member %q: %s: %q is not a source of this wiring%s", path, m.Kind, m.Source, didYouMean(m.Source, sortedKeys(w.Sources)))
		} else if m.Kind == Both {
			c.both(w, path, m, pos)
		}
	case Memory:
		if m.MemoryMode != Retained && m.MemoryMode != Initial {
			c.add(pos, "member %q: memory: is %s or %s, not %q", path, Retained, Initial, m.MemoryMode)
		}
	case Expr:
		if strings.TrimSpace(m.Expr) == "" {
			c.add(pos, "member %q: expr: is empty", path)
		}
		refs(pos, "member "+path+" expr", m.Expr)
	}
	if len(m.Addr) > 0 && (m.Kind == Memory || m.Kind == Expr) {
		c.add(pos, "member %q is %s, which takes no address keys (%s): remove them, or make it from:, to: or both:", path, m.Kind, strings.Join(sortedKeys(m.Addr), ", "))
	}
	for _, k := range sortedKeys(m.Addr) {
		if near := nearKey(k); near != "" {
			c.add(pos, "member %q: %q is passed to the driver as an address key, but it is one edit from %q: did you mean %s? (a misspelled event: would silently make a command re-sendable)", path, k, near, near)
		}
		if v, ok := m.Addr[k].(string); ok {
			refs(pos, "member "+path+" "+k, v)
		}
	}
	if m.Readback != "" {
		c.readback(w, path, m, pos)
	}
	if m.Event && m.Kind != To {
		c.add(pos, "member %q: event: is only for a to: member (it is a command that must never be sent twice), and this one is %s", path, m.Kind)
	}
	refs(pos, "member "+path+" unit", m.Unit)
	refs(pos, "member "+path+" desc", m.Desc)
}

// nearKey is the member key an address key looks like a misspelling of.
func nearKey(k string) string {
	for _, w := range []string{"readback", "event", "unit", "desc", "memory", "expr"} {
		limit := 2
		if len(w) <= 4 && (k == "" || k[0] != w[0]) {
			limit = 1 // a short word is a typo only if it starts the same
		}
		if k != w && editDistance(strings.ToLower(k), w) <= limit {
			return w
		}
	}
	return ""
}

// both refuses `both:` on a field-device driver.
func (c *checker) both(w *Wiring, path string, m *Member, pos Pos) {
	drv := w.Sources[m.Source].Driver
	typ := drv
	if c.env.Driver != nil {
		t, ok := c.env.Driver(drv)
		if !ok {
			return // reported at the source
		}
		typ = t
	}
	for _, p := range PeerDrivers {
		if typ == p {
			return
		}
	}
	if c.env.Driver == nil && !knownFieldDriver(typ) {
		return // a drivers: entry's name, which only the manifest can resolve
	}
	c.add(pos, "member %q: both: is only for a peer controller (%s); source %q is a %s field device. Use to: with readback: to command it and read it back", path, strings.Join(PeerDrivers, ", "), m.Source, typ)
}

var fieldDrivers = []string{"modbus", "opcua-client", "snmp", "redfish", "prometheus", "optommp", "plcnext", "profinet-device", "replay"}

func knownFieldDriver(t string) bool {
	for _, d := range fieldDrivers {
		if d == t {
			return true
		}
	}
	return false
}

// IsDriverType reports whether t is a driver type a wiring source can name
// (a field device or a peer controller).
func IsDriverType(t string) bool {
	for _, d := range PeerDrivers {
		if d == t {
			return true
		}
	}
	return knownFieldDriver(t)
}

// readback checks a to: member's read-back.
func (c *checker) readback(w *Wiring, path string, m *Member, pos Pos) {
	if m.Kind != To {
		c.add(pos, "member %q: readback: is only for a to: member, and this one is %s", path, m.Kind)
		return
	}
	t, ok := w.Members[m.Readback]
	switch {
	case !ok && w.Fill != nil:
	case !ok:
		c.add(pos, "member %q: readback: %q is not wired in this wiring: a member left out is writable memory, which shows nothing about the device. Wire it from:, both: or expr:%s", path, m.Readback, didYouMean(m.Readback, sortedKeys(w.Members)))
	case t.Kind != From && t.Kind != Both && t.Kind != Expr:
		c.add(pos, "member %q: readback: %q is %s: a read-back must be from:, both: or expr:", path, m.Readback, t.Kind)
	}
}

// inType refuses a path the wiring's type does not have, when the project's
// types are known.
func (c *checker) inType(w *Wiring, paths map[string]bool, path, what string, pos Pos) {
	if paths == nil || paths[indexRE.ReplaceAllString(path, "")] {
		return
	}
	c.add(pos, "%s names member %q, but type %s has none%s", what, path, w.Type, didYouMean(path, keysOf(paths)))
}

// typePaths is the member paths of a type, or nil when they are not known.
func (c *checker) typePaths(typ string) map[string]bool {
	if c.env.Type == nil {
		return nil
	}
	ps, ok := c.env.Type(typ)
	if !ok {
		return nil
	}
	m := map[string]bool{}
	for _, p := range ps {
		m[p] = true
	}
	return m
}

// explicit requires a member an alarm or similar names to be wired.
func (c *checker) explicit(w *Wiring, path, what string, pos Pos) {
	if _, ok := w.Members[path]; ok || w.Fill != nil {
		return
	}
	c.add(pos, "%s names member %q, which this wiring does not wire: a member left out is writable memory, so an alarm on it could be set by hand. Wire it from:, both: or expr:%s", what, path, didYouMean(path, sortedKeys(w.Members)))
}

// refs reports every {name} in s that is not a declared param, and a brace
// that is not part of one.
func (c *checker) refs(pos Pos, where, s string, params map[string]bool) {
	for _, m := range refRE.FindAllStringSubmatch(s, -1) {
		switch {
		case !paramRE.MatchString(m[1]):
			c.add(pos, "%s: {%s} is not a param name", where, m[1])
		case !params[m[1]]:
			c.add(pos, "%s: {%s} is not a param of this wiring (it has %s)%s", where, m[1], paramList(params), didYouMean(m[1], keysOf(params)))
		}
	}
	if rest := refRE.ReplaceAllString(s, ""); strings.ContainsAny(rest, "{}") {
		c.add(pos, "%s: an unmatched brace in %q: write {param} to substitute a param", where, s)
	}
}

func paramList(params map[string]bool) string {
	ks := keysOf(params)
	if len(ks) == 0 {
		return "no params"
	}
	return "params " + strings.Join(ks, ", ")
}

// kindOf is a member's effective kind in a wiring: its own entry, else the
// nearest whole-struct `memory:` above it, else implicit retained memory.
// known is false for a path a fill wiring may bring from the peer.
func kindOf(w *Wiring, path string) (k Kind, mode string, known bool) {
	for p := path; ; {
		if m, ok := w.Members[p]; ok && (p == path || m.Kind == Memory) {
			return m.Kind, m.MemoryMode, true
		}
		i := strings.LastIndexAny(p, ".[")
		if i < 0 {
			break
		}
		p = p[:i]
	}
	if w.Fill != nil {
		return "", "", false
	}
	return Memory, Retained, true
}

// sameKinds enforces one kind per member per type across its wirings: a
// member's writability must not depend on which wiring an instance uses,
// or no call site could be checked (design §2.4).
func (c *checker) sameKinds(ws []*Wiring) {
	for _, group := range c.byType {
		if len(group) < 2 {
			continue
		}
		paths := map[string]bool{}
		for _, w := range group {
			for p := range w.Members {
				paths[p] = true
			}
		}
		for _, p := range sortedKeys(paths) {
			if isPrefix(paths, p) {
				continue // a whole struct: its leaves are compared
			}
			var first *Wiring
			var fk Kind
			var fmode string
			for _, w := range group {
				k, mode, known := kindOf(w, p)
				if !known {
					continue
				}
				if first == nil {
					first, fk, fmode = w, k, mode
					continue
				}
				if k != fk || (k == Memory && mode != fmode) {
					c.add(w.Pos, "member %q of type %s is %s in wiring %q but %s in %q: a member has one kind per type, so an FB's writes can be checked%s",
						p, w.Type, describe(k, mode), w.WiringName(), describe(fk, fmode), first.WiringName(), implicitHint(w, p))
				}
			}
		}
		var ver *Wiring
		for _, w := range group {
			if w.Version == "" {
				continue
			}
			if ver != nil && ver.Version != w.Version {
				c.add(w.Pos, "type %s has version %q in wiring %q and %q in %q: a type publishes one Template definition, so it has one version", w.Type, w.Version, w.WiringName(), ver.Version, ver.WiringName())
			}
			if ver == nil {
				ver = w
			}
		}
	}
}

// isPrefix reports whether another path is under p (p is a struct).
func isPrefix(paths map[string]bool, p string) bool {
	for q := range paths {
		if strings.HasPrefix(q, p+".") || strings.HasPrefix(q, p+"[") {
			return true
		}
	}
	return false
}

func describe(k Kind, mode string) string {
	if k == Memory {
		return "memory (" + mode + ")"
	}
	return string(k)
}

func implicitHint(w *Wiring, p string) string {
	if _, ok := w.Members[p]; !ok {
		return " (here it is left out, which makes it retained memory)"
	}
	return ""
}

func (c *checker) instanceNames(is []*Instance) {
	seen := map[string]*Instance{}
	for _, in := range is {
		if in.Name == "" {
			continue
		}
		if prev, dup := seen[ir.NameKey(in.Name)]; dup {
			c.add(in.Pos, "instance %q is declared again (first at %s:%d)", in.Name, prev.Pos.File, prev.Pos.Line)
			continue
		}
		seen[ir.NameKey(in.Name)] = in
		if w, clash := c.wirings[ir.NameKey(in.Name)]; clash {
			c.add(in.Pos, "instance %q has the name of the wiring at %s:%d: rename one", in.Name, w.Pos.File, w.Pos.Line)
		}
	}
}

func (c *checker) instance(in *Instance) {
	switch {
	case in.Name == "":
		c.add(in.Pos, "an instance needs name:")
		return
	case !identRE.MatchString(in.Name):
		c.add(in.Pos, "instance name %q is not an IEC identifier", in.Name)
	}
	if in.Type == "" {
		c.add(in.Pos, "instance %s needs type:", in.Name)
		return
	}
	w := c.pick(in)
	if w == nil {
		return
	}

	for _, p := range sortedKeys(in.Params) {
		_, ok := w.Params[p]
		switch {
		case !ok:
			c.add(in.Pos, "instance %s: %q is not a param of wiring %q (it has %s)%s", in.Name, p, w.WiringName(), paramNames(w), didYouMean(p, sortedKeys(w.Params)))
		case isStructured(in.Params[p]):
			c.add(in.Pos, "instance %s: param %q must be a scalar", in.Name, p)
		case in.Params[p] == nil:
			c.add(in.Pos, "instance %s: param %q is empty: give it a value, or leave it out to take the wiring's default", in.Name, p)
		}
	}
	for _, p := range sortedKeys(w.Params) {
		if _, given := in.Params[p]; !given && w.Params[p].Required() {
			c.add(in.Pos, "instance %s does not give param %q, which wiring %q requires (it has no default)", in.Name, p, w.WiringName())
		}
	}

	tp := c.typePaths(in.Type)
	for _, p := range flatten(in.Set) {
		if !pathRE.MatchString(p) {
			c.add(in.Pos, "instance %s: set: %q is not a member path", in.Name, p)
			continue
		}
		c.inType(w, tp, p, "instance "+in.Name+": set:", in.Pos)
		if k, mode, known := kindOf(w, p); known && k != Memory {
			c.add(in.Pos, "instance %s: set: %s is %s, not memory: set: gives an initial value to a memory member only (%s)", in.Name, p, describe(k, mode), setHint(k))
		}
	}
	for _, p := range sortedKeys(in.Wire) {
		m := in.Wire[p]
		pos := in.Pos
		base, ok := w.Members[p]
		c.inType(w, tp, p, "instance "+in.Name+": wire:", pos)
		for k, v := range m.Addr {
			if s, isStr := v.(string); isStr {
				c.refs(pos, "instance "+in.Name+" wire "+p+" "+k, s, paramSet(w))
			}
		}
		if m.Event {
			c.add(pos, "instance %s: wire: %s: event: belongs to the wiring (a kind's property), not an override", in.Name, p)
		}
		switch {
		case !ok && w.Fill != nil:
		case !ok:
			c.add(pos, "instance %s: wire: %s is not wired by wiring %q: wire: changes an address or source, so the wiring must give the member one%s", in.Name, p, w.WiringName(), didYouMean(p, sortedKeys(w.Members)))
		case base.Kind != From && base.Kind != To && base.Kind != Both:
			c.add(pos, "instance %s: wire: %s is %s: only a from:, to: or both: member has an address to change", in.Name, p, base.Kind)
		case m.Kind != "" && m.Kind != base.Kind:
			c.add(pos, "instance %s: wire: %s would make it %s, but the wiring makes it %s: a kind is the type's, and an override changes an address or source only", in.Name, p, m.Kind, base.Kind)
		case m.Source != "":
			if _, ok := w.Sources[m.Source]; !ok {
				c.add(pos, "instance %s: wire: %s: %q is not a source of wiring %q%s", in.Name, p, m.Source, w.WiringName(), didYouMean(m.Source, sortedKeys(w.Sources)))
			}
		}
	}
	for _, p := range sortedKeys(in.Alarms) {
		if t := in.Alarms[p]; !t.Off && t == (Tuning{}) {
			c.add(in.Pos, "instance %s: alarms: %s is empty: write false to turn the alarm off, or the fields to change (%s)", in.Name, p, strings.Join(TuningKeys, ", "))
		}
		found := false
		for _, a := range w.Alarms {
			found = found || a.Member == p
		}
		if !found {
			c.add(in.Pos, "instance %s: alarms: %s has no alarm in wiring %q to tune", in.Name, p, w.WiringName())
		}
	}
	if o := in.OfflineAlarm; o != nil {
		for _, s := range sortedKeys(o.BySource) {
			if _, ok := w.Sources[s]; !ok {
				c.add(in.Pos, "instance %s: offline-alarm: %q is not a source of wiring %q%s", in.Name, s, w.WiringName(), didYouMean(s, sortedKeys(w.Sources)))
			}
		}
	}
}

// pick resolves the wiring an instance uses.
func (c *checker) pick(in *Instance) *Wiring {
	group := c.byType[ir.NameKey(in.Type)]
	if in.Wiring != "" {
		w, ok := c.wirings[ir.NameKey(in.Wiring)]
		switch {
		case !ok:
			c.add(in.Pos, "instance %s: wiring %q is not declared%s", in.Name, in.Wiring, didYouMean(in.Wiring, wiringNames(c.wirings)))
			return nil
		case ir.NameKey(w.Type) != ir.NameKey(in.Type):
			c.add(in.Pos, "instance %s: wiring %q is for type %s, not %s", in.Name, in.Wiring, w.Type, in.Type)
			return nil
		}
		return w
	}
	switch len(group) {
	case 0:
		c.add(in.Pos, "instance %s: no wiring for type %s: declare one under wirings:", in.Name, in.Type)
		return nil
	case 1:
		return group[0]
	}
	if w, ok := c.wirings[ir.NameKey(in.Type)]; ok && ir.NameKey(w.Type) == ir.NameKey(in.Type) {
		return w
	}
	var names []string
	for _, w := range group {
		names = append(names, w.WiringName())
	}
	c.add(in.Pos, "instance %s: type %s has several wirings (%s) and none is named %s: say which with wiring:", in.Name, in.Type, strings.Join(names, ", "), in.Type)
	return nil
}

// paramSet is the names a substitution may use in w.
func paramSet(w *Wiring) map[string]bool {
	m := map[string]bool{"name": true, "desc": true}
	for p := range w.Params {
		m[p] = true
	}
	return m
}

func setHint(k Kind) string {
	switch k {
	case From, Expr:
		return "a value the device or the expression decides"
	case To:
		return "a command the program sends"
	}
	return "the peer's own memory"
}

func paramNames(w *Wiring) string {
	if len(w.Params) == 0 {
		return "none"
	}
	return strings.Join(sortedKeys(w.Params), ", ")
}

func wiringNames(m map[string]*Wiring) []string {
	var out []string
	for _, w := range m {
		out = append(out, w.WiringName())
	}
	sort.Strings(out)
	return out
}

func isStructured(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// flatten lists the member paths a nested set: gives a value to.
func flatten(m map[string]any) []string {
	var out []string
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for _, k := range sortedKeys(m) {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			if sub, ok := m[k].(map[string]any); ok {
				walk(p, sub)
				continue
			}
			out = append(out, p)
		}
	}
	walk("", m)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func keysOf[V any](m map[string]V) []string { return sortedKeys(m) }

// didYouMean suggests the nearest candidate within two edits.
func didYouMean(name string, candidates []string) string {
	best, bestD := "", 3
	for _, c := range candidates {
		if d := editDistance(strings.ToLower(name), strings.ToLower(c)); d < bestD {
			best, bestD = c, d
		}
	}
	if best == "" {
		return ""
	}
	return " (did you mean " + best + "?)"
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
