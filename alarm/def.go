package alarm

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"gopkg.in/yaml.v3"
)

// Priority orders the active list and colours the banner. Diagnostic is the
// zero value only because Go demands one; YAML that omits `priority:` gets
// Medium (see defaultPriority), matching the source system this models,
// where 633 of 635 alarms carried no priority at all.
type Priority uint8

const (
	Diagnostic Priority = iota
	Low
	Medium
	High
	Critical
)

// defaultPriority is what a definition with no explicit `priority:` gets.
const defaultPriority = Medium

var priorityNames = [...]string{"diagnostic", "low", "medium", "high", "critical"}

func (p Priority) String() string {
	if int(p) < len(priorityNames) {
		return priorityNames[p]
	}
	return "diagnostic"
}

// ParsePriority accepts the lowercase wire tokens, case-insensitively.
func ParsePriority(s string) (Priority, error) {
	for i, n := range priorityNames {
		if strings.EqualFold(s, n) {
			return Priority(i), nil
		}
	}
	return 0, fmt.Errorf("unknown priority %q (want one of %s)",
		s, strings.Join(priorityNames[:], ", "))
}

func (p Priority) MarshalJSON() ([]byte, error) { return json.Marshal(p.String()) }

func (p *Priority) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := ParsePriority(s)
	if err != nil {
		return err
	}
	*p = v
	return nil
}

func (p Priority) MarshalYAML() (any, error) { return p.String(), nil }

