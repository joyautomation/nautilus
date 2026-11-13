package project

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

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
}

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
		ID   string            `json:"id"`
		From *pipeEnd          `json:"from"`
		To   *pipeEnd          `json:"to"`
		Bind map[string]string `json:"bind"`
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
func (p *Project) CheckMimics(fsys fs.FS) ([]MimicReport, []string, []string) {
	tags := map[string]runtime.TagDef{}
	for _, d := range p.Runtime.Tags {
		tags[d.Name] = d
	}
	files, sources, sidecars := scanScreens(fsys)
	var reports []MimicReport
	var errs, warns []string
	for _, f := range files {
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", f, err))
			continue
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
				say("%s binds %s, which the project doesn't declare%s", where, name, nearest(root, tags))
			}
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
			}
			for _, prop := range sortedKeys(eq.Bind) {
				tag(where+"'s "+prop, eq.Bind[prop])
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

// scanScreens finds the project's mimics, its {Name}.svelte components and
// the ports their {Name}.component.json give them.
func scanScreens(fsys fs.FS) (mimics []string, sources map[string][]string, ports map[string][]string) {
	sources, ports = map[string][]string{}, map[string][]string{}
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
			}
			if raw, err := fs.ReadFile(fsys, p); err == nil && json.Unmarshal(raw, &sc) == nil {
				name := strings.TrimSuffix(n, ".component.json")
				for _, pt := range sc.Ports {
					ports[name] = append(ports[name], pt.Name)
				}
			}
		}
		return nil
	})
	sort.Strings(mimics)
	return mimics, sources, ports
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
