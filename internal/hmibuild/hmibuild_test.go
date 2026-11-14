package hmibuild

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repo(t *testing.T) string {
	t.Helper()
	r, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The lift station's own SvelteKit app builds unchanged: three pages, the
// kit, the 3D kit with three and Threlte, $app/*, import.meta.glob and
// static/ (its .glb models).
func TestBuildsTheLiftStationApp(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles ~200 Svelte files")
	}
	proj := filepath.Join(repo(t), "examples", "lift-station")
	res, err := Build(Options{Project: proj, App: filepath.Join(proj, "hmi"), Minify: true})
	if err != nil {
		var be *Error
		if errors.As(err, &be) {
			t.Fatalf("build failed:\n%s", strings.Join(be.Messages, "\n"))
		}
		t.Fatal(err)
	}
	if got := strings.Join(res.Pages, " "); got != "/ /3d /diagnostics" {
		t.Errorf("pages = %q, want / /3d /diagnostics", got)
	}
	for _, f := range []string{"index.html", "app.js", "app.css", "models/submersible-pump.glb"} {
		if len(res.Files[f]) == 0 {
			t.Errorf("no %s in the build", f)
		}
	}
	html := string(res.Files["index.html"])
	if !strings.Contains(html, `<script type="module" src="/app.js">`) || !strings.Contains(html, "Lift Station") {
		t.Errorf("index.html is not the app's app.html filled in:\n%s", html)
	}
	t.Logf("built in %s: %d files, %d Svelte compiles, %d warnings", res.Took, len(res.Files), res.Compiled, len(res.Warnings))
}

// A project with mimics and no app gets the default one: a page per mimic,
// its own components found by name.
func TestBuildsTheDefaultAppFromMimics(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "screens/gallery.mimic.json", `{"name":"Gallery","canvas":{"width":400,"height":300},
		"equipment":[{"id":"T1","component":"Tank","x":10,"y":10},{"id":"F1","component":"FilterCell","x":100,"y":10,"bind":{"level":"F1_Level"}}]}`)
	write(t, dir, "screens/pumps.mimic.json", `{"name":"Pumps","equipment":[{"id":"P1","component":"Pump","x":10,"y":10}]}`)
	write(t, dir, "screens/FilterCell.svelte", "<script>\n  let { level = 0 } = $props();\n</script>\n<div>{level}</div>\n")
	res, err := Build(Options{Project: dir, Title: "Filter gallery"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Pages, " "); got != "/ /pumps" {
		t.Errorf("pages = %q, want / /pumps", got)
	}
	if !strings.Contains(string(res.Files["index.html"]), "<title>Filter gallery</title>") {
		t.Errorf("index.html: %s", res.Files["index.html"])
	}
}

// The mistakes the model chart made fail the build with where and why.
func TestBuildErrorsSayWhereAndWhy(t *testing.T) {
	cases := map[string]struct{ file, text, want string }{
		"store rune conflict": {"screens/Cell.svelte", "<script>\n  let { state = 1 } = $props();\n  let error = $state('');\n</script>\n<p>{state}{error}</p>\n",
			"screens/Cell.svelte:3:"},
		"a package naut doesn't carry": {"screens/Cell.svelte", "<script>\n  import Chart from 'chart.js';\n</script>\n<p></p>\n",
			`"chart.js" is not one of the packages naut builds with`},
		"a missing component": {"", "", "component Cell: no such .svelte in the project"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "screens/s.mimic.json", `{"equipment":[{"id":"C","component":"Cell","x":0,"y":0}]}`)
			if c.file != "" {
				write(t, dir, c.file, c.text)
			}
			_, err := Build(Options{Project: dir})
			var be *Error
			if !errors.As(err, &be) {
				t.Fatalf("err = %v, want a build error", err)
			}
			if all := strings.Join(be.Messages, "\n"); !strings.Contains(all, c.want) {
				t.Errorf("messages:\n%s\nwant one containing %q", all, c.want)
			}
		})
	}
}

// An app with no +layout.svelte: its page has an empty layout chain, [],
// never null (the router maps over it; bw01 sonnet-1 and haiku-2 got a 500).
func TestAnAppWithNoLayout(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "hmi/src/routes/+page.svelte", "<p>hi</p>\n")
	res, err := Build(Options{Project: dir, App: filepath.Join(dir, "hmi")})
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for name, b := range res.Files {
		if strings.HasSuffix(name, ".js") {
			all.Write(b)
		}
	}
	js := all.String()
	if !strings.Contains(js, "layouts: []") || strings.Contains(js, "layouts: null") {
		t.Errorf("the route's layout chain is not []: %q", excerpt(js, "layouts:"))
	}
}

