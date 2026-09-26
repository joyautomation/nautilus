// Package fbcatalog is the function-block catalog the graphical editors'
// palettes list: every FUNCTION_BLOCK type a POU can instantiate — the
// standard blocks lang/ir registers, then every user block in scope (the
// file's own and the project libraries', the same prelude `naut check`
// composes, in any language) — each with its pins, names, types and
// directions. `naut ld graph` and `naut fbd graph` both send it with their
// models, so the ladder's FB picker and the FBD palette offer the same
// blocks with the same pins.
//
// The user half is a TEXTUAL scan: it runs before any parse, over ST, LD
// and FBD source alike, and a shape it cannot read simply yields no
// signature (the real compiler reports whatever is wrong).
package fbcatalog

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Pin is one declared pin: dir "in" | "out" | "inout".
type Pin struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Dir  string `json:"dir"`
}

// Sig is a FUNCTION_BLOCK's signature: its pins in declaration order.
type Sig struct {
	Name    string
	Inputs  []Pin // VAR_INPUT
	Outputs []Pin // VAR_OUTPUT
	InOuts  []Pin // VAR_IN_OUT (always bound by a call)
}

// Pins lists the signature inputs, then in-outs, then outputs.
func (s Sig) Pins() []Pin {
	out := make([]Pin, 0, len(s.Inputs)+len(s.InOuts)+len(s.Outputs))
	out = append(out, s.Inputs...)
	out = append(out, s.InOuts...)
	return append(out, s.Outputs...)
}

// Type is one insertable block type.
type Type struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	// User marks a project block (this file or a library), not a standard one.
	User bool `json:"user,omitempty"`
	// Pins: inputs, then in-outs, then outputs, each in declaration order.
	Pins []Pin `json:"pins,omitempty"`
	// Args is the starting argument text for a fresh insert: every input
	// and in-out as an open `_` placeholder (OpenArgs).
	Args string `json:"args,omitempty"`
	// Prefix names a fresh instance: the first free <prefix><n>.
	Prefix string `json:"prefix"`
}

// standard is the palette order and wording of the standard blocks; any
// block lang/ir registers that is not listed here still appears, after
// them, by name.
var standard = []struct{ name, detail, prefix string }{
	{"TON", "on-delay timer", "t"},
	{"TOF", "off-delay timer", "t"},
	{"TP", "pulse timer", "t"},
	{"CTU", "count up", "c"},
	{"CTD", "count down", "c"},
	{"CTUD", "count up/down", "c"},
	{"R_TRIG", "rising edge", "rt"},
	{"F_TRIG", "falling edge", "ft"},
	{"SR", "set-dominant latch", "sr"},
	{"RS", "reset-dominant latch", "rs"},
	{"PID", "closed-loop control", "pid"},
}

