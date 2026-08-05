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
	"fmt"
	"math"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/ir"
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
func trim(f float64) string { return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", f), "0"), ".") }

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

// compile wraps the expression in a throwaway POU whose VAR_EXTERNAL block
// is generated from the live tag store, so the expression sees exactly the
// tags the resource has, with their real types. The project's own library
// files come along, which is what makes an ST FUNCTION in blocks.st usable
// as a reusable assertion.
func compilePredicate(rt *runtime.Runtime, libs []string, expr string) (*predicate, error) {
	snap := rt.Tags().Snapshot()
	names := make([]string, 0, len(snap))
	for n := range snap {
		if n == resultTag {
			continue
		}
		if _, ok := stTypeName(snap[n].Kind); ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("PROGRAM __ExpectPOU\nVAR_EXTERNAL\n")
	for _, n := range names {
		ty, _ := stTypeName(snap[n].Kind)
		fmt.Fprintf(&b, "    %s : %s;\n", n, ty)
	}
	fmt.Fprintf(&b, "    %s : BOOL;\nEND_VAR\n", resultTag)
	fmt.Fprintf(&b, "%s := (%s);\nEND_PROGRAM\n", resultTag, expr)

	src := b.String()
	if len(libs) > 0 {
		src = stproject.Join(libs, src)
	}
	prog, err := runtime.Compile(src)
	if err != nil {
		return nil, fmt.Errorf("expression %q: %w", expr, err)
	}
	return &predicate{expr: expr, prog: prog}, nil
}

// eval runs the compiled expression against the live tag store.
func (p *predicate) eval(rt *runtime.Runtime) (bool, error) {
	rt.Tags().SetBool(resultTag, false)
	if err := p.prog.Run(rt.Tags()); err != nil {
		return false, fmt.Errorf("expression %q: %w", p.expr, err)
	}
	return rt.Tags().Bool(resultTag), nil
}

// stTypeName maps a stored value's kind onto a declarable ST scalar type.
// Compound values (UDTs, arrays, FB instances) have no place in a
// VAR_EXTERNAL block generated this way, so they are left out and an
// expression referencing one fails to compile with the name unknown.
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
