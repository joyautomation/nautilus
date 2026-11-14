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
		"/fine":    `<h1>Filter gallery</h1><svg viewBox="0 0 100 100" width="100" height="100"><rect x="10" y="10" width="80" height="80"/></svg>`,
		"/throws":  `<p>hi</p><script>setTimeout(() => { throw new Error("boom in a component") }, 0)</script>`,
		"/logged":  `<p>hi</p><script>console.error(new Error("a page failed to load")); console.error("a plain message")</script>`,
		"/blank":   `<div></div>`,
		"/pipes":   `<div data-broken-pipes='[{"id":"p1","ends":["F1_INF.a"]},{"id":"p2","ends":[]}]'><p>screen</p></div>`,
		"/outside": `<svg viewBox="0 0 300 300" width="300" height="300"><defs><clipPath id="c"><rect width="300" height="300"/></clipPath></defs><g><title>XV-105: closed</title><path d="M 10 10 L 20 10 L 10 1308 L 20 1308 Z"/></g><circle cx="300" cy="150" r="20"/><g clip-path="url(#c)"><path class="hatch" d="M -900 0 L 1200 2000"/></g></svg>`,
		"/wide":    `<div style="width:900px">a wide table</div>`,
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
	for _, p := range []string{"/fine", "/throws", "/logged", "/blank", "/pipes", "/outside", "/wide", "/[id]"} {
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
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	for _, not := range []string{"screen /fine", "a plain message", "/[id]", "circle", "hatch"} {
		if strings.Contains(all, not) {
			t.Errorf("%q should not be reported:\n%s", not, all)
		}
	}
}
