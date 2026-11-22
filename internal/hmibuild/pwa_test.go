package hmibuild

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

func shellFiles() map[string][]byte {
	return map[string][]byte{
		"index.html":   []byte("<html></html>"),
		"app.js":       []byte("1"),
		"app.css":      []byte("a{}"),
		"big.glb":      bytes.Repeat([]byte{1}, shellMaxFile+1),
		"_app/f.woff":  []byte("old"),
		"_app/f.woff2": []byte("new"),
	}
}

func decodePNG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// The manifest names the app, themes it, and points at icons that exist at
// the sizes a browser asks for (Chrome wants 192 and 512; iOS reads the
// touch icon).
func TestPWAManifestAndIcons(t *testing.T) {
	files := shellFiles()
	if err := pwaFiles(files, "Grain plant", &PWA{ShortName: "Grain", ThemeColor: "#1A2B3C"}); err != nil {
		t.Fatal(err)
	}
	var m struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
		Display   string
		StartURL  string `json:"start_url"`
		Theme     string `json:"theme_color"`
		Icons     []struct{ Src, Sizes string }
	}
	if err := json.Unmarshal(files["manifest.webmanifest"], &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "Grain plant" || m.ShortName != "Grain" || m.Display != "standalone" || m.StartURL != "./" || m.Theme != "#1a2b3c" {
		t.Errorf("manifest = %+v", m)
	}
	for _, ic := range m.Icons {
		img := decodePNG(t, files[ic.Src])
		var w, h int
		if _, err := fmt.Sscanf(ic.Sizes, "%dx%d", &w, &h); err != nil || img.Bounds().Dx() != w || img.Bounds().Dy() != h {
			t.Errorf("icon %s says %s, is %v", ic.Src, ic.Sizes, img.Bounds())
		}
	}
	if len(m.Icons) != 2 {
		t.Errorf("icons = %v", m.Icons)
	}
	if b := decodePNG(t, files["apple-touch-icon.png"]).Bounds(); b.Dx() != 180 {
		t.Errorf("touch icon is %v", b)
	}
	// the drawn icon takes the theme as its background
	if r, g, b, _ := decodePNG(t, files["icon-192.png"]).At(1, 1).RGBA(); r>>8 != 0x1a || g>>8 != 0x2b || b>>8 != 0x3c {
		t.Errorf("icon corner = %x %x %x, want the theme 1a2b3c", r>>8, g>>8, b>>8)
	}
}

// A project's own icon is the one scaled, not replaced by the drawn one.
func TestPWAOwnIconIsScaled(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	red := color.NRGBA{200, 10, 10, 255}
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			src.SetNRGBA(x, y, red)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	files := shellFiles()
	if err := pwaFiles(files, "x", &PWA{Icon: buf.Bytes()}); err != nil {
		t.Fatal(err)
	}
	r, g, b, a := decodePNG(t, files["icon-192.png"]).At(100, 100).RGBA()
	if r>>8 != 200 || g>>8 != 10 || b>>8 != 10 || a>>8 != 255 {
		t.Errorf("scaled icon pixel = %d %d %d %d, want the project's red", r>>8, g>>8, b>>8, a>>8)
	}
}

// Every refusal names the field and the fix.
func TestPWARefusals(t *testing.T) {
	small := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	wide := image.NewNRGBA(image.Rect(0, 0, 600, 512))
	enc := func(i image.Image) []byte {
		var b bytes.Buffer
		_ = png.Encode(&b, i)
		return b.Bytes()
	}
	for _, c := range []struct {
		p    PWA
		want string
	}{
		{PWA{ThemeColor: "red"}, "pwa.theme-color"},
		{PWA{Icon: []byte("GIF89a")}, "pwa.icon is not a PNG"},
		{PWA{Icon: enc(small)}, "512 pixels or more"},
		{PWA{Icon: enc(wide)}, "square PNG"},
	} {
		err := pwaFiles(shellFiles(), "x", &c.p)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: err = %v, want %q", c.p, err, c.want)
		}
	}
}

