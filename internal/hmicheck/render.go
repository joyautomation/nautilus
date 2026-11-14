package hmicheck

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

//go:embed probe.js
var probeJS string

// Finding is one thing wrong with a rendered page.
type Finding struct {
	Page  string // route, "/" or "/pumps"
	View  string // "desktop" or "phone"
	Error bool   // an error (else a warning)
	Text  string
}

func (f Finding) String() string {
	return fmt.Sprintf("screen %s (%s): %s", f.Page, f.View, f.Text)
}

// A view is a window size the screen is looked at in.
type view struct {
	name          string
	width, height int
	mobile        bool
}

var views = []view{
	{"desktop", 1920, 1080, false},
	{"phone", 390, 844, true},
}

// Settle is how long a page gets after it loads, for its first frame of
// tags, its fonts and its layout, before it is looked at.
var Settle = 1500 * time.Millisecond

// Render opens each page of the screen served at base (http://127.0.0.1:port)
// in the browser at path, at desktop and phone size, and says what is
// wrong with it. An error return is the browser itself failing (it
// wouldn't start); what's wrong with the screen is in the findings.
func Render(ctx context.Context, path, base string, pages []string) ([]Finding, error) {
	b, err := launch(ctx, path)
	if err != nil {
		return nil, err
	}
	defer b.close()
	var out []Finding
	for _, p := range pages {
		if strings.Contains(p, "[") {
			continue // a route with a parameter: no one URL to open
		}
		// the same finding at both sizes is said once, for both
		at := map[string]int{}
		for _, v := range views {
			fs, err := b.look(ctx, base+p, p, v)
			if err != nil {
				return out, err
			}
			for _, f := range fs {
				if i, ok := at[f.Text]; ok {
					out[i].View += " and " + f.View
					continue
				}
				at[f.Text] = len(out)
				out = append(out, f)
			}
		}
	}
	return out, nil
}

type remoteObject struct {
	Type        string          `json:"type"`
	Subtype     string          `json:"subtype"`
	Description string          `json:"description"`
	Value       json.RawMessage `json:"value"`
}

type probeResult struct {
	Blank bool `json:"blank"`
	Pipes []struct {
		ID   string   `json:"id"`
		Ends []string `json:"ends"`
	} `json:"pipes"`
	Outside []struct {
		Count int      `json:"count"`
		Names []string `json:"names"`
		More  int      `json:"more"`
		Past  int      `json:"past"`
		Size  int      `json:"size"`
	} `json:"outside"`
	Sideways int `json:"sideways"`
	Width    int `json:"width"`
}

func (b *browser) look(ctx context.Context, url, page string, v view) ([]Finding, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second+Settle)
	defer cancel()
	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := b.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &target); err != nil {
		return nil, err
	}
	defer b.call(context.Background(), "", "Target.closeTarget", map[string]any{"targetId": target.TargetID}, nil)
	var att struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &att); err != nil {
		return nil, err
	}
	s := att.SessionID

	var mu sync.Mutex
	var thrown []string
	loaded := make(chan struct{}, 1)
	add := func(text string) {
		mu.Lock()
		defer mu.Unlock()
		for _, t := range thrown {
			if t == text {
				return
			}
		}
		thrown = append(thrown, text)
	}
	b.on(s, func(m message) {
		switch m.Method {
		case "Page.loadEventFired":
			select {
			case loaded <- struct{}{}:
			default:
			}
		case "Runtime.exceptionThrown":
			var p struct {
				Details struct {
					Text      string        `json:"text"`
					Exception *remoteObject `json:"exception"`
				} `json:"exceptionDetails"`
			}
			if json.Unmarshal(m.Params, &p) == nil {
				t := p.Details.Text
				if e := p.Details.Exception; e != nil && e.Description != "" {
					t = e.Description
				}
				add(firstLine(t))
			}
		case "Runtime.consoleAPICalled":
			// an Error logged (the router logs what a page threw while
			// loading, then shows its error page) is the page failing
			var p struct {
				Type string         `json:"type"`
				Args []remoteObject `json:"args"`
			}
			if json.Unmarshal(m.Params, &p) == nil && p.Type == "error" {
				for _, a := range p.Args {
					if a.Subtype == "error" {
						add(firstLine(a.Description))
						break
					}
				}
			}
		}
	})
	defer b.on(s, nil)

	for _, c := range []struct {
		method string
		params any
	}{
		{"Page.enable", nil},
		{"Runtime.enable", nil},
		{"Emulation.setDeviceMetricsOverride", map[string]any{"width": v.width, "height": v.height, "deviceScaleFactor": 1, "mobile": v.mobile}},
		{"Page.navigate", map[string]any{"url": url}},
	} {
		if err := b.call(ctx, s, c.method, c.params, nil); err != nil {
			return nil, err
		}
	}
	select {
	case <-loaded:
	case <-ctx.Done():
		return []Finding{{Page: page, View: v.name, Error: true, Text: "the page didn't finish loading in 30 s"}}, nil
	}

	var res struct {
		Result           remoteObject `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	expr := strings.NewReplacer("SETTLE_MS", fmt.Sprint(Settle.Milliseconds()), "DEVICE_WIDTH", fmt.Sprint(v.width)).Replace(probeJS)
	if err := b.call(ctx, s, "Runtime.evaluate", map[string]any{"expression": expr, "awaitPromise": true, "returnByValue": true}, &res); err != nil {
		return nil, err
	}
	if res.ExceptionDetails != nil {
		return nil, fmt.Errorf("naut's page probe failed: %s", res.ExceptionDetails.Text)
	}
	var pr probeResult
	if err := json.Unmarshal(res.Result.Value, &pr); err != nil {
		return nil, fmt.Errorf("naut's page probe: %v", err)
	}

	var out []Finding
	f := func(isErr bool, format string, a ...any) {
		out = append(out, Finding{Page: page, View: v.name, Error: isErr, Text: fmt.Sprintf(format, a...)})
	}
	mu.Lock()
	for _, t := range thrown {
		f(true, "throws: %s", t)
	}
	mu.Unlock()
	if pr.Blank {
		f(true, "the page is blank: no text, drawing or control shows")
	}
	// Pipes and drawings look the same at any size: say them once.
	if v.name == views[0].name {
		const name = 5 // pipes named one by one; the rest in one line
		for i, p := range pr.Pipes {
			if i == name {
				var rest []string
				for _, q := range pr.Pipes[name:] {
					rest = append(rest, q.ID)
				}
				f(true, "and %d more pipes draw nothing, the same way: %s", len(rest), strings.Join(rest, ", "))
				break
			}
			if len(p.Ends) > 0 {
				f(true, "pipe %s draws nothing: %s %s not there when the screen runs (equipment that isn't on the screen, or a port its component doesn't declare)", p.ID, strings.Join(p.Ends, " and "), isAre(len(p.Ends)))
			} else {
				f(true, "pipe %s draws nothing: its ends and points are all one spot", p.ID)
			}
		}
		for _, o := range pr.Outside {
			names := strings.Join(o.Names, ", ")
			if o.More > 0 {
				names += fmt.Sprintf(" and %d more", o.More)
			}
			f(false, "%d shape(s) draw far outside their drawing, up to %d px past a %d px drawing: %s (a coordinate passed as text, \"130\" + 8 = \"1308\", or in the wrong units)", o.Count, o.Past, o.Size, names)
		}
	}
	if v.mobile && pr.Sideways > 1 {
		f(false, "scrolls sideways on a %d px wide phone: the page is %d px too wide", pr.Width, pr.Sideways)
	}
	return out, nil
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
