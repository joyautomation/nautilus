package virtdev

import (
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/devprofile"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/slave"
	"github.com/joyautomation/nautilus/opcua"
	"github.com/joyautomation/nautilus/runtime"
)

// InProcess is the in-process face: an io.Driver that stands in for a
// project's own drivers in `naut test`, answering their bindings from the
// virtual devices with no socket, while the plant steps on the test's
// virtual clock.
//
// It reads through the project's drivers as they were configured, so what
// a test proves is the project's register map and node ids, not just its
// logic: a Modbus driver's planned block reads go through the device's
// unit as requests (slave.Unit.Handle), so a wrong address, table or
// span answers exception 02 as it would on the wire, and each binding is
// decoded with its source's word and byte order and its scale; an OPC UA
// binding resolves its node id (by namespace URI when it names one, as the
// driver does at connect) against the device's mapped address space. A
// binding no device answers is a fault: Err reports it and the test fails.
//
// Each driver's companions are delivered as the real one delivers them: a
// Modbus source's __Online (true while every block answered) and
// __Outages, an OPC UA driver's __Online.
type InProcess struct {
	set   *Set
	plant *Plant
	prt   *runtime.Runtime
	sched *acceptance.Scheduler
	clock runtime.Clock

	mb  []*mbFace
	ua  []*uaFace
	ins []string
	// status: the project's runtime status drivers (runtime.StatsReader,
	// #850). No device answers them; they deliver their own values.
	status []nio.Driver

	mu     sync.Mutex
	faults []string
	seen   map[string]bool
}

type mbFace struct {
	drv *modbus.Driver
	// baselined is set by the first WriteOutputs, which the driver records
	// and never sends (its start-up baseline: a controller coming up does
	// not stomp the field with its initial values); written is what was
	// last accounted for, so only changes go out after it.
	baselined bool
	written   map[string]any
	plan      modbus.Plan
	sources   map[string]*mbSource // by source id
	outs      map[string]modbus.TagBinding
	enables   map[string][]*mbSource
}

type mbSource struct {
	src     modbus.Source
	unit    *slave.Unit
	device  string
	enabled bool
	online  bool
}

type uaFace struct {
	drv    *opcua.Driver
	device string
	online string
	nodes  map[string]uaNode // binding name -> what answers it
}

type uaNode struct {
	b        opcua.TagBinding
	tag      string // the plant tag, "" when no node answers
	writable bool
	why      string // why tag is ""
}

// NewInProcess builds a fresh plant for one test and binds the project's
// drivers to its devices. clock is the test's virtual clock: the plant is
// stepped to it before every read. A driver kind with no in-process face,
// or an endpoint no device answers, is an error here, before any scan.
func NewInProcess(s *Set, clock runtime.Clock, drivers []nio.NamedDriver) (*InProcess, error) {
	plant, err := NewPlant(s)
	if err != nil {
		return nil, err
	}
	prt, sched, err := acceptance.NewRuntime(plant.Options())
	if err != nil {
		return nil, fmt.Errorf("plant: %w", err)
	}
	ip := &InProcess{set: s, plant: plant, prt: prt, sched: sched, clock: clock, seen: map[string]bool{}}
	for _, nd := range drivers {
		switch d := nd.Driver.(type) {
		case *modbus.Driver:
			f, err := ip.bindModbus(d)
			if err != nil {
				return nil, fmt.Errorf("driver %s: %w", nd.Name, err)
			}
			ip.mb = append(ip.mb, f)
		case *opcua.Driver:
			f, err := ip.bindOPCUA(d)
			if err != nil {
				return nil, fmt.Errorf("driver %s: %w", nd.Name, err)
			}
			ip.ua = append(ip.ua, f)
		case runtime.StatsReader:
			// The runtime's own health, not a device's: kept as it is.
			ip.status = append(ip.status, nd.Driver)
		default:
			return nil, fmt.Errorf("driver %s (%T): virtual devices answer modbus and opcua-client drivers only", nd.Name, nd.Driver)
		}
		if in, ok := nd.Driver.(interface{ InputNames() []string }); ok {
			ip.ins = append(ip.ins, in.InputNames()...)
		}
	}
	if len(ip.mb)+len(ip.ua) == 0 {
		return nil, errors.New("the project has no modbus or opcua-client driver for its virtual devices to answer")
	}
	return ip, nil
}

// Inputs are the names the project's drivers deliver: a test's `given`
// may not set them, the devices do.
func (ip *InProcess) Inputs() []string { return append([]string(nil), ip.ins...) }

// Plant is the plant runtime, for a test that inspects the process.
func (ip *InProcess) Plant() *runtime.Runtime { return ip.prt }

