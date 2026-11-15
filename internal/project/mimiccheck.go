package project

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/internal/mimicdoc"
	"github.com/joyautomation/nautilus/runtime"
)

// MimicReport is one checked *.mimic.json.
type MimicReport struct {
	File      string
	Equipment int
	Pipes     int
}

// The kit's equipment and the ports each has (hmi mimic.ts BUILTIN_PORTS):
// a pipe may end on these without a .component.json.
var builtinPorts = map[string][]string{
	"Tank": {"top", "left", "right", "bottom"}, "Pump": {"in", "out"}, "Valve": {"in", "out"},
	"Heater": nil, "Gauge": nil, "Sparkline": nil, "CommandButton": nil, "WriteToggle": nil,
	"OffPage":    {"top", "left", "right", "bottom"},
	"OnOffValve": {"left", "right", "top", "bottom"}, "Instrument": {"left", "right", "top", "bottom"},
	"Machine": {"left", "right", "top", "bottom"}, "SetpointGroup": nil,
}

// builtinProps are the props each kit component takes (beyond label,
// width, height, look and write, which <Mimic> hands every one). A bind or
// a prop it doesn't take shows nothing.
var builtinProps = map[string][]string{
	"Tank": {"levelPct", "scale", "shape"}, "Pump": {"running", "speedPct"}, "Valve": {"openPct"},
	"Heater": {"powerPct"}, "Gauge": {"value", "min", "max", "unit", "color", "setpoint", "decimals", "sweep", "gap", "thickness"},
	"Sparkline":     {"values", "color", "yMin", "yMax", "endIndex", "windowSize", "compact"},
	"CommandButton": {"tag", "kind", "pulseMs", "held", "disabled", "disabledReason"},
	"WriteToggle":   {"tag", "value", "present", "onColor", "offColor", "alarm", "invert", "readonly", "readonlyReason"},
	"OffPage":       {"dir"},
	"OnOffValve":    {"tag", "open", "travelling", "failed", "vertical"},
	"Instrument":    {"tag", "value", "unit", "decimals", "alarm", "present"},
	"Machine":       {"kind", "tag", "running", "faulted", "speedPct"},
	"SetpointGroup": {"title", "items", "columns"},
}

// equipmentKeys are the keys a piece of equipment has; anything else in
// it is ignored when the screen runs.
var equipmentKeys = []string{"id", "component", "x", "y", "width", "height", "behind", "label", "props", "bind", "ports"}

// writers are the kit's controls: the `tag` prop is the tag they write.
var writers = map[string]bool{"CommandButton": true, "WriteToggle": true}

type mimicFile struct {
	Equipment []struct {
		ID        string            `json:"id"`
		Component string            `json:"component"`
		Bind      map[string]string `json:"bind"`
		Props     map[string]any    `json:"props"`
	} `json:"equipment"`
	Pipes []struct {
		ID     string            `json:"id"`
		Points [][]float64       `json:"points"`
		From   *pipeEnd          `json:"from"`
		To     *pipeEnd          `json:"to"`
		Bind   map[string]string `json:"bind"`
	} `json:"pipes"`
	Labels []struct {
		Text string `json:"text"`
		Bind string `json:"bind"`
	} `json:"labels"`
}

type pipeEnd struct {
	Equip string `json:"equip"`
	Port  string `json:"port"`
}

