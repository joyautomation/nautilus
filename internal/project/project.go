// Package project loads a manifest-defined nautilus controller: a directory
// (or embedded archive) holding nautilus.yaml plus IEC program files — the
// no-Go authoring surface. The manifest is pure data and covers what a
// hand-written main.go wires up: tasks, tags by role, the server, and a
// configured field driver. Anything beyond it (custom buses, simulation
// physics, native-Go blocks) is the Go extension tier — the library API
// stays the canonical seam, and this package only transcribes onto it.
package project

import (
	"fmt"
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
	Name      string           `yaml:"name"`
	Server    ServerConfig     `yaml:"server"`
	Tasks     []TaskConfig     `yaml:"tasks"`
	Tags      []TagConfig      `yaml:"tags"`
	Driver    DriverConfig     `yaml:"driver"`
	Sparkplug *SparkplugConfig `yaml:"sparkplug"`
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
	Init any    `yaml:"init"`
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

// ReadManifest decodes nautilus.yaml and stops there — no programs
// compiled, no driver constructed, nothing opened. Tooling that only needs
// the declarations uses this instead of Load: the language server reads a
// project's tags this way to answer completion and hover, and must stay
// cheap enough to do it on a keystroke.
func ReadManifest(fsys fs.FS) (*Manifest, error) {
	raw, err := fs.ReadFile(fsys, ManifestName)
	if err != nil {
		return nil, fmt.Errorf("no %s: %w", ManifestName, err)
	}
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestName, err)
	}
	return &m, nil
}

// Load reads nautilus.yaml and the project's IEC sources from fsys (a
// directory via os.DirFS, or a built binary's embedded archive).
func Load(fsys fs.FS) (*Project, error) {
	mp, err := ReadManifest(fsys)
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
	if opts.Driver, err = buildDriver(fsys, m.Driver); err != nil {
		return nil, err
	}

	addr := m.Server.Addr
	if addr == "" {
		addr = "localhost:8080"
	}
	name := m.Name
	if name == "" {
		name = "nautilus"
	}
	var inputs []string
	for _, t := range m.Tags {
		if strings.EqualFold(t.Role, "input") {
			inputs = append(inputs, t.Name)
		}
	}
	return &Project{
		Name:    name,
		Addr:    addr,
		Runtime: opts,
		Server: server.Options{
			Interval:    time.Duration(m.Server.Interval),
			OnlineEdits: m.Server.OnlineEdits,
		},
		sparkplug: m.Sparkplug,
		inputTags: inputs,
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
		switch strings.ToLower(t.Role) {
		case "input":
			if t.Init != nil {
				meta = append(meta, runtime.Init(normalize(t.Init)))
			}
			defs = append(defs, runtime.Input(t.Name, meta...))
		case "output":
			if t.Init != nil {
				meta = append(meta, runtime.Init(normalize(t.Init)))
			}
			defs = append(defs, runtime.Output(t.Name, meta...))
		case "setpoint":
			if t.Init == nil {
				return nil, fmt.Errorf("tag %s: a setpoint needs init (its value from scan one)", t.Name)
			}
			defs = append(defs, runtime.Setpoint(t.Name, normalize(t.Init), meta...))
		case "state":
			if t.Init == nil {
				return nil, fmt.Errorf("tag %s: state needs init", t.Name)
			}
			defs = append(defs, runtime.State(t.Name, normalize(t.Init), meta...))
		default:
			return nil, fmt.Errorf("tag %s: role must be input, output, setpoint, or state (got %q)", t.Name, t.Role)
		}
	}
	return defs, nil
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
