// Package codegen turns a walk — live or recorded, the same bytes — into
// the three files a project composes (docs/design/it-drivers.md §8):
//
//	snmp_manifest.yaml   sources + one explicit OID binding per member
//	tags/snmp.yaml       the tag file (struct tags typed by the contract set,
//	                     the __Online / __LastPollMs companions)
//	hw_types.st          the contract TYPE set, hw.TypesST("snmp")
//
// Everything here is a pure function of its inputs: no clock, no map
// iteration order, no host name, so a re-run is byte-identical and an
// import of `browse --record`'s file is byte-identical to the live import
// it recorded. The MIB knowledge is snmp/profiles'; this package names,
// orders and renders.
package codegen

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/internal/tagfile"
	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/snmp/profiles"
	"github.com/joyautomation/nautilus/snmp/walk"
)

// Options steer one import.
type Options struct {
	// Tag is the device prefix: the source id, the root tag's name and the
	// prefix of every child (SW1, SW1_Port01). Letters and digits only, so
	// `alarms.site-from: "^([A-Za-z0-9]+?)(?:_|$)"` recovers it from any
	// child tag.
	Tag string
	// Host and Port are where the driver will poll — recorded in the
	// manifest, never read from the walk.
	Host string
	Port int
	// Profile forces a profile; "" picks by sysObjectID, then by content.
	Profile string
	// Ports selects interfaces by ifIndex ("1-24,26" or "all"); "" means the
	// physical ethernet ports.
	Ports string
	// Version is "2c" (default) or "3"; the v3 fields name the USM user and
	// protocols. Credential variables are always SNMP_<TAG>_COMMUNITY /
	// _AUTH / _PRIV — named, never written.
	Version string
	User    string
	Auth    string
	Priv    string
	Context string
}

// Output is one import.
type Output struct {
	Manifest snmp.Manifest
	Profile  profiles.Profile
	Notes    []string
}

var tagRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)

// Generate expands the walk through its profile into a manifest. The result
// passes snmp.New — an import that could not run is an error here, not at
// `naut check` three commits later.
func Generate(w walk.Walk, o Options) (Output, error) {
	if !tagRE.MatchString(o.Tag) {
		return Output{}, fmt.Errorf("--tag %q: letters and digits only, starting with a letter (it prefixes every child tag, and alarms find the device by the text before the first '_')", o.Tag)
	}
	if o.Host == "" {
		return Output{}, fmt.Errorf("--host is required: it is the address the driver will poll, and a recorded walk does not know it")
	}
	ports, err := profiles.ParseIndexSet(o.Ports)
	if err != nil {
		return Output{}, err
	}
	prof, err := profiles.Pick(w, o.Profile)
	if err != nil {
		return Output{}, err
	}
	res, err := prof.Build(w, profiles.Options{Ports: ports})
	if err != nil {
		return Output{}, fmt.Errorf("profile %s: %w", prof.Name, err)
	}

	src := snmp.Source{ID: o.Tag, Host: o.Host, Port: o.Port, Version: o.Version}
	if src.Port == snmp.DefaultPort {
		src.Port = 0
	}
	env := "SNMP_" + strings.ToUpper(o.Tag) + "_"
	switch o.Version {
	case "", snmp.V2c:
		src.Version = snmp.V2c
		src.CommunityEnv = env + "COMMUNITY"
	case snmp.V3:
		src.User, src.Auth, src.Priv, src.Context = o.User, o.Auth, o.Priv, o.Context
		if o.Auth != "" {
			src.AuthEnv = env + "AUTH"
		}
		if o.Priv != "" {
			src.PrivEnv = env + "PRIV"
		}
	default:
		return Output{}, fmt.Errorf("--version %q (want 2c or 3)", o.Version)
	}

	m := snmp.Manifest{Sources: []snmp.Source{src}}
	seen := map[string]bool{}
	for _, in := range res.Instances {
		name := o.Tag + in.Suffix
		if seen[name] {
			return Output{}, fmt.Errorf("profile %s generates %s twice — two device names sanitise alike", prof.Name, name)
		}
		seen[name] = true
		m.Tags = append(m.Tags, snmp.Tag{Name: name, Type: in.Type, Source: o.Tag, Members: in.Members})
	}
	// By name: the root sorts before its children ("SW1" < "SW1_…"), and the
	// zero-padding makes the children's name order their numeric order.
	sort.SliceStable(m.Tags, func(i, j int) bool { return m.Tags[i].Name < m.Tags[j].Name })
	if _, err := snmp.New(m); err != nil {
		return Output{}, fmt.Errorf("profile %s generated an invalid manifest:\n%w", prof.Name, err)
	}
	return Output{Manifest: m, Profile: prof, Notes: res.Notes}, nil
}

