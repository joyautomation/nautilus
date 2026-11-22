package hmibuild

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
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

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Validate says what is wrong with p, in words that name the fix.
func (p *PWA) Validate() error {
	if p.ThemeColor != "" && !hexColor.MatchString(p.ThemeColor) {
		return fmt.Errorf("pwa.theme-color %q is not a color like \"#0f1419\"", p.ThemeColor)
	}
	if len(p.Icon) > 0 {
		img, err := png.Decode(bytes.NewReader(p.Icon))
		if err != nil {
			return fmt.Errorf("pwa.icon is not a PNG: %v", err)
		}
		b := img.Bounds()
		if b.Dx() != b.Dy() || b.Dx() < 512 {
			return fmt.Errorf("pwa.icon is %dx%d: it must be a square PNG of 512 pixels or more", b.Dx(), b.Dy())
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

	var src image.Image
	if len(p.Icon) > 0 {
		src, _ = png.Decode(bytes.NewReader(p.Icon)) // checked above
	} else {
		src = drawnIcon(512, theme)
	}
	icons := map[string]int{"icon-192.png": 192, "icon-512.png": 512, "apple-touch-icon.png": 180}
	for f, size := range icons {
		var buf bytes.Buffer
		if err := png.Encode(&buf, scale(src, size)); err != nil {
			return err
		}
		files[f] = buf.Bytes()
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
	files["pwa.js"] = []byte(pwaRegister)

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
		shell = append(shell, f)
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

// drawnIcon is the icon of a project that gave none: a rounded square in
// the theme color with a lighter mark, so two projects are told apart by
// their name under the icon and a project that cares gives pwa.icon.
func drawnIcon(n int, theme string) image.Image {
	var r, g, b uint8
	fmt.Sscanf(theme, "#%02x%02x%02x", &r, &g, &b)
	bg := color.NRGBA{r, g, b, 255}
	fg := color.NRGBA{uint8(min(255, int(r)+150)), uint8(min(255, int(g)+150)), uint8(min(255, int(b)+150)), 255}
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	c := float64(n) / 2
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := float64(x)+.5-c, float64(y)+.5-c
			d := dx*dx + dy*dy
			switch {
			case d < (c*.55)*(c*.55) && d > (c*.38)*(c*.38):
				img.SetNRGBA(x, y, fg) // a ring: a gauge
			default:
				img.SetNRGBA(x, y, bg)
			}
		}
	}
	return img
}

// scale is a box-filter resize to n x n (the stdlib has none).
func scale(src image.Image, n int) *image.NRGBA {
	sb := src.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		y0, y1 := sb.Min.Y+y*sb.Dy()/n, sb.Min.Y+(y+1)*sb.Dy()/n
		for x := 0; x < n; x++ {
			x0, x1 := sb.Min.X+x*sb.Dx()/n, sb.Min.X+(x+1)*sb.Dx()/n
			var sr, sg, sbl, sa, cnt uint32
			for yy := y0; yy < max(y1, y0+1); yy++ {
				for xx := x0; xx < max(x1, x0+1); xx++ {
					r, g, b, a := src.At(xx, yy).RGBA() // premultiplied
					sr += r >> 8
					sg += g >> 8
					sbl += b >> 8
					sa += a >> 8
					cnt++
				}
			}
			if sa == 0 {
				continue
			}
			// back to straight alpha
			out.SetNRGBA(x, y, color.NRGBA{uint8(sr * 255 / sa), uint8(sg * 255 / sa), uint8(sbl * 255 / sa), uint8(sa / cnt)})
		}
	}
	return out
}

const pwaRegister = `if ('serviceWorker' in navigator && window.isSecureContext) {
	navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(function () {});
}
`

// pwaWorker holds the app shell and answers nothing else. The controller's
// data (/api, the live stream, /_nautilus) is never stored and never
// answered by the worker, so an offline HMI can show its shell and the kit
// shows "not connected", never an old value as a live one.
const pwaWorker = `// nautilus HMI service worker (version %VERSION%)
const CACHE = 'nautilus-hmi-%VERSION%';
const SHELL = %SHELL%;
// the controller varies its answers on Origin (CORS): a module script asks with
// one and the shell was stored without, so a match must not compare it
const MATCH = { cacheName: CACHE, ignoreVary: true };

self.addEventListener('install', (e) => {
	e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL.map((f) => '/' + f))).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
	e.waitUntil(
		caches.keys()
			.then((ks) => Promise.all(ks.filter((k) => k.startsWith('nautilus-hmi-') && k !== CACHE).map((k) => caches.delete(k))))
			.then(() => self.clients.claim())
	);
});

function isData(url) {
	return url.pathname.startsWith('/api/') || url.pathname.startsWith('/_nautilus/') || url.pathname === '/sw.js';
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
	e.respondWith(caches.match(req, MATCH).then((hit) => hit || fetch(req)));
});
`
