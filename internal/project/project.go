// Package project loads a manifest-defined nautilus controller: a directory
// (or embedded archive) holding nautilus.yaml plus IEC program files — the
// no-Go authoring surface. The manifest is pure data and covers what a
// hand-written main.go wires up: tasks, tags by role, the server, and a
// configured field driver. Anything beyond it (custom buses, simulation
// physics, native-Go blocks) is the Go extension tier — the library API
// stays the canonical seam, and this package only transcribes onto it.
package project

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/eip"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
	"github.com/joyautomation/nautilus/sparkplug"
)

// ManifestName is the file that marks a directory as a manifest project.
const ManifestName = "nautilus.yaml"

// Manifest mirrors nautilus.yaml. Field-for-field it is runtime.Options +
// server.Options as data; the yaml decoder runs with KnownFields so a typo
// is an error, not silence.
type Manifest struct {
	Name   string       `yaml:"name"`
	Server ServerConfig `yaml:"server"`
	Tasks  []TaskConfig `yaml:"tasks"`
	// TagFiles names files holding additional tags — each a bare YAML
	// sequence of the same tag entries as Tags. This is how a GENERATED
	// tag set stays a separate reviewable artifact instead of a 500-line
	// smear through the file a person edits, and how one project ships to
	// several sites: the site's manifest picks which tag files it wants.
	// Resolved by ReadManifest, so every consumer (Load, the language
	// server, check) sees one composed tag list.
	TagFiles []string    `yaml:"tag-files"`
	Tags     []TagConfig `yaml:"tags"`
	// TagMeta layers HMI documentation onto tags declared elsewhere — keyed
	// by tag name, or by a dotted path (`P101.Speed`) for one field of a
	// UDT tag. Documentation only; it cannot change a tag's role or seed.
	TagMeta   map[string]MetaConfig `yaml:"tag-meta"`
	Driver    DriverConfig          `yaml:"driver"`
	Sparkplug *SparkplugConfig      `yaml:"sparkplug"`
	// Retain persists operator state (setpoints, online edits) across
	// restarts; Redundancy elects one scanning leader among replicas.
	// Both are wired by `nautilus run` — check/build/LSP only validate.
	Retain     *RetainConfig     `yaml:"retain"`
	Redundancy *RedundancyConfig `yaml:"redundancy"`
}

// RetainConfig says where retained state lives. In a cluster the ConfigMap
// is used; anywhere else the file. `retain: {}` takes both defaults.
type RetainConfig struct {
	// File is the JSON file path outside a cluster (default retain.json,
	// beside the controller's working directory).
	File string `yaml:"file"`
	// ConfigMap is the in-cluster store's name (default <name>-retain).
	ConfigMap string `yaml:"configmap"`
}

// RedundancyConfig turns on Lease-based leader election: replicas of this
// controller elect one scanning leader, the rest stand by. Outside a
// cluster the elector is standalone and always leader, so the same
// manifest runs on a bench and in the cluster. `redundancy: {}` takes the
// default lease name.
type RedundancyConfig struct {
	// Lease names the coordination.k8s.io Lease (default: the project name).
	Lease string `yaml:"lease"`
}

// SparkplugConfig republishes the tag store as a Sparkplug B edge node —
// the manifest form of sparkplug.Config + its options. The MQTT password
// is NEVER in the file: set NAUTILUS_MQTT_PASSWORD.
type SparkplugConfig struct {
	Broker          string   `yaml:"broker"`    // tcp://host:1883, ssl://host:8883
	GroupID         string   `yaml:"group-id"`  // Sparkplug group_id
	EdgeNode        string   `yaml:"edge-node"` // default: the project name
	ClientID        string   `yaml:"client-id"`
	Username        string   `yaml:"username"`
	PrimaryHost     string   `yaml:"primary-host"` // gate births on a SCADA host's STATE
	PublishInterval Duration `yaml:"publish-interval"`
	BdSeqFile       string   `yaml:"bdseq-file"` // persists bdSeq across restarts
	// Device publishes the field driver's input tags as a Sparkplug DEVICE
	// with this id (DBIRTH/DDEATH follow the driver's connection health).
	// Empty = everything publishes at node level.
	Device string `yaml:"device"`
	// RBE tuning: a default class, named classes, and glob assignments.
	DefaultClass  *RBEConfig           `yaml:"default-class"`
	Classes       map[string]RBEConfig `yaml:"classes"`
	MetricClasses map[string][]string  `yaml:"metric-classes"`
}

