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
	"github.com/joyautomation/nautilus/internal/dialect"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/eip"
	"github.com/joyautomation/nautilus/internal/stproject"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/prom"
	"github.com/joyautomation/nautilus/redfish"
	"github.com/joyautomation/nautilus/replay"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/sparkplug"
	sphost "github.com/joyautomation/nautilus/sparkplug/host"
)

// ManifestName is the file that marks a directory as a manifest project.
const ManifestName = "nautilus.yaml"

// Manifest mirrors nautilus.yaml. Field-for-field it is runtime.Options +
// server.Options as data; the yaml decoder runs with KnownFields so a typo
// is an error, not silence.
type Manifest struct {
	Name string `yaml:"name"`
	// Dialect opts the project into a vendor-semantics block library
	// (internal/dialect): "logix" adds TONR and friends, written in
	// nautilus so the runtime, the Logix writer and the L5X importer share
	// one definition. Empty or "nautilus" adds nothing; "siemens" and
	// "codesys" are reserved.
	Dialect string       `yaml:"dialect"`
	Server  ServerConfig `yaml:"server"`
	Tasks   []TaskConfig `yaml:"tasks"`
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
	TagMeta map[string]MetaConfig `yaml:"tag-meta"`
	Driver  DriverConfig          `yaml:"driver"`
	// Drivers configures SEVERAL field drivers on one scan — a Modbus bus
	// plus an MQTT feed on the same controller. Each entry is the same
	// shape as driver:, plus an optional name; setting both driver: and
	// drivers: is an error. One entry behaves exactly like driver:.
	Drivers   []DriverConfig   `yaml:"drivers"`
	Sparkplug *SparkplugConfig `yaml:"sparkplug"`
	// Retain persists operator state (setpoints, online edits) across
	// restarts; Redundancy elects one scanning leader among replicas.
	// Both are wired by `naut run` — check/build/LSP only validate.
	Retain     *RetainConfig     `yaml:"retain"`
	Redundancy *RedundancyConfig `yaml:"redundancy"`
	// Alarms turns BOOL tags into ISA-18.2 alarm state — the active list,
	// acknowledge, shelve, and the journal behind /api/alarms. Absent (the
	// default): no engine, and behaviour identical to before it existed.
	// AlarmFiles mirrors TagFiles exactly, for the same reason: a
	// GENERATED alarm set belongs in its own reviewable artifact, not
	// smeared through the file a person edits. Both are composed by
	// ReadManifest, so check, run and the language server see one set.
	Alarms     *AlarmsConfig `yaml:"alarms"`
	AlarmFiles []string      `yaml:"alarm-files"`
	// Target names a controller this project is DEPLOYED to instead of
	// being run by the nautilus runtime: today, an Allen-Bradley Logix
	// controller (docs/design/logix-authoring.md). With a target set,
	// `naut check` also runs that target's rules, so a construct the
	// target cannot take is a diagnostic on the keystroke that wrote it.
	Target *TargetConfig `yaml:"target"`
}

// TargetConfig is the deploy target. One kind at a time; logix is the
// only one so far.
type TargetConfig struct {
	Logix *LogixTarget `yaml:"logix"`
}

// LogixTarget is an Allen-Bradley Logix controller as a deploy target:
// what the L5X writer puts in the project envelope, where logixd reaches
// the controller, and where live values come from.
type LogixTarget struct {
	// Controller is the Logix controller (project) name. Default: the
	// task program's POU name.
	Controller string `yaml:"controller"`
	// Processor is the catalog number (1756-L85E); Revision the firmware
	// "major.minor" (38.11). Defaults match ECHO1.
	Processor string `yaml:"processor"`
	Revision  string `yaml:"revision"`
	// CommPath is the FactoryTalk Linx path logixd uses to reach the
	// controller (AB_ETH-1\10.0.0.5\Backplane\0). Required to deploy.
	CommPath string `yaml:"comm-path"`
	// Host and Slot are the controller's EtherNet/IP address for live
	// values (`naut logix serve`); Port 0 is 44818.
	Host string `yaml:"host"`
	Slot int    `yaml:"slot"`
	Port int    `yaml:"port"`
	// Agent is the logixd URL. NAUTILUS_LOGIXD_URL overrides it, and the
	// token is NEVER in the manifest — set NAUTILUS_LOGIXD_TOKEN.
	Agent string `yaml:"agent"`
	// Program, Routine and Task name the Logix program, its ladder
	// routine and the task it is scheduled in. Defaults: the POU name,
	// MainRoutine, MainTask.
	Program string `yaml:"program"`
	Routine string `yaml:"routine"`
	Task    string `yaml:"task"`
	// Side is the side code nautilus adds beside the program, in a Logix
	// program of its own: testing, verification and metrics logic that
	// never touches the user's routine.
	Side *LogixSide `yaml:"side"`
}

