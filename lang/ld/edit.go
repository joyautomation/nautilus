package ld

import (
	"fmt"
	"regexp"
	"strings"
)

// Structural edits for the ladder view, mirroring the FBD edit seam: the
// webview posts ops addressed at the render model, `nautilus ld edit`
// resolves them against a fresh parse, and the result comes back as text
// edits. The ladder twist: a rung is one line of text, so an op mutates
// the rung's MODEL and re-prints its body canonically — the rung-level
// rewrite IS the minimal edit, and formatting stays canonical for free.
//
// Element addressing: Path walks the rung's element tree — an index into
// the series, then for a branch [leg, index, leg, index …] as deep as
// needed. Coils are addressed with Coil (their index in the rung's coil
// zone). Inserts address the CONTAINING series with Path and a position
// with Index.

// EditOp is one structural edit.
type EditOp struct {
	Type string `json:"type"`
	Rung string `json:"rung,omitempty"` // rung name
	Path []int  `json:"path,omitempty"`
	Coil *int   `json:"coil,omitempty"`
	// setRef / insert payloads
	Ref    string `json:"ref,omitempty"`
	Neg    bool   `json:"neg,omitempty"`
	Mode   string `json:"mode,omitempty"` // coil "", "S", "R"
	Kind   string `json:"kind,omitempty"` // insert: contact | coil | edge | fn | fb | branch
	Fn     string `json:"fn,omitempty"`
	Inst   string `json:"inst,omitempty"`
	FbType string `json:"fbType,omitempty"`
	Args   string `json:"args,omitempty"`
	Index  int    `json:"index,omitempty"`
	// insert can carry a whole element (the webview's paste): branches keep
	// their legs, and fb instance names are uniquified against the program.
	Element *Element `json:"element,omitempty"`
	// rung ops
	Name  string `json:"name,omitempty"`
	After string `json:"after,omitempty"`
	// comment ops: setComment/addComment work on // runs (Comment indexes
	// Model.Comments); setRungComment rewrites the header's (* … *)
	Comment *int   `json:"comment,omitempty"`
	Text    string `json:"text,omitempty"`
	// declareVar payloads (Name is the variable): the header edits the
	// rung grammar can't express, so the diagram can introduce variables.
	VarType string `json:"varType,omitempty"`
	Section string `json:"section,omitempty"`
	// move targets (source is Rung + Path/Coil)
	ToRung  string `json:"toRung,omitempty"`
	ToPath  []int  `json:"toPath,omitempty"`
	ToIndex int    `json:"toIndex,omitempty"`
	ToCoil  bool   `json:"toCoil,omitempty"`
}

// TextEdit is a 1-based, end-exclusive replacement (mirrors lang/fbd).
type TextEdit struct {
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	EndLine int    `json:"endLine"`
	EndCol  int    `json:"endCol"`
	NewText string `json:"newText"`
}

var identOnly = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// refValid accepts what contacts/coils accept: a name with an optional
// accessor chain — judged by the rung tokenizer itself, so an edit can
// never write text the next parse rejects.
func refValid(s string) bool {
	t := &rungTok{src: s, line: 1}
	got, err := t.ident()
	return err == nil && got == strings.TrimSpace(s) && t.peek() == ""
}

