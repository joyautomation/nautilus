// generate.go turns the parsed device map into a modbus.Manifest and its
// tag file. Deterministic on purpose: sources sort by id, bindings by
// (source, table, address, name), the tag file by name (tagfile.Render) —
// a regeneration diffs against the last one instead of reshuffling.
package codegen

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/internal/tagfile"
	"github.com/joyautomation/nautilus/modbus"
)

// Options steer one generation. All patterns are path.Match globs against
// the generated tag name (<instance>_<register tag>).
type Options struct {
	// Instance restricts the output to one instance id ("" = all) — the
	// brief's `--instance`, for a map that describes a whole plant when
	// one controller only polls its own skid.
	Instance string
	// Writable marks matching tags writable in ADDITION to the map's own
	// writable: rows — the `--writable` escape hatch.
	Writable []string
	// Tags selects which tags to generate at all (empty = every register
	// of every instance).
	Tags []string
}

// TagMeta is what the device map knows about a tag beyond the manifest:
// tag-file metadata the binding shape cannot carry.
type TagMeta struct {
	Unit string
	Desc string
	Init any
}

// Output is one generation: the manifest plus the map-only metadata keyed
// by tag name, which TagsYAML folds into the tag file.
type Output struct {
	Manifest modbus.Manifest
	Meta     map[string]TagMeta
}

