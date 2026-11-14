package hmibuild

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/internal/mimicdoc"
)

// The kit's own equipment (hmi Mimic.svelte's builtin map): every other
// component a mimic names is the project's, a {Name}.svelte in it.
var builtinEquipment = map[string]bool{
	"Tank": true, "Heater": true, "Pump": true, "Valve": true, "Gauge": true,
	"Sparkline": true, "CommandButton": true, "WriteToggle": true, "OffPage": true,
}

// skipDir is what a project search never looks in: dot, dependency and
// build directories, and a directory that is a project of its own (its
// own nautilus.yaml).
func skipDir(dir string) bool {
	name := filepath.Base(dir)
	if strings.HasPrefix(name, ".") || name == "node_modules" || name == "build" || name == "dist" {
		return true
	}
	_, err := os.Stat(filepath.Join(dir, "nautilus.yaml"))
	return err == nil
}

// Mimics lists the project's *.mimic.json, slash paths from the project
// root, sorted.
func Mimics(project string) []string {
	var out []string
	_ = filepath.WalkDir(project, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != project && skipDir(p) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".mimic.json") {
			rel, _ := filepath.Rel(project, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

type mimicDoc struct {
	Name      string `json:"name"`
	Equipment []struct {
		Component string   `json:"component"`
		Width     *float64 `json:"width"`
		Height    *float64 `json:"height"`
	} `json:"equipment"`
}

// galleryComp is one of the project's components on the components page:
// its source, and the box the first screen to use it gives it.
type galleryComp struct {
	name, src     string
	width, height *float64
}

// defaultApp is the app for a project with mimics and no app of its own:
// a page per mimic (the first at "/"), live and operable, under a bar
// with the project's name, the pages, the connection and the theme.
func defaultApp(project, title string) (*appSource, error) {
	mimics := Mimics(project)
	if len(mimics) == 0 {
		return nil, &Error{Messages: []string{"nothing to build: the project has no hmi/ app (src/routes) and no *.mimic.json"}}
	}
	sources := componentSources(project)
	a := &appSource{dir: project, noStatic: true, virtual: map[string]vmod{}}
	a.layouts = []layout{{Component: "nx:default/layout.svelte"}}
	var screens []string
	var problems []string
	used := map[string]bool{}
	takenSlug := map[string]bool{}
	for i, m := range mimics {
		raw, err := os.ReadFile(filepath.Join(project, filepath.FromSlash(m)))
		if err != nil {
			return nil, err
		}
		// units drawn out, so the components a unit repeats are registered too
		if x, _, err := mimicdoc.ExpandUnits(raw); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", m, err))
			continue
		} else {
			raw = x
		}
		var doc mimicDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			problems = append(problems, fmt.Sprintf("%s: not a mimic: %v", m, err))
			continue
		}
		var imports, registry []string
		seen := map[string]bool{}
		for _, eq := range doc.Equipment {
			c := eq.Component
			if c == "" || builtinEquipment[c] || seen[c] {
				continue
			}
			seen[c] = true
			src, err := pickSource(sources[c], filepath.Dir(filepath.Join(project, filepath.FromSlash(m))), project)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: component %s: %v", m, c, err))
				continue
			}
			id := fmt.Sprintf("C%d", len(imports))
			imports = append(imports, fmt.Sprintf("\timport %s from %s;", id, jsq(src)))
			registry = append(registry, fmt.Sprintf("%s: %s", jsq(c), id))
			used[c] = true
		}
		// the first mimic is "/", the others by their file name
		id := "/"
		if i > 0 {
			slug := slugOf(m)
			if slug == "" || takenSlug[slug] {
				slug = fmt.Sprintf("%s-%d", slug, i)
			}
			takenSlug[slug] = true
			id = "/" + strings.TrimPrefix(slug, "-")
		}
		label := doc.Name
		if label == "" {
			label = strings.TrimSuffix(filepath.Base(m), ".mimic.json")
		}
		screens = append(screens, fmt.Sprintf("{ id: %s, href: %s, label: %s }", jsq(id), jsq(id), jsq(label)))
		pageMod := fmt.Sprintf("nx:default/page%d.svelte", i)
		a.virtual[pageMod] = vmod{kind: component, text: fmt.Sprintf(defaultPage,
			jsq(filepath.Join(project, filepath.FromSlash(m))), strings.Join(imports, "\n"), strings.Join(registry, ", "))}
		r, _ := routeFor(strings.TrimPrefix(id, "/"))
		r.Page = pageMod
		r.Layouts = []int{0}
		a.routes = append(a.routes, r)
	}
	if len(problems) > 0 {
		return nil, &Error{Messages: problems}
	}
	if !takenSlug["components"] {
		a.addComponentsPage(project, []int{0})
	}
	if title == "" {
		title = filepath.Base(project)
	}
	a.generate()
	a.virtual["nx:default/layout.svelte"] = vmod{kind: component, text: fmt.Sprintf(defaultLayout, jsq(title), strings.Join(screens, ", "))}
	a.virtual["nx:default/client"] = vmod{kind: plain, text: defaultClient}
	a.appHTML = strings.Replace(defaultHTML, "%title%", htmlEscape(title), 1)
	return a, nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugOf(mimic string) string {
	s := strings.ToLower(strings.TrimSuffix(filepath.Base(mimic), ".mimic.json"))
	return strings.Trim(slugRe.ReplaceAllString(s, "-"), "-")
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// componentSources maps a component's name to every {Name}.svelte in the
// project.
func componentSources(project string) map[string][]string {
	out := map[string][]string{}
	_ = filepath.WalkDir(project, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != project && skipDir(p) {
				return filepath.SkipDir
			}
			return nil
		}
		if n := d.Name(); strings.HasSuffix(n, ".svelte") && !strings.HasPrefix(n, "+") {
			name := strings.TrimSuffix(n, ".svelte")
			out[name] = append(out[name], p)
		}
		return nil
	})
	return out
}

