//go:build ignore

// Spike: build a SvelteKit-style SPA (examples/lift-station/hmi) with no
// Node and no npm: esbuild's Go API bundles, Svelte's compiler runs in goja,
// and a small router follows SvelteKit's routes/ conventions.
//
//	hmibuild <hmi dir> <out dir> <node_modules dir> [alias=dir ...]
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
)

var (
	vm      *goja.Runtime
	vmMu    sync.Mutex
	compile goja.Callable
	compMod goja.Callable
	nCompil int
	tCompil time.Duration
)

// compilerJS bundles svelte/compiler into one script goja can run.
func compilerJS(nodeModules string) string {
	cache := filepath.Join(os.TempDir(), "nx-svelte-compiler.js")
	if b, err := os.ReadFile(cache); err == nil {
		return string(b)
	}
	r := api.Build(api.BuildOptions{
		EntryPoints: []string{"svelte/compiler"},
		NodePaths:   []string{nodeModules},
		Bundle:      true,
		Format:      api.FormatIIFE,
		GlobalName:  "svelteCompiler",
		Platform:    api.PlatformBrowser,
		Target:      api.ES2022,
		Write:       false,
		Banner:      map[string]string{"js": "var process = { env: {}, platform: 'browser', versions: {}, cwd: () => '/' };"},
		LogLevel:    api.LogLevelError,
	})
	if len(r.Errors) > 0 {
		panic(fmt.Sprint(r.Errors))
	}
	js := string(r.OutputFiles[0].Contents)
	os.WriteFile(cache, []byte(js), 0o644)
	return js
}

func startCompiler(nodeModules string) {
	t := time.Now()
	vm = goja.New()
	if _, err := vm.RunScript("svelte-compiler.js", "var globalThis = this; var self = this;\n"+compilerJS(nodeModules)); err != nil {
		panic(err)
	}
	sc := vm.Get("svelteCompiler").ToObject(vm)
	compile, _ = goja.AssertFunction(sc.Get("compile"))
	compMod, _ = goja.AssertFunction(sc.Get("compileModule"))
	fmt.Println("compiler ready in", time.Since(t).Round(time.Millisecond))
}

type out struct {
	JS       string
	CSS      string
	Warnings []string
}

func svelteCompile(src, file string, module bool) (out, error) {
	vmMu.Lock()
	defer vmMu.Unlock()
	t := time.Now()
	defer func() { nCompil++; tCompil += time.Since(t) }()
	opts := vm.NewObject()
	opts.Set("filename", file)
	opts.Set("generate", "client")
	fn := compile
	if module {
		fn = compMod
	} else {
		opts.Set("css", "injected")
	}
	v, err := fn(goja.Undefined(), vm.ToValue(src), opts)
	if err != nil {
		return out{}, err
	}
	o := v.ToObject(vm)
	res := out{JS: o.Get("js").ToObject(vm).Get("code").String()}
	if ws, ok := o.Get("warnings").Export().([]any); ok {
		for _, w := range ws {
			if m, ok := w.(map[string]any); ok {
				res.Warnings = append(res.Warnings, fmt.Sprint(m["code"]))
			}
		}
	}
	return res, nil
}

// route is one +page.svelte under src/routes.
type route struct {
	ID      string // SvelteKit's route id: "/", "/3d", "/rack/[id]"
	Page    string
	Pattern string
	Params  []string
}

func routes(dir string) []route {
	var rs []route
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() != "+page.svelte" {
			return nil
		}
		rel, _ := filepath.Rel(dir, filepath.Dir(p))
		id := "/"
		if rel != "." {
			id = "/" + filepath.ToSlash(rel)
		}
		params := []string{}
		var segs []string
		for _, seg := range strings.Split(strings.Trim(id, "/"), "/") {
			if seg == "" {
				continue
			}
			if strings.HasPrefix(seg, "[") && strings.HasSuffix(seg, "]") {
				params = append(params, strings.Trim(seg, "[]"))
				segs = append(segs, `([^/]+)`)
			} else {
				segs = append(segs, regexp.QuoteMeta(seg))
			}
		}
		pat := ""
		if len(segs) > 0 {
			pat = "/" + strings.Join(segs, "/")
		}
		rs = append(rs, route{ID: id, Page: p, Pattern: "^" + pat + "/?$", Params: params})
		return nil
	})
	sort.Slice(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
	return rs
}

