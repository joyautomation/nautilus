package acceptance

// The `*_test.yaml` surface: what a test says, and how it is read off disk.
//
// The format is deliberately small and meant to stay that way — given,
// a time key, expect, always, suspend. Everything a real suite eventually
// wants that ISN'T orchestration (computed expectations, relational
// checks, reusable predicates) is an ST expression instead, compiled by
// the same compiler as the logic under test. That is the pressure valve
// which lets this schema stay frozen rather than growing into a poor
// programming language one key at a time.

import (
	"fmt"
	"io/fs"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SuffixTest marks a file as an acceptance suite. The suffix mirrors Go's,
// and it keeps tests out of the deployment artifact: `nautilus build`
// excludes them from the embedded project.
const SuffixTest = "_test.yaml"

// Suite is one `*_test.yaml` file.
type Suite struct {
	// Tolerance is the default `near` tolerance for every test in the file.
	Tolerance float64 `yaml:"tolerance"`
	// Suspend names tasks frozen in every test in the file.
	Suspend []string `yaml:"suspend"`
	Tests   []*Test  `yaml:"tests"`

	Path string `yaml:"-"` // for diagnostics
}

// Test is one named scenario: initial conditions, then steps.
type Test struct {
	Name string `yaml:"name"`
	// Suspend replaces the file's list for this test. Naming what you
	// freeze states the intent ("this test drives the temperature, so the
	// plant must not fight it") and survives a fifth task being added.
	Suspend   []string       `yaml:"suspend"`
	Tolerance *float64       `yaml:"tolerance"`
	Given     map[string]any `yaml:"given"`
	Steps     []*Step        `yaml:"steps"`

	// Single-step shorthand: a test with no `steps:` is itself one step.
	Scans   *int      `yaml:"scans"`
	Advance *Duration `yaml:"advance"`
	Until   *Duration `yaml:"until"`
	Hold    *Duration `yaml:"hold"`
	Expect  *Expect   `yaml:"expect"`
	Always  *Expect   `yaml:"always"`

	// Alarm state is host state, not tag state — see alarms.go for why it
	// gets keys of its own rather than living inside `expect:`.
	Alarms   *AlarmExpect  `yaml:"alarms"`
	Ack      *AckStep      `yaml:"ack"`
	Shelve   *ShelveStep   `yaml:"shelve"`
	Unshelve *UnshelveStep `yaml:"unshelve"`

	Line int `yaml:"-"`
}

// Step is one segment of a test: optional writes, a span of virtual time,
// and what must be true.
type Step struct {
	Given map[string]any `yaml:"given"`

	// Exactly one time key. Omit all three to assert without scanning.
	Scans   *int      `yaml:"scans"`   // N scans of the main task
	Advance *Duration `yaml:"advance"` // exactly this much virtual time
	Until   *Duration `yaml:"until"`   // up to this much, stop when Expect holds
	Hold    *Duration `yaml:"hold"`    // with Until: how long it must stay true

	Expect *Expect `yaml:"expect"` // checked at the end (or, with Until, each tick)
	Always *Expect `yaml:"always"` // checked after every scan in the step

	// The operator verbs, applied with `given:` before the step's time is
	// spent, and the alarm assertion, checked with `expect:` after it.
	Ack      *AckStep      `yaml:"ack"`
	Shelve   *ShelveStep   `yaml:"shelve"`
	Unshelve *UnshelveStep `yaml:"unshelve"`
	Alarms   *AlarmExpect  `yaml:"alarms"`

	Line int `yaml:"-"`
}

// UnmarshalYAML records the source line so a failure can point at it.
func (s *Step) UnmarshalYAML(node *yaml.Node) error {
	type raw Step // shed the method to avoid recursing
	var r raw
	if err := strictDecode(node, &r); err != nil {
		return err
	}
	*s = Step(r)
	s.Line = node.Line
	return nil
}

// UnmarshalYAML records the source line, as for Step.
func (t *Test) UnmarshalYAML(node *yaml.Node) error {
	type raw Test
	var r raw
	if err := strictDecode(node, &r); err != nil {
		return err
	}
	*t = Test(r)
	t.Line = node.Line
	return nil
}

// strictDecode decodes node into v and rejects any key the struct does not
// declare.
//
// yaml.v3's KnownFields is a property of the DECODER, and node.Decode
// builds a fresh one — so a type with a custom UnmarshalYAML silently
// loses strictness. Without this, `scanz: 1` inside a test would be
// ignored rather than reported, which is the worst possible outcome: a
// test that looks like it asserts something and does not.
func strictDecode(node *yaml.Node, v any) error {
	if err := node.Decode(v); err != nil {
		return err
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	known := yamlFields(reflect.TypeOf(v).Elem())
	for i := 0; i+1 < len(node.Content); i += 2 {
		if k := node.Content[i]; !known[k.Value] {
			return fmt.Errorf("line %d: unknown key %q", k.Line, k.Value)
		}
	}
	return nil
}

func yamlFields(t reflect.Type) map[string]bool {
	out := make(map[string]bool, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if name == "" {
			name = strings.ToLower(t.Field(i).Name)
		}
		if name != "-" {
			out[name] = true
		}
	}
	return out
}

// Duration parses "100ms" / "9.5s" yaml scalars, like the manifest's.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	v, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("bad duration %q (want e.g. 500ms, 9.5s, 2m)", node.Value)
	}
	*d = Duration(v)
	return nil
}

