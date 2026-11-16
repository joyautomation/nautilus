package hmicheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Each page is one thing a person would see wrong (or nothing wrong), and
// Render says it the way naut check prints it. Needs a browser; skipped
// without one.
func TestRenderSaysWhatAPersonWouldSee(t *testing.T) {
	path := FindBrowser()
	if path == "" {
		t.Skip("no Chrome, Chromium or Edge on this machine")
	}
	Settle = 300 * time.Millisecond
	pages := map[string]string{
		"/fine":      `<h1>Filter gallery</h1><svg viewBox="0 0 100 100" width="100" height="100"><rect x="10" y="10" width="80" height="80"/></svg>`,
		"/throws":    `<p>hi</p><script>setTimeout(() => { throw new Error("boom in a component") }, 0)</script>`,
		"/logged":    `<p>hi</p><script>console.error(new Error("a page failed to load")); console.error("a plain message")</script>`,
		"/blank":     `<div></div>`,
		"/pipes":     `<div data-broken-pipes='[{"id":"p1","ends":["F1_INF.a"]},{"id":"p2","ends":[]}]'><p>screen</p></div>`,
		"/outside":   `<svg viewBox="0 0 300 300" width="300" height="300"><defs><clipPath id="c"><rect width="300" height="300"/></clipPath></defs><g><title>XV-105: closed</title><path d="M 10 10 L 20 10 L 10 1308 L 20 1308 Z"/></g><circle cx="300" cy="150" r="20"/><g clip-path="url(#c)"><path class="hatch" d="M -900 0 L 1200 2000"/></g></svg>`,
		"/wide":      `<div style="width:900px">a wide table</div>`,
		"/tall":      `<svg viewBox="0 0 1600 1240" style="display:block;width:100%"><rect x="10" y="10" width="1500" height="1200"/></svg>`,
		"/small":     `<div class="mimic" data-scale="0.600" data-canvas="2120x1400" style="width:1272px;max-width:100%;height:840px"><p>gallery</p></div>`,
		"/dim":       `<p style="color:#30353b">XV-101</p><p style="color:#c3c2b7">P-501 running</p>`,
		"/themed":    `<script>document.documentElement.dataset.theme = localStorage.getItem('theme') || 'dark'</script><style>[data-theme=dark] body{background:#0d0d0d;color:#e8e6e1} [data-theme=light] body{background:#f3f2ee;color:#f0efe9}</style><p>Filter F-2 washing</p>`,
		"/tiny":      `<div style="transform:scale(0.5);transform-origin:0 0"><p style="font-size:14px;color:#000">closed</p></div>`,
		"/overlap":   `<p style="position:absolute;left:10px;top:10px;margin:0;font-size:14px;color:#fff;background:#000">XV-104 closed</p><p style="position:absolute;left:40px;top:12px;margin:0;font-size:14px;color:#fff">XV-105 open</p><p style="position:absolute;left:10px;top:60px;margin:0;font-size:14px;color:#fff">FIT-501</p><p style="position:absolute;left:10px;top:80px;margin:0;font-size:14px;color:#fff">0 gpm</p>`,
		"/fold":      `<div style="height:1200px">screen</div><label>Run time <input value="72"></label><label>High-rate flow <input value="7200"></label>`,
		"/fieldover": `<input value="8.0" style="position:absolute;left:10px;top:10px;width:80px;font-size:14px"><span style="position:absolute;left:20px;top:14px;font-size:14px;color:#fff">Headloss</span>`,
		"/crossed":   `<div data-crossed='[{"id":"F1_AIR","n":4},{"id":"F2_AIR","n":3}]'><p>screen</p></div>`,
		"/through":   `<div class="mimic" style="position:relative;width:600px;height:200px"><svg class="pipes" width="600" height="200" style="position:absolute;left:0;top:0"><g data-pipe="outlet"><g><path class="wall" d="M 0 50 L 600 50" fill="none" stroke="#777" stroke-width="4"/></g></g><g data-pipe="feed"><g><path class="wall" d="M 0 150 L 600 150" fill="none" stroke="#777" stroke-width="4"/></g></g></svg><span style="position:absolute;left:100px;top:41px;font-size:14px;line-height:18px;color:#000;background:#fff">MT-201</span><span style="position:absolute;left:300px;top:90px;font-size:14px;color:#000">clear of it</span><div style="position:absolute;left:400px;top:120px"><svg width="60" height="60"><circle cx="30" cy="30" r="28" fill="#fff" stroke="#000"/><text x="30" y="35" text-anchor="middle" font-size="12" fill="#000">FIT-201</text></svg></div></div>`,
		"/below":     `<div class="mimic" data-scale="0.900" data-canvas="2120x1400" style="width:1908px;max-width:100%;height:1260px"><p>gallery</p></div>`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"></head><body>` + body + `</body></html>`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var routes []string
	for _, p := range []string{"/fine", "/throws", "/logged", "/blank", "/pipes", "/outside", "/wide", "/tall", "/small", "/below", "/crossed", "/through", "/overlap", "/fold", "/fieldover", "/dim", "/themed", "/tiny", "/[id]"} {
		routes = append(routes, p)
	}
	found, err := Render(ctx, path, srv.URL, routes)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range found {
		kind := "warning"
		if f.Error {
			kind = "error"
		}
		got = append(got, kind+": "+f.String())
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{
		"error: screen /throws (desktop and phone): throws: Error: boom in a component",
		"error: screen /logged (desktop and phone): throws: Error: a page failed to load",
		"error: screen /blank (desktop and phone): the page is blank",
		"error: screen /pipes (desktop): pipe p1 draws nothing: F1_INF.a is not there",
		"error: screen /pipes (desktop): pipe p2 draws nothing: its ends and points are all one spot",
		"warning: screen /outside (desktop): 1 shape(s) draw far outside their drawing",
		"XV-105: closed",
		"warning: screen /wide (phone): scrolls sideways on a 390 px wide phone",
		`warning: screen /dim (desktop): 1 of 2 texts are hard to read (under 4.5:1 contrast, 3:1 for large text): "P-501 running" #c3c2b7 on #ffffff (1.8:1)`,
		`warning: screen /themed (desktop): in the light theme, 1 of 1 texts are hard to read`,
		`warning: screen /tiny (desktop): 1 of 1 texts are drawn under 12 px in a 1920×1080 window (the smallest 7.0 px): "closed"`,
		"warning: screen /tall (desktop): a 1904×1476 drawing (viewBox 1600×1240) runs 404 px past the bottom of a 1920×1080 window",
		"warning: screen /small (desktop): the 2120×1400 drawing is drawn at 60% to fit a 1920×1080 window, so 14 px text reads as 8 px",
		"warning: screen /crossed (desktop): pipe F1_AIR passes under 4 other pipes, and 1 more passes under 3 or more (F2_AIR): a line that crosses a stack of headers",
		"every drop still runs to its header",
		`warning: screen /through (desktop): 1 text on the drawing has a pipe running through it: "MT-201" (pipe outlet). A tag or a reading sits beside its line`,
		`warning: screen /overlap (desktop): 1 text is drawn over other text: "XV-104 closed" over "XV-105 open"`,
		`warning: screen /fold (desktop): 4 controls and texts sit below the bottom of a 1920×1080 window`,
		`reach them: "Run time", "High-rate flow"`,
		`warning: screen /fieldover (desktop): 1 text is drawn over other text: "Headloss" over "[8.0]"`,
		"warning: screen /below (desktop): the 2120×1400 drawing runs 196 px past the bottom of a 1920×1080 window, drawn at 90%",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	for _, not := range []string{"screen /fine", "a plain message", "/[id]", "circle", "hatch", "/tall (phone)", "/small (phone)", "/below (phone)", "/crossed (phone)", "/overlap (phone)", "/fold (phone)", "FIT-501", "/themed (desktop): in the dark theme", "XV-101", "FIT-201", "clear of it", "/through (phone): 1 text"} {
		if strings.Contains(all, not) {
			t.Errorf("%q should not be reported:\n%s", not, all)
		}
	}
}
