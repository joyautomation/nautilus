package logixserver

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/eip/cip"
	"github.com/joyautomation/nautilus/lang/l5x"
)

// L5XSurface is a tag surface derived from a Logix Designer export, plus the
// initial values the export carries and what could not be served.
type L5XSurface struct {
	Spec *TagSurfaceSpec
	// Values are the decorated <Data> initial values, keyed by the path a
	// client reads ("Tank.Level", "Program:MainProgram.Count", "Trend[2]").
	// Seed them with SeedValue; a value the surface has no leaf for (a
	// member of an omitted type) is not an error there.
	Values map[string]any
	// Skipped says, one line each, which tags, types and members were left
	// out and why — the export can name shapes no emulator can guess.
	Skipped []string
}

// builtinTypes are the predefined Logix structures an export references but
// never declares. Members are the externally useful ones; the layout is the
// emulator's own (clients read member offsets from the template, so it only
// has to be self-consistent).
var builtinTypes = map[string][]MemberSpec{
	"STRING": {
		{Name: "LEN", Datatype: "DINT"},
		{Name: "DATA", Datatype: "SINT", Dimension: 82},
	},
	"TIMER": {
		{Name: "PRE", Datatype: "DINT"}, {Name: "ACC", Datatype: "DINT"},
		{Name: "EN", Datatype: "BOOL"}, {Name: "TT", Datatype: "BOOL"}, {Name: "DN", Datatype: "BOOL"},
	},
	"COUNTER": {
		{Name: "PRE", Datatype: "DINT"}, {Name: "ACC", Datatype: "DINT"},
		{Name: "CU", Datatype: "BOOL"}, {Name: "CD", Datatype: "BOOL"}, {Name: "DN", Datatype: "BOOL"},
		{Name: "OV", Datatype: "BOOL"}, {Name: "UN", Datatype: "BOOL"},
	},
}

// SurfaceFromL5X derives a tag surface from a parsed L5X export:
//
//   - every DataType (and every Add-On Instruction, by its Input/Output
//     parameters) becomes a template; a UDT's BIT overlays become BOOL
//     members and their hidden host bytes are dropped. STRING, TIMER and
//     COUNTER are supplied when referenced.
//   - every controller-scope tag becomes a symbol; every program-scope tag
//     becomes a symbol in "Program:<prog>" scope, read as
//     "Program:<prog>.<tag>", exactly as a controller serves it.
//   - leaves are expanded down to elementary members, through nested UDTs
//     and one-dimensional arrays.
//
// Alias tags, multi-dimensional arrays, and types with no public shape
// (MESSAGE, AXIS_*, module-defined I/O structures an export only names) are
// left out and reported in Skipped. name overrides the controller name
// (default: the export's controller name).
func SurfaceFromL5X(f *l5x.File, name string) (*L5XSurface, error) {
	if f == nil || f.Controller == nil {
		return nil, fmt.Errorf("l5x: export has no <Controller>")
	}
	c := f.Controller
	if name == "" {
		name = c.Name
	}
	if name == "" {
		name = f.TargetName
	}
	b := &l5xBuilder{
		out: &L5XSurface{
			Spec:   &TagSurfaceSpec{ControllerName: name},
			Values: map[string]any{},
		},
		dataTypes: map[string]*l5x.DataType{},
		aois:      map[string]*l5x.AddOnInstruction{},
		members:   map[string][]MemberSpec{},
		canon:     map[string]string{},
		state:     map[string]int{},
	}
	for _, dt := range c.DataTypes {
		b.dataTypes[strings.ToUpper(dt.Name)] = dt
	}
	for _, a := range c.AOIs {
		b.aois[strings.ToUpper(a.Name)] = a
	}

	// Every declared type, in declared order (dependencies pulled in first).
	for _, dt := range c.DataTypes {
		b.ensure(dt.Name)
	}
	for _, a := range c.AOIs {
		b.ensure(a.Name)
	}

	for _, t := range c.Tags {
		b.addTag(t, "")
	}
	for _, p := range c.Programs {
		scope := "Program:" + p.Name
		b.out.Spec.Symbols = append(b.out.Spec.Symbols, SymbolSpec{Name: scope, Program: true})
		for _, t := range p.Tags {
			b.addTag(t, scope)
		}
	}
	return b.out, nil
}