// Standard lists the built-in blocks lang/ir registers, with their pins.
func Standard() []Type {
	seen := map[string]bool{}
	var out []Type
	add := func(name, detail, prefix string) {
		def, ok := ir.FBs[name]
		if !ok || seen[name] {
			return
		}
		seen[name] = true
		t := Type{Name: name, Detail: detail, Prefix: prefix}
		for _, s := range def.Inputs {
			t.Pins = append(t.Pins, Pin{Name: s.Name, Type: s.Type.String(), Dir: "in"})
		}
		for _, s := range def.InOuts {
			t.Pins = append(t.Pins, Pin{Name: s.Name, Type: s.Type.String(), Dir: "inout"})
		}
		for _, s := range def.Outputs {
			t.Pins = append(t.Pins, Pin{Name: s.Name, Type: s.Type.String(), Dir: "out"})
		}
		t.Args = OpenArgs(t.Pins)
		out = append(out, t)
	}
	for _, s := range standard {
		add(s.name, s.detail, s.prefix)
	}
	rest := make([]string, 0, len(ir.FBs))
	for n := range ir.FBs {
		if !seen[n] {
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	for _, n := range rest {
		add(n, "", Prefix(n))
	}
	return out
}

// IsStandard reports whether typ names a built-in block.
func IsStandard(typ string) bool {
	_, ok := ir.FBs[strings.ToUpper(typ)]
	return ok
}

// OpenArgs is a fresh insert's argument list: every input and in-out bound
// to `_`, the open placeholder the diagram shows as an unwired pin (and
// the compiler flags until something is wired to it).
func OpenArgs(pins []Pin) string {
	var parts []string
	for _, p := range pins {
		if p.Dir != "out" {
			parts = append(parts, p.Name+" := _")
		}
	}
	return strings.Join(parts, ", ")
}

// Prefix names instances of a user block by its type's first letter,
// lowercased: MotorStarter → m1, RateOfChange → r1.
func Prefix(typ string) string {
	for _, r := range typ {
		if unicode.IsLetter(r) {
			return strings.ToLower(string(r))
		}
	}
	return "fb"
}

// Scope is the user blocks in scope for one file: its own and its
// libraries'.
type Scope struct{ sigs map[string]Sig }

// NewScope scans src plus any library sources for user FB signatures.
// Later sources win, so the file being compiled shadows a library — the
// same precedence the ST compiler's in-file FB table has.
func NewScope(src string, libs []string) *Scope {
	s := &Scope{sigs: map[string]Sig{}}
	for _, l := range libs {
		for _, sig := range ScanSigs(l) {
			s.sigs[sig.Name] = sig
		}
	}
	for _, sig := range ScanSigs(src) {
		s.sigs[sig.Name] = sig
	}
	return s
}

// Lookup finds a user FB signature by type name (case-insensitively, the
// way IEC treats identifiers).
func (s *Scope) Lookup(typ string) (Sig, bool) {
	if s == nil {
		return Sig{}, false
	}
	if sig, ok := s.sigs[typ]; ok {
		return sig, true
	}
	for name, sig := range s.sigs {
		if strings.EqualFold(name, typ) {
			return sig, true
		}
	}
	return Sig{}, false
}

// UserNames lists the user blocks in scope, sorted case-insensitively. A
// user block named like a standard one is left out (the standard wins in
// the compiler's lookup too).
func (s *Scope) UserNames() []string {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.sigs))
	for n := range s.sigs {
		if !IsStandard(n) {
			names = append(names, n)
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return names
}

// Catalog lists the standard blocks (fixed order) then the user blocks in
// scope, sorted by name, every one with its pins and open-pin arguments.
func (s *Scope) Catalog() []Type {
	out := Standard()
	for _, n := range s.UserNames() {
		sig := s.sigs[n]
		t := Type{Name: n, User: true, Pins: sig.Pins(), Prefix: Prefix(n)}
		t.Args = OpenArgs(t.Pins)
		t.Detail = userDetail(sig)
		out = append(out, t)
	}
	return out
}

// userDetail is the picker's one-line summary of a user block's pins.
func userDetail(sig Sig) string {
	names := func(ps []Pin) string {
		var n []string
		for _, p := range ps {
			n = append(n, p.Name)
		}
		return strings.Join(n, ", ")
	}
	in := names(append(append([]Pin{}, sig.Inputs...), sig.InOuts...))
	out := names(sig.Outputs)
	if in == "" {
		in = "–"
	}
	if out == "" {
		out = "–"
	}
	return in + " → " + out
}

var (
	fbStartRe = regexp.MustCompile(`(?i)^\s*FUNCTION_BLOCK\s+([A-Za-z_][A-Za-z0-9_]*)`)
	fbEndRe   = regexp.MustCompile(`(?i)^\s*END_FUNCTION_BLOCK\s*$`)
	// A VAR_INPUT / VAR_OUTPUT / VAR_IN_OUT section anywhere in a block's
	// header text, declarations and END_VAR possibly sharing its line.
	varSectionRe = regexp.MustCompile(`(?is)\b(VAR_INPUT|VAR_OUTPUT|VAR_IN_OUT)\b(.*?)\bEND_VAR\b`)
	declNameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ScanSigs reads every `FUNCTION_BLOCK … END_FUNCTION_BLOCK` in source and
// returns its pins. Boundaries are found on the COMMENT-STRIPPED text, so a
// `(* ... *)` block comment whose text happens to start a line with one of
// those keywords is never mistaken for a declaration; bodies are sliced
// from the ORIGINAL source and ScanBody strips comments itself.
func ScanSigs(src string) []Sig {
	var out []Sig
	lines := strings.Split(src, "\n")
	stripped := strings.Split(StripComments(src), "\n")
	name, start := "", -1
	for i := range lines {
		if start == -1 {
			if m := fbStartRe.FindStringSubmatch(stripped[i]); m != nil {
				name, start = m[1], i+1
			}
			continue
		}
		if fbEndRe.MatchString(stripped[i]) {
			out = append(out, ScanBody(name, strings.Join(lines[start:i], "\n")))
			name, start = "", -1
		}
	}
	if start != -1 {
		out = append(out, ScanBody(name, strings.Join(lines[start:], "\n")))
	}
	return out
}

// ScanBody reads one block's pin sections out of its body text (the lines
// between FUNCTION_BLOCK and END_FUNCTION_BLOCK).
func ScanBody(name, body string) Sig {
	sig := Sig{Name: name}
	// A block's body may be ladder or a netlist; the pin sections precede
	// it, and stripping comments first keeps a commented-out END_VAR (or a
	// VAR_INPUT mentioned only in prose) out of the way.
	body = StripComments(body)
	for _, m := range varSectionRe.FindAllStringSubmatch(body, -1) {
		switch strings.ToUpper(m[1]) {
		case "VAR_INPUT":
			sig.Inputs = append(sig.Inputs, parseDecls(m[2], "in")...)
		case "VAR_OUTPUT":
			sig.Outputs = append(sig.Outputs, parseDecls(m[2], "out")...)
		default:
			sig.InOuts = append(sig.InOuts, parseDecls(m[2], "inout")...)
		}
	}
	return sig
}

// parseDecls reads `a, b : TYPE := init;` declarations out of one VAR
// section's text — several to a line, or several lines to a declaration.
func parseDecls(text, dir string) []Pin {
	var out []Pin
	for _, d := range strings.Split(text, ";") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		names, typ, ok := strings.Cut(d, ":")
		if !ok {
			continue
		}
		if i := strings.Index(typ, ":="); i >= 0 {
			typ = typ[:i]
		}
		typ = strings.TrimSpace(typ)
		if typ == "" {
			continue
		}
		for _, n := range strings.Split(names, ",") {
			n = strings.TrimSpace(n)
			if declNameRe.MatchString(n) {
				out = append(out, Pin{Name: n, Type: typ, Dir: dir})
			}
		}
	}
	return out
}
