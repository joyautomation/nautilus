package devprofile

import (
	"fmt"
	"strings"
	"time"
)

// AlarmSpec is an alarm a device of the profile raises by default (#717
// PR 5b): the BOOL point, polled or derived, whose TRUE is the alarm, and
// the fields of an alarm definition a profile may set. nautilus renders one
// alarm rule over the profile's device type, so one spec covers every device
// of the type; the rule is enabled only while the device answers
// (<Device>__Online), so a device that went quiet suppresses its alarms
// instead of holding them on. A project's own alarms: win over it.
type AlarmSpec struct {
	// Point is the BOOL point or derived point.
	Point string `yaml:"point"`
	// Name is the alarm's name, a template over the alarm rule's
	// placeholders ({desc} {tag} {member} {type} {path}); default
	// "{desc} {member}".
	Name string `yaml:"name"`
	// Priority is one of diagnostic, low, medium, high, critical; omitted
	// takes what an alarm with no priority gets (the project's alarms
	// defaults, else medium).
	Priority string `yaml:"priority"`
	// OnDelay is how long the point stays true before the alarm raises
	// ("2s"); omitted takes the project's default.
	OnDelay string `yaml:"on-delay"`
	Class   string `yaml:"class"`
	Notes   string `yaml:"notes"`
}

// AlarmPriorities are the priority names a spec may use, the alarm
// package's own (a test in internal/project keeps the two equal).
var AlarmPriorities = []string{"diagnostic", "low", "medium", "high", "critical"}

func (p *Profile) validateAlarms() error {
	seen := map[string]bool{}
	for i := range p.Alarms {
		a := &p.Alarms[i]
		if a.Point == "" {
			return fmt.Errorf("alarms[%d]: point: is required", i)
		}
		name, ok := p.boolPoint(a.Point)
		if !ok {
			return fmt.Errorf("alarms[%d]: point %q is not a BOOL point or derived point of %s (an alarm's condition is a BOOL)", i, a.Point, p.ID)
		}
		a.Point = name
		k := strings.ToLower(name)
		if seen[k] {
			return fmt.Errorf("alarms[%d]: %s has an alarm already", i, name)
		}
		seen[k] = true
		if err := checkTemplate(a.Name); err != nil {
			return fmt.Errorf("alarms[%d]: name: %w", i, err)
		}
		if a.Priority != "" {
			found := false
			for _, n := range AlarmPriorities {
				found = found || strings.EqualFold(n, a.Priority)
			}
			if !found {
				return fmt.Errorf("alarms[%d]: priority %q (want one of %s)", i, a.Priority, strings.Join(AlarmPriorities, ", "))
			}
		}
		if a.OnDelay != "" {
			if d, err := time.ParseDuration(a.OnDelay); err != nil || d < 0 {
				return fmt.Errorf("alarms[%d]: on-delay %q is not a duration like 2s", i, a.OnDelay)
			}
		}
	}
	return nil
}

// boolPoint finds a BOOL point or derived point by name, as declared.
func (p *Profile) boolPoint(name string) (string, bool) {
	if pt, ok := p.Point(name); ok && strings.EqualFold(pt.Type, "BOOL") {
		return pt.Name, true
	}
	for _, d := range p.Derived {
		if strings.EqualFold(d.Name, name) {
			return d.Name, true
		}
	}
	return "", false
}

// alarmPlaceholders are what an alarm name template may interpolate (the
// alarm package's, which this leaf cannot import; a test in internal/project
// compiles a profile's names through the alarm package).
var alarmPlaceholders = map[string]bool{"tag": true, "member": true, "site": true, "area": true, "desc": true, "type": true, "path": true}

// checkTemplate refuses an unknown or unclosed {placeholder} in a name.
func checkTemplate(s string) error {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			j := strings.IndexByte(s[i:], '}')
			if j < 0 {
				return fmt.Errorf("%q has an unclosed {", s)
			}
			if name := s[i+1 : i+j]; !alarmPlaceholders[name] {
				return fmt.Errorf("%q: unknown placeholder {%s} (want {tag} {member} {site} {area} {desc} {type} {path})", s, name)
			}
			i += j
		case '}':
			return fmt.Errorf("%q has a } with no {", s)
		}
	}
	return nil
}
