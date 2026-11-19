package project

// The manifest tier's half of substitute values (#707): the top-level
// `substitutes:` block, resolved against the project's input tags at Load.
//
//	substitutes:
//	  defaults: {max: 8h, role: supervisor, ack-on-start: true}
//	  rules:
//	    - match: "LS_*"        # a tag name or glob; "IOL_P3.Level" names a member
//	      substitute: false    # the value the program runs on
//	      max: 2h
//
// The rules are not keys in the tag files: those are generated (`naut
// plcnext import`, the IO-Link profiles) and would lose them on the next
// import. The runtime half (timing, quality, companions) is runtime/
// substitute*.go; this file decides WHICH inputs may be substituted, on
// what value, for how long at most.

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// Bounds on a substitute's longest grant.
const (
	MinSubstituteMax = time.Minute
	MaxSubstituteMax = 7 * 24 * time.Hour
	// DefaultSubstituteMax is the longest grant of a rule that, and the
	// block's defaults, name none.
	DefaultSubstituteMax = 8 * time.Hour
	// DefaultSubstituteRole is the role a rule names when nothing else
	// does. The built-in roles arrive with #708; until then it is shown
	// by the API and not enforced.
	DefaultSubstituteRole = "supervisor"
)

// SubstitutesConfig is the manifest's `substitutes:` section.
type SubstitutesConfig struct {
	// Defaults is what a rule gets for a key it leaves out.
	Defaults SubstituteDefaults `yaml:"defaults"`
	// Rules are tried in order; the first that matches an input decides it.
	Rules []SubstituteRule `yaml:"rules"`
}

// SubstituteDefaults are the keys a rule may leave to the block.
type SubstituteDefaults struct {
	Max           Duration `yaml:"max"`
	Role          string   `yaml:"role"`
	AckOnStart    *bool    `yaml:"ack-on-start"`
	OperatorValue *bool    `yaml:"operator-value"`
}

// SubstituteRule makes the inputs it matches substitutable.
type SubstituteRule struct {
	// Match is a tag name or glob (case-insensitive) over the project's
	// input tags. A name with a dot ("IOL_P3.Level") substitutes one
	// member of each matching struct tag: the part before the first dot is
	// the glob, the rest a member path.
	Match string `yaml:"match"`
	// Substitute is the value the program runs on, checked against the
	// tag's type as a write is. Required.
	Substitute any `yaml:"substitute"`
	// Max is the longest a person may grant, 1m to 168h (7 days).
	Max Duration `yaml:"max"`
	// Role is the #708 role allowed to start one.
	Role string `yaml:"role"`
	// AckOnStart: the program's start interlock waits (the
	// __SubstitutesUnacked tag) until the substitute is acknowledged.
	AckOnStart *bool `yaml:"ack-on-start"`
	// OperatorValue lets the person starting one choose the value (same
	// type check). Off by default: the engineer chose the safe substitute.
	OperatorValue *bool `yaml:"operator-value"`
}

// SubstituteSpec is one substitutable address with its rule resolved.
type SubstituteSpec struct {
	// Addr is the substitutable address: a tag, or a member path.
	Addr string
	// Root is its input tag.
	Root          string
	Value         any
	Max           time.Duration
	Role          string
	AckOnStart    bool
	OperatorValue bool
	// Rule is the index of the rule that decided it.
	Rule int
}

// Substitutes are the project's resolved substitute specs, in rule then
// tag order; nil when the manifest has no `substitutes:`.
func (p *Project) Substitutes() []SubstituteSpec { return p.substitutes }

