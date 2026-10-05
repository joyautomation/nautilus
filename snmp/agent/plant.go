package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/snmp/walk"
)

// Plant makes an agent answer from a plant simulation instead of its
// recording: every OID the manifest binds is served from the plant's live
// tag of the same name, read backwards through the binding (snmp.Feed).
// OIDs the plant has no value for — a tag it does not declare, a member
// the manifest does not bind — keep answering from the walk, so a plant
// can model as much or as little of a device as it likes.
//
// Rate members (InBps, ErrorRate) steer counters: the counter ramps at the
// rate the plant asks for, from wherever it has got to, so it stays
// monotonic and the driver's rate reads the plant's value back.
//
// The root tag's Online member (Switch.Online) is the plant's "device
// dark" input: false and the agent stops answering, true and it is back.
type Plant struct {
	Agent  *Agent
	Feeds  []snmp.Feed
	Online string // root tag whose Online member gates answering; "" = none
	Log    *slog.Logger

	rates  map[string]float64 // OID → the ramp last set
	errs   map[string]bool    // errors already logged, logged once
	down   bool
	failed bool
}

// NewPlant binds an agent to source's tags in m.
func NewPlant(a *Agent, m snmp.Manifest, source string) (*Plant, error) {
	feeds, err := m.Feeds(source)
	if err != nil {
		return nil, err
	}
	return &Plant{Agent: a, Feeds: feeds, Online: m.OnlineTag(source), Log: slog.Default()}, nil
}

// Apply serves one snapshot of the plant's tags ({tag: {member: value}},
// the shape /api/state delivers struct tags in). It returns how many OIDs
// took a plant value.
func (p *Plant) Apply(tags map[string]any) int {
	if p.rates == nil {
		p.rates, p.errs = map[string]float64{}, map[string]bool{}
	}
	n := 0
	for _, f := range p.Feeds {
		raw, ok := member(tags, f.Tag, f.Member)
		if !ok {
			continue
		}
		v, err := hw.Coerce(raw, f.Field)
		if err != nil {
			p.once(fmt.Sprintf("%s.%s: %v", f.Tag, f.Member, err))
			continue
		}
		if f.Binding.Rate {
			r, err := f.Rate(v)
			if err != nil {
				p.once(err.Error())
				continue
			}
			if last, seen := p.rates[f.OID]; !seen || last != r {
				if err := p.Agent.Ramp(f.OID, r); err != nil {
					p.once(fmt.Sprintf("%s.%s: %v", f.Tag, f.Member, err))
					continue
				}
				p.rates[f.OID] = r
			}
			n++
			continue
		}
		cur, ok := p.Agent.Value(f.OID)
		if !ok {
			p.once(fmt.Sprintf("%s.%s: %s is not in the walk", f.Tag, f.Member, f.OID))
			continue
		}
		vb, err := f.Serve(v, cur)
		if err != nil {
			p.once(err.Error())
			continue
		}
		if !sameVarbind(vb, cur) {
			p.Agent.SetValue(vb)
		}
		n++
	}
	if p.Online != "" {
		if on, ok := member(tags, p.Online, "Online"); ok {
			down := on == false
			if down != p.down {
				p.down = down
				p.Agent.SetDrop(down)
				p.Log.Info("snmp agent: plant says the device is "+map[bool]string{true: "dark: not answering", false: "back: answering"}[down], "tag", p.Online)
			}
		}
	}
	return n
}

// Run polls the plant's /api/state every interval until ctx ends. A plant
// that does not answer leaves the agent serving what it last served.
func (p *Plant) Run(ctx context.Context, fetch func(context.Context) (map[string]any, error), interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		tags, err := fetch(ctx)
		switch {
		case err != nil && !p.failed:
			p.failed = true
			p.Log.Warn("snmp agent: plant not answering; serving the last values", "error", err)
		case err == nil:
			if p.failed {
				p.failed = false
				p.Log.Info("snmp agent: plant answering again")
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

func member(tags map[string]any, tag, name string) (any, bool) {
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
		p.Log.Warn("snmp agent: plant value not served", "error", msg)
	}
}

func sameVarbind(a, b walk.Varbind) bool {
	return a.Type == b.Type && a.Int == b.Int && a.Uint == b.Uint && a.Str == b.Str && string(a.Bytes) == string(b.Bytes)
}
