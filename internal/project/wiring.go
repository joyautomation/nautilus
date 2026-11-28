package project

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/wirebind"
	"github.com/joyautomation/nautilus/wiring"
)

// composeWiring reads the project's struct wiring (wirings: and instances:
// in the manifest, and every file under wiring-files:), checks it as one
// namespace against the project's TYPEs, and resolves it (wirebind): each
// instance becomes a tag of its type with its members' kinds, and its
// connection's companions (<Instance>__Online, and __Outages on Modbus)
// become input tags. The Modbus driver its source names gets its bindings
// (withWired). What this build does not run yet is refused, naming the
// slice of #1458 that brings it.
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

	types, terr := projectTypes(fsys, m)
	env := wiring.Env{Driver: m.wiringDriver}
	if terr == nil {
		env.Type = func(name string) ([]string, bool) {
			t, _, ok := ir.Lookup(types, name)
			if !ok || t.Kind != ir.TypeStruct {
				return nil, false
			}
			return memberPaths(t), true
		}
	}
	problems := wiring.Check(files, env)
	problems = append(problems, instanceTagClashes(files, m)...)
	if err := wiring.Err(problems); err != nil {
		return err
	}
	if terr != nil {
		// The sources report their own parse error when they compile;
		// without the TYPEs no instance can be resolved, so say why here.
		return fmt.Errorf("%s: the project's TYPEs could not be read, so its instances cannot be wired: %w", manifestName, terr)
	}
	set, problems := wirebind.Resolve(files, wirebind.Env{Types: types, Driver: m.wiringDriver})
	if err := wiring.Err(problems); err != nil {
		return err
	}
	tags, err := wiredTags(set, m)
	if err != nil {
		return err
	}
	m.Tags = append(m.Tags, tags...)
	// expr: members: checked in the project's scope (§6); the runtime
	// evaluates them each main scan (runtime/exprstage.go).
	shadows, problems := checkExprs(fsys, m, set, types)
	if len(problems) > 0 {
		return wiring.Err(sortedFindings(problems))
	}
	m.exprShadows = shadows
	// A member's unit: and desc: are its tag meta, under its path, as a
	// tag-meta: entry on a UDT member is; the project's own tag-meta: wins.
	for _, in := range set.Instances {
		for _, mem := range in.Members {
			if mem.Unit == "" && mem.Desc == "" {
				continue
			}
			key := in.Key(mem)
			if _, own := m.TagMeta[key]; own {
				continue
			}
			if m.TagMeta == nil {
				m.TagMeta = map[string]MetaConfig{}
			}
			m.TagMeta[key] = MetaConfig{Unit: mem.Unit, Desc: mem.Desc}
		}
	}
	m.wired = set
	m.wiringWarnings = wiredWarnings(m)
	return nil
}

// WiredOnTarget refuses a manifest's wired instances for a vendor target
// (logix, siemens) whose writer does not carry wiring yet (design §14,
// #1478): every check and deploy path that hands the manifest to a writer
// calls it, so check and deploy agree. A memory-only instance passes.
func WiredOnTarget(m *Manifest, target string) error {
	writer := map[string]string{"logix": "Logix", "siemens": "Siemens"}[target]
	if writer == "" || m.wired == nil {
		return nil
	}
	var errs []string
	for _, in := range m.wired.Instances {
		if !in.Wired() {
			continue
		}
		var srcs []string
		for _, c := range in.Conns {
			srcs = append(srcs, fmt.Sprintf("source %s, %s", c.Source, driverLabel(c.DriverType)))
		}
		how := strings.Join(srcs, "; ")
		if how == "" {
			how = "an expression member"
		}
		errs = append(errs, fmt.Sprintf("instance %s is wired (%s); the %s writer does not carry wiring yet (#1478), so nothing on the controller would fill it. "+
			"Not supported on this target yet: remove the instance, or run the project on nautilus", in.Name, how, writer))
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(errs, "; "))
}

func driverLabel(t string) string {
	switch t {
	case "modbus":
		return "Modbus"
	case "opcua-client":
		return "OPC UA"
	case "eip":
		return "EtherNet/IP"
	}
	return t
}

// projectTypes is the project's TYPE table, read as runtime.New will
// compose it: every library, with each ST task program (a TYPE may be
// declared in either).
func projectTypes(fsys fs.FS, m *Manifest) (map[string]*ir.Type, error) {
	libFiles, err := projectLibs(fsys, m)
	if err != nil {
		return nil, err
	}
	libs := make([]string, len(libFiles))
	for i, l := range libFiles {
		libs[i] = l.ST
	}
	srcs := []string{stproject.Join(libs, "")}
	for _, t := range m.Tasks {
		if t.Program == "" {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Clean(t.Program))
		if err != nil || runtime.Language(string(b)) != "st" {
			continue // reported when the task loads; an LD or FBD program declares no TYPE
		}
		srcs = append(srcs, stproject.Join(libs, string(b)))
	}
	out := map[string]*ir.Type{}
	for i, src := range srcs {
		prog, err := st.Parse(src)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			continue
		}
		types, err := st.Types(prog)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			continue
		}
		for k, t := range types {
			if t.Kind == ir.TypeFB {
				continue
			}
			if _, _, dup := ir.Lookup(out, k); !dup {
				out[k] = t
			}
		}
	}
	return out, nil
}

