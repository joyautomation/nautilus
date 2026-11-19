package acceptance

import (
	"fmt"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/runtime"
)

// Substitute values in a sequence test (#707):
//
//	- substitute: {tag: Level, value: 95, for: 30m}
//	  advance: 31m        # past the grant: the real reading is back
//	- restore: [Level]   # or `all`
//	- ack-substitutes: all
//
// The step goes through the same rules a person starting one through the
// API meets: the tag must be named by a `substitutes:` rule, the value is
// the rule's unless the rule allows an operator's (`operator-value:`), and
// `for:` defaults to the rule's `max` and may not exceed it. Time is the
// test's virtual time, so an expiry is a step that spends it.

// SubstituteRule is one `substitutes:` rule as the harness needs it: what
// internal/project resolves from the manifest (project.SubstituteSpec).
type SubstituteRule struct {
	// Addr is the substitutable tag, or a member path of one.
	Addr string
	// Value is the rule's substitute.
	Value any
	// Max is the longest grant.
	Max           time.Duration
	AckOnStart    bool
	OperatorValue bool
}

// WithSubstitutes gives a run the manifest's substitute rules, which is
// what makes the `substitute:` step mean anything. Without it a test using
// the step fails with that as the reason, rather than silently passing.
func WithSubstitutes(rules []SubstituteRule) Option {
	return func(o *runOptions) { o.substitutes = rules }
}

// SubstituteStep is a step's `substitute:` verb.
type SubstituteStep struct {
	Tag   string    `yaml:"tag"`
	Value any       `yaml:"value"`
	For   *Duration `yaml:"for"`
	By    string    `yaml:"by"`
}

func (r *testRun) rule(addr string) (SubstituteRule, bool) {
	for _, s := range r.substitutes {
		if strings.EqualFold(s.Addr, addr) {
			return s, true
		}
	}
	return SubstituteRule{}, false
}

// substitute applies a step's `restore:`, `substitute:` and
// `ack-substitutes:`, in that order, before its time is spent.
func (r *testRun) substitute(st *Step) error {
	if st.Substitute == nil && st.Restore == nil && st.AckSubstitutes == nil {
		return nil
	}
	if len(r.substitutes) == 0 {
		return fmt.Errorf("substitute: this run has no substitute rules: the project needs a `substitutes:` block, " +
			"and the runner must be given it (acceptance.WithSubstitutes; `naut test` does)")
	}
	if u := st.Restore; u != nil {
		for _, n := range u.Names {
			if !activeSubstitute(r.rt, n) {
				return fmt.Errorf("restore: %s is not substituted", n)
			}
		}
		for _, s := range r.rt.Substitutes() {
			if u.All || containsFold(u.Names, s.Name) {
				r.rt.Restore(s.ID, "test")
			}
		}
	}
	if s := st.Substitute; s != nil {
		if s.Tag == "" {
			return fmt.Errorf("substitute: needs `tag:`")
		}
		rule, ok := r.rule(s.Tag)
		if !ok {
			return fmt.Errorf("substitute: %s is not substitutable: no `substitutes:` rule names it", s.Tag)
		}
		value := rule.Value
		if s.Value != nil {
			v, err := r.coerce(s.Tag, s.Value)
			if err != nil {
				return fmt.Errorf("substitute %s: %w", s.Tag, err)
			}
			if rv, err := r.coerce(s.Tag, rule.Value); err != nil || fmt.Sprint(rv) != fmt.Sprint(v) {
				if !rule.OperatorValue {
					return fmt.Errorf("substitute %s: the rule's value is %v and it does not allow another (operator-value: true)", s.Tag, rule.Value)
				}
			}
			value = v
		}
		d := rule.Max
		if s.For != nil {
			if d = s.For.get(); d <= 0 {
				return fmt.Errorf("substitute %s: for: must be positive", s.Tag)
			}
			if d > rule.Max {
				return fmt.Errorf("substitute %s: for: %s is over the rule's max of %s", s.Tag, d, rule.Max)
			}
		}
		by := s.By
		if by == "" {
			by = "test"
		}
		if _, err := r.rt.Substitute(s.Tag, runtime.SubstituteRequest{Value: value, For: d, By: by, AckOnStart: rule.AckOnStart}); err != nil {
			return err
		}
	}
	if u := st.AckSubstitutes; u != nil {
		var ids []uint64
		for _, s := range r.rt.Substitutes() {
			if u.All || containsFold(u.Names, s.Name) {
				ids = append(ids, s.ID)
			}
		}
		r.rt.AckSubstitutes(ids, "test")
	}
	return nil
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func activeSubstitute(rt *runtime.Runtime, name string) bool {
	for _, s := range rt.Substitutes() {
		if strings.EqualFold(s.Name, name) {
			return true
		}
	}
	return false
}
