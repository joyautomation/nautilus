// Package writer turns nautilus ladder source into a Logix L5X project: the
// nautilus → Allen-Bradley writer of docs/design/logix-authoring.md §4.
//
// It works from the ladder GRAPH (lang/ld.Graph), never the IR: the graph
// keeps rung comments, integer widths as declared, and source lines, all of
// which the IR loses (logix-target.md §6.4). Each nautilus rung becomes one
// Logix rung of neutral text — the same text Logix exports, so the reader in
// lang/l5x parses what this emits and the round trip is a test, not a hope.
//
// The supported subset is small and enforced here, once, for two consumers:
// `naut check --target logix` runs the same lowering and reports its
// diagnostics; `naut logix write` runs it and emits the L5X. A construct the
// writer cannot express is a diagnostic naming the construct and the
// alternative, never a download failure.
//
// What maps, and how (every rewrite the round-trip comparator undoes):
//
//	Name / /Name                  XIC / XIO
//	[ a | b ]                     [ a ,b ]        nesting preserved
//	( X ) ( S X ) ( R X )         OTE / OTL / OTU; several coils become the
//	                              [OTE(a) ,OTE(b) ] branch Logix writes
//	GT(a, b) … NE(a, b)           GT … NE (neutral-text names); a negated
//	                              compare is its complement (/GT → LE)
//	+Name at the head of a rung   XIC(Name)ONS(rt_<rung>_<Name>)
//	+Name / -Name elsewhere       a helper rung XIC(Name)OSR|OSF(st,out)
//	                              placed before, then XIC(out) in place —
//	                              because ONS/OSR/OSF act on the rung-in
//	                              condition, which is only Name itself at
//	                              the head of a rung
//	t:TON(PT := T#10S)            TON(t,?,?) with PRE in the TIMER tag;
//	                              power continues as XIC(t.DN), since a
//	                              Logix timer passes rung-in through
//	t:TOF(PT := …)                TOF(t,?,?) ends its rung; anything after
//	                              it moves to a new rung headed XIC(t.DN),
//	                              because DN can be true while the input
//	                              is false
//	c:CTU(PV := 5, R := x)        CTU(c,?,?) with PRE in the COUNTER tag,
//	                              ending its rung the same way (DN stays
//	                              true at the preset with no pulse present),
//	                              plus XIC(x)RES(c) on the rung after
//	PT := tvar / PV := ivar       a helper rung MOVE(tvar,t.PRE) before,
//	                              tvar carried as a DINT of milliseconds
//	t.Q t.ET t.IN c.CV c.Q        t.DN t.ACC t.EN c.ACC c.DN
//	VAR_EXTERNAL / VAR            controller tag / program tag
//	PROGRAM                       one program, MainRoutine, one task
package writer

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/st"
)

// Options shape the project around the program. Every field has a default
// that produces a buildable project for the DemoLine fixture.
type Options struct {
	// Controller is the Logix controller (project) name. Default: the
	// nautilus PROGRAM name.
	Controller string
	// Program is the Logix program name. Default: the nautilus PROGRAM name.
	Program string
	// Routine is the ladder routine's name. Default "MainRoutine".
	Routine string
	// Task is the task the program is scheduled in. Default "MainTask".
	Task string
	// PeriodMs makes the task PERIODIC at this rate; 0 is CONTINUOUS.
	PeriodMs int
	// ProcessorType, MajorRev, MinorRev and SoftwareRevision identify the
	// target. Defaults: 1756-L85E, 38, 11, 38.01 — what ECHO1 runs. A
	// hardware.L5X merge replaces these in a later phase
	// (logix-authoring.md §6, decision 1).
	ProcessorType    string
	MajorRev         string
	MinorRev         string
	SoftwareRevision string
	// ExportDate is written verbatim; empty leaves the attribute pinned
	// ("(pinned)", what lang/l5x.Normalize writes), so a regenerated
	// project is byte-identical to the last one unless the logic changed.
	ExportDate string
	// Libs are project library sources in scope for the parse (FB
	// signatures). A user block is rejected either way, but with its
	// signature known the message can say so by name.
	Libs []string
	// Inits and Descs are the manifest's initial values and descriptions
	// by tag name, for the tags the program declares VAR_EXTERNAL: a
	// nautilus tag's seed and desc live in nautilus.yaml, not in the
	// program, and the Logix tag carries both.
	Inits map[string]any
	Descs map[string]string
	// Side is the side code: logic nautilus adds beside the user's
	// program, in its own Logix program scheduled after it, for testing,
	// verification and metrics. Nothing of it touches the user's routine.
	Side Side
}