// The worker holds the small files of the bundle, not itself, not a big
// model, and its version moves exactly when what it holds moves.
func TestPWAWorkerShellAndVersion(t *testing.T) {
	build := func(js string) string {
		f := shellFiles()
		f["app.js"] = []byte(js)
		if err := pwaFiles(f, "x", nil); err != nil {
			t.Fatal(err)
		}
		return string(f["sw.js"])
	}
	f := shellFiles()
	f["index.html"] = []byte("<html>new</html>")
	if err := pwaFiles(f, "x", nil); err != nil {
		t.Fatal(err)
	}
	if string(f["sw.js"]) == build("1") {
		t.Error("a changed index.html left the worker's version alone")
	}
	a, a2, b := build("1"), build("1"), build("2")
	if a != a2 {
		t.Error("the same bundle gave two workers")
	}
	if a == b {
		t.Error("a changed app.js left the worker's version alone: a redeployed HMI keeps its old shell")
	}
	for _, want := range []string{`"index.html"`, `"app.js"`, `"manifest.webmanifest"`, `"icon-192.png"`} {
		if !strings.Contains(a, want) {
			t.Errorf("the shell lacks %s", want)
		}
	}
	// built again over its own output, the worker still does not hold itself
	again := shellFiles()
	if err := pwaFiles(again, "x", nil); err != nil {
		t.Fatal(err)
	}
	if err := pwaFiles(again, "x", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(again["sw.js"]), `"sw.js"`) {
		t.Error("the worker holds itself after a second build")
	}
	if !strings.Contains(a, `"_app/f.woff2"`) {
		t.Error("the shell lacks the woff2 font")
	}
	for _, not := range []string{`"sw.js"`, `"big.glb"`, `"_app/f.woff"`} {
		if strings.Contains(a, not) {
			t.Errorf("the shell holds %s", not)
		}
	}
}

// swHarness runs the built worker in goja with the few browser parts it
// touches, and reports what it does with a request.
type swHarness struct {
	vm *goja.Runtime
}

