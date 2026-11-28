// Package wirebind turns checked struct wiring into what the drivers and the
// runtime run (docs/design/one-struct-model.md, #1458): each instance with
// its params substituted, its per-instance overrides applied and each
// member's kind, and, for each driver, the manifest its own file would
// decode to (modbus.go). The drivers never learn of it: a Modbus instance is
// a source and a binding per member, named by the member's full path
// ("P101.Speed", "P101.Cmd.Start"), which is the key the runtime delivers
// and pushes under.
//
// The manifest composes it (internal/project): it hands Resolve the files
// wiring.Check has passed and the project's TYPEs, and builds the instance
// tags itself, since this package never reaches the runtime.
//
// What it binds grows slice by slice (§18). Slice 5 binds Modbus: from: and
// to: members, memory members, read-backs and event: commands, an instance
// reading one connection of its own. The rest is refused here, naming the
// slice it arrives with, never run half-way.
package wirebind

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/wiring"
)

// Set is a project's instances, resolved.
type Set struct {
	Instances []*Instance
}

// Instance is one instance, ready to run.
type Instance struct {
	Name   string
	Type   string // the TYPE, as the project declares it
	Wiring string // the wiring it uses
	Desc   string
	Pos    wiring.Pos
	// Members are the wiring's entries, in path order, each with the
	// instance's overrides applied. A member no entry names is memory.
	Members []Member
	// Conns are the connections the instance reads and writes, one per
	// source its members use.
	Conns []Conn
	// Implicit are the member paths no entry names: retained memory
	// (design §1), which naut check lists.
	Implicit []string
}

// Wired says whether any member is filled or sent by a driver (from:, to:,
// both:) or computed (expr:): what a vendor writer cannot carry yet.
func (in *Instance) Wired() bool {
	for _, m := range in.Members {
		if m.Kind != wiring.Memory {
			return true
		}
	}
	return false
}

// Retained lists the member paths that are retained memory: the implicit
// ones and those written memory: retained.
func (in *Instance) Retained() []string {
	out := append([]string(nil), in.Implicit...)
	for _, m := range in.Members {
		if m.Kind == wiring.Memory && m.MemoryMode == wiring.Retained {
			out = append(out, m.Path)
		}
	}
	sort.Strings(out)
	return out
}

// Member is one wiring entry of one instance.
type Member struct {
	// Path is the member's path as the TYPE spells it ("Cmd.Start").
	Path       string
	Kind       wiring.Kind
	MemoryMode string
	// Readback is the read-back's path, as the TYPE spells it.
	Readback string
	Event    bool
	Unit     string
	Desc     string
	// Source names the connection (Conn.Source) of a from:, to: or both:.
	Source string
	// Addr are the driver's own keys, params substituted.
	Addr map[string]any
	// Expr is an expr: member's expression, params substituted (§6). The
	// project checks it (internal/project wiring_expr.go).
	Expr string
	// Type is the member's type in the TYPE.
	Type *ir.Type
	Pos  wiring.Pos
}

// InstancesList is the set's instances; none for a nil set.
func (s *Set) InstancesList() []*Instance {
	if s == nil {
		return nil
	}
	return s.Instances
}

// Key is the member's delivery and push key: its full path, the spelling
// the runtime uses ("P101.Cmd.Start", "P101.Levels[1]").
func (in *Instance) Key(m Member) string {
	if strings.HasPrefix(m.Path, "[") {
		return in.Name + m.Path
	}
	return in.Name + "." + m.Path
}

// Conn is one connection an instance uses.
type Conn struct {
	// Source is the wiring's source name.
	Source string
	// Driver is the source's driver: as written (a drivers: entry or a
	// type), DriverType what it resolved to.
	Driver     string
	DriverType string
	// ID names the connection: the instance's name when it is the
	// instance's only one, so its companions are <Instance>__Online (and
	// __Outages on Modbus), as a devices: row's are.
	ID string
	// Keys are the source's keys, params substituted.
	Keys map[string]any
	Pos  wiring.Pos
}

// Online is the connection's companion that says the device answers.
func (c Conn) Online() string { return c.ID + "__Online" }

// Outages is a Modbus connection's companion that counts its outages.
func (c Conn) Outages() string { return c.ID + "__Outages" }

