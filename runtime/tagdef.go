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
//	                         before each scan. Not seeded by default: if
//	                         the driver doesn't deliver it, a read faults
//	                         loudly instead of running on a silent zero.
//	                         EXCEPT a Comms input — one a communications
//	                         driver delivers (Modbus, EtherNet/IP, a
//	                         Sparkplug host, the IT pollers) — which is
//	                         seeded with its Init or the zero of its type,
//	                         so a device that is down when the controller
//	                         starts costs its own tags, not every program
//	                         that reads them (see TagDef.Comms).
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
	// GVL marks a tag no manifest entry declares: a library's file-level
	// VAR_GLOBAL made it one (project.gvlTags). A program's declaration of
	// it is the global's own, so New does not hold it to the tag model's
	// refusal of a function-block type the way it holds a manifest tag.
	GVL bool
	// Comms marks an INPUT that a communications driver delivers: a
	// protocol source on the far end of a wire — a Modbus device, a Logix
	// controller over EtherNet/IP, an edge node through a Sparkplug host,
	// a switch or BMC through the IT pollers — that can be down when the
	// controller starts and stay down for as long as it likes. Such an
	// input is SEEDED: with Init when there is one, else with the zero of
	// its type, so it exists from scan one and the programs that read it
	// run. That is the Allen-Bradley behaviour, and the one an engineer
	// expects: a Logix controller does not stop executing because a
	// produced/consumed partner or a remote rack is missing; the tags
	// read their last (or initial) value and the logic checks the
	// connection's status bit. Here that bit is the source's `__Online`
	// companion, false until the device answers, and the driver's own
	// quality report says NotConnected for the value meanwhile.
	//
	// Without Comms an input keeps the strict rule: unseeded, and a read
	// before the first delivery faults the scan. That is right for local
	// I/O (a PLCnext's Axioline bus, a groov EPIC's own modules), where a
	// missing input means the configuration is wrong, not that a cable
	// is out.
	//
	// The manifest loader sets it per tag from the driver that delivers
	// it (project.markComms), so a composite drivers: set mixing a Modbus
	// skid with local I/O seeds exactly the Modbus tags. A Go composition
	// sets it by hand, or not at all.
	Comms bool
	// DriverType is the IEC type the delivering driver's binding produces
	// ("BOOL", "DINT", "REAL", a UDT the project's ST declares) when the
	// driver knows it — a Modbus binding's format does. It is the LAST
	// resort for a Comms input's seed type, after the tag's own Type and
	// the type the programs declare it as; empty when the driver cannot
	// say. Ignored for every other tag.
	DriverType string
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