// RBEConfig is one publish class's report-by-exception tuning.
type RBEConfig struct {
	Deadband    float64  `yaml:"deadband"`
	MinInterval Duration `yaml:"min-interval"`
	MaxInterval Duration `yaml:"max-interval"`
	// EveryChange publishes every transition unconditionally (alarms,
	// counters) — deadband/min-interval ignored.
	EveryChange bool `yaml:"every-change"`
}

type ServerConfig struct {
	// Addr is the tag-API listen address (default "localhost:8080");
	// NAUTILUS_ADDR overrides at start. The write token is NEVER in the
	// manifest — set NAUTILUS_TOKEN in the environment.
	Addr        string   `yaml:"addr"`
	OnlineEdits bool     `yaml:"online-edits"`
	Interval    Duration `yaml:"interval"`
}

// TaskConfig is one program on its own scan. The FIRST task is the main
// task: it owns field I/O and is the default online-edit target.
type TaskConfig struct {
	Name    string   `yaml:"name"`
	Program string   `yaml:"program"` // .st, .fbd, or .ld file in the project
	Scan    Duration `yaml:"scan"`
	DtTag   string   `yaml:"dt-tag"`
}

// TagConfig declares one tag by role — the manifest form of runtime.TagDef.
type TagConfig struct {
	Name string `yaml:"name"`
	Role string `yaml:"role"` // input | output | setpoint | state
	// Type names a UDT the project's ST declares, for a tag whose value is
	// a struct rather than a scalar. It replaces both the prose desc: that
	// used to describe such a tag and the type-from-seed inference: a tag
	// with a type has a knowable shape even with no init.
	Type string `yaml:"type"`
	Init any    `yaml:"init"`
	Unit string `yaml:"unit"`
	Desc string `yaml:"desc"`
}

// MetaConfig is HMI documentation for a tag, kept separate from the tag's
// declaration so it can be attached to a GENERATED tag without redeclaring
// it. This is not an override mechanism in disguise: it reaches only unit
// and desc, so its worst failure is a stale sentence, where an override on
// role or init would silently change what the controller does.
//
// It exists because some generators cannot supply documentation at all —
// `nautilus eip import` is the case that forced it, since Logix keeps tag
// descriptions in the offline project file rather than anywhere the CIP tag
// browse can reach. A generator that HAS descriptions should emit them into
// its tag file instead.
type MetaConfig struct {
	Unit string `yaml:"unit"`
	Desc string `yaml:"desc"`
}

// DriverConfig selects and configures the field driver. "memory" (the
// default) is the loopback used for bring-up; "eip" polls an
// Allen-Bradley Logix controller. Custom buses are the Go tier.
type DriverConfig struct {
	Type string `yaml:"type"`

	// eip
	Host        string              `yaml:"host"`
	Slot        int                 `yaml:"slot"`
	Manifest    string              `yaml:"manifest"` // YAML eip.Manifest file
	ScanRate    Duration            `yaml:"scan-rate"`
	ScanClasses map[string]Duration `yaml:"scan-classes"`
	TagClasses  map[string][]string `yaml:"tag-classes"`
}

// Duration parses "100ms" / "2s" yaml scalars.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	v, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("bad duration %q (want e.g. 100ms, 2s)", node.Value)
	}
	*d = Duration(v)
	return nil
}

// Project is a loaded manifest: everything runtime.New and server.New need.
type Project struct {
	Name      string
	Addr      string
	Runtime   runtime.Options
	Server    server.Options
	sparkplug *SparkplugConfig
	inputTags []string // role-input tag names, for the Sparkplug device

	// Retain/Redundancy carry the manifest's sections for `nautilus run`
	// to wire; Load itself constructs nothing — check, build, and the LSP
	// load projects too, and must not touch a cluster to do it.
	Retain     *RetainConfig
	Redundancy *RedundancyConfig
}

// RetainNames resolves the retain section's defaults against the project
// name: (configMapName, filePath), ready for retain.New.
func (p *Project) RetainNames() (configMap, file string) {
	configMap, file = p.Name+"-retain", "retain.json"
	if p.Retain.ConfigMap != "" {
		configMap = p.Retain.ConfigMap
	}
	if p.Retain.File != "" {
		file = p.Retain.File
	}
	return configMap, file
}

// LeaseName resolves the redundancy section's default lease name.
func (p *Project) LeaseName() string {
	if p.Redundancy.Lease != "" {
		return p.Redundancy.Lease
	}
	return p.Name
}