// Env is what Resolve learns from the manifest.
type Env struct {
	// Types is the project's TYPE table (st.Types), keyed by spelling.
	Types map[string]*ir.Type
	// Driver resolves a source's driver: to its driver type.
	Driver func(name string) (typ string, ok bool)
}

// Resolve builds the set from files that wiring.Check has passed (with the
// project's types). It refuses what this build does not run yet, naming the
// slice of §18 that brings it, and every problem is reported at once.
func Resolve(files []*wiring.File, env Env) (*Set, []wiring.Problem) {
	r := resolver{env: env, wirings: map[string]*wiring.Wiring{}, byType: map[string][]*wiring.Wiring{}}
	for _, f := range files {
		for i := range f.Wirings {
			w := &f.Wirings[i]
			r.wirings[ir.NameKey(w.WiringName())] = w
			r.byType[ir.NameKey(w.Type)] = append(r.byType[ir.NameKey(w.Type)], w)
		}
	}
	for _, f := range files {
		for i := range f.Wirings {
			r.fence(&f.Wirings[i])
		}
	}
	set := &Set{}
	for _, f := range files {
		for i := range f.Instances {
			if in := r.instance(&f.Instances[i]); in != nil {
				set.Instances = append(set.Instances, in)
			}
		}
	}
	return set, r.out
}

type resolver struct {
	env     Env
	wirings map[string]*wiring.Wiring
	byType  map[string][]*wiring.Wiring
	out     []wiring.Problem
}

func (r *resolver) add(pos wiring.Pos, format string, a ...any) {
	r.out = append(r.out, wiring.Problem{Pos: pos, Msg: fmt.Sprintf(format, a...)})
}

// fence refuses a wiring's parts that a later slice runs.
func (r *resolver) fence(w *wiring.Wiring) {
	name := w.WiringName()
	if w.Fill != nil {
		r.add(w.Pos, "wiring %q: fill: (a type a peer driver fills whole) arrives with slice 12 of struct wiring (#1458); this build does not run it yet", name)
	}
	if len(w.Alarms) > 0 {
		r.add(w.Pos, "wiring %q: alarms: in a wiring arrive with slice 9 of struct wiring (#1458); until then declare the alarm in the project's alarms: on the member's path (%s.<member>)", name, "<instance>")
	}
	for _, s := range sortedKeys(w.Sources) {
		typ, _ := r.driverType(w.Sources[s].Driver)
		if typ != "modbus" {
			r.add(w.Pos, "wiring %q: source %q uses the %s driver: this build runs struct wiring on Modbus only (#1458 slice 5); %s", name, s, typ, laterSlice(typ))
		}
	}
	for _, p := range sortedKeys(w.Members) {
		m := w.Members[p]
		pos := wiring.Pos{File: w.Pos.File, Line: m.Line}
		if m.Line == 0 {
			pos = w.Pos
		}
		switch m.Kind {
		case wiring.Both:
			// wiring.Check refuses both: on a field device, and every
			// source this build runs is one; this is the last line.
			r.add(pos, "wiring %q: member %q: both: is for a peer controller, which this build does not wire yet (#1458 slices 12 and 14)", name, p)
		}
	}
}

// laterSlice says when a driver type's wiring arrives.
func laterSlice(typ string) string {
	switch typ {
	case "opcua-client":
		return "OPC UA sources arrive with slice 9"
	case "eip":
		return "EtherNet/IP arrives with slice 12"
	case "sparkplug-host":
		return "the Sparkplug host arrives with slice 14"
	case "snmp", "redfish", "prometheus":
		return "the IT drivers arrive with slice 15"
	}
	return "other drivers arrive in later slices"
}

func (r *resolver) driverType(drv string) (string, bool) {
	if r.env.Driver != nil {
		if t, ok := r.env.Driver(drv); ok {
			return t, true
		}
	}
	t := strings.ToLower(drv)
	return t, wiring.IsDriverType(t)
}

// pick is the wiring an instance uses (wiring.Check has resolved it once).
func (r *resolver) pick(in *wiring.Instance) *wiring.Wiring {
	if in.Wiring != "" {
		return r.wirings[ir.NameKey(in.Wiring)]
	}
	group := r.byType[ir.NameKey(in.Type)]
	if len(group) == 1 {
		return group[0]
	}
	return r.wirings[ir.NameKey(in.Type)]
}