// ApplyEdit resolves op against source and returns the text edits.
func ApplyEdit(src string, op EditOp) ([]TextEdit, error) {
	m, err := Graph(src)
	if err != nil {
		return nil, err
	}
	switch op.Type {
	case "addRung":
		return opAddRung(src, m, op)
	case "deleteRung":
		r, err := findRung(m, op.Rung)
		if err != nil {
			return nil, err
		}
		return []TextEdit{{Line: r.Line, Col: 1, EndLine: r.EndLine + 1, EndCol: 1}}, nil
	case "renameRung":
		return opRenameRung(src, m, op)
	case "move":
		return opMove(m, op)
	case "setComment":
		return opSetComment(m, op)
	case "addComment":
		return opAddComment(src, m, op)
	case "setRungComment":
		return opSetRungComment(src, m, op)
	case "declareVar":
		return opDeclareVar(src, m, op)
	case "deleteVar":
		return opDeleteVar(src, m, op)
	}

	r, err := findRung(m, op.Rung)
	if err != nil {
		return nil, err
	}
	switch op.Type {
	case "setRef":
		if !refValid(op.Ref) {
			return nil, fmt.Errorf("ld edit: %q is not a valid reference", op.Ref)
		}
		el, err := locate(r, op)
		if err != nil {
			return nil, err
		}
		if el.Kind != "contact" && el.Kind != "coil" && el.Kind != "edge" {
			return nil, fmt.Errorf("ld edit: only contacts, coils, and edges retag")
		}
		el.Ref = op.Ref
	case "toggleNeg":
		el, err := locate(r, op)
		if err != nil {
			return nil, err
		}
		if el.Kind != "contact" && el.Kind != "fn" {
			return nil, fmt.Errorf("ld edit: only contacts and function contacts toggle negation")
		}
		el.Neg = !el.Neg
	case "setCoilMode":
		el, err := locate(r, op)
		if err != nil {
			return nil, err
		}
		if el.Kind != "coil" {
			return nil, fmt.Errorf("ld edit: %s is not a coil", el.Kind)
		}
		if op.Mode != "" && op.Mode != "S" && op.Mode != "R" && op.Mode != "P" && op.Mode != "N" {
			return nil, fmt.Errorf("ld edit: coil mode must be \"\", S, R, P, or N")
		}
		el.Mode = op.Mode
	case "setArgs":
		el, err := locate(r, op)
		if err != nil {
			return nil, err
		}
		if el.Kind != "fn" && el.Kind != "fb" {
			return nil, fmt.Errorf("ld edit: only function contacts and blocks take arguments")
		}
		// A function contact can change its FUNCTION in the same gesture —
		// the diagram edits the whole call text (GT(...) → LE(...)).
		if op.Fn != "" && el.Kind == "fn" {
			if !identOnly.MatchString(op.Fn) {
				return nil, fmt.Errorf("ld edit: %q is not a valid function name", op.Fn)
			}
			el.Fn = strings.ToUpper(op.Fn)
		}
		el.Args = op.Args
	case "insert":
		if op.Element != nil {
			uniquifyInsts(m, op.Element)
		}
		if err := opInsert(r, op); err != nil {
			return nil, err
		}
	case "delete":
		if err := opDelete(r, op); err != nil {
			return nil, err
		}
	case "addLeg":
		el, err := locate(r, op)
		if err != nil {
			return nil, err
		}
		if el.Kind != "branch" {
			return nil, fmt.Errorf("ld edit: %s is not a branch", el.Kind)
		}
		el.Legs = append(el.Legs, []Element{{Kind: "contact", Ref: "_"}})
	case "wrapBranch":
		// Wrap the selected series element in a parallel branch: the element
		// becomes leg one, a `_` placeholder opens leg two (the classic
		// "add an OR path around this contact" gesture).
		if op.Coil != nil {
			return nil, fmt.Errorf("ld edit: coils don't branch — power always reaches the coil zone")
		}
		el, err := locate(r, op)
		if err != nil {
			return nil, err
		}
		wrapped := *el
		*el = Element{Kind: "branch", Legs: [][]Element{
			{wrapped},
			{{Kind: "contact", Ref: "_"}},
		}}
	default:
		return nil, fmt.Errorf("ld edit: unknown op %q", op.Type)
	}

	// The mutated rung re-prints canonically; the parse gate below keeps
	// the never-break-the-text guarantee.
	body := "    " + printRung(r)
	if _, err := parseRungText(body, r.Line); err != nil {
		return nil, fmt.Errorf("ld edit: refused — the result would not parse: %w", err)
	}
	return []TextEdit{{Line: r.Line + 1, Col: 1, EndLine: r.EndLine + 1, EndCol: 1, NewText: body + "\n"}}, nil
}