// Sparkplug builds the manifest's edge node over a compiled runtime, or
// (nil, nil) when the manifest has no sparkplug section.
func (p *Project) Sparkplug(rt *runtime.Runtime) (*sparkplug.Node, error) {
	c := p.sparkplug
	if c == nil {
		return nil, nil
	}
	if c.Broker == "" || c.GroupID == "" {
		return nil, fmt.Errorf("sparkplug: broker and group-id are required")
	}
	edge := c.EdgeNode
	if edge == "" {
		edge = p.Name
	}
	cfg := sparkplug.Config{
		BrokerURL:       c.Broker,
		GroupID:         c.GroupID,
		EdgeNode:        edge,
		ClientID:        c.ClientID,
		Username:        c.Username,
		Password:        os.Getenv("NAUTILUS_MQTT_PASSWORD"),
		PrimaryHostID:   c.PrimaryHost,
		PublishInterval: time.Duration(c.PublishInterval),
		BdSeqFile:       c.BdSeqFile,
	}
	rbe := func(r RBEConfig) sparkplug.RBE {
		return sparkplug.RBE{
			Deadband:    r.Deadband,
			MinInterval: time.Duration(r.MinInterval),
			MaxInterval: time.Duration(r.MaxInterval),
			Disable:     r.EveryChange,
		}
	}
	var opts []sparkplug.Option
	if c.DefaultClass != nil {
		opts = append(opts, sparkplug.WithDefaultRBE(rbe(*c.DefaultClass)))
	}
	for name, r := range c.Classes {
		opts = append(opts, sparkplug.WithPublishClass(name, rbe(r)))
	}
	for class, patterns := range c.MetricClasses {
		opts = append(opts, sparkplug.WithMetricClass(class, patterns...))
	}
	if c.Device != "" {
		// The field driver's tags as a Sparkplug DEVICE: its DBIRTH/DDEATH
		// track the driver's own connection health when it reports one.
		tags := p.inputTags
		if n, ok := p.Runtime.Driver.(interface{ InputNames() []string }); ok {
			tags = n.InputNames()
		}
		var health func() bool
		if h, ok := p.Runtime.Driver.(interface{ Health() eip.Health }); ok {
			health = func() bool { return h.Health().Connected }
		}
		opts = append(opts, sparkplug.WithDevice(sparkplug.Device{
			ID:     c.Device,
			Tags:   tags,
			Health: health,
		}))
	}
	return sparkplug.New(rt, cfg, opts...)
}

var programRe = regexp.MustCompile(`(?mi)^\s*PROGRAM\b`)

// ReadManifest decodes a manifest and stops there — no programs compiled, no
// driver constructed, nothing opened beyond the manifest and its tag files.
// Tooling that only needs the declarations uses this instead of Load: the
// language server reads a project's tags this way to answer completion and
// hover, and must stay cheap enough to do it on a keystroke.
//
// name selects the manifest; "" means nautilus.yaml. A project may hold
// several — one per site — sharing the same programs and differing in which
// tag files they compose.
func ReadManifest(fsys fs.FS, name string) (*Manifest, error) {
	if name == "" {
		name = ManifestName
	}
	name, err := projectPath(name)
	if err != nil {
		return nil, fmt.Errorf("manifest %w", err)
	}
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("no %s: %w", name, err)
	}
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if err := composeTags(fsys, &m, name); err != nil {
		return nil, err
	}
	return &m, nil
}

// projectPath keeps a manifest-referenced file inside the project. The
// deployable artifact is the directory (or the archive built from it), so a
// path that escapes it would load in development and vanish once built.
func projectPath(p string) (string, error) {
	c := path.Clean(p)
	if !fs.ValidPath(c) {
		return "", fmt.Errorf("path %q is outside the project", p)
	}
	return c, nil
}

