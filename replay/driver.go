package replay

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"path"
	"sort"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/hw"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// Driver delivers a Manifest's tags from its recording at the replay clock.
// It implements io.Driver plus the optional surfaces the runtime and the
// multi-driver look for (InputNames, OutputNames, StructDefs, Quality,
// Start/Stop).
type Driver struct {
	m    Manifest
	fsys fs.FS
	log  *slog.Logger
	now  func() time.Time

	mu      sync.Mutex
	h       *History
	loadErr error
	loaded  bool
	clock   Clock
	matched map[string]int // tag → members found in the recording
	reads   uint64
}

// Option configures a Driver.
type Option func(*Driver)

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(d *Driver) { d.log = l } }

// WithClock replaces time.Now (tests drive virtual time with it).
func WithClock(now func() time.Time) Option { return func(d *Driver) { d.now = now } }

// WithHistory supplies the recording directly instead of reading
// m.History from the project (tests).
func WithHistory(h *History) Option {
	return func(d *Driver) { d.h, d.loaded = h, true }
}

// New builds the driver. Nothing is read yet: the recording loads on Start
// (or the first read), so a project whose recording is absent still
// checks and builds.
func New(m Manifest, fsys fs.FS, opts ...Option) (*Driver, error) {
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("replay: %w", err)
	}
	d := &Driver{m: m, fsys: fsys, log: slog.Default(), now: time.Now}
	for _, o := range opts {
		o(d)
	}
	d.clock.Speed = 1
	return d, nil
}

// Start loads the recording.
func (d *Driver) Start(context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.loadLocked()
}

// Stop is a no-op: the driver has no goroutines.
func (d *Driver) Stop() {}

func (d *Driver) loadLocked() {
	if d.h == nil && !d.loaded {
		d.loaded = true
		f, err := d.fsys.Open(path.Clean(d.m.History))
		if err != nil {
			d.loadErr = fmt.Errorf("replay: %s: %w", d.m.History, err)
			d.log.Error("replay: cannot read the recording; its tags stay NotConnected", "history", d.m.History, "error", err)
			return
		}
		defer f.Close()
		h, err := LoadHistory(f)
		if err != nil {
			d.loadErr = fmt.Errorf("replay: %s: %w", d.m.History, err)
			d.log.Error("replay: cannot load the recording", "history", d.m.History, "error", err)
			return
		}
		d.h = h
	}
	if d.h == nil || d.matched != nil {
		return
	}
	d.matched = map[string]int{}
	for _, t := range d.m.Tags {
		ht, _ := hw.TypeByName(t.Type)
		for _, f := range ht.Fields {
			if _, ok := d.h.Series[t.Series+"."+f.Name]; ok {
				d.matched[t.Name]++
			}
		}
		if d.matched[t.Name] == 0 {
			d.log.Warn("replay: no series in the recording for this tag; it delivers its constants only", "tag", t.Name, "series", t.Series)
		}
	}
	from, to := float64(d.h.T0), float64(d.h.End())
	if !d.m.From.IsZero() {
		from = math.Max(from, float64(d.m.From.Unix()))
	}
	if !d.m.To.IsZero() {
		to = math.Min(to, float64(d.m.To.Unix()))
	}
	start := from
	if !d.m.Start.IsZero() {
		start = float64(d.m.Start.Unix())
	}
	d.clock.From, d.clock.To = from, to
	d.clock.Seek(start)
	d.log.Info("replay: loaded", "history", d.m.History, "series", len(d.h.Series),
		"from", time.Unix(int64(from), 0).UTC(), "to", time.Unix(int64(to), 0).UTC())
}

// InputNames are the replayed tags and the clock's read-backs.
func (d *Driver) InputNames() []string {
	out := []string{TagAt, TagFrom, TagTo}
	for _, t := range d.m.Tags {
		out = append(out, t.Name)
	}
	return out
}

// OutputNames are the clock's controls.
func (d *Driver) OutputNames() []string { return []string{TagSpeed, TagPause, TagSeek} }

// StructDefs are the contract types the replayed tags use.
func (d *Driver) StructDefs() map[string]*ir.StructDef {
	out := map[string]*ir.StructDef{}
	for _, t := range d.m.Tags {
		out[t.Type] = hw.StructDef(t.Type)
	}
	return out
}

// ReadInputs delivers every tag at the clock's current position.
func (d *Driver) ReadInputs() (nio.Values, error) {
	out := nio.Values{}
	return out, d.ReadInputsInto(out)
}

