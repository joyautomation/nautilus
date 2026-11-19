package devbind

import (
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/joyautomation/nautilus/devprofile"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/iolink"
	"github.com/joyautomation/nautilus/iolink/iodd"
	"github.com/joyautomation/nautilus/lang/ir"
)

// An IO-Link port device (#705, docs/design/iolink.md): a sensor on a
// master's port, delivered as its own struct tag next to the master's.
// Everything it reads comes from the master's points (the iolink: block
// of the master's profile names them), and everything it writes goes
// through them.

// portDev is one port of a master row, resolved.
type portDev struct {
	// Name is the port device's tag; Num its port number.
	Name string
	Num  int
	// Master is the master row's name; prof its profile's iolink: block.
	Master string
	spec   *devprofile.IOLinkSpec
	dev    *iolink.Device
	def    *ir.StructDef
	// Source points of the master, as internal member names (master.Point),
	// "" when the profile has none.
	inM, validM, vendorM, deviceM string
	// Command bindings of the master, as command tag names, "" when none.
	outC, outValidC string
	// cmds maps a command tag (<Port>_<Item>) to its process data out item.
	cmds map[string]int
	// rearm is the port's __Rearm tag, "" for a port with no process data out.
	rearm string
	// isdu is how the port's parameters are reached; nil when the master
	// offers none.
	isdu *isduRefs
}

// nodeRef is a node id with the namespace URI that decides its index on the
// live server ("" when the profile only has a placeholder for it).
type nodeRef struct{ ID, URI string }

// isduRefs are a port's ISDU methods, resolved: the spec, and the node ids
// of the port device's object and of its ReadISDU and WriteISDU (write is
// the zero value when the master has none).
type isduRefs struct {
	spec                *devprofile.ISDUSpec
	object, read, write nodeRef
}

// nodeRef expands a node id template for port n.
func (d *device) nodeRef(tmpl string, n int) nodeRef {
	if tmpl == "" {
		return nodeRef{}
	}
	id := strings.ReplaceAll(d.prof.Substitute(tmpl, d.Params), "{n}", strconv.Itoa(n))
	r := nodeRef{ID: id}
	if ns := devprofile.NodeNamespace(id); ns > 0 {
		if uri := d.prof.Namespace(ns); uri != "" && !devprofile.IsPlaceholderNamespace(uri) {
			r.URI = uri
		}
	}
	return r
}

// structType is the port device's struct type: one member per process data
// in item, as profile points the types renderer and the manifest check
// already take.
func (p *portDev) structType() devprofile.Struct {
	st := devprofile.Struct{Name: p.dev.TypeName}
	for _, m := range p.dev.Members {
		st.Points = append(st.Points, devprofile.Point{Name: m.Name, Type: m.Type, Unit: m.Unit, Desc: m.Desc})
	}
	return st
}

// PortInfo is what a project needs to know of a port device beyond its
// tags: which master and port it is, and what the IODD says it is.
type PortInfo struct {
	Name   string // the port device's tag
	Master string
	Num    int
	Device string // the IODD's device name
}

// Ports are the set's port devices, in row then port order.
func (s *Set) Ports() []PortInfo {
	if s == nil {
		return nil
	}
	var out []PortInfo
	for _, d := range s.devices {
		for _, p := range d.ports {
			out = append(out, PortInfo{Name: p.Name, Master: d.Name, Num: p.Num, Device: p.dev.IODD.Identity.DeviceName})
		}
	}
	return out
}

// IdentityTag and IdentityFaultTag are a port device's companion tags.
func IdentityTag(port string) string { return port + "__Identity" }

// RearmTag is the command a port with process data out is armed by: a
// pulse (false then true) on it, once the device on the port is the right
// one, lets the port send the program's current values. An operator's
// write to it is journalled by the audit journal like any tag write.
func RearmTag(port string) string         { return port + "__Rearm" }
func IdentityFaultTag(port string) string { return port + "__IdentityFault" }