// CheckMimics checks every *.mimic.json against the project, offline, as
// CheckScenes does scenes: each tag a binding, a label or a control names
// is declared (a control's is one the tag API lets it write); each
// component is the kit's or the project's own {Name}.svelte; equipment ids
// are unique; each pipe end names equipment that's there, at a port it has
// (the kit's, or its {Name}.component.json's).
//
// strict is false for a manifest other than the project's own (-m line.yaml):
// a screen is drawn for the project's nautilus.yaml, and a second manifest
// may leave tags it binds out on purpose, so those are warnings there.
func (p *Project) CheckMimics(fsys fs.FS, strict bool) ([]MimicReport, []string, []string) {
	tags := map[string]runtime.TagDef{}
	for _, d := range p.Runtime.Tags {
		tags[d.Name] = d
	}
	files, sources, sidecars, examples := scanScreens(fsys)
	var reports []MimicReport
	var errs, warns []string
	unshown := map[string]bool{} // project components without examples, said once
	for _, f := range files {
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", f, err))
			continue
		}
		// a unit's copies are equipment, pipes and labels like any other
		if x, _, err := mimicdoc.ExpandUnits(raw); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", f, err))
			continue
		} else {
			raw = x
		}
		var m mimicFile
		if err := json.Unmarshal(raw, &m); err != nil {
			errs = append(errs, fmt.Sprintf("%s: not a mimic: %v", f, err))
			continue
		}
		say := func(format string, a ...any) { errs = append(errs, f+": "+fmt.Sprintf(format, a...)) }
		tag := func(where, name string) {
			root := strings.TrimPrefix(strings.TrimSpace(name), "!")
			if i := strings.IndexAny(root, ".["); i > 0 {
				root = root[:i]
			}
			if root == "" {
				return
			}
			if _, ok := tags[root]; !ok {
				if strict {
					say("%s binds %s, which the project doesn't declare%s", where, name, nearest(root, tags))
				} else {
					warns = append(warns, fmt.Sprintf("%s: %s binds %s, which this manifest doesn't declare (the screen shows nothing for it here)", f, where, name))
				}
			}
		}
		// keys equipment doesn't have, and props a kit component doesn't take
		var keyed struct {
			Equipment []map[string]json.RawMessage `json:"equipment"`
		}
		_ = json.Unmarshal(raw, &keyed)
		// one warning for each mistake, naming the first piece that makes it
		// (a unit's copies make it together)
		type noted struct {
			ids []string
			say func(id, more string) string
		}
		var order []string
		notes := map[string]*noted{}
		note := func(key, id string, say func(id, more string) string) {
			if notes[key] == nil {
				notes[key] = &noted{say: say}
				order = append(order, key)
			}
			notes[key].ids = append(notes[key].ids, id)
		}
		for i, e := range keyed.Equipment {
			if i >= len(m.Equipment) {
				break
			}
			eq := m.Equipment[i]
			takes, kit := builtinProps[eq.Component]
			has := func(k string) bool {
				for _, t := range append(takes, "label", "width", "height", "look", "write") {
					if t == k {
						return true
					}
				}
				return false
			}
			for _, k := range sortedKeys(e) {
				if hasKey(equipmentKeys, k) || k == "fromUnit" {
					continue
				}
				hint := "a component's own props go in \"props\", its live ones in \"bind\""
				if kit && has(k) {
					hint = fmt.Sprintf("put it in \"props\": {%q: %s}", k, string(e[k]))
				}
				note("top\x00"+k+"\x00"+hint, eq.ID, func(id, more string) string {
					return fmt.Sprintf("%s: equipment %s%s has %q, which equipment doesn't have (it has %s), so the screen ignores it: %s", f, id, more, k, strings.Join(equipmentKeys, ", "), hint)
				})
			}
			if !kit {
				continue
			}
			for _, part := range []struct {
				name string
				keys []string
			}{{"bind", sortedKeys(eq.Bind)}, {"props", sortedKeys(eq.Props)}} {
				for _, k := range part.keys {
					if !has(k) {
						note(eq.Component+"\x00"+part.name+"\x00"+k, eq.ID, func(id, more string) string {
							return fmt.Sprintf("%s: equipment %s's %s%s has %s, which %s doesn't take, so it shows nothing (%s takes %s)", f, id, part.name, more, k, eq.Component, eq.Component, strings.Join(takes, ", "))
						})
					}
				}
			}
		}
		for _, k := range order {
			n := notes[k]
			more := ""
			if len(n.ids) > 1 {
				more = fmt.Sprintf(" (and %d more like it)", len(n.ids)-1)
			}
			warns = append(warns, n.say(n.ids[0], more))
		}
		ports := map[string][]string{} // equipment id -> its ports
		components := map[string]string{}
		seen := map[string]bool{}
		for _, eq := range m.Equipment {
			where := "equipment " + eq.ID
			if eq.ID == "" {
				say("equipment with no id (component %s)", eq.Component)
			} else if seen[eq.ID] {
				say("two pieces of equipment are %s", eq.ID)
			}
			seen[eq.ID] = true
			components[eq.ID] = eq.Component
			if bp, ok := builtinPorts[eq.Component]; ok {
				ports[eq.ID] = bp
			} else if len(sources[eq.Component]) == 0 {
				say("%s is a %s, and there is no %s.svelte in the project (the kit's own are %s)", where, eq.Component, eq.Component, strings.Join(sortedKeys(builtinPorts), ", "))
			} else {
				ports[eq.ID] = sidecars[eq.Component]
				if !examples[eq.Component] && !unshown[eq.Component] {
					unshown[eq.Component] = true
					warns = append(warns, fmt.Sprintf("%s: %s has no examples: give its %s.component.json \"examples\", the props for each state it draws (\"examples\": {\"running\": {...}, \"faulted\": {...}}). The screens' /components page draws each one, and naut check renders it", f, eq.Component, eq.Component))
				}
			}
			for _, prop := range sortedKeys(eq.Bind) {
				tag(where+"'s "+prop, eq.Bind[prop])
			}
			if eq.Component == "SetpointGroup" {
				items, _ := eq.Props["items"].([]any)
				if len(items) == 0 {
					say("%s is a SetpointGroup with no items (props.items: [{\"tag\": ..., \"label\": ...}])", where)
				}
				for i, it := range items {
					m, _ := it.(map[string]any)
					t, _ := m["tag"].(string)
					if t == "" {
						say("%s's item %d has no tag", where, i+1)
					} else if d, ok := tags[t]; !ok {
						say("%s's item %d writes %s, which the project doesn't declare%s", where, i+1, t, nearest(t, tags))
					} else if d.Role == runtime.RoleInput {
						warns = append(warns, fmt.Sprintf("%s: %s's item %d writes %s, an input: the tag API refuses a write to an input a driver owns", f, where, i+1, t))
					}
				}
			}
			if writers[eq.Component] {
				t, _ := eq.Props["tag"].(string)
				if t == "" {
					say("%s is a %s with no tag to write (props.tag)", where, eq.Component)
				} else if d, ok := tags[t]; !ok {
					say("%s writes %s, which the project doesn't declare%s", where, t, nearest(t, tags))
				} else if d.Role == runtime.RoleInput {
					warns = append(warns, fmt.Sprintf("%s: %s writes %s, an input: the tag API refuses a write to an input a driver owns", f, where, t))
				}
			}
		}
		for _, pp := range m.Pipes {
			where := "pipe " + pp.ID
			// a pipe draws through its points and its anchored ends; with both
			// ends anchored it needs no points at all
			if n := len(pp.Points) + btoi(pp.From != nil && pp.From.Equip != "") + btoi(pp.To != nil && pp.To.Equip != ""); n < 2 {
				say("%s has nothing to draw: give it points, or anchor both ends with from and to", where)
			}
			for _, end := range []*pipeEnd{pp.From, pp.To} {
				if end == nil || end.Equip == "" {
					continue
				}
				comp, ok := components[end.Equip]
				if !ok {
					say("%s ends on %s, which isn't on the screen", where, end.Equip)
					continue
				}
				if end.Port != "" && !hasPort(ports[end.Equip], end.Port) {
					have := "no ports"
					if len(ports[end.Equip]) > 0 {
						have = "ports " + strings.Join(ports[end.Equip], ", ")
					}
					hint := ""
					if _, kit := builtinPorts[comp]; !kit {
						hint = " (a component's ports are in its " + comp + ".component.json)"
					}
					say("%s ends on %s's port %q; a %s has %s%s", where, end.Equip, end.Port, comp, have, hint)
				}
			}
			for _, prop := range sortedKeys(pp.Bind) {
				tag(where+"'s "+prop, pp.Bind[prop])
			}
		}
		for i, l := range m.Labels {
			if l.Bind != "" {
				where := fmt.Sprintf("label %d", i+1)
				if l.Text != "" {
					where = "label " + l.Text
				}
				tag(where, l.Bind)
			}
		}
		reports = append(reports, MimicReport{File: f, Equipment: len(m.Equipment), Pipes: len(m.Pipes)})
	}
	return reports, errs, warns
}

