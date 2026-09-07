package project

// The manifest tier's half of the alarm subsystem: `alarms:` and
// `alarm-files:` in nautilus.yaml, composed into the definition set the
// alarm package's Engine runs.
//
// Nothing here decides alarm philosophy — that is alarm/'s job. This file
// only transcribes YAML onto alarm.Config, builds the TagInfo list Expand
// needs out of the composed tag set, and wires the engine to the runtime
// it was built over: Read through Tags.ReadPath, Now through the runtime's
// clock (so a virtual clock in an acceptance test walks a five-minute
// on-delay in a microsecond), Evaluate through OnScan.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// AlarmsConfig is the manifest's `alarms:` section — the whole alarm
// subsystem as data.
//
// Rules cover the UDT members (fourteen of them cover a fleet, because a
// fleet is the same handful of types repeated); Defs are the standalone
// BOOLs that name themselves. Both may also live in `alarm-files:`, which
// mirrors `tag-files:` exactly, so a GENERATED alarm set stays a separate
// reviewable artifact.
type AlarmsConfig struct {
	// Defaults is what a definition gets when it does not say for itself.
	Defaults alarm.Defaults `yaml:"defaults"`
	// ShelveTimes is the shelf durations an HMI offers (served on
	// /api/meta). Empty takes alarm.DefaultShelveTimes.
	ShelveTimes []alarm.Duration `yaml:"shelve-times"`
	// Journal is the in-memory ring's depth plus an optional durable sink.
	Journal AlarmJournalConfig `yaml:"journal"`
	// Notify is the notification pipeline: log lines, webhooks. Notifiers
	// run off the scan goroutine on a bounded queue.
	Notify []AlarmNotifyConfig `yaml:"notify"`

	Rules []alarm.Rule `yaml:"rules"`
	Defs  []alarm.Def  `yaml:"defs"`

	// SiteFrom and AreaFrom derive a definition's site and area from the
	// TAG NAME, by regexp: the first capture group of a match becomes the
	// value, and a tag the pattern does not match simply has none. This is
	// how `RTU9_WEL15_FIT_001` becomes site RTU9, area WEL15 without a
	// generator writing `site:` on two thousand entries — and why the
	// rules' `{site}` / `{area}` placeholders have anything to
	// interpolate. Empty (the default) leaves both blank.
	SiteFrom string `yaml:"site-from"`
	AreaFrom string `yaml:"area-from"`
}

// AlarmJournalConfig is `alarms.journal:`: how deep the always-on
// in-memory ring is, and whether events also go somewhere durable.
//
// The ring is in front of every sink, so the journal view works on a box
// with no database at all, and it is bounded by construction — which is
// the answer to a flapping storm across a couple of thousand alarms.
type AlarmJournalConfig struct {
	// Keep is the ring depth. 0 = alarm.DefaultKeep.
	Keep int `yaml:"keep"`
	// Sink is "" (ring only), "file", or "postgres".
	Sink string `yaml:"sink"`
	// Path is the file sink's JSONL path, inside the project.
	Path string `yaml:"path"`
	// DSNEnv names the ENVIRONMENT VARIABLE holding the postgres sink's
	// connection string. Not the DSN itself: a manifest is committed, and
	// a database password in git is a password you have to rotate. Default
	// NAUTILUS_ALARM_DB_URL, falling back to DATABASE_URL — the historian's
	// own variable, since alarm_events and samples belong in one place.
	DSNEnv string `yaml:"dsn-env"`
}

// AlarmNotifyConfig is one entry of `alarms.notify:`. Exactly one of the
// two forms per entry: `- log: true` or `- webhook: https://…`.
type AlarmNotifyConfig struct {
	// Log writes every event to the structured log — often the only
	// notifier a site needs, since an alarm journal that also lands in the
	// log stream is searchable by whatever already collects logs.
	Log bool `yaml:"log"`
	// Webhook POSTs each event as JSON to this URL.
	Webhook string `yaml:"webhook"`
	// HeaderEnv names an environment variable holding an API key, sent as
	// the header named by Header (default "Authorization"). Same rule as
	// the DSN: the secret is never in the file.
	Header    string `yaml:"header"`
	HeaderEnv string `yaml:"header-env"`
}