func main() {
	hmi, _ := filepath.Abs(os.Args[1])
	outDir, _ := filepath.Abs(os.Args[2])
	nodeModules, _ := filepath.Abs(os.Args[3])
	aliases := map[string]string{}
	for _, a := range os.Args[4:] {
		k, v, _ := strings.Cut(a, "=")
		aliases[k] = v
	}
	startCompiler(nodeModules)
	src := filepath.Join(hmi, "src")
	rs := routes(filepath.Join(src, "routes"))
	layout := filepath.Join(src, "routes", "+layout.svelte")

	var table []string
	for _, r := range rs {
		pj, _ := json.Marshal(r.Params)
		table = append(table, fmt.Sprintf(`{ id: %q, re: new RegExp(%q), params: %s, load: () => import(%q) }`, r.ID, r.Pattern, pj, r.Page))
	}
	router := `// nx:router — SvelteKit's routes/, client-side.
const routes = [` + strings.Join(table, ",\n") + `];
const base = (document.querySelector('meta[name="nx-base"]')?.content || '').replace(/\/$/, '');
export const nav = $state({ Page: null, url: new URL(location.href), route: { id: null }, params: {} });
async function show(url) {
	let path = url.pathname.slice(base.length) || '/';
	for (const r of routes) {
		const m = path.match(r.re);
		if (!m) continue;
		const mod = await r.load();
		nav.params = Object.fromEntries(r.params.map((p, i) => [p, decodeURIComponent(m[i + 1])]));
		nav.route = { id: r.id };
		nav.url = url;
		nav.Page = mod.default;
		return;
	}
	nav.route = { id: null }; nav.url = url; nav.Page = null;
}
export function goto(href) { const u = new URL(href, location.href); history.pushState({}, '', u); return show(u); }
addEventListener('popstate', () => show(new URL(location.href)));
addEventListener('click', (e) => {
	const a = e.target.closest?.('a[href]');
	if (!a || e.defaultPrevented || e.button || e.metaKey || e.ctrlKey || e.shiftKey || a.target) return;
	const u = new URL(a.href, location.href);
	if (u.origin !== location.origin || !u.pathname.startsWith(base)) return;
	e.preventDefault(); goto(u);
});
show(new URL(location.href));
export { base };
`
	root := `<script>
	import Layout from ` + jsq(layout) + `;
	import { nav } from 'nx:router';
</script>
<Layout>{#if nav.Page}<nav.Page data={{}} params={nav.params} />{/if}</Layout>
`
	entry := `import { mount } from 'svelte';
import Root from 'nx:root.svelte';
mount(Root, { target: document.getElementById('app') });
`
	appState := `import { nav } from 'nx:router';
export const page = { get url() { return nav.url; }, get route() { return nav.route; }, get params() { return nav.params; }, data: {}, status: 200, error: null, form: null, state: {} };
export const navigating = { from: null, to: null };
export const updated = { current: false, check: async () => false };
`
	appPaths := `import { base } from 'nx:router';
export { base };
export const assets = base;
export function resolve(path, params = {}) { return base + path.replace(/\[(\w+)\]/g, (_, k) => params[k] ?? ''); }
`
	appNav := `export { goto } from 'nx:router';
export function invalidate() { return Promise.resolve(); }
export function invalidateAll() { return Promise.resolve(); }
`
	virtual := map[string]struct {
		text   string
		loader api.Loader
		svelte int // 0 plain, 1 component, 2 module
	}{
		"nx:entry":         {entry, api.LoaderJS, 0},
		"nx:root.svelte":   {root, api.LoaderJS, 1},
		"nx:router":        {router, api.LoaderJS, 2},
		"$app/state":       {appState, api.LoaderJS, 0},
		"$app/paths":       {appPaths, api.LoaderJS, 0},
		"$app/navigation":  {appNav, api.LoaderJS, 0},
		"$app/environment": {"export const browser = true, dev = false, building = false, version = '';", api.LoaderJS, 0},
	}
	var warnings []string
	plugin := api.Plugin{Name: "nx-svelte", Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: `^(nx:|\$app/)`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: a.Path, Namespace: "nx"}, nil
		})
		b.OnResolve(api.OnResolveOptions{Filter: `^\$lib(/|$)`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			r := b.Resolve("./"+strings.TrimPrefix(strings.TrimPrefix(a.Path, "$lib"), "/"), api.ResolveOptions{ResolveDir: filepath.Join(src, "lib"), Kind: a.Kind})
			return api.OnResolveResult{Path: r.Path, Errors: r.Errors}, nil
		})
		// one copy of each runtime package for the whole app, the compiler's
		// own Svelte above all: a linked kit's node_modules must not bring a
		// second one (its compiled output calls the runtime it was made for)
		b.OnResolve(api.OnResolveOptions{Filter: `^(svelte|three|@threlte/[^/]+)(/|$)`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if a.PluginData == "pinned" {
				return api.OnResolveResult{}, nil
			}
			r := b.Resolve(a.Path, api.ResolveOptions{ResolveDir: nodeModules, Kind: a.Kind, PluginData: "pinned"})
			return api.OnResolveResult{Path: r.Path, Errors: r.Errors}, nil
		})
		for k, v := range aliases {
			k, v := k, v
			b.OnResolve(api.OnResolveOptions{Filter: "^" + regexp.QuoteMeta(k) + "(/|$)"}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				// as svelte.config's kit.alias: the name stands for the directory
				sub := strings.TrimPrefix(strings.TrimPrefix(a.Path, k), "/")
				r := b.Resolve("./"+sub, api.ResolveOptions{ResolveDir: v, Kind: a.Kind})
				return api.OnResolveResult{Path: r.Path, Errors: r.Errors}, nil
			})
		}
		b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "nx"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			v := virtual[a.Path]
			text := v.text
			if v.svelte > 0 {
				o, err := svelteCompile(text, a.Path, v.svelte == 2)
				if err != nil {
					return api.OnLoadResult{}, err
				}
				text = o.JS
			}
			return api.OnLoadResult{Contents: &text, Loader: api.LoaderJS, ResolveDir: src}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `\.svelte$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			raw, err := os.ReadFile(a.Path)
			if err != nil {
				return api.OnLoadResult{}, err
			}
			o, err := cached(string(raw), a.Path, false)
			if err != nil {
				return api.OnLoadResult{}, fmt.Errorf("%s: %v", a.Path, err)
			}
			for _, w := range o.Warnings {
				warnings = append(warnings, filepath.Base(a.Path)+": "+w)
			}
			js, err := expandGlobs(o.JS, hmi, filepath.Dir(a.Path))
			if err != nil {
				return api.OnLoadResult{}, fmt.Errorf("%s: %v", a.Path, err)
			}
			return api.OnLoadResult{Contents: &js, Loader: api.LoaderJS, ResolveDir: filepath.Dir(a.Path)}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `\.svelte\.(js|ts)$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			raw, err := os.ReadFile(a.Path)
			if err != nil {
				return api.OnLoadResult{}, err
			}
			text := string(raw)
			if strings.HasSuffix(a.Path, ".ts") {
				t := api.Transform(text, api.TransformOptions{Loader: api.LoaderTS, Target: api.ESNext})
				if len(t.Errors) > 0 {
					return api.OnLoadResult{}, fmt.Errorf("%v", t.Errors)
				}
				text = string(t.Code)
			}
			o, err := cached(text, a.Path, true)
			if err != nil {
				return api.OnLoadResult{}, fmt.Errorf("%s: %v", a.Path, err)
			}
			return api.OnLoadResult{Contents: &o.JS, Loader: api.LoaderJS, ResolveDir: filepath.Dir(a.Path)}, nil
		})
	}}

	t := time.Now()
	res := api.Build(api.BuildOptions{
		EntryPoints: []string{"nx:entry"},
		Outdir:      outDir,
		EntryNames:  "app",
		ChunkNames:  "chunks/[name]-[hash]",
		AssetNames:  "assets/[name]-[hash]",
		Bundle:      true,
		Splitting:   true,
		Format:      api.FormatESModule,
		Platform:    api.PlatformBrowser,
		Target:      api.ES2022,
		NodePaths:   []string{nodeModules},
		Conditions:  []string{"svelte", "browser", "import", "default"},
		MainFields:  []string{"svelte", "browser", "module", "main"},
		Loader:      map[string]api.Loader{".woff2": api.LoaderFile, ".woff": api.LoaderFile, ".ttf": api.LoaderFile, ".png": api.LoaderFile, ".svg": api.LoaderFile, ".glb": api.LoaderFile},
		Define:      map[string]string{"process.env.NODE_ENV": `"production"`, "import.meta.env.DEV": "false", "import.meta.env.PROD": "true", "import.meta.env.SSR": "false"},
		Plugins:     []api.Plugin{plugin},
		MinifySyntax: os.Getenv("NX_DEBUG") == "", MinifyWhitespace: os.Getenv("NX_DEBUG") == "", MinifyIdentifiers: os.Getenv("NX_DEBUG") == "",
		Write:       true,
		LogLevel:    api.LogLevelWarning,
		Metafile:    false,
	})
	for _, e := range res.Errors {
		loc := ""
		if e.Location != nil {
			loc = fmt.Sprintf("%s:%d: ", e.Location.File, e.Location.Line)
		}
		fmt.Println("ERROR", loc+e.Text)
	}
	if len(res.Errors) > 0 {
		os.Exit(1)
	}
	css := ""
	if _, err := os.Stat(filepath.Join(outDir, "app.css")); err == nil {
		css = `<link rel="stylesheet" href="/app.css">`
	}
	html := `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">` + css + `<title>HMI</title></head><body><div id="app" style="display: contents"></div><script type="module" src="/app.js"></script></body></html>`
	os.WriteFile(filepath.Join(outDir, "index.html"), []byte(html), 0o644)
	// static/ is served as is, from the app root, as SvelteKit copies it
	if st := filepath.Join(hmi, "static"); dirExists(st) {
		filepath.Walk(st, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(st, p)
			dst := filepath.Join(outDir, rel)
			os.MkdirAll(filepath.Dir(dst), 0o755)
			b, _ := os.ReadFile(p)
			return os.WriteFile(dst, b, 0o644)
		})
	}
	fmt.Printf("built %d routes in %s (%d svelte compiles, %s in the compiler)\n", len(rs), time.Since(t).Round(time.Millisecond), nCompil, tCompil.Round(time.Millisecond))
	if len(warnings) > 0 {
		fmt.Println("compiler warnings:", len(warnings), warnings[:min(5, len(warnings))])
	}
}

