package hmibuild

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/crc32"
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
// touches, and reports what it does.
type swHarness struct {
	vm *goja.Runtime
}

func newSWWith(t *testing.T, files map[string][]byte) *swHarness {
	t.Helper()
	if err := pwaFiles(files, "x", nil); err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	if _, err := vm.RunString(`
		var handlers = {};
		var self = { location: { origin: 'https://box' }, clients: { claim: function () { claimed = true; } },
			skipWaiting: function () { return Promise.resolve(); },
			addEventListener: function (n, f) { handlers[n] = f; } };
		var claimed = false;
		function URL(u) {
			var m = /^(https?:\/\/[^\/]+)(\/[^?#]*)?/.exec(u);
			this.origin = m[1]; this.pathname = m[2] || '/';
		}
		var console = { error: function () { errors.push(Array.prototype.join.call(arguments, ' ')); } };
		var errors = [];
		// what the controller answers per path: {status, type}; default 200 text/javascript
		var served = {};
		var networkDown = false, fetched = [];
		var stores = {}, deleted = [], existing = ['nautilus-hmi-old', 'other-app'];
		var held = {};   // cached path -> body
		function res(path, body) {
			var o = served[path] || {};
			var type = o.type || 'text/javascript', status = o.status || 200;
			return { ok: status < 400, status: status, headers: { get: function () { return type; } }, body: body };
		}
		function fetch(r) {
			var u = typeof r === 'string' ? r : r.url;
			var path = u.replace('https://box', '');
			fetched.push(path);
			return networkDown ? Promise.reject(new Error('offline')) : Promise.resolve(res(path, 'NET:' + path));
		}
		var caches = {
			match: function (r, o) {
				if (!o || o.ignoreVary !== true || !o.cacheName) return Promise.resolve(undefined);
				var k = typeof r === 'string' ? r : new URL(r.url).pathname;
				return Promise.resolve(held[k] === undefined ? undefined : { body: 'CACHE:' + k });
			},
			open: function () { return Promise.resolve({ put: function (k, r) { held[k] = r.body; return Promise.resolve(); } }); },
			keys: function () { return Promise.resolve(existing.concat([CACHE])); },
			delete: function (k) { deleted.push(k); return Promise.resolve(); }
		};
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := vm.RunString(string(files["sw.js"])); err != nil {
		t.Fatalf("sw.js: %v", err)
	}
	return &swHarness{vm: vm}
}

func newSW(t *testing.T) *swHarness { return newSWWith(t, shellFiles()) }

func (h *swHarness) run(t *testing.T, js string) goja.Value {
	t.Helper()
	v, err := h.vm.RunString(js)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// lifecycle sends install or activate and says whether it settled, and how.
func (h *swHarness) lifecycle(t *testing.T, name string) (ok bool, err string) {
	t.Helper()
	h.run(t, `var life = { done: false, err: null };
		handlers['`+name+`']({ waitUntil: function (p) { p.then(function () { life.done = true; }, function (e) { life.err = String(e); }); } });`)
	h.run(t, `0`) // let the promise jobs run
	o := h.run(t, `life`).ToObject(h.vm)
	if e := o.Get("err"); e != nil && !goja.IsNull(e) {
		return false, e.String()
	}
	return o.Get("done").ToBoolean(), ""
}

// fetchEvent sends a request and says whether the worker answered it, and
// with what.
func (h *swHarness) fetchEvent(t *testing.T, method, url, mode string) (answered bool, body string) {
	t.Helper()
	v := h.run(t, `(function () {
		var res = { answered: false, body: null };
		var e = { request: { method: '`+method+`', url: '`+url+`', mode: '`+mode+`' },
			respondWith: function (p) { res.answered = true; Promise.resolve(p).then(function (b) { res.body = b && b.body; }); } };
		handlers.fetch(e);
		return res;
	})()`)
	h.run(t, `0`)
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
	h.run(t, `held['/index.html'] = 'x'`)
	if ans, body := h.fetchEvent(t, "GET", "https://box/pumps", "navigate"); !ans || body != "NET:/pumps" {
		t.Errorf("online navigate = %v %q, want the network's answer", ans, body)
	}
	h.run(t, `networkDown = true`)
	if ans, body := h.fetchEvent(t, "GET", "https://box/pumps", "navigate"); !ans || body != "CACHE:/index.html" {
		t.Errorf("offline navigate = %v %q, want the cached shell", ans, body)
	}
}

// A redeployed HMI is never run from an old copy: the files with no hash in
// their name (app.js, app.css) ask the network first and fall back to the
// shell only offline; the hashed ones under _app/ come from the shell.
func TestPWAWorkerNeverRunsAnOldAppJS(t *testing.T) {
	h := newSW(t)
	h.run(t, `held['/app.js'] = 'old'; held['/_app/chunk-AB12.js'] = 'chunk'`)
	if _, body := h.fetchEvent(t, "GET", "https://box/app.js", "cors"); body != "NET:/app.js" {
		t.Errorf("online app.js = %q, want the network's, not the shell's old copy", body)
	}
	h.run(t, `fetched.length = 0`)
	if _, body := h.fetchEvent(t, "GET", "https://box/_app/chunk-AB12.js", "cors"); body != "CACHE:/_app/chunk-AB12.js" {
		t.Errorf("hashed chunk = %q, want the shell's", body)
	}
	if n := h.run(t, `fetched.length`).ToInteger(); n != 0 {
		t.Errorf("a hashed chunk in the shell went to the network (%d fetches)", n)
	}
	h.run(t, `networkDown = true`)
	if _, body := h.fetchEvent(t, "GET", "https://box/app.js", "cors"); body != "CACHE:/app.js" {
		t.Errorf("offline app.js = %q, want the shell's", body)
	}
}

// Install holds every file of the shell, under its escaped URL, then takes
// over; activate drops the older versions' caches and no one else's.
func TestPWAWorkerInstallAndActivate(t *testing.T) {
	files := shellFiles()
	files["static/100%.png"] = []byte("p")
	h := newSWWith(t, files)
	if ok, err := h.lifecycle(t, "install"); !ok {
		t.Fatalf("install: %v", err)
	}
	if got := h.run(t, `held['/app.js']`).String(); got != "NET:/app.js" {
		t.Errorf("app.js held as %q", got)
	}
	// a name with % asks for itself: the fetch is for the escaped path
	if !h.run(t, `fetched.indexOf('/static/100%25.png') >= 0`).ToBoolean() {
		t.Errorf("the shell fetched %v, not the escaped /static/100%%25.png", h.run(t, `fetched`).Export())
	}
	if ok, err := h.lifecycle(t, "activate"); !ok {
		t.Fatalf("activate: %v", err)
	}
	if got := h.run(t, `deleted.join(',')`).String(); got != "nautilus-hmi-old" {
		t.Errorf("deleted caches = %q, want only the older HMI shell", got)
	}
	if !h.run(t, `claimed`).ToBoolean() {
		t.Error("the new worker did not take over the open pages")
	}
}

// A file the controller cannot give, or gives as the page (its answer to a
// path with no file), fails the install, and the reason is in the console:
// not a shell holding HTML as a script, not a silent old shell.
func TestPWAWorkerInstallRefusesWhatItCannotHold(t *testing.T) {
	for name, served := range map[string]string{
		"404":          `served['/app.js'] = { status: 404 }`,
		"html as a js": `served['/app.js'] = { type: 'text/html; charset=utf-8' }`,
	} {
		h := newSW(t)
		h.run(t, served)
		ok, err := h.lifecycle(t, "install")
		if ok || !strings.Contains(err, "cannot hold app.js") {
			t.Errorf("%s: install ok=%v err=%q, want it to fail naming app.js", name, ok, err)
		}
		if n := h.run(t, `errors.length`).ToInteger(); n == 0 {
			t.Errorf("%s: nothing in the console", name)
		}
	}
	// index.html itself is HTML
	h := newSW(t)
	h.run(t, `served['/index.html'] = { type: 'text/html' }`)
	if ok, err := h.lifecycle(t, "install"); !ok {
		t.Errorf("index.html as HTML refused: %v", err)
	}
}

// pwa.js reloads a page once when a new worker takes it over (the page and
// its chunks must be one build), and not on the first install.
func TestPWARegisterReloadsOnTakeover(t *testing.T) {
	run := func(controlled bool) (reloads int) {
		vm := goja.New()
		ctrl := "null"
		if controlled {
			ctrl = "{}"
		}
		if _, err := vm.RunString(`
			var reloads = 0, listener = null, registered = null;
			var window = { isSecureContext: true };
			var location = { reload: function () { reloads++; } };
			var navigator = { serviceWorker: { controller: ` + ctrl + `,
				addEventListener: function (n, f) { if (n === 'controllerchange') listener = f; },
				register: function (u, o) { registered = u + '|' + o.scope; return Promise.resolve(); } } };
			var console = { error: function () {} };
		`); err != nil {
			t.Fatal(err)
		}
		if _, err := vm.RunString(pwaRegister); err != nil {
			t.Fatal(err)
		}
		if got := vm.Get("registered").String(); got != "/sw.js|/" {
			t.Errorf("registered %q", got)
		}
		_, _ = vm.RunString(`listener(); if (` + ctrl + ` !== null) listener();`)
		return int(vm.Get("reloads").ToInteger())
	}
	if n := run(true); n != 1 {
		t.Errorf("a takeover of a controlled page reloaded %d times, want once", n)
	}
	if n := run(false); n != 0 {
		t.Errorf("the first install reloaded the page (%d)", n)
	}
}

// A PNG that claims a huge picture is refused from its header, not decoded.
func TestPWAIconBombRefused(t *testing.T) {
	// 8-bit grayscale IHDR claiming 40000 x 40000, then nothing
	hdr := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13, 'I', 'H', 'D', 'R',
		0, 0, 0x9c, 0x40, 0, 0, 0x9c, 0x40, 8, 0, 0, 0, 0}
	crc := crc32IEEE(hdr[12:])
	hdr = append(hdr, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	err := (&PWA{Icon: hdr}).Validate()
	if err == nil || !strings.Contains(err.Error(), "at most 4096") {
		t.Errorf("err = %v, want the size refused from the header", err)
	}
}

// What the app ships itself under a name naut would write is kept.
func TestPWAKeepsTheAppsOwnIconAndRegisterScript(t *testing.T) {
	files := shellFiles()
	files["icon-192.png"] = []byte("mine")
	files["pwa.js"] = []byte("// mine")
	if err := pwaFiles(files, "x", nil); err != nil {
		t.Fatal(err)
	}
	if string(files["icon-192.png"]) != "mine" || string(files["pwa.js"]) != "// mine" {
		t.Error("naut overwrote a file the app ships")
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

func crc32IEEE(b []byte) uint32 { return crc32.ChecksumIEEE(b) }

// The playground builds without the manifest and worker (it is not served
// from the root of its own origin).
func TestNoPWABuild(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "hmi/src/routes/+page.svelte", "<p>hi</p>\n")
	res, err := Build(Options{Project: dir, App: filepath.Join(dir, "hmi"), NoPWA: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"manifest.webmanifest", "sw.js", "pwa.js", "icon-192.png"} {
		if _, ok := res.Files[f]; ok {
			t.Errorf("%s in a NoPWA build", f)
		}
	}
	if strings.Contains(string(res.Files["index.html"]), "manifest") {
		t.Error("index.html links a manifest in a NoPWA build")
	}
}

// pushHarness adds what the push and click handlers touch.
func pushHarness(t *testing.T) *swHarness {
	h := newSW(t)
	h.run(t, `
		var shown = [], opened = [], focused = [], navigated = [], closed = 0;
		var windows = [];   // {url, canNavigate}
		self.registration = { showNotification: function (title, options) { shown.push({ title: title, options: options }); return Promise.resolve(); } };
		self.clients.matchAll = function () {
			return Promise.resolve(windows.map(function (w) {
				var o = { url: w.url, focus: function () { focused.push(w.url); return Promise.resolve(o); } };
				if (w.canNavigate) o.navigate = function (u) { navigated.push(u); return Promise.resolve(o); };
				return o;
			}));
		};
		self.clients.openWindow = function (u) { opened.push(u); return Promise.resolve({}); };
		function pushOf(data) {
			return { data: data === undefined ? null : { json: function () { if (data instanceof Error) throw data; return data; } },
				waitUntil: function (p) { pending = p; } };
		}
		var pending = null;
	`)
	return h
}

// A page shows one notification whatever it holds: a push the browser is
// told of and shows nothing for may cost the subscription, and an alarm
// that could not be read must still reach the person.
func TestPWAWorkerPushAlwaysShows(t *testing.T) {
	for name, data := range map[string]string{
		"full":        `{alarm:'HiTemp', name:'Wet well high', state:'unack-active', priority:'high', area:'wetwell', atMs: 1700000000000, level:1}`,
		"no name":     `{alarm:'HiTemp', state:'unack-active'}`,
		"empty":       `{}`,
		"null":        `null`,
		"a string":    `'boom'`,
		"a number":    `42`,
		"no data":     `undefined`,
		"bad json":    `new Error('not json')`,
		"wrong types": `{alarm: 7, name: {x:1}, state: [], priority: 3, area: true, atMs: 'soon'}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := pushHarness(t)
			h.run(t, `handlers.push(pushOf(`+data+`)); pending`)
			h.run(t, `0`)
			if h.run(t, `pending === null`).ToBoolean() {
				t.Fatal("the worker did not hold itself open (waitUntil) while it showed the notification")
			}
			if n := h.run(t, `shown.length`).ToInteger(); n != 1 {
				t.Fatalf("%d notifications shown, want exactly 1", n)
			}
			if title := h.run(t, `shown[0].title`).String(); title == "" || title == "undefined" {
				t.Errorf("title %q", title)
			}
			if tag := h.run(t, `shown[0].options.tag`).String(); !strings.HasPrefix(tag, "alarm-") {
				t.Errorf("tag %q", tag)
			}
		})
	}
}