func (ip *InProcess) bindModbus(d *modbus.Driver) (*mbFace, error) {
	m := d.Manifest()
	f := &mbFace{drv: d, plan: d.Plan(), sources: map[string]*mbSource{}, outs: map[string]modbus.TagBinding{}, enables: map[string][]*mbSource{}, written: map[string]any{}}
	for _, src := range m.Sources {
		inst, ok := ip.modbusDevice(src.Addr(), src.UnitID)
		if !ok {
			return nil, fmt.Errorf("source %s (%s unit %d): no virtual device in %s answers there", src.ID, src.Addr(), src.UnitID, FileName)
		}
		ms := &mbSource{src: src, unit: ip.plant.stores[strings.ToLower(inst)].(*modbusStore).unit, device: inst, enabled: true}
		f.sources[src.ID] = ms
		if src.Enable != "" {
			f.enables[src.Enable] = append(f.enables[src.Enable], ms)
		}
	}
	for _, t := range m.Tags {
		if t.Writable {
			f.outs[t.Name] = t
		}
	}
	return f, nil
}

// modbusDevice finds the Modbus device answering at addr and unit.
func (ip *InProcess) modbusDevice(addr string, unit uint8) (string, bool) {
	for _, d := range ip.set.Devices {
		p := ip.set.Profile(d.Name)
		if p.Protocol != devprofile.ProtoModbus {
			continue
		}
		if ep, _ := d.modbusEndpoint(p); ep == addr && d.Unit(p) == unit {
			return d.Name, true
		}
	}
	return "", false
}

func (ip *InProcess) bindOPCUA(d *opcua.Driver) (*uaFace, error) {
	var inst *Instance
	for i := range ip.set.Devices {
		dv := &ip.set.Devices[i]
		p := ip.set.Profile(dv.Name)
		if p.Protocol != devprofile.ProtoOPCUA {
			continue
		}
		if ep, _ := opcuaEndpoint(*dv, p); ep != "" && sameEndpoint(ep, d.Endpoint()) {
			inst = dv
		}
	}
	if inst == nil {
		return nil, fmt.Errorf("endpoint %s: no virtual device in %s answers there", d.Endpoint(), FileName)
	}
	p := ip.set.Profile(inst.Name)
	mapped, writable, err := ip.set.Mapped(*inst)
	if err != nil {
		return nil, err
	}
	byID := map[string]string{}
	for _, n := range mapped.Nodes {
		if n.Tag == "" {
			continue
		}
		if nid, err := ua.ParseNodeID(n.NodeID); err == nil {
			byID[nid.String()] = n.Tag
		}
	}
	open := map[string]bool{}
	for _, t := range writable {
		open[t] = true
	}
	f := &uaFace{drv: d, device: inst.Name, online: d.OnlineTagName(), nodes: map[string]uaNode{}}
	for _, b := range d.Manifest().Tags {
		n := uaNode{b: b}
		nid, err := ua.ParseNodeID(b.NodeID)
		switch {
		case err != nil:
			n.why = fmt.Sprintf("node id %q: %v", b.NodeID, err)
		case b.Namespace != "":
			// The driver resolves the URI against the server's live
			// namespace array at connect; so does this.
			idx := -1
			for i, uri := range p.OPCUA.Namespaces {
				if uri == b.Namespace {
					idx = i + 1
				}
			}
			if idx < 0 {
				n.why = fmt.Sprintf("namespace %s is not in %s's namespace array %v", b.Namespace, inst.Name, p.OPCUA.Namespaces)
				break
			}
			nid.SetNamespace(uint16(idx))
			fallthrough
		default:
			if n.why != "" {
				break
			}
			tag, ok := byID[nid.String()]
			if !ok {
				n.why = fmt.Sprintf("node %s: %s serves no such node", nid, inst.Name)
				break
			}
			n.tag, n.writable = tag, open[tag]
		}
		f.nodes[b.Name] = n
	}
	return f, nil
}

// sameEndpoint compares two opc.tcp URLs by host and port.
func sameEndpoint(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimPrefix(strings.ToLower(s), "opc.tcp://")
		if i := strings.Index(s, "/"); i >= 0 {
			s = s[:i]
		}
		return s
	}
	return norm(a) == norm(b)
}

// step brings the plant to the test's virtual time.
func (ip *InProcess) step() {
	target := ip.clock.Now().Sub(acceptance.Epoch)
	if d := target - ip.sched.Elapsed(); d > 0 {
		ip.sched.Advance(d)
	}
}

