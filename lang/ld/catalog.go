package ld

import (
	"strings"

	"github.com/joyautomation/nautilus/lang/fbcatalog"
)

// The block catalog: every FUNCTION_BLOCK type a rung in this file can
// instantiate — the standard blocks ladder places power on, then every user
// block in scope (this file's own and the project libraries' — the same
// prelude `naut check` composes, in any language). The list itself is the
// shared one (lang/fbcatalog, which `naut fbd graph` sends too); this file
// adds what only ladder needs: the power pins, and a starting argument list
// with the rung's power left off it.

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
	// Pins in declaration order: dir "in" | "out" | "inout".
	Pins []Pin `json:"pins,omitempty"`
	// Args is the starting argument text for a fresh insert.
	Args string `json:"args,omitempty"`
	// Prefix names a fresh instance: the first free <prefix><n>.
	Prefix string `json:"prefix"`
}

// ladderArgs are the standard blocks' ladder defaults: the power pin is the
// rung, so only the preset is left to fill.
var ladderArgs = map[string]string{
	"TON": "PT := T#1S", "TOF": "PT := T#1S", "TP": "PT := T#1S",
	"CTU": "PV := 10", "CTD": "PV := 10", "CTUD": "PV := 10",
}

// catalog lists the standard blocks a rung can power (fixed order), then
// the user blocks in scope, sorted by name. A block defined in this file
// shadows a library block of the same name, as it does in the compiler.
func (r *resolver) catalog() []FBType {
	var out []FBType
	for _, t := range fbcatalog.Standard() {
		in, pOut, ok := builtinPowerPins(t.Name)
		if !ok {
			continue // PID and the like: no pin means "run" — ST/FBD only
		}
		out = append(out, FBType{Name: t.Name, Detail: t.Detail, PowerIn: in, PowerOut: pOut,
			Pins: t.Pins, Args: ladderArgs[t.Name], Prefix: t.Prefix})
	}
	for _, n := range r.scope.UserNames() {
		sig, _ := r.scope.Lookup(n)
		in, pOut := r.powerPins(n, "")
		t := FBType{Name: n, User: true, PowerIn: in, PowerOut: pOut, Args: defaultUserArgs(sig, in),
			Pins: sig.Pins(), Prefix: fbcatalog.Prefix(n)}
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
func defaultUserArgs(sig fbcatalog.Sig, powerIn string) string {
	var parts []string
	for _, p := range sig.Inputs {
		if strings.EqualFold(p.Name, powerIn) || isBool(p.Type) {
			continue
		}
		parts = append(parts, p.Name+" := _")
	}
	for _, p := range sig.InOuts {
		parts = append(parts, p.Name+" := _")
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
