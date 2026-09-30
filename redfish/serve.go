// serve.go is the manifest read backwards, for a stand-in BMC serving a
// plant simulation (`naut redfish serve --from`): which tag member feeds
// each resource path, and how a member's value is written into the body
// the driver will read it back from. The manifest the monitoring project
// polls with is the one description of the BMC both ways — the same
// resources, paths, eq/map/scale, exists and agg (snmp/serve.go is the
// SNMP half; hw.Binding.Invert is the shared arithmetic).
package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/redfish/mockup"
)

// Feed is one member a source's manifest binds to a resource path.
type Feed struct {
	Tag      string
	Member   string
	Field    hw.Field
	Binding  MemberBinding
	Resource string
	Path     *Path
	// Shadowed names other value members bound to the same resource path:
	// they read what this one writes, and are not fed themselves.
	Shadowed []string
}

// Feeds lists the members source's tags bind, value members before
// exists: members (an exists: member only ever removes a value — a
// sensor's Fault is its Reading gone — so it must run after the member
// that writes the reading). Two value members on one path: the first by
// name feeds it.
func (m Manifest) Feeds(source string) ([]Feed, error) {
	found := false
	for _, s := range m.Sources {
		found = found || s.ID == source
	}
	if !found {
		return nil, fmt.Errorf("redfish: no source %q in the manifest", source)
	}
	var values, exists []Feed
	owner := map[string]int{} // resource+path → index in values
	for _, t := range m.Tags {
		if t.Source != source {
			continue
		}
		names := make([]string, 0, len(t.Members))
		for n := range t.Members {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			b := t.Members[name]
			if b.IsStatic() {
				continue
			}
			_, f, ok := hw.FieldOf(t.Type, name)
			if !ok {
				return nil, fmt.Errorf("redfish: tag %s: type %s has no member %q", t.Name, t.Type, name)
			}
			res := b.Resource
			if res == "" {
				res = t.Resource
			}
			p, err := ParsePath(b.Path)
			if err != nil {
				return nil, fmt.Errorf("redfish: tag %s: member %s: %w", t.Name, name, err)
			}
			fd := Feed{Tag: t.Name, Member: name, Field: f, Binding: b, Resource: mockup.Normalize(res), Path: p}
			if b.Exists != nil {
				exists = append(exists, fd)
				continue
			}
			key := fd.Resource + "#" + b.Path
			if i, ok := owner[key]; ok {
				values[i].Shadowed = append(values[i].Shadowed, t.Name+"."+name)
				continue
			}
			owner[key] = len(values)
			values = append(values, fd)
		}
	}
	return append(values, exists...), nil
}

// ErrNotServable is a member no body can be written for: a rate (it would
// need a counter the plant does not keep), a sum or count aggregate.
var ErrNotServable = errors.New("not servable from a plant value")