// LogixSide selects the side code.
type LogixSide struct {
	// Heartbeat names a controller DINT incremented once per task scan,
	// which `naut test --target logix` waits on for an exact `scans: n`.
	Heartbeat string `yaml:"heartbeat"`
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
	// StoreForward buffers up to this many data messages while the broker
	// (or the primary host) is unreachable and replays them, marked
	// historical, on reconnect. Zero disables (the default).
	StoreForward int `yaml:"store-forward"`
	// FlattenUDTs publishes each UDT member as a plain metric
	// ("P101/Running") instead of the tag as one Sparkplug Template, for
	// hosts that do not read Templates. Default false: Templates.
	FlattenUDTs bool `yaml:"flatten-udts"`
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
	// Historian is the base URL of a `naut historian` daemon; when set,
	// the API proxies GET /api/history* there so the HMI keeps one origin.
	// NAUTILUS_HISTORIAN_URL overrides at start.
	Historian string `yaml:"historian"`
	// HMI is a built HMI's directory, relative to the project (e.g. the
	// SvelteKit `adapter-static` output, "./hmi/build") — same rule as
	// every other manifest-referenced path (tag-files, driver.manifest):
	// it must resolve inside the project (see projectPath), so what
	// `naut build` ships is what a reviewer can see in the checkout.
	// When set, the controller serves that directory at "/" (SPA fallback
	// to its index.html for a client-side route), and the built-in
	// dashboard moves to "/_nautilus/" — see server.Options.HMI. Empty (the
	// default): unchanged, the dashboard keeps "/".
	//
	// The HMI must call the tag API same-origin (a relative "/api/..."
	// base URL) — server.hmi and the API are one process on one address,
	// so there is no cross-origin URL to configure.
	HMI string `yaml:"hmi"`
}

// TaskConfig is one program on its own scan. The FIRST task is the main
// task: it owns field I/O and is the default online-edit target.
type TaskConfig struct {
	Name    string   `yaml:"name"`
	Program string   `yaml:"program"` // .st, .fbd, or .ld file in the project
	Scan    Duration `yaml:"scan"`
	DtTag   string   `yaml:"dt-tag"`
	// LateThreshold is how late a scan may start before the diagnostics
	// count it as late (runtime.Lateness). Default: a tenth of scan.
	LateThreshold Duration `yaml:"late-threshold"`
	// CPU pins this task's thread to one CPU (Linux); Priority > 0 asks
	// for SCHED_FIFO at that priority (needs CAP_SYS_NICE). A pointer so
	// that cpu: 0 is a request and an absent key is not.
	CPU      *int `yaml:"cpu"`
	Priority int  `yaml:"priority"`
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
	// Alias binds the tag to a controller's I/O point or another tag on a
	// vendor target — on Logix, the alias tag's AliasFor
	// ("Local:1:I.Data.3"). The nautilus runtime ignores it: the tag is a
	// tag. It is the one place a hardware address lives in a project.
	Alias string `yaml:"alias"`
}

// MetaConfig is HMI documentation for a tag, kept separate from the tag's
// declaration so it can be attached to a GENERATED tag without redeclaring
// it. This is not an override mechanism in disguise: it reaches only unit
// and desc, so its worst failure is a stale sentence, where an override on
// role or init would silently change what the controller does.
//
// It exists because some generators cannot supply documentation at all —
// `naut eip import` is the case that forced it, since Logix keeps tag
// descriptions in the offline project file rather than anywhere the CIP tag
// browse can reach. A generator that HAS descriptions should emit them into
// its tag file instead.
type MetaConfig struct {
	Unit string `yaml:"unit"`
	Desc string `yaml:"desc"`
}