// DefaultAlarmDSNEnv / FallbackAlarmDSNEnv are where a postgres journal
// looks for its connection string when the manifest names no variable.
const (
	DefaultAlarmDSNEnv  = "NAUTILUS_ALARM_DB_URL"
	FallbackAlarmDSNEnv = "DATABASE_URL"
)

// alarmConfig is the section as the alarm package wants it.
func (c *AlarmsConfig) alarmConfig() *alarm.Config {
	return &alarm.Config{
		Defaults:    c.Defaults,
		ShelveTimes: c.ShelveTimes,
		Rules:       c.Rules,
		Defs:        c.Defs,
	}
}

// ShelveDurations is the offered shelf durations, defaulted.
func (c *AlarmsConfig) ShelveDurations() []time.Duration {
	return c.alarmConfig().ShelveDurations()
}

// validate rejects at LOAD time what would otherwise be silence at run
// time: an unusable glob, a mistyped placeholder, a site-from that is not
// a regexp. Expand does the first two; this does the third and is called
// from ReadManifest so `nautilus check` and the language server see it
// without compiling anything.
func (c *AlarmsConfig) validate() error {
	for _, p := range [][2]string{{"site-from", c.SiteFrom}, {"area-from", c.AreaFrom}} {
		if p[1] == "" {
			continue
		}
		re, err := regexp.Compile(p[1])
		if err != nil {
			return fmt.Errorf("alarms.%s: %w", p[0], err)
		}
		if re.NumSubexp() < 1 {
			return fmt.Errorf("alarms.%s: %q has no capture group — the FIRST group is "+
				"what becomes the value, e.g. \"^([A-Z]+[0-9]+)_\"", p[0], p[1])
		}
	}
	switch strings.ToLower(c.Journal.Sink) {
	case "", "ring", "file", "postgres":
	default:
		return fmt.Errorf("alarms.journal.sink %q: want file or postgres (omit it for the "+
			"in-memory ring, which is always on regardless)", c.Journal.Sink)
	}
	if strings.EqualFold(c.Journal.Sink, "file") && c.Journal.Path == "" {
		return errors.New("alarms.journal: sink: file needs path: (where the JSONL goes)")
	}
	for i, n := range c.Notify {
		if !n.Log && n.Webhook == "" {
			return fmt.Errorf("alarms.notify[%d]: an entry is either `log: true` or `webhook: <url>`", i)
		}
		if n.Log && n.Webhook != "" {
			return fmt.Errorf("alarms.notify[%d]: log and webhook are separate entries, not one", i)
		}
	}
	return nil
}

// composeAlarms folds alarm-files into the manifest's own `alarms:`
// section, exactly the way composeTags folds tag-files into tags:.
//
// Files are read in listed order and the manifest's own rules and defs
// come last, so a hand-written entry is the final word on ordering — and,
// as with tags, a duplicate id is an ERROR naming both sources rather than
// last-wins, which reads fine on the day it is written and rots silently
// once the generator that emitted one side changes.
func composeAlarms(fsys fs.FS, m *Manifest) error {
	if len(m.AlarmFiles) == 0 && m.Alarms == nil {
		return nil
	}
	if m.Alarms == nil {
		// `alarm-files:` with no `alarms:` is a complete configuration: the
		// files carry everything. Materialize the section so defaults,
		// journal and shelve times exist to be read.
		m.Alarms = &AlarmsConfig{}
	}
	for _, f := range m.AlarmFiles {
		p, err := projectPath(f)
		if err != nil {
			return fmt.Errorf("alarm-files: %w", err)
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("alarm-files: %w", err)
		}
		file, err := alarm.Load(strings.NewReader(string(raw)))
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("%s: %w", p, err)
		}
		// Prepended, not appended: a file's rules and defs are read BEFORE
		// the manifest's own, so `alarms:` is the last word — the same
		// order composeTags uses.
		m.Alarms.Rules = append(file.Rules, m.Alarms.Rules...)
		m.Alarms.Defs = append(file.Defs, m.Alarms.Defs...)
	}
	return m.Alarms.validate()
}

// ── composing definitions ──────────────────────────────────────────────

