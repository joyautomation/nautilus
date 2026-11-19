//go:build !js

package project

// Live I/O configuration (#706): a candidate manifest is staged, checked
// against what runs, and applied while the scan keeps running. A Modbus
// device (a source, which is what a devices: row becomes) is applied on its
// own, in the driver that runs, with modbus.ReplaceSource; any other changed
// driver is rebuilt, started and swapped in with runtime.ReplaceDriver.
//
// Only addressing and binding may change: the tag set, the drivers and the
// devices are what the runtime was built with, so a candidate that changes
// them is refused with what to do instead (restart, or activate a program).
// A refused or failed apply leaves what runs as it was.
//
// The HTTP API over this (pending → validate → apply → result) and the
// running-versus-manifest report it feeds are server/ioconfig.go's (#706
// PR 3b); live probes of a changed device are PR 4; keeping an applied
// configuration across a restart or a takeover is iokeep.go's (PR 5).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/devbind"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/opcua"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
)

// IORuntime is what an apply needs of the running runtime.
type IORuntime interface {
	ReplaceDriverObserved(ctx context.Context, name string, d nio.Driver) (nio.Driver, <-chan nio.Values, error)
	ReplaceDriverAtStart(ctx context.Context, name string, d nio.Driver) (nio.Driver, error)
	ReplaceDriver(ctx context.Context, name string, d nio.Driver) (nio.Driver, error)
	Tags() *runtime.Tags
}

// IOConfig is a running project's I/O configuration: what the manifest it
// booted from says, what runs now, and the applies between the two.
type IOConfig struct {
	p        *Project
	rt       IORuntime
	manifest string
	hold     bool // the runtime's Options.HoldOutputsOnSwap (#706 decision 1)
	bootYAML string
	bootRows []devbind.Device // the boot manifest's devices:
	bootST   string           // and their composed types

	applying sync.Mutex // one apply at a time
	mu       sync.Mutex // guards boot and running
	boot     map[string]ioUnit
	running  map[string]ioUnit

	// replaceSource is modbus.(*Driver).ReplaceSource and swapDriver is
	// (*IOConfig).swap; a test makes either fail.
	replaceSource func(mb *modbus.Driver, src modbus.Source, tags []modbus.TagBinding, opts modbus.ReplaceOptions) error
	swapDriver    func(ctx context.Context, name string, fsys fs.FS, cfg DriverConfig) (*swapped, error)

	// Gate, when set, is the write gate naut run installs on the drivers:
	// a driver without a look of its own (the OPC UA client) commands
	// nothing while it is closed, and willCommand says so.
	Gate func() bool

	// faults are, per unit, why putting back a failed apply failed: what
	// runs there is not known (Fault) until an apply re-establishes it.
	faults map[string]string

	// gen counts what changed what runs: every apply and every revert. A
	// watch reverts only over its own apply's generation.
	gen   int
	watch *ioWatch // the last apply's (IOWatch)

	// Keeping what an apply changed across a restart and a takeover
	// (iokeep.go). bootFS is the project the runtime booted from.
	bootFS fs.FS
	// Keep, when set, saves the retain store now and returns once it holds
	// what RetainedIO says (runtime.SaveRetained); nil when the project has
	// no retain: block, and nothing is kept.
	Keep func(ctx context.Context) error
	// KeepStore is the retain store's kind ("file", "configmap"): a file
	// is one replica's own, so a redundant project cannot keep in it.
	KeepStore string
	// PeersKeepIO, when set (a redundant pair, naut run), says why the
	// other replica can't keep a live apply across a takeover (its build
	// predates retain io, #1013), or "" when it can: a mixed-version pair
	// refuses a live apply at validate.
	PeersKeepIO func() string
	// atStart: the restore under way is a standalone controller's, before
	// its first scan, so a driver swapped whole keeps its start-up
	// baseline (as at any start) instead of starting from the commands.
	atStart bool
	// kept is what the store is to hold: what runs, except an apply whose
	// revert-if-silent watch has not settled (provisional).
	kept        map[string]ioUnit
	provisional *retain.IOProvisional
	// discarded are the kept changes a restore dropped, for the store
	// (recovery by hand) and the report (Discarded).
	discarded      map[string]retain.IOUnit
	discardedFiles map[string]string
	dropped        []IODiscard
	// held are the units a restore holds (holdUnits), until replaced.
	held map[string]bool
	// seq is the sequence of the store state this replica last saved or
	// followed: it moves only on a durable save (IOSaved) or a follow, so
	// two replicas never claim one number for two states. storeSeq is the
	// highest the store is known to hold; changed counts changes to what
	// is kept, savedChanged those a save has made durable. A save with
	// changes pending takes max(seq, storeSeq)+1 (RetainedIO).
	seq, storeSeq         uint64
	changed, savedChanged uint64
	// offered is what RetainedIO last returned, which IOSaved commits.
	offered struct {
		seq, changed uint64
		hash         string
		gen          int
	}
	// unreadable says why the store could not be read at the last edge:
	// nothing is kept from it or promised to it until an edge reads it.
	unreadable string
	// restoring: a restore runs; edgeSeen: a leadership edge has re-applied
	// (or found current) what the store keeps. An apply waits for both.
	restoring, edgeSeen bool
	// unsaved says why the store does not hold what an apply kept (its
	// save failed); a retry clears it once a save lands. closed ends that
	// retry (Close).
	unsaved   string
	retrying  bool
	closed    chan struct{}
	closeOnce sync.Once
	// memo is the store state the last restore made run, at gen.
	memo struct {
		st  string
		gen int
	}
}

// swapped is a driver swapped in whole, and how to hear what the first scan
// after the swap commanded through it.
type swapped struct {
	d nio.Driver
	// pushed is what the first push handed d (runtime.ReplaceDriverObserved).
	pushed <-chan nio.Values
	// shares, for a Modbus driver, is each source's first share, as the
	// driver decided it (its baseline, the write gate, a parked source):
	// nsrc of them.
	shares <-chan srcShare
	nsrc   int
}

type srcShare struct {
	id string
	modbus.FirstShare
}

// ErrRollbackFailed is an apply that failed and could not put back all it
// had changed: what runs is no longer known. The error names what is left.
var ErrRollbackFailed = errors.New("putting back a failed I/O apply failed")

// unknownHash marks a unit whose configuration is not known after a failed
// rollback: it differs from every manifest and every candidate's base.
const unknownHash = "unknown"

// firstPushWait bounds how long Apply waits for the first scan after it to
// hand over its commands.
const firstPushWait = 2 * time.Second

// swappable are the driver types a whole-driver swap is proven for. The
// new driver starts before the old one stops: harmless for a poller, but a
// Sparkplug host would end the old session's STATE after the new one
// declared it, and two PROFINET devices would answer for one station name.
var swappable = map[string]bool{"modbus": true, "opcua-client": true}

// undoTimeout bounds putting a driver back after a failed apply.
const undoTimeout = 10 * time.Second

// ErrApplyBusy refuses an apply while another runs.
var ErrApplyBusy = errors.New("another I/O apply is in progress; try again when it is done")

// ErrIOStale refuses an apply whose candidate was checked against an I/O
// configuration that no longer runs.
var ErrIOStale = errors.New("the I/O configuration that runs has changed since this candidate was validated; stage it again")

// NewIOConfig reads the manifest p was loaded from (fsys, manifest) as the
// boot configuration. rt is the runtime built from p.
func NewIOConfig(p *Project, rt IORuntime, fsys fs.FS, manifest string) (*IOConfig, error) {
	m, err := ReadManifest(fsys, manifest)
	if err != nil {
		return nil, err
	}
	_, units, err := ioUnits(fsys, m)
	if err != nil {
		return nil, err
	}
	types, _ := m.devices.TypesST()
	running := make(map[string]ioUnit, len(units))
	for k, u := range units {
		running[k] = u
	}
	io := &IOConfig{
		p: p, rt: rt, manifest: manifest, hold: p.Runtime.HoldOutputsOnSwap,
		bootYAML: nonIOYAML(m), bootRows: m.Devices, bootST: types, boot: units, running: running,
		replaceSource: func(mb *modbus.Driver, src modbus.Source, tags []modbus.TagBinding, opts modbus.ReplaceOptions) error {
			return mb.ReplaceSource(src, tags, opts)
		},
	}
	io.swapDriver = io.swap
	p.ioDiffers = io.driverDiffers
	io.bootFS = fsys
	io.kept = copyUnits(running) // its own map: an apply changes running, not this
	io.memo.st = hashOf((*retain.IOState)(nil))
	io.closed = make(chan struct{})
	return io, nil
}

