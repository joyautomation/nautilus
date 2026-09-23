package l5x

import (
	"fmt"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/ld"
)

// Mapping RLL onto the nautilus ladder render model.
//
// This is a RENDERING, not a translation. Nothing here claims a Logix rung
// and a nautilus rung compute the same thing — the reader draws the rung
// Logix exported, so the ladder viewer, hover and the revision diff work
// on Allen-Bradley code. That is why the mapping can afford to be
// generous: an instruction nautilus has never heard of still draws as a
// box with its operands, which is exactly what Studio 5000 draws.
//
// Four element shapes carry all of RLL:
//
//	XIC / XIO          a contact, negated for XIO
//	OTE / OTL / OTU    a coil, mode "" / "S" / "R"
//	timers, counters   a function block: instance, type, remaining operands
//	everything else    a function box: mnemonic and operands, verbatim
//
// The rung's coil zone is its trailing run of output coils — including the
// `[OTE(a) ,OTE(b)]` branch that is how Logix writes parallel outputs.
// Anything else stays in the condition zone where it was written, because
// that is where Logix draws it: a MOV or a TON is a box ON the rung, not a
// coil at the right rail.

// coilMode maps the three output-bit instructions to a ladder coil mode.
var coilMode = map[string]string{
	"OTE": "",  // energize
	"OTL": "S", // latch
	"OTU": "R", // unlatch
}

// blockInstr are the instructions whose first operand is a structure the
// instruction owns — a timer, a counter, a message. They draw as a block
// with an instance name, the way a nautilus TON does, rather than as a
// function box.
var blockInstr = map[string]bool{
	"TON": true, "TOF": true, "RTO": true, "TONR": true, "TOFR": true, "RTOR": true,
	"CTU": true, "CTD": true, "CTUD": true,
	"MSG": true, "PID": true, "PIDE": true, "SFR": true,
}

// LadderOptions selects what a Ladder call draws.
type LadderOptions struct {
	// Routine selects one routine as "Program/Routine", or just
	// "Routine" when the name is unambiguous. Empty draws every RLL
	// routine in the export, each as its own group.
	Routine string
	// AOIs includes the routines inside Add-On Instruction definitions.
	AOIs bool
}

// Ladder renders an export's RLL routines as the nautilus ladder model —
// the same JSON `naut ld graph` emits for a .ld file, so every
// consumer of it works unchanged on Logix code.
//
// Each routine becomes a Block and its rungs carry POU, so a viewer groups
// them under headings. Rungs keep the line they occupy in the L5X itself:
// the export is the source file here, and click-to-source and the
// revision diff both address rungs by line.
func Ladder(f *File, opts LadderOptions) (*ld.Model, error) {
	if f == nil || f.Controller == nil {
		return nil, fmt.Errorf("l5x: no controller to read routines from")
	}
	var all []owned
	for _, p := range f.Controller.Programs {
		for _, r := range p.Routines {
			all = append(all, owned{p.Name, r})
		}
	}
	if opts.AOIs {
		for _, a := range f.Controller.AOIs {
			for _, r := range a.Routines {
				all = append(all, owned{a.Name, r})
			}
		}
	}

	var picked []owned
	var skipped []string
	for _, o := range all {
		if o.r.Type != "RLL" {
			skipped = append(skipped, o.owner+"/"+o.r.Name+" ("+o.r.Type+")")
			continue
		}
		if opts.Routine != "" && !routineMatches(o.owner, o.r.Name, opts.Routine) {
			continue
		}
		picked = append(picked, o)
	}
	if len(picked) == 0 {
		if opts.Routine != "" {
			return nil, fmt.Errorf("l5x: no RLL routine matches %q (have: %s)",
				opts.Routine, strings.Join(routineNames(all), ", "))
		}
		return nil, fmt.Errorf("l5x: no RLL routines in this export (have: %s)",
			strings.Join(skipped, ", "))
	}

	name := f.TargetName
	if name == "" {
		name = f.Controller.Name
	}
	m := &ld.Model{Name: name, Rungs: []ld.Rung{}}
	single := len(picked) == 1
	for _, o := range picked {
		pou := ""
		if !single {
			pou = o.owner + "/" + o.r.Name
			end := o.r.Line
			if n := len(o.r.Rungs); n > 0 {
				end = o.r.Rungs[n-1].Line
			}
			m.Blocks = append(m.Blocks, ld.Block{Name: pou, Line: o.r.Line, EndLine: end})
		}
		for _, rg := range o.r.Rungs {
			rung, err := rungModel(rg, pou)
			if err != nil {
				return nil, fmt.Errorf("%s/%s rung %d: %w", o.owner, o.r.Name, rg.Number, err)
			}
			m.Rungs = append(m.Rungs, rung)
		}
	}
	return m, nil
}