// scanScreens finds the project's mimics, its {Name}.svelte components,
// the ports their {Name}.component.json give them, and which give
// examples (named sets of props, one per state the component draws).
func scanScreens(fsys fs.FS) (mimics []string, sources map[string][]string, ports map[string][]string, examples map[string]bool) {
	sources, ports, examples = map[string][]string{}, map[string][]string{}, map[string]bool{}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		n := d.Name()
		if d.IsDir() {
			if p != "." && (strings.HasPrefix(n, ".") || n == "node_modules" || n == "build" || n == "dist" || isProject(fsys, p)) {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case strings.HasSuffix(n, ".mimic.json"):
			mimics = append(mimics, p)
		case strings.HasSuffix(n, ".svelte") && !strings.HasPrefix(n, "+"):
			name := strings.TrimSuffix(n, ".svelte")
			sources[name] = append(sources[name], p)
		case strings.HasSuffix(n, ".component.json"):
			var sc struct {
				Ports []struct {
					Name string `json:"name"`
				} `json:"ports"`
				Examples map[string]json.RawMessage `json:"examples"`
			}
			if raw, err := fs.ReadFile(fsys, p); err == nil && json.Unmarshal(raw, &sc) == nil {
				name := strings.TrimSuffix(n, ".component.json")
				if len(sc.Examples) > 0 {
					examples[name] = true
				}
				for _, pt := range sc.Ports {
					ports[name] = append(ports[name], pt.Name)
				}
			}
		}
		return nil
	})
	sort.Strings(mimics)
	return mimics, sources, ports, examples
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func hasPort(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// nearest suggests a declared tag that differs only in case.
func nearest(name string, tags map[string]runtime.TagDef) string {
	for t := range tags {
		if strings.EqualFold(t, name) {
			return " (did you mean " + t + "?)"
		}
	}
	return ""
}

func hasKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}