// ── rendering ────────────────────────────────────────────────────────────

// memberKeyOrder is the order binding keys render in a flow map.
var memberKeyOrder = []string{"oid", "const", "map", "eq", "ports", "rate", "width", "scale", "offset", "scan-class", "derived"}

// ManifestYAML renders the manifest by hand: block style for sources and
// tags, one flow map per member binding, members in contract order (the
// shape brief §6.1 shows) — compact enough to review, stable enough to
// diff. The keys are exactly what snmp.LoadManifest decodes with
// KnownFields; TestManifestRoundTrips pins it.
//
// command is the generating invocation; notes are the profile's findings,
// recorded in the header so the reviewer sees why a member is zero.
func ManifestYAML(out Output, command string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by `%s`.\n", command)
	fmt.Fprintf(&b, "# Profile %s: %s.\n", out.Profile.Name, out.Profile.Desc)
	b.WriteString("# Do not edit the bindings — re-run the import. `naut check` validates this\n")
	b.WriteString("# file offline. Credentials are NAMED here, never written: set the variables\n")
	b.WriteString("# below where the controller runs (or switch to *-file: for a mounted secret).\n")
	b.WriteString("# Commands are opt-in: add a `writes:` section by hand, in a reviewed diff.\n")
	if len(out.Notes) > 0 {
		b.WriteString("#\n# Import notes:\n")
		for _, n := range out.Notes {
			fmt.Fprintf(&b, "#   - %s\n", n)
		}
	}
	m := out.Manifest
	b.WriteString("sources:\n")
	for _, s := range m.Sources {
		fmt.Fprintf(&b, "  - id: %s\n", s.ID)
		fmt.Fprintf(&b, "    host: %s\n", yamlString(s.Host))
		if s.Port != 0 && s.Port != snmp.DefaultPort {
			fmt.Fprintf(&b, "    port: %d\n", s.Port)
		}
		fmt.Fprintf(&b, "    version: %s\n", yamlString(s.Version))
		kv := func(k, v string) {
			if v != "" {
				fmt.Fprintf(&b, "    %s: %s\n", k, yamlString(v))
			}
		}
		kv("community-env", s.CommunityEnv)
		kv("community-file", s.CommunityFile)
		kv("user", s.User)
		kv("auth", s.Auth)
		kv("auth-env", s.AuthEnv)
		kv("auth-file", s.AuthFile)
		kv("priv", s.Priv)
		kv("priv-env", s.PrivEnv)
		kv("priv-file", s.PrivFile)
		kv("context", s.Context)
		if s.Timeout != 0 {
			fmt.Fprintf(&b, "    timeout: %s\n", s.Timeout)
		}
		if s.Retries != nil {
			fmt.Fprintf(&b, "    retries: %d\n", *s.Retries)
		}
		if s.MaxRepetitions != 0 {
			fmt.Fprintf(&b, "    max-repetitions: %d\n", s.MaxRepetitions)
		}
		if s.Interval != 0 {
			fmt.Fprintf(&b, "    interval: %s\n", s.Interval)
		}
		if s.StaleAfter != 0 {
			fmt.Fprintf(&b, "    stale-after: %s\n", s.StaleAfter)
		}
		kv("enable", s.Enable)
	}
	b.WriteString("tags:\n")
	for _, t := range m.Tags {
		fmt.Fprintf(&b, "  - name: %s\n", t.Name)
		fmt.Fprintf(&b, "    type: %s\n", t.Type)
		fmt.Fprintf(&b, "    source: %s\n", t.Source)
		b.WriteString("    members:\n")
		names := contractOrder(t.Type, t.Members)
		width := 0
		for _, n := range names {
			width = max(width, len(n))
		}
		for _, n := range names {
			fmt.Fprintf(&b, "      %-*s {%s}\n", width+1, n+":", memberFlow(t.Members[n]))
		}
	}
	if len(m.Writes) > 0 {
		b.WriteString("writes:\n")
		for _, w := range m.Writes {
			fmt.Fprintf(&b, "  - {name: %s, tag: %s, member: %s, oid: %s", w.Name, w.Tag, w.Member, w.OID)
			if len(w.Set) > 0 {
				keys := sortedKeys(w.Set)
				parts := make([]string, len(keys))
				for i, k := range keys {
					parts[i] = fmt.Sprintf("%s: %d", k, w.Set[k])
				}
				fmt.Fprintf(&b, ", set: {%s}", strings.Join(parts, ", "))
			}
			b.WriteString("}\n")
		}
	}
	return []byte(b.String())
}