// ioUnit is one thing an apply replaces: a whole driver, or one source of a
// Modbus driver. A Modbus driver is a unit of its own too, for its
// driver-wide settings (scan rate and classes); a change there swaps the
// whole driver.
type ioUnit struct {
	driver string
	device string // a Modbus source id; "" for the driver itself
	typ    string // the driver's type, lower case
	hash   string

	cfg  DriverConfig // the driver's entry (a driver unit)
	fsys fs.FS        // where cfg's files are read from
	src  modbus.Source
	tags []modbus.TagBinding // the source's bindings, in manifest order

	from string // "" (the manifest), "upload", or the commit
	at   time.Time
	by   string

	// files are the project files the unit's candidate was read from that
	// differ from the boot files (what retain keeps and naut pull writes).
	files map[string]string
}

func unitKey(driver, device string) string {
	if device == "" {
		return driver
	}
	return driver + "/" + device
}

func (u ioUnit) key() string { return unitKey(u.driver, u.device) }

func hashOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Every value hashed here is plain data; should one ever not
		// marshal, its Go spelling still tells two configurations apart.
		b = []byte(fmt.Sprintf("%#v", v))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// driverConfigs resolves a manifest's driver:/drivers: (and devices:) into
// the named entries Load builds. It mirrors buildDrivers, which a test pins.
func driverConfigs(m *Manifest) ([]DriverConfig, error) {
	configs := m.Drivers
	if !reflect.DeepEqual(m.Driver, DriverConfig{}) {
		if len(configs) > 0 {
			return nil, fmt.Errorf("%s: driver: and drivers: are both set — drivers: is the plural of the same section, so move the single driver into the list", ManifestName)
		}
		configs = []DriverConfig{m.Driver}
	}
	if m.devices.Len() > 0 {
		var err error
		if configs, err = withDevices(configs, m.devices); err != nil {
			return nil, err
		}
	}
	names := driverNames(configs)
	out := make([]DriverConfig, len(configs))
	for i, c := range configs {
		c.Name = names[i]
		out[i] = c
	}
	return out, nil
}

// ioUnits is a manifest's I/O configuration, unit by unit.
func ioUnits(fsys fs.FS, m *Manifest) ([]DriverConfig, map[string]ioUnit, error) {
	configs, err := driverConfigs(m)
	if err != nil {
		return nil, nil, err
	}
	units := map[string]ioUnit{}
	for _, c := range configs {
		typ := strings.ToLower(c.Type)
		if typ == "modbus" {
			mm, err := modbusManifest(fsys, c)
			if err != nil {
				return nil, nil, err
			}
			settings := struct {
				ScanRate    Duration
				ScanClasses map[string]Duration
				TagClasses  map[string][]string
			}{c.ScanRate, c.ScanClasses, c.TagClasses}
			units[c.Name] = ioUnit{driver: c.Name, typ: typ, hash: hashOf(settings), cfg: c, fsys: fsys}
			for _, src := range mm.Sources {
				var tags []modbus.TagBinding
				for _, t := range mm.Tags {
					if t.Source == src.ID {
						tags = append(tags, t)
					}
				}
				u := ioUnit{driver: c.Name, device: src.ID, typ: typ, src: src, tags: tags, cfg: c, fsys: fsys}
				u.hash = hashOf(struct {
					Source modbus.Source
					Tags   []modbus.TagBinding
				}{src, tags})
				units[u.key()] = u
			}
			continue
		}
		if typ == "opcua-client" {
			// What it binds, an OPC UA devices: row's derived from its
			// profile: an edited profile node id is a change too.
			om, err := opcuaManifest(fsys, c)
			if err != nil {
				return nil, nil, fmt.Errorf("driver %s: %w", c.Name, err)
			}
			hc := c
			hc.Validate = "" // how a change is checked, not what runs
			units[c.Name] = ioUnit{driver: c.Name, typ: typ, cfg: c, fsys: fsys, hash: hashOf(struct {
				Config   DriverConfig
				Device   string
				Manifest opcua.Manifest
			}{hc, c.opcuaDevice, om})}
			continue
		}
		var file string
		if c.Manifest != "" {
			raw, err := fs.ReadFile(fsys, path.Clean(c.Manifest))
			if err != nil {
				return nil, nil, fmt.Errorf("driver %s: %w", c.Name, err)
			}
			file = string(raw)
		}
		units[c.Name] = ioUnit{driver: c.Name, typ: typ, cfg: c, fsys: fsys, hash: hashOf(struct {
			Config DriverConfig
			File   string
		}{c, file})}
	}
	return configs, units, nil
}

// nonIOYAML is the manifest without its I/O sections: what a candidate is
// compared on to say it changes more than an apply applies.
func nonIOYAML(m *Manifest) string {
	c := *m
	c.Driver, c.Drivers, c.Devices = DriverConfig{}, nil, nil
	b, err := yaml.Marshal(&c)
	if err != nil {
		return ""
	}
	return string(b)
}

// IOChange is one unit a candidate changes.
type IOChange struct {
	Driver string `json:"driver"`
	// Device is the Modbus source (a devices: row) applied on its own; ""
	// when the whole driver is swapped.
	Device string `json:"device,omitempty"`
	Before string `json:"before"` // the running configuration's hash
	After  string `json:"after"`  // the candidate's
}

// IOCheck is one validation check's result.
type IOCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// IOCommand is an output the scan after an apply sends to a changed device,
// with the value it would send now (#706 decision 1).
type IOCommand struct {
	Tag   string `json:"tag"`
	Value any    `json:"value"`
}

// IOValidation is a candidate's checks, what it changes and what the apply
// will command.
type IOValidation struct {
	Valid   bool       `json:"valid"`
	Checks  []IOCheck  `json:"checks"`
	Changes []IOChange `json:"changes"`
	// WillCommand is every output the first scan after the apply sends to a
	// changed device or driver. Under HoldOutputsOnSwap (Held) it lists only
	// the keep-alive (rewrite:) bindings, which a start-up baseline never
	// holds back; the rest are sent once they change.
	WillCommand []IOCommand `json:"willCommand"`
	Held        bool        `json:"held,omitempty"`
	Notes       []string    `json:"notes,omitempty"`
	// Probes are the live checks of each changed device (live or static,
	// and why), run once every static check has passed.
	Probes []IOProbe `json:"probes,omitempty"`
}

// IOResult is what an apply did.
type IOResult struct {
	Changes []IOChange `json:"changes"`
	// Answered are the changed devices that had read when the apply
	// returned; Pending, those not yet. Watch follows them after (Watch).
	Answered []string `json:"answered,omitempty"`
	Pending  []string `json:"pending,omitempty"`
	Watch    *IOWatch `json:"watch,omitempty"`
	// Commanded is what the first scan after the apply handed the changed
	// devices and drivers and they queued to send, by the driver's own
	// rule (its write gate, a parked source, a held baseline).
	Commanded []IOCommand `json:"commanded"`
	Notes     []string    `json:"notes,omitempty"`
	// Kept says whether the retain store holds the change, so a restart
	// or a takeover comes back on it; KeptWhy says why not.
	Kept    bool   `json:"kept"`
	KeptWhy string `json:"keptWhy,omitempty"`
}

// IOCandidate is a staged I/O configuration.
type IOCandidate struct {
	From string // "upload", or the commit it was staged from
	By   string

	fsys    fs.FS
	m       *Manifest
	err     error // the candidate does not load
	configs map[string]DriverConfig
	units   map[string]ioUnit
	built   map[string]nio.Driver // its drivers, built, never started
	mu      sync.Mutex            // guards val and base: Validate and Apply may race
	val     *IOValidation         // the last Validate's
	base    map[string]string     // the running hashes it was validated against

	files   map[string]string // what it reads that differs from the boot files
	restore bool              // a kept configuration re-applied (RestoreIO)
}

// Validation is the last Validate's result, nil before one.
func (c *IOCandidate) Validation() *IOValidation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.val
}

