package acceptance

// Expectations come in two forms that share one key.
//
// A tag matcher — `PumpRun: true`, `TempC: {near: 72, tol: 0.5}` — covers
// the overwhelming majority and reads like a table.
//
// An ST boolean expression — `ABS(TempC - TempSP) < 0.5` — covers
// everything a fixed matcher vocabulary cannot: expectations computed from
// other tags, relationships between them, and reusable predicates written
// as ST FUNCTIONs in the project's own library files. It is compiled by
// the same compiler as the logic under test, so it type-checks and the
// editor already knows the tag names.
//
// Which one you wrote is unambiguous from the YAML node: a scalar string
// is an expression, a mapping is tag matchers.

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/runtime"
)

// Expect is a conjunction of terms — all must hold.
type Expect struct {
	Terms []Term
	Line  int
}

// Term is either one tag's matcher or one ST expression.
type Term struct {
	Tag     string // "" when this is an expression
	Matcher Matcher
	Expr    string // "" when this is a tag matcher
	Line    int
}

// UnmarshalYAML accepts a mapping of tag→matcher, a single expression
// string, or a sequence mixing both.
func (e *Expect) UnmarshalYAML(node *yaml.Node) error {
	e.Line = node.Line
	switch node.Kind {
	case yaml.MappingNode:
		terms, err := mappingTerms(node)
		if err != nil {
			return err
		}
		e.Terms = terms
	case yaml.ScalarNode:
		e.Terms = []Term{{Expr: node.Value, Line: node.Line}}
	case yaml.SequenceNode:
		for _, item := range node.Content {
			switch item.Kind {
			case yaml.ScalarNode:
				e.Terms = append(e.Terms, Term{Expr: item.Value, Line: item.Line})
			case yaml.MappingNode:
				terms, err := mappingTerms(item)
				if err != nil {
					return err
				}
				e.Terms = append(e.Terms, terms...)
			default:
				return fmt.Errorf("line %d: an expectation is a tag mapping or an ST expression", item.Line)
			}
		}
	default:
		return fmt.Errorf("line %d: an expectation is a tag mapping or an ST expression", node.Line)
	}
	if len(e.Terms) == 0 {
		return fmt.Errorf("line %d: empty expectation", node.Line)
	}
	return nil
}

func mappingTerms(node *yaml.Node) ([]Term, error) {
	var out []Term
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		var m Matcher
		if err := v.Decode(&m); err != nil {
			return nil, fmt.Errorf("line %d: tag %s: %w", v.Line, k.Value, err)
		}
		out = append(out, Term{Tag: k.Value, Matcher: m, Line: k.Line})
	}
	return out, nil
}

// Matcher is how one tag's value is compared.
type Matcher struct {
	Op string // eq | near | gt | ge | lt | le | between

	Bool   bool
	Str    string
	IsBool bool
	IsStr  bool

	Num    float64
	Tol    *float64
	Lo, Hi float64
}

// UnmarshalYAML reads a bare scalar as exact equality, or an object as one
// of the comparison forms.
func (m *Matcher) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var v any
		if err := node.Decode(&v); err != nil {
			return err
		}
		return m.setExact(v)
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("want a value or a matcher like {near: 72.0, tol: 0.5}")
	}
	var raw struct {
		Eq      any       `yaml:"eq"`
		Near    *float64  `yaml:"near"`
		Tol     *float64  `yaml:"tol"`
		Gt      *float64  `yaml:"gt"`
		Ge      *float64  `yaml:"ge"`
		Lt      *float64  `yaml:"lt"`
		Le      *float64  `yaml:"le"`
		Between []float64 `yaml:"between"`
	}
	dec := node.Decode(&raw)
	if dec != nil {
		return dec
	}
	set := 0
	switch {
	case raw.Eq != nil:
		set++
		if err := m.setExact(raw.Eq); err != nil {
			return err
		}
	}
	for op, p := range map[string]*float64{"near": raw.Near, "gt": raw.Gt, "ge": raw.Ge, "lt": raw.Lt, "le": raw.Le} {
		if p != nil {
			set++
			m.Op, m.Num = op, *p
		}
	}
	if raw.Between != nil {
		if len(raw.Between) != 2 {
			return fmt.Errorf("between wants exactly two bounds, got %d", len(raw.Between))
		}
		set++
		m.Op, m.Lo, m.Hi = "between", raw.Between[0], raw.Between[1]
	}
	if set == 0 {
		return fmt.Errorf("empty matcher (want eq, near+tol, gt, ge, lt, le, or between)")
	}
	if set > 1 {
		return fmt.Errorf("a matcher takes one comparison, not several")
	}
	if raw.Tol != nil {
		if m.Op != "near" {
			return fmt.Errorf("`tol` belongs with `near`")
		}
		m.Tol = raw.Tol
	}
	return nil
}

