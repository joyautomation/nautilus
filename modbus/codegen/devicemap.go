// Package codegen turns a committed DEVICE MAP into the files a manifest
// project consumes over Modbus: modbus_manifest.yaml (decoded by
// modbus.LoadManifest) and tags/modbus.yaml (composed via tag-files:).
// `naut modbus import` is the command wrapper; everything here is a
// pure function of the map, so a regeneration is byte-identical and diffs
// cleanly against the last one.
//
// The device map describes device TYPES once (register map, formats,
// scaling — straight off the datasheet) and INSTANCES per physical device
// or gateway drop (host, unit-id, overrides). Shape:
//
//	devices:                       # one entry per device TYPE
//	  temp-controller:
//	    description: PID loop controller behind a Modbus TCP gateway
//	    port: 502                  # default 502
//	    word-order: big            # big (default) | little
//	    byte-order: big            # big (default) | little
//	    timeout: 3s                # per-request; driver default when absent
//	    max-block: 64              # cap registers per block read
//	    scan-class: slow           # default class for this type's registers
//	    registers:
//	      - tag: Tpv               # tag name = <instance id>_<tag>
//	        address: 0             # 0-based PDU address (40001 → 0)
//	        table: holding         # holding (default) | input | coil | discrete
//	        format: int32          # int16 uint16 int32 uint32 float32
//	                               # float64 bool bit:N (default uint16;
//	                               # bool on bit tables)
//	        scale: 0.1             # engineering = raw*scale + offset
//	        offset: 0
//	        writable: true         # output tag: runtime writes propagate
//	        write-only: true       # writable register with no read-back
//	        rewrite: 2.5s          # re-assert the output on this period
//	        scan-class: fast       # override the type default
//	        unit: degC             # rides into the tag file
//	        desc: Process temperature
//	        init: 0.0              # tag-file init for a writable tag
//	instances:                     # one entry per device / gateway drop
//	  - id: TC_A                   # tag prefix and manifest source id
//	    type: temp-controller
//	    host: 192.168.10.10
//	    port: 502                  # override the type's
//	    unit-id: 1                 # gateway drops: same host, different id
//	    word-order: little         # override the type's
//	    byte-order: big
//	    timeout: 2s
//	    max-block: 32
//	    enable-tag: CFG_HeatersOn  # BOOL tag; false parks the source
//	    scan-class: fast           # override for all this instance's tags
//	    desc: Zone A loop          # prefixes each register's desc
//
// Unknown keys are errors (KnownFields), exactly like the manifest the map
// generates.
package codegen

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// DeviceMap is the parsed devices.yaml.
type DeviceMap struct {
	Devices   map[string]DeviceType `yaml:"devices"`
	Instances []Instance            `yaml:"instances"`
}

// DeviceType is one device model: the register map every instance shares.
type DeviceType struct {
	Description string     `yaml:"description"`
	Port        int        `yaml:"port"`
	WordOrder   string     `yaml:"word-order"`
	ByteOrder   string     `yaml:"byte-order"`
	Timeout     duration   `yaml:"timeout"`
	MaxBlock    int        `yaml:"max-block"`
	ScanClass   string     `yaml:"scan-class"`
	Registers   []Register `yaml:"registers"`
}

// Register is one row of a type's register map.
type Register struct {
	Tag       string   `yaml:"tag"`
	Address   uint16   `yaml:"address"`
	Table     string   `yaml:"table"`
	Format    string   `yaml:"format"`
	Scale     float64  `yaml:"scale"`
	Offset    float64  `yaml:"offset"`
	Writable  bool     `yaml:"writable"`
	WriteOnly bool     `yaml:"write-only"`
	Rewrite   duration `yaml:"rewrite"`
	ScanClass string   `yaml:"scan-class"`
	Unit      string   `yaml:"unit"`
	Desc      string   `yaml:"desc"`
	Init      any      `yaml:"init"`
}

// Instance is one physical device (or one unit-id behind a gateway).
type Instance struct {
	ID        string   `yaml:"id"`
	Type      string   `yaml:"type"`
	Host      string   `yaml:"host"`
	Port      int      `yaml:"port"`
	UnitID    uint8    `yaml:"unit-id"`
	WordOrder string   `yaml:"word-order"`
	ByteOrder string   `yaml:"byte-order"`
	Timeout   duration `yaml:"timeout"`
	MaxBlock  int      `yaml:"max-block"`
	EnableTag string   `yaml:"enable-tag"`
	ScanClass string   `yaml:"scan-class"`
	Desc      string   `yaml:"desc"`
}

// duration parses "100ms" / "2.5s" yaml scalars, the same wrinkle
// modbus.Manifest smooths: time.Duration has no YAML face of its own.
type duration time.Duration

func (d *duration) UnmarshalYAML(node *yaml.Node) error {
	v, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("bad duration %q (want e.g. 100ms, 2.5s)", node.Value)
	}
	*d = duration(v)
	return nil
}

// ParseDeviceMap decodes and validates a device map. Every finding is
// reported at once (errors.Join) — a map with three problems is three
// lines, not three edit-run cycles.
func ParseDeviceMap(raw []byte) (DeviceMap, error) {
	var dm DeviceMap
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // a typo is an error, not a silently dropped register
	if err := dec.Decode(&dm); err != nil {
		return DeviceMap{}, fmt.Errorf("device map: %w", err)
	}

	var errs []error
	if len(dm.Devices) == 0 {
		errs = append(errs, fmt.Errorf("device map: no devices"))
	}
	if len(dm.Instances) == 0 {
		errs = append(errs, fmt.Errorf("device map: no instances"))
	}
	types := make([]string, 0, len(dm.Devices))
	for name := range dm.Devices {
		types = append(types, name)
	}
	sort.Strings(types)
	for _, name := range types {
		dt := dm.Devices[name]
		if len(dt.Registers) == 0 {
			errs = append(errs, fmt.Errorf("device %s: no registers", name))
		}
		seen := map[string]bool{}
		for _, r := range dt.Registers {
			if r.Tag == "" {
				errs = append(errs, fmt.Errorf("device %s: register at address %d: missing tag", name, r.Address))
				continue
			}
			if !identifier(r.Tag) {
				errs = append(errs, fmt.Errorf("device %s: register tag %q is not a valid tag name", name, r.Tag))
			}
			if seen[r.Tag] {
				errs = append(errs, fmt.Errorf("device %s: duplicate register tag %q", name, r.Tag))
			}
			seen[r.Tag] = true
		}
	}
	ids := map[string]bool{}
	for _, in := range dm.Instances {
		if in.ID == "" {
			errs = append(errs, fmt.Errorf("instance with host %q: missing id", in.Host))
			continue
		}
		if !identifier(in.ID) {
			errs = append(errs, fmt.Errorf("instance %s: id is not a valid tag prefix (letters, digits, _)", in.ID))
		}
		if ids[in.ID] {
			errs = append(errs, fmt.Errorf("duplicate instance id %q", in.ID))
		}
		ids[in.ID] = true
		if in.Host == "" {
			errs = append(errs, fmt.Errorf("instance %s: missing host", in.ID))
		}
		if _, ok := dm.Devices[in.Type]; !ok {
			errs = append(errs, fmt.Errorf("instance %s: unknown device type %q (have %v)", in.ID, in.Type, types))
		}
	}
	if len(errs) > 0 {
		return DeviceMap{}, errors.Join(errs...)
	}
	return dm, nil
}

// identifier reports whether s works as a tag name segment: what ST can
// declare under VAR_EXTERNAL.
func identifier(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