// A project's own component declares its pipe ports in a
// {Name}.component.json; the screen naut builds carries them on the
// equipment, or every pipe anchored to it draws nothing (bw01 fable-2).
// An instance's own ports, even none, stay as they are.
func TestASidecarsPortsReachTheScreen(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "screens/gallery.mimic.json", `{"name":"Gallery","canvas":{"width":400,"height":300},
		"equipment":[{"id":"F1","component":"FilterCell","x":100,"y":10},
			{"id":"F2","component":"FilterCell","x":200,"y":10,"ports":[]},
			{"id":"T1","component":"Tank","x":10,"y":10}],
		"pipes":[{"id":"p1","from":{"equip":"T1","port":"right"},"to":{"equip":"F1","port":"inlet_zz"}}]}`)
	write(t, dir, "screens/FilterCell.svelte", "<div>cell</div>\n")
	write(t, dir, "screens/FilterCell.component.json", `{"ports":[{"name":"inlet_zz","part":"cell","x":0.5,"y":0}]}`)
	// a deeper one of the same name loses, as it does in the editor
	write(t, dir, "screens/old/FilterCell.component.json", `{"ports":[{"name":"stale_zz","x":0,"y":0}]}`)
	res, err := Build(Options{Project: dir, Title: "Gallery"})
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for name, b := range res.Files {
		if strings.HasSuffix(name, ".js") {
			all.Write(b)
		}
	}
	js := all.String()
	if n := strings.Count(js, "inlet_zz"); n != 2 {
		t.Errorf("inlet_zz appears %d times, want 2 (F1's port and the pipe end): %q", n, excerpt(js, "inlet_zz"))
	}
	if strings.Contains(js, "stale_zz") {
		t.Errorf("the deeper sidecar's ports were used")
	}
}

func TestWithSidecarPorts(t *testing.T) {
	ports := map[string]json.RawMessage{"Cell": json.RawMessage(`[{"name":"a","x":0,"y":0}]`), "Tank": json.RawMessage(`[]`)}
	doc := []byte(`{"equipment":[{"id":"c1","component":"Cell"},{"id":"c2","component":"Cell","ports":[]},{"id":"t","component":"Tank"}],"pipes":[]}`)
	var got struct {
		Equipment []struct {
			ID    string          `json:"id"`
			Ports json.RawMessage `json:"ports"`
		} `json:"equipment"`
	}
	if err := json.Unmarshal(withSidecarPorts(doc, ports), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"c1": `[{"name":"a","x":0,"y":0}]`, "c2": `[]`, "t": ``}
	for _, e := range got.Equipment {
		if string(e.Ports) != want[e.ID] {
			t.Errorf("%s ports = %s, want %s", e.ID, e.Ports, want[e.ID])
		}
	}
	if string(withSidecarPorts([]byte("not json"), ports)) != "not json" {
		t.Errorf("an unreadable doc changed")
	}
}

func excerpt(s, at string) string {
	i := strings.Index(s, at)
	if i < 0 {
		return "(not found)"
	}
	return s[i:min(len(s), i+60)]
}

func TestServerCodeIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "hmi/src/routes/+page.svelte", "<p>hi</p>\n")
	write(t, dir, "hmi/src/routes/+page.server.ts", "export const load = () => ({});\n")
	_, err := Build(Options{Project: dir, App: filepath.Join(dir, "hmi")})
	var be *Error
	if !errors.As(err, &be) || !strings.Contains(be.Messages[0], "Go is the only backend") {
		t.Fatalf("err = %v", err)
	}
}

func TestRoutes(t *testing.T) {
	for dir, want := range map[string]string{
		"":                `^/?$`,
		"3d":              `^/3d/?$`,
		"rack/[id]":       `^/rack/([^/]+)/?$`,
		"(ops)/pumps":     `^/pumps/?$`,
		"docs/[...rest]":  `^/docs(?:/(.*))?/?$`,
		"site/[[filter]]": `^/site(?:/([^/]+))?/?$`,
	} {
		r, err := routeFor(dir)
		if err != nil || r.Pattern != want {
			t.Errorf("routeFor(%q) = %q, %v; want %q", dir, r.Pattern, err, want)
		}
	}
}

func write(t *testing.T, dir, rel, text string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
