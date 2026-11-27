package facade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/siemens/webapi"
)

// ── discovery ───────────────────────────────────────────────────────────

// types maps the Web API's datatype names to the IEC types the facade
// binds, and how their JSON values decode.
var types = map[string]string{
	"bool": "BOOL",
	"sint": "SINT", "int": "INT", "dint": "DINT", "lint": "LINT",
	"usint": "USINT", "uint": "UINT", "udint": "UDINT", "ulint": "ULINT",
	"byte": "BYTE", "word": "WORD", "dword": "DWORD", "lword": "LWORD",
	"real": "REAL", "lreal": "LREAL",
	"time": "TIME",
}

// maxDepth bounds the browse into structs and instances.
const maxDepth = 4

// Discover browses the roots and returns what it can bind, and what it
// skipped with why. A root that does not exist is an error: it names the
// tags DB or a program the project says is there.
func Discover(ctx context.Context, c *webapi.Client, roots []Root) ([]Binding, []string, error) {
	bs, skipped, _, err := discover(ctx, c, roots)
	return bs, skipped, err
}

// discover is Discover, also counting the members whose browse failed: a
// re-discovery that could not browse all of the CPU (one in the middle of
// a download) is not a layout to adopt.
func discover(ctx context.Context, c *webapi.Client, roots []Root) ([]Binding, []string, int, error) {
	var bs []Binding
	var skipped []string
	failed := 0
	for _, r := range roots {
		sym, err := webapi.Symbol(r.DB)
		if err != nil {
			return nil, nil, 0, err
		}
		if err := walk(ctx, c, sym, r.Prefix, 0, &bs, &skipped, &failed); err != nil {
			return nil, nil, 0, fmt.Errorf("browse %s: %w", sym, err)
		}
	}
	return bs, skipped, failed, nil
}

func walk(ctx context.Context, c *webapi.Client, sym, name string, depth int, bs *[]Binding, skipped *[]string, failed *int) error {
	vs, err := c.Browse(ctx, sym, false)
	if err != nil {
		return err
	}
	for _, v := range vs {
		child := sym + "." + memberSymbol(v.Name)
		cname := v.Name
		if name != "" {
			cname = name + "." + v.Name
		}
		if v.HasChildren || strings.EqualFold(v.Datatype, "struct") {
			if depth+1 >= maxDepth {
				*skipped = append(*skipped, cname+": nested deeper than the facade browses")
				continue
			}
			if err := walk(ctx, c, child, cname, depth+1, bs, skipped, failed); err != nil {
				*failed++
				*skipped = append(*skipped, fmt.Sprintf("%s: %v", cname, err))
			}
			continue
		}
		if len(v.ArrayDimensions) > 0 {
			*skipped = append(*skipped, cname+": arrays are not bound yet")
			continue
		}
		t, ok := types[strings.ToLower(v.Datatype)]
		if !ok {
			*skipped = append(*skipped, fmt.Sprintf("%s: type %s is not bound yet", cname, v.Datatype))
			continue
		}
		*bs = append(*bs, Binding{Name: cname, Symbol: child, Type: t, Parent: sym, Member: v.Name})
	}
	return nil
}

// memberSymbol quotes a member name that is not a plain identifier.
func memberSymbol(n string) string {
	if s, err := webapi.Symbol("x", n); err == nil {
		return strings.TrimPrefix(s, `"x".`)
	}
	return n
}

// ── values ──────────────────────────────────────────────────────────────

// decode turns a read's JSON into the Go value the tag store holds for
// the type: bool, int64, float64.
func decode(t string, raw json.RawMessage) (any, error) {
	switch {
	case t == "BOOL":
		var b bool
		err := json.Unmarshal(raw, &b)
		return b, err
	case t == "REAL" || t == "LREAL":
		var f float64
		err := json.Unmarshal(raw, &f)
		return f, err
	case t == "TIME":
		return decodeTime(raw)
	default: // integers and bit strings
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, err
		}
		i, err := n.Int64()
		if err != nil {
			return nil, fmt.Errorf("%s value %s is not a whole number", t, raw)
		}
		return i, nil
	}
}

// decodeTime reads a TIME as the milliseconds naut run shows it as (a
// TON's PT of T#2s is 2000). The Web API's JSON for TIME is not measured
// on a CPU yet: a number is taken as milliseconds, and an IEC literal
// (T#2s, T#1m_30s_5ms) is read as one.
func decodeTime(raw json.RawMessage) (any, error) {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		if i, err := n.Int64(); err == nil {
			return i, nil
		}
		if f, err := n.Float64(); err == nil && f == math.Trunc(f) {
			return int64(f), nil
		}
		return nil, fmt.Errorf("TIME value %s is not a whole number of milliseconds", raw)
	}
	var str string
	if err := json.Unmarshal(raw, &str); err != nil {
		return nil, fmt.Errorf("TIME value %s is neither milliseconds nor a TIME literal", raw)
	}
	ms, ok := timeLiteralMs(str)
	if !ok {
		return nil, fmt.Errorf("TIME value %q is neither milliseconds nor a TIME literal", str)
	}
	return ms, nil
}