// loadPorts resolves a master row's ports: each port's IODD is read from
// the project's iodd/ folder and derived, and every point the master's
// profile names for the port must be one the row selects.
func (d *device) loadPorts(fsys fs.FS, where string) error {
	spec := d.prof.IOLink
	if d.prof.Protocol != devprofile.ProtoOPCUA {
		return fmt.Errorf("%s: ports: only an OPC UA master has port devices", where)
	}
	selected := map[string]bool{}
	for _, pt := range d.points {
		selected[pt.Name] = true
	}
	nums := make([]int, 0, len(d.Ports))
	for k := range d.Ports {
		nums = append(nums, k)
	}
	sort.Ints(nums)
	for _, k := range nums {
		p := d.Ports[k]
		at := fmt.Sprintf("%s: port %d", where, k)
		if p.IODD == "" {
			return fmt.Errorf("%s: iodd is required: a port device is derived from its IODD (naut iolink import)", at)
		}
		raw, err := fs.ReadFile(fsys, path.Join(IODDDir, p.IODD))
		if err != nil {
			return fmt.Errorf("%s: iodd %s: %w", at, p.IODD, err)
		}
		parsed, err := iodd.Parse(raw)
		if err != nil {
			return fmt.Errorf("%s: iodd %s: %w", at, p.IODD, err)
		}
		dev, err := iolink.Derive(p.IODD, parsed, spec.PDValid != "")
		if err != nil {
			return fmt.Errorf("%s: iodd %s: %w", at, p.IODD, err)
		}
		pd := &portDev{Name: p.Name, Num: k, Master: d.Name, spec: spec, dev: dev, cmds: map[string]int{}}
		need := func(key, tmpl string) (string, error) {
			if tmpl == "" {
				return "", nil
			}
			name := devprofile.PortPoint(tmpl, k)
			if !selected[name] {
				return "", fmt.Errorf("%s: the master's points: leave out %s, which %s names for the port (%s): without it the port cannot be decoded; select it", at, name, "iolink:", key)
			}
			return name, nil
		}
		var pin, pv, vid, did, pout, pov string
		if pin, err = need("pd-in", spec.PDIn.Point); err != nil {
			return err
		}
		if pv, err = need("pd-valid", spec.PDValid); err != nil {
			return err
		}
		if vid, err = need("vendor-id", spec.VendorID); err != nil {
			return err
		}
		if did, err = need("device-id", spec.DeviceID); err != nil {
			return err
		}
		if err := carrierFits(at, "pd-in", spec.PDIn.Form, dev.In.Octets()); err != nil {
			return err
		}
		if dev.Out != nil {
			if spec.PDOut == nil {
				return fmt.Errorf("%s: iodd %s has process data out and %s offers none (its profile has no pd-out)", at, p.IODD, d.prof.ID)
			}
			if pout, err = need("pd-out", spec.PDOut.Point); err != nil {
				return err
			}
			if pov, err = need("pd-out-valid", spec.PDOutValid); err != nil {
				return err
			}
			if err := carrierFits(at, "pd-out", spec.PDOut.Form, dev.Out.Octets()); err != nil {
				return err
			}
			for _, c := range dev.Commands {
				pd.cmds[CommandPortTag(p.Name, c.Name)] = c.Item
			}
			pd.rearm = RearmTag(p.Name)
		}
		pd.inM, pd.validM, pd.vendorM, pd.deviceM = mem(d.Name, pin), mem(d.Name, pv), mem(d.Name, vid), mem(d.Name, did)
		pd.outC, pd.outValidC = cmd(d.Name, pout), cmd(d.Name, pov)
		pd.def = &ir.StructDef{Name: dev.TypeName, FieldIndex: map[string]int{}}
		for i, m := range dev.Members {
			pd.def.Fields = append(pd.def.Fields, ir.StructField{Name: m.Name, Type: irType(m.Type)})
			pd.def.FieldIndex[m.Name] = i
		}
		if is := spec.ISDU; is.Offers() {
			pd.isdu = &isduRefs{spec: is, object: d.nodeRef(is.Object, k), read: d.nodeRef(is.Read, k), write: d.nodeRef(is.Write, k)}
		}
		d.ports = append(d.ports, pd)
	}
	return nil
}

func mem(master, point string) string {
	if point == "" {
		return ""
	}
	return memberName(master, point)
}

func cmd(master, point string) string {
	if point == "" {
		return ""
	}
	return CommandTag(master, point)
}

// carrierFits refuses a u32 carrier for an image longer than four octets:
// an error at load naming the port, the length and the form, not a
// truncated value later.
func carrierFits(at, key, form string, octets int) error {
	if form == devprofile.FormU32 && octets > 4 {
		return fmt.Errorf("%s: the IODD's process data is %d octets and the master's %s carries it as %s, which holds four", at, octets, key, form)
	}
	return nil
}

// CommandPortTag is the output tag a port device's process data out item
// is written through.
func CommandPortTag(port, item string) string { return port + "_" + item }

