package ld

import (
	"fmt"
	"regexp"
	"strings"
)

var endVarRe = regexp.MustCompile(`(?i)END_VAR`)

// The ladder render model: rung-structured, because LD layout is CANONICAL
// — rungs stack top to bottom, elements read left to right, branch legs
// stack inside their rung. No coordinates, no layout block; the drawing is
// a pure function of the source, which is exactly what makes ladder ladder.

// Model is the JSON the diagram preview consumes (`nautilus ld graph`).
//
// A file may hold more than one POU: FUNCTION_BLOCK definitions whose
// bodies are ladder, with or without a PROGRAM alongside them. Rungs and
// declarations stay in one flat, source-ordered list — an editor that
// knows nothing about blocks still draws every rung — and each carries the
// POU it belongs to ("" for the file's PROGRAM). Blocks lists the
// FUNCTION_BLOCK POUs so a viewer can group the rungs under headings.
type Model struct {
	Name     string    `json:"name"`
	Vars     []VarDecl `json:"vars,omitempty"`
	Rungs    []Rung    `json:"rungs"`
	Comments []Comment `json:"comments,omitempty"`
	Blocks   []Block   `json:"blocks,omitempty"`

	// res carries the FB signatures this parse resolved, so an edit that
	// inserts a block places its power pins the same way the compiler will.
	res *resolver
}

// Block is one FUNCTION_BLOCK POU defined in the file: a rung group.
type Block struct {
	Name    string `json:"name"`
	Line    int    `json:"line"`    // 1-based line of FUNCTION_BLOCK
	EndLine int    `json:"endLine"` // 1-based line of END_FUNCTION_BLOCK
	Pins    []Pin  `json:"pins,omitempty"`
}

// Pin is one VAR_INPUT / VAR_OUTPUT declaration on a Block, in order.
type Pin struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Dir  string `json:"dir"` // "in" | "out"
}

// Comment is a run of consecutive full-line // comments inside the LD
// block — a diagram note, rendered above whatever follows it (mirroring
// FBD's comment nodes). Text joins the lines with \n.
type Comment struct {
	Line    int    `json:"line"`
	EndLine int    `json:"endLine"`
	Text    string `json:"text"`
}

// VarDecl mirrors the FBD model's header declaration (the ladder preview
// resolves array bounds and section badges the same way).
type VarDecl struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Init    string `json:"init,omitempty"`
	Section string `json:"section"`
	Line    int    `json:"line"`
	POU     string `json:"pou,omitempty"` // owning FUNCTION_BLOCK, "" for the program
}

// Rung is one network: a series of condition elements, then its coils.
type Rung struct {
	Name     string    `json:"name"`
	Comment  string    `json:"comment,omitempty"` // the header's (* … *) text
	Line     int       `json:"line"`              // 1-based source line of the RUNG header
	EndLine  int       `json:"endLine"`           // 1-based last line of the rung's body
	Elements []Element `json:"elements"`
	Coils    []Element `json:"coils"`
	POU      string    `json:"pou,omitempty"` // owning FUNCTION_BLOCK, "" for the program
	// Inline marks a one-line rung — elements on the RUNG header itself.
	// An edit rewrites it in place, keeping the author's one-line style.
	Inline  bool `json:"inline,omitempty"`
	bodyCol int  // 1-based column the inline elements start at
}

// Element is one drawable ladder element.
type Element struct {
	Kind string `json:"kind"` // "contact" | "edge" | "branch" | "fn" | "fb" | "coil"
	Ref  string `json:"ref,omitempty"`
	Neg  bool   `json:"neg,omitempty"`  // contact / fn: negated
	Mode string `json:"mode,omitempty"` // coil: "" | "S" | "R" | "P" | "N"; edge: "P" | "N"
	Fn   string `json:"fn,omitempty"`
	Args string `json:"args,omitempty"`
	Inst string `json:"inst,omitempty"`
	Type string `json:"type,omitempty"`
	// The boolean pins a rung's power enters/leaves an FB on.
	PowerIn  string      `json:"powerIn,omitempty"`
	PowerOut string      `json:"powerOut,omitempty"`
	Legs     [][]Element `json:"legs,omitempty"`
}