func (r *resolver) instance(in *wiring.Instance) *Instance {
	w := r.pick(in)
	if w == nil {
		r.add(in.Pos, "instance %s: no wiring for type %s", in.Name, in.Type)
		return nil
	}
	typ, typName, ok := ir.Lookup(r.env.Types, in.Type)
	if !ok || typ == nil || typ.Kind != ir.TypeStruct || typ.Struct == nil {
		r.add(in.Pos, "instance %s: type %s is not a STRUCT the project declares: an instance is a struct whose members are wired", in.Name, in.Type)
		return nil
	}
	if len(in.Set) > 0 {
		r.add(in.Pos, "instance %s: set: (initial values for memory members) arrives with slice 6 of struct wiring (#1458); until then give the member an initial value in the TYPE", in.Name)
	}
	if len(in.Alarms) > 0 {
		r.add(in.Pos, "instance %s: alarms: (tuning the wiring's alarms) arrives with slice 9 of struct wiring (#1458)", in.Name)
	}
	if in.OfflineAlarm != nil {
		r.add(in.Pos, "instance %s: offline-alarm: arrives with slice 9 of struct wiring (#1458), with the comms-loss alarm per connection", in.Name)
	}
	out := &Instance{Name: in.Name, Type: typName, Wiring: w.WiringName(), Desc: in.Desc, Pos: in.Pos}
	vals := r.params(in, w)
	subst := func(v any) any { return substitute(v, vals) }

	used := map[string]bool{}
	seen := map[string]string{} // canonical path -> the entry that named it
	for _, p := range sortedKeys(w.Members) {
		e := w.Members[p]
		pos := wiring.Pos{File: w.Pos.File, Line: e.Line}
		if e.Line == 0 {
			pos = w.Pos
		}
		if o, ok := in.Wire[p]; ok {
			if o.Readback != "" || o.Unit != "" || o.Desc != "" {
				r.add(in.Pos, "instance %s: wire: %s may change an address or a source only; readback:, unit: and desc: are the wiring's", in.Name, p)
			}
			if o.Source != "" {
				e.Source = o.Source
			}
			addr := map[string]any{}
			for k, v := range e.Addr {
				addr[k] = v
			}
			for k, v := range o.Addr {
				addr[k] = v
			}
			e.Addr = addr
			pos = in.Pos
		}
		path, mt, err := canonical(typ, p)
		if err != nil {
			r.add(pos, "instance %s: member %q: %v", in.Name, p, err)
			continue
		}
		if prev, dup := seen[path]; dup {
			// Two entries for one member ("Levels[01]" and "Levels[1]"):
			// one kind would win and the other's binding go nowhere.
			r.add(pos, "wiring %q: members %q and %q are one member, %s: a member has one entry", w.WiringName(), prev, p, path)
			continue
		}
		seen[path] = p
		m := Member{Path: path, Kind: e.Kind, MemoryMode: e.MemoryMode, Event: e.Event, Unit: e.Unit, Desc: e.Desc, Source: e.Source, Type: mt, Pos: pos}
		if e.Event && mt.Kind != ir.TypeBool {
			// A trigger's rest is FALSE: what a driver may still send after
			// an outage (its release). A number has no value that is always
			// safe to send late (0 may be an action of its own, and an
			// offset moves it on the wire).
			r.add(pos, "instance %s: member %q is %s and event: true: an event command is a BOOL (a start, a reset), whose release FALSE is safe to send late; make the member BOOL, or drop event: to make it a level command", in.Name, p, mt)
		}
		if e.Readback != "" {
			if rb, _, err := canonical(typ, e.Readback); err == nil {
				m.Readback = rb
			} else {
				r.add(pos, "instance %s: member %q: readback %q: %v", in.Name, p, e.Readback, err)
			}
		}
		if s, ok := subst(e.Expr).(string); ok {
			m.Expr = s
		}
		if s, ok := subst(e.Unit).(string); ok {
			m.Unit = s
		}
		if s, ok := subst(e.Desc).(string); ok {
			m.Desc = s
		}
		if len(e.Addr) > 0 {
			m.Addr = map[string]any{}
			for k, v := range e.Addr {
				m.Addr[k] = subst(v)
			}
		}
		if e.Kind == wiring.From || e.Kind == wiring.To || e.Kind == wiring.Both {
			used[e.Source] = true
		}
		out.Members = append(out.Members, m)
	}

	out.Implicit = implicitMemory(out.Members, typ)

	var srcs []string
	for s := range used {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)
	if len(srcs) > 1 {
		r.add(in.Pos, "instance %s reads %d connections (%s): an instance on more than one connection arrives with slice 9 of struct wiring (#1458, mixed sources), with a companion per connection; until then give each its own type, or one source", in.Name, len(srcs), strings.Join(srcs, ", "))
	}
	for _, s := range srcs {
		src := w.Sources[s]
		typ, _ := r.driverType(src.Driver)
		c := Conn{Source: s, Driver: src.Driver, DriverType: typ, ID: in.Name, Keys: map[string]any{}, Pos: in.Pos}
		if len(srcs) > 1 {
			c.ID = in.Name + "_" + s
		}
		for k, v := range src.Keys {
			c.Keys[k] = subst(v)
		}
		out.Conns = append(out.Conns, c)
	}
	return out
}

