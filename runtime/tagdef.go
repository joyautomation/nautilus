package runtime

import (
	"fmt"
	"sort"
	"strings"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// Declarative tag definitions: one place per tag for its ROLE in the scan
// data path, its initial value, and its HMI documentation — instead of
// spreading the same name across Options.Inputs, Outputs, Seed, and Meta by
// hand (and faulting a scan when one of the four is forgotten).
//
// The roles mirror how tags are actually used:
//
//	Input(name)            — the driver produces it; copied driver → tags
//	                         before each scan. NOT seeded: if the driver
//	                         doesn't deliver it, a read faults loudly
//	                         instead of running on a silent zero.
//	Output(name)           — the logic produces it; copied tags → driver
//	                         after each scan. Add Init(...) when the logic
//	                         also READS it (a seal-in latch reads its own
//	                         coil on scan one, before the first write).
//	Setpoint(name, init)   — operator/config value: seeded so it exists on
//	                         scan one, written later by an HMI or the API.
//	State(name, init)      — logic-owned value that must survive read-
//	                         before-write and restarts-by-seed: latches,
//	                         integrators, mode words.
//
// TagDefs and the flat Options fields compose — everything is merged in
// New() — so existing compositions keep working unchanged.

// TagRole says how a tag enters and leaves the store each scan.
type TagRole int

const (
	// RoleInput: driver → tag before each scan.
	RoleInput TagRole = iota
	// RoleOutput: tag → driver after each scan.
	RoleOutput
	// RoleSetpoint: seeded operator/config value; HMI/API-writable.
	RoleSetpoint
	// RoleState: seeded logic-owned value (latches, integrators).
	RoleState
)

// TagDef declares one tag. Build them with Input/Output/Setpoint/State.
type TagDef struct {
	Name string
	Role TagRole
	Init any // seed value; nil = none
	Meta TagMeta
	// Type names a UDT declared by the project's ST — `Motor` for a tag the
	// programs bind as `VAR_EXTERNAL P101 : Motor;`. It is resolved against
	// the compiled programs' TYPE table in New, so one declaration serves
	// the logic, the tag store, the driver, and the HMI. Empty for a scalar,
	// whose type comes from its seed as before.
	Type string
}

// TagOpt customizes a TagDef (Desc, Unit, Init).
type TagOpt func(*TagDef)

// Desc sets the HMI description shown in tag tables.
func Desc(s string) TagOpt { return func(d *TagDef) { d.Meta.Desc = s } }

// Unit sets the engineering unit ("°C", "%", "L/s", ...).
func Unit(s string) TagOpt { return func(d *TagDef) { d.Meta.Unit = s } }

// Init seeds an initial value. Required on an Output the logic also READS:
// a seal-in latch reads its own coil before the first write, and a read of
// a tag that was never written faults the scan.
func Init(v any) TagOpt { return func(d *TagDef) { d.Init = v } }

func newDef(name string, role TagRole, init any, opts []TagOpt) TagDef {
	d := TagDef{Name: name, Role: role, Init: init}
	for _, o := range opts {
		o(&d)
	}
	return d
}

// Input declares a driver-fed field input (level, temperature, ...).
func Input(name string, opts ...TagOpt) TagDef { return newDef(name, RoleInput, nil, opts) }

// Output declares a logic-fed field output (pump command, valve position).
// Add Init(...) when the logic reads it back (seal-in latches).
func Output(name string, opts ...TagOpt) TagDef { return newDef(name, RoleOutput, nil, opts) }

// Setpoint declares an operator/config value with its initial state.
func Setpoint(name string, initial any, opts ...TagOpt) TagDef {
	return newDef(name, RoleSetpoint, initial, opts)
}

// State declares a logic-owned value (latch, integrator, mode word) with
// the initial state it needs to survive read-before-write.
func State(name string, initial any, opts ...TagOpt) TagDef {
	return newDef(name, RoleState, initial, opts)
}

// Typed declares a tag whose value is a UDT named by the project's ST. The
// role still decides how it moves each scan; the type decides its shape.
func Typed(name string, role TagRole, typeName string, opts ...TagOpt) TagDef {
	d := newDef(name, role, nil, opts)
	d.Type = typeName
	return d
}

// expandTags merges o.Tags into the flat Options fields, resolving any UDT
// names against types (the compiled programs' TYPE table). The caller's maps
// are never mutated; TagDef seeds/meta win over the flat fields on a
// duplicate name (the declarative form is the single source of truth).
//
// A typed tag with no explicit Init seeds the ZERO of its type for the roles
// that are seeded at all — so a `Motor` setpoint exists on scan one with its
// fields correctly named and its StructDef attached, instead of being absent
// until something writes it. Inputs and outputs stay unseeded: an input that
// the driver has not delivered must still fault loudly rather than run on a
// silent zero, which is the whole reason RoleInput exists.
func expandTags(o Options, types map[string]*ir.Type) (Options, error) {
	if len(o.Tags) == 0 {
		return o, nil
	}
	seed := make(nio.Values, len(o.Seed)+len(o.Tags))
	for k, v := range o.Seed {
		seed[k] = v
	}
	meta := make(map[string]TagMeta, len(o.Meta)+len(o.Tags))
	for k, v := range o.Meta {
		meta[k] = v
	}
	inputs := append([]string(nil), o.Inputs...)
	outputs := append([]string(nil), o.Outputs...)
	for _, d := range o.Tags {
		if d.Name == "" {
			continue
		}
		switch d.Role {
		case RoleInput:
			inputs = append(inputs, d.Name)
		case RoleOutput:
			outputs = append(outputs, d.Name)
		}
		switch {
		case d.Init != nil:
			seed[d.Name] = d.Init
		case d.Type != "":
			t, ok := types[d.Type]
			if !ok {
				return o, fmt.Errorf("tag %s: no TYPE %s is declared by this project's "+
					"ST — a tag's type is the one the programs use, so declare it in a "+
					"library .st file (known: %s)", d.Name, d.Type, knownTypes(types))
			}
			if d.Role == RoleSetpoint || d.Role == RoleState {
				seed[d.Name] = ir.Zero(t)
			}
		}
		if d.Meta != (TagMeta{}) {
			meta[d.Name] = d.Meta
		}
	}
	o.Inputs, o.Outputs, o.Seed, o.Meta = inputs, outputs, seed, meta
	return o, nil
}

// knownTypes lists what the project does declare, so a misspelled type name
// is one message rather than a hunt through the library files.
func knownTypes(types map[string]*ir.Type) string {
	if len(types) == 0 {
		return "none — this project declares no TYPE"
	}
	names := make([]string, 0, len(types))
	for n := range types {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