// Graph parses LD source into the render model. libs are project library
// sources whose FUNCTION_BLOCK signatures are in scope, so an FB element
// shows the pins the rung's power actually uses.
func Graph(src string, libs ...string) (*Model, error) {
	m := &Model{Name: pouName(src)}
	m.Vars = scanVars(src)
	m.Blocks = scanBlocks(src)
	res := newResolver(src, libs)
	m.res = res

	lines := strings.Split(src, "\n")
	inLD := false
	pou := "" // the FUNCTION_BLOCK currently open, "" inside the PROGRAM
	var rung *rungParse
	lastBody := 0 // last line holding the current rung's elements
	flush := func() error {
		if rung == nil {
			return nil
		}
		p := &rungTok{src: rung.text, line: rung.line}
		elems, err := p.series(false)
		if err != nil {
			return err
		}
		if p.peek() != "" {
			return p.errf("unexpected %q", p.peek())
		}
		end := lastBody
		if end < rung.line {
			end = rung.line
		}
		r := Rung{Name: rung.name, Comment: rung.comment, Line: rung.line, EndLine: end, POU: rung.pou}
		if strings.TrimSpace(rung.headText) != "" {
			r.Inline = true
			r.bodyCol = rung.bodyCol
		}
		condEnd := len(elems)
		for condEnd > 0 {
			if _, ok := elems[condEnd-1].(coilEl); ok {
				condEnd--
				continue
			}
			break
		}
		r.Elements = toElements(elems[:condEnd], res)
		r.Coils = toElements(elems[condEnd:], res)
		m.Rungs = append(m.Rungs, r)
		rung = nil
		return nil
	}
	var com *Comment
	flushCom := func() {
		if com != nil {
			m.Comments = append(m.Comments, *com)
			com = nil
		}
	}
	for i, raw := range lines {
		n := i + 1
		switch {
		case !inLD && fbStartRe.MatchString(raw):
			pou = fbStartRe.FindStringSubmatch(raw)[1]
		case !inLD && fbEndRe.MatchString(raw):
			pou = ""
		case !inLD && ldStartRe.MatchString(raw):
			inLD = true
		case inLD && ldEndRe.MatchString(raw):
			flushCom()
			if err := flush(); err != nil {
				return nil, err
			}
			inLD = false
		case inLD:
			if idx := rungRe.FindStringSubmatchIndex(raw); idx != nil {
				flushCom()
				if err := flush(); err != nil {
					return nil, err
				}
				mm := rungRe.FindStringSubmatch(raw)
				name := mm[1]
				if name == "" {
					name = fmt.Sprintf("rung%d", n)
				}
				rung = &rungParse{name: name, comment: mm[2], line: n, pou: pou,
					text: strings.TrimSpace(mm[3]), headText: strings.TrimSpace(mm[3]),
					bodyCol: idx[6] + 1}
				lastBody = n
			} else if t := strings.TrimSpace(raw); strings.HasPrefix(t, "//") {
				text := strings.TrimSpace(strings.TrimPrefix(t, "//"))
				if com == nil {
					com = &Comment{Line: n, EndLine: n, Text: text}
				} else {
					com.EndLine = n
					com.Text += "\n" + text
				}
			} else if t != "" {
				flushCom()
				if rung == nil {
					return nil, fmt.Errorf("ld: line %d: rung elements before any RUNG", n)
				}
				rung.text += " " + t
				lastBody = n
			} else {
				flushCom()
			}
		}
	}
	flushCom()
	if err := flush(); err != nil {
		return nil, err
	}
	return m, nil
}

func toElements(elems []any, res *resolver) []Element {
	out := make([]Element, 0, len(elems))
	for _, e := range elems {
		switch x := e.(type) {
		case contact:
			out = append(out, Element{Kind: "contact", Ref: x.ref, Neg: x.neg})
		case fnEl:
			out = append(out, Element{Kind: "fn", Fn: x.fn, Args: x.args, Neg: x.neg})
		case edgeEl:
			mode := "P"
			if !x.rise {
				mode = "N"
			}
			out = append(out, Element{Kind: "edge", Ref: x.ref, Mode: mode})
		case fbEl:
			in, pOut := res.powerPins(x.typ, x.args)
			out = append(out, Element{
				Kind: "fb", Inst: x.inst, Type: x.typ, Args: x.args,
				PowerIn: in, PowerOut: pOut,
			})
		case branch:
			b := Element{Kind: "branch"}
			for _, leg := range x.legs {
				b.Legs = append(b.Legs, toElements(leg, res))
			}
			out = append(out, b)
		case coilEl:
			out = append(out, Element{Kind: "coil", Ref: x.ref, Mode: x.mode})
		}
	}
	return out
}

