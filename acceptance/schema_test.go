package acceptance

// The JSON Schema shipped with the VS Code extension is what an author
// actually sees: completion, hover text, and a squiggle on a typo. It is
// hand-written, so the one thing that must not happen is it drifting away
// from the loader — a key the schema knows and the loader rejects, or
// worse, a key the loader honors that the schema flags as invalid.
//
// This is an internal test (not acceptance_test) so it can reflect over
// the unexported struct tags that define the format.

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

const schemaPath = "../tools/vscode-iec/schemas/nautilus-test.schema.json"

type jsonSchema struct {
	Properties  map[string]json.RawMessage `json:"properties"`
	Definitions map[string]schemaDef       `json:"definitions"`
}

type schemaDef struct {
	Properties   map[string]json.RawMessage `json:"properties"`
	Dependencies map[string]json.RawMessage `json:"dependencies"`
}

func loadSchema(t *testing.T) jsonSchema {
	t.Helper()
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("the extension's schema is missing: %v", err)
	}
	var s jsonSchema
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	return s
}

// goKeys is the set of yaml keys a struct actually accepts.
func goKeys(v any) []string {
	t := reflect.TypeOf(v)
	var out []string
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if name == "" {
			name = strings.ToLower(t.Field(i).Name)
		}
		if name != "-" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func schemaKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestSchemaMatchesLoader(t *testing.T) {
	s := loadSchema(t)
	for _, tc := range []struct {
		what   string
		got    []string
		schema []string
	}{
		{"the file itself", goKeys(Suite{}), schemaKeys(s.Properties)},
		{"a test", goKeys(Test{}), schemaKeys(s.Definitions["test"].Properties)},
		{"a step", goKeys(Step{}), schemaKeys(s.Definitions["step"].Properties)},
	} {
		if !reflect.DeepEqual(tc.got, tc.schema) {
			t.Errorf("%s: the loader accepts %v, the extension's schema declares %v\n"+
				"update %s to match", tc.what, tc.got, tc.schema, schemaPath)
		}
	}
}

// Every matcher the schema offers must be one the evaluator implements —
// otherwise completion suggests a comparison that silently never matches.
func TestSchemaMatchersAreImplemented(t *testing.T) {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Definitions map[string]struct {
			AnyOf []struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"anyOf"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var offered []string
	for _, alt := range doc.Definitions["matcher"].AnyOf {
		for k := range alt.Properties {
			offered = append(offered, k)
		}
	}
	if len(offered) == 0 {
		t.Fatal("schema offers no matchers — the matcher definition moved?")
	}
	for _, op := range offered {
		if op == "tol" {
			continue // a modifier on `near`, not a comparison of its own
		}
		m := Matcher{Op: op, Num: 1}
		if op == "between" {
			m.Lo, m.Hi = 0, 2
		}
		if _, want := m.match(irOne(), 0.5); want == "?" {
			t.Errorf("schema offers matcher %q but the evaluator has no case for it", op)
		}
	}
}

// irOne is a REAL 1.0 — a value every matcher can be asked about.
func irOne() ir.Value { return ir.RealVal(1) }

// A test with no `steps:` IS one step (Test.steps()), so the schema's
// inline single-step keys must carry exactly the constraints a step's do —
// "one of scans/advance/until", "hold needs until", "until needs expect".
//
// This is not hypothetical: the constraints were first written on `step`
// alone, so an editor happily accepted `{scans: 1, advance: 2s}` at test
// level while `nautilus test` rejected it. The author sees no squiggle and
// then a failure at run time, which is the exact opposite of the point.
func TestSchemaConstrainsInlineStepLikeAStep(t *testing.T) {
	s := loadSchema(t)
	step, test := s.Definitions["step"], s.Definitions["test"]
	if len(step.Dependencies) == 0 {
		t.Fatal("the step definition declares no dependencies — did the schema move?")
	}
	for key, want := range step.Dependencies {
		got, ok := test.Dependencies[key]
		if !ok {
			t.Errorf("schema: `step` constrains %q but `test` does not, so the inline "+
				"single-step form accepts what the loader rejects", key)
			continue
		}
		if !bytes.Equal(normalizeJSON(t, want), normalizeJSON(t, got)) {
			t.Errorf("schema: %q is constrained differently on `test` (%s) than on `step` (%s)",
				key, got, want)
		}
	}
}

// normalizeJSON re-encodes so key order can't make equal values compare
// unequal.
func normalizeJSON(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