func TestPWAWorkerPushSaysTheAlarm(t *testing.T) {
	h := pushHarness(t)
	h.run(t, `handlers.push(pushOf({alarm:'HiTemp', name:'Wet well high', state:'unack-active', priority:'high', area:'wetwell', atMs: 0}))`)
	h.run(t, `0`)
	if got := h.run(t, `shown[0].title`).String(); got != "HIGH: Wet well high" {
		t.Errorf("title %q", got)
	}
	if got := h.run(t, `shown[0].options.body`).String(); got != "unack active · wetwell" {
		t.Errorf("body %q", got)
	}
	// A repeat of the same alarm replaces: one tag per alarm, renotify.
	if h.run(t, `shown[0].options.tag`).String() != "alarm-HiTemp" || !h.run(t, `shown[0].options.renotify`).ToBoolean() {
		t.Error("a repeat of an alarm does not replace its notification")
	}
	// A hostile name is cut, not shown at any length.
	h.run(t, `handlers.push(pushOf({alarm:'A', name: new Array(5000).join('x')}))`)
	h.run(t, `0`)
	if n := len(h.run(t, `shown[1].title`).String()); n > 200 {
		t.Errorf("a %d-character title", n)
	}
}

func TestPWAWorkerClickOpensTheAlarmsAndActsOnNothing(t *testing.T) {
	click := func(h *swHarness) {
		h.run(t, `var note = { closed: false, data: { url: '/alarms' }, close: function () { this.closed = true; } };
			handlers.notificationclick({ notification: note, waitUntil: function (p) { pending = p; } })`)
		h.run(t, `0`)
		if h.run(t, `pending === null`).ToBoolean() {
			t.Error("the worker did not hold itself open (waitUntil) for the tap")
		}
		if !h.run(t, `note.closed`).ToBoolean() {
			t.Error("the notification stays after a tap")
		}
	}
	// No window: one is opened on the alarms.
	h := pushHarness(t)
	click(h)
	if got := h.run(t, `opened.join(',')`).String(); got != "/alarms" {
		t.Errorf("opened %q", got)
	}
	// A window of the HMI: brought forward and sent to the alarms, none opened.
	h = pushHarness(t)
	h.run(t, `windows.push({ url: 'https://box/trends', canNavigate: true })`)
	click(h)
	if h.run(t, `focused.join(',')`).String() != "https://box/trends" || h.run(t, `navigated.join(',')`).String() != "/alarms" || h.run(t, `opened.length`).ToInteger() != 0 {
		t.Errorf("focused %v navigated %v opened %v", h.run(t, `focused`), h.run(t, `navigated`), h.run(t, `opened`))
	}
	// A window of another origin is not touched.
	h = pushHarness(t)
	h.run(t, `windows.push({ url: 'https://elsewhere/x', canNavigate: true })`)
	click(h)
	if h.run(t, `focused.length`).ToInteger() != 0 || h.run(t, `opened.join(',')`).String() != "/alarms" {
		t.Error("another origin's window was focused")
	}
	// The worker never calls the controller from a tap (no ack, no fetch).
	if n := h.run(t, `fetched.length`).ToInteger(); n != 0 {
		t.Errorf("a tap fetched %d times", n)
	}
}