// Side selects the side code the writer emits (package doc of side.go).
type Side struct {
	// Heartbeat names a controller DINT the side program increments once
	// per task scan — the scan counter a live test waits on. Empty: none.
	Heartbeat string
}

// SideProgram is the Logix program the side code lives in.
const SideProgram = "Nautilus"

// Enabled reports whether any side code is configured.
func (s Side) Enabled() bool { return s.Heartbeat != "" }

func (o Options) withDefaults(program string) Options {
	def := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	def(&o.Controller, program)
	def(&o.Program, program)
	def(&o.Routine, "MainRoutine")
	def(&o.Task, "MainTask")
	def(&o.ProcessorType, "1756-L85E")
	def(&o.MajorRev, "38")
	def(&o.MinorRev, "11")
	def(&o.SoftwareRevision, "38.01")
	def(&o.ExportDate, "(pinned)")
	return o
}

// Diag is one thing the writer cannot express, located in the source.
// Rule is the stable identifier of the rule that fired (rules.go), so an
// editor can group or document them; Message names the construct and the
// alternative.
type Diag struct {
	Rule    string
	Line    int
	Rung    string // the rung's name, "" for a declaration-level rule
	Message string
}

func (d Diag) String() string {
	return fmt.Sprintf("%d: logix target: %s [%s]", d.Line, d.Message, d.Rule)
}

// Check runs the writer's rules over ladder source and reports every
// construct the Logix target cannot express. A parse error is returned as
// err; nil diagnostics means the source writes cleanly.
func Check(src string, libs ...string) ([]Diag, error) {
	m, err := ld.Graph(src, libs...)
	if err != nil {
		return nil, err
	}
	lw := lowerSrc(m, src, Options{Libs: libs}.withDefaults(m.Name))
	return lw.diags, nil
}

// Write lowers ladder source to an L5X controller export. Diagnostics are
// returned alongside a nil document when any rule fires: the writer never
// emits a project it knows will not build.
func Write(src string, opts Options) ([]byte, []Diag, error) {
	m, err := ld.Graph(src, opts.Libs...)
	if err != nil {
		return nil, nil, err
	}
	if m.Name == "" {
		return nil, nil, fmt.Errorf("logix writer: source declares no PROGRAM")
	}
	opts = opts.withDefaults(m.Name)
	lw := lowerSrc(m, src, opts)
	if len(lw.diags) > 0 {
		return nil, lw.diags, nil
	}
	return emit(lw, opts), nil, nil
}

// ── lowering ────────────────────────────────────────────────────────────────

// lowered is the Logix-shaped program: tags by scope and rungs of neutral
// text, in order, plus every diagnostic the lowering raised.
type lowered struct {
	model *ld.Model
	opts  Options
	diags []Diag

	vars     map[string]ld.VarDecl // by lower-cased name
	ctrlTags []tagDef
	progTags []tagDef
	rungs    []rungOut
	// sideRungs is the side program's routine (side.go).
	sideRungs []rungOut

	// presetVars are TIME/integer variables that feed a preset (the only
	// place a TIME is allowed); genNames guards generated tag names.
	presetVars map[string]bool
	genNames   map[string]bool
	// types are the UDTs resolved so far (nil: known bad); rawTypes the
	// TYPE declarations found in the libraries (types.go).
	types    map[string]*udt
	rawTypes map[string]*st.TypeDecl
	// st marks a Structured Text program: block instances are FBD
	// structures, TIME is a DINT, and the routine is stLines.
	st      bool
	stLines []string
	// src is the program source (a ladder file may declare blocks of its
	// own); aois the Add-On Instructions resolved so far (nil: known
	// bad), aoiOrder the ones to emit, in first-use order; inAOI marks a
	// block body being lowered (aoi.go).
	src      string
	aois     map[string]*aoiDef
	aoiOrder []*aoiDef
	inAOI    bool
}

