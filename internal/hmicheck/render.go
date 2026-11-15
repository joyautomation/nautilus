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
	Crossed []struct {
		ID string `json:"id"`
		N  int    `json:"n"`
	} `json:"crossed"`
	Outside []struct {
		Count int      `json:"count"`
		Names []string `json:"names"`
		More  int      `json:"more"`
		Past  int      `json:"past"`
		Size  int      `json:"size"`
	} `json:"outside"`
	Mimics []struct {
		Canvas  [2]int  `json:"canvas"`
		Scale   float64 `json:"scale"`
		Below   int     `json:"below"`
		Room    [2]int  `json:"room"`
		Scrolls bool    `json:"scrolls"`
	} `json:"mimics"`
	Drawings []struct {
		Size    [2]int  `json:"size"`
		ViewBox *[2]int `json:"viewBox"`
		Below   int     `json:"below"`
	} `json:"drawings"`
	Fold *struct {
		Count int      `json:"count"`
		Below int      `json:"below"`
		Names []string `json:"names"`
	} `json:"fold"`
	Through []struct {
		Text string `json:"text"`
		Pipe string `json:"pipe"`
	} `json:"through"`
	Sideways int `json:"sideways"`
	Width    int `json:"width"`
}

// SmallScale is the scale under which a drawing fitted to a desktop window
// reads too small: the kit's 14 px text at 0.75 is 10.5 px.
const SmallScale = 0.75

func (b *browser) look(ctx context.Context, url, page string, v view) ([]Finding, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second+2*Settle) // a load, and a reload in the other theme
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
		if len(pr.Crossed) > 0 {
			c := pr.Crossed[0]
			more := ""
			if n := len(pr.Crossed) - 1; n > 0 {
				var ids []string
				for _, o := range pr.Crossed[1:min(len(pr.Crossed), 6)] {
					ids = append(ids, o.ID)
				}
				if n > 5 {
					ids = append(ids, "…")
				}
				more = fmt.Sprintf(", and %d more pipes pass under 3 or more (%s)", n, strings.Join(ids, ", "))
				if n == 1 {
					more = fmt.Sprintf(", and 1 more passes under 3 or more (%s)", ids[0])
				}
			}
			f(false, "pipe %s passes under %d other pipes%s: a line that crosses a stack of headers to reach its own can't be traced by eye. Put each header on the side of the equipment its line leaves from, nearest first in the order the drops reach them, so each drop crosses at most two; every drop still runs to its header (an OffPage connector is for a line that goes on to another screen)", c.ID, c.N, more)
		}
		for _, o := range pr.Outside {
			names := strings.Join(o.Names, ", ")
			if o.More > 0 {
				names += fmt.Sprintf(" and %d more", o.More)
			}
			f(false, "%d shape(s) draw far outside their drawing, up to %d px past a %d px drawing: %s (a coordinate passed as text, \"130\" + 8 = \"1308\", or in the wrong units)", o.Count, o.Past, o.Size, names)
		}
	}
	// How a drawing meets a desktop window: whole, and big enough to read.
	// (A phone shows a drawing however the screen decided to: no rule here.)
	if !v.mobile {
		for _, m := range pr.Mimics {
			pct := int(m.Scale*100 + 0.5)
			switch {
			case m.Scrolls:
			case m.Below > 2:
				f(false, "the %d×%d drawing runs %d px past the bottom of a %d×%d window, drawn at %d%% to fill the width: an operator scrolls to see the rest. fit=\"contain\" on <Mimic> shows it whole; a canvas drawn for the room (%d×%d here) shows it whole at full size", m.Canvas[0], m.Canvas[1], m.Below, v.width, v.height, pct, m.Room[0], m.Room[1])
			case m.Scale < SmallScale:
				f(false, "the %d×%d drawing is drawn at %d%% to fit a %d×%d window, so 14 px text reads as %.0f px: draw the canvas for the room (%d×%d here), or split it into screens", m.Canvas[0], m.Canvas[1], pct, v.width, v.height, 14*m.Scale, m.Room[0], m.Room[1])
			}
		}
	}
	if !v.mobile {
		for _, d := range pr.Drawings {
			vb := ""
			if d.ViewBox != nil {
				vb = fmt.Sprintf(" (viewBox %d×%d)", d.ViewBox[0], d.ViewBox[1])
			}
			f(false, "a %d×%d drawing%s runs %d px past the bottom of a %d×%d window: an operator scrolls to see the rest. Size it to the room left in the window (an <svg> with a viewBox keeps its shape at any size: width: 100%%; height: 100%% in a container that has a height), or draw it for the room", d.Size[0], d.Size[1], vb, d.Below, v.width, v.height)
		}
	}
	// A line through a label: the label can't be read, the line can't be
	// traced. (The drawing is the same at any size: said once, at desktop.)
	if !v.mobile && len(pr.Through) > 0 {
		var q []string
		for _, t := range pr.Through[:min(len(pr.Through), 5)] {
			q = append(q, fmt.Sprintf("%q (pipe %s)", t.Text, t.Pipe))
		}
		if n := len(pr.Through) - 5; n > 0 {
			q = append(q, fmt.Sprintf("and %d more", n))
		}
		what := fmt.Sprintf("%d texts on the drawing have a pipe running through them", len(pr.Through))
		if len(pr.Through) == 1 {
			what = "1 text on the drawing has a pipe running through it"
		}
		f(false, "%s: %s. A tag or a reading sits beside its line, never on it: move the text clear of every pipe (to the side of the equipment the pipes don't reach), or route the pipe to a port and around the text", what, strings.Join(q, ", "))
	}
	// What an operator scrolls to reach. A components page is a catalogue,
	// long on purpose.
	if !v.mobile && pr.Fold != nil && page != "/components" {
		where := ""
		if pr.Fold.Below > 0 {
			where = fmt.Sprintf(" (up to %d px past it)", pr.Fold.Below)
		}
		var q []string
		for _, n := range pr.Fold.Names {
			q = append(q, fmt.Sprintf("%q", n))
		}
		f(false, "%d controls and texts sit below the bottom of a %d×%d window%s or in a box that scrolls, so an operator scrolls to reach them: %s: fit the screen to the window (the drawing the room it needs, the rest in a panel beside it, groups denser), or move what is seldom used to a page of its own", pr.Fold.Count, v.width, v.height, where, strings.Join(q, ", "))
	}
	// Text a person can read: contrast against what it is drawn on, in each
	// theme the page has, and size. (A phone's text is the screen's own
	// choice of layout: no rule here yet.)
	if !v.mobile {
		reload := func() error {
			if err := b.call(ctx, s, "Page.reload", nil, nil); err != nil {
				return err
			}
			select {
			case <-loaded:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-time.After(Settle):
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		}
		for _, line := range b.legibility(ctx, s, v, reload) {
			f(false, "%s", line)
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