func (p *Priority) UnmarshalYAML(node *yaml.Node) error {
	v, err := ParsePriority(node.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	*p = v
	return nil
}

// Duration parses "5m" / "300s" YAML scalars, the same spelling
// internal/project.Duration accepts, so a manifest reads consistently.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	v, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("line %d: bad duration %q (want e.g. 5m, 30s)", node.Line, node.Value)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Def is one alarm definition: the condition to watch and the operator
// policy that governs it.
//
// Tag is a condition PATH, not necessarily a tag name — a Sparkplug Template
// lands as one struct tag, so the fleet's conditions are overwhelmingly
// "RTU9_WEL15_FIT_001.HH" rather than a flat BOOL. It must resolve to a
// BOOL; true means in alarm.
type Def struct {
	ID   string `yaml:"id" json:"id"`     // defaults to Tag
	Tag  string `yaml:"tag" json:"tag"`   // "tag" or "tag.member"; BOOL, true = in alarm
	Name string `yaml:"name" json:"name"` // the human-readable identity; defaults to ID

	Priority Priority `yaml:"priority" json:"priority"`

	Class   string `yaml:"class" json:"class,omitempty"`     // grouping + notification routing
	Site    string `yaml:"site" json:"site,omitempty"`       // sort + filter axes on the table
	Area    string `yaml:"area" json:"area,omitempty"`       //
	Display string `yaml:"display" json:"display,omitempty"` // display path, if any
	Notes   string `yaml:"notes" json:"notes,omitempty"`

	// Delays are host-side qualification and de-qualification. They stay off
	// the wire: an operator screen shows what an alarm IS doing, and a
	// definition's delay is only ever interesting while editing the
	// manifest, where YAML — not JSON — is the format.
	OnDelay  time.Duration `yaml:"-" json:"-"`
	OffDelay time.Duration `yaml:"-" json:"-"`

	AckRequired bool `yaml:"-" json:"ackRequired"` // false = annunciate-only
	AutoClear   bool `yaml:"-" json:"autoClear"`   // false = latched: RTN still needs an ack
	Shelvable   bool `yaml:"-" json:"shelvable"`

	// Enable names a BOOL that must be true for the definition to be
	// evaluated; "" means always enabled. Pointing it at "<site>__Online"
	// is what stops a dead edge node from freezing four alarms on.
	Enable string `yaml:"enable" json:"enable,omitempty"`

	// set records which fields the YAML actually spelled, so ApplyDefaults
	// can fill only the rest. Unexported: it is load-time bookkeeping, not
	// part of the definition, and never crosses the wire.
	set fieldSet
}

// fieldSet marks the Def fields a source explicitly set, so a manifest's
// `defaults:` block fills the gaps without overwriting a deliberate value.
// Only fields with a non-zero package default need a bit; strings whose
// default is "" are indistinguishable from unset and don't.
type fieldSet uint16

const (
	setPriority fieldSet = 1 << iota
	setAckRequired
	setAutoClear
	setShelvable
	setOnDelay
	setOffDelay
	setClass
	setSite
	setArea
	setEnable
)

// defWire is Def's YAML shape. Def itself cannot carry these tags: the
// three policy bools default to TRUE, so an absent key and an explicit
// `false` must be distinguishable, and durations arrive as "5m" strings.
type defWire struct {
	ID       string    `yaml:"id"`
	Tag      string    `yaml:"tag"`
	Name     string    `yaml:"name"`
	Priority *Priority `yaml:"priority"`
	Class    string    `yaml:"class"`
	Site     string    `yaml:"site"`
	Area     string    `yaml:"area"`
	Display  string    `yaml:"display"`
	Notes    string    `yaml:"notes"`
	OnDelay  *Duration `yaml:"on-delay"`
	OffDelay *Duration `yaml:"off-delay"`
	Ack      *bool     `yaml:"ack-required"`
	Clear    *bool     `yaml:"auto-clear"`
	Shelve   *bool     `yaml:"shelve"`
	Enable   string    `yaml:"enable"`
}

// UnmarshalYAML applies the package defaults (medium, ack-required,
// auto-clear, shelvable) and records which keys were spelled explicitly.
func (d *Def) UnmarshalYAML(node *yaml.Node) error {
	var w defWire
	if err := strictDecode(node, &w); err != nil {
		return err
	}
	*d = Def{
		ID: w.ID, Tag: w.Tag, Name: w.Name,
		Priority: defaultPriority,
		Class:    w.Class, Site: w.Site, Area: w.Area,
		Display: w.Display, Notes: w.Notes,
		AckRequired: true, AutoClear: true, Shelvable: true,
		Enable: w.Enable,
	}
	if w.Priority != nil {
		d.Priority, d.set = *w.Priority, d.set|setPriority
	}
	if w.OnDelay != nil {
		d.OnDelay, d.set = time.Duration(*w.OnDelay), d.set|setOnDelay
	}
	if w.OffDelay != nil {
		d.OffDelay, d.set = time.Duration(*w.OffDelay), d.set|setOffDelay
	}
	if w.Ack != nil {
		d.AckRequired, d.set = *w.Ack, d.set|setAckRequired
	}
	if w.Clear != nil {
		d.AutoClear, d.set = *w.Clear, d.set|setAutoClear
	}
	if w.Shelve != nil {
		d.Shelvable, d.set = *w.Shelve, d.set|setShelvable
	}
	if w.Class != "" {
		d.set |= setClass
	}
	if w.Site != "" {
		d.set |= setSite
	}
	if w.Area != "" {
		d.set |= setArea
	}
	if w.Enable != "" {
		d.set |= setEnable
	}
	return nil
}

// MarshalYAML round-trips a Def back through defWire, so `nautilus alarms
// list` emits YAML a manifest could take back verbatim — the policy bools
// and the delays are on Def as plain Go values and would otherwise be lost
// by the `yaml:"-"` tags UnmarshalYAML needs.
func (d Def) MarshalYAML() (any, error) {
	w := defWire{
		ID: d.ID, Tag: d.Tag, Name: d.Name,
		Class: d.Class, Site: d.Site, Area: d.Area,
		Display: d.Display, Notes: d.Notes, Enable: d.Enable,
	}
	p := d.Priority
	w.Priority = &p
	ack, clear, shelve := d.AckRequired, d.AutoClear, d.Shelvable
	w.Ack, w.Clear, w.Shelve = &ack, &clear, &shelve
	if d.OnDelay != 0 {
		on := Duration(d.OnDelay)
		w.OnDelay = &on
	}
	if d.OffDelay != 0 {
		off := Duration(d.OffDelay)
		w.OffDelay = &off
	}
	return w, nil
}

// Match selects the conditions a Rule generates definitions for.
//
// Type matches the ir.StructDef name of a struct tag; Member matches a
// field within it; Tag matches the tag name. All three are globs
// (path.Match syntax: *, ?, [class]) and an empty field means "any".
//
// A rule with no Member matches only FLAT BOOL tags, and a rule with a
// Member matches only struct members — because a struct tag is not itself a
// BOOL, so "which member" is never a detail a rule can leave out.
type Match struct {
	Type   string `yaml:"type"`
	Member string `yaml:"member"`
	Tag    string `yaml:"tag"`
}

// UnmarshalYAML exists only to keep `match:` strict. A Rule is decoded
// through strictDecode, which calls node.Decode and so builds a fresh
// decoder without KnownFields — nested mappings would lose their
// strictness, and `match: {typ: AnalogInput}` matching nothing at all is
// the kind of silence that costs an afternoon.
func (m *Match) UnmarshalYAML(node *yaml.Node) error {
	type matchWire struct {
		Type   string `yaml:"type"`
		Member string `yaml:"member"`
		Tag    string `yaml:"tag"`
	}
	var w matchWire
	if err := strictDecode(node, &w); err != nil {
		return err
	}
	*m = Match{Type: w.Type, Member: w.Member, Tag: w.Tag}
	return nil
}

func (m Match) empty() bool { return m.Type == "" && m.Member == "" && m.Tag == "" }

// validate reports an unusable glob at load time rather than silently
// matching nothing for the life of the process.
func (m Match) validate() error {
	for _, p := range [][2]string{{"type", m.Type}, {"member", m.Member}, {"tag", m.Tag}} {
		if p[1] == "" {
			continue
		}
		if _, err := path.Match(p[1], ""); err != nil {
			return fmt.Errorf("match.%s: bad pattern %q: %w", p[0], p[1], err)
		}
	}
	return nil
}

func (m Match) matches(tagName, typeName, member string) bool {
	if m.Member == "" != (member == "") {
		return false
	}
	if m.Type != "" && !glob(m.Type, typeName) {
		return false
	}
	if m.Tag != "" && !glob(m.Tag, tagName) {
		return false
	}
	if m.Member != "" && !glob(m.Member, member) {
		return false
	}
	return true
}

func glob(pattern, s string) bool {
	if pattern == s {
		return true
	}
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}

// Rule generates definitions in bulk. Fourteen of them cover ~1 850 fleet
// alarms, because a fleet is the same handful of UDTs repeated: the rule
// names the (type, member) pair once and Expand materializes an instance
// per tag.
//
// ID, Name and Enable are templates over {tag} {member} {site} {area}
// {desc} {type} {path}; an unknown placeholder is an error, so a typo in a
// name template fails the load instead of shipping "{sight}" to an
// operator screen.
type Rule struct {
	Match Match `yaml:"match"`

	ID   string `yaml:"id"`   // template; default "{path}"
	Name string `yaml:"name"` // template; default "{desc} {member}"

	Priority *Priority `yaml:"priority"`
	Class    string    `yaml:"class"`
	Site     string    `yaml:"site"`    // template; default the tag's own site
	Area     string    `yaml:"area"`    // template; default the tag's own area
	Display  string    `yaml:"display"` // template
	Notes    string    `yaml:"notes"`

	OnDelay  *Duration `yaml:"on-delay"`
	OffDelay *Duration `yaml:"off-delay"`

	AckRequired *bool `yaml:"ack-required"`
	AutoClear   *bool `yaml:"auto-clear"`
	Shelve      *bool `yaml:"shelve"`

	Enable string `yaml:"enable"` // template, e.g. "{site}__Online"
}

// TagInfo is everything Expand needs to know about one tag in the store.
//
// Struct is non-nil for a struct tag and is what lets a rule enumerate
// members; TypeName defaults to Struct.Name when empty. Desc, Site and Area
// are optional tag metadata that name templates interpolate — Desc falls
// back to Name, which keeps a generated name legible even with no metadata
// at all.
type TagInfo struct {
	Name     string
	TypeName string
	Struct   *ir.StructDef

	Desc string
	Site string
	Area string
}

// candidate is one BOOL condition a rule could claim: a flat tag (member
// "") or one member of a struct tag.
type candidate struct {
	tag, typeName, member string
	info                  TagInfo
}

func (c candidate) path() string {
	if c.member == "" {
		return c.tag
	}
	return c.tag + "." + c.member
}

// Expand materializes rules against the tag list and folds in the explicit
// definitions, returning the complete set sorted by ID.
//
// The order it works in is fixed so the result is byte-identical run to run:
// tags sorted by name, members in declaration order within a tag, rules in
// file order with FIRST MATCH WINS. A duplicate id — two rules colliding, or
// a rule colliding with a hand-written entry — is an error naming both
// sources, never last-wins: last-wins reads fine on the day it is written
// and rots silently when the generator that emitted one side changes.
//
// Definitions are materialized here, at compose time, and never per scan.
func Expand(rules []Rule, defs []Def, tags []TagInfo) ([]Def, error) {
	for i, r := range rules {
		if r.Match.empty() {
			return nil, fmt.Errorf("rule %d: match: is empty — a rule with no match would claim every tag", i)
		}
		if err := r.Match.validate(); err != nil {
			return nil, fmt.Errorf("rule %d: %w", i, err)
		}
	}

	sorted := append([]TagInfo(nil), tags...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	out := make([]Def, 0, len(defs)+len(sorted))
	from := make(map[string]string, len(defs)+len(sorted))
	add := func(d Def, src string) error {
		if prev, dup := from[d.ID]; dup {
			return fmt.Errorf("alarm %q is declared by both %s and %s — an id may be "+
				"declared once (narrow a rule's match, or drop the explicit entry)",
				d.ID, prev, src)
		}
		from[d.ID] = src
		out = append(out, d)
		return nil
	}

	for _, t := range sorted {
		for _, c := range candidatesFor(t) {
			for i, r := range rules {
				if !r.Match.matches(c.tag, c.typeName, c.member) {
					continue
				}
				d, err := r.def(c)
				if err != nil {
					return nil, fmt.Errorf("rule %d (%s): %w", i, c.path(), err)
				}
				if err := add(d, fmt.Sprintf("rule %d", i)); err != nil {
					return nil, err
				}
				break // first match wins
			}
		}
	}

	for i, d := range defs {
		if d.Tag == "" {
			return nil, fmt.Errorf("alarm %d (%q): tag: is required — it is the BOOL condition to watch", i, d.ID)
		}
		if d.ID == "" {
			d.ID = d.Tag
		}
		if d.Name == "" {
			d.Name = d.ID
		}
		if err := add(d, fmt.Sprintf("alarm %q", d.ID)); err != nil {
			return nil, err
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// candidatesFor lists the BOOL conditions a tag offers: every BOOL member
// of a struct tag, in declaration order, or the tag itself if it is flat.
func candidatesFor(t TagInfo) []candidate {
	typeName := t.TypeName
	if typeName == "" && t.Struct != nil {
		typeName = t.Struct.Name
	}
	if t.Struct == nil {
		return []candidate{{tag: t.Name, typeName: typeName, info: t}}
	}
	out := make([]candidate, 0, len(t.Struct.Fields))
	for _, f := range t.Struct.Fields {
		if f.Type == nil || f.Type.Kind != ir.TypeBool {
			continue
		}
		out = append(out, candidate{tag: t.Name, typeName: typeName, member: f.Name, info: t})
	}
	return out
}

// def instantiates a rule for one condition.
func (r Rule) def(c candidate) (Def, error) {
	desc := c.info.Desc
	if desc == "" {
		desc = c.tag
	}
	vars := map[string]string{
		"tag":    c.tag,
		"member": c.member,
		"site":   c.info.Site,
		"area":   c.info.Area,
		"desc":   desc,
		"type":   c.typeName,
		"path":   c.path(),
	}
	tmpl := func(field, s, fallback string) (string, error) {
		if s == "" {
			s = fallback
		}
		v, err := interpolate(s, vars)
		if err != nil {
			return "", fmt.Errorf("%s: %w", field, err)
		}
		return v, nil
	}

	d := Def{
		Tag:      c.path(),
		Priority: defaultPriority,
		Class:    r.Class,
		Display:  r.Display,
		Notes:    r.Notes,

		AckRequired: true, AutoClear: true, Shelvable: true,
	}
	var err error
	if d.ID, err = tmpl("id", r.ID, "{path}"); err != nil {
		return Def{}, err
	}
	defName := "{desc}"
	if c.member != "" {
		defName = "{desc} {member}"
	}
	if d.Name, err = tmpl("name", r.Name, defName); err != nil {
		return Def{}, err
	}
	if d.Site, err = tmpl("site", r.Site, "{site}"); err != nil {
		return Def{}, err
	}
	if d.Area, err = tmpl("area", r.Area, "{area}"); err != nil {
		return Def{}, err
	}
	if d.Enable, err = tmpl("enable", r.Enable, ""); err != nil {
		return Def{}, err
	}
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" {
		d.Name = d.ID
	}

	if r.Priority != nil {
		d.Priority, d.set = *r.Priority, d.set|setPriority
	}
	if r.OnDelay != nil {
		d.OnDelay, d.set = time.Duration(*r.OnDelay), d.set|setOnDelay
	}
	if r.OffDelay != nil {
		d.OffDelay, d.set = time.Duration(*r.OffDelay), d.set|setOffDelay
	}
	if r.AckRequired != nil {
		d.AckRequired, d.set = *r.AckRequired, d.set|setAckRequired
	}
	if r.AutoClear != nil {
		d.AutoClear, d.set = *r.AutoClear, d.set|setAutoClear
	}
	if r.Shelve != nil {
		d.Shelvable, d.set = *r.Shelve, d.set|setShelvable
	}
	if d.Class != "" {
		d.set |= setClass
	}
	if d.Site != "" {
		d.set |= setSite
	}
	if d.Area != "" {
		d.set |= setArea
	}
	if d.Enable != "" {
		d.set |= setEnable
	}
	return d, nil
}

// interpolate substitutes {name} placeholders and rejects unknown ones, so
// a mistyped template fails the load rather than reaching an operator
// screen as literal braces.
func interpolate(s string, vars map[string]string) (string, error) {
	if !strings.ContainsRune(s, '{') {
		return s, nil
	}
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			b.WriteString(s)
			return b.String(), nil
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			return "", fmt.Errorf("unclosed { in %q", s)
		}
		key := s[i+1 : i+j]
		v, ok := vars[key]
		if !ok {
			return "", fmt.Errorf("unknown placeholder {%s} (known: %s)", key, strings.Join(varNames(vars), ", "))
		}
		b.WriteString(s[:i])
		b.WriteString(v)
		s = s[i+j+1:]
	}
}

func varNames(vars map[string]string) []string {
	out := make([]string, 0, len(vars))
	for k := range vars {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Defaults is the manifest's `alarms.defaults:` block: what a definition
// gets when it does not say. Every field is a pointer or a string so
// "unset" stays distinguishable from "set to the zero value".
type Defaults struct {
	Priority    *Priority `yaml:"priority"`
	Class       string    `yaml:"class"`
	Site        string    `yaml:"site"`
	Area        string    `yaml:"area"`
	OnDelay     *Duration `yaml:"on-delay"`
	OffDelay    *Duration `yaml:"off-delay"`
	AckRequired *bool     `yaml:"ack-required"`
	AutoClear   *bool     `yaml:"auto-clear"`
	Shelve      *bool     `yaml:"shelve"`
	Enable      string    `yaml:"enable"`
}

// ApplyDefaults fills, in place, every field a definition did not spell for
// itself. Call it before Expand: a rule's own values count as spelled, so
// defaults never overwrite a rule.
func ApplyDefaults(d Defaults, defs []Def) {
	for i := range defs {
		p := &defs[i]
		if d.Priority != nil && p.set&setPriority == 0 {
			p.Priority = *d.Priority
		}
		if d.OnDelay != nil && p.set&setOnDelay == 0 {
			p.OnDelay = time.Duration(*d.OnDelay)
		}
		if d.OffDelay != nil && p.set&setOffDelay == 0 {
			p.OffDelay = time.Duration(*d.OffDelay)
		}
		if d.AckRequired != nil && p.set&setAckRequired == 0 {
			p.AckRequired = *d.AckRequired
		}
		if d.AutoClear != nil && p.set&setAutoClear == 0 {
			p.AutoClear = *d.AutoClear
		}
		if d.Shelve != nil && p.set&setShelvable == 0 {
			p.Shelvable = *d.Shelve
		}
		if d.Class != "" && p.set&setClass == 0 {
			p.Class = d.Class
		}
		if d.Site != "" && p.set&setSite == 0 {
			p.Site = d.Site
		}
		if d.Area != "" && p.set&setArea == 0 {
			p.Area = d.Area
		}
		if d.Enable != "" && p.set&setEnable == 0 {
			p.Enable = d.Enable
		}
	}
}

// JournalConfig is `alarms.journal:` — how deep the in-memory ring is and
// whether events also go somewhere durable.
type JournalConfig struct {
	Keep int    `yaml:"keep"` // ring depth; 0 = DefaultKeep
	Sink string `yaml:"sink"` // "" | "file" | "postgres"
	Path string `yaml:"path"` // file sink
	DSN  string `yaml:"dsn"`  // postgres sink; "" = reuse the historian's DATABASE_URL
}

// Config is the manifest's `alarms:` block. Rules and Defs here are the
// inline half; alarm-files supply the rest and are read first, per the
// tag-files precedent.
type Config struct {
	Defaults    Defaults      `yaml:"defaults"`
	ShelveTimes []Duration    `yaml:"shelve-times"`
	Journal     JournalConfig `yaml:"journal"`
	Rules       []Rule        `yaml:"rules"`
	Defs        []Def         `yaml:"defs"`
}

// DefaultShelveTimes is the offered shelf durations when a manifest does
// not list its own.
var DefaultShelveTimes = []time.Duration{
	5 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour,
}

// ShelveDurations is ShelveTimes as plain durations, falling back to
// DefaultShelveTimes.
func (c *Config) ShelveDurations() []time.Duration {
	if len(c.ShelveTimes) == 0 {
		return DefaultShelveTimes
	}
	out := make([]time.Duration, len(c.ShelveTimes))
	for i, d := range c.ShelveTimes {
		out[i] = time.Duration(d)
	}
	return out
}

// Compose folds the config's own rules and definitions, plus any read from
// alarm-files, into the final definition set: defaults applied, rules
// expanded against the tags, ids checked. This is the one call the manifest
// tier needs.
func (c *Config) Compose(files []File, tags []TagInfo) ([]Def, error) {
	rules := make([]Rule, 0, len(c.Rules))
	defs := make([]Def, 0, len(c.Defs))
	for _, f := range files {
		rules = append(rules, f.Rules...)
		defs = append(defs, f.Defs...)
	}
	rules = append(rules, c.Rules...)
	defs = append(defs, c.Defs...)

	out, err := Expand(rules, defs, tags)
	if err != nil {
		return nil, err
	}
	// One pass, after expansion, so rule-generated and hand-written
	// definitions are filled by exactly the same code. A field either
	// source spelled for itself is marked set and stays put.
	ApplyDefaults(c.Defaults, out)
	return out, nil
}

// File is one alarm-file: a bare YAML list, mirroring a tag-file exactly,
// whose entries are definitions — or rules, which an entry declares by
// carrying a `match:` key. Keeping both in one list means a generated file
// can emit the handful of rules that cover a site's UDTs alongside the
// standalone BOOLs that name themselves, in one reviewable artifact.
type File struct {
	Defs  []Def
	Rules []Rule
}

// Load reads one alarm-file. A typo is an error, not silence: every entry
// is decoded with unknown keys rejected.
func Load(r io.Reader) (*File, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var nodes []yaml.Node
	if err := dec.Decode(&nodes); err != nil {
		if err == io.EOF {
			return &File{}, nil // an empty file is an empty list, not an error
		}
		return nil, fmt.Errorf("%w (an alarm file is a bare YAML list of alarms and rules, "+
			"with no top-level keys)", err)
	}
	out := &File{}
	for i := range nodes {
		n := &nodes[i]
		if n.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("line %d: entry %d is not a mapping", n.Line, i)
		}
		if hasKey(n, "match") {
			var r Rule
			if err := strictDecode(n, &r); err != nil {
				return nil, fmt.Errorf("line %d: %w", n.Line, err)
			}
			if err := r.Match.validate(); err != nil {
				return nil, fmt.Errorf("line %d: %w", n.Line, err)
			}
			out.Rules = append(out.Rules, r)
			continue
		}
		var d Def
		if err := d.UnmarshalYAML(n); err != nil {
			return nil, fmt.Errorf("line %d: %w", n.Line, err)
		}
		out.Defs = append(out.Defs, d)
	}
	return out, nil
}

func hasKey(n *yaml.Node, key string) bool {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return true
		}
	}
	return false
}

// strictDecode decodes node into v and rejects any key the struct does not
// declare.
//
// yaml.v3's KnownFields is a property of the DECODER, and node.Decode
// builds a fresh one — so a type with a custom UnmarshalYAML (Def has one)
// silently loses strictness. Without this, `pryority: high` would be
// ignored rather than reported, which is the worst outcome available: an
// alarm that looks configured and is not. Same trick, same reason, as
// acceptance/spec.go.
func strictDecode(node *yaml.Node, v any) error {
	if err := node.Decode(v); err != nil {
		return err
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	known := yamlFields(reflect.TypeOf(v).Elem())
	for i := 0; i+1 < len(node.Content); i += 2 {
		if k := node.Content[i]; !known[k.Value] {
			return fmt.Errorf("line %d: unknown key %q", k.Line, k.Value)
		}
	}
	return nil
}

func yamlFields(t reflect.Type) map[string]bool {
	out := make(map[string]bool, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = strings.ToLower(f.Name)
		}
		out[name] = true
	}
	return out
}