// AlarmDefs materializes the manifest's rules and definitions against the
// project's tags, over a compiled runtime — the count `nautilus check`
// prints and `nautilus alarms list` dumps.
//
// It needs the runtime because a rule matches a struct TYPE and a member,
// and a tag's shape is only knowable once the project's ST TYPE table has
// been compiled. It changes nothing and opens nothing, so `check` can call
// it as safely as `run` can.
func (p *Project) AlarmDefs(rt *runtime.Runtime) ([]alarm.Def, error) {
	if p.Alarms == nil {
		return nil, nil
	}
	return p.Alarms.alarmConfig().Compose(nil, p.alarmTagInfo(rt))
}

// alarmTagInfo describes every declared tag to the rule expander: its
// name, its type, its struct shape when it has one, and the metadata a
// name template interpolates.
//
// The universe is the MANIFEST's tags, not the runtime's whole global
// namespace: a rule's job is to claim declared conditions, and a scratch
// global a program happens to create is not one. Struct shape comes from
// the live value when the tag is seeded and from the compiled TYPE table
// when it is not (a typed INPUT is deliberately unseeded).
func (p *Project) alarmTagInfo(rt *runtime.Runtime) []alarm.TagInfo {
	site := captureFunc(p.Alarms.SiteFrom)
	area := captureFunc(p.Alarms.AreaFrom)
	types := rt.Types()
	tags := rt.Tags()

	out := make([]alarm.TagInfo, 0, len(p.Runtime.Tags))
	for _, d := range p.Runtime.Tags {
		info := alarm.TagInfo{
			Name:     d.Name,
			TypeName: d.Type,
			Desc:     d.Meta.Desc,
			Site:     site(d.Name),
			Area:     area(d.Name),
		}
		if v, err := tags.ReadGlobal(d.Name); err == nil && v.Kind == ir.TypeStruct {
			info.Struct = v.Struct
		} else if t, ok := types[d.Type]; ok && d.Type != "" && t.Kind == ir.TypeStruct {
			info.Struct = t.Struct
		}
		if info.TypeName == "" && info.Struct != nil {
			info.TypeName = info.Struct.Name
		}
		out = append(out, info)
	}
	return out
}

// captureFunc turns a site-from/area-from pattern into the function that
// applies it. A pattern that does not compile was already rejected by
// validate; here it degrades to "no site", never a panic.
func captureFunc(pattern string) func(string) string {
	if pattern == "" {
		return func(string) string { return "" }
	}
	re, err := regexp.Compile(pattern)
	if err != nil || re.NumSubexp() < 1 {
		return func(string) string { return "" }
	}
	return func(name string) string {
		if m := re.FindStringSubmatch(name); m != nil {
			return m[1]
		}
		return ""
	}
}