func (m *Matcher) setExact(v any) error {
	m.Op = "eq"
	switch x := v.(type) {
	case bool:
		m.Bool, m.IsBool = x, true
	case string:
		m.Str, m.IsStr = x, true
	case int:
		m.Num = float64(x)
	case float64:
		m.Num = x
	default:
		return fmt.Errorf("cannot compare against %v (%T)", v, v)
	}
	return nil
}

// match reports whether got satisfies the matcher, and renders what was
// wanted for the failure line.
func (m Matcher) match(got ir.Value, defaultTol float64) (bool, string) {
	switch m.Op {
	case "eq":
		switch {
		case m.IsBool:
			return got.Kind == ir.TypeBool && got.B == m.Bool, fmt.Sprintf("%v", m.Bool)
		case m.IsStr:
			return got.Kind == ir.TypeString && got.S == m.Str, fmt.Sprintf("%q", m.Str)
		default:
			return numOf(got) == m.Num, trim(m.Num)
		}
	case "near":
		tol := defaultTol
		if m.Tol != nil {
			tol = *m.Tol
		}
		if tol <= 0 {
			return false, fmt.Sprintf("%s ± (no tolerance — set `tol:` here or `tolerance:` for the file)", trim(m.Num))
		}
		return math.Abs(numOf(got)-m.Num) <= tol, fmt.Sprintf("%s ± %s", trim(m.Num), trim(tol))
	case "gt":
		return numOf(got) > m.Num, "> " + trim(m.Num)
	case "ge":
		return numOf(got) >= m.Num, ">= " + trim(m.Num)
	case "lt":
		return numOf(got) < m.Num, "< " + trim(m.Num)
	case "le":
		return numOf(got) <= m.Num, "<= " + trim(m.Num)
	case "between":
		n := numOf(got)
		return n >= m.Lo && n <= m.Hi, fmt.Sprintf("between %s and %s", trim(m.Lo), trim(m.Hi))
	}
	return false, "?"
}

func numOf(v ir.Value) float64 {
	switch v.Kind {
	case ir.TypeReal:
		return v.F
	case ir.TypeInt, ir.TypeTime:
		return float64(v.I)
	}
	return math.NaN()
}

// trim renders a float without trailing zeros, so a want reads like the
// number the author wrote.
func trim(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", f), "0"), ".")
}

// show renders an actual value for a failure line.
func show(v ir.Value) string {
	switch v.Kind {
	case ir.TypeBool:
		return fmt.Sprintf("%v", v.B)
	case ir.TypeString:
		return fmt.Sprintf("%q", v.S)
	case ir.TypeReal:
		return trim(v.F)
	case ir.TypeInt, ir.TypeTime:
		return fmt.Sprintf("%d", v.I)
	}
	return "?"
}

// ─── ST expression predicates ──────────────────────────────────────────

// resultTag is where a compiled predicate leaves its answer. The leading
// underscores keep it clear of any name a project would choose.
const resultTag = "__expect"

// predicate is one compiled ST boolean expression, cached across the many
// evaluations an `until` loop performs.
type predicate struct {
	expr string
	prog *runtime.Program
}

// Externals is the VAR_EXTERNAL block an expectation expression compiles
// against: tag name → the ST type it is declared as.
type Externals map[string]string

// ExternalsOf reports the tags an expression may name, as the union of what
// the tag store HOLDS and what the programs DECLARE.
//
// Neither source alone is right. The store misses a tag nothing has written
// yet — an output with no seed exists in no snapshot, and is exactly the
// sort of tag a first-scan expectation names. The declarations miss a tag
// only the manifest seeds. A struct-typed (UDT/Template) tag declares as
// its own TYPE name rather than being left out: the project's library .st
// already declares that TYPE, exprSource composes those libraries ahead of
// the generated POU, so `WEL15_FIT_001.VALUE < X + 5.0` compiles exactly
// the way `WEL15_FIT_001.VALUE: {gt: 800}` (the matcher form) already
// worked. Arrays and FB instances still have no place in a VAR_EXTERNAL
// block generated this way, so they stay left out.
func ExternalsOf(rt *runtime.Runtime) Externals {
	ext := Externals{}
	for name, t := range rt.Globals() {
		if t == nil {
			continue
		}
		if ty, ok := stTypeNameOfType(t); ok {
			ext[name] = ty
		}
	}
	// The store wins on a disagreement: it is what eval actually reads.
	for name, v := range rt.Tags().Snapshot() {
		if ty, ok := stTypeNameOfValue(v); ok {
			ext[name] = ty
		}
	}
	delete(ext, resultTag)
	return ext
}

// exprPrefix opens the generated assignment; its length is what maps a
// compiler column back onto the author's expression.
const exprPrefix = resultTag + " := ("