// opMove relocates an element: take it out of its source position, insert
// it at the target (another spot in the same rung, or a different rung).
// Index adjustment for same-series moves, and the re-parse gate on every
// touched rung, keep the drag gesture unable to corrupt the text.
func opMove(m *Model, op EditOp) ([]TextEdit, error) {
	from, err := findRung(m, op.Rung)
	if err != nil {
		return nil, err
	}
	toName := op.ToRung
	if toName == "" {
		toName = op.Rung
	}
	to, err := findRung(m, toName)
	if err != nil {
		return nil, err
	}

	// Extract the element from its source.
	var el Element
	if op.Coil != nil {
		if *op.Coil < 0 || *op.Coil >= len(from.Coils) {
			return nil, fmt.Errorf("ld edit: coil %d out of range", *op.Coil)
		}
		if len(from.Coils) == 1 && op.ToCoil && to == from {
			// Reordering a single coil is a no-op, not an orphaned rung.
			return nil, nil
		}
		if len(from.Coils) == 1 && (!op.ToCoil || to != from) {
			return nil, fmt.Errorf("ld edit: a rung needs at least one coil — move something in first")
		}
		el = from.Coils[*op.Coil]
		from.Coils = append(from.Coils[:*op.Coil], from.Coils[*op.Coil+1:]...)
	} else {
		if len(op.Path) == 0 {
			return nil, fmt.Errorf("ld edit: move needs a source path or coil")
		}
		series, err := locateSeries(from, op.Path[:len(op.Path)-1])
		if err != nil {
			return nil, err
		}
		i := op.Path[len(op.Path)-1]
		if i < 0 || i >= len(*series) {
			return nil, fmt.Errorf("ld edit: path %v out of range", op.Path)
		}
		el = (*series)[i]
		*series = append((*series)[:i], (*series)[i+1:]...)
	}

	// Same-rung fixups: the target was addressed against the pre-removal
	// tree, and removing the source shifts its later siblings down by one
	// — both inside the target path (when it walks past the removed
	// element) and in ToIndex (when the target IS the source's series).
	if to == from && op.Coil == nil {
		parent := op.Path[:len(op.Path)-1]
		srcIdx := op.Path[len(op.Path)-1]
		if len(op.ToPath) > len(parent) && eqInts(op.ToPath[:len(parent)], parent) {
			switch {
			case op.ToPath[len(parent)] == srcIdx:
				return nil, fmt.Errorf("ld edit: can't move an element into itself")
			case op.ToPath[len(parent)] > srcIdx:
				op.ToPath[len(parent)]--
			}
		}
		if eqInts(op.ToPath, parent) && op.ToIndex > srcIdx {
			op.ToIndex--
		}
	}

	// Insert at the target.
	if op.ToCoil {
		if el.Kind != "coil" {
			return nil, fmt.Errorf("ld edit: only coils live in the coil zone")
		}
		i := op.ToIndex
		if i < 0 || i > len(to.Coils) {
			i = len(to.Coils)
		}
		to.Coils = append(to.Coils[:i], append([]Element{el}, to.Coils[i:]...)...)
	} else {
		if el.Kind == "coil" {
			return nil, fmt.Errorf("ld edit: coils live at the rung's right end")
		}
		series, err := locateSeries(to, op.ToPath)
		if err != nil {
			return nil, err
		}
		i := op.ToIndex
		if i < 0 || i > len(*series) {
			i = len(*series)
		}
		*series = append((*series)[:i], append([]Element{el}, (*series)[i:]...)...)
	}

	// Re-print every touched rung, each behind the parse gate.
	rungs := []*Rung{from}
	if to != from {
		rungs = append(rungs, to)
	}
	var edits []TextEdit
	for _, r := range rungs {
		body := "    " + printRung(r)
		if _, err := parseRungText(body, r.Line); err != nil {
			return nil, fmt.Errorf("ld edit: refused — the result would not parse: %w", err)
		}
		edits = append(edits, TextEdit{Line: r.Line + 1, Col: 1, EndLine: r.EndLine + 1, EndCol: 1, NewText: body + "\n"})
	}
	return edits, nil
}

func eqInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func findRung(m *Model, name string) (*Rung, error) {
	for i := range m.Rungs {
		if strings.EqualFold(m.Rungs[i].Name, name) {
			return &m.Rungs[i], nil
		}
	}
	return nil, fmt.Errorf("ld edit: no rung named %q", name)
}

// locate resolves Path/Coil to an element pointer inside the rung.
func locate(r *Rung, op EditOp) (*Element, error) {
	if op.Coil != nil {
		if *op.Coil < 0 || *op.Coil >= len(r.Coils) {
			return nil, fmt.Errorf("ld edit: coil %d out of range", *op.Coil)
		}
		return &r.Coils[*op.Coil], nil
	}
	series := &r.Elements
	var el *Element
	path := op.Path
	for len(path) > 0 {
		i := path[0]
		if i < 0 || i >= len(*series) {
			return nil, fmt.Errorf("ld edit: path %v out of range", op.Path)
		}
		el = &(*series)[i]
		path = path[1:]
		if len(path) > 0 {
			if el.Kind != "branch" || path[0] < 0 || path[0] >= len(el.Legs) {
				return nil, fmt.Errorf("ld edit: path %v does not walk a branch leg", op.Path)
			}
			series = &el.Legs[path[0]]
			path = path[1:]
			el = nil
		}
	}
	if el == nil {
		return nil, fmt.Errorf("ld edit: path %v addresses a series, not an element", op.Path)
	}
	return el, nil
}

// locateSeries resolves Path to the series it names ([] = the rung root;
// [i, leg] = that branch leg, recursively).
func locateSeries(r *Rung, path []int) (*[]Element, error) {
	series := &r.Elements
	for len(path) > 0 {
		if len(path) < 2 {
			return nil, fmt.Errorf("ld edit: series path must pair element and leg indexes")
		}
		i, leg := path[0], path[1]
		if i < 0 || i >= len(*series) || (*series)[i].Kind != "branch" {
			return nil, fmt.Errorf("ld edit: series path does not walk a branch")
		}
		if leg < 0 || leg >= len((*series)[i].Legs) {
			return nil, fmt.Errorf("ld edit: leg %d out of range", leg)
		}
		series = &(*series)[i].Legs[leg]
		path = path[2:]
	}
	return series, nil
}

func newElement(op EditOp) (Element, error) {
	switch op.Kind {
	case "contact":
		ref := op.Ref
		if ref == "" {
			ref = "_" // an open contact: retag it next, the diagnostic guides
		}
		if !refValid(ref) {
			return Element{}, fmt.Errorf("ld edit: %q is not a valid reference", ref)
		}
		return Element{Kind: "contact", Ref: ref, Neg: op.Neg}, nil
	case "coil":
		ref := op.Ref
		if ref == "" {
			ref = "_"
		}
		if !refValid(ref) {
			return Element{}, fmt.Errorf("ld edit: %q is not a valid reference", ref)
		}
		return Element{Kind: "coil", Ref: ref, Mode: op.Mode}, nil
	case "edge":
		ref := op.Ref
		if ref == "" {
			ref = "_"
		}
		if !refValid(ref) {
			return Element{}, fmt.Errorf("ld edit: %q is not a valid reference", ref)
		}
		if op.Mode != "P" && op.Mode != "N" {
			return Element{}, fmt.Errorf("ld edit: an edge contact needs mode P or N")
		}
		return Element{Kind: "edge", Ref: ref, Mode: op.Mode}, nil
	case "branch":
		// A fresh branch: the new parallel path plus a placeholder leg.
		return Element{Kind: "branch", Legs: [][]Element{
			{{Kind: "contact", Ref: "_"}},
			{{Kind: "contact", Ref: "_"}},
		}}, nil
	case "fn":
		if op.Fn == "" {
			return Element{}, fmt.Errorf("ld edit: a function contact needs its function name")
		}
		return Element{Kind: "fn", Fn: strings.ToUpper(op.Fn), Args: op.Args}, nil
	case "fb":
		if !identOnly.MatchString(op.Inst) || !identOnly.MatchString(op.FbType) {
			return Element{}, fmt.Errorf("ld edit: a block needs an instance name and a type")
		}
		in, out := powerPins(op.FbType)
		return Element{Kind: "fb", Inst: op.Inst, Type: op.FbType, Args: op.Args, PowerIn: in, PowerOut: out}, nil
	}
	return Element{}, fmt.Errorf("ld edit: unknown element kind %q", op.Kind)
}