// DriverConfig selects and configures the field driver. "memory" (the
// default) is the loopback used for bring-up; "eip" polls an
// Allen-Bradley Logix controller; "sparkplug-host" consumes a Sparkplug B
// group as a host application; "modbus" polls a set of Modbus TCP sources.
// Custom buses are the Go tier.
//
// One flat struct serves every driver type, so `manifest` is shared: for
// eip it is the imported Logix tag manifest, for sparkplug-host the
// imported Sparkplug manifest, for modbus the imported modbus manifest.
// modbus deliberately adds NO keys of its own — it reuses manifest,
// scan-rate, scan-classes and tag-classes wholesale. buildDriver validates
// per type, so a key belonging to another driver is inert rather than an
// error.
type DriverConfig struct {
	Type string `yaml:"type"`
	// Name tells drivers in a drivers: list apart — in status rows, error
	// messages, and logs. Defaults to the driver's type, deduped as
	// "eip-2" when a type repeats. Inert on a lone driver: section.
	Name string `yaml:"name"`

	// eip (manifest/scan-rate/scan-classes/tag-classes shared with modbus)
	Host        string              `yaml:"host"`
	Slot        int                 `yaml:"slot"`
	Manifest    string              `yaml:"manifest"` // YAML eip / host / modbus manifest file
	ScanRate    Duration            `yaml:"scan-rate"`
	ScanClasses map[string]Duration `yaml:"scan-classes"`
	TagClasses  map[string][]string `yaml:"tag-classes"`

	// sparkplug-host. The MQTT password is NEVER in the file — set
	// NAUTILUS_MQTT_PASSWORD, the same rule the sparkplug: section keeps.
	Broker   string   `yaml:"broker"`    // tcp://host:1883, ssl://host:8883
	GroupID  string   `yaml:"group-id"`  // the Sparkplug group_id to consume
	GroupIDs []string `yaml:"group-ids"` // several groups; overrides group-id
	HostID   string   `yaml:"host-id"`   // STATE topic spBv1.0/STATE/<host-id>
	ClientID string   `yaml:"client-id"`
	Username string   `yaml:"username"`
	// Primary publishes the STATE certificate (default true). false makes
	// this a passive consumer: it subscribes and reads, but never announces
	// itself and never sends NCMD/DCMD.
	Primary   *bool  `yaml:"primary"`
	StateForm string `yaml:"state-form"` // "3.0" (default) | "2.x" | "both"
	// ReorderTimeout is how long an unfilled sequence gap waits before the
	// host gives up and asks the node to rebirth (default 5s).
	ReorderTimeout Duration `yaml:"reorder-timeout"`
	// StaleAfter marks a silent node stale (0 = off).
	StaleAfter Duration `yaml:"stale-after"`
	Keepalive  Duration `yaml:"keepalive"` // MQTT keepalive (default 30s)
	// OnUnknown is the policy for metrics on the wire that the manifest does
	// not bind: "log" (default), "ignore", or "strict".
	OnUnknown string `yaml:"on-unknown"`
	// RebirthOnStart asks every manifest node to rebirth on connect
	// (default true) — births are not retained, so a host starting
	// mid-stream sees nothing until it asks.
	RebirthOnStart *bool `yaml:"rebirth-on-start"`
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

// cpuList is the runtime's affinity set for a manifest's single cpu: key.
func cpuList(cpu *int) []int {
	if cpu == nil {
		return nil
	}
	return []int{*cpu}
}

// Project is a loaded manifest: everything runtime.New and server.New need.
type Project struct {
	Name      string
	Addr      string
	Runtime   runtime.Options
	Server    server.Options
	sparkplug *SparkplugConfig
	inputTags []string // role-input tag names, for the Sparkplug device

	// HMIDir is server.hmi's path, cleaned and relative to the project
	// (e.g. "hmi/build") — "" when unset. `naut build` uses it to warn
	// on a large embed; Server.HMI (above) is the fs.FS actually served.
	HMIDir string

	// HMIMissing is true when server.hmi names a directory (HMIDir) that
	// does not exist on fsys — the normal state of a fresh clone, since
	// an HMI's own build output is gitignored. Load leaves Server.HMI nil
	// in that case, so the built-in dashboard keeps "/" instead of the
	// server 404ing every request once the HMI it can't find claims the
	// route; `naut run` and `naut build` each check this to print their
	// own one-line "not built yet" warning naming HMIDir.
	HMIMissing bool

	// Retain/Redundancy carry the manifest's sections for `naut run`
	// to wire; Load itself constructs nothing — check, build, and the LSP
	// load projects too, and must not touch a cluster to do it.
	Retain     *RetainConfig
	Redundancy *RedundancyConfig

	// Alarms is the composed `alarms:` section (alarm-files folded in),
	// or nil when the manifest declares none. Load composes and validates
	// it; building the engine over a compiled runtime is a separate call
	// (see alarms.go) for the same reason Sparkplug is: Load must stay
	// side-effect-free so check, build and the LSP can use it.
	Alarms *AlarmsConfig
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
	if c.StoreForward > 0 {
		opts = append(opts, sparkplug.WithStoreForward(c.StoreForward))
	}
	if c.FlattenUDTs {
		opts = append(opts, sparkplug.WithFlattenUDTs())
	}
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
		opts = append(opts, sparkplug.WithDevice(sparkplug.Device{
			ID:     c.Device,
			Tags:   tags,
			Health: driverHealth(p.Runtime.Driver),
		}))
	}
	return sparkplug.New(rt, cfg, opts...)
}