type l5xBuilder struct {
	out       *L5XSurface
	dataTypes map[string]*l5x.DataType
	aois      map[string]*l5x.AddOnInstruction
	// members is each emitted template's member list, keyed upper-case, and
	// canon its declared spelling — the leaf walk needs both.
	members map[string][]MemberSpec
	canon   map[string]string
	state   map[string]int // 0 new, 1 visiting, 2 emitted, 3 unresolvable
}

// ensure emits the template for typeName (and, first, every template it
// references). It reports whether typeName is servable: elementary, or a
// template that was emitted.
func (b *l5xBuilder) ensure(typeName string) bool {
	if typeName == "BIT" {
		return true
	}
	if _, ok := cipTypeForName(typeName); ok {
		return true
	}
	key := strings.ToUpper(typeName)
	switch b.state[key] {
	case 1: // a cycle — Logix UDTs cannot be recursive, so the export is odd
		return false
	case 2:
		return true
	case 3:
		return false
	}
	b.state[key] = 1

	var src []MemberSpec
	switch {
	case b.dataTypes[key] != nil:
		dt := b.dataTypes[key]
		typeName = dt.Name
		for _, m := range dt.Members {
			if m.Hidden {
				continue // a BIT host byte; its BITs become BOOLs
			}
			dtype := m.DataType
			if strings.EqualFold(dtype, "BIT") {
				dtype = "BOOL"
			}
			src = append(src, MemberSpec{Name: m.Name, Datatype: dtype, Dimension: m.Dimension})
		}
	case b.aois[key] != nil:
		a := b.aois[key]
		typeName = a.Name
		src = append(src, MemberSpec{Name: "EnableIn", Datatype: "BOOL"}, MemberSpec{Name: "EnableOut", Datatype: "BOOL"})
		for _, p := range a.Parameters {
			if p.Usage == "InOut" || strings.EqualFold(p.Name, "EnableIn") || strings.EqualFold(p.Name, "EnableOut") {
				continue // InOut is a reference, not storage in the backing tag
			}
			src = append(src, MemberSpec{Name: p.Name, Datatype: p.DataType, Dimension: p.Dimension})
		}
	case builtinTypes[key] != nil:
		typeName = key
		src = builtinTypes[key]
	default:
		b.state[key] = 3
		return false
	}

	var members []MemberSpec
	for _, m := range src {
		if !b.ensure(m.Datatype) {
			b.skip(fmt.Sprintf("member %s.%s: type %s has no public shape", typeName, m.Name, m.Datatype))
			continue
		}
		if c, ok := b.canon[strings.ToUpper(m.Datatype)]; ok {
			m.Datatype = c
		}
		members = append(members, m)
	}
	b.out.Spec.Templates = append(b.out.Spec.Templates, TemplateSpec{Name: typeName, Members: members})
	b.members[key] = members
	b.canon[key] = typeName
	b.state[key] = 2
	return true
}

func (b *l5xBuilder) skip(msg string) {
	for _, s := range b.out.Skipped {
		if s == msg {
			return
		}
	}
	b.out.Skipped = append(b.out.Skipped, msg)
}