// timeLiteralMs reads T#[-]1d2h3m4s5ms (any case, "_" ignored, TIME# too)
// as milliseconds.
func timeLiteralMs(s string) (int64, bool) {
	u := strings.ToLower(strings.ReplaceAll(s, "_", ""))
	switch {
	case strings.HasPrefix(u, "time#"):
		u = u[5:]
	case strings.HasPrefix(u, "t#"):
		u = u[2:]
	default:
		return 0, false
	}
	neg := strings.HasPrefix(u, "-")
	u = strings.TrimPrefix(u, "-")
	if u == "" {
		return 0, false
	}
	unit := map[string]int64{"d": 86400000, "h": 3600000, "m": 60000, "s": 1000, "ms": 1}
	var total int64
	for u != "" {
		i := 0
		for i < len(u) && u[i] >= '0' && u[i] <= '9' {
			i++
		}
		if i == 0 {
			return 0, false
		}
		n, err := strconv.ParseInt(u[:i], 10, 64)
		if err != nil {
			return 0, false
		}
		u = u[i:]
		name := ""
		switch {
		case strings.HasPrefix(u, "ms"):
			name = "ms"
		case u != "" && strings.ContainsRune("dhms", rune(u[0])):
			name = u[:1]
		default:
			return 0, false
		}
		u = u[len(name):]
		total += n * unit[name]
	}
	if neg {
		total = -total
	}
	return total, true
}

var intRange = map[string][2]float64{
	"SINT": {-128, 127}, "INT": {-32768, 32767}, "DINT": {-2147483648, 2147483647},
	"USINT": {0, 255}, "UINT": {0, 65535}, "UDINT": {0, 4294967295},
	"BYTE": {0, 255}, "WORD": {0, 65535}, "DWORD": {0, 4294967295},
}