// blockType is the Logix structure behind a block instance: TIMER and
// COUNTER on a ladder rung, FBD_TIMER and FBD_COUNTER in an ST routine.
func (lw *lowered) blockType(typ string) string {
	if lw.st {
		return stBlockTypes[strings.ToUpper(typ)]
	}
	return blockTypes[strings.ToUpper(typ)]
}

// tagDef is one Logix tag to emit.
type tagDef struct {
	Name     string
	DataType string // BOOL SINT INT DINT REAL LREAL TIMER COUNTER
	Dim      int    // 0 scalar, n = Dimensions="n"
	Value    string // scalar initial value in Logix spelling ("0", "85.0"); "" = zero
	Preset   int64  // TIMER.PRE / COUNTER.PRE
	Scope    string // "" controller, else the program
	Desc     string
	Line     int
	// Struct is set for a tag of a user-defined type; Init its manifest
	// initial value (a map of members), when any.
	Struct *udt
	Init   any
	// AOI marks an Add-On Instruction instance.
	AOI bool
}

// rungOut is one emitted rung.
type rungOut struct {
	Comment string
	Text    string // neutral text without the trailing ';'
	Source  string // the nautilus rung it came from
	Line    int
}

func lower(m *ld.Model, opts Options) *lowered {
	return lowerSrc(m, "", opts)
}

func lowerSrc(m *ld.Model, src string, opts Options) *lowered {
	lw := &lowered{model: m, opts: opts, vars: map[string]ld.VarDecl{},
		presetVars: map[string]bool{}, genNames: map[string]bool{}, aois: map[string]*aoiDef{}}
	lw.loadTypes()
	lw.src = src
	for _, v := range m.Vars {
		if v.POU != "" {
			continue // a block's own; lowered with the block (aoi.go)
		}
		lw.vars[strings.ToLower(v.Name)] = v
	}
	// Presets are found before tags are typed, because a TIME variable is
	// legal only when every use of it is a preset.
	for _, r := range m.Rungs {
		if r.POU == "" {
			lw.scanPresets(r.Elements)
		}
	}
	for _, v := range m.Vars {
		if v.POU == "" {
			lw.declare(v)
		}
	}
	notes := lw.notesByLine()
	for _, r := range m.Rungs {
		if r.POU != "" {
			continue
		}
		lw.rung(r, notes[r.Line])
	}
	lw.side()
	return lw
}

func (lw *lowered) diag(rule string, line int, rung, format string, a ...any) {
	lw.diags = append(lw.diags, Diag{Rule: rule, Line: line, Rung: rung, Message: fmt.Sprintf(format, a...)})
}

// notesByLine maps a rung's header line to the `//` note run that sits
// directly above it. Logix has one comment per rung, so the note and the
// header's (* … *) text travel together.
func (lw *lowered) notesByLine() map[int]string {
	out := map[int]string{}
	for _, c := range lw.model.Comments {
		out[c.EndLine+1] = c.Text
	}
	return out
}

// rungComment joins a rung's note run and header comment the way the
// comparator expects to find them in the L5X.
func rungComment(notes, header string) string {
	switch {
	case notes != "" && header != "":
		return notes + "\n" + header
	case notes != "":
		return notes
	default:
		return header
	}
}

// ── declarations ────────────────────────────────────────────────────────────