// contractOrder lists a tag's bound members in the contract's field order.
func contractOrder(typ string, members map[string]snmp.Member) []string {
	t, _ := hw.TypeByName(typ)
	var out []string
	for _, f := range t.Fields {
		if _, ok := members[f.Name]; ok {
			out = append(out, f.Name)
		}
	}
	return out
}

func memberFlow(mb snmp.Member) string {
	var parts []string
	for _, k := range memberKeyOrder {
		switch k {
		case "oid":
			if mb.OID != "" {
				parts = append(parts, "oid: "+mb.OID)
			}
		case "const":
			if mb.Const != nil {
				parts = append(parts, "const: "+yamlScalar(mb.Const))
			}
		case "map":
			if mb.Map != nil {
				keys := sortedNumeric(mb.Map)
				kv := make([]string, len(keys))
				for i, key := range keys {
					kv[i] = strconv.Quote(key) + ": " + yamlScalar(mb.Map[key])
				}
				parts = append(parts, "map: {"+strings.Join(kv, ", ")+"}")
			}
		case "eq":
			if mb.Eq != nil {
				parts = append(parts, "eq: "+yamlScalar(mb.Eq))
			}
		case "ports":
			if mb.Ports != nil {
				keys := make([]string, 0, len(mb.Ports))
				for key := range mb.Ports {
					keys = append(keys, key)
				}
				sort.Slice(keys, func(i, j int) bool {
					a, _ := strconv.Atoi(keys[i])
					b, _ := strconv.Atoi(keys[j])
					return a < b
				})
				kv := make([]string, len(keys))
				for i, key := range keys {
					kv[i] = strconv.Quote(key) + ": " + strconv.Itoa(mb.Ports[key])
				}
				parts = append(parts, "ports: {"+strings.Join(kv, ", ")+"}")
			}
		case "rate":
			if mb.Rate {
				parts = append(parts, "rate: true")
			}
		case "width":
			if mb.Width != 0 {
				parts = append(parts, "width: "+strconv.Itoa(mb.Width))
			}
		case "scale":
			if mb.Scale != 0 {
				parts = append(parts, "scale: "+strconv.FormatFloat(mb.Scale, 'g', -1, 64))
			}
		case "offset":
			if mb.Offset != 0 {
				parts = append(parts, "offset: "+strconv.FormatFloat(mb.Offset, 'g', -1, 64))
			}
		case "scan-class":
			if mb.ScanClass != "" {
				parts = append(parts, "scan-class: "+yamlString(mb.ScanClass))
			}
		case "derived":
			if mb.Derived != "" {
				parts = append(parts, "derived: "+strconv.Quote(mb.Derived))
			}
		}
	}
	return strings.Join(parts, ", ")
}