// composeTags folds tag-files into m.Tags: each file in listed order, then
// the manifest's own tags last.
//
// A name declared twice is an ERROR naming both sources, never last-wins.
// Last-wins reads fine on the day it is written and rots silently: regenerate
// a tag file, and an override that no longer matches anything keeps applying,
// or stops applying, with no diff to show for it. The remedies stay legible —
// fix the generator, or narrow its scope so the tag is never generated.
func composeTags(fsys fs.FS, m *Manifest, manifestName string) error {
	from := make(map[string]string, len(m.Tags))
	out := make([]TagConfig, 0, len(m.Tags))
	add := func(tags []TagConfig, src string) error {
		for _, t := range tags {
			// An unnamed tag is tagDefs' error to report, with its own
			// wording; skipping it here keeps one message per problem.
			if t.Name == "" {
				continue
			}
			if prev, dup := from[t.Name]; dup {
				return fmt.Errorf("tag %q is declared in both %s and %s — "+
					"a tag may be declared once (fix the generator, or narrow "+
					"its scope so the tag is not generated)", t.Name, prev, src)
			}
			from[t.Name] = src
		}
		out = append(out, tags...)
		return nil
	}
	for _, f := range m.TagFiles {
		p, err := projectPath(f)
		if err != nil {
			return fmt.Errorf("tag-files: %w", err)
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("tag-files: %w", err)
		}
		var tags []TagConfig
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true)
		if err := dec.Decode(&tags); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("%s: %w (a tag file is a bare YAML list of tags, "+
				"with no top-level keys)", p, err)
		}
		if err := add(tags, p); err != nil {
			return err
		}
	}
	if err := add(m.Tags, manifestName); err != nil {
		return err
	}
	m.Tags = out
	return nil
}

// Load reads a manifest and the project's IEC sources from fsys (a directory
// via os.DirFS, or a built binary's embedded archive). name selects the
// manifest; "" means nautilus.yaml.
func Load(fsys fs.FS, name string) (*Project, error) {
	mp, err := ReadManifest(fsys, name)
	if err != nil {
		return nil, err
	}
	m := *mp
	if len(m.Tasks) == 0 {
		return nil, fmt.Errorf("%s: at least one task (a program file) is required", ManifestName)
	}

	// Libraries: every .st in the project root without a PROGRAM — the
	// same rule the editor, LSP, and pull use, so tooling agrees.
	libs, err := libraries(fsys)
	if err != nil {
		return nil, err
	}

	readProgram := func(t TaskConfig) (string, error) {
		if t.Program == "" {
			return "", fmt.Errorf("%s: every task needs a program file", ManifestName)
		}
		src, err := fs.ReadFile(fsys, path.Clean(t.Program))
		if err != nil {
			return "", fmt.Errorf("task program %q: %w", t.Program, err)
		}
		if !programRe.Match(src) {
			return "", fmt.Errorf("%s has no PROGRAM declaration", t.Program)
		}
		return string(src), nil
	}

	opts := runtime.Options{Libraries: libs}
	main := m.Tasks[0]
	if opts.Program, err = readProgram(main); err != nil {
		return nil, err
	}
	opts.Scan = time.Duration(main.Scan)
	opts.DtTag = main.DtTag
	for _, t := range m.Tasks[1:] {
		src, err := readProgram(t)
		if err != nil {
			return nil, err
		}
		name := t.Name
		if name == "" {
			name = strings.TrimSuffix(path.Base(t.Program), path.Ext(t.Program))
		}
		opts.Tasks = append(opts.Tasks, runtime.Task{
			Name:      name,
			Program:   src,
			Libraries: libs,
			Scan:      time.Duration(t.Scan),
			DtTag:     t.DtTag,
		})
	}

	if opts.Tags, err = tagDefs(m.Tags); err != nil {
		return nil, err
	}
	opts.Meta = applyTagMeta(opts.Tags, m.TagMeta)
	if opts.Driver, err = buildDriver(fsys, m.Driver); err != nil {
		return nil, err
	}

	addr := m.Server.Addr
	if addr == "" {
		addr = "localhost:8080"
	}
	projName := m.Name
	if projName == "" {
		projName = "nautilus"
	}
	var inputs []string
	for _, t := range m.Tags {
		if strings.EqualFold(t.Role, "input") {
			inputs = append(inputs, t.Name)
		}
	}
	return &Project{
		Name:    projName,
		Addr:    addr,
		Runtime: opts,
		Server: server.Options{
			Interval:    time.Duration(m.Server.Interval),
			OnlineEdits: m.Server.OnlineEdits,
		},
		sparkplug:  m.Sparkplug,
		inputTags:  inputs,
		Retain:     m.Retain,
		Redundancy: m.Redundancy,
	}, nil
}

func libraries(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(path.Ext(e.Name()), ".st") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var libs []string
	for _, n := range names {
		src, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, err
		}
		if !programRe.Match(src) {
			libs = append(libs, string(src))
		}
	}
	return libs, nil
}