// Generate builds the manifest from the map. The result passes
// modbus.Manifest.Validate — a map that generates an invalid manifest is an
// error here, at import time, not at `naut check` three commits later.
func Generate(dm DeviceMap, opts Options) (Output, error) {
	instances := dm.Instances
	if opts.Instance != "" {
		instances = nil
		for _, in := range dm.Instances {
			if in.ID == opts.Instance {
				instances = append(instances, in)
			}
		}
		if len(instances) == 0 {
			ids := make([]string, len(dm.Instances))
			for i, in := range dm.Instances {
				ids[i] = in.ID
			}
			return Output{}, fmt.Errorf("no instance %q in the device map (have %s)",
				opts.Instance, strings.Join(ids, ", "))
		}
	}

	out := Output{Meta: map[string]TagMeta{}}
	for _, in := range instances {
		dt := dm.Devices[in.Type]
		out.Manifest.Sources = append(out.Manifest.Sources, modbus.Source{
			ID:        in.ID,
			Host:      in.Host,
			Port:      pick(in.Port, dt.Port),
			UnitID:    in.UnitID,
			WordOrder: pickStr(in.WordOrder, dt.WordOrder),
			ByteOrder: pickStr(in.ByteOrder, dt.ByteOrder),
			Timeout:   time.Duration(pickDur(in.Timeout, dt.Timeout)),
			Enable:    in.EnableTag,
			MaxBlock:  pick(in.MaxBlock, dt.MaxBlock),
		})
		for _, r := range dt.Registers {
			name := in.ID + "_" + r.Tag
			if len(opts.Tags) > 0 && !matchesAny(name, opts.Tags) {
				continue
			}
			scale := r.Scale
			if scale == 1 {
				scale = 0 // 0 means 1 in the manifest; normalize so it renders nothing
			}
			b := modbus.TagBinding{
				Name:      name,
				Source:    in.ID,
				Table:     pickStr(r.Table, modbus.TableHolding),
				Address:   r.Address,
				Format:    r.Format,
				Scale:     scale,
				Offset:    r.Offset,
				Writable:  r.Writable || matchesAny(name, opts.Writable),
				Rewrite:   time.Duration(r.Rewrite),
				ScanClass: pickStr(r.ScanClass, pickStr(in.ScanClass, dt.ScanClass)),
				WriteOnly: r.WriteOnly,
			}
			if b.Format == "" {
				if b.Table == modbus.TableCoil || b.Table == modbus.TableDiscrete {
					b.Format = "bool"
				} else {
					b.Format = "uint16"
				}
			}
			out.Manifest.Tags = append(out.Manifest.Tags, b)
			out.Meta[name] = TagMeta{
				Unit: r.Unit,
				Desc: joinDesc(in.Desc, r.Desc),
				Init: r.Init,
			}
		}
	}

	sort.Slice(out.Manifest.Sources, func(i, j int) bool {
		return out.Manifest.Sources[i].ID < out.Manifest.Sources[j].ID
	})
	tags := out.Manifest.Tags
	sort.Slice(tags, func(i, j int) bool {
		a, b := tags[i], tags[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Table != b.Table {
			return tableRank(a.Table) < tableRank(b.Table)
		}
		if a.Address != b.Address {
			return a.Address < b.Address
		}
		return a.Name < b.Name
	})

	if err := out.Manifest.Validate(); err != nil {
		return Output{}, fmt.Errorf("the map generates an invalid manifest:\n%w", err)
	}
	return out, nil
}

// tableRank orders tables the way plan.go does: registers before bits.
func tableRank(table string) int {
	switch table {
	case modbus.TableHolding:
		return 0
	case modbus.TableInput:
		return 1
	case modbus.TableCoil:
		return 2
	default:
		return 3
	}
}

func pick(v, fallback int) int {
	if v != 0 {
		return v
	}
	return fallback
}

func pickStr(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

func pickDur(v, fallback duration) duration {
	if v != 0 {
		return v
	}
	return fallback
}

// joinDesc composes the instance's description with the register's: the
// map says "Zone A loop" once, every tag says which loop it belongs to.
func joinDesc(instance, register string) string {
	switch {
	case instance == "":
		return register
	case register == "":
		return instance
	}
	return instance + " — " + register
}

func matchesAny(name string, pats []string) bool {
	for _, p := range pats {
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

// ── rendering ────────────────────────────────────────────────────────────

// ManifestYAML renders the manifest by hand rather than yaml.Marshal:
// sources as block maps with defaulted keys omitted, bindings as one flow
// map per line — the shape brief §5 shows, compact enough to review and
// stable enough to diff. The keys are the lowercased field names
// modbus.LoadManifest expects (KnownFields), and the round trip is pinned
// by TestManifestRoundTrips.
//
// command is the generating invocation, recorded in the header so the file
// says how to regenerate itself.
func ManifestYAML(m modbus.Manifest, command string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by `%s` — the\n", command)
	b.WriteString("# device map's sources and tag bindings. Do not edit — re-run the import.\n")
	b.WriteString("# Validated offline: `naut check` builds the block-read plan from this\n")
	b.WriteString("# file with no device in sight.\n")

	b.WriteString("sources:\n")
	for _, s := range m.Sources {
		fmt.Fprintf(&b, "  - id: %s\n", s.ID)
		fmt.Fprintf(&b, "    host: %s\n", s.Host)
		if s.Port != 0 && s.Port != 502 {
			fmt.Fprintf(&b, "    port: %d\n", s.Port)
		}
		fmt.Fprintf(&b, "    unitid: %d\n", s.UnitID)
		if s.WordOrder != "" && s.WordOrder != modbus.OrderBig {
			fmt.Fprintf(&b, "    wordorder: %s\n", s.WordOrder)
		}
		if s.ByteOrder != "" && s.ByteOrder != modbus.OrderBig {
			fmt.Fprintf(&b, "    byteorder: %s\n", s.ByteOrder)
		}
		if s.Timeout != 0 {
			fmt.Fprintf(&b, "    timeout: %s\n", s.Timeout)
		}
		if s.RetryMin != 0 {
			fmt.Fprintf(&b, "    retrymin: %s\n", s.RetryMin)
		}
		if s.RetryMax != 0 {
			fmt.Fprintf(&b, "    retrymax: %s\n", s.RetryMax)
		}
		if s.Enable != "" {
			fmt.Fprintf(&b, "    enable: %s\n", s.Enable)
		}
		if s.MaxBlock != 0 {
			fmt.Fprintf(&b, "    maxblock: %d\n", s.MaxBlock)
		}
	}

	b.WriteString("tags:\n")
	for _, t := range m.Tags {
		fmt.Fprintf(&b, "  - {name: %s, source: %s, table: %s, address: %d, format: %s",
			t.Name, t.Source, t.Table, t.Address, t.Format)
		if t.Scale != 0 && t.Scale != 1 {
			fmt.Fprintf(&b, ", scale: %s", floatYAML(t.Scale))
		}
		if t.Offset != 0 {
			fmt.Fprintf(&b, ", offset: %s", floatYAML(t.Offset))
		}
		if t.Writable {
			b.WriteString(", writable: true")
		}
		if t.Rewrite != 0 {
			fmt.Fprintf(&b, ", rewrite: %s", t.Rewrite)
		}
		if t.ScanClass != "" {
			fmt.Fprintf(&b, ", scanclass: %s", t.ScanClass)
		}
		if t.WriteOnly {
			b.WriteString(", writeonly: true")
		}
		b.WriteString("}\n")
	}
	return []byte(b.String())
}

// floatYAML renders a float the way yaml decodes it back: shortest exact
// form. Whole values keep no forced decimal here — Scale/Offset are
// float64 in the manifest shape regardless of the literal.
func floatYAML(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// TagsYAML renders the tag file for a manifest: role from Writable, plus
// whatever map-only metadata (unit/desc/init) the caller has. meta may be
// nil — `naut modbus tags` without --map re-derives names and roles
// from the committed manifest alone.
//
// skip holds globs for bindings to LEAVE OUT, for tags the project declares
// by hand (the eip convention, stale-exclusion error included).
func TagsYAML(m modbus.Manifest, meta map[string]TagMeta, skip []string) ([]byte, error) {
	header := []string{
		"Generated by `naut modbus import` from the device map. Do not edit —",
		"re-run the import, or `naut modbus tags modbus_manifest.yaml --map",
		"devices.yaml` to re-derive this file from the committed manifest (without",
		"--map the unit/desc/init columns, which only the map knows, are dropped).",
		"",
		"Compose it into a project with:  tag-files: [tags/modbus.yaml]",
	}
	if len(skip) > 0 {
		header = append(header, "",
			"Not generated (declared by hand in the project): "+strings.Join(skip, ", "))
	}

	var out []tagfile.Tag
	var skipped int
	for _, t := range m.Tags {
		if matchesAny(t.Name, skip) {
			skipped++
			continue
		}
		tag := tagfile.Tag{Name: t.Name, Role: "input"}
		if t.Writable {
			// Writable bindings are the driver's output set. A program
			// that also reads one back before the first poll needs an
			// init, which the map supplies per register.
			tag.Role = "output"
		}
		if md, ok := meta[t.Name]; ok {
			tag.Unit = md.Unit
			tag.Desc = md.Desc
			tag.Init = md.Init
		}
		out = append(out, tag)
	}
	if len(skip) > 0 && skipped == 0 {
		return nil, fmt.Errorf("no binding matches any of the skip patterns (%s) — "+
			"a stale exclusion would silently regenerate a tag you declare by hand",
			strings.Join(skip, ", "))
	}
	return tagfile.Render(header, out)
}

// MetaFromMap rebuilds the tag-name → metadata index Generate produces,
// for `naut modbus tags --map`: the manifest names the tags, the map
// supplies what the manifest cannot carry.
func MetaFromMap(dm DeviceMap) map[string]TagMeta {
	meta := map[string]TagMeta{}
	for _, in := range dm.Instances {
		dt := dm.Devices[in.Type]
		for _, r := range dt.Registers {
			meta[in.ID+"_"+r.Tag] = TagMeta{
				Unit: r.Unit,
				Desc: joinDesc(in.Desc, r.Desc),
				Init: r.Init,
			}
		}
	}
	return meta
}