// pickSource chooses the {Name}.svelte a mimic means: the one beside the
// mimic, else the only one in the project.
func pickSource(paths []string, mimicDir, project string) (string, error) {
	switch len(paths) {
	case 0:
		return "", fmt.Errorf("no such .svelte in the project (the kit's own are %s)", strings.Join(builtinNames(), ", "))
	case 1:
		return paths[0], nil
	}
	for _, p := range paths {
		if filepath.Dir(p) == mimicDir {
			return p, nil
		}
	}
	var rels []string
	for _, p := range paths {
		r, _ := filepath.Rel(project, p)
		rels = append(rels, filepath.ToSlash(r))
	}
	return "", fmt.Errorf("more than one in the project (%s); keep one, or put the one meant beside the mimic", strings.Join(rels, ", "))
}

func builtinNames() []string {
	var out []string
	for n := range builtinEquipment {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

const defaultClient = `import { createRealtimeClient } from '@joyautomation/nautilus-hmi';
// One realtime client for the app: the layout starts it, every page reads it.
export const rt = createRealtimeClient({ url: '/api/stream' });
`

const defaultLayout = `<script>
	import '@joyautomation/nautilus-hmi/theme.css';
	import '@joyautomation/nautilus-hmi/fonts.css';
	import { onMount } from 'svelte';
	import { theme, ConfirmDialog, StatusPill, ThemeSwitch } from '@joyautomation/nautilus-hmi';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { rt } from 'nx:default/client';
	let { children } = $props();
	onMount(() => {
		theme.init();
		rt.start();
		return () => rt.stop();
	});
	const title = %s;
	const screens = [%s];
</script>

<ConfirmDialog />
<div class="nx-shell">
	<header>
		<b>{title}</b>
		{#if screens.length > 1}
			<nav aria-label="Screens">
				{#each screens as s (s.id)}
					<a href={resolve(s.href) + page.url.search} aria-current={page.route.id === s.id ? 'page' : undefined}>{s.label}</a>
				{/each}
			</nav>
		{/if}
		<span class="gap"></span>
		<StatusPill kind={rt.connected ? 'good' : 'critical'} label={rt.connected ? 'Connected' : 'Offline'} />
		<ThemeSwitch />
	</header>
	<main>{@render children()}</main>
</div>

<style>
	:global(html, body) { margin: 0; height: 100%%; background: var(--bg); color: var(--ink); font-family: var(--font); }
	.nx-shell { display: flex; flex-direction: column; height: 100vh; height: 100dvh; }
	header { display: flex; align-items: center; gap: 12px; padding: 8px 16px; border-bottom: 1px solid color-mix(in srgb, var(--ink) 14%%, transparent); }
	b { font-family: var(--font-display, var(--font)); font-size: var(--font-md); }
	nav { display: flex; gap: 4px; }
	nav a { color: var(--muted); text-decoration: none; padding: 4px 10px; border-radius: var(--radius-pill, 999px); }
	nav a[aria-current='page'] { color: var(--ink); background: color-mix(in srgb, var(--ink) 10%%, transparent); }
	.gap { flex: 1; }
	main { flex: 1; min-height: 0; overflow: auto; padding: 16px; }
</style>
`

const defaultPage = `<script>
	import { Mimic } from '@joyautomation/nautilus-hmi';
	import doc from %s;
%s
	import { rt } from 'nx:default/client';
	const tags = $derived(rt.frame?.tags ?? {});
	const write = (name, value) => rt.writeTag(name, value);
</script>

<Mimic {doc} {tags} registry={{ %s }} {write} fit="contain" />
`

const defaultHTML = `<!doctype html>
<html lang="en">
	<head>
		<meta charset="utf-8" />
		<meta name="viewport" content="width=device-width, initial-scale=1" />
		<link rel="icon" href="data:," />
		<title>%title%</title>
		<script>
			try { var t = localStorage.getItem('theme'); if (t === 'system') t = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'; document.documentElement.dataset.theme = t === 'light' ? 'light' : 'dark'; } catch (e) { document.documentElement.dataset.theme = 'dark'; }
		</script>
		%sveltekit.head%
	</head>
	<body>
		%sveltekit.body%
	</body>
</html>
`

// projectGallery is every component of the project's own that a screen
// shows: each a mimic places (with the box the first to place it gives
// it), then each with a {Name}.component.json, as an app's are.
func projectGallery(project string) []*galleryComp {
	sources := componentSources(project)
	var out []*galleryComp
	at := map[string]*galleryComp{}
	for _, m := range Mimics(project) {
		raw, err := os.ReadFile(filepath.Join(project, filepath.FromSlash(m)))
		if err != nil {
			continue
		}
		if x, _, err := mimicdoc.ExpandUnits(raw); err == nil {
			raw = x
		}
		var doc mimicDoc
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		for _, eq := range doc.Equipment {
			c := eq.Component
			if c == "" || builtinEquipment[c] {
				continue
			}
			if g := at[c]; g != nil {
				if g.width == nil && eq.Width != nil {
					g.width = eq.Width
				}
				if g.height == nil && eq.Height != nil {
					g.height = eq.Height
				}
				continue
			}
			src, err := pickSource(sources[c], filepath.Dir(filepath.Join(project, filepath.FromSlash(m))), project)
			if err != nil {
				continue
			}
			g := &galleryComp{name: c, src: src, width: eq.Width, height: eq.Height}
			at[c] = g
			out = append(out, g)
		}
	}
	sidecars := sidecarFiles(project)
	var declared []string
	for name := range sidecars {
		if at[name] == nil && !builtinEquipment[name] && len(sources[name]) > 0 {
			declared = append(declared, name)
		}
	}
	sort.Strings(declared)
	for _, name := range declared {
		src := sources[name][0]
		for _, p := range sources[name][1:] {
			if len(p) < len(src) {
				src = p
			}
		}
		out = append(out, &galleryComp{name: name, src: src})
	}
	return out
}

// addComponentsPage adds ComponentsRoute to the app, under the layouts
// given, when the project has components of its own to show and the app
// has no page there already.
func (a *appSource) addComponentsPage(project string, layouts []int) {
	for _, r := range a.routes {
		if r.ID == ComponentsRoute {
			return
		}
	}
	gallery := projectGallery(project)
	if len(gallery) == 0 {
		return
	}
	const pageMod = "nx:components.svelte"
	if a.virtual == nil {
		a.virtual = map[string]vmod{}
	}
	a.virtual[pageMod] = vmod{kind: component, text: componentsPage(gallery, sidecarFiles(project))}
	r, _ := routeFor(ComponentsRoute[1:])
	r.Page = pageMod
	r.Layouts = layouts
	a.routes = append(a.routes, r)
	a.generate()
}

// ComponentsRoute is where naut's own screens show the project's
// components, each in each of the examples its {Name}.component.json
// gives (`"examples": {"open": {...props}, "closed": {...}}`).
const ComponentsRoute = "/components"

// componentsPage is the Svelte page for ComponentsRoute.
func componentsPage(gallery []*galleryComp, sidecars map[string]json.RawMessage) string {
	var imports, entries []string
	for i, g := range gallery {
		imports = append(imports, fmt.Sprintf("\timport C%d from %s;", i, jsq(g.src)))
		// only what the page draws from: the examples, or a preview
		sc := json.RawMessage("null")
		var all map[string]json.RawMessage
		if json.Unmarshal(sidecars[g.name], &all) == nil {
			keep := map[string]json.RawMessage{}
			for _, k := range []string{"examples", "preview"} {
				if v, ok := all[k]; ok {
					keep[k] = v
				}
			}
			if b, err := json.Marshal(keep); err == nil {
				sc = b
			}
		}
		num := func(f *float64) string {
			if f == nil {
				return "undefined"
			}
			return fmt.Sprint(*f)
		}
		entries = append(entries, fmt.Sprintf("\t\t{ name: %s, C: C%d, width: %s, height: %s, sidecar: %s }",
			jsq(g.name), i, num(g.width), num(g.height), sc))
	}
	return fmt.Sprintf(componentsPageText, strings.Join(imports, "\n"), strings.Join(entries, ",\n"))
}

const componentsPageText = `<script>
%s
	// Each component's examples: its sidecar's "examples" (name -> props),
	// else its "preview" (flat, or {props}), else none.
	const comps = [
%s
	];
	const examples = (sc) => {
		if (sc && sc.examples && typeof sc.examples === 'object' && Object.keys(sc.examples).length > 0) return Object.entries(sc.examples);
		if (sc && sc.preview && typeof sc.preview === 'object') return [['preview', sc.preview.props && typeof sc.preview.props === 'object' ? sc.preview.props : sc.preview]];
		return [];
	};
	const write = async () => 'The components page writes nothing';
</script>

<div class="nx-components">
	<h1>Components</h1>
	<p class="nx-lede">Each of the project's own components, in each of the examples its <code>{'{Name}'}.component.json</code> gives.</p>
	{#each comps as c (c.name)}
		{@const C = c.C}
		{@const ex = examples(c.sidecar)}
		<section aria-label={c.name}>
			<h2>{c.name}</h2>
			{#if ex.length === 0}
				<p class="nx-none">No examples: add <code>"examples": {"{"} "running": {"{"} ... props {"}"} {"}"}</code> to {c.name}.component.json. Drawn with no props.</p>
			{/if}
			<div class="nx-row">
				{#each ex.length > 0 ? ex : [['no props', {}]] as [label, props] (label)}
					<figure>
						<div class="nx-box" style:width={c.width ? c.width + 'px' : undefined} style:height={c.height ? c.height + 'px' : undefined}>
							<C look="isa101" {...(c.width ? { width: c.width } : {})} {...(c.height ? { height: c.height } : {})} {...props} {write} />
						</div>
						<figcaption>{label}</figcaption>
					</figure>
				{/each}
			</div>
		</section>
	{/each}
</div>

<style>
	.nx-components { display: grid; gap: 28px; max-width: 1800px; }
	h1 { margin: 0; font-size: var(--font-lg); }
	h2 { margin: 0 0 10px; font-size: var(--font-md); }
	.nx-lede, .nx-none { margin: 0; color: var(--muted); }
	.nx-none { margin-bottom: 10px; }
	code { font-family: var(--mono); }
	.nx-row { display: flex; flex-wrap: wrap; gap: 20px; align-items: flex-start; }
	figure { margin: 0; display: grid; gap: 8px; justify-items: start; }
	.nx-box { position: relative; }
	figcaption { color: var(--ink-2); font-size: var(--font-sm); }
</style>
`