// Stage reads a candidate I/O configuration from fsys: a commit's file
// snapshot, or the running project's files with uploads laid over them
// (OverlayFS). from is the commit, or "upload"; by is who staged it. A
// candidate that does not load is staged anyway and fails validation.
func (io *IOConfig) Stage(fsys fs.FS, from, by string) *IOCandidate {
	// What the candidate reads is recorded, so an apply can keep the files
	// that differ from the boot ones (iokeep.go).
	rec := &recordFS{base: fsys, seen: map[string]bool{}}
	fsys = rec
	c := &IOCandidate{From: from, By: by, fsys: fsys}
	defer func() {
		c.files = io.differingFiles(rec)
		for k, u := range c.units {
			u.files = c.files
			c.units[k] = u
		}
	}()
	m, err := ReadManifest(fsys, io.manifest)
	if err != nil {
		c.err = err
		return c
	}
	c.m = m
	configs, units, err := ioUnits(fsys, m)
	if err != nil {
		c.err = err
		return c
	}
	if _, named, err := buildDrivers(fsys, *m); err != nil {
		c.err = err
		return c
	} else {
		c.built = map[string]nio.Driver{}
		for _, nd := range named {
			c.built[nd.Name] = nd.Driver
		}
	}
	c.configs = map[string]DriverConfig{}
	for _, cfg := range configs {
		c.configs[cfg.Name] = cfg
	}
	c.units = units
	return c
}

// Validate checks c against what runs now and records what it was checked
// against: Apply refuses it once that changes.
func (io *IOConfig) Validate(c *IOCandidate) *IOValidation {
	return io.validate(c, true)
}

// validate is Validate; probes false skips the live probes (a restore of a
// configuration that already ran, iokeep.go).
func (io *IOConfig) validate(c *IOCandidate, probes bool) *IOValidation {
	io.mu.Lock()
	running := make(map[string]ioUnit, len(io.running))
	for k, u := range io.running {
		running[k] = u
	}
	io.mu.Unlock()

	v := &IOValidation{Checks: []IOCheck{}, Changes: []IOChange{}, WillCommand: []IOCommand{}}
	check := func(name string, problems []string) {
		ck := IOCheck{Name: name, OK: len(problems) == 0}
		if !ck.OK {
			ck.Detail = strings.Join(problems, "; ")
		}
		v.Checks = append(v.Checks, ck)
	}
	live := IOCheck{Name: "live", Skipped: true, OK: true, Detail: "not run: the candidate failed a static check"}
	finish := func() *IOValidation {
		v.Checks = append(v.Checks, live)
		v.Valid = true
		for _, ck := range v.Checks {
			v.Valid = v.Valid && ck.OK
		}
		base := map[string]string{}
		for k, u := range running {
			base[k] = u.hash
		}
		c.mu.Lock()
		c.val, c.base = v, base
		c.mu.Unlock()
		return v
	}

	if c.err != nil {
		check("schema", []string{c.err.Error()})
		return finish()
	}
	check("schema", nil)

	// Topology: the same drivers of the same types, the same Modbus sources.
	var topo []string
	for _, k := range sortedKeys(running) {
		if _, ok := c.units[k]; !ok {
			topo = append(topo, removedText(running[k]))
		}
	}
	for _, k := range sortedKeys(c.units) {
		u := c.units[k]
		r, ok := running[k]
		switch {
		case !ok:
			topo = append(topo, addedText(u))
		case u.device == "" && r.typ != u.typ:
			topo = append(topo, fmt.Sprintf("driver %s changes type from %s to %s", k, r.typ, u.typ))
		}
	}
	if len(topo) > 0 {
		check("topology", append(topo, "the drivers and devices are fixed while the runtime runs: restart to apply this"))
		return finish()
	}
	check("topology", nil)

	// What changes: a Modbus driver's own settings swap it whole; else each
	// changed source is applied on its own.
	whole := map[string]bool{}
	for _, k := range sortedKeys(c.units) {
		if u := c.units[k]; u.device == "" && u.hash != running[k].hash {
			whole[u.driver] = true
			v.Changes = append(v.Changes, IOChange{Driver: u.driver, Before: running[k].hash, After: u.hash})
		}
	}
	for _, k := range sortedKeys(c.units) {
		if u := c.units[k]; u.device != "" && !whole[u.driver] && u.hash != running[k].hash {
			v.Changes = append(v.Changes, IOChange{Driver: u.driver, Device: u.device, Before: running[k].hash, After: u.hash})
		}
	}
	if len(v.Changes) == 0 {
		check("changes", []string{"the candidate's I/O configuration is what runs: nothing to apply"})
	} else {
		check("changes", nil)
	}

	// Kept: a redundant project keeps an apply in its retain store, or a
	// standby that takes over would come up on the old addresses.
	if !c.restore {
		check("kept", io.keptProblems(c))
	}

	// A whole driver swaps only inside the composite (two or more drivers).
	var comp []string
	for _, ch := range v.Changes {
		if ch.Device != "" {
			continue
		}
		if _, ok := io.p.Runtime.Driver.(*nio.Multi); !ok {
			comp = append(comp, fmt.Sprintf("driver %s: swapping a whole driver live needs two or more drivers composed, and this project has one; restart to apply it (a Modbus device's own settings apply live on their own)", ch.Driver))
		}
	}
	check("composite", comp)
	var kinds []string
	for _, ch := range v.Changes {
		if t := c.units[ch.Driver].typ; ch.Device == "" && !swappable[t] {
			kinds = append(kinds, fmt.Sprintf("driver %s (type %s) is not swapped live yet (modbus and opcua-client are): restart to apply it", ch.Driver, t))
		}
	}
	check("swappable", kinds)

	// Tags: each changed driver delivers and accepts what it did, typed as
	// it was; a changed device keeps its own bindings.
	var tagp []string
	seen := map[string]bool{}
	for _, ch := range v.Changes {
		if seen[ch.Driver] {
			continue
		}
		seen[ch.Driver] = true
		if err := sameTypedTags(io.currentDriver(ch.Driver), c.built[ch.Driver]); err != nil {
			tagp = append(tagp, fmt.Sprintf("driver %s: %v", ch.Driver, err))
		}
	}
	if len(tagp) > 0 {
		tagp = append(tagp, "the tag set is fixed while the runtime runs: restart, or activate a program")
	}
	check("tags", tagp)

	check("device-type", io.deviceTypeProblems(c))
	check("fields", io.fieldProblems(c, running))
	check("outputs", newOutputConflicts(running, c.units))

	if nonIOYAML(c.m) != io.bootYAML {
		v.Notes = append(v.Notes, "the candidate's manifest changes more than its I/O; only drivers:, devices: and the files they name are applied (activate a program, or deploy, for the rest)")
	}
	v.Held = io.hold
	var why []string
	v.WillCommand, why = io.willCommand(c, v.Changes)
	v.Notes = append(v.Notes, why...)
	if len(v.WillCommand) > 0 {
		v.Notes = append(v.Notes, "willCommand holds the commands as of this validation; the scan may change them before the apply, and the apply reports what the first scan after it actually handed over")
	}
	for _, ch := range v.Changes {
		if ch.Device == "" {
			v.Notes = append(v.Notes, fmt.Sprintf("driver %s is swapped whole: the new driver connects before the old one disconnects, so a device that allows a single connection may refuse it until the old one has gone", ch.Driver))
		}
	}
	// Probe the changed devices only once the candidate passes every
	// static check: an invalid one is refused whatever the devices say.
	statically := true
	for _, ck := range v.Checks {
		statically = statically && ck.OK
	}
	if statically && probes {
		v.Probes = probe(probeTargets(c, v.Changes, running))
		live = liveCheck(v.Probes)
	}
	return finish()
}

func removedText(u ioUnit) string {
	if u.device != "" {
		return fmt.Sprintf("Modbus device %s (driver %s) is not in the candidate", u.device, u.driver)
	}
	return fmt.Sprintf("driver %s is not in the candidate", u.driver)
}

func addedText(u ioUnit) string {
	if u.device != "" {
		return fmt.Sprintf("Modbus device %s (driver %s) is new", u.device, u.driver)
	}
	return fmt.Sprintf("driver %s is new", u.driver)
}

// sameTypedTags is runtime.ReplaceDriver's tag check, with the types too.
func sameTypedTags(old, d nio.Driver) error {
	o, ok1 := old.(tagNames)
	n, ok2 := d.(tagNames)
	if !ok1 || !ok2 {
		return fmt.Errorf("both drivers must declare their tags")
	}
	if a, b := sortedNames(o.InputNames()), sortedNames(n.InputNames()); !slices.Equal(a, b) {
		return fmt.Errorf("the candidate delivers inputs %v, not %v", b, a)
	}
	if a, b := sortedNames(o.OutputNames()), sortedNames(n.OutputNames()); !slices.Equal(a, b) {
		return fmt.Errorf("the candidate accepts outputs %v, not %v", b, a)
	}
	ot, ok1 := old.(inputTyper)
	nt, ok2 := d.(inputTyper)
	if ok1 && ok2 {
		var retyped []string
		for _, name := range sortedNames(o.InputNames()) {
			if a, b := ot.InputType(name), nt.InputType(name); a != b {
				retyped = append(retyped, fmt.Sprintf("%s from %s to %s", name, a, b))
			}
		}
		if len(retyped) > 0 {
			return fmt.Errorf("the candidate retypes %s", strings.Join(retyped, ", "))
		}
	}
	return nil
}

