package devbind

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/devprofile"
	nio "github.com/joyautomation/nautilus/io"

	"github.com/joyautomation/nautilus/internal/tagfile"
	"github.com/joyautomation/nautilus/lang/ir"
)

// TypesST is the ST the project compiles for its devices' types
// (devprofile.TypesST over the profiles in use); "" with no devices.
func (s *Set) TypesST() (string, error) {
	if s.Len() == 0 {
		return "", nil
	}
	return devprofile.TypesST(s.types), nil
}

// Tags are the tags the devices declare: each device's struct tag (an
// input), its driver's companions (<Device>__Online, and a Modbus
// device's <Device>__Outages), and a command tag (an output) per selected
// writable point.
func (s *Set) Tags() []tagfile.Tag {
	var out []tagfile.Tag
	for _, dt := range s.TagsByDevice() {
		out = append(out, dt.Tags...)
	}
	return out
}

// DeviceTags are one device's tags.
type DeviceTags struct {
	Device string
	Tags   []tagfile.Tag
}

// TagsByDevice is Tags, grouped by the device that declares them.
func (s *Set) TagsByDevice() []DeviceTags {
	if s == nil {
		return nil
	}
	var out []DeviceTags
	for _, d := range s.devices {
		desc := d.Desc
		if desc == "" {
			desc = d.prof.Title
		}
		dt := DeviceTags{Device: d.Name}
		dt.Tags = append(dt.Tags, tagfile.Tag{Name: d.Name, Role: "input", Type: d.prof.TypeName(), Desc: desc})
		// The driver's companions for the device (its Modbus source, or
		// its own OPC UA driver), so a program can gate on them.
		dt.Tags = append(dt.Tags, tagfile.Tag{Name: d.Name + "__Online", Role: "input", Type: "BOOL", Desc: d.Name + ": the device answers"})
		if d.prof.Protocol == devprofile.ProtoModbus {
			dt.Tags = append(dt.Tags, tagfile.Tag{Name: d.Name + "__Outages", Role: "input", Type: "DINT", Desc: d.Name + ": times it stopped answering since start"})
		}
		for _, pt := range d.points {
			// A listed port drives its process data out (and the valid flag)
			// through the gate: no command tag for the master's own.
			if !pt.Writable || d.owned(pt.Name) {
				continue
			}
			dt.Tags = append(dt.Tags, tagfile.Tag{
				Name: CommandTag(d.Name, pt.Name), Role: "output", Type: strings.ToUpper(pt.Type),
				Unit: pt.Unit, Desc: joinDesc(d.Name, pt.Desc),
			})
		}
		for _, pd := range d.ports {
			where := fmt.Sprintf("%s port %d: %s", d.Name, pd.Num, pd.dev.IODD.Identity.DeviceName)
			dt.Tags = append(dt.Tags,
				tagfile.Tag{Name: pd.Name, Role: "input", Type: pd.dev.TypeName, Desc: where},
				tagfile.Tag{Name: IdentityTag(pd.Name), Role: "input", Type: "INT", Desc: pd.Name + ": identity check (0 Ok, 1 NoDevice, 2 WrongDevice, 3 Unchecked, 4 AwaitingRearm)"},
				tagfile.Tag{Name: IdentityFaultTag(pd.Name), Role: "input", Type: "BOOL", Desc: pd.Name + ": wrong, missing or unconfirmed device on " + d.Name + " port " + fmt.Sprint(pd.Num) + " (true until the port is re-armed)"})
			for _, c := range pd.dev.Commands {
				dt.Tags = append(dt.Tags, tagfile.Tag{
					Name: CommandPortTag(pd.Name, c.Name), Role: "output", Type: c.Type,
					Unit: c.Unit, Desc: joinDesc(pd.Name, c.Desc),
				})
			}
			if pd.rearm != "" {
				dt.Tags = append(dt.Tags, tagfile.Tag{
					Name: pd.rearm, Role: "output", Type: "BOOL",
					Desc: pd.Name + ": pulse to let the port send its process data out, after the controller starts or the device on the port is swapped",
				})
			}
		}
		out = append(out, dt)
	}
	return out
}

func joinDesc(dev, desc string) string {
	if desc == "" {
		return dev
	}
	return dev + ": " + desc
}

// assembler builds a device's struct value from its members' deliveries.
type assembler struct {
	dev     string
	def     *ir.StructDef
	members []string // internal names, index-parallel to def.Fields
	last    ir.Value // the last value delivered; Kind 0 before any
	boxed   any      // last, boxed once: a map[string]any entry costs an allocation per box
	// derived are the computed BOOL members after the polled ones: each
	// field index, the source's, and the test. got marks the polled members
	// delivered so far, so a derived point whose source has not arrived is
	// false, never "0 equals the code".
	derived []derivedField
	got     []bool
}

type derivedField struct {
	field, src int
	d          devprofile.Derived
}

func newAssembler(d *device) *assembler {
	a := &assembler{dev: d.Name, def: &ir.StructDef{Name: d.prof.TypeName(), FieldIndex: map[string]int{}}}
	for i, pt := range d.points {
		a.def.Fields = append(a.def.Fields, ir.StructField{Name: pt.Name, Type: irType(pt.Type)})
		a.def.FieldIndex[pt.Name] = i
		a.members = append(a.members, memberName(d.Name, pt.Name))
	}
	for _, dp := range d.derived {
		src := a.def.FieldIndex[dp.From]
		a.def.Fields = append(a.def.Fields, ir.StructField{Name: dp.Name, Type: ir.BoolT})
		a.def.FieldIndex[dp.Name] = len(a.def.Fields) - 1
		a.derived = append(a.derived, derivedField{field: len(a.def.Fields) - 1, src: src, d: dp})
	}
	a.got = make([]bool, len(a.members))
	return a
}