// hasProgramDecl reports whether src contains a PROGRAM declaration,
// deciding by lexical token rather than raw text: a comment or string
// literal containing the word "program" (e.g. a block comment that reads
// "program; the site program owns...") must not count, or a library file
// gets misidentified as a program and silently dropped from the composed
// library set — surfacing later as spurious "unknown type" errors for
// whatever it declared. lang/st's lexer already strips (* ... *) and //
// comments and tokenizes string literals separately from keywords, so a
// single PROGRAM token scan is both cheap and correct without a full parse.
func hasProgramDecl(src []byte) bool {
	return stproject.DeclaresProgram(string(src))
}

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
	if !dialect.Known(m.Dialect) {
		return nil, fmt.Errorf("%s: dialect %q is not one of %s", name, m.Dialect, strings.Join(dialect.Names, ", "))
	}
	if err := composeTags(fsys, &m, name); err != nil {
		return nil, err
	}
	if err := composeAlarms(fsys, &m); err != nil {
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

// HMIBuildHint names where to run the HMI's own build for a "not built
// yet" warning (see Project.HMIMissing): server.hmi points at a build's
// OUTPUT (e.g. "hmi/build"), one level under the HMI's own project
// ("hmi/"), which is where `npm run build` actually runs. Falls back to
// "its project" when hmiDir has no parent to name (server.hmi at the
// project root, an unusual layout).
func HMIBuildHint(hmiDir string) string {
	if parent := path.Dir(hmiDir); parent != "." && parent != "" && parent != "/" {
		return parent + "/"
	}
	return "its project"
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
	// Keyed by ir.NameKey: tag names are IEC identifiers, so Level and
	// LEVEL are one tag declared twice (#197).
	type decl struct{ name, src string }
	from := make(map[string]decl, len(m.Tags))
	out := make([]TagConfig, 0, len(m.Tags))
	add := func(tags []TagConfig, src string) error {
		for _, t := range tags {
			// An unnamed tag is tagDefs' error to report, with its own
			// wording; skipping it here keeps one message per problem.
			if t.Name == "" {
				continue
			}
			if prev, dup := from[ir.NameKey(t.Name)]; dup {
				if prev.name != t.Name {
					return fmt.Errorf("tag %q (%s) and tag %q (%s) are one tag — "+
						"tag names are case-insensitive, like every IEC identifier; "+
						"rename one, or declare it once", prev.name, prev.src, t.Name, src)
				}
				return fmt.Errorf("tag %q is declared in both %s and %s — "+
					"a tag may be declared once (fix the generator, or narrow "+
					"its scope so the tag is not generated)", t.Name, prev.src, src)
			}
			from[ir.NameKey(t.Name)] = decl{t.Name, src}
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

	// Libraries: every PROGRAM-less .st/.ld/.fbd in the project root and
	// under lib/ — the same rule the editor, LSP, and pull use, so tooling
	// agrees.
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
		if !hasProgramDecl(src) {
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
	opts.LateThreshold = time.Duration(main.LateThreshold)
	opts.CPUs, opts.Priority = cpuList(main.CPU), main.Priority
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
			// A task without its own threshold inherits the main task's
			// setting in runtime.New (Options.LateThreshold).
			LateThreshold: time.Duration(t.LateThreshold),
			CPUs:          cpuList(t.CPU),
			Priority:      t.Priority,
		})
	}

	if opts.Tags, err = tagDefs(m.Tags); err != nil {
		return nil, err
	}
	opts.Tags = append(opts.Tags, gvlTags(libs, opts.Tags)...)
	opts.Meta = applyTagMeta(opts.Tags, m.TagMeta)
	if opts.Driver, err = buildDrivers(fsys, m); err != nil {
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
	var hmiFS fs.FS
	var hmiDir string
	var hmiMissing bool
	if m.Server.HMI != "" {
		hmiPath, err := projectPath(m.Server.HMI)
		if err != nil {
			return nil, fmt.Errorf("server.hmi: %w", err)
		}
		hmiDir = hmiPath
		// Check existence up front (rather than deferring to serve time,
		// as an earlier version of this did) so the caller can decide
		// what "not built yet" means for it: `naut check`/the language
		// server say nothing (HMIMissing is just metadata to them);
		// `naut run` and `naut build` each print their own one-line
		// warning and fall back to the built-in dashboard instead of
		// leaving Server.HMI set to an fs.FS that 404s every request —
		// see server.handleHMI, which has no way to know "no files here"
		// means "give '/' back to the dashboard" instead of "the HMI's
		// own build is just an empty SPA."
		switch st, statErr := fs.Stat(fsys, hmiPath); {
		case statErr != nil && errors.Is(statErr, fs.ErrNotExist):
			hmiMissing = true
		case statErr != nil:
			return nil, fmt.Errorf("server.hmi: %w", statErr)
		case !st.IsDir():
			return nil, fmt.Errorf("server.hmi: %s: not a directory (run the HMI's own build first, e.g. `npm run build` in its project)", hmiPath)
		default:
			if hmiFS, err = fs.Sub(fsys, hmiPath); err != nil {
				return nil, fmt.Errorf("server.hmi: %w", err)
			}
		}
	}
	return &Project{
		Name:    projName,
		Addr:    addr,
		Runtime: opts,
		Server: server.Options{
			Interval:     time.Duration(m.Server.Interval),
			OnlineEdits:  m.Server.OnlineEdits,
			HistorianURL: m.Server.Historian,
			HMI:          hmiFS,
		},
		sparkplug:  m.Sparkplug,
		inputTags:  inputs,
		Retain:     m.Retain,
		Redundancy: m.Redundancy,
		Alarms:     m.Alarms,
		HMIDir:     hmiDir,
		HMIMissing: hmiMissing,
	}, nil
}

// libraries composes the project's prelude: every root-level file with no
// PROGRAM, plus every `.st`/`.ld`/`.fbd` file under lib/ at any depth (see
// stproject.LibDir). `.st` files join verbatim, in path order; then `.ld`
// and `.fbd` files — libraries of ladder / netlist FUNCTION_BLOCKs, the IEC
// answer to a JSR — transpiled to ST, also in path order. See
// internal/stproject for why that tier order, and why it never decides
// whether a call resolves.
//
// Unlike the editor-side composition, a library that will not transpile is
// an ERROR here, and so is a PROGRAM under lib/: this is the path `naut
// check`, `run`, `build` and `test` take, and silently dropping a block
// would fail later as "unknown type" in whichever program used it. Errors
// name the file by its project-relative path (lib/motor.ld). The rule
// itself is stproject.Libraries, shared with `naut compose` (what the VS
// Code extension downloads), so the two can never disagree.
func libraries(fsys fs.FS) ([]string, error) {
	libs, err := stproject.Libraries(fsys, nil)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(libs))
	for i, l := range libs {
		out[i] = l.ST
	}
	return out, nil
}

// gvlTags turns a GVL — a library file's file-level VAR_GLOBAL block, the
// Codesys habit (#175) — into tags: each global the manifest does not
// already declare becomes a state tag, seeded with the zero of its
// declared type (runtime.expandTags), so it exists from scan one, is
// visible to every program (the block composes into each one's prelude),
// and reaches the HMI and the tag API like any other tag. A global the
// manifest also declares is that tag: the manifest supplies its role,
// init, unit and desc, and the GVL's declaration must agree with a type:
// it states. VAR_GLOBAL CONSTANT entries are constants, not tags.
func gvlTags(libs []string, declared []runtime.TagDef) []runtime.TagDef {
	have := make(map[string]bool, len(declared))
	for _, d := range declared {
		have[ir.NameKey(d.Name)] = true
	}
	var out []runtime.TagDef
	for _, src := range libs {
		prog, err := st.Parse(src)
		if err != nil {
			continue // the library reports its own error when it compiles
		}
		for _, g := range st.FileGlobals(prog) {
			if g.Constant || have[ir.NameKey(g.Name)] {
				continue
			}
			have[ir.NameKey(g.Name)] = true
			out = append(out, runtime.TagDef{Name: g.Name, Role: runtime.RoleState})
		}
	}
	return out
}

// TagDefs is the manifest's tags as the runtime declares them, plus each
// task's dt-tag (a REAL the runtime writes every scan): the table a
// program compiles against without declaring its tags. Tooling that
// compiles one file at a time — `naut check`, the language server —
// resolves it with runtime.ResolveTagScope, exactly as runtime.New does.
func TagDefs(m *Manifest) ([]runtime.TagDef, error) {
	defs, err := tagDefs(m.Tags)
	if err != nil {
		return nil, err
	}
	for _, t := range m.Tasks {
		if t.DtTag == "" {
			continue
		}
		dup := false
		for _, d := range defs {
			if ir.SameName(d.Name, t.DtTag) {
				dup = true
				break
			}
		}
		if !dup {
			defs = append(defs, runtime.State(t.DtTag, 0.0))
		}
	}
	return defs, nil
}

// TagDefsFor finds the manifest governing a source file (nautilus.yaml in
// its directory or the nearest one above it) and returns its TagDefs; nil
// when the file is in no project, or the manifest does not read.
func TagDefsFor(file string) []runtime.TagDef {
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil
	}
	dir := filepath.Dir(abs)
	for range 32 {
		if fi, err := os.Stat(filepath.Join(dir, ManifestName)); err == nil && !fi.IsDir() {
			m, err := ReadManifest(os.DirFS(dir), "")
			if err != nil {
				return nil
			}
			defs, err := TagDefs(m)
			if err != nil {
				return nil
			}
			return defs
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
	return nil
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
// documentation needs no new type; `naut check` reports keys that name
// nothing, which is where a typo surfaces.
func applyTagMeta(defs []runtime.TagDef, tm map[string]MetaConfig) map[string]runtime.TagMeta {
	if len(tm) == 0 {
		return nil
	}
	byName := make(map[string]int, len(defs))
	for i, d := range defs {
		byName[ir.NameKey(d.Name)] = i
	}
	out := make(map[string]runtime.TagMeta, len(tm))
	for key, mc := range tm {
		i, ok := byName[ir.NameKey(key)]
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

// normalize maps yaml's integer literals onto float64 (yaml decodes 65 as
// int, 65.0 as float64). BOOL and string pass through. This is only the
// fallback shape: runtime.expandTags seeds a scalar tag against the type the
// programs declare it as (VAR_EXTERNAL), and ir.SeedFromInit turns an
// integral float back into an INT there — so `init: 0` on a `Counter : DINT`
// seeds an integer, and only a tag no program declares seeds a REAL from a
// number.
//
// It does NOT recurse into a struct tag's nested init map: a member's
// target kind (REAL vs INT vs BOOL) is only known once the tag's `type:`
// resolves against the compiled TYPE table, so ir.SeedFromInit does that
// conversion itself, per member, once expandTags has the type in hand.
func normalize(v any) any {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return v
}

// driverHealth adapts a field driver's connection state onto the bool the
// Sparkplug device: wiring gates DBIRTH/DDEATH with. nil means the driver
// has no notion of a connection (memory) and the device is always healthy —
// the behaviour that existed before health reporting did. A multi-driver
// set is healthy only when EVERY child that has an opinion is: an operator
// reading the device online should be able to trust all of its tags, not
// the lucky subset whose bus is up.
func driverHealth(d nio.Driver) func() bool {
	switch drv := d.(type) {
	case *eip.Driver:
		return func() bool { return drv.Health().Connected }
	case *sphost.Driver:
		return func() bool { return drv.Status().Connected }
	case *nio.Multi:
		var checks []func() bool
		for _, c := range drv.Children() {
			if h := driverHealth(c.Driver); h != nil {
				checks = append(checks, h)
			}
		}
		if len(checks) == 0 {
			return nil
		}
		return func() bool {
			for _, h := range checks {
				if !h() {
					return false
				}
			}
			return true
		}
	}
	return nil
}

// buildDrivers resolves the manifest's driver:/drivers: pair into one
// nio.Driver. The singular form is sugar for a one-element list, so one
// construction path serves both; two or more wrap in nio.Multi, which is
// where duplicate tag ownership across drivers is refused (a load-time
// error naming both drivers — the same no-last-wins rule tag files keep).
func buildDrivers(fsys fs.FS, m Manifest) (nio.Driver, error) {
	configs := m.Drivers
	if !reflect.DeepEqual(m.Driver, DriverConfig{}) {
		if len(configs) > 0 {
			return nil, fmt.Errorf("%s: driver: and drivers: are both set — drivers: is the plural of the same section, so move the single driver into the list", ManifestName)
		}
		configs = []DriverConfig{m.Driver}
	}
	switch len(configs) {
	case 0:
		// No driver section at all: the memory loopback, as ever.
		return buildDriver(fsys, DriverConfig{})
	case 1:
		return buildDriver(fsys, configs[0])
	}
	// Names default to the type, deduped "eip-2" — stable, predictable, and
	// only needed once two drivers share a manifest. An explicit name that
	// collides is caught by NewMulti, which checks the final set.
	counts := map[string]int{}
	named := make([]nio.NamedDriver, 0, len(configs))
	for i, c := range configs {
		name := c.Name
		if name == "" {
			base := strings.ToLower(c.Type)
			if base == "" {
				base = "memory"
			}
			counts[base]++
			if n := counts[base]; n > 1 {
				name = fmt.Sprintf("%s-%d", base, n)
			} else {
				name = base
			}
		}
		d, err := buildDriver(fsys, c)
		if err != nil {
			return nil, fmt.Errorf("drivers[%d] (%s): %w", i, name, err)
		}
		named = append(named, nio.NamedDriver{Name: name, Driver: d})
	}
	multi, err := nio.NewMulti(named...)
	if err != nil {
		return nil, fmt.Errorf("drivers: %w", err)
	}
	return multi, nil
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
		// "host:port" reaches a controller (or `naut logix emulate`) off
		// the standard 44818 — a bare host keeps the default.
		host := d.Host
		if h, p, err := net.SplitHostPort(host); err == nil {
			port, err := strconv.Atoi(p)
			if err != nil || port <= 0 || port > 65535 {
				return nil, fmt.Errorf("driver eip: host %q: bad port %q", d.Host, p)
			}
			host = h
			opts = append(opts, eip.WithPort(port))
		}
		if d.ScanRate != 0 {
			opts = append(opts, eip.WithScanRate(time.Duration(d.ScanRate)))
		}
		for name, rate := range d.ScanClasses {
			opts = append(opts, eip.WithScanClass(name, time.Duration(rate)))
		}
		for class, patterns := range d.TagClasses {
			opts = append(opts, eip.WithTagClass(class, patterns...))
		}
		return eip.New(host, em, opts...)
	case "sparkplug-host":
		// A Sparkplug B host application: consume a whole group of edge
		// nodes as INPUT tags, send operator writes back as NCMD/DCMD.
		//
		// host.New NEVER dials — buildDriver runs inside `naut check`
		// and `naut build`, i.e. in CI with no broker — so everything
		// that can fail on bad configuration fails here, offline, and the
		// connection is Start's job. Same split as eip.
		if d.Broker == "" {
			return nil, fmt.Errorf("driver sparkplug-host: broker is required (tcp://host:1883)")
		}
		if d.HostID == "" {
			return nil, fmt.Errorf("driver sparkplug-host: host-id is required (the STATE topic spBv1.0/STATE/<host-id>)")
		}
		if d.Manifest == "" {
			return nil, fmt.Errorf("driver sparkplug-host: manifest (the imported sparkplug manifest .yaml) is required")
		}
		// A host that subscribed to every group would consume other
		// projects' traffic silently; name the group you mean.
		if d.GroupID == "" && len(d.GroupIDs) == 0 {
			return nil, fmt.Errorf("driver sparkplug-host: group-id (or group-ids) is required")
		}
		raw, err := fs.ReadFile(fsys, path.Clean(d.Manifest))
		if err != nil {
			return nil, fmt.Errorf("driver sparkplug-host: %w", err)
		}
		// ParseManifest decodes with KnownFields(true), so a typo is an
		// error rather than a silently dropped binding.
		hm, err := sphost.ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("driver sparkplug-host: %s: %w", d.Manifest, err)
		}
		groups := d.GroupIDs
		if len(groups) == 0 {
			groups = []string{d.GroupID}
		}
		cfg := sphost.Config{
			BrokerURL: d.Broker,
			HostID:    d.HostID,
			GroupIDs:  groups,
			ClientID:  d.ClientID,
			Username:  d.Username,
			Password:  os.Getenv("NAUTILUS_MQTT_PASSWORD"),
			Keepalive: time.Duration(d.Keepalive),
			// primary: and rebirth-on-start: both DEFAULT TRUE, so they are
			// pointers here — an absent key is "yes", and only an explicit
			// `false` opts out.
			Primary:          d.Primary == nil || *d.Primary,
			StateForm:        d.StateForm,
			ReorderTimeout:   time.Duration(d.ReorderTimeout),
			StaleAfter:       time.Duration(d.StaleAfter),
			NoRebirthOnStart: d.RebirthOnStart != nil && !*d.RebirthOnStart,
		}
		opts := []sphost.Option{sphost.WithLogger(slog.Default().With("driver", "sparkplug-host"))}
		if d.OnUnknown != "" {
			opts = append(opts, sphost.WithDiscovery(d.OnUnknown))
		}
		return sphost.New(hm, cfg, opts...)
	case "modbus":
		// Modbus TCP: every polled field device on a skid or site (brief
		// docs/design/modbus.md). modbus.New NEVER dials — it validates the
		// manifest and computes the block-read plan offline, so `nautilus
		// check` and `build` pass with no device in sight, the same split
		// eip and sparkplug-host make. The config keys are eip's, reused
		// wholesale: manifest, scan-rate, scan-classes, tag-classes.
		if d.Manifest == "" {
			return nil, fmt.Errorf("driver modbus: manifest (the imported modbus_manifest.yaml) is required")
		}
		raw, err := fs.ReadFile(fsys, path.Clean(d.Manifest))
		if err != nil {
			return nil, fmt.Errorf("driver modbus: %w", err)
		}
		// ParseManifest decodes with KnownFields semantics, so a typo is an
		// error rather than a silently dropped binding.
		mm, err := modbus.ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("driver modbus: %s: %w", d.Manifest, err)
		}
		opts := []modbus.Option{modbus.WithLogger(slog.Default().With("driver", "modbus"))}
		if d.ScanRate != 0 {
			opts = append(opts, modbus.WithScanRate(time.Duration(d.ScanRate)))
		}
		for name, rate := range d.ScanClasses {
			opts = append(opts, modbus.WithScanClass(name, time.Duration(rate)))
		}
		for class, patterns := range d.TagClasses {
			opts = append(opts, modbus.WithTagClass(class, patterns...))
		}
		return modbus.New(mm, opts...)
	case "snmp", "redfish", "prometheus":
		// The IT-hardware drivers (brief docs/design/it-drivers.md): a
		// switch, a PDU or a UPS over SNMP, a server BMC over Redfish, a
		// commodity host over node_exporter — all delivering the same UDT
		// set on hw.Base, and all wired exactly like modbus: a generated
		// manifest, New never dials, the eip keys reused as poll intervals.
		// Secrets never enter the manifest; an unset credential variable
		// is a `naut check` warning, not a load error, because check runs
		// on laptops that have no secrets.
		if d.Manifest == "" {
			return nil, fmt.Errorf("driver %s: manifest (the imported %s_manifest.yaml) is required", d.Type, d.Type)
		}
		raw, err := fs.ReadFile(fsys, path.Clean(d.Manifest))
		if err != nil {
			return nil, fmt.Errorf("driver %s: %w", d.Type, err)
		}
		log := slog.Default().With("driver", d.Type)
		switch d.Type {
		case "snmp":
			m, err := snmp.ParseManifest(raw)
			if err != nil {
				return nil, fmt.Errorf("driver snmp: %s: %w", d.Manifest, err)
			}
			opts := []snmp.Option{snmp.WithLogger(log)}
			if d.ScanRate != 0 {
				opts = append(opts, snmp.WithScanRate(time.Duration(d.ScanRate)))
			}
			for name, rate := range d.ScanClasses {
				opts = append(opts, snmp.WithScanClass(name, time.Duration(rate)))
			}
			for class, patterns := range d.TagClasses {
				opts = append(opts, snmp.WithTagClass(class, patterns...))
			}
			return snmp.New(m, opts...)
		case "redfish":
			m, err := redfish.ParseManifest(raw)
			if err != nil {
				return nil, fmt.Errorf("driver redfish: %s: %w", d.Manifest, err)
			}
			opts := []redfish.Option{redfish.WithLogger(log)}
			if d.ScanRate != 0 {
				opts = append(opts, redfish.WithScanRate(time.Duration(d.ScanRate)))
			}
			for name, rate := range d.ScanClasses {
				opts = append(opts, redfish.WithScanClass(name, time.Duration(rate)))
			}
			for class, patterns := range d.TagClasses {
				opts = append(opts, redfish.WithTagClass(class, patterns...))
			}
			return redfish.New(m, opts...)
		default:
			m, err := prom.ParseManifest(raw)
			if err != nil {
				return nil, fmt.Errorf("driver prometheus: %s: %w", d.Manifest, err)
			}
			opts := []prom.Option{prom.WithLogger(log)}
			if d.ScanRate != 0 {
				opts = append(opts, prom.WithScanRate(time.Duration(d.ScanRate)))
			}
			for name, rate := range d.ScanClasses {
				opts = append(opts, prom.WithScanClass(name, time.Duration(rate)))
			}
			for class, patterns := range d.TagClasses {
				opts = append(opts, prom.WithTagClass(class, patterns...))
			}
			return prom.New(m, opts...)
		}
	case "replay":
		// A recording played back as live tags at a steerable clock: the
		// baseline of a plant simulation. The recording itself is read on
		// Start, so check and build pass where it is absent.
		if d.Manifest == "" {
			return nil, fmt.Errorf("driver replay: manifest (the replay_manifest.yaml) is required")
		}
		raw, err := fs.ReadFile(fsys, path.Clean(d.Manifest))
		if err != nil {
			return nil, fmt.Errorf("driver replay: %w", err)
		}
		m, err := replay.ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("driver replay: %s: %w", d.Manifest, err)
		}
		return replay.New(m, fsys, replay.WithLogger(slog.Default().With("driver", "replay")))
	default:
		return nil, fmt.Errorf("driver type %q: manifest projects support memory, eip, sparkplug-host, modbus, snmp, redfish, prometheus and replay — custom buses are the Go tier (io.Driver)", d.Type)
	}
}