type tagNames interface {
	InputNames() []string
	OutputNames() []string
}

func sortedNames(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// deviceTypeProblems refuses a devices: row whose profile or points change:
// that changes the device's struct, which the runtime fixed at start. A
// vendored profile's addresses may change; its members may not.
func (io *IOConfig) deviceTypeProblems(c *IOCandidate) []string {
	rows := map[string]devbind.Device{}
	for _, r := range io.bootRows {
		rows[r.Name] = r
	}
	var out []string
	for _, r := range c.m.Devices {
		b, ok := rows[r.Name]
		if !ok {
			continue // topology reports it
		}
		if b.Profile != r.Profile {
			out = append(out, fmt.Sprintf("device %s changes profile from %s to %s", r.Name, b.Profile, r.Profile))
		}
		if !slices.Equal(b.Points, r.Points) {
			out = append(out, fmt.Sprintf("device %s changes its points from %v to %v", r.Name, b.Points, r.Points))
		}
	}
	if st, _ := c.m.devices.TypesST(); st != io.bootST {
		out = append(out, "the device types (their struct members) change")
	}
	if len(out) > 0 {
		out = append(out, "a device's struct is fixed while the runtime runs: restart to apply this")
	}
	return out
}

// What an apply may change (#706 v1): addressing and binding. Every other
// field is refused by name, so nothing changes live that was not meant to
// (word order, timeouts, a keep-alive, a scale, a security policy).
var (
	liveSource   = fieldSet("Host", "Port", "UnitID")
	liveModbus   = fieldSet("Table", "Address", "ScanClass")
	liveOPCUA    = fieldSet("NodeID", "Namespace", "ScanClass")
	liveRow      = fieldSet("Name", "Endpoint", "UnitID", "Profile", "Points", "Validate") // profile and points: device-type's
	liveDriverOf = map[string]map[string]bool{
		"modbus":       fieldSet("Type", "Name", "Manifest", "ScanRate", "ScanClasses", "TagClasses", "Validate"),
		"opcua-client": fieldSet("Type", "Name", "Manifest", "Endpoint", "ScanRate", "ScanClasses", "TagClasses", "Validate"),
	}
)

func fieldSet(names ...string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out
}

// changedFields names the exported fields of two values of one struct type
// that differ and allowed does not hold, as a manifest spells them (the
// yaml key, else the field name in lower case).
func changedFields(a, b any, allowed map[string]bool) []string {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	var out []string
	for i := 0; i < va.NumField(); i++ {
		f := va.Type().Field(i)
		if !f.IsExported() || allowed[f.Name] {
			continue
		}
		if reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
			continue
		}
		name := strings.ToLower(f.Name)
		if tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ","); tag != "" && tag != "-" {
			name = tag
		}
		out = append(out, name)
	}
	return out
}