// sanitizeElement validates a pasted element tree; the parse gate is the
// final word, but these errors read better than a printer/parser mismatch.
func sanitizeElement(e *Element) error {
	switch e.Kind {
	case "contact", "coil":
		if e.Ref == "" {
			e.Ref = "_"
		}
		if !refValid(e.Ref) {
			return fmt.Errorf("ld edit: %q is not a valid reference", e.Ref)
		}
	case "edge":
		if e.Ref == "" {
			e.Ref = "_"
		}
		if !refValid(e.Ref) {
			return fmt.Errorf("ld edit: %q is not a valid reference", e.Ref)
		}
		if e.Mode != "P" && e.Mode != "N" {
			return fmt.Errorf("ld edit: an edge contact needs mode P or N")
		}
	case "fn":
		if e.Fn == "" {
			return fmt.Errorf("ld edit: a function contact needs its function name")
		}
	case "fb":
		if !identOnly.MatchString(e.Inst) || !identOnly.MatchString(e.Type) {
			return fmt.Errorf("ld edit: a block needs an instance name and a type")
		}
	case "branch":
		if len(e.Legs) < 2 {
			return fmt.Errorf("ld edit: a branch needs at least two legs")
		}
		for li := range e.Legs {
			for i := range e.Legs[li] {
				if err := sanitizeElement(&e.Legs[li][i]); err != nil {
					return err
				}
			}
		}
	default:
		return fmt.Errorf("ld edit: unknown element kind %q", e.Kind)
	}
	return nil
}

func collectInsts(els []Element, taken map[string]bool) {
	for _, e := range els {
		if e.Kind == "fb" && e.Inst != "" {
			taken[strings.ToLower(e.Inst)] = true
		}
		for _, leg := range e.Legs {
			collectInsts(leg, taken)
		}
	}
}

// uniquifyInsts renames pasted fb instances that already exist anywhere in
// the program — a paste duplicates state, it never aliases the original.
func uniquifyInsts(m *Model, el *Element) {
	taken := map[string]bool{}
	for i := range m.Rungs {
		collectInsts(m.Rungs[i].Elements, taken)
		collectInsts(m.Rungs[i].Coils, taken)
	}
	var walk func(e *Element)
	walk = func(e *Element) {
		if e.Kind == "fb" && taken[strings.ToLower(e.Inst)] {
			base := strings.TrimRight(e.Inst, "0123456789")
			n := 2
			for taken[strings.ToLower(fmt.Sprintf("%s%d", base, n))] {
				n++
			}
			e.Inst = fmt.Sprintf("%s%d", base, n)
			taken[strings.ToLower(e.Inst)] = true
		}
		for li := range e.Legs {
			for i := range e.Legs[li] {
				walk(&e.Legs[li][i])
			}
		}
	}
	walk(el)
}

func opInsert(r *Rung, op EditOp) error {
	var el Element
	if op.Element != nil {
		el = *op.Element
		if err := sanitizeElement(&el); err != nil {
			return err
		}
	} else {
		var err error
		el, err = newElement(op)
		if err != nil {
			return err
		}
	}
	if el.Kind == "coil" {
		i := op.Index
		if i < 0 || i > len(r.Coils) {
			i = len(r.Coils)
		}
		r.Coils = append(r.Coils[:i], append([]Element{el}, r.Coils[i:]...)...)
		return nil
	}
	series, err := locateSeries(r, op.Path)
	if err != nil {
		return err
	}
	i := op.Index
	if i < 0 || i > len(*series) {
		i = len(*series)
	}
	*series = append((*series)[:i], append([]Element{el}, (*series)[i:]...)...)
	return nil
}