// CheckAlarms is `nautilus check`'s alarm pass: compose the definitions
// and cross-check every condition path against the project's tags.
//
// It returns the composed set, the problems that must be fixed, and the
// ones that merely look wrong. The split is the same judgment call
// checkManifest makes about tags:
//
//   - a path into a DECLARED struct tag that names a member the type does
//     not have, or a member that is not a BOOL, is an ERROR. The type is
//     right here in the project; nothing at run time can make `.HHH`
//     appear, and the alarm would sit Suppressed forever looking like a
//     quiet plant.
//   - a path whose ROOT tag this manifest does not declare is a WARNING.
//     On a Sparkplug host most tags arrive from the field and a site that
//     has never birthed legitimately has none — that is exactly the case
//     the engine Suppresses rather than faults on.
//   - a rule that claims nothing is a WARNING: dead, or a generator that
//     moved out from under it.
func (p *Project) CheckAlarms(rt *runtime.Runtime) (defs []alarm.Def, errs, warns []string, err error) {
	if p.Alarms == nil {
		return nil, nil, nil, nil
	}
	tags := p.alarmTagInfo(rt)
	if defs, err = p.Alarms.alarmConfig().Compose(nil, tags); err != nil {
		return nil, nil, nil, err
	}

	byName := make(map[string]alarm.TagInfo, len(tags))
	for _, t := range tags {
		byName[t.Name] = t
	}
	check := func(what, path string) {
		root, member, dotted := strings.Cut(path, ".")
		info, declared := byName[root]
		if !declared {
			warns = append(warns, fmt.Sprintf("%s watches %q, which %s declares no tag for — "+
				"correct if the tag arrives from the field, dead otherwise", what, path, ManifestName))
			return
		}
		if !dotted {
			return // a flat tag: the engine reports a non-BOOL at run time
		}
		if info.Struct == nil {
			errs = append(errs, fmt.Sprintf("%s watches %q, but %s is not a struct tag, "+
				"so it has no member %q", what, path, root, member))
			return
		}
		i, ok := info.Struct.FieldIndex[member]
		if !ok || i >= len(info.Struct.Fields) {
			errs = append(errs, fmt.Sprintf("%s watches %q, but %s has no member %q (%s has: %s)",
				what, path, root, member, info.Struct.Name, memberNames(info.Struct)))
			return
		}
		if f := info.Struct.Fields[i]; f.Type == nil || f.Type.Kind != ir.TypeBool {
			errs = append(errs, fmt.Sprintf("%s watches %q, which is not a BOOL — "+
				"an alarm condition is a bit that is true when the alarm is in", what, path))
		}
	}
	for _, d := range defs {
		check("alarm "+d.ID, d.Tag)
		if d.Enable != "" {
			check("alarm "+d.ID+"'s enable", d.Enable)
		}
	}

	// A rule claiming nothing: count what each one produced by re-running
	// it alone. Cheap (the tag list is already in hand) and it is the
	// question a reviewer asks first — "did my fourteen rules actually
	// match?"
	for i, r := range p.Alarms.Rules {
		got, rerr := alarm.Expand([]alarm.Rule{r}, nil, tags)
		if rerr != nil || len(got) > 0 {
			continue
		}
		warns = append(warns, fmt.Sprintf("alarms.rules[%d] (%s) matches no tag in this project — "+
			"dead, or a generator moved out from under it", i, ruleLabel(r)))
	}
	return defs, errs, warns, nil
}

func ruleLabel(r alarm.Rule) string {
	var parts []string
	for _, p := range [][2]string{{"type", r.Match.Type}, {"member", r.Match.Member}, {"tag", r.Match.Tag}} {
		if p[1] != "" {
			parts = append(parts, p[0]+": "+p[1])
		}
	}
	return "match " + strings.Join(parts, ", ")
}

func memberNames(sd *ir.StructDef) string {
	out := make([]string, 0, len(sd.Fields))
	for _, f := range sd.Fields {
		out = append(out, f.Name)
	}
	return strings.Join(out, ", ")
}

// ── building the engine ────────────────────────────────────────────────

// Alarms is a running alarm subsystem: the engine, what it was built from,
// and the sinks it owns. Close releases the durable ones and unregisters
// the scan hook; the engine itself keeps working (badly) without it, which
// is why Close is the caller's job and not a finalizer.
type Alarms struct {
	Engine      *alarm.Engine
	Defs        []alarm.Def
	ShelveTimes []time.Duration

	cancel  func()
	closers []io.Closer
}

// Close stops evaluation and releases the journal sinks.
func (a *Alarms) Close() error {
	if a == nil {
		return nil
	}
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	var errs []error
	if a.Engine != nil {
		errs = append(errs, a.Engine.Close())
	}
	for _, c := range a.closers {
		errs = append(errs, c.Close())
	}
	a.closers = nil
	return errors.Join(errs...)
}

// NewAlarms builds the manifest's alarm engine over a compiled runtime,
// opens the configured journal sink and notifiers, and registers
// evaluation on the runtime's post-scan hook. It returns (nil, nil) when
// the manifest declares no alarms — a project without them behaves
// exactly as it did before this existed.
//
// This is `nautilus run`'s call. It opens files and databases, so
// check/build/LSP use AlarmDefs instead, and the acceptance harness uses
// AlarmEngine.
func (p *Project) NewAlarms(rt *runtime.Runtime) (*Alarms, error) {
	return p.alarms(rt, true, nil)
}