// Serve writes v into doc (the member's resource, decoded) so the driver
// reads v back.
func (f Feed) Serve(doc map[string]any, v ir.Value) error {
	b := f.Binding
	switch {
	case b.Exists != nil:
		if v.Kind != ir.TypeBool {
			return fmt.Errorf("%s.%s: exists: wants a BOOL", f.Tag, f.Member)
		}
		if v.B == *b.Exists {
			return nil // "there": the value member wrote it, or the recording has it
		}
		err := f.Path.Map(doc, func(int, any) any { return Deleted })
		if errors.Is(err, ErrNoTarget) {
			return nil // already not there
		}
		return err
	case b.Rate:
		return fmt.Errorf("%s.%s: rate: %w", f.Tag, f.Member, ErrNotServable)
	case b.Agg == "max" || b.Agg == "min":
		// The aggregate reads v when its extreme element is v and none
		// passes it: move the extreme, clamp the rest.
		want, ok := number(v)
		if !ok {
			return fmt.Errorf("%s.%s: agg: wants a number", f.Tag, f.Member)
		}
		vals, err := f.Path.Eval(doc)
		if err != nil || len(vals) == 0 {
			return err
		}
		ext, best := 0, math.NaN()
		for i, x := range vals {
			if r, ok := toRaw(x); ok {
				if fx, ok := rawFloat(r); ok && (math.IsNaN(best) || (b.Agg == "max") == (fx > best)) {
					ext, best = i, fx
				}
			}
		}
		if best == want {
			return nil
		}
		return f.Path.Map(doc, func(i int, old any) any {
			if i == ext {
				return jsonNumber(want)
			}
			r, ok := toRaw(old)
			fx, isNum := rawFloat(r)
			if !ok || !isNum {
				return old
			}
			if b.Agg == "max" {
				return jsonNumber(math.Min(fx, want))
			}
			return jsonNumber(math.Max(fx, want))
		})
	case b.Agg != "":
		return fmt.Errorf("%s.%s: agg %s: %w", f.Tag, f.Member, b.Agg, ErrNotServable)
	}
	vals, err := f.Path.Eval(doc)
	if err != nil {
		return err
	}
	cur := zeroRaw(f.Field)
	if len(vals) > 0 {
		if r, ok := toRaw(vals[0]); ok {
			cur = r
		}
	}
	raw, err := b.Binding.Invert(f.Field, v, cur)
	if err != nil {
		return fmt.Errorf("%s.%s: %w", f.Tag, f.Member, err)
	}
	if len(vals) > 0 && raw == cur {
		return nil
	}
	return f.Path.Map(doc, func(int, any) any { return fromRaw(raw) })
}

// zeroRaw is the wire kind a missing property is written as.
func zeroRaw(f hw.Field) hw.Raw {
	switch f.Kind {
	case ir.TypeString:
		return hw.RawStringVal("")
	case ir.TypeBool:
		return hw.RawBoolVal(false)
	case ir.TypeInt:
		return hw.RawIntVal(0)
	}
	return hw.RawFloatVal(0)
}

// fromRaw is toRaw backwards: the JSON value a BMC would put in the body.
func fromRaw(r hw.Raw) any {
	switch r.Kind {
	case hw.RawInt:
		return json.Number(strconv.FormatInt(r.I, 10))
	case hw.RawUint:
		return json.Number(strconv.FormatUint(r.U, 10))
	case hw.RawFloat:
		return jsonNumber(r.F)
	case hw.RawBool:
		return r.B
	}
	return r.S
}

func jsonNumber(f float64) json.Number {
	return json.Number(strconv.FormatFloat(f, 'g', -1, 64))
}

func rawFloat(r hw.Raw) (float64, bool) {
	switch r.Kind {
	case hw.RawInt:
		return float64(r.I), true
	case hw.RawUint:
		return float64(r.U), true
	case hw.RawFloat:
		return r.F, true
	}
	return 0, false
}

func number(v ir.Value) (float64, bool) {
	switch v.Kind {
	case ir.TypeReal:
		return v.F, true
	case ir.TypeInt:
		return float64(v.I), true
	}
	return 0, false
}

// Plant makes a stand-in BMC answer from a plant simulation: every member
// the manifest binds is written into its resource from the plant's live
// tag of the same name. Members and resources the plant does not have
// keep the recording. The root tag's Online member (Server.Online) is the
// plant's "BMC dark": false and the stand-in stops listening.
type Plant struct {
	Server *mockup.Server
	Feeds  []Feed
	Online string
	Log    *slog.Logger

	errs   map[string]bool
	dark   bool
	failed bool
}

// NewPlant binds a stand-in to source's tags in m.
func NewPlant(s *mockup.Server, m Manifest, source string) (*Plant, error) {
	feeds, err := m.Feeds(source)
	if err != nil {
		return nil, err
	}
	online := ""
	for _, t := range m.Tags {
		if t.Source == source {
			if _, _, ok := hw.FieldOf(t.Type, "Online"); ok {
				online = t.Name
				break
			}
		}
	}
	// The root's Online is the dark switch, never a value to write: a
	// manifest that binds it (exists: on the system's Id) must not have
	// the plant delete the Id to say "offline".
	kept := feeds[:0]
	for _, f := range feeds {
		if !(f.Tag == online && f.Member == "Online") {
			kept = append(kept, f)
		}
	}
	return &Plant{Server: s, Feeds: kept, Online: online, Log: slog.Default()}, nil
}