func newSW(t *testing.T) *swHarness {
	t.Helper()
	f := shellFiles()
	if err := pwaFiles(f, "x", nil); err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	if _, err := vm.RunString(`
		var handlers = {};
		var self = { location: { origin: 'https://box' }, clients: { claim: function () {} },
			skipWaiting: function () { return Promise.resolve(); },
			addEventListener: function (n, f) { handlers[n] = f; } };
		function URL(u) {
			var m = /^(https?:\/\/[^\/]+)(\/[^?#]*)?/.exec(u);
			this.origin = m[1]; this.pathname = m[2] || '/';
		}
		var cached = { '/index.html': 'SHELL' };
		var networkDown = false, fetched = [];
		var caches = {
			match: function (r, o) { if (!o || o.ignoreVary !== true || !o.cacheName) return Promise.resolve(undefined); var k = typeof r === 'string' ? r : new URL(r.url).pathname; return Promise.resolve(cached[k]); },
			open: function () { return Promise.resolve({ addAll: function () { return Promise.resolve(); } }); },
			keys: function () { return Promise.resolve([]); },
			delete: function () { return Promise.resolve(); }
		};
		function fetch(r) { fetched.push(r.url); return networkDown ? Promise.reject(new Error('offline')) : Promise.resolve('NET'); }
		function Response() {}
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := vm.RunString(string(f["sw.js"])); err != nil {
		t.Fatalf("sw.js: %v", err)
	}
	return &swHarness{vm: vm}
}

// fetchEvent sends a request and says whether the worker answered it, and
// with what.
func (h *swHarness) fetchEvent(t *testing.T, method, url, mode string) (answered bool, body string) {
	t.Helper()
	v, err := h.vm.RunString(`(function () {
		var res = { answered: false, body: null };
		var e = { request: { method: '` + method + `', url: '` + url + `', mode: '` + mode + `' },
			respondWith: function (p) { res.answered = true; Promise.resolve(p).then(function (b) { res.body = b; }); } };
		handlers.fetch(e);
		return res;
	})()`)
	if err != nil {
		t.Fatal(err)
	}
	// let the promise jobs run
	if _, err := h.vm.RunString(`0`); err != nil {
		t.Fatal(err)
	}
	o := v.ToObject(h.vm)
	if s := o.Get("body"); s != nil && !goja.IsNull(s) && !goja.IsUndefined(s) {
		body = s.String()
	}
	return o.Get("answered").ToBoolean(), body
}

// The worker never touches the controller's data: not the API, not the
// live stream's path, not a write, not another origin. An answer from the
// worker there would be an old value shown as live.
func TestPWAWorkerNeverAnswersData(t *testing.T) {
	h := newSW(t)
	for _, c := range []struct{ method, url string }{
		{"GET", "https://box/api/state"},
		{"GET", "https://box/api/stream"},
		{"GET", "https://box/_nautilus/assets/x.css"},
		{"GET", "https://box/sw.js"},
		{"POST", "https://box/api/alarms/ack"},
		{"POST", "https://box/form"},
		{"GET", "https://elsewhere/app.js"},
	} {
		if ans, _ := h.fetchEvent(t, c.method, c.url, "cors"); ans {
			t.Errorf("%s %s: the worker answered it", c.method, c.url)
		}
	}
}

// A screen opens offline from the shell; online, the network answers.
func TestPWAWorkerOpensTheShellOffline(t *testing.T) {
	h := newSW(t)
	if ans, body := h.fetchEvent(t, "GET", "https://box/pumps", "navigate"); !ans || body != "NET" {
		t.Errorf("online navigate = %v %q, want the network's answer", ans, body)
	}
	if _, err := h.vm.RunString(`networkDown = true`); err != nil {
		t.Fatal(err)
	}
	if ans, body := h.fetchEvent(t, "GET", "https://box/pumps", "navigate"); !ans || body != "SHELL" {
		t.Errorf("offline navigate = %v %q, want the cached shell", ans, body)
	}
}

// The default app is installable out of the box: the head links the
// manifest, and the files are in the bundle.
func TestDefaultBuildIsInstallable(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "hmi/src/routes/+page.svelte", "<p>hi</p>\n")
	res, err := Build(Options{Project: dir, App: filepath.Join(dir, "hmi"), Title: "Grain plant", PWA: &PWA{ThemeColor: "#102030"}})
	if err != nil {
		t.Fatal(err)
	}
	html := string(res.Files["index.html"])
	for _, want := range []string{`rel="manifest" href="/manifest.webmanifest"`, `content="#102030"`, `/pwa.js`, `apple-touch-icon`} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html lacks %s:\n%s", want, html)
		}
	}
	for _, f := range []string{"manifest.webmanifest", "sw.js", "pwa.js", "icon-192.png", "icon-512.png"} {
		if len(res.Files[f]) == 0 {
			t.Errorf("no %s in the bundle", f)
		}
	}
	if !strings.Contains(string(res.Files["sw.js"]), `"index.html"`) {
		t.Error("the worker's shell lacks index.html: it was written after the worker")
	}
	// a different theme and name make a different worker
	res2, err := Build(Options{Project: dir, App: filepath.Join(dir, "hmi"), Title: "Other", PWA: &PWA{ThemeColor: "#304050"}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(res.Files["sw.js"], res2.Files["sw.js"]) {
		t.Error("two different HMIs got the same worker version")
	}
}

// An app that ships its own manifest or worker keeps them: naut adds
// nothing and links nothing.
func TestAnAppWithItsOwnManifestKeepsIt(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "hmi/src/routes/+page.svelte", "<p>hi</p>\n")
	write(t, dir, "hmi/static/manifest.webmanifest", `{"name":"Mine"}`)
	res, err := Build(Options{Project: dir, App: filepath.Join(dir, "hmi")})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Files["manifest.webmanifest"]) != `{"name":"Mine"}` {
		t.Errorf("the app's manifest was replaced: %s", res.Files["manifest.webmanifest"])
	}
	if _, ok := res.Files["sw.js"]; ok {
		t.Error("naut added a worker beside the app's own manifest")
	}
	if strings.Contains(string(res.Files["index.html"]), "manifest.webmanifest") {
		t.Error("naut linked a manifest the app did not ask for")
	}
}