// resolveSubstitutes resolves the block against the project's tags. It
// refuses what could never work: a rule with no `substitute:` or an
// impossible bound, a rule that matches no input (and says whether it
// matched a tag that is not one), two rules with the same match. Value
// types are checked later, against the compiled runtime (CheckSubstitutes).
func resolveSubstitutes(c *SubstitutesConfig, tags []runtime.TagDef) ([]SubstituteSpec, error) {
	if c == nil {
		return nil, nil
	}
	d := c.Defaults
	if err := checkSubstituteBounds("substitutes.defaults", d.Max); err != nil {
		return nil, err
	}
	if len(c.Rules) == 0 {
		return nil, fmt.Errorf("substitutes: needs at least one rule (a `match:` and a `substitute:` value)")
	}
	var inputs, others []string
	for _, t := range tags {
		if t.Role == runtime.RoleInput {
			inputs = append(inputs, t.Name)
		} else {
			others = append(others, t.Name)
		}
	}
	var out []SubstituteSpec
	seenMatch := map[string]int{}
	taken := map[string]int{} // address (lower-case) → the rule that took it
	for i, r := range c.Rules {
		where := fmt.Sprintf("substitutes.rules[%d]", i)
		if strings.TrimSpace(r.Match) == "" {
			return nil, fmt.Errorf("%s: match: is required (a tag name or glob)", where)
		}
		key := strings.ToLower(r.Match)
		if j, dup := seenMatch[key]; dup {
			return nil, fmt.Errorf("%s: match %q repeats rules[%d]: the first would always win", where, r.Match, j)
		}
		seenMatch[key] = i
		if r.Substitute == nil {
			return nil, fmt.Errorf("%s (%s): substitute: is required (the value the program runs on)", where, r.Match)
		}
		if err := checkSubstituteBounds(where, r.Max); err != nil {
			return nil, err
		}
		spec := SubstituteSpec{
			Value: normalize(r.Substitute), Rule: i,
			Max:  pickDuration(time.Duration(r.Max), time.Duration(d.Max), DefaultSubstituteMax),
			Role: pickString(r.Role, d.Role, DefaultSubstituteRole),
		}
		spec.AckOnStart = pickBool(r.AckOnStart, d.AckOnStart)
		spec.OperatorValue = pickBool(r.OperatorValue, d.OperatorValue)
		rootGlob, member, dotted := strings.Cut(r.Match, ".")
		matched, decided, shadow := false, 0, -1
		for _, in := range inputs {
			if ok, err := path.Match(strings.ToLower(rootGlob), strings.ToLower(in)); err != nil {
				return nil, fmt.Errorf("%s: match %q: %w", where, r.Match, err)
			} else if !ok {
				continue
			}
			matched = true
			s := spec
			s.Root, s.Addr = in, in
			if dotted {
				s.Addr = in + "." + member
			}
			if j, was := taken[strings.ToLower(s.Addr)]; was {
				shadow = j // an earlier rule decided it: the first match wins
				continue
			}
			taken[strings.ToLower(s.Addr)] = i
			decided++
			out = append(out, s)
		}
		if matched && decided == 0 {
			return nil, fmt.Errorf("%s: match %q is shadowed by rules[%d]: every input it matches is already decided there, so it would change nothing (put the specific rule first)", where, r.Match, shadow)
		}
		if matched {
			continue
		}
		for _, o := range others {
			if ok, _ := path.Match(strings.ToLower(rootGlob), strings.ToLower(o)); ok {
				return nil, fmt.Errorf("%s: match %q names %s, which is not an input: only a driver-fed input can be substituted (an output or a setpoint is the logic's or the operator's)", where, r.Match, o)
			}
		}
		return nil, fmt.Errorf("%s: match %q matches no input tag in this project", where, r.Match)
	}
	return out, nil
}

func checkSubstituteBounds(where string, max Duration) error {
	if max == 0 {
		return nil
	}
	if d := time.Duration(max); d < MinSubstituteMax || d > MaxSubstituteMax {
		return fmt.Errorf("%s: max: %s is outside %s to %s (write 7 days as 168h)", where, d, MinSubstituteMax, MaxSubstituteMax)
	}
	return nil
}

func pickDuration(vs ...time.Duration) time.Duration {
	for _, v := range vs {
		if v != 0 {
			return v
		}
	}
	return 0
}

func pickString(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func pickBool(vs ...*bool) bool {
	for _, v := range vs {
		if v != nil {
			return *v
		}
	}
	return false
}

// substitutableRoots are the input tags a project's specs name, once each,
// in the order first seen: runtime.Options.Substitutable.
func substitutableRoots(specs []SubstituteSpec) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range specs {
		if k := strings.ToLower(s.Root); !seen[k] {
			seen[k] = true
			out = append(out, s.Root)
		}
	}
	return out
}

// CheckSubstitutes holds every spec's value to its address's type, as a
// write to it would be, against the compiled runtime: what `naut check`
// reports. A project without the block has nothing to say.
func (p *Project) CheckSubstitutes(rt *runtime.Runtime) (errs []string) {
	for _, s := range p.substitutes {
		if err := rt.CheckSubstituteValue(s.Addr, s.Value); err != nil {
			errs = append(errs, fmt.Sprintf("substitutes.rules[%d]: the substitute for %s: %v", s.Rule, s.Addr, err))
		}
	}
	return errs
}

// AcceptanceSubstitutes is the project's rules in the form the `naut test`
// harness takes (acceptance.WithSubstitutes).
func (p *Project) AcceptanceSubstitutes() []acceptance.SubstituteRule {
	out := make([]acceptance.SubstituteRule, len(p.substitutes))
	for i, s := range p.substitutes {
		out[i] = acceptance.SubstituteRule{Addr: s.Addr, Value: s.Value, Max: s.Max,
			AckOnStart: s.AckOnStart, OperatorValue: s.OperatorValue}
	}
	return out
}

// serverSubstitutes is the specs in the form the server answers for.
func serverSubstitutes(specs []SubstituteSpec) []server.SubstituteRule {
	var out []server.SubstituteRule
	for _, s := range specs {
		out = append(out, server.SubstituteRule{Tag: s.Addr, Value: s.Value, MaxMs: s.Max.Milliseconds(),
			Role: s.Role, AckOnStart: s.AckOnStart, OperatorValue: s.OperatorValue, Rule: s.Rule})
	}
	return out
}