// convertWrite checks a written value against the tag's type and returns
// what the Web API is sent. A value outside the type's range is refused:
// the CPU's own answer to one is not pinned down, and nautilus's would be
// a wrap the caller did not ask for.
func convertWrite(t string, v any) (any, error) {
	switch t {
	case "BOOL":
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return nil, errors.New("BOOL takes true or false")
	case "TIME":
		// The Web API's JSON for TIME is not pinned down on a CPU yet
		// (#789): a write could land as a different duration.
		return nil, errors.New("TIME is not written over the Web API yet: the encoding the CPU takes is not measured (#789)")
	case "REAL", "LREAL":
		f, ok := number(v)
		if !ok {
			return nil, fmt.Errorf("%s takes a number", t)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%s cannot be sent NaN or infinity over the Web API", t)
		}
		if t == "REAL" && math.Abs(f) > math.MaxFloat32 {
			return nil, fmt.Errorf("%v is outside REAL (a 32-bit float, at most %g)", v, math.MaxFloat32)
		}
		return f, nil
	}
	f, ok := number(v)
	if !ok || f != math.Trunc(f) {
		return nil, fmt.Errorf("%s takes a whole number", t)
	}
	if r, bounded := intRange[t]; bounded && (f < r[0] || f > r[1]) {
		return nil, fmt.Errorf("%v is outside %s (%.0f to %.0f)", v, t, r[0], r[1])
	}
	if (t == "ULINT" || t == "LWORD") && f < 0 {
		return nil, fmt.Errorf("%v is outside %s", v, t)
	}
	if math.Abs(f) > 1<<53 {
		return nil, fmt.Errorf("%v is too large to send exactly", v)
	}
	return int64(f), nil
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// ── the driver ──────────────────────────────────────────────────────────

// Driver polls the bound symbols into the values the runtime reads. It
// implements io.Driver and io.QualityReporter; the runtime's outputs are
// empty, so WriteOutputs has nothing to do (writes go through the
// facade's TagWriter).
//
// Values are held: a tag whose read fails keeps its last value, and its
// quality says why it is no longer current. A failed poll (the CPU
// unreachable, the login refused) is retried with an exponential backoff,
// so a CPU that restarted or changed its users is not asked for a login
// on every poll.
type Driver struct {
	c    *webapi.Client
	bs   []Binding
	poll time.Duration
	// BackoffMin and BackoffMax bound the wait after a failed poll.
	// Defaults: one poll period (at least 1 s), and 30 s.
	BackoffMin, BackoffMax time.Duration
	now                    func() time.Time
	// tags, when set, are the bindings that are runtime tags: Quality
	// reports only those (the rest are program locals, which carry no
	// quality in a frame).
	tags map[string]bool
	// onGone, when set, is called when a read says a bound symbol no
	// longer exists, or reads a value its bound type cannot hold (a
	// download changed the program): the facade then browses the CPU
	// again.
	onGone func()

	mu        sync.Mutex
	values    nio.Values      // last good value per tag
	failing   map[string]bool // tags whose last read failed
	connected bool            // the last poll reached the CPU
	readOnce  bool
	lastErr   string
	since     time.Time
	backoff   time.Duration
	nextTry   time.Time
}

// NewDriver builds a driver over the bindings; Run starts it.
func NewDriver(c *webapi.Client, bs []Binding, poll time.Duration) *Driver {
	min := poll
	if min < time.Second {
		min = time.Second
	}
	return &Driver{c: c, bs: bs, poll: poll, BackoffMin: min, BackoffMax: 30 * time.Second,
		now: time.Now, values: nio.Values{}, failing: map[string]bool{}, since: time.Now()}
}

// Run polls until ctx ends.
func (d *Driver) Run(ctx context.Context) {
	t := time.NewTicker(d.poll)
	defer t.Stop()
	for {
		if d.due() {
			d.PollOnce(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// due reports whether the backoff after a failed poll has run out.
func (d *Driver) due() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.now().Before(d.nextTry)
}

// PollOnce reads every bound symbol once.
func (d *Driver) PollOnce(ctx context.Context) {
	syms := make([]string, len(d.bs))
	for i, b := range d.bs {
		syms[i] = b.Symbol
	}
	rs, err := d.c.ReadMany(ctx, syms)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		if d.connected || d.lastErr == "" {
			d.since = d.now()
		}
		d.connected = false
		d.lastErr = err.Error()
		if d.backoff == 0 {
			d.backoff = d.BackoffMin
		} else {
			d.backoff *= 2
		}
		if d.backoff > d.BackoffMax {
			d.backoff = d.BackoffMax
		}
		d.nextTry = d.now().Add(d.backoff)
		return
	}
	if !d.connected {
		d.since = d.now()
	}
	d.connected, d.lastErr, d.readOnce = true, "", true
	d.backoff, d.nextTry = 0, time.Time{}
	for i, r := range rs {
		name := d.bs[i].Name
		if r.Err != nil {
			d.failing[name] = true
			d.lastErr = fmt.Sprintf("read %s: %v", d.bs[i].Symbol, r.Err)
			if d.onGone != nil && webapi.Code(r.Err) == webapi.CodeAddressDoesNotExist {
				d.onGone()
			}
			continue // the last value is held; Quality says it is Bad
		}
		v, err := decode(d.bs[i].Type, r.Value)
		if err != nil {
			d.failing[name] = true
			d.lastErr = fmt.Sprintf("read %s: %v", d.bs[i].Symbol, err)
			if d.onGone != nil { // retyped under us, most likely
				d.onGone()
			}
			continue
		}
		delete(d.failing, name)
		d.values[name] = v
	}
}

// ReadInputs returns the held values, or an error until the first poll has
// reached the CPU. After that it never fails: Quality says which values
// are no longer current, and why.
func (d *Driver) ReadInputs() (nio.Values, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.readOnce {
		if d.lastErr != "" {
			return nil, errors.New(d.lastErr)
		}
		return nil, errors.New("siemens: not read yet")
	}
	out := make(nio.Values, len(d.values))
	for k, v := range d.values {
		out[k] = v
	}
	return out, nil
}

// WriteOutputs has nothing to write: the CPU runs the program.
func (d *Driver) WriteOutputs(nio.Values) error { return nil }

// Quality reports the non-Good tags (io.QualityReporter): NotConnected for
// a tag never read, Bad for one whose last read failed, Stale for every
// held value while the CPU cannot be reached.
func (d *Driver) Quality() map[string]nio.Quality {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out map[string]nio.Quality
	for _, b := range d.bs {
		if d.tags != nil && !d.tags[b.Name] {
			continue
		}
		_, held := d.values[b.Name]
		var q nio.Quality
		switch {
		case !held:
			q = nio.NotConnected
		case !d.connected:
			q = nio.Stale
		case d.failing[b.Name]:
			q = nio.Bad
		default:
			continue
		}
		if out == nil {
			out = map[string]nio.Quality{}
		}
		out[b.Name] = q
	}
	return out
}

var _ nio.QualityReporter = (*Driver)(nil)

// Health is the driver's state for the status panel.
type Health struct {
	Connected bool
	Tags      int
	Failing   int
	LastError string
	SinceMs   int64
}

// Health reports the poll state.
func (d *Driver) Health() Health {
	d.mu.Lock()
	defer d.mu.Unlock()
	return Health{Connected: d.connected, Tags: len(d.bs), Failing: len(d.failing), LastError: d.lastErr,
		SinceMs: d.since.UnixMilli()}
}
