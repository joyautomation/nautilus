package alarm

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RationalizationState is where an alarm stands in ISA-18.2
// rationalization (#715): not yet reviewed, reviewed by the team that
// owns it, or approved (signed off under management of change).
type RationalizationState string

const (
	RationalizationPending  RationalizationState = "pending"
	RationalizationReviewed RationalizationState = "reviewed"
	RationalizationApproved RationalizationState = "approved"
)

// RationalizationStates lists the states in lifecycle order.
var RationalizationStates = []RationalizationState{
	RationalizationPending, RationalizationReviewed, RationalizationApproved,
}

// ParseRationalizationState accepts a state name, case-insensitively.
// The empty string is pending: an alarm nobody has reviewed says nothing.
func ParseRationalizationState(s string) (RationalizationState, error) {
	if s == "" {
		return RationalizationPending, nil
	}
	for _, st := range RationalizationStates {
		if strings.EqualFold(s, string(st)) {
			return st, nil
		}
	}
	return "", fmt.Errorf("unknown rationalization state %q (want pending, reviewed or approved)", s)
}

// RationalizationDate is the layout of Rationalization.At.
const RationalizationDate = "2006-01-02"

// Rationalization is the `rationalization:` block of a definition or a
// rule: the review state and, once reviewed, who reviewed it and on what
// day. The zero value is pending.
type Rationalization struct {
	State RationalizationState `yaml:"state"`
	By    string               `yaml:"by,omitempty"`
	At    string               `yaml:"at,omitempty"` // YYYY-MM-DD
}

// Effective is the state with the empty string read as pending.
func (r Rationalization) Effective() RationalizationState {
	if r.State == "" {
		return RationalizationPending
	}
	return r.State
}

// IsZero reports a block that says nothing: pending, with no reviewer.
func (r Rationalization) IsZero() bool {
	return r.Effective() == RationalizationPending && r.By == "" && r.At == ""
}

// Validate checks the state and that a reviewed or approved alarm names
// who and when: an approval nobody signed is not one.
func (r Rationalization) Validate() error {
	st, err := ParseRationalizationState(string(r.State))
	if err != nil {
		return err
	}
	if st == RationalizationPending {
		return nil
	}
	if strings.TrimSpace(r.By) == "" || r.At == "" {
		return fmt.Errorf("rationalization: state %s needs by: (who) and at: (the day, YYYY-MM-DD)", st)
	}
	if _, err := time.Parse(RationalizationDate, r.At); err != nil {
		return fmt.Errorf("rationalization.at %q: want a day as YYYY-MM-DD", r.At)
	}
	return nil
}

// UnmarshalYAML keeps the block strict (see Match.UnmarshalYAML: a nested
// mapping decoded through a fresh decoder would lose KnownFields) and
// validates it at load, so `naut check` reports a bad state or an
// unsigned approval.
//
// at: is read from the scalar's text, so `at: 2026-11-20` unquoted (which
// YAML would type as a timestamp) still arrives as the day written.
func (r *Rationalization) UnmarshalYAML(node *yaml.Node) error {
	type wire struct {
		State string    `yaml:"state"`
		By    string    `yaml:"by"`
		At    yaml.Node `yaml:"at"`
	}
	var w wire
	if err := strictDecode(node, &w); err != nil {
		return err
	}
	st, err := ParseRationalizationState(w.State)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	out := Rationalization{State: st, By: strings.TrimSpace(w.By), At: strings.TrimSpace(w.At.Value)}
	if err := out.Validate(); err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	*r = out
	return nil
}