// Apply serves one snapshot of the plant's tags ({tag: {member: value}}).
// It returns how many members took a plant value.
func (p *Plant) Apply(tags map[string]any) int {
	if p.errs == nil {
		p.errs = map[string]bool{}
	}
	byRes := map[string][]Feed{}
	var order []string
	for _, f := range p.Feeds {
		if _, ok := byRes[f.Resource]; !ok {
			order = append(order, f.Resource)
		}
		byRes[f.Resource] = append(byRes[f.Resource], f)
	}
	n := 0
	for _, res := range order {
		err := p.Server.Edit(res, func(doc map[string]any) error {
			for _, f := range byRes[res] {
				raw, ok := plantMember(tags, f.Tag, f.Member)
				if !ok {
					continue
				}
				v, err := hw.Coerce(raw, f.Field)
				if err != nil {
					p.once(fmt.Sprintf("%s.%s: %v", f.Tag, f.Member, err))
					continue
				}
				if err := f.Serve(doc, v); err != nil {
					p.once(err.Error())
					continue
				}
				n++
			}
			return nil
		})
		if err != nil {
			p.once(fmt.Sprintf("%s: %v", res, err))
		}
	}
	if p.Online != "" {
		if on, ok := plantMember(tags, p.Online, "Online"); ok {
			dark := on == false
			if dark != p.dark {
				if err := p.Server.SetDark(dark); err != nil {
					p.once(fmt.Sprintf("%s: %v", p.Online, err))
				} else {
					p.dark = dark
					p.Log.Info("redfish stand-in: plant says the BMC is "+map[bool]string{true: "dark: not listening", false: "back: listening"}[dark], "tag", p.Online)
				}
			}
		}
	}
	return n
}

// Run polls the plant every interval until ctx ends.
func (p *Plant) Run(ctx context.Context, fetch func(context.Context) (map[string]any, error), interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		tags, err := fetch(ctx)
		switch {
		case err != nil && !p.failed:
			p.failed = true
			p.Log.Warn("redfish stand-in: plant not answering; serving the last values", "error", err)
		case err == nil:
			if p.failed {
				p.failed = false
				p.Log.Info("redfish stand-in: plant answering again")
			}
			p.Apply(tags)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// TagPatterns are /api/state filters covering source's tags: "NODE1" and
// "NODE1_*". nil past the controller's cap of 40.
func (m Manifest) TagPatterns(source string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range m.Tags {
		if t.Source != source {
			continue
		}
		p := t.Name
		if i := strings.IndexByte(p, '_'); i > 0 {
			p = p[:i+1] + "*"
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	if len(out) > 40 {
		return nil
	}
	return out
}

// StateFetcher reads a controller's /api/state, filtered to patterns.
func StateFetcher(base string, patterns []string) func(context.Context) (map[string]any, error) {
	u := strings.TrimRight(base, "/") + "/api/state"
	if len(patterns) > 0 {
		u += "?tags=" + url.QueryEscape(strings.Join(patterns, ","))
	}
	client := &http.Client{Timeout: 5 * time.Second}
	return func(ctx context.Context) (map[string]any, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", u, resp.Status)
		}
		var st struct {
			Tags map[string]any `json:"tags"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			return nil, fmt.Errorf("%s: %w", u, err)
		}
		return st.Tags, nil
	}
}

func plantMember(tags map[string]any, tag, name string) (any, bool) {
	st, ok := tags[tag].(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := st[name]
	return v, ok && v != nil
}

func (p *Plant) once(msg string) {
	if !p.errs[msg] {
		p.errs[msg] = true
		p.Log.Warn("redfish stand-in: plant value not served", "error", msg)
	}
}