var (
	arrayRe = regexp.MustCompile(`(?i)^ARRAY\s*\[\s*(-?\d+)\s*\.\.\s*(-?\d+)\s*(,[^\]]*)?\]\s*OF\s+([A-Za-z_][A-Za-z0-9_]*)$`)
	nameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// scalarTypes maps nautilus element types onto Logix atomic types. The
// width comes from the declaration, which is the whole reason the writer
// reads the graph and not the IR.
var scalarTypes = map[string]string{
	"BOOL": "BOOL", "SINT": "SINT", "INT": "INT", "DINT": "DINT",
	"REAL": "REAL", "LREAL": "LREAL",
}

// blockTypes are the standard blocks with a Logix structure behind them.
var blockTypes = map[string]string{"TON": "TIMER", "TOF": "TIMER", "CTU": "COUNTER"}

func (lw *lowered) declare(v ld.VarDecl) {
	scope := ""
	switch strings.ToUpper(v.Section) {
	case "VAR_EXTERNAL":
	case "VAR":
		scope = lw.opts.Program
	default:
		lw.diag(ruleVarSection, v.Line, "", "%s %s: only VAR (program tags) and VAR_EXTERNAL (controller tags) have a Logix home; a program takes no parameters", v.Section, v.Name)
		return
	}
	if !lw.checkName(v.Name, v.Line, "") {
		return
	}
	typ := strings.TrimSpace(v.Type)
	dim := 0
	if m := arrayRe.FindStringSubmatch(typ); m != nil {
		lo, _ := strconv.Atoi(m[1])
		hi, _ := strconv.Atoi(m[2])
		if m[3] != "" {
			lw.diag(ruleArrayShape, v.Line, "", "%s: multi-dimensional arrays are not in the v1 subset; declare one array per dimension", v.Name)
			return
		}
		if lo != 0 {
			lw.diag(ruleArrayShape, v.Line, "", "%s: Logix arrays start at 0, this one starts at %d; declare ARRAY [0..%d]", v.Name, lo, hi-lo)
			return
		}
		if v.Init != "" {
			lw.diag(ruleArrayInit, v.Line, "", "%s: array initializers are not in the v1 subset; the array downloads as zeros, set values from logic or the HMI", v.Name)
			return
		}
		dim = hi + 1
		typ = m[4]
	}
	u := strings.ToUpper(typ)
	if dim > 0 && u == "BOOL" && dim%32 != 0 {
		lw.diag(ruleArrayShape, v.Line, "", "%s: a Logix BOOL array's size must be a multiple of 32; declare ARRAY [0..%d] OF BOOL", v.Name, (dim/32+1)*32-1)
		return
	}
	init := v.Init
	if init == "" && scope == "" {
		if iv, ok := lw.opts.Inits[v.Name]; ok && iv != nil {
			init = fmt.Sprint(iv)
		}
	}
	switch {
	case scalarTypes[u] != "":
		val, ok := literal(u, init)
		if !ok {
			lw.diag(ruleInit, v.Line, "", "%s: initial value %q is not a literal the Logix tag can carry", v.Name, init)
			return
		}
		lw.addTag(tagDef{Name: v.Name, DataType: scalarTypes[u], Dim: dim, Value: val, Scope: scope, Line: v.Line})
	case u == "TIME":
		if !lw.presetVars[strings.ToLower(v.Name)] && !lw.st {
			lw.diag(ruleTime, v.Line, "", "%s: TIME has no Logix type; a TIME variable is carried as DINT milliseconds only where it feeds a timer preset (PT := %s)", v.Name, v.Name)
			return
		}
		if dim > 0 {
			lw.diag(ruleTime, v.Line, "", "%s: an array of TIME is not in the v1 subset", v.Name)
			return
		}
		ms := int64(0)
		if v.Init != "" {
			var ok bool
			if ms, ok = parseTime(v.Init); !ok {
				lw.diag(ruleInit, v.Line, "", "%s: initial value %q is not a TIME literal (T#10S, T#1h30m, T#500ms)", v.Name, v.Init)
				return
			}
		}
		lw.addTag(tagDef{Name: v.Name, DataType: "DINT", Value: strconv.FormatInt(ms, 10), Scope: scope, Line: v.Line})
	case lw.blockType(u) != "":
		if dim > 0 {
			lw.diag(ruleArrayShape, v.Line, "", "%s: an array of %s is not in the v1 subset; declare one instance per element", v.Name, u)
			return
		}
		if v.Init != "" {
			lw.diag(ruleInit, v.Line, "", "%s: a %s instance takes no initializer; the preset comes from the rung (PT := / PV :=)", v.Name, u)
			return
		}
		lw.addTag(tagDef{Name: v.Name, DataType: lw.blockType(u), Scope: scope, Line: v.Line})
	case lw.rawTypes[strings.ToLower(u)] != nil:
		udt, ok := lw.resolveType(typ, v.Line, v.Name)
		if !ok {
			return
		}
		if v.Init != "" {
			lw.diag(ruleInit, v.Line, "", "%s: a structure's initial values come from the manifest (init: {member: value}), not the declaration", v.Name)
			return
		}
		var init any
		if scope == "" {
			init = lw.opts.Inits[v.Name]
		}
		lw.addTag(tagDef{Name: v.Name, DataType: udt.Name, Dim: dim, Scope: scope, Line: v.Line, Struct: udt, Init: init})
	case lw.blockSourceExists(typ):
		if dim > 0 {
			lw.diag(ruleArrayShape, v.Line, "", "%s: an array of %s instances is not in the subset; declare one instance per element", v.Name, typ)
			return
		}
		a, ok := lw.resolveAOI(typ, v.Line)
		if !ok {
			return
		}
		lw.addTag(tagDef{Name: v.Name, DataType: a.Name, Scope: scope, Line: v.Line, AOI: true})
	default:
		alt := "the v1 subset is BOOL, SINT, INT, DINT, REAL, LREAL, TON, TOF, CTU, STRUCT types declared in a library, and FUNCTION_BLOCKs (as Add-On Instructions)"
		switch u {
		case "TP", "CTD", "CTUD":
			alt = "its IEC load/reset semantics differ from the Logix instruction; " + alt
		case "R_TRIG", "F_TRIG":
			alt = "use an edge contact (+Name / -Name) in the rung instead"
		case "STRING":
			alt = "a Logix STRING is a fixed 82-byte structure with no nautilus equivalent in v1"
		case "WORD", "DWORD", "LWORD", "BYTE", "UINT", "UDINT", "USINT", "ULINT", "LINT":
			alt = "Logix has no unsigned or bit-string atomics; declare the signed width (SINT, INT, DINT)"
		}
		lw.diag(ruleType, v.Line, "", "%s: type %s is not in the Logix v1 subset; %s", v.Name, v.Type, alt)
	}
}

func (lw *lowered) addTag(t tagDef) {
	if t.Desc == "" {
		t.Desc = lw.opts.Descs[t.Name]
	}
	if t.Scope == "" {
		lw.ctrlTags = append(lw.ctrlTags, t)
	} else {
		lw.progTags = append(lw.progTags, t)
	}
}

// checkName enforces Logix tag naming: 40 characters at most, a letter or
// underscore first, and no consecutive or trailing underscores.
func (lw *lowered) checkName(name string, line int, rung string) bool {
	switch {
	case !nameRe.MatchString(name):
		lw.diag(ruleName, line, rung, "%s: a Logix tag name is letters, digits and underscores, starting with a letter or underscore", name)
	case len(name) > 40:
		lw.diag(ruleName, line, rung, "%s: a Logix tag name is at most 40 characters, this one is %d", name, len(name))
	case strings.Contains(name, "__") || strings.HasSuffix(name, "_"):
		lw.diag(ruleName, line, rung, "%s: a Logix tag name cannot have consecutive or trailing underscores", name)
	default:
		return true
	}
	return false
}

// genTag declares a generated program tag (edge storage, edge output). The
// name is a pure function of the rung and reference, so a regeneration of
// unchanged source yields the same tags and the project does not drift.
func (lw *lowered) genTag(name string, line int, rung string) {
	if lw.genNames[name] {
		return
	}
	lw.genNames[name] = true
	if len(name) > 40 {
		lw.diag(ruleName, line, rung, "generated tag %s is %d characters (Logix allows 40); shorten the rung name or the reference", name, len(name))
		return
	}
	if _, clash := lw.vars[strings.ToLower(name)]; clash {
		lw.diag(ruleName, line, rung, "generated tag %s clashes with a declared variable; rename one", name)
		return
	}
	lw.addTag(tagDef{Name: name, DataType: "BOOL", Scope: lw.opts.Program, Line: line})
}

// ── literals ────────────────────────────────────────────────────────────────

var (
	timeUnitRe = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)(ms|s|m|h|d)`)
	numRe      = regexp.MustCompile(`^-?\d+(\.\d+)?([eE][-+]?\d+)?$`)
)

// parseTime reads an IEC TIME literal (T#10S, TIME#1h30m, T#500ms, T#1.5s)
// as whole milliseconds.
func parseTime(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	u := strings.ToUpper(s)
	switch {
	case strings.HasPrefix(u, "TIME#"):
		s = s[5:]
	case strings.HasPrefix(u, "T#"):
		s = s[2:]
	default:
		return 0, false
	}
	s = strings.ReplaceAll(s, "_", "")
	if s == "" {
		return 0, false
	}
	var ms float64
	for s != "" {
		m := timeUnitRe.FindStringSubmatch(s)
		if m == nil {
			return 0, false
		}
		n, _ := strconv.ParseFloat(m[1], 64)
		switch strings.ToLower(m[2]) {
		case "ms":
			ms += n
		case "s":
			ms += n * 1000
		case "m":
			ms += n * 60000
		case "h":
			ms += n * 3600000
		case "d":
			ms += n * 86400000
		}
		s = s[len(m[0]):]
	}
	return int64(ms + 0.5), true
}

// literal renders a nautilus initializer in the spelling the Logix tag's
// Decorated value takes. Empty means the type's zero.
func literal(typ, init string) (string, bool) {
	init = strings.TrimSpace(init)
	if init == "" {
		return "", true
	}
	if i := strings.Index(init, "#"); i > 0 && !strings.ContainsAny(init[:i], "0123456789") {
		// a typed literal: INT#5, REAL#1.5, BOOL#TRUE
		init = init[i+1:]
	}
	switch typ {
	case "BOOL":
		switch strings.ToUpper(init) {
		case "TRUE", "1":
			return "1", true
		case "FALSE", "0":
			return "0", true
		}
		return "", false
	case "REAL", "LREAL":
		f, err := strconv.ParseFloat(init, 64)
		if err != nil {
			return "", false
		}
		return formatReal(f), true
	default:
		n, ok := parseInt(init)
		if !ok {
			return "", false
		}
		return strconv.FormatInt(n, 10), true
	}
}

// parseInt reads a decimal or base#digits integer literal (16#FF, 2#1010),
// underscores allowed.
func parseInt(s string) (int64, bool) {
	s = strings.ReplaceAll(s, "_", "")
	base := 10
	if i := strings.Index(s, "#"); i > 0 {
		b, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, false
		}
		base, s = b, s[i+1:]
	}
	n, err := strconv.ParseInt(s, base, 64)
	return n, err == nil
}

// formatReal spells a float the way a Decorated REAL Value does: shortest
// round-trip digits, always with a decimal point.
func formatReal(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// sortedKeys is a test aid for deterministic diagnostics.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Language names the source language of a program file, by extension.
func Language(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ld":
		return "ld"
	case ".st":
		return "st"
	}
	return ""
}

// WriteProgram lowers a program in whichever language its path says.
func WriteProgram(path, src string, opts Options) ([]byte, []Diag, error) {
	switch Language(path) {
	case "ld":
		return Write(src, opts)
	case "st":
		return WriteST(src, opts)
	}
	return nil, nil, fmt.Errorf("%s: only ladder (.ld) and structured text (.st) programs are in the Logix subset", path)
}

// CheckProgram runs the rules for a program in whichever language its
// path says.
func CheckProgram(path, src string, libs ...string) ([]Diag, error) {
	switch Language(path) {
	case "ld":
		return Check(src, libs...)
	case "st":
		return CheckST(src, libs...)
	}
	return nil, fmt.Errorf("%s: only ladder (.ld) and structured text (.st) programs are in the Logix subset", path)
}

func (lw *lowered) blockSourceExists(name string) bool {
	src, _ := lw.blockSource(name)
	return src != ""
}