// yamlScalar renders a binding literal so yaml.v3 decodes it back to the
// same Go value: ints bare, floats with a decimal point, strings quoted.
func yamlScalar(v any) string {
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		s := strconv.FormatFloat(x, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0"
		}
		return s
	case string:
		return strconv.Quote(x)
	}
	return strconv.Quote(fmt.Sprint(v))
}

// yamlString quotes a string only when YAML would read it as something
// else (a number, a bool, a flow indicator).
func yamlString(s string) string {
	if plainRE.MatchString(s) && !ambiguous[strings.ToLower(s)] {
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return s
		}
	}
	return strconv.Quote(s)
}

var (
	plainRE   = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
	ambiguous = map[string]bool{"true": true, "false": true, "yes": true, "no": true, "on": true, "off": true, "null": true, "~": true, "y": true, "n": true}
)

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedNumeric orders map keys numerically when they are numbers ("2"
// before "10"), lexically otherwise.
func sortedNumeric[V any](m map[string]V) []string {
	out := sortedKeys(m)
	sort.SliceStable(out, func(i, j int) bool {
		a, ea := strconv.ParseInt(out[i], 10, 64)
		b, eb := strconv.ParseInt(out[j], 10, 64)
		if ea == nil && eb == nil {
			return a < b
		}
		return out[i] < out[j]
	})
	return out
}

// TagsYAML renders the tag file for a manifest — a pure function of the
// committed manifest, so `naut snmp tags` reproduces the import's file byte
// for byte with nothing but the repo. Struct tags carry their contract type
// and its description; each source adds its __Online / __LastPollMs
// companions (the guard every alarm rule enables on); each write is an
// output. skip holds globs to leave out for tags declared by hand; a
// pattern that matches nothing is an error (a stale exclusion).
func TagsYAML(m snmp.Manifest, skip []string) ([]byte, error) {
	for _, p := range skip {
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("bad skip pattern %q: %w", p, err)
		}
	}
	hit := map[string]int{}
	var out []tagfile.Tag
	add := func(t tagfile.Tag) {
		for _, p := range skip {
			if ok, _ := path.Match(p, t.Name); ok {
				hit[p]++
				return
			}
		}
		out = append(out, t)
	}
	for _, t := range m.Tags {
		typ, _ := hw.TypeByName(t.Type)
		add(tagfile.Tag{Name: t.Name, Role: "input", Type: t.Type, Desc: typ.Desc})
	}
	for _, s := range m.Sources {
		add(tagfile.Tag{Name: hw.OnlineTagName(s.ID), Role: "input", Desc: "SNMP agent " + s.ID + " answered within stale-after — interlock on this"})
		add(tagfile.Tag{Name: hw.LastPollTagName(s.ID), Role: "input", Unit: "ms", Desc: "epoch ms of " + s.ID + "'s last complete poll, 0 before the first"})
	}
	for _, w := range m.Writes {
		add(tagfile.Tag{Name: w.Name, Role: "output", Desc: "command → " + w.Tag + "." + w.Member})
	}
	for _, p := range skip {
		if hit[p] == 0 {
			return nil, fmt.Errorf("skip pattern %q matches no tag — a stale exclusion would silently regenerate a tag you declare by hand", p)
		}
	}
	header := []string{
		"Generated by `naut snmp import`. Do not edit — re-run the import, or",
		"`naut snmp tags snmp_manifest.yaml` to re-derive this file from the",
		"committed manifest alone.",
		"",
		"Compose it into a project with:  tag-files: [tags/snmp.yaml]",
		"and the contract types with the generated hw_types.st.",
		"",
		"<id>__Online / <id>__LastPollMs are synthesized by the driver: a device's",
		"values hold (Stale) when it stops answering, so interlock on __Online.",
	}
	if len(skip) > 0 {
		header = append(header, "", "Not generated (declared by hand in the project): "+strings.Join(skip, ", "))
	}
	return tagfile.Render(header, out)
}

// TypesST is hw_types.st as the snmp importer writes it.
func TypesST() ([]byte, error) { return hw.TypesST("snmp") }
