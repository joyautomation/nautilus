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
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SuffixTest marks a file as an acceptance suite. The suffix mirrors Go's,
// and it keeps tests out of the deployment artifact: `naut build`
// excludes them from the embedded project.
const SuffixTest = "_test.yaml"

// Suite is one `*_test.yaml` file.
type Suite struct {
	// Tolerance is the default `near` tolerance for every test in the file.
	Tolerance float64 `yaml:"tolerance"`
	// Suspend names tasks frozen in every test in the file.
	Suspend []string `yaml:"suspend"`
	// Devices runs every test in the file against the project's virtual
	// devices (virtual-devices.yaml) instead of the memory stub: the project's own
	// drivers are answered by the devices in process, through their
	// register maps and node ids, while the devices' simulators and the
	// process model step on the same virtual clock. No socket either way.
	Devices bool    `yaml:"devices"`
	Tests   []*Test `yaml:"tests"`

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

	Line       int `yaml:"-"`
	AlarmsLine int `yaml:"-"` // the single-step shorthand's `alarms:` key
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

	// Force holds tags at values for the rest of the test (or until an
	// `unforce:`), the controller's force table — unlike `given:`, which
	// writes once and lets the driver or the logic take the tag back. A
	// forced input beats the plant model every scan; a forced output beats
	// the logic. Unforce names the addresses to release, or is `all`.
	Force   map[string]any `yaml:"force"`
	Unforce *UnforceList   `yaml:"unforce"`

	// Substitute starts a substitute value (#707) on a tag the manifest's
	// `substitutes:` names, as a person with the right would through the
	// API; Restore ends them (a list of tags, or `all`); AckSubstitutes
	// acknowledges the ones that wait for it. Their time is the test's
	// virtual time, so an expiry is a step that spends it. See
	// substitutes.go.
	Substitute     *SubstituteStep `yaml:"substitute"`
	Restore        *UnforceList    `yaml:"restore"`
	AckSubstitutes *UnforceList    `yaml:"ack-substitutes"`

	// Plant writes the virtual devices' plant tags (<Device>_<Point>, a
	// simulator hook, the process model's) before the step's time is
	// spent: how a sequence test makes the process do something the model
	// would not on its own (a plug, a trip). Needs `devices: true`.
	Plant map[string]any `yaml:"plant"`

	// Repeat runs the step N times; Steps makes the step a group of steps
	// (which Repeat then repeats as a whole). A group has no keys of its own
	// beyond these two. Both are expanded as the test runs, so a failure
	// still names the step as written (see walkSteps).
	Repeat *int    `yaml:"repeat"`
	Steps  []*Step `yaml:"steps"`

	Line int `yaml:"-"`
	// AlarmsLine is the line of the `alarms:` key, where a failed alarm
	// assertion points. (A failed `expect:`/`always:` points at the term
	// that broke; see Term.Line.)
	AlarmsLine int `yaml:"-"`
}

// UnforceList is `unforce:` — a list of forced addresses, or the word
// `all`.
type UnforceList struct {
	All   bool
	Names []string
}

func (u *UnforceList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if node.Value != "all" {
			return fmt.Errorf("line %d: unforce: a list of tag names, or `all`", node.Line)
		}
		u.All = true
		return nil
	}
	return node.Decode(&u.Names)
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
	s.AlarmsLine = keyLines(node, s.Expect, s.Always)
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
	t.AlarmsLine = keyLines(node, t.Expect, t.Always)
	return nil
}

// keyLines stamps the line of each assertion KEY in a step's mapping onto
// what it decoded into — `expect:` and `always:` on their Expect, and
// returns the `alarms:` key's line. A failure points at the assertion that
// broke, not at the top of the step it sits in: the step's first line is
// usually its `given:`, which is the one line that did nothing wrong.
func keyLines(node *yaml.Node, expect, always *Expect) (alarms int) {
	if node.Kind != yaml.MappingNode {
		return 0
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		k := node.Content[i]
		switch k.Value {
		case "expect":
			if expect != nil {
				expect.KeyLine = k.Line
			}
		case "always":
			if always != nil {
				always.KeyLine = k.Line
			}
		case "alarms":
			alarms = k.Line
		}
	}
	return alarms
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
		Line: t.Line, AlarmsLine: t.AlarmsLine,
	}}, nil
}

// MaxExpandedSteps bounds how many steps a test may run once `repeat:` and
// groups are expanded; nested repeats multiply, and a typo must not become
// a test that never ends.
const MaxExpandedSteps = 100000

// MaxRepeat bounds one `repeat:`: a loop that long is a typo or a test
// that wants a different tool.
const MaxRepeat = 10000