func pouName(src string) string {
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(t), "PROGRAM ") {
			f := strings.Fields(t)
			if len(f) >= 2 {
				return f[1]
			}
		}
	}
	return ""
}

// scanBlocks lists the FUNCTION_BLOCK POUs the file defines, with their
// pins — the rung groups an editor draws under their own headings.
func scanBlocks(src string) []Block {
	var out []Block
	lines := strings.Split(src, "\n")
	start := -1
	name := ""
	for i, l := range lines {
		if start == -1 {
			if m := fbStartRe.FindStringSubmatch(l); m != nil {
				name, start = m[1], i
			}
			continue
		}
		if fbEndRe.MatchString(l) {
			sig := scanFBBody(name, strings.Join(lines[start+1:i], "\n"))
			b := Block{Name: name, Line: start + 1, EndLine: i + 1}
			for _, p := range sig.inputs {
				b.Pins = append(b.Pins, Pin{Name: p.name, Type: p.typ, Dir: "in"})
			}
			for _, p := range sig.outputs {
				b.Pins = append(b.Pins, Pin{Name: p.name, Type: p.typ, Dir: "out"})
			}
			out = append(out, b)
			start, name = -1, ""
		}
	}
	return out
}

// scanVars reads header declarations line-based, mirroring lang/fbd's vars
// list: every `name : TYPE [:= init];` inside VAR* sections. Declarations
// inside a FUNCTION_BLOCK carry that block's name in POU.
func scanVars(src string) []VarDecl {
	var out []VarDecl
	section := ""
	pou := ""
	lines := strings.Split(src, "\n")
	for i, raw := range lines {
		t := strings.TrimSpace(raw)
		u := strings.ToUpper(t)
		if section == "" {
			if m := fbStartRe.FindStringSubmatch(raw); m != nil {
				pou = m[1]
				continue
			}
			if fbEndRe.MatchString(raw) {
				pou = ""
				continue
			}
		}
		switch {
		case u == "VAR" || strings.HasPrefix(u, "VAR_"):
			section = strings.Fields(u)[0]
			// A compact section — `VAR_INPUT Start : BOOL; END_VAR` all on
			// one line — declares on the header line itself.
			rest := strings.TrimSpace(t[len(strings.Fields(t)[0]):])
			if rest == "" {
				continue
			}
			if loc := endVarRe.FindStringIndex(rest); loc != nil {
				rest = rest[:loc[0]]
				out = append(out, compactDecls(rest, section, pou, i+1)...)
				section = ""
				continue
			}
			out = append(out, compactDecls(rest, section, pou, i+1)...)
			continue
		case u == "END_VAR":
			section = ""
			continue
		}
		if section == "" || t == "" || strings.HasPrefix(t, "(*") || strings.HasPrefix(t, "//") {
			continue
		}
		if loc := endVarRe.FindStringIndex(t); loc != nil {
			out = append(out, compactDecls(t[:loc[0]], section, pou, i+1)...)
			section = ""
			continue
		}
		out = append(out, compactDecls(t, section, pou, i+1)...)
	}
	return out
}

// compactDecls reads the `name : TYPE [:= init];` declarations on one line —
// one, or several separated by `;`.
func compactDecls(text, section, pou string, line int) []VarDecl {
	var out []VarDecl
	for _, decl := range strings.Split(text, ";") {
		decl = strings.TrimSpace(decl)
		if decl == "" {
			continue
		}
		parts := strings.SplitN(decl, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		rest := strings.TrimSpace(parts[1])
		init := ""
		if idx := strings.Index(rest, ":="); idx >= 0 {
			init = strings.TrimSpace(rest[idx+2:])
			rest = strings.TrimSpace(rest[:idx])
		}
		if name == "" || rest == "" {
			continue
		}
		out = append(out, VarDecl{Name: name, Type: rest, Init: init, Section: section, Line: line, POU: pou})
	}
	return out
}