// exprSource wraps an expression in a throwaway POU that declares every tag
// as VAR_EXTERNAL, so the expression sees exactly what the resource has,
// with real types. The project's own library files come along, which is
// what makes an ST FUNCTION in blocks.st usable as a reusable assertion.
//
// preludeLines counts the library source placed ahead of the POU, which is
// what tells a mistake in the expression apart from a broken library file.
func exprSource(ext Externals, libs []string, expr string) (src string, preludeLines int) {
	names := make([]string, 0, len(ext))
	for n := range ext {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("PROGRAM __ExpectPOU\nVAR_EXTERNAL\n")
	for _, n := range names {
		fmt.Fprintf(&b, "    %s : %s;\n", n, ext[n])
	}
	fmt.Fprintf(&b, "    %s : BOOL;\nEND_VAR\n", resultTag)
	fmt.Fprintf(&b, "%s%s);\nEND_PROGRAM\n", exprPrefix, expr)

	src = b.String()
	if len(libs) > 0 {
		prelude := stproject.Join(libs, "")
		src = prelude + src
		preludeLines = strings.Count(prelude, "\n")
	}
	return src, preludeLines
}

// compile wraps the expression and compiles it against the live runtime.
func compilePredicate(rt *runtime.Runtime, libs []string, expr string) (*predicate, error) {
	src, _ := exprSource(ExternalsOf(rt), libs, expr)
	prog, err := runtime.Compile(src)
	if err != nil {
		return nil, fmt.Errorf("expression %q: %w", expr, err)
	}
	return &predicate{expr: expr, prog: prog}, nil
}

// CheckExpr compiles one expectation expression without running it, for
// tooling that wants the compiler's verdict on an expression in isolation —
// an editor squiggling a test file as it is typed. It shares exprSource
// with the runner, so what checks here is what runs there.
//
// The message is rewritten for where it will be shown: the line number a
// compiler error carries belongs to the generated POU, which the author
// never sees, so it is stripped rather than shown as a lie.
func CheckExpr(ext Externals, libs []string, expr string) error {
	src, preludeLines := exprSource(ext, libs, expr)
	_, err := runtime.Compile(src)
	if err == nil {
		return nil
	}
	msg := err.Error()
	pos, known := st.ParseErrorPos(err)
	if le, ok := st.AsLowerError(err); ok && le.Pos.Line > 0 {
		pos, known, msg = le.Pos, true, le.Err.Error()
	}
	msg = linePrefix.ReplaceAllString(msg, "")
	// A failure positioned in the prelude is the project's library files
	// being broken, not this expression. Say so, or the author hunts for a
	// mistake that isn't in front of them.
	if known && pos.Line <= preludeLines && preludeLines > 0 {
		msg = "in project library files: " + msg
	}
	return errors.New(msg)
}

// linePrefix is the "line N: " a compiler error carries; the line belongs
// to generated scaffolding and would only mislead.
var linePrefix = regexp.MustCompile(`^line \d+: `)

// eval runs the compiled expression against the live tag store.
func (p *predicate) eval(rt *runtime.Runtime) (bool, error) {
	rt.Tags().SetBool(resultTag, false)
	if err := p.prog.Run(rt.Tags()); err != nil {
		return false, fmt.Errorf("expression %q: %w", p.expr, err)
	}
	return rt.Tags().Bool(resultTag), nil
}

// stTypeName maps a stored value's kind onto a declarable ST scalar type.
// Arrays and FB instances have no place in a VAR_EXTERNAL block generated
// this way, so they are left out and an expression referencing one fails to
// compile with the name unknown. Structs are handled separately by
// stTypeNameOfType/stTypeNameOfValue, which need the UDT's name, not just
// its kind.
func stTypeName(k ir.TypeKind) (string, bool) {
	switch k {
	case ir.TypeBool:
		return "BOOL", true
	case ir.TypeInt:
		return "DINT", true
	case ir.TypeReal:
		return "REAL", true
	case ir.TypeTime:
		return "TIME", true
	case ir.TypeString:
		return "STRING", true
	}
	return "", false
}

// stTypeNameOfType maps a program-declared global's type onto a declarable
// ST type name: a scalar's own keyword, or — for a struct-typed (UDT) tag —
// the UDT's TYPE name, so the generated VAR_EXTERNAL block reads
// `P101 : Motor;` exactly as the real program does.
func stTypeNameOfType(t *ir.Type) (string, bool) {
	if t.Kind == ir.TypeStruct {
		if t.Struct == nil || t.Struct.Name == "" {
			return "", false
		}
		return t.Struct.Name, true
	}
	return stTypeName(t.Kind)
}

// stTypeNameOfValue is stTypeNameOfType's counterpart for a stored value —
// used for a tag the store holds but no program declares (manifest-seeded
// UDT tags), where the UDT name comes from the value's own StructDef.
func stTypeNameOfValue(v ir.Value) (string, bool) {
	if v.Kind == ir.TypeStruct {
		if v.Struct == nil || v.Struct.Name == "" {
			return "", false
		}
		return v.Struct.Name, true
	}
	return stTypeName(v.Kind)
}