// hasBody reports whether the step has any key of its own besides the
// repeat/group pair: something to do or check.
func (s *Step) hasBody() bool {
	return s.Given != nil || s.Scans != nil || s.Advance != nil || s.Until != nil ||
		s.Hold != nil || s.Expect != nil || s.Always != nil || s.Ack != nil ||
		s.Shelve != nil || s.Unshelve != nil || s.Alarms != nil || s.Force != nil ||
		s.Unforce != nil || s.Substitute != nil || s.Restore != nil ||
		s.AckSubstitutes != nil || s.Plant != nil
}

// count is how many steps this one runs once expanded, saturating above
// MaxExpandedSteps so a nest of large repeats cannot overflow.
func (s *Step) count() int {
	n := 1
	if len(s.Steps) > 0 {
		n = countSteps(s.Steps)
	}
	r := 1
	if s.Repeat != nil && *s.Repeat > 0 {
		r = *s.Repeat
	}
	if n > MaxExpandedSteps || r > MaxExpandedSteps || n*r > MaxExpandedSteps {
		return MaxExpandedSteps + 1
	}
	return n * r
}

func countSteps(steps []*Step) int {
	total := 0
	for _, st := range steps {
		total += st.count()
		if total > MaxExpandedSteps {
			return MaxExpandedSteps + 1
		}
	}
	return total
}

// validateSteps checks a list of steps, groups included.
func validateSteps(testName string, steps []*Step) error {
	for _, st := range steps {
		if err := st.validate(testName); err != nil {
			return fmt.Errorf("%d: %w", st.Line, err)
		}
	}
	return nil
}

// stepRun is one executed step of a test: the step as written, and where
// it sits. Index is the 1-based position of its top-level step (what
// "step 3" has always meant; a group does not renumber what follows it),
// Path the dotted position inside groups ("3.2", empty for a top-level
// step), Repeat which iteration of each enclosing repeat it is ("7 of 24",
// outermost first, empty when none).
type stepRun struct {
	Step   *Step
	Index  int
	Path   string
	Repeat string
}

// walkSteps calls fn for every step a test runs, in order, groups entered
// and repeats repeated, and stops at the first fn that returns stop or an
// error. Expansion is lazy: nothing is built for iterations never reached.
func walkSteps(steps []*Step, fn func(stepRun) (stop bool, err error)) (bool, error) {
	return walkLevel(steps, nil, nil, fn)
}

func walkLevel(steps []*Step, path []int, reps []string, fn func(stepRun) (bool, error)) (bool, error) {
	for i, st := range steps {
		here := append(append([]int(nil), path...), i+1)
		n := 1
		if st.Repeat != nil {
			n = *st.Repeat
		}
		for k := 1; k <= n; k++ {
			rs := reps
			if n > 1 { // repeat: 1 is the step as it is today, unlabelled
				rs = append(append([]string(nil), reps...), fmt.Sprintf("%d of %d", k, n))
			}
			if len(st.Steps) > 0 {
				if stop, err := walkLevel(st.Steps, here, rs, fn); stop || err != nil {
					return stop, err
				}
				continue
			}
			parts := make([]string, len(here))
			for j, v := range here {
				parts[j] = strconv.Itoa(v)
			}
			run := stepRun{Step: st, Index: here[0], Repeat: strings.Join(rs, ", ")}
			if len(here) > 1 {
				run.Path = strings.Join(parts, ".")
			}
			if stop, err := fn(run); stop || err != nil {
				return stop, err
			}
		}
	}
	return false, nil
}

// validate rejects the mistakes worth catching before anything runs.
func (s *Step) validate(testName string) error {
	if s.Repeat != nil && (*s.Repeat < 1 || *s.Repeat > MaxRepeat) {
		return fmt.Errorf("test %q: repeat must be between 1 and %d, got %d", testName, MaxRepeat, *s.Repeat)
	}
	if len(s.Steps) > 0 {
		if s.hasBody() {
			return fmt.Errorf("test %q: a step with `steps:` is a group and holds only `steps:` and `repeat:`; put the rest inside it", testName)
		}
		if err := validateSteps(testName, s.Steps); err != nil {
			return err
		}
		return nil
	}
	if s.Steps != nil {
		return fmt.Errorf("test %q: `steps:` is empty", testName)
	}
	if s.Repeat != nil && !s.hasBody() {
		return fmt.Errorf("test %q: `repeat:` needs something to repeat: a step's own keys, or `steps:`", testName)
	}
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
		if n := countSteps(steps); n > MaxExpandedSteps {
			return nil, fmt.Errorf("%s:%d: test %q runs more than %d steps once `repeat:` is expanded (counting stops there); lower a repeat",
				name, t.Line, t.Name, MaxExpandedSteps)
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
