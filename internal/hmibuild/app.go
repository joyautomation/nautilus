package hmibuild

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// vkind is how a generated module compiles: plain JS, a Svelte component,
// or a runes module (.svelte.js).
type vkind int

const (
	plain vkind = iota
	component
	module
)

type vmod struct {
	text string
	kind vkind
}

// appSource is an app as naut builds it: its directory, its routes, and
// the modules naut generates for it (entry, router, $app/*).
type appSource struct {
	dir      string // the app's root (src/ and static/ in it)
	routes   []route
	layouts  []layout
	appHTML  string // src/app.html, or naut's own
	virtual  map[string]vmod
	noStatic bool
}

type route struct {
	ID       string   // SvelteKit's route id: "/", "/3d", "/rack/[id]"
	Pattern  string   // a JS RegExp source matching the path after the base
	Params   []string // in the order the pattern captures them
	Page     string   // +page.svelte (absolute), or a nx: module
	PageLoad string   // +page.ts / +page.js, or ""
	Layouts  []int    // indexes into appSource.layouts, root first
}

type layout struct {
	Dir       string // relative to src/routes, "" for the root
	Component string // +layout.svelte, or "" (its children render as they are)
	Load      string // +layout.ts / +layout.js, or ""
}

func (a *appSource) pageIDs() []string {
	var ids []string
	for _, r := range a.routes {
		ids = append(ids, r.ID)
	}
	return ids
}

// Go is the only backend: these run on a server, which a naut-built HMI
// doesn't have.
var serverOnly = regexp.MustCompile(`^\+(page|layout)\.server\.(js|ts)$|^\+server\.(js|ts)$|^hooks\.server\.(js|ts)$`)