// fault records a protocol fault once per message.
func (ip *InProcess) fault(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	ip.mu.Lock()
	defer ip.mu.Unlock()
	if !ip.seen[msg] {
		ip.seen[msg] = true
		ip.faults = append(ip.faults, msg)
	}
}

// Err is every protocol fault seen so far: a block a device refused, a
// binding no device answers, a write refused. Nil when there were none.
func (ip *InProcess) Err() error {
	ip.mu.Lock()
	defer ip.mu.Unlock()
	if len(ip.faults) == 0 {
		return nil
	}
	// Sorted: the drivers' bindings are visited in map order, and a test's
	// failure text must read the same on every run.
	msgs := append([]string(nil), ip.faults...)
	sort.Strings(msgs)
	return errors.New(strings.Join(msgs, "; "))
}

// ReadInputs steps the plant to now and reads every binding the project's
// drivers poll.
func (ip *InProcess) ReadInputs() (nio.Values, error) {
	ip.step()
	out := nio.Values{}
	for _, f := range ip.mb {
		ip.readModbus(f, out)
	}
	for _, f := range ip.ua {
		ip.readOPCUA(f, out)
	}
	for _, d := range ip.status {
		v, err := d.ReadInputs()
		if err != nil {
			return nil, err
		}
		for k, x := range v {
			out[k] = x
		}
	}
	return out, nil
}

func (ip *InProcess) readModbus(f *mbFace, out nio.Values) {
	for _, ms := range f.sources {
		ms.online = ms.enabled
	}
	for _, blk := range f.plan.Blocks {
		ms := f.sources[blk.Source]
		if !ms.enabled {
			continue
		}
		req := make([]byte, 4)
		binary.BigEndian.PutUint16(req[0:], blk.Start)
		binary.BigEndian.PutUint16(req[2:], blk.Count)
		resp, exc := ms.unit.Handle(blk.FC(), req)
		if exc != 0 {
			ip.fault("modbus source %s (%s): FC %02X %s %d+%d answered exception %02X",
				blk.Source, ms.device, blk.FC(), blk.Table, blk.Start, blk.Count, exc)
			continue
		}
		for _, t := range blk.Bindings {
			v, err := decodeFromBlock(blk, t, resp, ms.src)
			if err != nil {
				ip.fault("modbus %s: %v", t.Name, err)
				continue
			}
			out[t.Name] = v
		}
	}
	for _, ms := range f.sources {
		out[ms.src.OnlineTagName()] = ms.online
		out[ms.src.OutagesTagName()] = int64(0)
	}
}

// decodeFromBlock pulls one binding's value out of a block read's response
// data, decoded as the driver decodes it.
func decodeFromBlock(blk modbus.Block, t modbus.TagBinding, resp []byte, src modbus.Source) (any, error) {
	if len(resp) < 1 {
		return nil, errors.New("empty response")
	}
	data := resp[1:]
	off := int(t.Address - blk.Start)
	if blk.Table == modbus.TableCoil || blk.Table == modbus.TableDiscrete {
		if off/8 >= len(data) {
			return nil, fmt.Errorf("bit %d past the response", off)
		}
		return data[off/8]&(1<<(off%8)) != 0, nil
	}
	format := t.Format
	if format == "" {
		format = "uint16"
	}
	f, err := modbus.ParseFormat(format)
	if err != nil {
		return nil, err
	}
	regs := make([]uint16, f.Words())
	for i := range regs {
		j := 2 * (off + i)
		if j+1 >= len(data) {
			return nil, fmt.Errorf("register %d past the response", int(t.Address)+i)
		}
		regs[i] = binary.BigEndian.Uint16(data[j:])
	}
	return modbus.Decode(f, regs, src.WordOrder, src.ByteOrder, t.Scale, t.Offset)
}

func (ip *InProcess) readOPCUA(f *uaFace, out nio.Values) {
	tags := ip.prt.Tags()
	for name, n := range f.nodes {
		if n.b.ScanClass == opcua.NoPoll {
			continue
		}
		if n.tag == "" {
			ip.fault("opcua %s (%s): %s", name, f.device, n.why)
			continue
		}
		v, ok := tags.ReadPath(n.tag)
		if !ok {
			ip.fault("opcua %s: plant tag %s has no value", name, n.tag)
			continue
		}
		out[name] = plainOPCUA(v)
	}
	out[f.online] = true
}

// plainOPCUA is a value as the opcua driver delivers it: ints as int64,
// floats as float64, bool and string as themselves.
func plainOPCUA(v any) any {
	switch x := v.(type) {
	case ir.Value:
		switch x.Kind {
		case ir.TypeBool:
			return x.B
		case ir.TypeReal:
			return x.F
		case ir.TypeString:
			return x.S
		default:
			return x.I
		}
	}
	return v
}