func tagDefs(tags []TagConfig) ([]runtime.TagDef, error) {
	var defs []runtime.TagDef
	for _, t := range tags {
		if t.Name == "" {
			return nil, fmt.Errorf("%s: a tag needs a name", ManifestName)
		}
		var meta []runtime.TagOpt
		if t.Unit != "" {
			meta = append(meta, runtime.Unit(t.Unit))
		}
		if t.Desc != "" {
			meta = append(meta, runtime.Desc(t.Desc))
		}
		var def runtime.TagDef
		switch strings.ToLower(t.Role) {
		case "input":
			if t.Init != nil {
				meta = append(meta, runtime.Init(normalize(t.Init)))
			}
			def = runtime.Input(t.Name, meta...)
		case "output":
			if t.Init != nil {
				meta = append(meta, runtime.Init(normalize(t.Init)))
			}
			def = runtime.Output(t.Name, meta...)
		case "setpoint":
			// A typed tag needs no init: zero-of-type is a complete, correctly
			// shaped value, which is exactly what a seed is for. An untyped
			// one still does — without either, it has no value on scan one and
			// no knowable shape.
			if t.Init == nil && t.Type == "" {
				return nil, fmt.Errorf("tag %s: a setpoint needs init or type (its value from scan one)", t.Name)
			}
			def = runtime.Setpoint(t.Name, normalize(t.Init), meta...)
		case "state":
			if t.Init == nil && t.Type == "" {
				return nil, fmt.Errorf("tag %s: state needs init or type", t.Name)
			}
			def = runtime.State(t.Name, normalize(t.Init), meta...)
		default:
			return nil, fmt.Errorf("tag %s: role must be input, output, setpoint, or state (got %q)", t.Name, t.Role)
		}
		def.Type = t.Type
		defs = append(defs, def)
	}
	return defs, nil
}

// applyTagMeta layers a tag-meta: block onto the tags. A key matching a tag
// by name is merged into that tag's own documentation and WINS over it: the
// block exists precisely to say what a generator could not, so a hand-written
// unit must beat a generated blank — or an out-of-date generated string.
//
// Keys matching no tag (a dotted field path, or a typo) pass through to
// Options.Meta as-is. The meta key space is plain strings, so per-field
// documentation needs no new type; `nautilus check` reports keys that name
// nothing, which is where a typo surfaces.
func applyTagMeta(defs []runtime.TagDef, tm map[string]MetaConfig) map[string]runtime.TagMeta {
	if len(tm) == 0 {
		return nil
	}
	byName := make(map[string]int, len(defs))
	for i, d := range defs {
		byName[d.Name] = i
	}
	out := make(map[string]runtime.TagMeta, len(tm))
	for key, mc := range tm {
		i, ok := byName[key]
		if !ok {
			out[key] = runtime.TagMeta{Unit: mc.Unit, Desc: mc.Desc}
			continue
		}
		if mc.Unit != "" {
			defs[i].Meta.Unit = mc.Unit
		}
		if mc.Desc != "" {
			defs[i].Meta.Desc = mc.Desc
		}
	}
	return out
}

// normalize maps yaml's integer literals onto the float64 the tag store
// expects for numerics (yaml decodes 65 as int, 65.0 as float64; ST REAL
// tags want the latter). BOOL and string pass through.
func normalize(v any) any {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return v
}

func buildDriver(fsys fs.FS, d DriverConfig) (nio.Driver, error) {
	switch strings.ToLower(d.Type) {
	case "", "memory":
		// Loopback for bring-up: inputs read back whatever was written,
		// so logic and HMI run before any field hardware exists.
		return nio.NewMemory(), nil
	case "eip":
		if d.Host == "" {
			return nil, fmt.Errorf("driver eip: host is required")
		}
		if d.Manifest == "" {
			return nil, fmt.Errorf("driver eip: manifest (the imported tag manifest .yaml) is required")
		}
		raw, err := fs.ReadFile(fsys, path.Clean(d.Manifest))
		if err != nil {
			return nil, fmt.Errorf("driver eip: %w", err)
		}
		var em eip.Manifest
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true)
		if err := dec.Decode(&em); err != nil {
			return nil, fmt.Errorf("driver eip: %s: %w", d.Manifest, err)
		}
		opts := []eip.Option{eip.WithSlot(d.Slot)}
		if d.ScanRate != 0 {
			opts = append(opts, eip.WithScanRate(time.Duration(d.ScanRate)))
		}
		for name, rate := range d.ScanClasses {
			opts = append(opts, eip.WithScanClass(name, time.Duration(rate)))
		}
		for class, patterns := range d.TagClasses {
			opts = append(opts, eip.WithTagClass(class, patterns...))
		}
		return eip.New(d.Host, em, opts...)
	default:
		return nil, fmt.Errorf("driver type %q: manifest projects support memory and eip — custom buses are the Go tier (io.Driver)", d.Type)
	}
}
