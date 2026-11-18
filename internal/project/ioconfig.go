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
// configuration across a restart or a takeover is PR 5, and until then an
// apply is refused on a redundant project.

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
	"github.com/joyautomation/nautilus/runtime"
)

// IORuntime is what an apply needs of the running runtime.
type IORuntime interface {
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

	// replaceSource is modbus.(*Driver).ReplaceSource; a test makes it fail.
	replaceSource func(mb *modbus.Driver, src modbus.Source, tags []modbus.TagBinding, opts modbus.ReplaceOptions) error
}

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
	return &IOConfig{
		p: p, rt: rt, manifest: manifest, hold: p.Runtime.HoldOutputsOnSwap,
		bootYAML: nonIOYAML(m), bootRows: m.Devices, bootST: types, boot: units, running: running,
		replaceSource: func(mb *modbus.Driver, src modbus.Source, tags []modbus.TagBinding, opts modbus.ReplaceOptions) error {
			return mb.ReplaceSource(src, tags, opts)
		},
	}, nil
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
	changes []IOChange
	val     *IOValidation
	base    map[string]string // the running hashes it was validated against
}

// Validation is the last Validate's result, nil before one.
func (c *IOCandidate) Validation() *IOValidation { return c.val }

// Stage reads a candidate I/O configuration from fsys: a commit's file
// snapshot, or the running project's files with uploads laid over them
// (OverlayFS). from is the commit, or "upload"; by is who staged it. A
// candidate that does not load is staged anyway and fails validation.
func (io *IOConfig) Stage(fsys fs.FS, from, by string) *IOCandidate {
	c := &IOCandidate{From: from, By: by, fsys: fsys}
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
	finish := func() *IOValidation {
		v.Checks = append(v.Checks, IOCheck{Name: "live", Skipped: true, OK: true,
			Detail: "not run: probing a changed device before the apply comes with #706 PR 4"})
		v.Valid = true
		for _, ck := range v.Checks {
			v.Valid = v.Valid && ck.OK
		}
		c.val = v
		c.base = map[string]string{}
		for k, u := range running {
			c.base[k] = u.hash
		}
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

	// Redundancy: nothing keeps an applied configuration yet, so a standby
	// that takes over would run the manifest's.
	if io.p.Redundancy != nil {
		check("redundancy", []string{"a live I/O apply is not yet kept across a takeover (#706 PR 5), so a standby would come up on the old addresses; deploy the change instead"})
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
	for _, ch := range v.Changes {
		if ch.Device == "" {
			continue
		}
		k := unitKey(ch.Driver, ch.Device)
		if a, b := bindingNames(running[k].tags), bindingNames(c.units[k].tags); !slices.Equal(a, b) {
			tagp = append(tagp, fmt.Sprintf("device %s binds %v, not %v: a tag cannot move between devices live", ch.Device, b, a))
		}
	}
	if len(tagp) > 0 {
		tagp = append(tagp, "the tag set is fixed while the runtime runs: restart, or activate a program")
	}
	check("tags", tagp)

	check("device-type", io.deviceTypeProblems(c))
	check("outputs", newOutputConflicts(running, c.units))

	if nonIOYAML(c.m) != io.bootYAML {
		v.Notes = append(v.Notes, "the candidate's manifest changes more than its I/O; only drivers:, devices: and the files they name are applied (activate a program, or deploy, for the rest)")
	}
	v.Held = io.hold
	v.WillCommand = io.willCommand(c, v.Changes)
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

func bindingNames(tags []modbus.TagBinding) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		out = append(out, t.Name+"/"+fmt.Sprint(t.Writable))
	}
	sort.Strings(out)
	return out
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

// newOutputConflicts reports the Modbus outputs the candidate binds twice at
// one address (by register, or by bit for bit:N) that what runs does not:
// two tags would command one point. Today's manifest check is by name only.
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

func outputConflicts(units map[string]ioUnit) []string {
	type claim struct {
		tag  string
		mask uint16 // the bits of the word it writes
	}
	var out []string
	for _, k := range sortedKeys(units) {
		u := units[k]
		if u.device == "" {
			continue
		}
		words := map[string][]claim{}
		for _, t := range u.tags {
			if !t.Writable {
				continue
			}
			f, err := modbus.ParseFormat(t.Format)
			if err != nil {
				continue
			}
			n, mask := f.Words(), uint16(0xffff)
			if f.Kind == "bit" {
				mask = 1 << f.Bit
			}
			for w := 0; w < n; w++ {
				at := fmt.Sprintf("%s@%d", t.Table, int(t.Address)+w)
				for _, c := range words[at] {
					if c.mask&mask != 0 && c.tag != t.Name {
						out = append(out, fmt.Sprintf("device %s: outputs %s and %s both write %s", u.device, c.tag, t.Name, at))
					}
				}
				words[at] = append(words[at], claim{t.Name, mask})
			}
		}
	}
	return out
}

// willCommand lists the outputs the first scan after the apply sends to
// what changes, with the values they hold now.
func (io *IOConfig) willCommand(c *IOCandidate, changes []IOChange) []IOCommand {
	names := map[string]bool{}
	for _, ch := range changes {
		if ch.Device != "" {
			for _, t := range c.units[unitKey(ch.Driver, ch.Device)].tags {
				if t.Writable && (!io.hold || t.Rewrite > 0) {
					names[t.Name] = true
				}
			}
			continue
		}
		u := c.units[ch.Driver]
		if u.typ == "modbus" {
			for _, su := range c.units {
				if su.driver != ch.Driver || su.device == "" {
					continue
				}
				for _, t := range su.tags {
					if t.Writable && (!io.hold || t.Rewrite > 0) {
						names[t.Name] = true
					}
				}
			}
			continue
		}
		if io.hold {
			continue
		}
		if o, ok := c.built[ch.Driver].(tagNames); ok {
			for _, n := range o.OutputNames() {
				names[n] = true
			}
		}
	}
	out := make([]IOCommand, 0, len(names))
	for _, n := range sortedNames(keysOf(names)) {
		val, _ := io.rt.Tags().ReadPath(n)
		out = append(out, IOCommand{Tag: n, Value: val})
	}
	return out
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
// a step that fails puts back the steps before it. by is who applies it.
func (io *IOConfig) Apply(ctx context.Context, c *IOCandidate, by string) error {
	if !io.applying.TryLock() {
		return ErrApplyBusy
	}
	defer io.applying.Unlock()
	if c.val == nil {
		return errors.New("validate the candidate before applying it")
	}
	if !c.val.Valid {
		return errors.New("the candidate is not valid; it cannot be applied")
	}
	io.mu.Lock()
	stale := len(c.base) != len(io.running)
	for k, u := range io.running {
		if c.base[k] != u.hash {
			stale = true
		}
	}
	running := make(map[string]ioUnit, len(io.running))
	for k, u := range io.running {
		running[k] = u
	}
	io.mu.Unlock()
	if stale {
		return ErrIOStale
	}

	var undo []func()
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
	opts := modbus.ReplaceOptions{HoldOutputs: io.hold}
	for _, ch := range c.val.Changes {
		if ch.Device == "" {
			continue
		}
		k := unitKey(ch.Driver, ch.Device)
		mb := modbusOf(io.currentDriver(ch.Driver))
		if mb == nil {
			rollback()
			return fmt.Errorf("driver %s: no running Modbus driver to apply device %s to", ch.Driver, ch.Device)
		}
		next, prev := c.units[k], running[k]
		if err := io.replaceSource(mb, next.src, next.tags, opts); err != nil {
			rollback()
			io.log(ch, c, by, err)
			return fmt.Errorf("device %s: %w (nothing was changed)", ch.Device, err)
		}
		undo = append(undo, func() {
			if err := io.replaceSource(mb, prev.src, prev.tags, opts); err != nil {
				slog.Error("config.apply: putting a device back failed", "driver", ch.Driver, "device", ch.Device, "err", err)
			}
		})
	}
	for _, ch := range c.val.Changes {
		if ch.Device != "" {
			continue
		}
		prev := running[ch.Driver]
		if err := io.swap(ctx, ch.Driver, c.fsys, c.configs[ch.Driver]); err != nil {
			rollback()
			io.log(ch, c, by, err)
			return fmt.Errorf("driver %s: %w (nothing was changed)", ch.Driver, err)
		}
		undo = append(undo, func() {
			if err := io.swap(context.Background(), ch.Driver, prev.fsys, prev.cfg); err != nil {
				slog.Error("config.apply: putting a driver back failed", "driver", ch.Driver, "err", err)
			}
		})
	}

	at := time.Now()
	io.mu.Lock()
	for _, ch := range c.val.Changes {
		for k, u := range c.units {
			if u.driver == ch.Driver && (ch.Device == "" || u.device == ch.Device) {
				u.from, u.at, u.by = c.From, at, by
				io.running[k] = u
			}
		}
	}
	io.mu.Unlock()
	for _, ch := range c.val.Changes {
		io.log(ch, c, by, nil)
	}
	return nil
}

// swap builds the driver cfg describes, gates and starts it, swaps it in for
// the one named name and stops that one.
func (io *IOConfig) swap(ctx context.Context, name string, fsys fs.FS, cfg DriverConfig) error {
	set, ok := io.p.Runtime.Driver.(*nio.Multi)
	if !ok {
		return errors.New("the runtime's driver is not a composite")
	}
	d, err := buildDriver(fsys, cfg)
	if err != nil {
		return err
	}
	set.Gate(d)
	if s, ok := d.(interface{ Start(context.Context) }); ok {
		s.Start(context.Background()) // runs until Stop, as naut run starts drivers
	}
	prev, err := io.rt.ReplaceDriver(ctx, name, d)
	if err != nil {
		stopDriver(d)
		return err
	}
	stopDriver(prev)
	return nil
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
