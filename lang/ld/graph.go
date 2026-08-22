package ld

import (
	"fmt"
	"strings"
)

// The ladder render model: rung-structured, because LD layout is CANONICAL
// — rungs stack top to bottom, elements read left to right, branch legs
// stack inside their rung. No coordinates, no layout block; the drawing is
// a pure function of the source, which is exactly what makes ladder ladder.

// Model is the JSON the diagram preview consumes (`nautilus ld graph`).
type Model struct {
	Name     string    `json:"name"`
	Vars     []VarDecl `json:"vars,omitempty"`
	Rungs    []Rung    `json:"rungs"`
	Comments []Comment `json:"comments,omitempty"`
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
}

// Rung is one network: a series of condition elements, then its coils.
type Rung struct {
	Name     string    `json:"name"`
	Comment  string    `json:"comment,omitempty"` // the header's (* … *) text
	Line     int       `json:"line"`              // 1-based source line of the RUNG header
	EndLine  int       `json:"endLine"`           // 1-based last line of the rung's body
	Elements []Element `json:"elements"`
	Coils    []Element `json:"coils"`
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

// Graph parses LD source into the render model.
func Graph(src string) (*Model, error) {
	m := &Model{Name: pouName(src)}
	m.Vars = scanVars(src)

	lines := strings.Split(src, "\n")
	inLD := false
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
		r := Rung{Name: rung.name, Comment: rung.comment, Line: rung.line, EndLine: end}
		condEnd := len(elems)
		for condEnd > 0 {
			if _, ok := elems[condEnd-1].(coilEl); ok {
				condEnd--
				continue
			}
			break
		}
		r.Elements = toElements(elems[:condEnd])
		r.Coils = toElements(elems[condEnd:])
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
		case !inLD && ldStartRe.MatchString(raw):
			inLD = true
		case inLD && ldEndRe.MatchString(raw):
			flushCom()
			if err := flush(); err != nil {
				return nil, err
			}
			inLD = false
		case inLD:
			if mm := rungRe.FindStringSubmatch(raw); mm != nil {
				flushCom()
				if err := flush(); err != nil {
					return nil, err
				}
				name := mm[1]
				if name == "" {
					name = fmt.Sprintf("rung%d", n)
				}
				rung = &rungParse{name: name, comment: mm[2], line: n}
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

func toElements(elems []any) []Element {
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
			in, pOut := powerPins(x.typ)
			out = append(out, Element{
				Kind: "fb", Inst: x.inst, Type: x.typ, Args: x.args,
				PowerIn: in, PowerOut: pOut,
			})
		case branch:
			b := Element{Kind: "branch"}
			for _, leg := range x.legs {
				b.Legs = append(b.Legs, toElements(leg))
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

// scanVars reads header declarations line-based, mirroring lang/fbd's vars
// list: every `name : TYPE [:= init];` inside VAR* sections.
func scanVars(src string) []VarDecl {
	var out []VarDecl
	section := ""
	lines := strings.Split(src, "\n")
	for i, raw := range lines {
		t := strings.TrimSpace(raw)
		u := strings.ToUpper(t)
		switch {
		case u == "VAR" || strings.HasPrefix(u, "VAR_"):
			section = strings.Fields(u)[0]
			continue
		case u == "END_VAR":
			section = ""
			continue
		}
		if section == "" || t == "" || strings.HasPrefix(t, "(*") || strings.HasPrefix(t, "//") {
			continue
		}
		decl := strings.TrimSuffix(t, ";")
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
		out = append(out, VarDecl{Name: name, Type: rest, Init: init, Section: section, Line: i + 1})
	}
	return out
}