// scanApp reads a SvelteKit-style app: src/routes (pages, layouts, their
// load functions), src/app.html, static/.
func scanApp(dir string) (*appSource, error) {
	routesDir := filepath.Join(dir, "src", "routes")
	a := &appSource{dir: dir}
	layoutAt := map[string]int{}
	var problems []string
	err := filepath.WalkDir(routesDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(routesDir, p)
		if serverOnly.MatchString(name) {
			problems = append(problems, fmt.Sprintf("src/routes/%s: runs on a server, and an HMI naut builds has none (Go is the only backend). Read the controller's tag API from the browser instead (a +page.ts load, or the kit's realtime client)", filepath.ToSlash(rel)))
			return nil
		}
		dirRel := filepath.ToSlash(filepath.Dir(rel))
		if dirRel == "." {
			dirRel = ""
		}
		switch name {
		case "+layout.svelte", "+layout.ts", "+layout.js":
			i, ok := layoutAt[dirRel]
			if !ok {
				i = len(a.layouts)
				layoutAt[dirRel] = i
				a.layouts = append(a.layouts, layout{Dir: dirRel})
			}
			if name == "+layout.svelte" {
				a.layouts[i].Component = p
			} else {
				a.layouts[i].Load = p
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", routesDir, err)
	}
	if len(problems) > 0 {
		return nil, &Error{Messages: problems}
	}
	err = filepath.WalkDir(routesDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "+page.svelte" {
			return err
		}
		rel, _ := filepath.Rel(routesDir, filepath.Dir(p))
		dirRel := filepath.ToSlash(rel)
		if dirRel == "." {
			dirRel = ""
		}
		r, err := routeFor(dirRel)
		if err != nil {
			return err
		}
		r.Page = p
		for _, ext := range []string{".ts", ".js"} {
			if f := filepath.Join(filepath.Dir(p), "+page"+ext); exists(f) {
				r.PageLoad = f
			}
		}
		// the layouts above it, root first
		segs := []string{""}
		if dirRel != "" {
			parts := strings.Split(dirRel, "/")
			for i := range parts {
				segs = append(segs, strings.Join(parts[:i+1], "/"))
			}
		}
		for _, s := range segs {
			if i, ok := layoutAt[s]; ok {
				r.Layouts = append(r.Layouts, i)
			}
		}
		a.routes = append(a.routes, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(a.routes) == 0 {
		return nil, &Error{Messages: []string{"src/routes: no +page.svelte, so no page to show"}}
	}
	sortRoutes(a.routes)
	if b, err := os.ReadFile(filepath.Join(dir, "src", "app.html")); err == nil {
		a.appHTML = string(b)
	}
	a.generate()
	return a, nil
}

// routeFor turns a routes/ directory into a route: [param], [...rest],
// [[optional]], and (group) directories, as SvelteKit has them.
func routeFor(dirRel string) (route, error) {
	r := route{ID: "/" + dirRel, Params: []string{}}
	var re strings.Builder
	for _, seg := range strings.Split(dirRel, "/") {
		switch {
		case seg == "":
		case strings.HasPrefix(seg, "(") && strings.HasSuffix(seg, ")"):
			// a group: not part of the URL
		case strings.HasPrefix(seg, "[[") && strings.HasSuffix(seg, "]]"):
			r.Params = append(r.Params, seg[2:len(seg)-2])
			re.WriteString(`(?:/([^/]+))?`)
		case strings.HasPrefix(seg, "[...") && strings.HasSuffix(seg, "]"):
			r.Params = append(r.Params, seg[4:len(seg)-1])
			re.WriteString(`(?:/(.*))?`)
		case strings.HasPrefix(seg, "[") && strings.HasSuffix(seg, "]"):
			r.Params = append(r.Params, seg[1:len(seg)-1])
			re.WriteString(`/([^/]+)`)
		case strings.ContainsAny(seg, "[]"):
			return r, &Error{Messages: []string{fmt.Sprintf("src/routes/%s: a segment that mixes text and [params] isn't supported by naut's build; give the param a segment of its own", dirRel)}}
		default:
			re.WriteString("/" + regexp.QuoteMeta(seg))
		}
	}
	r.Pattern = "^" + re.String() + "/?$"
	return r, nil
}

// sortRoutes puts static segments before params and rest params last, so
// the first match is the most specific, as SvelteKit chooses.
func sortRoutes(rs []route) {
	rank := func(r route) string {
		var b strings.Builder
		for _, seg := range strings.Split(strings.Trim(r.ID, "/"), "/") {
			switch {
			case strings.HasPrefix(seg, "[..."):
				b.WriteByte('3')
			case strings.HasPrefix(seg, "[["):
				b.WriteByte('2')
			case strings.HasPrefix(seg, "["):
				b.WriteByte('1')
			default:
				b.WriteByte('0')
			}
		}
		return b.String()
	}
	sort.SliceStable(rs, func(i, j int) bool {
		ri, rj := rank(rs[i]), rank(rs[j])
		if ri != rj {
			return ri < rj
		}
		return rs[i].ID < rs[j].ID
	})
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func jsq(s string) string { b, _ := json.Marshal(s); return string(b) }

// generate writes the modules naut supplies: the entry, the router (a
// runes module: its state is what $app/state reads), the root and chain
// components that keep layouts mounted while the page under them changes,
// and SvelteKit's $app modules.
func (a *appSource) generate() {
	var layouts, routes []string
	for _, l := range a.layouts {
		comp, load := "null", "null"
		if l.Component != "" {
			comp = "() => import(" + jsq(l.Component) + ")"
		}
		if l.Load != "" {
			load = "() => import(" + jsq(l.Load) + ")"
		}
		layouts = append(layouts, fmt.Sprintf("{ component: %s, load: %s }", comp, load))
	}
	for _, r := range a.routes {
		params, _ := json.Marshal(r.Params)
		chain, _ := json.Marshal(r.Layouts)
		load := "null"
		if r.PageLoad != "" {
			load = "() => import(" + jsq(r.PageLoad) + ")"
		}
		routes = append(routes, fmt.Sprintf("{ id: %s, re: new RegExp(%s), params: %s, layouts: %s, page: () => import(%s), load: %s }",
			jsq(r.ID), jsq(r.Pattern), params, chain, jsq(r.Page), load))
	}
	if a.virtual == nil {
		a.virtual = map[string]vmod{}
	}
	for k, v := range map[string]vmod{
		"nx:entry":              {entryJS, plain},
		"nx:root.svelte":        {rootSvelte, component},
		"nx:chain.svelte":       {chainSvelte, component},
		"nx:passthrough.svelte": {"<script>let { children } = $props();</script>\n{@render children?.()}\n", component},
		"nx:status.svelte":      {statusSvelte, component},
		"nx:router":             {strings.Replace(strings.Replace(routerJS, "/*LAYOUTS*/", strings.Join(layouts, ",\n\t"), 1), "/*ROUTES*/", strings.Join(routes, ",\n\t"), 1), module},
		"$app/state":            {appStateJS, plain},
		"$app/stores":           {appStoresJS, plain},
		"$app/paths":            {appPathsJS, plain},
		"$app/navigation":       {appNavigationJS, plain},
		"$app/environment":      {"export const browser = true, dev = false, building = false, version = '';\n", plain},
	} {
		a.virtual[k] = v
	}
}

const entryJS = `import { mount } from 'svelte';
import Root from 'nx:root.svelte';
mount(Root, { target: document.getElementById('nx-app') });
`

const rootSvelte = `<script>
	import Chain from 'nx:chain.svelte';
	import { nav } from 'nx:router';
</script>
{#if nav.comps.length}<Chain comps={nav.comps} datas={nav.datas} />{/if}
`

// One level of the layout chain: a layout with the rest of the chain as its
// children, or the page. A layout whose component is unchanged between two
// routes stays mounted (its state, its realtime client), as in SvelteKit.
const chainSvelte = `<script>
	import Chain from 'nx:chain.svelte';
	let { comps, datas, i = 0 } = $props();
	const C = $derived(comps[i]);
	const last = $derived(i === comps.length - 1);
</script>
{#if C}{#if last}<C data={datas[i]} />{:else}<C data={datas[i]}><Chain {comps} {datas} i={i + 1} /></C>{/if}{/if}
`

// What shows for a path no page has, or a load that threw.
const statusSvelte = `<script>
	let { data } = $props();
</script>
<div style="padding: 2rem; font-family: system-ui, sans-serif">
	<h1 style="font-size: 1.4rem; margin: 0 0 .5rem">{data.status}</h1>
	<p style="margin: 0; white-space: pre-wrap">{data.message}</p>
</div>
`

// The router: SvelteKit's routes/, client-side. The base is where the app
// is served from (naut run: "/"; the playground: its view path), read from
// <meta name="nx-base">.
const routerJS = `import Passthrough from 'nx:passthrough.svelte';
import Status from 'nx:status.svelte';
const layouts = [
	/*LAYOUTS*/
];
const routes = [
	/*ROUTES*/
];
export const base = (document.querySelector('meta[name="nx-base"]')?.content || '').replace(/\/$/, '');
export const nav = $state({ comps: [], datas: [], url: new URL(location.href), route: { id: null }, params: {}, data: {}, status: 200, error: null, state: {} });
const comps = new Map();
async function comp(loader) {
	if (!loader) return Passthrough;
	let p = comps.get(loader);
	if (!p) comps.set(loader, (p = loader().then((m) => m.default)));
	return p;
}
const listeners = { before: new Set(), after: new Set() };
export const hooks = listeners;
let seq = 0;
async function show(url, kind = 'goto') {
	const mine = ++seq;
	const path = url.pathname.slice(base.length) || '/';
	for (const r of routes) {
		const m = path.match(r.re);
		if (!m) continue;
		const params = Object.fromEntries(r.params.map((p, i) => [p, m[i + 1] === undefined ? '' : decodeURIComponent(m[i + 1])]));
		try {
			const chain = r.layouts.map((i) => layouts[i]);
			const found = await Promise.all([...chain.map((l) => comp(l.component)), comp(r.page)]);
			const datas = [];
			let merged = {};
			const loads = [...chain.map((l) => l.load), r.load];
			for (const loader of loads) {
				if (loader) {
					const mod = await loader();
					if (typeof mod.load === 'function') {
						const parentData = merged;
						const out = await mod.load({ params, url, route: { id: r.id }, fetch: (...a) => fetch(...a), parent: async () => parentData, depends() {}, untrack: (f) => f(), setHeaders() {}, data: null });
						merged = { ...merged, ...(out ?? {}) };
					}
				}
				datas.push(merged);
			}
			if (mine !== seq) return;
			Object.assign(nav, { comps: found, datas, url, route: { id: r.id }, params, data: merged, status: 200, error: null });
		} catch (e) {
			if (mine !== seq) return;
			console.error(e);
			const msg = (e && e.message) || String(e);
			Object.assign(nav, { comps: [Status], datas: [{ status: 500, message: msg }], url, route: { id: r.id }, params, data: {}, status: 500, error: { message: msg } });
		}
		for (const f of listeners.after) f({ from: null, to: { url, route: { id: r.id }, params }, type: kind });
		return;
	}
	if (mine !== seq) return;
	const root = layouts.length && routes.some((r) => r.layouts[0] === 0) ? [await comp(layouts[0].component)] : [];
	Object.assign(nav, { comps: [...root, Status], datas: [...root.map(() => ({})), { status: 404, message: 'Not found: ' + path }], url, route: { id: null }, params: {}, data: {}, status: 404, error: { message: 'Not found' } });
}
export async function goto(href, opts = {}) {
	const u = new URL(href, location.href);
	for (const f of listeners.before) { let stop = false; f({ to: { url: u }, cancel: () => (stop = true), type: 'goto' }); if (stop) return; }
	if (opts.replaceState) history.replaceState(opts.state ?? {}, '', u); else history.pushState(opts.state ?? {}, '', u);
	nav.state = opts.state ?? {};
	return show(u);
}
export function reload() { return show(new URL(location.href)); }
addEventListener('popstate', (e) => { nav.state = e.state ?? {}; show(new URL(location.href), 'popstate'); });
addEventListener('click', (e) => {
	const a = e.target.closest?.('a[href]');
	if (!a || e.defaultPrevented || e.button || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
	if (a.target && a.target !== '_self') return;
	if (a.hasAttribute('download') || a.getAttribute('rel')?.includes('external') || a.dataset.sveltekitReload !== undefined) return;
	const u = new URL(a.href, location.href);
	if (u.origin !== location.origin || !u.pathname.startsWith(base + '/') && u.pathname !== base) return;
	if (u.pathname === location.pathname && u.search === location.search && u.hash) return;
	e.preventDefault();
	goto(u);
});
show(new URL(location.href), 'enter');
`

const appStateJS = `import { nav } from 'nx:router';
export const page = {
	get url() { return nav.url; }, get route() { return nav.route; }, get params() { return nav.params; },
	get data() { return nav.data; }, get status() { return nav.status; }, get error() { return nav.error; },
	get state() { return nav.state; }, form: null
};
export const navigating = { from: null, to: null, type: null, complete: null };
export const updated = { current: false, check: async () => false };
`

const appStoresJS = `import { readable } from 'svelte/store';
import { page as live } from '$app/state';
import { nav } from 'nx:router';
const snap = () => ({ url: live.url, route: live.route, params: live.params, data: live.data, status: live.status, error: live.error, state: live.state, form: null });
export const page = readable(snap(), (set) => { const id = setInterval(() => set(snap()), 100); return () => clearInterval(id); });
export const navigating = readable(null);
export const updated = { ...readable(false), check: async () => false };
export function getStores() { return { page, navigating, updated }; }
void nav;
`

const appPathsJS = `import { base } from 'nx:router';
export { base };
export const assets = base;
export function resolve(path, params = {}) {
	return base + path.replace(/\[\[?(?:\.\.\.)?(\w+)\]?\]/g, (_, k) => params[k] ?? '').replace(/\/+$/, '') || base + '/';
}
export const resolveRoute = resolve;
`

const appNavigationJS = `import { goto as go, reload, hooks } from 'nx:router';
export const goto = go;
export const invalidate = () => reload();
export const invalidateAll = () => reload();
export const refreshAll = () => reload();
export const preloadData = async () => ({ type: 'loaded', status: 200, data: {} });
export const preloadCode = async () => {};
export function beforeNavigate(f) { hooks.before.add(f); }
export function afterNavigate(f) { hooks.after.add(f); }
export function onNavigate() {}
export const disableScrollHandling = () => {};
export function pushState(url, state) { history.pushState(state, '', url); }
export function replaceState(url, state) { history.replaceState(state, '', url); }
`

// html is the app's index.html: its own src/app.html with SvelteKit's
// placeholders filled, or naut's.
func (a *appSource) html(css bool) string {
	head := `<meta name="nx-base" content="">`
	if css {
		head += "\n\t\t" + `<link rel="stylesheet" href="/app.css">`
	}
	head += "\n\t\t" + `<script type="module" src="/app.js"></script>`
	body := `<div id="nx-app" style="display: contents"></div>`
	page := a.appHTML
	if page == "" {
		page = "<!doctype html>\n<html lang=\"en\">\n\t<head>\n\t\t<meta charset=\"utf-8\" />\n\t\t<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\" />\n\t\t<title>Operator screen</title>\n\t\t%sveltekit.head%\n\t</head>\n\t<body>\n\t\t%sveltekit.body%\n\t</body>\n</html>\n"
	}
	r := strings.NewReplacer("%sveltekit.head%", head, "%sveltekit.body%", body, "%sveltekit.assets%", "", "%sveltekit.nonce%", "", "%sveltekit.env.PUBLIC_", "%")
	return r.Replace(page)
}

// copyStatic adds static/ as it is, from the app root, as SvelteKit copies
// it. A built file of the same name wins.
func (a *appSource) copyStatic(files map[string][]byte) error {
	if a.noStatic {
		return nil
	}
	dir := filepath.Join(a.dir, "static")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil
	}
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if _, ok := files[rel]; ok {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[rel] = b
		return nil
	})
}
