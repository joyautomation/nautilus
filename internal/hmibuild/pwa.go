package hmibuild

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// PWA makes the built HMI installable (#722): a web app manifest, icons, a
// service worker that holds the app shell and nothing else, and a line in
// index.html for each. Every field is optional.
type PWA struct {
	// Name is the installed app's name; "" is the project's title.
	Name string
	// ShortName is the label under the icon (about 12 characters); "" is Name.
	ShortName string
	// ThemeColor is the browser chrome and splash color, "#rrggbb"; ""
	// is the kit's dark background.
	ThemeColor string
	// Icon is a square PNG, 512 pixels or more; nil draws one from
	// ThemeColor.
	Icon []byte
}

// DefaultTheme is the kit's dark background (theme.css --bg).
const DefaultTheme = "#0f1419"

// maxIconSide bounds an icon's width and height before it is decoded.
const maxIconSide = 4096

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Validate says what is wrong with p, in words that name the fix.
func (p *PWA) Validate() error {
	if p.ThemeColor != "" && !hexColor.MatchString(p.ThemeColor) {
		return fmt.Errorf("pwa.theme-color %q is not a color like \"#0f1419\"", p.ThemeColor)
	}
	if len(p.Icon) > 0 {
		// the header first: a small file can claim a huge picture
		cfg, err := iconConfig(p.Icon)
		if err != nil {
			return fmt.Errorf("pwa.icon is not a PNG: %v", err)
		}
		if cfg.Width != cfg.Height || cfg.Width < 512 {
			return fmt.Errorf("pwa.icon is %dx%d: it must be a square PNG of 512 pixels or more", cfg.Width, cfg.Height)
		}
		if cfg.Width > maxIconSide {
			return fmt.Errorf("pwa.icon is %dx%d: use a 512 or 1024 pixel PNG (at most %d)", cfg.Width, cfg.Height, maxIconSide)
		}
	}
	return nil
}

// pwaFiles are the files that make the bundle installable, added to files.
// The service worker's version is a hash of what it holds, so a rebuilt HMI
// replaces the old shell and an unchanged one keeps it.
func pwaFiles(files map[string][]byte, title string, p *PWA) error {
	if p == nil {
		p = &PWA{}
	}
	if err := p.Validate(); err != nil {
		return err
	}
	name := firstNonEmpty(p.Name, title, "Nautilus HMI")
	short := firstNonEmpty(p.ShortName, name)
	theme := strings.ToLower(firstNonEmpty(p.ThemeColor, DefaultTheme))

	if err := iconFiles(files, p, theme); err != nil {
		return err
	}

	manifest, _ := json.MarshalIndent(map[string]any{
		"name":             name,
		"short_name":       short,
		"start_url":        "./",
		"scope":            "./",
		"display":          "standalone",
		"background_color": theme,
		"theme_color":      theme,
		"icons": []map[string]string{
			{"src": "icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
		},
	}, "", "\t")
	files["manifest.webmanifest"] = append(manifest, '\n')
	if _, own := files["pwa.js"]; !own {
		files["pwa.js"] = []byte(pwaRegister)
	}

	// the shell: everything small the build wrote, but not the worker
	var shell []string
	h := sha256.New()
	names := make([]string, 0, len(files))
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)
	for _, f := range names {
		// .woff is the old format: a browser with a service worker reads woff2
		if f == "sw.js" || len(files[f]) > shellMaxFile || strings.HasSuffix(f, ".woff") {
			continue
		}
		shell = append(shell, escapePath(f))
		h.Write([]byte(f))
		h.Write(files[f])
	}
	list, _ := json.Marshal(shell)
	version := hex.EncodeToString(h.Sum(nil)[:8])
	files["sw.js"] = []byte(strings.NewReplacer("%VERSION%", version, "%SHELL%", string(list)).Replace(pwaWorker))
	return nil
}

// shellMaxFile is the largest file the worker holds ahead of time (3D
// models and the like are fetched when used).
const shellMaxFile = 1 << 20