func keyNames(m map[string]bool) string {
	var out []string
	for k := range m {
		out = append(out, strings.ToLower(k))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// fieldProblems refuses every change outside the allow-lists above: on a
// driver entry, a Modbus source, a Modbus or OPC UA binding (a binding's
// source among them, so a tag never moves between devices), a devices:
// row.
func (io *IOConfig) fieldProblems(c *IOCandidate, running map[string]ioUnit) []string {
	var out []string
	refuse := func(what string, fields []string, allowed map[string]bool) {
		if len(fields) > 0 {
			out = append(out, fmt.Sprintf("%s: %s change (live, only %s may)", what, strings.Join(fields, ", "), keyNames(allowed)))
		}
	}
	for _, k := range sortedKeys(c.units) {
		u, r := c.units[k], running[k]
		switch {
		case u.device != "":
			refuse("device "+u.device, changedFields(r.src, u.src, liveSource), liveSource)
		case liveDriverOf[u.typ] != nil:
			refuse("driver "+u.driver, changedFields(r.cfg, u.cfg, liveDriverOf[u.typ]), liveDriverOf[u.typ])
		}
	}
	// Modbus bindings, across each driver: a tag that moves to another
	// device changes its binding's source.
	bindings := func(units map[string]ioUnit, driver string) map[string]modbus.TagBinding {
		out := map[string]modbus.TagBinding{}
		for _, u := range units {
			if u.driver == driver && u.device != "" {
				for _, t := range u.tags {
					out[t.Name+"/"+fmt.Sprint(t.Writable)] = t
				}
			}
		}
		return out
	}
	for _, k := range sortedKeys(c.units) {
		u := c.units[k]
		if u.device != "" || u.typ != "modbus" {
			continue
		}
		was, now := bindings(running, u.driver), bindings(c.units, u.driver)
		for _, n := range sortedKeys(now) {
			if b, ok := was[n]; ok {
				refuse("tag "+now[n].Name, changedFields(b, now[n], liveModbus), liveModbus)
			}
		}
	}
	// OPC UA client bindings, by tag.
	for _, k := range sortedKeys(c.units) {
		u := c.units[k]
		if u.typ != "opcua-client" {
			continue
		}
		was, err1 := uaBindings(running[k])
		now, err2 := uaBindings(u)
		if err1 != nil || err2 != nil {
			continue // schema reports it
		}
		for _, n := range sortedKeys(now) {
			if b, ok := was[n]; ok {
				refuse("tag "+n, changedFields(b, now[n], liveOPCUA), liveOPCUA)
			}
		}
	}
	rows := map[string]devbind.Device{}
	for _, r := range io.bootRows {
		rows[r.Name] = r
	}
	for _, r := range c.m.Devices {
		if b, ok := rows[r.Name]; ok {
			refuse("devices: row "+r.Name, changedFields(b, r, liveRow), fieldSet("Endpoint", "UnitID", "Validate"))
		}
	}
	if len(out) > 0 {
		out = append(out, "restart to apply the rest")
	}
	return out
}

// uaBindings reads an OPC UA client unit's manifest, by tag.
func uaBindings(u ioUnit) (map[string]opcua.TagBinding, error) {
	m, err := opcuaManifest(u.fsys, u.cfg) // a devices: row's, derived from its profile
	if err != nil {
		return nil, err
	}
	out := map[string]opcua.TagBinding{}
	for _, t := range m.Tags {
		out[t.Name] = t
	}
	return out, nil
}

// newOutputConflicts reports the Modbus outputs the candidate binds twice at
// one point of one device (by register, or by bit for bit:N) that what runs
// does not: two tags would command one point. A device is its endpoint
// (host, port, unit id), not its source id: two sources on one endpoint
// write the same registers. Today's manifest check is by name only.
func newOutputConflicts(running, cand map[string]ioUnit) []string {
	before := map[string]bool{}
	for _, c := range outputConflicts(running) {
		before[c] = true
	}
	var out []string
	for _, c := range outputConflicts(cand) {
		if !before[c] {
			out = append(out, c)
		}
	}
	return out
}

func endpointOf(src modbus.Source) string {
	port := src.Port
	if port == 0 {
		port = 502
	}
	return fmt.Sprintf("%s:%d unit %d", strings.ToLower(src.Host), port, src.UnitID)
}

func outputConflicts(units map[string]ioUnit) []string {
	type claim struct {
		tag, device string
		mask        uint16 // the bits of the word it writes
	}
	var out []string
	words := map[string][]claim{}
	for _, k := range sortedKeys(units) {
		u := units[k]
		if u.device == "" {
			continue
		}
		ep := endpointOf(u.src)
		for _, t := range u.tags {
			if !t.Writable {
				continue
			}
			format := t.Format
			if format == "" && (t.Table == modbus.TableCoil || t.Table == modbus.TableDiscrete) {
				format = "bool" // the driver's default for a bit table
			}
			f, err := modbus.ParseFormat(format)
			if err != nil {
				continue // the manifest check reports it
			}
			n, mask := f.Words(), uint16(0xffff)
			if f.Kind == "bit" {
				mask = 1 << f.Bit
			}
			for w := 0; w < n; w++ {
				at := fmt.Sprintf("%s@%d on %s", t.Table, int(t.Address)+w, ep)
				for _, c := range words[at] {
					if c.mask&mask != 0 && c.tag != t.Name {
						out = append(out, fmt.Sprintf("outputs %s (device %s) and %s (device %s) both write %s", c.tag, c.device, t.Name, u.device, at))
					}
				}
				words[at] = append(words[at], claim{t.Name, u.device, mask})
			}
		}
	}
	return out
}

// willCommand lists the outputs the first scan after the apply would have
// the changed devices and drivers send, with the values they hold now. It
// follows the driver's own rule: modbus.HeldAtBaseline under a held swap,
// and the write gate and a parked source (modbus.Driver.Sending). why says
// what sends nothing.
func (io *IOConfig) willCommand(c *IOCandidate, changes []IOChange) (cmds []IOCommand, why []string) {
	names := map[string]bool{}
	modbusTags := func(mb *modbus.Driver, u ioUnit) {
		if mb != nil {
			if ok, reason := mb.Sending(u.device); !ok {
				why = append(why, fmt.Sprintf("device %s commands nothing now: %s", u.device, reason))
				return
			}
		}
		for _, t := range u.tags {
			if t.Writable && (!io.hold || !modbus.HeldAtBaseline(t)) {
				names[t.Name] = true
			}
		}
	}
	for _, ch := range changes {
		mb := modbusOf(io.currentDriver(ch.Driver))
		if ch.Device != "" {
			modbusTags(mb, c.units[unitKey(ch.Driver, ch.Device)])
			continue
		}
		if c.units[ch.Driver].typ == "modbus" {
			// The new driver takes the gates and enable tags the old one
			// answers to: ask the old one.
			for _, k := range sortedKeys(c.units) {
				if su := c.units[k]; su.driver == ch.Driver && su.device != "" {
					modbusTags(mb, su)
				}
			}
			continue
		}
		if io.Gate != nil && !io.Gate() {
			why = append(why, fmt.Sprintf("driver %s commands nothing now: the write gate is closed", ch.Driver))
			continue
		}
		if io.hold {
			why = append(why, fmt.Sprintf("driver %s: held (HoldOutputsOnSwap), its first commands are its baseline and are sent once they change", ch.Driver))
			continue
		}
		if o, ok := c.built[ch.Driver].(tagNames); ok {
			for _, n := range o.OutputNames() {
				names[n] = true
			}
		}
	}
	cmds = make([]IOCommand, 0, len(names))
	for _, n := range sortedNames(keysOf(names)) {
		val, _ := io.rt.Tags().ReadPath(n)
		cmds = append(cmds, IOCommand{Tag: n, Value: val})
	}
	return cmds, why
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Apply applies a valid candidate: each changed Modbus device in the driver
// that runs, then each changed driver swapped whole. It is all or nothing:
// a step that fails puts back the steps before it, and if putting back
// fails too the error wraps ErrRollbackFailed, names what is left, marks
// those units unknown (Differs) and sets Fault. by is who applies it.
//
// The result's Commanded is what the first scan after the apply handed the
// changed devices and drivers, waited for up to firstPushWait. Answered and
// Pending say which changed devices had read when it returned; a watch
// (IOConfig.Watch) follows the pending ones after, off the apply lock: a
// device still silent at its deadline stays applied, reads Bad and alarms,
// and the operator decides (#706, James's decision), unless the apply asked
// for ApplyOptions.RevertIfSilent.
func (io *IOConfig) Apply(ctx context.Context, c *IOCandidate, by string) (*IOResult, error) {
	return io.ApplyWith(ctx, c, by, ApplyOptions{})
}

// ApplyOptions are an apply's choices.
type ApplyOptions struct {
	// RevertIfSilent, when set, has the apply's watch put the whole apply
	// back if a changed device has not read by then: unless a newer apply
	// has landed meanwhile, or the revert was cancelled (CancelRevert). Off
	// by default: a reversion on a running plant can be its own surprise.
	RevertIfSilent time.Duration
}

// answerWait is how long an apply's watch waits for its changed devices
// to read when it is not asked to revert.
var answerWait = 5 * time.Second

// ApplyWith is Apply with options.
func (io *IOConfig) ApplyWith(ctx context.Context, c *IOCandidate, by string, opts ApplyOptions) (*IOResult, error) {
	io.mu.Lock()
	restoring, starting := io.restoring, io.Keep != nil && !io.edgeSeen
	io.mu.Unlock()
	switch {
	case restoring:
		return nil, fmt.Errorf("%w: the kept I/O configuration is being re-applied", ErrApplyBusy)
	case starting:
		return nil, fmt.Errorf("%w: the controller has not yet re-applied its kept I/O configuration (it is starting, or stands by)", ErrApplyBusy)
	}
	if !io.applying.TryLock() {
		return nil, ErrApplyBusy
	}
	defer io.applying.Unlock()
	return io.apply(ctx, c, by, opts)
}

// apply is ApplyWith under the apply lock. A restore candidate (RestoreIO)
// keeps each unit's recorded origin and neither waits for a scan nor
// starts a watch: the scan is held until it returns.
func (io *IOConfig) apply(ctx context.Context, c *IOCandidate, by string, opts ApplyOptions) (*IOResult, error) {
	c.mu.Lock()
	val, base := c.val, c.base
	c.mu.Unlock()
	if val == nil {
		return nil, errors.New("validate the candidate before applying it")
	}
	if !val.Valid {
		return nil, errors.New("the candidate is not valid; it cannot be applied")
	}
	io.mu.Lock()
	stale := len(base) != len(io.running)
	for k, u := range io.running {
		if base[k] != u.hash {
			stale = true
		}
	}
	running := make(map[string]ioUnit, len(io.running))
	for k, u := range io.running {
		running[k] = u
	}
	io.mu.Unlock()
	if stale {
		return nil, ErrIOStale
	}

	type undoStep struct {
		what string   // "device DEV_A" / "driver opcua-client"
		keys []string // the units it leaves unknown if it fails
		run  func() error
	}
	var undo []undoStep
	fail := func(ch IOChange, what string, err error) (*IOResult, error) {
		io.log(ch, c, by, err)
		var left []string
		var lost []string
		for i := len(undo) - 1; i >= 0; i-- {
			if uerr := undo[i].run(); uerr != nil {
				left = append(left, fmt.Sprintf("%s (%v)", undo[i].what, uerr))
				lost = append(lost, undo[i].keys...)
			}
		}
		if len(left) == 0 {
			return nil, fmt.Errorf("%s: %w (nothing was changed)", what, err)
		}
		msg := fmt.Sprintf("%s: %v; putting back failed for %s: what runs there is not known; apply a candidate for it, or restart", what, err, strings.Join(left, ", "))
		io.markUnknown(lost, by, msg)
		slog.Error("config.apply: rollback failed", "err", msg)
		return nil, fmt.Errorf("%w: %s", ErrRollbackFailed, msg)
	}

	type share struct {
		what string
		ch   <-chan modbus.FirstShare
	}
	var shares []share
	type push struct {
		name string
		sw   *swapped
	}
	var pushes []push

	for _, ch := range val.Changes {
		if ch.Device == "" {
			continue
		}
		ch := ch
		k := unitKey(ch.Driver, ch.Device)
		what := "device " + ch.Device
		mb := modbusOf(io.currentDriver(ch.Driver))
		if mb == nil {
			return fail(ch, what, fmt.Errorf("no running Modbus driver %s", ch.Driver))
		}
		next, prev := c.units[k], running[k]
		got := make(chan modbus.FirstShare, 1)
		opts := modbus.ReplaceOptions{HoldOutputs: io.hold, Observe: func(s modbus.FirstShare) { got <- s }}
		if c.restore {
			// A restore's first share follows the driver's own rule, as a
			// boot's does: its start-up baseline at a standalone start, the
			// full set at a takeover (a standby has no baseline, #606).
			opts.HoldOutputs = false
		}
		if err := io.replaceSource(mb, next.src, next.tags, opts); err != nil {
			return fail(ch, what, err)
		}
		shares = append(shares, share{what, got})
		undo = append(undo, undoStep{what, []string{k}, func() error {
			return io.replaceSource(mb, prev.src, prev.tags, modbus.ReplaceOptions{HoldOutputs: io.hold})
		}})
	}
	for _, ch := range val.Changes {
		if ch.Device != "" {
			continue
		}
		ch := ch
		what := "driver " + ch.Driver
		prev, next := running[ch.Driver], c.units[ch.Driver]
		sw, err := io.swapDriver(ctx, ch.Driver, next.fsys, next.cfg)
		if err != nil {
			return fail(ch, what, err)
		}
		pushes = append(pushes, push{ch.Driver, sw})
		var keys []string
		for k, u := range running {
			if u.driver == ch.Driver {
				keys = append(keys, k)
			}
		}
		undo = append(undo, undoStep{what, keys, func() error {
			uctx, cancel := context.WithTimeout(context.Background(), undoTimeout)
			defer cancel()
			_, err := io.swapDriver(uctx, ch.Driver, prev.fsys, prev.cfg)
			return err
		}})
	}

	// What runs is now the candidate, unit for unit: its files are what a
	// later swap rebuilds a driver from (or puts one back from), so even a
	// unit whose hash did not change takes the candidate's. Only a changed
	// unit takes this apply's origin.
	at := time.Now()
	io.mu.Lock()
	prevKept, prevProv := io.kept, io.provisional
	for k, u := range c.units {
		switch prev := io.running[k]; {
		case c.restore:
			// A restore's units carry the origin they were kept with.
		case u.hash == prev.hash:
			u.from, u.at, u.by = prev.from, prev.at, prev.by
		default:
			u.from, u.at, u.by = c.From, at, by
		}
		io.running[k] = u
		delete(io.faults, k) // what runs there is known again
	}
	io.gen++
	gen := io.gen
	// What the store keeps: what runs now, unless the apply may still be
	// reverted (decision recorded on #706: a pending revert is not carried
	// across a restart or takeover, which come back on what ran before it).
	switch {
	case c.restore:
		io.kept = copyUnits(io.running)
	case opts.RevertIfSilent > 0:
		io.provisional = &retain.IOProvisional{Gen: gen, From: c.From, By: by, At: at,
			Deadline: at.Add(opts.RevertIfSilent)}
		io.changed++
	default:
		io.kept, io.provisional = copyUnits(io.running), nil
		io.changed++
	}
	if !c.restore {
		io.held = nil // a full candidate replaced every unit that was held
	}
	io.mu.Unlock()
	for _, ch := range val.Changes {
		io.log(ch, c, by, nil)
	}
	if c.restore {
		return &IOResult{Changes: val.Changes, Commanded: []IOCommand{}}, nil
	}

	// Durable before it is answered: a takeover right after must find it.
	kept, keptWhy := io.keepNow(opts.RevertIfSilent > 0)
	if !kept && opts.RevertIfSilent == 0 && io.p.Redundancy != nil && io.Keep != nil {
		// On a redundant project a change the store does not hold would be
		// undone by the next takeover: put it back now, and say so.
		var left, lost []string
		for i := len(undo) - 1; i >= 0; i-- {
			if uerr := undo[i].run(); uerr != nil {
				left = append(left, fmt.Sprintf("%s (%v)", undo[i].what, uerr))
				lost = append(lost, undo[i].keys...)
			}
		}
		io.mu.Lock()
		for k := range c.units {
			if u, ok := running[k]; ok {
				io.running[k] = u
			}
		}
		io.kept, io.provisional = prevKept, prevProv
		io.gen++
		io.changed++ // the put-back's save is newer than anything the failed one may leave
		io.mu.Unlock()
		msg := "the apply was not kept in the retain store (" + keptWhy + "), so a takeover would bring back the old configuration"
		// The put-back is saved too: behind the failed save in the store's
		// one writer, and by compare-and-swap, so it is what the store holds
		// last, whatever the failed one did.
		pctx, pcancel := context.WithTimeout(context.Background(), putBackWait)
		perr := io.Keep(pctx)
		pcancel()
		if perr != nil {
			msg += "; saving the put-back failed too (" + perr.Error() + "): the store may still hold the apply until the next save"
		}
		if len(left) > 0 {
			msg = fmt.Sprintf("%s; putting it back failed for %s: what runs there is not known; apply a candidate for it, or restart", msg, strings.Join(left, ", "))
			io.markUnknown(lost, by, msg)
			slog.Error("config.apply: not kept, and putting it back failed", "err", msg)
			return nil, fmt.Errorf("%w: %s", ErrRollbackFailed, msg)
		}
		slog.Warn("config.apply", "result", "put back", "reason", msg, "by", by)
		return nil, fmt.Errorf("%w: %s; it was put back", ErrNotKept, msg)
	}

	res := &IOResult{Changes: val.Changes, Commanded: []IOCommand{}, Kept: kept, KeptWhy: keptWhy,
		Notes: []string{"applied; each changed device was probed before the apply where its check is live (validation.probes)"}}
	wait, cancel := context.WithTimeout(ctx, firstPushWait)
	defer cancel()
	cmds := map[string]any{}
	took := func(what string, s modbus.FirstShare) {
		switch {
		case s.GateClosed:
			res.Notes = append(res.Notes, what+": the write gate was closed, so the first scan after the apply commanded nothing")
		case s.Parked:
			res.Notes = append(res.Notes, what+": parked by its enable tag; its commands wait until it is enabled")
		}
		for n, v := range s.Queued {
			cmds[n] = v
		}
	}
	for _, sh := range shares {
		select {
		case s := <-sh.ch:
			took(sh.what, s)
		case <-wait.Done():
			res.Notes = append(res.Notes, sh.what+": no scan has handed it its commands yet")
		}
	}
	for _, p := range pushes {
		if p.sw.shares != nil {
			// A Modbus driver: each source's first share, as it decided it.
			for i := 0; i < p.sw.nsrc; i++ {
				select {
				case s := <-p.sw.shares:
					took("device "+s.id, s.FirstShare)
				case <-wait.Done():
					res.Notes = append(res.Notes, "driver "+p.name+": no scan has handed every device its commands yet")
					i = p.sw.nsrc
				}
			}
			continue
		}
		select {
		case vals := <-p.sw.pushed:
			switch {
			case io.Gate != nil && !io.Gate():
				res.Notes = append(res.Notes, "driver "+p.name+": the write gate is closed, so the first scan after the apply commanded nothing")
			case io.hold:
				res.Notes = append(res.Notes, "driver "+p.name+": held, its first commands are its baseline")
			default:
				for n, v := range vals {
					cmds[n] = v
				}
			}
		case <-wait.Done():
			res.Notes = append(res.Notes, "driver "+p.name+": no scan has handed it its commands yet")
		}
	}
	for _, n := range sortedNames(keysOf2(cmds)) {
		res.Commanded = append(res.Commanded, IOCommand{Tag: n, Value: cmds[n]})
	}

	// What the changed devices say from here is the watch's: it runs on a
	// context of its own (a client that goes away must never make a
	// silence, nor a revert) and the apply lock is given back now.
	deadline := answerWait
	if opts.RevertIfSilent > 0 {
		deadline = opts.RevertIfSilent
	}
	w := &ioWatch{
		IOWatch: IOWatch{Gen: gen, From: c.From, By: by, Deadline: time.Now().Add(deadline),
			Revert: opts.RevertIfSilent > 0, State: WatchWatching},
		targets: io.answerTargets(c, val.Changes, running),
		revert: func() (left, lost []string) {
			for i := len(undo) - 1; i >= 0; i-- {
				if uerr := undo[i].run(); uerr != nil {
					left = append(left, fmt.Sprintf("%s (%v)", undo[i].what, uerr))
					lost = append(lost, undo[i].keys...)
				}
			}
			return left, lost
		},
		restore: func() {
			for k := range c.units {
				if u, ok := running[k]; ok {
					io.running[k] = u
				}
			}
		},
		onEnd: io.watchEnded,
	}
	w.answer()
	res.Answered, res.Pending = w.Answered, w.Pending
	io.startWatch(w)
	snap := w.snapshot()
	res.Watch = &snap
	return res, nil
}

// The watch's states.
const (
	WatchWatching   = "watching"      // devices still pending, before the deadline
	WatchAnswered   = "answered"      // every changed device read
	WatchSilent     = "silent"        // one did not by the deadline; the change stays
	WatchReverted   = "reverted"      // one did not, and the apply was put back
	WatchReverting  = "reverting"     // putting it back: a cancel is refused from here
	WatchRevertFail = "revert-failed" // putting it back failed (Fault)
	WatchCancelled  = "cancelled"     // CancelRevert: the revert will not happen
	WatchSuperseded = "superseded"    // a newer apply landed; this one is not reverted
)

// IOWatch is the last apply's watch over its changed devices: which have
// read, which are pending, by when, and whether it will revert.
//
// It is held in this process's memory only, and a pending revert is not
// carried across a restart or a takeover: the apply it would revert is not
// kept in the retain store until the watch settles (answered or
// cancelled), so either comes back on the configuration before it
// (iokeep.go; decision recorded on #706).
type IOWatch struct {
	Gen      int       `json:"gen"` // the apply generation it watches
	From     string    `json:"from"`
	By       string    `json:"by"`
	Deadline time.Time `json:"deadline"`
	Revert   bool      `json:"revert"` // reverts the apply if a device is still silent at the deadline
	Answered []string  `json:"answered,omitempty"`
	Pending  []string  `json:"pending,omitempty"`
	Silent   []string  `json:"silent,omitempty"`
	State    string    `json:"state"`
	Reason   string    `json:"reason,omitempty"`
	Ended    time.Time `json:"ended,omitzero"` // when it left watching
}

type answerTarget struct {
	what    string
	names   []string
	quality func() map[string]nio.Quality
}

type ioWatch struct {
	mu sync.Mutex
	IOWatch
	targets []answerTarget
	read    []map[string]bool
	revert  func() (left, lost []string) // under the apply lock
	restore func()                       // under io.mu
	stop    context.CancelFunc
	done    chan struct{}   // closed when runWatch returns
	onEnd   func(w IOWatch) // once, when it leaves watching for good
}

func (w *ioWatch) snapshot() IOWatch {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.IOWatch
	s.Answered = append([]string(nil), s.Answered...)
	s.Pending = append([]string(nil), s.Pending...)
	s.Silent = append([]string(nil), s.Silent...)
	return s
}

// answer looks once at the drivers that run and moves the targets whose
// every tag has read to Answered.
func (w *ioWatch) answer() {
	if w.read == nil {
		w.read = make([]map[string]bool, len(w.targets))
		for i := range w.read {
			w.read[i] = map[string]bool{}
		}
	}
	var answered, pending []string
	for i, tg := range w.targets {
		q := tg.quality()
		all := true
		for _, n := range tg.names {
			if qq, bad := q[n]; !bad || qq.IsGood() {
				w.read[i][n] = true
			}
			all = all && w.read[i][n]
		}
		if all {
			answered = append(answered, tg.what)
		} else {
			pending = append(pending, tg.what)
		}
	}
	w.mu.Lock()
	w.Answered, w.Pending = answered, pending
	w.mu.Unlock()
}

// answerTargets are the changed devices, each with its data tags and the
// quality the driver that runs reports for them.
func (io *IOConfig) answerTargets(c *IOCandidate, changes []IOChange, running map[string]ioUnit) []answerTarget {
	var targets []answerTarget
	modbusTarget := func(driver string, u ioUnit) answerTarget {
		var names []string
		for _, t := range u.tags {
			if !t.WriteOnly {
				names = append(names, t.Name)
			}
		}
		return answerTarget{"device " + u.device, names, func() map[string]nio.Quality {
			if mb := modbusOf(io.currentDriver(driver)); mb != nil {
				return mb.Quality()
			}
			return nil
		}}
	}
	for _, ch := range changes {
		switch {
		case ch.Device != "":
			targets = append(targets, modbusTarget(ch.Driver, c.units[unitKey(ch.Driver, ch.Device)]))
		case c.units[ch.Driver].typ == "modbus":
			for _, k := range sortedKeys(c.units) {
				// Only a device the swap changed: one that was already
				// silent is not this apply's to answer for (nor to revert).
				if u := c.units[k]; u.driver == ch.Driver && u.device != "" && u.hash != running[k].hash {
					targets = append(targets, modbusTarget(ch.Driver, u))
				}
			}
		default:
			name := ch.Driver
			ua := opcuaOf(io.currentDriver(name))
			if ua == nil {
				continue
			}
			var names []string
			for _, n := range ua.InputNames() {
				if n != ua.OnlineTagName() {
					names = append(names, n)
				}
			}
			targets = append(targets, answerTarget{"driver " + name, names, func() map[string]nio.Quality {
				if ua := opcuaOf(io.currentDriver(name)); ua != nil {
					return ua.Quality()
				}
				return nil
			}})
		}
	}
	return targets
}

// startWatch makes w the watch, superseding the one before, and runs it on
// a context of the engine's own.
func (io *IOConfig) startWatch(w *ioWatch) {
	ctx, cancel := context.WithCancel(context.Background())
	w.stop = cancel
	w.done = make(chan struct{})
	io.mu.Lock()
	prev := io.watch
	io.watch = w
	io.mu.Unlock()
	if prev != nil {
		prev.finish(WatchSuperseded, "a newer apply landed")
	}
	go io.runWatch(ctx, w)
}

// finish ends w in state (when it is still watching, or reverting) and
// stops it.
func (w *ioWatch) finish(state, reason string) {
	w.mu.Lock()
	ended := w.State == WatchWatching || w.State == WatchReverting
	if ended {
		w.State, w.Reason, w.Ended = state, reason, time.Now()
	}
	w.mu.Unlock()
	if w.stop != nil {
		w.stop()
	}
	if ended && w.onEnd != nil {
		w.onEnd(w.snapshot())
	}
}

func (io *IOConfig) runWatch(ctx context.Context, w *ioWatch) {
	defer close(w.done)
	defer w.stop()
	for {
		w.answer()
		w.mu.Lock()
		pending, deadline := len(w.Pending), w.Deadline
		w.mu.Unlock()
		if pending == 0 {
			w.finish(WatchAnswered, "")
			return
		}
		if !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return // superseded or cancelled: whoever stopped it said why
		case <-time.After(20 * time.Millisecond):
		}
	}
	// The deadline: silence is what the drivers that run say now.
	w.answer()
	snap := w.snapshot()
	if len(snap.Pending) == 0 {
		w.finish(WatchAnswered, "")
		return
	}
	silent := strings.Join(snap.Pending, ", ")
	w.mu.Lock()
	w.Silent = snap.Pending
	w.mu.Unlock()
	if !snap.Revert {
		w.finish(WatchSilent, silent+" did not read by the deadline: the change stays, its tags read Bad")
		return
	}
	// Revert: exclusive with any apply, and only over this apply's own
	// generation (a newer one has landed otherwise).
	io.applying.Lock()
	defer io.applying.Unlock()
	if ctx.Err() != nil {
		return // cancelled or superseded while waiting for the lock
	}
	io.mu.Lock()
	gen := io.gen
	io.mu.Unlock()
	if gen != snap.Gen {
		w.finish(WatchSuperseded, "a newer apply landed; not reverted")
		return
	}
	// Only the leader reverts: a replica that stepped down runs what the
	// store keeps once it follows it, and the pending apply was never kept.
	if io.Gate != nil && !io.Gate() {
		w.finish(WatchSuperseded, "this controller no longer leads: not reverted here (the apply was not kept, so the leader runs the configuration before it)")
		return
	}
	// Silence once more, now that nothing else can change what runs: a
	// device that answered while this waited for the lock is not silent.
	w.answer()
	snap = w.snapshot()
	if len(snap.Pending) == 0 {
		w.mu.Lock()
		w.Silent = nil // it is not silent after all
		w.mu.Unlock()
		w.finish(WatchAnswered, "")
		return
	}
	silent = strings.Join(snap.Pending, ", ")
	// Reverting: a cancel is refused from here on, and the record never
	// says cancelled for a revert that ran.
	w.mu.Lock()
	if w.State != WatchWatching {
		w.mu.Unlock()
		return // cancelled just before
	}
	w.State, w.Silent = WatchReverting, snap.Pending
	w.mu.Unlock()
	reason := silent + " did not read by the deadline (revertIfSilent)"
	left, lost := w.revert()
	if len(left) > 0 {
		msg := fmt.Sprintf("%s; putting it back failed for %s: what runs there is not known; apply a candidate for it, or restart", reason, strings.Join(left, ", "))
		io.markUnknown(lost, snap.By, msg)
		slog.Error("config.apply: revert failed", "err", msg)
		w.finish(WatchRevertFail, msg)
		return
	}
	io.mu.Lock()
	w.restore()
	io.gen++
	io.mu.Unlock()
	slog.Warn("config.apply", "result", "reverted", "reason", reason, "by", snap.By)
	w.finish(WatchReverted, reason)
}

// Watch is the last apply's watch, nil before any apply.
func (io *IOConfig) Watch() *IOWatch {
	io.mu.Lock()
	w := io.watch
	io.mu.Unlock()
	if w == nil {
		return nil
	}
	s := w.snapshot()
	return &s
}

// ErrNoRevert refuses a cancel: no revert is pending (none was asked for,
// it is under way, or it has happened). Its message says which.
var ErrNoRevert = errors.New("no revert is pending")

// CancelRevert stops a pending revert: the watch ends cancelled, and the
// apply stays whatever its devices say. A revert already under way, or
// done, is not cancelled: the error says so.
func (io *IOConfig) CancelRevert(by string) (*IOWatch, error) {
	io.mu.Lock()
	w := io.watch
	io.mu.Unlock()
	if w == nil {
		return nil, fmt.Errorf("%w: no apply has been made", ErrNoRevert)
	}
	w.mu.Lock()
	switch {
	case w.State == WatchWatching && w.Revert:
		w.State, w.Reason, w.Ended = WatchCancelled, "the revert was cancelled by "+by, time.Now()
		w.mu.Unlock()
		w.stop()
		s := w.snapshot()
		if w.onEnd != nil {
			w.onEnd(s)
		}
		return &s, nil
	case w.State == WatchReverting:
		w.mu.Unlock()
		return nil, fmt.Errorf("%w: a revert is in progress and cannot be stopped", ErrNoRevert)
	case w.State == WatchReverted || w.State == WatchRevertFail:
		state, at, reason := w.State, w.Ended, w.Reason
		w.mu.Unlock()
		return nil, fmt.Errorf("%w: already %s at %s (%s)", ErrNoRevert, state, at.Format(time.RFC3339), reason)
	}
	state, revert := w.State, w.Revert
	w.mu.Unlock()
	if !revert {
		return nil, fmt.Errorf("%w: the last apply did not ask to revert", ErrNoRevert)
	}
	return nil, fmt.Errorf("%w: the last apply's watch is %s", ErrNoRevert, state)
}

// Close stops the watch (a test, or a controller shutting down). A revert
// already under way is not cut short: Close waits for it, up to
// undoTimeout. Were it cut short anyway, nothing is lost: the store never
// held the apply it reverts, so a restart comes back on what ran before.
func (io *IOConfig) Close() {
	io.closeOnce.Do(func() {
		if io.closed != nil {
			close(io.closed)
		}
	})
	io.mu.Lock()
	w := io.watch
	io.mu.Unlock()
	if w == nil || w.stop == nil {
		return
	}
	w.stop()
	if w.done == nil {
		return
	}
	select {
	case <-w.done:
	case <-time.After(undoTimeout):
		slog.Warn("config.apply: closing while a revert is still under way")
	}
}

func keysOf2(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// markUnknown marks units whose configuration a failed rollback left
// unknown, with why (Fault).
func (io *IOConfig) markUnknown(keys []string, by, why string) {
	io.mu.Lock()
	defer io.mu.Unlock()
	if io.faults == nil {
		io.faults = map[string]string{}
	}
	for _, k := range keys {
		u := io.running[k]
		u.hash, u.from, u.at, u.by = unknownHash, "rollback failed", time.Now(), by
		io.running[k] = u
		io.faults[k] = why
	}
}

// Fault is what failed rollbacks left unknown and no apply since has
// re-established, "" when nothing.
func (io *IOConfig) Fault() string {
	io.mu.Lock()
	defer io.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, k := range sortedKeys(io.faults) {
		if why := io.faults[k]; !seen[why] {
			seen[why] = true
			out = append(out, why)
		}
	}
	return strings.Join(out, "; ")
}

// swap builds the driver cfg describes, gates and starts it, swaps it in for
// the one named name and stops that one. It returns the new driver and the
// outputs the first push after the swap hands it.
func (io *IOConfig) swap(ctx context.Context, name string, fsys fs.FS, cfg DriverConfig) (*swapped, error) {
	set, ok := io.p.Runtime.Driver.(*nio.Multi)
	if !ok {
		return nil, errors.New("the runtime's driver is not a composite")
	}
	d, err := buildDriver(fsys, cfg)
	if err != nil {
		return nil, err
	}
	sw := &swapped{d: d}
	if mb := modbusOf(d); mb != nil {
		// One slot per source, so a share never blocks the scan that reports
		// it: d is not swapped in yet, so its sources cannot change before
		// ObserveFirstShares arms them.
		ch := make(chan srcShare, len(mb.Health().Sources))
		sw.nsrc = mb.ObserveFirstShares(func(id string, s modbus.FirstShare) { ch <- srcShare{id, s} })
		sw.shares = ch
	}
	set.Gate(d)
	if s, ok := d.(interface{ Start(context.Context) }); ok {
		s.Start(context.Background()) // runs until Stop, as naut run starts drivers
	}
	var prev nio.Driver
	var pushed <-chan nio.Values
	if io.atStart {
		prev, err = io.rt.ReplaceDriverAtStart(ctx, name, d)
	} else {
		prev, pushed, err = io.rt.ReplaceDriverObserved(ctx, name, d)
	}
	if err != nil {
		stopDriver(d)
		return nil, err
	}
	sw.pushed = pushed
	stopDriver(prev)
	return sw, nil
}

func stopDriver(d nio.Driver) {
	if s, ok := d.(interface{ Stop() }); ok {
		s.Stop()
	}
}

func (io *IOConfig) log(ch IOChange, c *IOCandidate, by string, err error) {
	args := []any{"driver", ch.Driver, "before", ch.Before, "after", ch.After, "from", c.From, "by", by}
	if ch.Device != "" {
		args = append(args, "device", ch.Device)
	}
	if err != nil {
		slog.Warn("config.apply", append(args, "result", "failed", "err", err.Error())...)
		return
	}
	slog.Info("config.apply", append(args, "result", "applied")...)
}

// currentDriver is the driver named name that runs now.
func (io *IOConfig) currentDriver(name string) nio.Driver { return io.p.currentDriver(name) }

// currentDriver is the driver named name that runs now: the composite's
// child (an apply may have replaced it), else the one Load built.
func (p *Project) currentDriver(name string) nio.Driver {
	if m, ok := p.Runtime.Driver.(*nio.Multi); ok {
		if d, ok := m.Child(name); ok {
			return d
		}
	}
	for _, nd := range p.drivers {
		if nd.Name == name {
			return nd.Driver
		}
	}
	return nil
}

// modbusOf is the Modbus driver d is or wraps (devbind.ModbusDriver).
func modbusOf(d nio.Driver) *modbus.Driver {
	for d != nil {
		if mb, ok := d.(*modbus.Driver); ok {
			return mb
		}
		u, ok := d.(interface{ Unwrap() nio.Driver })
		if !ok {
			return nil
		}
		d = u.Unwrap()
	}
	return nil
}

// sameDriver reports whether a and b are the same driver value, without
// panicking on a type that cannot be compared.
func sameDriver(a, b nio.Driver) bool {
	ta := reflect.TypeOf(a)
	if ta != reflect.TypeOf(b) || ta == nil || !ta.Comparable() {
		return false
	}
	return a == b
}

// IOUnitState is one unit's configuration: what the manifest the runtime
// booted from says, and what runs.
type IOUnitState struct {
	Driver       string    `json:"driver"`
	Device       string    `json:"device,omitempty"`
	ManifestHash string    `json:"manifestHash"`
	RunningHash  string    `json:"runningHash"`
	AppliedFrom  string    `json:"appliedFrom,omitempty"` // a commit, or "upload"
	At           time.Time `json:"at,omitzero"`
	By           string    `json:"by,omitempty"`
}

// Running is every unit's state, sorted.
func (io *IOConfig) Running() []IOUnitState {
	io.mu.Lock()
	defer io.mu.Unlock()
	out := make([]IOUnitState, 0, len(io.running))
	for _, k := range sortedKeys(io.running) {
		u := io.running[k]
		out = append(out, IOUnitState{
			Driver: u.driver, Device: u.device, ManifestHash: io.boot[k].hash, RunningHash: u.hash,
			AppliedFrom: u.from, At: u.at, By: u.by,
		})
	}
	return out
}

// Differs is the units whose running configuration is not the manifest's:
// empty when what runs is exactly what the manifest says.
func (io *IOConfig) Differs() []IOUnitState {
	var out []IOUnitState
	for _, s := range io.Running() {
		if s.RunningHash != s.ManifestHash {
			out = append(out, s)
		}
	}
	return out
}

// OverlayFS is base with files laid over it, by project-relative path: an
// uploaded candidate (#711's configuration screens). An uploaded file
// replaces the project's; a directory listing does not see a new one.
func OverlayFS(base fs.FS, files map[string][]byte) fs.FS {
	m := overlay{base: base, files: map[string][]byte{}}
	for p, b := range files {
		m.files[path.Clean(p)] = b
	}
	return m
}

type overlay struct {
	base  fs.FS
	files map[string][]byte
}

func (o overlay) Open(name string) (fs.File, error) {
	if b, ok := o.files[name]; ok {
		return fstest.MapFS{name: &fstest.MapFile{Data: b}}.Open(name)
	}
	return o.base.Open(name)
}