// params are an instance's param values: its own, else the wiring's
// defaults, with the built-in {name} and {desc}.
func (r *resolver) params(in *wiring.Instance, w *wiring.Wiring) map[string]any {
	vals := map[string]any{"name": in.Name, "desc": in.Desc}
	for k, p := range w.Params {
		if p.Default != nil {
			vals[k] = p.Default
		}
	}
	for k, v := range in.Params {
		vals[k] = v
	}
	return vals
}

// substitute fills each {param} in a text value. The result is text, which
// the driver's side parses as the key's own type (design §4: address:
// "{reg}" with reg: 40 is the register 40).
func substitute(v any, vals map[string]any) any {
	s, ok := v.(string)
	if !ok || !strings.Contains(s, "{") {
		return v
	}
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			b.WriteString(s)
			break
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		name := s[i+1 : i+j]
		if pv, ok := vals[name]; ok {
			b.WriteString(fmt.Sprint(pv))
		} else {
			b.WriteString(s[i : i+j+1])
		}
		s = s[i+j+1:]
	}
	return b.String()
}

// canonical resolves a wiring path in t and spells it as the TYPE does,
// with each index as its plain number: the spelling the runtime's keys use.
func canonical(t *ir.Type, path string) (string, *ir.Type, error) {
	var b strings.Builder
	cur := t
	for _, seg := range ir.PathSegments(path) {
		if strings.HasPrefix(seg, "[") {
			if cur.Kind != ir.TypeArray || cur.Elem == nil {
				return "", nil, fmt.Errorf("%s is not an array", strings.TrimPrefix(b.String(), "."))
			}
			n, err := strconv.Atoi(strings.TrimSpace(strings.Trim(seg, "[]")))
			if err != nil || n < cur.ArrLoBound || n >= cur.ArrLoBound+cur.ArrLen {
				return "", nil, fmt.Errorf("index %s is outside %s's bounds [%d..%d]", seg, strings.TrimPrefix(b.String(), "."), cur.ArrLoBound, cur.ArrLoBound+cur.ArrLen-1)
			}
			fmt.Fprintf(&b, "[%d]", n)
			cur = cur.Elem
			continue
		}
		if cur.Kind != ir.TypeStruct || cur.Struct == nil {
			return "", nil, fmt.Errorf("%s has no members", strings.TrimPrefix(b.String(), "."))
		}
		found := false
		for _, f := range cur.Struct.Fields {
			if ir.SameName(f.Name, seg) {
				b.WriteString("." + f.Name)
				cur, found = f.Type, true
				break
			}
		}
		if !found {
			return "", nil, fmt.Errorf("type %s has no member %s", cur.Struct.Name, seg)
		}
	}
	return strings.TrimPrefix(b.String(), "."), cur, nil
}

// implicitMemory lists the members of t no entry names: they are retained
// memory (design §1). A struct an entry names whole is its entry's.
func implicitMemory(members []Member, t *ir.Type) []string {
	listed := map[string]bool{}
	for _, m := range members {
		listed[m.Path] = true
	}
	var out []string
	var walk func(prefix string, t *ir.Type)
	walk = func(prefix string, t *ir.Type) {
		if listed[prefix] {
			return
		}
		if t.Kind != ir.TypeStruct || t.Struct == nil {
			if prefix != "" {
				out = append(out, prefix)
			}
			return
		}
		for _, f := range t.Struct.Fields {
			p := f.Name
			if prefix != "" {
				p = prefix + "." + f.Name
			}
			walk(p, f.Type)
		}
	}
	walk("", t)
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