// escapePath is a file's path as a URL path: a name with %, # or ? asks for
// itself, not for something else.
func escapePath(f string) string {
	parts := strings.Split(f, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// pwaHead is what index.html gets for the manifest, theme and icon.
func pwaHead(theme string) string {
	return strings.Join([]string{
		`<link rel="manifest" href="/manifest.webmanifest">`,
		`<meta name="theme-color" content="` + theme + `">`,
		`<link rel="apple-touch-icon" href="/apple-touch-icon.png">`,
		`<meta name="mobile-web-app-capable" content="yes">`,
		`<meta name="apple-mobile-web-app-capable" content="yes">`,
		`<script type="module" src="/pwa.js"></script>`,
	}, "\n\t\t")
}

const pwaRegister = `if ('serviceWorker' in navigator && window.isSecureContext) {
	// a new worker takes over a page the old one was serving: reload once,
	// so the page and the chunks it loads are one build (not on the first
	// install, when there was no old page to replace)
	var had = !!navigator.serviceWorker.controller;
	var reloaded = false;
	navigator.serviceWorker.addEventListener('controllerchange', function () {
		if (!had || reloaded) { had = true; return; }
		reloaded = true;
		location.reload();
	});
	navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(function (e) {
		console.error('the HMI cannot be installed: its service worker did not register', e);
	});
}
`

// pwaWorker holds the app shell and answers nothing else. The controller's
// data (/api, the live stream, /_nautilus) is never stored and never
// answered by the worker, so an offline HMI can show its shell and the kit
// shows "not connected", never an old value as a live one. Files under
// _app/ carry a content hash in their name and are answered from the shell;
// the files without one (the page, app.js, app.css) ask the network first,
// so a redeployed HMI is never run from an old copy.
const pwaWorker = `// nautilus HMI service worker (version %VERSION%)
const CACHE = 'nautilus-hmi-%VERSION%';
const SHELL = %SHELL%;
// the controller varies its answers on Origin (CORS): a module script asks with
// one and the shell was stored without, so a match must not compare it
const MATCH = { cacheName: CACHE, ignoreVary: true };

// A file the controller cannot give, or that it answers with the page (its
// answer to a path it has no file for), is not stored: the install fails and
// the old shell stays, with the reason in the console.
async function store(cache, file) {
	const res = await fetch('/' + file, { cache: 'reload' });
	const type = res.headers.get('content-type') || '';
	if (!res.ok || (type.indexOf('text/html') >= 0 && file !== 'index.html')) {
		throw new Error('the HMI shell cannot hold ' + file + ' (' + res.status + ' ' + type + ')');
	}
	await cache.put('/' + file, res);
}

self.addEventListener('install', (e) => {
	e.waitUntil(
		caches.open(CACHE)
			.then((c) => Promise.all(SHELL.map((f) => store(c, f))))
			.then(() => self.skipWaiting())
			.catch((err) => { console.error(err); throw err; })
	);
});

self.addEventListener('activate', (e) => {
	e.waitUntil(
		caches.keys()
			.then((ks) => Promise.all(ks.filter((k) => k.startsWith('nautilus-hmi-') && k !== CACHE).map((k) => caches.delete(k))))
			.then(() => self.clients.claim())
	);
});

// A page from the controller (Web Push, #722). Every push shows a
// notification: a browser that is told of a push and shows nothing may take
// the subscription away, and an alarm that could not be read must still
// reach the person. The payload is the controller's own page for one alarm
// ({alarm, name, state, priority, area, atMs, level, ack}); anything missing
// or malformed falls back to a plain one. The tag is the alarm, so a repeat
// replaces rather than piles up.
//
// ack is the page's acknowledgement link (#715): the controller's
// /alarm-ack/<token> under its ack-url, which may carry a path. Anything
// else in it is ignored, so a tap only ever opens an ack link or the
// alarms.
const ACK = /^https?:\/\/[^\/?#@\\\s\x00-\x1f\x7f]+(\/[^?#@\\\s\x00-\x1f\x7f]*)?\/alarm-ack\/[A-Za-z0-9_-]+$/;

// A page's screen (#1249): the alarm, or a flood's focus view, on this
// origin. Anything else opens the alarms.
const PATH = /^\/alarms(\?[A-Za-z0-9=&%._~+-]{0,512})?$/;

function pagePath(v) {
	return typeof v === 'string' && PATH.test(v) ? v : '/alarms';
}

function ackLink(v) {
	return typeof v === 'string' && v.length <= 4096 && ACK.test(v) ? v : '';
}

function describe(d) {
	const str = (v) => (typeof v === 'string' ? Array.from(v).slice(0, 120).join('') : '');
	const name = str(d.name) || str(d.alarm) || 'Alarm';
	const pri = str(d.priority);
	const state = str(d.state).replace(/-/g, ' ');
	const ack = ackLink(d.ack);
	const parts = [];
	if (state) parts.push(state);
	if (str(d.area)) parts.push(str(d.area));
	if (typeof d.atMs === 'number' && d.atMs > 0) {
		parts.push(new Date(d.atMs).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }));
	}
	let body = parts.join(' · ');
	if (ack) body += (body ? '\n' : '') + 'Tap to acknowledge';
	const options = {
		body: body,
		tag: 'alarm-' + (str(d.alarm) || 'page'),
		renotify: true,
		data: { url: pagePath(d.path), ack: ack },
	};
	// Where a browser shows actions, the alarms stay one press away.
	if (ack) options.actions = [{ action: 'alarms', title: 'Open alarms' }];
	return { title: (pri ? pri.toUpperCase() + ': ' : '') + name, options: options };
}

self.addEventListener('push', (e) => {
	let d = {};
	try {
		d = (e.data && e.data.json()) || {};
	} catch (_) {
		d = {};
	}
	const n = describe(d);
	e.waitUntil(self.registration.showNotification(n.title, n.options));
});

// A tap on a page with an ack link opens the link's confirm page in a
// window of its own: it shows the alarm and its state, and the person
// presses Acknowledge there (a tap on a lock screen by mistake acks
// nothing), with the HMI they had open left where it was. The worker never
// calls the controller itself. Otherwise (no link, or the Open alarms
// action) a tap opens the alarms: an open window of this HMI is brought
// forward and sent there, otherwise one is opened.
self.addEventListener('notificationclick', (e) => {
	e.notification.close();
	const data = e.notification.data || {};
	const ack = e.action === 'alarms' ? '' : ackLink(data.ack);
	if (ack) {
		// the notification is gone: a window refused still leaves the alarms
		e.waitUntil(self.clients.openWindow(ack).catch(() => self.clients.openWindow('/alarms')));
		return;
	}
	const url = data.url || '/alarms';
	e.waitUntil(
		self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((ws) => {
			const mine = ws.filter((w) => new URL(w.url).origin === self.location.origin && 'focus' in w);
			// the one the person is looking at, else any
			const w = mine.find((c) => c.visibilityState === 'visible') || mine[0];
			if (!w) return self.clients.openWindow(url);
			// navigate() rejects for a window this worker does not control: open one then
			return w.focus().then((f) => (f && 'navigate' in f ? f.navigate(url) : undefined)).catch(() => self.clients.openWindow(url));
		})
	);
});

function isData(url) {
	// an ack link's page is the controller's, never the app's shell
	return url.pathname.startsWith('/api/') || url.pathname.startsWith('/_nautilus/') || url.pathname === '/sw.js' ||
		url.pathname.includes('/alarm-ack/');
}

self.addEventListener('fetch', (e) => {
	const req = e.request;
	if (req.method !== 'GET') return;
	const url = new URL(req.url);
	if (url.origin !== self.location.origin || isData(url)) return; // the browser handles it
	if (req.mode === 'navigate') {
		// the shell answers every screen: the app routes on the client
		e.respondWith(fetch(req).catch(() => caches.match('/index.html', MATCH)));
		return;
	}
	if (url.pathname.startsWith('/_app/')) {
		e.respondWith(caches.match(req, MATCH).then((hit) => hit || fetch(req)));
		return;
	}
	// no hash in the name: the network's copy when there is one
	e.respondWith(fetch(req).catch(() => caches.match(req, MATCH)));
});
`
