package ld

import (
	"sort"
	"strings"
	"unicode"
)

// The block catalog: every FUNCTION_BLOCK type a rung in this file can
// instantiate — the standard blocks ladder places power on, then every user
// block in scope (this file's own and the project libraries' — the same
// prelude `naut check` composes, in any language). The ladder palette's FB
// picker lists it, and prefills an insert from it: the instance-name prefix
// and a starting argument list with the rung's power left off it.

// FBType is one insertable block type.
type FBType struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	// User marks a project block (this file or a library), not a standard one.
	User bool `json:"user,omitempty"`
	// The pins the rung's power enters / leaves on for a fresh insert. An
	// empty PowerIn is a block that takes no power (no free BOOL input): it
	// may only sit where the rung's condition is the rail itself.
	PowerIn  string `json:"powerIn,omitempty"`
	PowerOut string `json:"powerOut,omitempty"`
	// Pins of a user block in declaration order: dir "in" | "out" | "inout".
	Pins []Pin `json:"pins,omitempty"`
	// Args is the starting argument text for a fresh insert.
	Args string `json:"args,omitempty"`
	// Prefix names a fresh instance: the first free <prefix><n>.
	Prefix string `json:"prefix"`
}

var standardFBs = []FBType{
	{Name: "TON", Detail: "on-delay timer", Args: "PT := T#1S", Prefix: "t"},
	{Name: "TOF", Detail: "off-delay timer", Args: "PT := T#1S", Prefix: "t"},
	{Name: "TP", Detail: "pulse timer", Args: "PT := T#1S", Prefix: "t"},
	{Name: "CTU", Detail: "count up", Args: "PV := 10", Prefix: "c"},
	{Name: "CTD", Detail: "count down", Args: "PV := 10", Prefix: "c"},
	{Name: "CTUD", Detail: "count up/down", Args: "PV := 10", Prefix: "c"},
	{Name: "R_TRIG", Detail: "rising edge", Prefix: "rt"},
	{Name: "F_TRIG", Detail: "falling edge", Prefix: "ft"},
	{Name: "SR", Detail: "set-dominant latch", Prefix: "sr"},
	{Name: "RS", Detail: "reset-dominant latch", Prefix: "rs"},
}

// catalog lists the standard blocks (fixed order) then the user blocks in
// scope, sorted by name. A block defined in this file shadows a library
// block of the same name, as it does in the compiler.
func (r *resolver) catalog() []FBType {
	out := make([]FBType, 0, len(standardFBs)+len(r.sigs))
	for _, t := range standardFBs {
		t.PowerIn, t.PowerOut, _ = builtinPowerPins(t.Name)
		out = append(out, t)
	}
	names := make([]string, 0, len(r.sigs))
	for n := range r.sigs {
		if _, _, std := builtinPowerPins(n); !std {
			names = append(names, n)
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	for _, n := range names {
		sig := r.sigs[n]
		in, out2 := r.powerPins(n, "")
		t := FBType{Name: n, User: true, PowerIn: in, PowerOut: out2, Args: defaultUserArgs(sig, in), Prefix: instPrefix(n)}
		for _, p := range sig.inputs {
			t.Pins = append(t.Pins, Pin{Name: p.name, Type: p.typ, Dir: "in"})
		}
		for _, p := range sig.inouts {
			t.Pins = append(t.Pins, Pin{Name: p.name, Type: p.typ, Dir: "inout"})
		}
		for _, p := range sig.outputs {
			t.Pins = append(t.Pins, Pin{Name: p.name, Type: p.typ, Dir: "out"})
		}
		t.Detail = userDetail(t)
		out = append(out, t)
	}
	return out
}

// defaultUserArgs is a user block's starting argument list
// (docs/functions.md "Power pins in ladder"): the rung's power lands on the
// power-in pin, so it is never passed; other BOOL inputs are left unbound
// (FALSE until someone wires them); every non-BOOL input and every in-out
// gets a `_` placeholder to retag — the diagnostic on `_` says what is left.
// Outputs are captured with `Pin => Tag` when the author wants them.
func defaultUserArgs(sig fbSig, powerIn string) string {
	var parts []string
	for _, p := range sig.inputs {
		if strings.EqualFold(p.name, powerIn) || isBool(p.typ) {
			continue
		}
		parts = append(parts, p.name+" := _")
	}
	for _, p := range sig.inouts {
		parts = append(parts, p.name+" := _")
	}
	return strings.Join(parts, ", ")
}

// userDetail is the picker's one-line summary: power in → out.
func userDetail(t FBType) string {
	in, out := t.PowerIn, t.PowerOut
	if in == "" {
		in = "no power"
	}
	if out == "" {
		out = "passes through"
	}
	return in + " → " + out
}

// instPrefix names instances of a user block by its type's first letter,
// lowercased: MotorStarter → m1, RateOfChange → r1.
func instPrefix(typ string) string {
	for _, r := range typ {
		if unicode.IsLetter(r) {
			return strings.ToLower(string(r))
		}
	}
	return "fb"
}