// routineMatches accepts "Program/Routine" or a bare routine name.
func routineMatches(owner, name, sel string) bool {
	if strings.EqualFold(sel, name) {
		return true
	}
	return strings.EqualFold(sel, owner+"/"+name)
}

// owned pairs a routine with the program or AOI that declares it.
type owned struct {
	owner string
	r     *Routine
}

func routineNames(all []owned) []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range all {
		n := o.owner + "/" + o.r.Name
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// rungModel turns one rung's neutral text into a ladder rung.
func rungModel(rg Rung, pou string) (ld.Rung, error) {
	terms, err := ParseRung(rg.Text)
	if err != nil {
		return ld.Rung{}, err
	}
	body, coils := splitCoils(terms)
	out := ld.Rung{
		Name:     fmt.Sprintf("%d", rg.Number),
		Comment:  rg.Comment,
		Line:     rg.Line,
		EndLine:  rg.Line,
		Elements: elements(body),
		Coils:    coilElements(coils),
		POU:      pou,
	}
	if out.Elements == nil {
		out.Elements = []ld.Element{}
	}
	if out.Coils == nil {
		out.Coils = []ld.Element{}
	}
	return out, nil
}

// splitCoils peels the rung's trailing output zone off its condition zone.
//
// A term joins the coil zone when it is an output-bit instruction, or a
// branch whose every leg is a single one — `XIC(a)[OTE(b) ,OTE(c)]`, which
// is how Logix writes two coils in parallel. The scan stops at the first
// term that is neither, so a MOV or a TON stays where it was written.
func splitCoils(terms []Term) (body, coils []Term) {
	i := len(terms)
	for i > 0 && isCoilZone(terms[i-1]) {
		i--
	}
	return terms[:i], terms[i:]
}

func isCoilZone(t Term) bool {
	if t.Instr != nil {
		_, ok := coilMode[strings.ToUpper(t.Instr.Mnemonic)]
		return ok
	}
	if len(t.Legs) == 0 {
		return false
	}
	for _, leg := range t.Legs {
		if len(leg) != 1 || leg[0].Instr == nil {
			return false
		}
		if _, ok := coilMode[strings.ToUpper(leg[0].Instr.Mnemonic)]; !ok {
			return false
		}
	}
	return true
}

// coilElements flattens the coil zone, opening a parallel-output branch
// into the several coils it draws as.
func coilElements(terms []Term) []ld.Element {
	var out []ld.Element
	for _, t := range terms {
		if t.Instr != nil {
			out = append(out, coilElement(t.Instr))
			continue
		}
		for _, leg := range t.Legs {
			out = append(out, coilElement(leg[0].Instr))
		}
	}
	return out
}

func coilElement(in *Instr) ld.Element {
	el := ld.Element{Kind: "coil", Mode: coilMode[strings.ToUpper(in.Mnemonic)]}
	if len(in.Args) > 0 {
		el.Ref = in.Args[0]
	}
	return el
}

// elements maps a condition-zone series.
func elements(terms []Term) []ld.Element {
	var out []ld.Element
	for _, t := range terms {
		if t.Instr == nil {
			legs := make([][]ld.Element, 0, len(t.Legs))
			for _, leg := range t.Legs {
				e := elements(leg)
				if e == nil {
					e = []ld.Element{}
				}
				legs = append(legs, e)
			}
			out = append(out, ld.Element{Kind: "branch", Legs: legs})
			continue
		}
		out = append(out, element(t.Instr))
	}
	return out
}

// element maps one instruction to its drawable shape.
func element(in *Instr) ld.Element {
	m := strings.ToUpper(in.Mnemonic)
	switch m {
	case "XIC", "XIO":
		el := ld.Element{Kind: "contact", Neg: m == "XIO"}
		if len(in.Args) > 0 {
			el.Ref = in.Args[0]
		}
		return el
	}
	// An output coil that is NOT in the trailing zone — Logix allows a
	// coil mid-rung with logic continuing past it. The coil zone is a
	// property of the layout, so mid-rung it draws as the box it is.
	if _, ok := coilMode[m]; ok {
		return ld.Element{Kind: "fn", Fn: in.Mnemonic, Args: joinArgs(in.Args)}
	}
	if blockInstr[m] && len(in.Args) > 0 {
		return ld.Element{
			Kind: "fb",
			Inst: in.Args[0],
			Type: in.Mnemonic,
			Args: joinArgs(in.Args[1:]),
		}
	}
	return ld.Element{Kind: "fn", Fn: in.Mnemonic, Args: joinArgs(in.Args)}
}

// joinArgs renders operands the way the viewer shows them: comma-space
// separated, with Logix's "?" for an unset operand left as written.
func joinArgs(args []string) string {
	return strings.Join(args, ", ")
}