// Comms marks an Input as delivered by a communications driver, so it is
// seeded with its Init or the zero of its type instead of faulting the
// scans that read it until the first delivery — see TagDef.Comms. The
// manifest loader decides this from the driver; a Go composition says it.
func Comms() TagOpt { return func(d *TagDef) { d.Comms = true } }

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
// until something writes it. Outputs stay unseeded, and so do inputs that a
// LOCAL I/O driver delivers (or that no driver claims at all): an input the
// driver has not delivered must still fault loudly rather than run on a
// silent zero, which is the whole reason RoleInput exists.
//
// A Comms input (TagDef.Comms — one a communications driver delivers) is
// the exception, and it is seeded: with its Init when it has one (as any
// input with an init: always was), else with the zero of the first type
// that can be known for it — its own Type, then the type the programs
// declare it as (globals), then the driver binding's DriverType. A Comms
// input none of the three can type is left unseeded rather than guessed
// at (a made-up REAL would be a wrong kind the moment the real value
// arrived): its name comes back in the third result, for `naut check` to
// report. No program reads such a tag — a program that did would have
// declared its type — so leaving it absent faults nothing.
//
// The seed is a STORE value, never a delivery: the driver's own map does
// not carry it, so a BatchReader that prunes its key set each scan cannot
// delete it, and the scan's input copy (Tags.setMany) only writes names
// the driver actually delivered. The first real value replaces it; after
// that the driver's last-known-value rules apply unchanged.
//
// A typed tag's Init may instead be a (possibly nested) map of member name
// to value — see ir.SeedFromInit — to seed only the members that need a
// non-zero starting value (a UDT's setpoint fields) while every other
// member still takes the zero of its own type. An unknown member name or a
// scalar Init against a struct type is a load error naming the tag and the
// member path.
//
// An UNTYPED tag's Init takes its kind from the programs: globals maps each
// tag a program binds (VAR_EXTERNAL, including a library block's) to its
// declared type, and a scalar declared there — BOOL, INT, REAL, STRING —
// converts the init the way a struct member's does. So `init: 0` on a tag
// the logic declares `Counter : DINT` seeds an integer (and births as one),
// while `init: 65` on `TempSP : REAL` is still a REAL. A tag no program
// declares keeps the raw init, as before: a number seeds a REAL.
func expandTags(o Options, types, globals map[string]*ir.Type) (Options, []string, error) {
	if len(o.Tags) == 0 {
		return o, nil, nil
	}
	var untyped []string
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
	folded := make(map[string]*ir.Type, len(globals))
	for name, t := range globals {
		folded[ir.NameKey(name)] = t
	}
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
		case d.Type != "":
			// A typed tag's init is validated and shaped against the
			// resolved type (SeedFromInit), rather than stored as the raw
			// manifest value — a nested map is not one of the kinds
			// Tags.Set understands, and `type: INT` with `init: 2.5` is a
			// mistake to report, not a REAL to store.
			t, err := ResolveTagType(d.Type, types)
			if err != nil {
				return o, nil, fmt.Errorf("tag %s: %w", d.Name, err)
			}
			if d.Init == nil {
				if d.Role == RoleSetpoint || d.Role == RoleState || commsInput(d) {
					seed[d.Name] = ir.Zero(t)
				}
				break
			}
			v, err := ir.SeedFromInit(t, d.Init)
			if err != nil {
				return o, nil, fmt.Errorf("tag %s (type %s): %w", d.Name, d.Type, err)
			}
			seed[d.Name] = v
		case d.Init != nil:
			if t := scalarGlobal(folded, d.Name); t != nil {
				v, err := ir.SeedFromInit(t, d.Init)
				if err != nil {
					return o, nil, fmt.Errorf("tag %s (declared %s by a program): %w", d.Name, t.String(), err)
				}
				seed[d.Name] = v
				break
			}
			if t := typeOfInit(d.Init); t == ir.TimeT {
				// `init: T#5s` with no type: is a TIME, and seeds as one.
				v, err := ir.SeedFromInit(t, d.Init)
				if err != nil {
					return o, nil, fmt.Errorf("tag %s: %w", d.Name, err)
				}
				seed[d.Name] = v
				break
			}
			seed[d.Name] = d.Init
		case d.Role == RoleSetpoint || d.Role == RoleState:
			// Neither init nor type: a GVL global (a library's file-level
			// VAR_GLOBAL), which starts at the zero of the type the
			// programs declare it as — the Codesys meaning of a GVL entry.
			if t := folded[ir.NameKey(d.Name)]; t != nil {
				seed[d.Name] = ir.Zero(t)
			}
		case commsInput(d):
			// Neither init nor type on a Comms input: the programs' type,
			// then the driver binding's. See the doc comment above.
			if t := folded[ir.NameKey(d.Name)]; t != nil && t.Kind != ir.TypeFB {
				seed[d.Name] = ir.Zero(t)
				break
			}
			if d.DriverType != "" {
				if t, err := ResolveTagType(d.DriverType, types); err == nil && t.Kind != ir.TypeFB {
					seed[d.Name] = ir.Zero(t)
					break
				}
			}
			untyped = append(untyped, d.Name)
		}
		if d.Meta != (TagMeta{}) {
			meta[d.Name] = d.Meta
		}
	}
	o.Inputs, o.Outputs, o.Seed, o.Meta = inputs, outputs, seed, meta
	return o, untyped, nil
}

// commsInput is an input a communications driver delivers — the one input
// that is seeded without an init: (TagDef.Comms).
func commsInput(d TagDef) bool { return d.Role == RoleInput && d.Comms }

// scalarGlobal returns the type a program declares tag name as, when that
// is one of the kinds an init: can seed (BOOL, INT, REAL, STRING, TIME,
// ARRAY); nil otherwise. globals is keyed by ir.NameKey: a program may case
// a tag differently from the manifest. A struct-typed global is the
// `type:` path's business.
func scalarGlobal(globals map[string]*ir.Type, name string) *ir.Type {
	t := globals[ir.NameKey(name)]
	if t == nil {
		return nil
	}
	switch t.Kind {
	case ir.TypeBool, ir.TypeInt, ir.TypeReal, ir.TypeString, ir.TypeTime, ir.TypeArray:
		return t
	}
	return nil
}

// knownTypes lists what the project does declare, so a misspelled type name
// is one message rather than a hunt through the library files.
func knownTypes(types map[string]*ir.Type) string {
	names := make([]string, 0, len(types))
	for n, t := range types {
		// st.Types lists the project's FUNCTION_BLOCKs too, so that one
		// is refused by name; they are not types a tag can take.
		if t != nil && t.Kind == ir.TypeFB {
			continue
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		return "none — this project declares no TYPE"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