// addTag emits one tag as a symbol plus its leaves and initial values.
func (b *l5xBuilder) addTag(t *l5x.Tag, scope string) {
	path := t.Name
	if scope != "" {
		path = scope + "." + t.Name
	}
	if t.TagType == "Alias" {
		b.skip(fmt.Sprintf("tag %s: alias for %s (aliases are not served)", path, t.AliasFor))
		return
	}
	if !b.ensure(t.DataType) {
		b.skip(fmt.Sprintf("tag %s: type %s has no public shape", path, t.DataType))
		return
	}
	dtype := t.DataType
	if c, ok := b.canon[strings.ToUpper(dtype)]; ok {
		dtype = c
	}
	var dims []uint32
	for _, d := range strings.Fields(t.Dimensions) {
		n, err := strconv.ParseUint(d, 10, 32)
		if err != nil || n == 0 {
			b.skip(fmt.Sprintf("tag %s: unreadable dimensions %q", path, t.Dimensions))
			return
		}
		dims = append(dims, uint32(n))
	}
	if len(dims) > 1 {
		b.skip(fmt.Sprintf("tag %s: multi-dimensional arrays are not served", path))
		return
	}
	b.out.Spec.Symbols = append(b.out.Spec.Symbols, SymbolSpec{Name: t.Name, Scope: scope, Datatype: dtype, Dims: dims})
	dim := 0
	if len(dims) == 1 {
		dim = int(dims[0])
	}
	b.leaves(path, dtype, dim)
	if t.Value != nil {
		b.out.Values[path] = t.Value
	}
}

// leaves appends the elementary read leaves beneath path.
func (b *l5xBuilder) leaves(path, dtype string, dim int) {
	if dim > 0 {
		for i := 0; i < dim; i++ {
			b.leaves(path+"["+strconv.Itoa(i)+"]", dtype, 0)
		}
		return
	}
	if _, ok := cipTypeForName(dtype); ok {
		b.out.Spec.Tags = append(b.out.Spec.Tags, TagSpec{Path: path, Datatype: strings.ToUpper(dtype)})
		return
	}
	for _, m := range b.members[strings.ToUpper(dtype)] {
		b.leaves(path+"."+m.Name, m.Datatype, m.Dimension)
	}
}

// SeedValue writes an initial value into the store at path. Scalars (bool,
// numbers) set one leaf; a map sets members ("path.key"), a slice sets
// elements ("path[i]"), and a string fills a STRING's LEN and DATA[i]
// leaves. It fails, naming the path, when no leaf matches — unless lenient,
// which skips unknown leaves (an L5X initial value for an omitted member).
func SeedValue(store *TagStore, path string, v any, lenient bool) error {
	switch x := v.(type) {
	case nil:
		return nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := SeedValue(store, path+"."+k, x[k], lenient); err != nil {
				return err
			}
		}
		return nil
	case []any:
		for i, e := range x {
			if err := SeedValue(store, path+"["+strconv.Itoa(i)+"]", e, lenient); err != nil {
				return err
			}
		}
		return nil
	case string:
		if _, _, ok := store.Resolve(path + ".LEN"); !ok {
			if lenient {
				return nil
			}
			return fmt.Errorf("%s: a string value needs a STRING tag", path)
		}
		if len(x) > 82 {
			x = x[:82]
		}
		store.UpdateValue(path+".LEN", float64(len(x)))
		for i := 0; i < 82; i++ {
			c := 0.0
			if i < len(x) {
				c = float64(int8(x[i]))
			}
			store.UpdateValue(path+".DATA["+strconv.Itoa(i)+"]", c)
		}
		return nil
	}
	leafType, _, ok := store.Resolve(path)
	if !ok {
		if lenient {
			return nil
		}
		return fmt.Errorf("no tag %q", path)
	}
	switch x := v.(type) {
	case bool:
		if leafType == cip.TypeBOOL {
			store.UpdateValue(path, x)
		} else {
			store.UpdateValue(path, toFloat(x))
		}
	case float64, float32, int, int32, int64, uint, uint32, uint64:
		if leafType == cip.TypeBOOL {
			store.UpdateValue(path, toFloat(x) != 0)
		} else {
			store.UpdateValue(path, toFloat(x))
		}
	default:
		return fmt.Errorf("%s: unsupported value %v (%T)", path, v, v)
	}
	return nil
}