// memberPaths lists every member path of a struct type, structs included,
// without array indexes (wiring.Env.Type).
func memberPaths(t *ir.Type) []string {
	var out []string
	var walk func(prefix string, t *ir.Type)
	walk = func(prefix string, t *ir.Type) {
		for t != nil && t.Kind == ir.TypeArray {
			t = t.Elem
		}
		if t == nil || t.Kind != ir.TypeStruct || t.Struct == nil {
			return
		}
		for _, f := range t.Struct.Fields {
			p := f.Name
			if prefix != "" {
				p = prefix + "." + f.Name
			}
			out = append(out, p)
			walk(p, f.Type)
		}
	}
	walk("", t)
	return out
}

// wiredTags are the tags the instances declare: each instance (a tag of its
// type), and each connection's companions. A companion named like a tag, a
// device or another instance is an error, as two tags of one name are.
func wiredTags(set *wirebind.Set, m *Manifest) ([]TagConfig, error) {
	taken := map[string]string{}
	for _, t := range m.Tags {
		taken[ir.NameKey(t.Name)] = "the tag " + t.Name
	}
	for _, in := range set.Instances {
		taken[ir.NameKey(in.Name)] = "the instance " + in.Name
	}
	var out []TagConfig
	for _, in := range set.Instances {
		def := runtime.InstanceDef{Members: map[string]runtime.MemberDef{}}
		for _, mem := range in.Members {
			def.Members[mem.Path] = runtime.MemberDef{Kind: memberKind(mem.Kind), Readback: mem.Readback, Event: mem.Event, Expr: mem.Expr}
		}
		out = append(out, TagConfig{Name: in.Name, Role: "state", Type: in.Type, Desc: in.Desc, instance: &def})
		for _, c := range in.Conns {
			if c.Shared {
				// The driver's own companion, one for every instance on it:
				// declared here unless the project (an import's tag file)
				// or another instance already did.
				name := m.sharedOPCUACompanion(c.Driver)
				if _, dup := taken[ir.NameKey(name)]; !dup {
					taken[ir.NameKey(name)] = "the shared OPC UA session's companion"
					out = append(out, TagConfig{Name: name, Role: "input", Type: "BOOL", Desc: c.Driver + ": the OPC UA server answers"})
				}
				continue
			}
			companions := []TagConfig{{Name: c.Online(), Role: "input", Type: "BOOL", Desc: in.Name + ": the device answers"}}
			if c.DriverType == "modbus" {
				companions = append(companions, TagConfig{Name: c.Outages(), Role: "input", Type: "DINT", Desc: in.Name + ": times it stopped answering since start"})
			}
			for _, t := range companions {
				if prev, dup := taken[ir.NameKey(t.Name)]; dup {
					return nil, fmt.Errorf("instance %s: its connection's companion %s has the name of %s: rename one", in.Name, t.Name, prev)
				}
				taken[ir.NameKey(t.Name)] = "a companion of instance " + in.Name
				out = append(out, t)
			}
		}
	}
	return out, nil
}

func memberKind(k wiring.Kind) runtime.MemberKind {
	switch k {
	case wiring.From:
		return runtime.MemberFrom
	case wiring.To:
		return runtime.MemberTo
	case wiring.Both:
		return runtime.MemberBoth
	case wiring.Expr:
		return runtime.MemberExpr
	}
	return runtime.MemberMemory
}

// withWired hands each Modbus instance connection to the Modbus driver its
// source names: a drivers: entry by name, or, by the type modbus, the
// project's one Modbus driver (added when it has none, as devices: rows add
// one). The configs are copied, not changed.
func withWired(configs []DriverConfig, set *wirebind.Set) ([]DriverConfig, error) {
	if set == nil {
		return configs, nil
	}
	out := append([]DriverConfig(nil), configs...)
	names := driverNames(out)
	byType := -1
	for _, in := range set.Instances {
		for _, c := range in.Conns {
			if c.DriverType != "modbus" {
				continue
			}
			at := -1
			for i := range out {
				if strings.EqualFold(names[i], c.Driver) && strings.EqualFold(out[i].Type, "modbus") {
					at = i
				}
			}
			if at < 0 && strings.EqualFold(c.Driver, "modbus") {
				if byType < 0 {
					for i := range out {
						if strings.EqualFold(out[i].Type, "modbus") {
							if byType >= 0 {
								return nil, fmt.Errorf("instance %s: source %s names driver: modbus, and the project has more than one modbus driver: name the drivers: entry instead", in.Name, c.Source)
							}
							byType = i
						}
					}
					if byType < 0 {
						out = append(out, DriverConfig{Type: "modbus"})
						names = driverNames(out)
						byType = len(out) - 1
					}
				}
				at = byType
				out[at].wiredByType = true
			}
			if at < 0 {
				return nil, fmt.Errorf("instance %s: source %s: driver %s is not a modbus driver of the project", in.Name, c.Source, c.Driver)
			}
			out[at].wired = set
		}
	}
	return withWiredOPCUA(out, set)
}

// wiredOwns says whether a connection's driver: names the driver d (named
// name in the project): by its name, or by its type when it is the one a
// source naming the type resolves to.
func wiredOwns(d DriverConfig, name string) func(string) bool {
	return func(drv string) bool {
		if strings.EqualFold(drv, name) {
			return true
		}
		if !strings.EqualFold(drv, d.Type) {
			return false
		}
		return d.wiredByType
	}
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