func opDelete(r *Rung, op EditOp) error {
	if op.Coil != nil {
		if *op.Coil < 0 || *op.Coil >= len(r.Coils) {
			return fmt.Errorf("ld edit: coil %d out of range", *op.Coil)
		}
		if len(r.Coils) == 1 {
			// The grammar needs one coil per rung, but a delete gesture must
			// never dead-end (the never-block philosophy): the last coil
			// becomes the `_` placeholder, and the diagnostic on `_` guides.
			c := &r.Coils[0]
			if c.Ref == "_" && c.Mode == "" {
				return fmt.Errorf("ld edit: a rung needs a coil — delete the rung itself to remove it")
			}
			c.Ref, c.Mode = "_", ""
			return nil
		}
		r.Coils = append(r.Coils[:*op.Coil], r.Coils[*op.Coil+1:]...)
		return nil
	}
	if len(op.Path) == 0 {
		return fmt.Errorf("ld edit: delete needs a path or a coil")
	}
	series, err := locateSeries(r, op.Path[:len(op.Path)-1])
	if err != nil {
		return err
	}
	i := op.Path[len(op.Path)-1]
	if i < 0 || i >= len(*series) {
		return fmt.Errorf("ld edit: path %v out of range", op.Path)
	}
	*series = append((*series)[:i], (*series)[i+1:]...)
	return nil
}

func opAddRung(src string, m *Model, op EditOp) ([]TextEdit, error) {
	name := op.Name
	if name == "" || !identOnly.MatchString(name) {
		return nil, fmt.Errorf("ld edit: a rung needs a valid name")
	}
	if _, err := findRung(m, name); err == nil {
		return nil, fmt.Errorf("ld edit: a rung named %q already exists", name)
	}
	// Insert after the named rung, or before END_LD.
	at := 0
	if op.After != "" {
		r, err := findRung(m, op.After)
		if err != nil {
			return nil, err
		}
		at = r.EndLine + 1
	} else {
		for i, line := range strings.Split(src, "\n") {
			if ldEndRe.MatchString(line) {
				at = i + 1
				break
			}
		}
		if at == 0 {
			return nil, fmt.Errorf("ld edit: no END_LD to insert before")
		}
	}
	text := fmt.Sprintf("\n  RUNG %s\n    ( _ )\n", name)
	return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1, NewText: text}}, nil
}

func opRenameRung(src string, m *Model, op EditOp) ([]TextEdit, error) {
	if !identOnly.MatchString(op.Name) {
		return nil, fmt.Errorf("ld edit: %q is not a valid rung name", op.Name)
	}
	if _, err := findRung(m, op.Name); err == nil {
		return nil, fmt.Errorf("ld edit: a rung named %q already exists", op.Name)
	}
	r, err := findRung(m, op.Rung)
	if err != nil {
		return nil, err
	}
	line := strings.Split(src, "\n")[r.Line-1]
	re := regexp.MustCompile(`(?i)(RUNG\s+)` + regexp.QuoteMeta(r.Name))
	if !re.MatchString(line) {
		return nil, fmt.Errorf("ld edit: rung header for %q not found on line %d", r.Name, r.Line)
	}
	newLine := re.ReplaceAllString(line, "${1}"+op.Name)
	return []TextEdit{{Line: r.Line, Col: 1, EndLine: r.Line, EndCol: len(line) + 1, NewText: newLine}}, nil
}

// ── comments ────────────────────────────────────────────────────────────────

// printComment renders note text back as a run of // lines.
func printComment(text string) string {
	var b strings.Builder
	for _, ln := range strings.Split(text, "\n") {
		b.WriteString("  // " + strings.TrimSpace(ln) + "\n")
	}
	return b.String()
}