// take moves the device's members out of vals and, once any member has
// been delivered, puts the struct in under the device's name. A member
// that is not in vals keeps its last value. A delivery that changes
// nothing hands back the same value without copying: this runs every scan.
func (a *assembler) take(vals map[string]any) {
	var next ir.Value // a copy, made at the first member that differs
	for i, m := range a.members {
		v, ok := vals[m]
		if !ok {
			continue
		}
		delete(vals, m)
		iv, ok := toIR(v, a.def.Fields[i].Type)
		if !ok {
			continue
		}
		first := !a.got[i] // a member's first delivery recomputes what reads it, even at its zero
		a.got[i] = true
		cur := a.cur(i)
		if next.Kind == 0 {
			if a.last.Kind != 0 && !first && sameScalar(cur, iv) {
				continue
			}
			next = a.base()
		}
		next.Fld[i] = iv
	}
	if next.Kind != 0 {
		for _, df := range a.derived {
			next.Fld[df.field] = ir.BoolVal(a.got[df.src] && df.d.Eval(next.Fld[df.src].I))
		}
		a.last, a.boxed = next, next
	}
	if a.last.Kind == 0 {
		return // nothing delivered yet: absent, NotConnected
	}
	vals[a.dev] = a.boxed
}

// cur is member i's current value: the last delivered, or zero.
func (a *assembler) cur(i int) ir.Value {
	if a.last.Kind != 0 {
		return a.last.Fld[i]
	}
	return ir.Zero(a.def.Fields[i].Type)
}

// base is a fresh copy of the last value, or the zero struct.
func (a *assembler) base() ir.Value {
	if a.last.Kind != 0 {
		v := a.last
		v.Fld = append([]ir.Value(nil), a.last.Fld...)
		return v
	}
	v := ir.Value{Kind: ir.TypeStruct, Struct: a.def, Fld: make([]ir.Value, len(a.def.Fields))}
	for i, f := range a.def.Fields {
		v.Fld[i] = ir.Zero(f.Type)
	}
	return v
}

func irType(iec string) *ir.Type {
	switch strings.ToUpper(iec) {
	case "BOOL":
		return ir.BoolT
	case "REAL", "LREAL":
		return ir.RealT
	case "STRING":
		return ir.StringT
	}
	if t := ir.IntNamed(strings.ToUpper(iec)); t != nil {
		return t
	}
	panic(fmt.Sprintf("devbind: point type %s", iec))
}

// toIR converts a driver's plain value to the member's kind.
func toIR(v any, t *ir.Type) (ir.Value, bool) {
	switch t.Kind {
	case ir.TypeBool:
		switch x := v.(type) {
		case bool:
			return ir.BoolVal(x), true
		case int64:
			return ir.BoolVal(x != 0), true
		}
	case ir.TypeInt:
		switch x := v.(type) {
		case int64:
			return ir.IntVal(x), true
		case float64:
			return ir.IntVal(int64(x)), true
		case bool:
			if x {
				return ir.IntVal(1), true
			}
			return ir.IntVal(0), true
		}
	case ir.TypeReal:
		switch x := v.(type) {
		case float64:
			return ir.RealVal(x), true
		case int64:
			return ir.RealVal(float64(x)), true
		}
	case ir.TypeString:
		if x, ok := v.(string); ok {
			return ir.StringVal(x), true
		}
	}
	return ir.Value{}, false
}

func sameScalar(a, b ir.Value) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case ir.TypeBool:
		return a.B == b.B
	case ir.TypeReal:
		return a.F == b.F || (a.F != a.F && b.F != b.F)
	case ir.TypeString:
		return a.S == b.S
	}
	return a.I == b.I
}

// rollup turns a driver's per-binding quality into per-device quality: a
// device is NotConnected while none of its polled members has arrived,
// Bad if any polled member is Bad (or a quality this build does not know)
// or still missing while others arrived, else Stale if any is, else
// Substituted if any is (a member that is not being refreshed outranks one
// that is held on purpose). Bindings that are not members pass through.
func rollup(asm []*assembler, member map[string]int, in map[string]nio.Quality) map[string]nio.Quality {
	var out map[string]nio.Quality
	mark := func(name string, q nio.Quality) {
		if out == nil {
			out = map[string]nio.Quality{}
		}
		out[name] = q
	}
	for name, q := range in {
		if _, ok := member[name]; !ok {
			mark(name, q)
		}
	}
	for _, a := range asm {
		polled, notConn, stale, bad, subst := 0, 0, false, false, false
		for _, m := range a.members {
			polled++
			switch in[m] {
			case nio.Good:
			case nio.NotConnected:
				notConn++
			case nio.Stale:
				stale = true
			case nio.Substituted:
				subst = true
			default: // Bad, or a quality this build does not know
				bad = true
			}
		}
		switch {
		case polled == 0:
		case notConn == polled:
			mark(a.dev, nio.NotConnected)
		case bad || notConn > 0:
			mark(a.dev, nio.Bad)
		case stale:
			mark(a.dev, nio.Stale)
		case subst:
			mark(a.dev, nio.Substituted)
		}
	}
	return out
}