// AlarmEngine is NewAlarms with nothing durable behind it: the in-memory
// ring journal, no notifiers, no files, no database. It is what `nautilus
// test` and the acceptance harness build, because a test that writes to
// the site's alarm database is not a test.
func (p *Project) AlarmEngine(rt *runtime.Runtime) (*alarm.Engine, error) {
	a, err := p.alarms(rt, false, nil)
	if err != nil || a == nil {
		return nil, err
	}
	return a.Engine, nil
}

func (p *Project) alarms(rt *runtime.Runtime, durable bool, log *slog.Logger) (*Alarms, error) {
	if p.Alarms == nil {
		return nil, nil
	}
	defs, err := p.AlarmDefs(rt)
	if err != nil {
		return nil, err
	}
	out := &Alarms{Defs: defs, ShelveTimes: p.Alarms.ShelveDurations()}

	journal := alarm.Journal(alarm.NewRing(p.Alarms.Journal.Keep))
	if durable {
		sink, closer, err := p.alarmSink()
		if err != nil {
			return nil, err
		}
		if sink != nil {
			journal = alarm.NewMulti(journal, sink)
			out.closers = append(out.closers, closer)
		}
	}

	var notify []alarm.Notifier
	if durable {
		if notify, err = p.alarmNotifiers(log); err != nil {
			return nil, err
		}
	}

	tags := rt.Tags()
	eng, err := alarm.New(alarm.Options{
		Defs: defs,
		Read: tags.ReadPath,
		// The runtime's clock, not time.Now: NowMs follows an injected
		// VirtualClock, so an acceptance test's `advance: 5m` walks an
		// on-delay exactly rather than never.
		Now:     func() time.Time { return time.UnixMilli(tags.NowMs()) },
		Journal: journal,
		Keep:    p.Alarms.Journal.Keep,
		Notify:  notify,
		Log:     log,
	})
	if err != nil {
		out.Close()
		return nil, err
	}
	out.Engine = eng
	// Evaluate after each main scan, not on a ticker: the store is
	// consistent there, and a wall-clock ticker inside the engine would
	// never fire against a stopped virtual clock.
	out.cancel = rt.OnScan(func(*runtime.Tags) { eng.Evaluate() })
	return out, nil
}

// alarmSink opens the configured durable journal, or (nil, nil, nil) for
// the ring alone.
func (p *Project) alarmSink() (alarm.Journal, io.Closer, error) {
	j := p.Alarms.Journal
	switch strings.ToLower(j.Sink) {
	case "", "ring":
		return nil, nil, nil
	case "file":
		path, err := projectPath(j.Path)
		if err != nil {
			return nil, nil, fmt.Errorf("alarms.journal.path: %w", err)
		}
		f, err := alarm.NewFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf("alarms.journal: %w", err)
		}
		return f, f, nil
	case "postgres":
		env := j.DSNEnv
		if env == "" {
			env = DefaultAlarmDSNEnv
		}
		dsn := os.Getenv(env)
		if dsn == "" && j.DSNEnv == "" {
			dsn = os.Getenv(FallbackAlarmDSNEnv)
		}
		if dsn == "" {
			return nil, nil, fmt.Errorf("alarms.journal: sink: postgres, but %s is empty — "+
				"the connection string is an environment variable, never the manifest", env)
		}
		pg, err := alarm.NewPostgres(dsn)
		if err != nil {
			return nil, nil, fmt.Errorf("alarms.journal: %w", err)
		}
		return pg, pg, nil
	}
	return nil, nil, fmt.Errorf("alarms.journal.sink %q is not one of file, postgres", j.Sink)
}

func (p *Project) alarmNotifiers(log *slog.Logger) ([]alarm.Notifier, error) {
	var out []alarm.Notifier
	for i, n := range p.Alarms.Notify {
		switch {
		case n.Log:
			out = append(out, alarm.NewLogNotifier(log))
		case n.Webhook != "":
			o := alarm.WebhookOptions{}
			if n.HeaderEnv != "" {
				name := n.Header
				if name == "" {
					name = "Authorization"
				}
				v := os.Getenv(n.HeaderEnv)
				if v == "" {
					return nil, fmt.Errorf("alarms.notify[%d]: header-env %s is empty", i, n.HeaderEnv)
				}
				o.Headers = map[string]string{name: v}
			}
			out = append(out, alarm.NewWebhook(n.Webhook, o))
		}
	}
	return out, nil
}