func opSetComment(m *Model, op EditOp) ([]TextEdit, error) {
	if op.Comment == nil || *op.Comment < 0 || *op.Comment >= len(m.Comments) {
		return nil, fmt.Errorf("ld edit: no such comment")
	}
	c := m.Comments[*op.Comment]
	text := strings.TrimSpace(op.Text)
	if text == "" { // empty text deletes the run, matching the FBD editor
		return []TextEdit{{Line: c.Line, Col: 1, EndLine: c.EndLine + 1, EndCol: 1}}, nil
	}
	return []TextEdit{{Line: c.Line, Col: 1, EndLine: c.EndLine + 1, EndCol: 1, NewText: printComment(text)}}, nil
}

func opAddComment(src string, m *Model, op EditOp) ([]TextEdit, error) {
	text := strings.TrimSpace(op.Text)
	if text == "" {
		return nil, fmt.Errorf("ld edit: a comment needs text")
	}
	// Insert after the named rung, or before END_LD — same placement as addRung.
	at := 0
	if op.After != "" {
		r, err := findRung(m, op.After)
		if err != nil {
			return nil, err
		}
		at = r.EndLine + 1
	} else {
		for i, line := range strings.Split(src, "\n") {
			if ldEndRe.MatchString(line) {
				at = i + 1
				break
			}
		}
		if at == 0 {
			return nil, fmt.Errorf("ld edit: no END_LD to insert before")
		}
	}
	return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1, NewText: "\n" + printComment(text)}}, nil
}

var headerCommentRe = regexp.MustCompile(`\s*\(\*.*?\*\)\s*$`)

func opSetRungComment(src string, m *Model, op EditOp) ([]TextEdit, error) {
	r, err := findRung(m, op.Rung)
	if err != nil {
		return nil, err
	}
	text := strings.Join(strings.Fields(strings.ReplaceAll(op.Text, "\n", " ")), " ")
	if strings.Contains(text, "*)") {
		return nil, fmt.Errorf("ld edit: a rung comment can't contain *)")
	}
	line := strings.Split(src, "\n")[r.Line-1]
	newLine := strings.TrimRight(headerCommentRe.ReplaceAllString(line, ""), " \t")
	if text != "" {
		newLine += "  (* " + text + " *)"
	}
	return []TextEdit{{Line: r.Line, Col: 1, EndLine: r.Line, EndCol: len(line) + 1, NewText: newLine}}, nil
}

// ── header variables ────────────────────────────────────────────────────────
// The same seam as the FBD editor's vars panel: declarations live in the ST
// header (before the LD block), so the diagram edits them textually.

var ldVarSectionRe = regexp.MustCompile(`(?i)^\s*(VAR_EXTERNAL|VAR)\s*$`)
var varKeywordRe = regexp.MustCompile(`(?i)\b(VAR|VAR_EXTERNAL|END_VAR)\b`)

// opDeclareVar inserts "name : TYPE;" into a header section (VAR_EXTERNAL
// default, VAR for retained locals), creating the section above LD if needed.
func opDeclareVar(src string, m *Model, op EditOp) ([]TextEdit, error) {
	name := strings.TrimSpace(op.Name)
	typ := strings.TrimSpace(op.VarType)
	section := strings.ToUpper(strings.TrimSpace(op.Section))
	if section == "" {
		section = "VAR_EXTERNAL"
	}
	if section != "VAR" && section != "VAR_EXTERNAL" {
		return nil, fmt.Errorf("ld edit: unknown section %q", section)
	}
	if !identOnly.MatchString(name) {
		return nil, fmt.Errorf("ld edit: %q is not a valid identifier", name)
	}
	if !identOnly.MatchString(typ) {
		return nil, fmt.Errorf("ld edit: %q is not a valid type name", typ)
	}
	for _, v := range m.Vars {
		if strings.EqualFold(v.Name, name) {
			return nil, fmt.Errorf("ld edit: %q is already declared", name)
		}
	}

	lines := strings.Split(src, "\n")
	ldLine := -1 // 0-based line of the LD block start = end of the header
	for i, l := range lines {
		if ldStartRe.MatchString(l) {
			ldLine = i
			break
		}
	}
	if ldLine == -1 {
		return nil, fmt.Errorf("ld edit: no LD block")
	}

	insertAt := -1 // 0-based line of the target section's END_VAR
	inSection := ""
	for i := 0; i < ldLine; i++ {
		if mm := ldVarSectionRe.FindStringSubmatch(lines[i]); mm != nil {
			inSection = strings.ToUpper(mm[1])
			continue
		}
		if strings.EqualFold(strings.TrimSpace(lines[i]), "END_VAR") {
			if inSection == section {
				insertAt = i
			}
			inSection = ""
		}
	}

	decl := "    " + name + " : " + typ + ";\n"
	if insertAt >= 0 {
		at := insertAt + 1 // 1-based line of END_VAR
		return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1, NewText: decl}}, nil
	}
	at := ldLine + 1
	return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1,
		NewText: section + "\n" + decl + "END_VAR\n"}}, nil
}