// owned reports whether a master's point is one a listed port drives
// itself: the process data out point and its valid flag. The master has no
// command tag for them, so a program cannot write the same process data
// from two places.
func (d *device) owned(point string) bool {
	for _, p := range d.ports {
		if p.dev.Out == nil {
			continue
		}
		for _, tmpl := range []string{p.spec.PDOut.Point, p.spec.PDOutValid} {
			if tmpl != "" && devprofile.PortPoint(tmpl, p.Num) == point {
				return true
			}
		}
	}
	return false
}

// portWarnings are what naut check says of ports whose outputs cannot be
// kept from a wrong device (iolink.md §6.2): a master with no id points
// cannot tell one device from another, and one with no pd-out-valid keeps
// sending its last process data out to whatever is on the port.
func portWarnings(ports []*portDev) []string {
	var out []string
	for _, p := range ports {
		if p.vendorM == "" {
			out = append(out, fmt.Sprintf("port device %s (%s port %d) is not identity-checked: its master profile has no vendor-id/device-id points, so a wrong device on the port is not seen", p.Name, p.Master, p.Num))
		}
		if p.dev.Out != nil && p.outValidC == "" {
			out = append(out, fmt.Sprintf("port device %s (%s port %d): its master profile has no pd-out-valid, so a wrong device on the port is sent the master's last process data out; only the identity fault says so", p.Name, p.Master, p.Num))
		}
	}
	return out
}

// ── reading ─────────────────────────────────────────────────────────────

// portIn is the read side of one port: it turns the master's members into
// the port device's struct and its identity.
type portIn struct {
	*portDev
	// gate is the write side's, told the identity each read; nil on a copy
	// (NewAssembly) that has no write side.
	gate *iolink.OutGate
	// The master's last deliveries.
	inV, vendorV, deviceV, validV any
	state                         iolink.State
	checked                       bool // the master has delivered what the check needs
	bad                           bool // the last decode failed, or the master says the data is invalid
	loggedState                   iolink.State
	last                          ir.Value
	boxed                         any
}

func newPortIn(p *portDev, gate *iolink.OutGate) *portIn {
	return &portIn{portDev: p, gate: gate, state: iolink.NoDevice, loggedState: iolink.Ok}
}

// names are the tags the port delivers.
func (p *portIn) names() []string {
	return []string{p.Name, IdentityTag(p.Name), IdentityFaultTag(p.Name)}
}

func u32Val(v any) (uint32, bool) {
	switch x := v.(type) {
	case int64:
		return uint32(x), x >= 0 && x <= 0xFFFFFFFF
	case uint32:
		return x, true
	case float64:
		return uint32(x), x >= 0 && x == float64(uint32(x))
	}
	return 0, false
}

// take reads the port's source members out of vals (it leaves them: the
// master's own struct takes them) and delivers the port device and its two
// companions. A member vals does not carry keeps its last value.
func (p *portIn) take(vals map[string]any) {
	for _, s := range []struct {
		name string
		into *any
	}{{p.inM, &p.inV}, {p.vendorM, &p.vendorV}, {p.deviceM, &p.deviceV}, {p.validM, &p.validV}} {
		if s.name == "" {
			continue
		}
		if v, ok := vals[s.name]; ok {
			*s.into = v
		}
	}
	if p.inV == nil {
		return // nothing from the master yet: the port is absent, NotConnected
	}
	haveIDs := p.vendorM != ""
	var vendor, device uint32
	if haveIDs {
		v, ok1 := u32Val(p.vendorV)
		d, ok2 := u32Val(p.deviceV)
		if p.vendorV == nil || p.deviceV == nil || !ok1 || !ok2 {
			return // the ids have not arrived (or are not numbers): nothing is known yet
		}
		vendor, device = v, d
	}
	state := iolink.Check(p.dev.IODD, haveIDs, vendor, device)
	if p.gate != nil {
		p.gate.SetState(state)
	}
	if state != p.state || !p.checked {
		if state.Fault() && state != p.loggedState {
			slog.Warn("iolink: the device on a port is not the one expected",
				"port", p.Name, "master", p.Master, "number", p.Num, "state", state.String(),
				"expectedVendor", p.dev.IODD.Identity.VendorID, "expectedDevice", p.dev.IODD.Identity.DeviceID,
				"readVendor", vendor, "readDevice", device)
		}
		p.loggedState = state
	}
	p.state, p.checked = state, true
	// A port that can be armed and has not been reads AwaitingRearm: the
	// right device, nothing sent, and the fault stays active so the
	// operator sees why.
	shown := state
	if p.gate != nil && state.Drives() && !p.gate.Armed() {
		shown = iolink.AwaitingRearm
	}
	vals[IdentityTag(p.Name)] = int64(shown)
	vals[IdentityFaultTag(p.Name)] = shown.Fault()
	p.bad = false
	if !state.Drives() {
		p.bad = true // the bytes are not decoded: whatever is on the port is not the device
		p.deliver(vals)
		return
	}
	valid := true
	if p.validM != "" {
		valid, _ = p.validV.(bool)
	}
	members, err := p.dev.Decode(p.spec.PDIn.Form, p.inV, valid)
	if err != nil {
		p.bad = true // a decode fault: Bad, and the last good struct stays
		p.deliver(vals)
		return
	}
	p.bad = !valid
	next := ir.Value{Kind: ir.TypeStruct, Struct: p.def, Fld: make([]ir.Value, len(p.def.Fields))}
	for i, m := range members {
		iv, ok := toIR(m, p.def.Fields[i].Type)
		if !ok {
			iv = ir.Zero(p.def.Fields[i].Type)
		}
		next.Fld[i] = iv
	}
	p.last, p.boxed = next, next
	p.deliver(vals)
}