var cache sync.Map

func cached(text, file string, module bool) (out, error) {
	h := sha256.Sum256([]byte(fmt.Sprint(module) + file + "\x00" + text))
	k := hex.EncodeToString(h[:])
	if v, ok := cache.Load(k); ok {
		return v.(out), nil
	}
	o, err := svelteCompile(text, file, module)
	if err == nil {
		cache.Store(k, o)
	}
	return o, err
}

func jsq(s string) string { b, _ := json.Marshal(s); return string(b) }

// import.meta.glob, as Vite has it: a literal pattern or array of them
// ("/src/..." from the app root, or relative), optionally { eager: true }.
var globCall = regexp.MustCompile(`import\.meta\.glob\(\s*(\[[^\]]*\]|'[^']*'|"[^"]*")\s*(?:,\s*(\{[^}]*\}))?\s*\)`)
var strLit = regexp.MustCompile(`'([^']*)'|"([^"]*)"`)

func expandGlobs(js, appRoot, dir string) (string, error) {
	var head []string
	n := 0
	outJS := globCall.ReplaceAllStringFunc(js, func(call string) string {
		m := globCall.FindStringSubmatch(call)
		eager := strings.Contains(m[2], "eager") && regexp.MustCompile(`eager\s*:\s*true`).MatchString(m[2])
		var files []string
		for _, lm := range strLit.FindAllStringSubmatch(m[1], -1) {
			pat := lm[1] + lm[2]
			base := dir
			if strings.HasPrefix(pat, "/") {
				base, pat = appRoot, strings.TrimPrefix(pat, "/")
			}
			hits, _ := filepath.Glob(filepath.Join(base, pat))
			files = append(files, hits...)
		}
		sort.Strings(files)
		var entries []string
		for _, f := range files {
			key := f
			if rel, err := filepath.Rel(appRoot, f); err == nil && !strings.HasPrefix(rel, "..") {
				key = "/" + filepath.ToSlash(rel)
			}
			if eager {
				id := fmt.Sprintf("__nx_glob_%d", n)
				n++
				head = append(head, fmt.Sprintf("import * as %s from %s;", id, jsq(f)))
				entries = append(entries, fmt.Sprintf("%s: %s", jsq(key), id))
			} else {
				entries = append(entries, fmt.Sprintf("%s: () => import(%s)", jsq(key), jsq(f)))
			}
		}
		return "{" + strings.Join(entries, ", ") + "}"
	})
	if len(head) == 0 {
		return outJS, nil
	}
	return strings.Join(head, "\n") + "\n" + outJS, nil
}

func dirExists(p string) bool { st, err := os.Stat(p); return err == nil && st.IsDir() }