// ReadInputsInto is ReadInputs into the runtime's map.
func (d *Driver) ReadInputsInto(dst nio.Values) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.loadLocked()
	if d.h == nil {
		// No recording: deliver every tag at its constants and zero, and
		// the clock at 0, so the store holds a value of the right shape —
		// a program that reads Rec_X must not fault — and Quality says
		// NotConnected. Replay_At = 0 is how the program tells.
		dst[TagAt], dst[TagFrom], dst[TagTo] = int64(0), int64(0), int64(0)
		for _, tag := range d.m.Tags {
			v, err := d.value(tag, 0)
			if err != nil {
				return err
			}
			dst[tag.Name] = v
		}
		return nil
	}
	d.reads++
	t := d.clock.Advance(d.now())
	dst[TagAt] = int64(t)
	dst[TagFrom] = int64(d.clock.From)
	dst[TagTo] = int64(d.clock.To)
	for _, tag := range d.m.Tags {
		v, err := d.value(tag, t)
		if err != nil {
			return err
		}
		dst[tag.Name] = v
	}
	return nil
}

// value is one tag at recorded time t: the recording where it has the
// member, the manifest's constant where it does not, zero otherwise.
func (d *Driver) value(tag Tag, t float64) (ir.Value, error) {
	v, _ := hw.Zero(tag.Type)
	v.Fld = append([]ir.Value(nil), v.Fld...)
	ht, _ := hw.TypeByName(tag.Type)
	for i, f := range ht.Fields {
		if x, ok := d.recorded(tag.Series+"."+f.Name, t); ok {
			var raw any = x
			if f.Kind == ir.TypeBool {
				raw = x >= 0.5
			}
			fv, err := hw.Coerce(raw, f)
			if err != nil {
				return ir.Value{}, fmt.Errorf("replay: %s.%s: %w", tag.Name, f.Name, err)
			}
			v.Fld[i] = fv
			continue
		}
		if c, ok := tag.Const[f.Name]; ok {
			fv, err := hw.Coerce(c, f)
			if err != nil {
				return ir.Value{}, fmt.Errorf("replay: %s.%s: %w", tag.Name, f.Name, err)
			}
			v.Fld[i] = fv
		}
	}
	return v, nil
}

// recorded is the series at t, or false with no recording (constants only).
func (d *Driver) recorded(name string, t float64) (float64, bool) {
	if d.h == nil {
		return 0, false
	}
	return d.h.At(name, t)
}

// WriteOutputs takes the clock controls.
func (d *Driver) WriteOutputs(vals nio.Values) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	d.clock.Advance(now) // settle the old rate up to now before changing it
	if v, ok := vals[TagSpeed]; ok {
		if s, ok := number(v); ok {
			d.clock.Speed = math.Max(s, 0)
		}
	}
	if v, ok := vals[TagPause]; ok {
		if b, ok := v.(bool); ok {
			d.clock.Pause = b
		} else if iv, ok := v.(ir.Value); ok && iv.Kind == ir.TypeBool {
			d.clock.Pause = iv.B
		}
	}
	if v, ok := vals[TagSeek]; ok {
		if s, ok := number(v); ok && s > 0 && int64(s) != d.clock.lastSeek {
			d.clock.lastSeek = int64(s)
			d.clock.Seek(s)
		}
	}
	return nil
}

// Quality reads NotConnected on every replayed tag until the recording has
// loaded: nothing has been delivered, and nothing will be until it does.
func (d *Driver) Quality() map[string]nio.Quality {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.h != nil {
		return nil
	}
	out := map[string]nio.Quality{}
	for _, n := range d.InputNames() {
		out[n] = nio.NotConnected
	}
	return out
}

// Health is the status row: where the clock is, how fast, and whether the
// recording loaded.
type Health struct {
	History   string
	Loaded    bool
	LastError string
	Tags      int
	Matched   int
	Series    int
	AtS       int64
	FromS     int64
	ToS       int64
	Speed     float64
	Paused    bool
	Loops     int
	Reads     uint64
}

// Health reports the driver's state.
func (d *Driver) Health() Health {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := Health{History: d.m.History, Loaded: d.h != nil, Tags: len(d.m.Tags), Speed: d.clock.Speed,
		Paused: d.clock.Pause, Loops: d.clock.Loops, Reads: d.reads}
	if d.loadErr != nil {
		h.LastError = d.loadErr.Error()
	}
	if d.h != nil {
		h.Series = len(d.h.Series)
		h.AtS, h.FromS, h.ToS = int64(d.clock.Pos), int64(d.clock.From), int64(d.clock.To)
	}
	names := make([]string, 0, len(d.matched))
	for n, c := range d.matched {
		if c > 0 {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	h.Matched = len(names)
	return h
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case ir.Value:
		switch x.Kind {
		case ir.TypeReal:
			return x.F, true
		case ir.TypeInt:
			return float64(x.I), true
		}
	}
	return 0, false
}