func (p *portIn) deliver(vals map[string]any) {
	if p.last.Kind != 0 {
		vals[p.Name] = p.boxed
	}
}

// copyFresh is a port reader with decode state of its own that feeds the
// same gate: virtdev's in-process face reads through copies, and the gate
// must see the identity they check.
func (p *portIn) copyFresh() *portIn { return newPortIn(p.portDev, p.gate) }

// quality is the port device's quality (§6.1): NotConnected until the
// master's process data has arrived once; Bad while the identity is wrong
// or missing, the master says the data is invalid, or a decode failed;
// else what the master's members say.
func (p *portIn) quality(in map[string]nio.Quality) (nio.Quality, bool) {
	names := []string{p.inM, p.vendorM, p.deviceM, p.validM}
	polled, notConn, stale, bad := 0, 0, false, false
	for _, m := range names {
		if m == "" {
			continue
		}
		polled++
		switch in[m] {
		case nio.Good:
		case nio.NotConnected:
			notConn++
		case nio.Stale:
			stale = true
		default:
			bad = true
		}
	}
	switch {
	case polled == 0, !p.checked && notConn == polled:
		return nio.NotConnected, true
	case !p.checked || bad || notConn > 0 || p.bad:
		return nio.Bad, true
	case stale:
		return nio.Stale, true
	}
	return nio.Good, false // Good is the absence of an entry
}

// ── the set of a master's ports ──────────────────────────────────────────

// ports is a master driver's port devices: the read side of each, and the
// write side that gates their process data out.
type ports struct {
	mu  sync.Mutex
	in  []*portIn
	out []*portOut
}

func newPorts(devs []*portDev) *ports {
	ps := &ports{}
	for _, d := range devs {
		po := newPortOut(d)
		ps.out = append(ps.out, po)
		var gate *iolink.OutGate
		if po != nil {
			gate = po.gate
		}
		ps.in = append(ps.in, newPortIn(d, gate))
	}
	return ps
}

func (ps *ports) names() []string {
	var out []string
	for _, p := range ps.in {
		out = append(out, p.names()...)
	}
	return out
}

func (ps *ports) take(vals map[string]any) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, p := range ps.in {
		p.take(vals)
	}
}

func (ps *ports) clear(dst nio.Values) {
	for _, n := range ps.names() {
		delete(dst, n)
	}
}

func (ps *ports) newAssembly() func(nio.Values) {
	fresh := make([]*portIn, len(ps.in))
	for i, p := range ps.in {
		fresh[i] = p.copyFresh()
	}
	return func(vals nio.Values) {
		ps.mu.Lock()
		defer ps.mu.Unlock()
		for _, p := range fresh {
			p.take(vals)
		}
	}
}

func (ps *ports) inputType(name string) (string, bool) {
	for _, p := range ps.in {
		if p.Name == name {
			return p.def.Name, true
		}
	}
	return "", false
}

func (ps *ports) quality(in map[string]nio.Quality, out map[string]nio.Quality) map[string]nio.Quality {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, p := range ps.in {
		if q, listed := p.quality(in); listed {
			if out == nil {
				out = map[string]nio.Quality{}
			}
			out[p.Name] = q
		}
	}
	return out
}