// opDeleteVar removes a declaration by name. References the rungs still
// hold become undeclared-variable diagnostics — deliberately allowed, the
// same breadcrumb philosophy as the FBD editor.
func opDeleteVar(src string, m *Model, op EditOp) ([]TextEdit, error) {
	name := strings.TrimSpace(op.Name)
	for _, v := range m.Vars {
		if !strings.EqualFold(v.Name, name) {
			continue
		}
		// Whole-line removal only: the line must hold just this declaration —
		// a compact header (several decls, or the section markers sharing
		// the line) would lose more than the variable.
		t := strings.TrimSpace(strings.Split(src, "\n")[v.Line-1])
		if strings.Count(t, ";") != 1 || !strings.HasSuffix(t, ";") || varKeywordRe.MatchString(t) {
			return nil, fmt.Errorf("ld edit: %q shares its line with other declarations — edit the header text directly", name)
		}
		return []TextEdit{{Line: v.Line, Col: 1, EndLine: v.Line + 1, EndCol: 1}}, nil
	}
	return nil, fmt.Errorf("ld edit: no declaration named %q", name)
}

// ── printing ────────────────────────────────────────────────────────────────

// printRung renders a rung's body back to canonical rung text.
func printRung(r *Rung) string {
	parts := make([]string, 0, len(r.Elements)+len(r.Coils))
	for i := range r.Elements {
		parts = append(parts, printElement(&r.Elements[i]))
	}
	for i := range r.Coils {
		parts = append(parts, printElement(&r.Coils[i]))
	}
	return strings.Join(parts, " ")
}

func printElement(e *Element) string {
	switch e.Kind {
	case "contact":
		if e.Neg {
			return "/" + e.Ref
		}
		return e.Ref
	case "fn":
		if e.Neg {
			return "/" + e.Fn + "(" + e.Args + ")"
		}
		return e.Fn + "(" + e.Args + ")"
	case "edge":
		if e.Mode == "N" {
			return "-" + e.Ref
		}
		return "+" + e.Ref
	case "fb":
		if strings.TrimSpace(e.Args) == "" {
			return e.Inst + ":" + e.Type
		}
		return e.Inst + ":" + e.Type + "(" + e.Args + ")"
	case "branch":
		legs := make([]string, len(e.Legs))
		for i, leg := range e.Legs {
			ps := make([]string, len(leg))
			for j := range leg {
				ps[j] = printElement(&leg[j])
			}
			legs[i] = strings.Join(ps, " ")
		}
		return "[ " + strings.Join(legs, " | ") + " ]"
	case "coil":
		if e.Mode != "" {
			return "( " + e.Mode + " " + e.Ref + " )"
		}
		return "( " + e.Ref + " )"
	}
	return ""
}

// parseRungText re-parses a printed body as the never-break gate.
func parseRungText(body string, line int) ([]any, error) {
	p := &rungTok{src: strings.TrimSpace(body), line: line}
	elems, err := p.series(false)
	if err != nil {
		return nil, err
	}
	if p.peek() != "" {
		return nil, p.errf("unexpected %q", p.peek())
	}
	return elems, nil
}