func (d *Duration) get() time.Duration {
	if d == nil {
		return 0
	}
	return time.Duration(*d)
}

// steps normalizes the shorthand: a test with no `steps:` is one step
// built from its own time/expect keys.
func (t *Test) steps() ([]*Step, error) {
	inline := t.Scans != nil || t.Advance != nil || t.Until != nil ||
		t.Hold != nil || t.Expect != nil || t.Always != nil ||
		t.Alarms != nil || t.Ack != nil || t.Shelve != nil || t.Unshelve != nil
	if len(t.Steps) > 0 {
		if inline {
			return nil, fmt.Errorf("test %q: use either `steps:` or the single-step keys, not both", t.Name)
		}
		return t.Steps, nil
	}
	if !inline {
		return nil, fmt.Errorf("test %q: needs `steps:` or a single step's keys (scans/advance/until, expect)", t.Name)
	}
	return []*Step{{
		Scans: t.Scans, Advance: t.Advance, Until: t.Until, Hold: t.Hold,
		Expect: t.Expect, Always: t.Always,
		Ack: t.Ack, Shelve: t.Shelve, Unshelve: t.Unshelve, Alarms: t.Alarms,
		Line: t.Line,
	}}, nil
}

// validate rejects the mistakes worth catching before anything runs.
func (s *Step) validate(testName string) error {
	n := 0
	for _, set := range []bool{s.Scans != nil, s.Advance != nil, s.Until != nil} {
		if set {
			n++
		}
	}
	if n > 1 {
		return fmt.Errorf("test %q: a step takes one of scans/advance/until, not several", testName)
	}
	if s.Hold != nil && s.Until == nil {
		return fmt.Errorf("test %q: `hold` only means something with `until` (how long the expectation must stay true)", testName)
	}
	if s.Until != nil && s.Expect == nil {
		return fmt.Errorf("test %q: `until` needs an `expect` — it runs until that holds", testName)
	}
	if s.Scans != nil && *s.Scans < 0 {
		return fmt.Errorf("test %q: scans cannot be negative", testName)
	}
	if s.Until != nil && s.Alarms != nil {
		// `until` polls `expect` on every tick; polling an alarm
		// assertion too would need a second predicate and a second
		// failure story. A step that waits, then asserts alarms, is two
		// steps — and reads better as two.
		return fmt.Errorf("test %q: `alarms:` is checked at the end of a step, so it does not "+
			"go with `until:` — use `advance:`, or split it into two steps", testName)
	}
	return nil
}

// LoadSuite reads and validates one suite file.
func LoadSuite(fsys fs.FS, name string) (*Suite, error) {
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	var s Suite
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // a typo is an error, not silence
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	s.Path = name
	if len(s.Tests) == 0 {
		return nil, fmt.Errorf("%s: no tests declared", name)
	}
	seen := map[string]bool{}
	for _, t := range s.Tests {
		if t.Name == "" {
			return nil, fmt.Errorf("%s:%d: every test needs a name", name, t.Line)
		}
		if seen[t.Name] {
			return nil, fmt.Errorf("%s:%d: duplicate test name %q", name, t.Line, t.Name)
		}
		seen[t.Name] = true
		steps, err := t.steps()
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", name, t.Line, err)
		}
		for _, st := range steps {
			if err := st.validate(t.Name); err != nil {
				return nil, fmt.Errorf("%s:%d: %w", name, st.Line, err)
			}
		}
	}
	return &s, nil
}

// DiscoverSuites walks fsys for `*_test.yaml`, skipping dot-directories.
// Returned in path order so a run is reproducible.
func DiscoverSuites(fsys fs.FS) ([]string, error) {
	var out []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && strings.HasPrefix(path.Base(p), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), SuffixTest) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}
