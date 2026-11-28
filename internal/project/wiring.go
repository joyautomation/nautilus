package project

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/wiring"
)

// composeWiring reads the project's struct wiring (wirings: and instances:
// in the manifest, and every file under wiring-files:), checks it as one
// namespace, and then refuses it: nothing runs a wiring yet (#1458, slice 5
// of docs/design/one-struct-model.md §18). Checking first means a project
// that adds wiring early learns every mistake in it at once, and the
// refusal below is the last thing it hears, not the first.
//
// raw is the manifest's text, for the position of each inline declaration.
func composeWiring(fsys fs.FS, m *Manifest, manifestName string, raw []byte) error {
	if len(m.Wirings) == 0 && len(m.Instances) == 0 && len(m.WiringFiles) == 0 {
		return nil
	}
	var files []*wiring.File
	for _, f := range m.WiringFiles {
		p, err := projectPath(f)
		if err != nil {
			return fmt.Errorf("wiring-files: %w", err)
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("wiring-files: %w", err)
		}
		wf, err := wiring.Decode(p, b)
		if err != nil {
			hint := "a wiring file holds wirings: and instances:"
			if strings.Contains(err.Error(), "field devices not found") {
				hint = "devices: rows in a wiring file arrive with slice 10 of #1458; keep them in nautilus.yaml for now"
			}
			return fmt.Errorf("wiring-files: %w (%s)", err, hint)
		}
		files = append(files, wf)
	}
	inline := &wiring.File{Wirings: m.Wirings, Instances: m.Instances}
	inline.SetPositions(manifestName, raw)
	files = append(files, inline)

	problems := wiring.Check(files, wiring.Env{Driver: m.wiringDriver})
	problems = append(problems, instanceTagClashes(files, m)...)
	if err := wiring.Err(problems); err != nil {
		return err
	}
	return fmt.Errorf("%s: wirings and instances are decoded and checked, but this build does not run them yet "+
		"(struct wiring, #1458, arrives slice by slice): remove wirings:, instances: and wiring-files: until it does", manifestName)
}

// instanceTagClashes reports an instance named like a tag: they share one
// namespace, since an instance is a tag of its type.
func instanceTagClashes(files []*wiring.File, m *Manifest) []wiring.Problem {
	tags := make(map[string]string, len(m.Tags))
	for _, t := range m.Tags {
		tags[ir.NameKey(t.Name)] = t.Name
	}
	for _, d := range m.Devices {
		tags[ir.NameKey(d.Name)] = d.Name + " (a devices: row)"
	}
	var out []wiring.Problem
	for _, f := range files {
		for _, in := range f.Instances {
			if t, ok := tags[ir.NameKey(in.Name)]; ok {
				out = append(out, wiring.Problem{Pos: in.Pos, Msg: fmt.Sprintf(
					"instance %q has the name of the tag %s: an instance is a tag of its type, so a name is declared once (rename one)", in.Name, t)})
			}
		}
	}
	return out
}

// wiringDriver resolves a source's `driver:` to a driver type: a configured
// driver by its name, else a driver type nothing but a wiring needs to
// configure (a Modbus bus is added for devices:, and so for a wiring).
func (m *Manifest) wiringDriver(name string) (string, bool) {
	configs := m.Drivers
	if len(configs) == 0 && m.Driver.Type != "" {
		configs = []DriverConfig{m.Driver}
	}
	for i, n := range driverNames(configs) {
		if strings.EqualFold(n, name) {
			return strings.ToLower(configs[i].Type), true
		}
	}
	if t := strings.ToLower(name); wiring.IsDriverType(t) {
		return t, true
	}
	return "", false
}