// WriteOutputs answers the project's writes: a Modbus output as the
// write request the driver would send (FC 5 for a coil, FC 16 for
// registers, a bit:N merged into its register), an OPC UA output into the
// device's plant tag if the device lets it be written.
func (ip *InProcess) WriteOutputs(vals nio.Values) error {
	names := make([]string, 0, len(vals))
	for k := range vals {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, f := range ip.mb {
		for _, name := range names {
			if srcs, ok := f.enables[name]; ok {
				on, _ := vals[name].(bool)
				for _, ms := range srcs {
					ms.enabled = on
				}
			}
		}
		baseline := !f.baselined
		f.baselined = true
		for _, name := range names {
			t, ok := f.outs[name]
			if !ok {
				continue
			}
			ms := f.sources[t.Source]
			if !ms.enabled {
				continue
			}
			v := vals[name]
			if prev, ok := f.written[name]; ok && reflect.DeepEqual(prev, v) {
				continue // unchanged since it was last accounted for
			}
			f.written[name] = v
			if baseline && t.Rewrite <= 0 {
				continue // recorded, never sent: the driver's start-up baseline
			}
			if err := writeModbus(ms, t, v); err != nil {
				ip.fault("modbus write %s (%s): %v", name, ms.device, err)
			}
		}
	}
	tags := ip.prt.Tags()
	for _, f := range ip.ua {
		for _, name := range names {
			n, ok := f.nodes[name]
			if !ok || !n.b.Writable {
				continue
			}
			switch {
			case n.tag == "":
				ip.fault("opcua write %s (%s): %s", name, f.device, n.why)
			case !n.writable:
				ip.fault("opcua write %s (%s): Bad_NotWritable: %s is read-only on the device", name, f.device, n.b.NodeID)
			default:
				if err := tags.SetPath(n.tag, vals[name]); err != nil {
					ip.fault("opcua write %s (%s): %v", name, f.device, err)
				}
			}
		}
	}
	return nil
}

// writeModbus sends one binding's value as the request the driver sends.
func writeModbus(ms *mbSource, t modbus.TagBinding, v any) error {
	if t.Table == modbus.TableCoil {
		req := []byte{byte(t.Address >> 8), byte(t.Address), 0, 0}
		if truthy(v) {
			req[2] = 0xFF
		}
		if _, exc := ms.unit.Handle(0x05, req); exc != 0 {
			return fmt.Errorf("FC 05 coil %d answered exception %02X", t.Address, exc)
		}
		return nil
	}
	format := t.Format
	if format == "" {
		format = "uint16"
	}
	f, err := modbus.ParseFormat(format)
	if err != nil {
		return err
	}
	regs, err := modbus.Encode(f, v, ms.src.WordOrder, ms.src.ByteOrder, t.Scale, t.Offset)
	if err != nil {
		return err
	}
	if f.Kind == "bit" {
		// Read-modify-write, as the driver does for a bit in a register.
		resp, exc := ms.unit.Handle(0x03, []byte{byte(t.Address >> 8), byte(t.Address), 0, 1})
		if exc != 0 {
			return fmt.Errorf("FC 03 holding %d answered exception %02X", t.Address, exc)
		}
		cur := binary.BigEndian.Uint16(resp[1:])
		mask, _ := modbus.Encode(f, true, ms.src.WordOrder, ms.src.ByteOrder, 0, 0)
		if truthy(v) {
			cur |= mask[0]
		} else {
			cur &^= mask[0]
		}
		regs = []uint16{cur}
	}
	req := make([]byte, 5+2*len(regs))
	binary.BigEndian.PutUint16(req[0:], t.Address)
	binary.BigEndian.PutUint16(req[2:], uint16(len(regs)))
	req[4] = byte(2 * len(regs))
	for i, r := range regs {
		binary.BigEndian.PutUint16(req[5+2*i:], r)
	}
	if _, exc := ms.unit.Handle(0x10, req); exc != 0 {
		return fmt.Errorf("FC 10 holding %d+%d answered exception %02X", t.Address, len(regs), exc)
	}
	return nil
}

var _ nio.Driver = (*InProcess)(nil)

// SetPlant writes one plant tag (a step's `plant:`), after stepping the
// plant to now, so the write lands at the step's start.
func (ip *InProcess) SetPlant(name string, v any) error {
	ip.step()
	tags := ip.prt.Tags()
	if _, ok := tags.ReadPath(name); !ok {
		return fmt.Errorf("the plant has no tag %s (device points are <Device>_<Point>)", name)
	}
	if n, ok := v.(int); ok {
		v = int64(n)
	}
	return tags.SetPath(name, v)
}