// transform is what the master's driver is handed for what the runtime
// pushed: the ports' command tags (and __Rearm) taken out, and each port's
// gated process data out and valid flag put in as the master's own command
// bindings. A refused value or re-arm is the returned error; everything
// else is still in the result.
func (ps *ports) transform(vals nio.Values) (nio.Values, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	into := make(nio.Values, len(vals)+len(ps.out))
	for k, v := range vals {
		into[k] = v
	}
	for _, o := range ps.out {
		if o == nil {
			continue
		}
		for tag := range o.cmds {
			delete(into, tag)
		}
		delete(into, o.rearm)
	}
	var first error
	for _, o := range ps.out {
		if o == nil {
			continue
		}
		if err := o.push(vals, into); err != nil && first == nil {
			first = err
		}
	}
	return into, first
}

// write hands the master's driver what transform makes of a push; a
// refused value is the returned error after everything else was pushed.
func (ps *ports) write(drv nio.Driver, vals nio.Values) error {
	into, first := ps.transform(vals)
	if err := drv.WriteOutputs(into); err != nil {
		return err
	}
	return first
}

// ── writing ─────────────────────────────────────────────────────────────

// portOut is the write side of one port with process data out: the
// program sets command tags and pulses __Rearm, and the gate decides what
// reaches the master.
type portOut struct {
	*portDev
	gate *iolink.OutGate
	// ref is each item's value last handed to the gate, so an unchanged
	// push costs nothing; nil before the first push.
	ref []any
	// rearmSeen and rearmPrev detect the pulse: the first push is only the
	// state of the world, a pulse is a later push that has it true after one
	// that had it false.
	rearmSeen, rearmPrev bool
	started              bool // a push has been seen: see push
	pending              bool // the image is going out this call: the valid flag follows the next
	valid                bool // the valid flag last handed to the master
}

func newPortOut(p *portDev) *portOut {
	if p.dev.Out == nil {
		return nil
	}
	return &portOut{portDev: p, gate: iolink.NewOutGate(p.dev.Out, iolink.NoDevice)}
}

// push takes the values the runtime pushed and puts into what to hand the
// master's driver for this port: its process data out image when the gate
// has one to send, and the valid flag.
func (o *portOut) push(vals nio.Values, into nio.Values) error {
	var first error
	fail := func(err error) {
		if first == nil {
			first = err
		}
	}
	if o.ref == nil {
		o.ref = make([]any, len(o.dev.Commands))
	}
	for tag, item := range o.cmds {
		v := vals[tag]
		if sameValue(o.ref[item], v) {
			continue
		}
		if err := o.gate.Set(item, v); err != nil {
			fail(fmt.Errorf("%s: %w", tag, err))
			continue
		}
		o.ref[item] = v
	}
	rv, _ := vals[o.rearm].(bool)
	if o.rearmSeen && rv && !o.rearmPrev {
		if err := o.gate.Rearm(); err != nil {
			slog.Warn("iolink: re-arm refused", "port", o.Name, "master", o.Master, "number", o.Num, "reason", err.Error())
			fail(fmt.Errorf("%s: %w", o.rearm, err))
		} else {
			slog.Info("iolink: port re-armed: it sends the program's current values", "port", o.Name, "master", o.Master, "number", o.Num)
		}
	}
	o.rearmSeen, o.rearmPrev = true, rv
	plan, err := o.gate.Plan()
	if err != nil {
		fail(fmt.Errorf("%s: %w", o.Name, err))
	}
	// The master's driver takes the FIRST push after it starts as the state
	// of the world, records it and never sends it (its start-up baseline), so
	// an image planned on this push would be lost. A port armed by its first
	// check (a power-up, a takeover) has one ready now: it goes with the
	// next push, a scan later.
	firstPush := !o.started
	o.started = true
	if plan.Image != nil && o.outC != "" && !firstPush {
		carrier, err := iolink.Carrier(o.spec.PDOut.Form, plan.Image)
		if err != nil {
			fail(fmt.Errorf("%s: %w", o.Name, err))
		} else {
			into[o.outC] = carrier
		}
	}
	if o.outValidC != "" {
		// The image first, the flag one push later: the master must hold
		// the new image before it is told the image is valid.
		switch {
		case !plan.Valid:
			o.pending, o.valid = false, false
		case firstPush:
			// the image is held back (above): the flag waits for it
		case o.valid:
		case o.pending:
			o.valid = true
		default:
			o.pending = true
		}
		into[o.outValidC] = o.valid
	}
	return first
}

func (o *portOut) portTag(item int) string {
	for tag, i := range o.cmds {
		if i == item {
			return tag
		}
	}
	return o.Name
}

func sameValue(a, b any) bool { return a == b }
